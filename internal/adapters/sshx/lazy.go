package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// DefaultDialBackoff is how long Lazy refuses to dial again after a failed
// attempt, when Config.DialBackoff is unset. The poller runs every 30 seconds
// and the user can press «Проверить» as often as they like; neither should be
// able to turn an unreachable peer into a stream of connection attempts.
const DefaultDialBackoff = 2 * time.Second

// ErrClosed means the client has been closed and will not connect again.
var ErrClosed = errors.New("sshx: the ssh client is closed")

// Lazy is an SSH client that connects when it is first needed and reconnects
// when the connection dies. It is what the app is wired with.
//
// Dial connects eagerly, which is wrong for this program: constructing the app
// with a *Client fails at startup whenever the peer is unreachable, and a
// monitor that will not start when the link is down is of no use to anybody -
// that is the situation it exists to report on. Lazy is built without touching
// the network, and every call that needs a session opens one at that moment.
//
// It satisfies the three interfaces the app declares - app.RemoteProbe,
// app.RemoteShell and app.Forwarder - so the app never sees a concrete type.
//
// All methods are safe for concurrent use. Several callers arriving at once
// open one connection between them, not one each.
type Lazy struct {
	cfg Config

	// gate is a mutex that a context can give up waiting for: it is held across
	// the dial, so an ordinary sync.Mutex would make a caller in a hurry wait
	// out somebody else's connection attempt. Fail fast beats stall.
	gate chan struct{}

	// mu guards everything below and is never held across I/O.
	mu       sync.Mutex
	sess     *session
	lastErr  error
	failedAt time.Time
	closed   bool
}

// session is one connection together with the signal that it has died.
type session struct {
	client *Client
	// dead is closed when the transport is gone. It comes from the library's
	// own Wait, so liveness is a fact reported by the connection rather than
	// something guessed from the text of an error - which matters twice over
	// here, because these machines localise their error messages.
	dead chan struct{}
}

// NewLazy builds a client that has not connected to anything yet. It never
// fails: a bad configuration is reported by the first call that needs it.
func NewLazy(cfg Config) *Lazy {
	return &Lazy{cfg: cfg.withDefaults(), gate: make(chan struct{}, 1)}
}

// TCPReachable opens an ordinary TCP connection from this machine.
//
// It deliberately needs no session, and keeps working when SSH is down. That is
// the point of it: it is what separates "the port is closed" from "a local
// filter refused us", and losing that answer exactly when the link breaks would
// be the worst possible moment to lose it.
func (l *Lazy) TCPReachable(ctx context.Context, addr string, port int) error {
	return TCPReachable(ctx, addr, port)
}

// ServiceRunning asks the peer about one of its Windows services.
func (l *Lazy) ServiceRunning(ctx context.Context, name string) (bool, error) {
	return withSession(ctx, l, func(c *Client) (bool, error) {
		return c.ServiceRunning(ctx, name)
	})
}

// PeerCanReachUs asks the peer to open a connection back to this machine.
func (l *Lazy) PeerCanReachUs(ctx context.Context, localAddr string, port int) error {
	_, err := withSession(ctx, l, func(c *Client) (struct{}, error) {
		return struct{}{}, c.PeerCanReachUs(ctx, localAddr, port)
	})
	return err
}

// runResult carries what Run answers, so it can travel through withSession.
type runResult struct {
	stdout   string
	stderr   string
	exitCode int
}

// RunPowerShell runs a script on the peer. A command that exits non-zero is
// reported through exitCode with a nil error, exactly as *Client does, and is
// never mistaken for a broken connection.
func (l *Lazy) RunPowerShell(ctx context.Context, script string) (
	stdout, stderr string, exitCode int, err error,
) {
	res, err := withSession(ctx, l, func(c *Client) (runResult, error) {
		out, errOut, code, runErr := c.RunPowerShell(ctx, script)
		return runResult{stdout: out, stderr: errOut, exitCode: code}, runErr
	})
	if err != nil {
		return "", "", ExitUnknown, err
	}
	return res.stdout, res.stderr, res.exitCode, nil
}

// LocalForward opens an ssh -L style tunnel over the current session.
//
// What happens to a live tunnel when the connection drops: it stops working,
// and it does not heal. A forward is bound to the connection that created it,
// so when that transport dies the tunnel's own goroutines end, connections to
// the local port are accepted and immediately dropped, and a later reconnect
// does not adopt it - the caller must open it again. This is stated plainly
// rather than papered over, because a tunnel that looks alive and carries
// nothing is worse than one that is visibly gone.
//
// What does NOT happen is a reconnect killing a working tunnel: Lazy only ever
// drops a session whose transport has already died, and closes a live
// connection in Close alone.
//
// ctx governs the tunnel's lifetime, not merely its setup - cancelling it shuts
// the tunnel down, which is the contract *Client.LocalForward documents. Pass
// the process lifetime here, as app.Forwards does with its base context, never
// the context of the request that asked for the tunnel.
func (l *Lazy) LocalForward(ctx context.Context, localPort int, remoteHost string, remotePort int) (
	io.Closer, error,
) {
	return withSession(ctx, l, func(c *Client) (io.Closer, error) {
		return c.LocalForward(ctx, localPort, remoteHost, remotePort)
	})
}

// Close shuts the connection down if one is open and stops the client
// reconnecting. Closing twice, or closing one that never connected, is not an
// error.
func (l *Lazy) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.closed = true
	sess := l.sess
	l.sess = nil
	if sess == nil {
		return nil
	}
	if err := sess.client.Close(); err != nil {
		return fmt.Errorf("closing the connection to %s: %w", l.cfg.hostPort(), err)
	}
	return nil
}

// withSession runs fn against a live session, dialing first if there is none.
//
// If fn fails and the transport turns out to be gone, the session is dropped,
// one fresh connection is made and fn runs exactly once more. Nothing else is
// retried: a command that failed on a working connection failed for a reason of
// its own, and repeating it would only be slower and no more true.
func withSession[T any](ctx context.Context, l *Lazy, fn func(*Client) (T, error)) (T, error) {
	var zero T

	sess, err := l.acquire(ctx)
	if err != nil {
		return zero, err
	}

	out, err := fn(sess.client)
	switch {
	case err == nil:
		return out, nil
	case ctx.Err() != nil:
		// The caller gave up. That is not the connection's fault and redialing
		// would only make the wait longer.
		return zero, err
	case alive(sess):
		// A real answer from a working session.
		return zero, err
	}

	l.drop(sess)
	fresh, dialErr := l.acquire(ctx)
	if dialErr != nil {
		return zero, fmt.Errorf("reconnecting to %s after the session dropped: %w",
			l.cfg.hostPort(), dialErr)
	}
	return fn(fresh.client)
}

// acquire returns a live session, connecting if there is not one already.
func (l *Lazy) acquire(ctx context.Context) (*session, error) {
	if sess, err := l.cached(); sess != nil || err != nil {
		return sess, err
	}

	// Only one caller dials; the rest wait here and then find the connection
	// already made. Waiting is cancellable so nobody is stuck behind a dial
	// they no longer care about.
	if err := l.enter(ctx); err != nil {
		return nil, fmt.Errorf("waiting to connect to %s: %w", l.cfg.hostPort(), err)
	}
	defer l.leave()

	if sess, err := l.cached(); sess != nil || err != nil {
		return sess, err
	}
	return l.dial(ctx)
}

// cached reports the live session, or nil when one has to be dialed. A session
// whose transport has died is dropped here, which is what makes the *next* call
// after a drop reconnect rather than fail.
func (l *Lazy) cached() (*session, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil, ErrClosed
	}
	if l.sess == nil {
		return nil, nil
	}
	if alive(l.sess) {
		return l.sess, nil
	}
	// Already gone; this only releases what the library still holds.
	_ = l.sess.client.Close()
	l.sess = nil
	return nil, nil
}

// dial opens one connection, unless a recent failure says not to try yet.
func (l *Lazy) dial(ctx context.Context) (*session, error) {
	l.mu.Lock()
	if l.lastErr != nil && time.Since(l.failedAt) < l.cfg.DialBackoff {
		err := l.lastErr
		l.mu.Unlock()
		return nil, err
	}
	l.mu.Unlock()

	client, err := Dial(ctx, l.cfg)

	l.mu.Lock()
	defer l.mu.Unlock()

	if err != nil {
		l.lastErr, l.failedAt = err, time.Now()
		return nil, err
	}
	l.lastErr = nil
	if l.closed {
		// Close ran while we were dialing; do not leave a socket behind.
		_ = client.Close()
		return nil, ErrClosed
	}
	l.sess = newSession(client)
	return l.sess, nil
}

// drop forgets sess, but only if it is still the current one - a concurrent
// caller may already have replaced it, and closing its connection would then
// break a session that works.
func (l *Lazy) drop(sess *session) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.sess != sess {
		return
	}
	l.sess = nil
	_ = sess.client.Close() // it is already dead; this releases the socket
}

// enter takes the dial gate, giving up if ctx does.
func (l *Lazy) enter(ctx context.Context) error {
	// Checked first on purpose: with both cases of the select ready, Go picks
	// one at random, and a caller who has already given up must never be the
	// one that starts a connection.
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case l.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// leave releases the dial gate.
func (l *Lazy) leave() { <-l.gate }

// newSession wraps a connection and starts watching for its death.
func newSession(client *Client) *session {
	sess := &session{client: client, dead: make(chan struct{})}
	go func() {
		// The error says why the connection ended; that it ended is all we need,
		// and the reason reaches the caller through the failing call itself.
		_ = client.conn.Wait()
		close(sess.dead)
	}()
	return sess
}

// alive reports whether the transport is still up, without blocking.
//
// There is a narrow window in which a call fails microseconds before the
// library reports the death, and this returns true. The consequence is that the
// call reports its error and the *next* call reconnects, instead of retrying
// within this one. That is the safe way round: no stall, no wrong answer, and
// one poll cycle later at worst.
func alive(sess *session) bool {
	select {
	case <-sess.dead:
		return false
	default:
		return true
	}
}
