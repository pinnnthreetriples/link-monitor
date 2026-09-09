package sshx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Forward is a running local port forward - the equivalent of ssh -L. It is the
// concrete type LocalForward returns; callers that only need to stop it can
// hold it as an io.Closer.
type Forward struct {
	listener net.Listener
	client   *Client
	remote   string

	done      chan struct{}
	closeOnce sync.Once
	closeErr  error

	// wg counts the accept loop and every connection pump, so Close can wait
	// for all of them and leave no goroutine behind.
	wg sync.WaitGroup

	// mu guards conns, the set of sockets Close has to break out of a blocking
	// read. A pump removes itself when it finishes.
	mu    sync.Mutex
	conns map[io.Closer]struct{}
}

// LocalForward listens on 127.0.0.1:localPort and forwards every connection to
// remoteHost:remotePort through the SSH transport, the way ssh -L does.
//
// It binds the loopback only: a forward that answered on the LAN would hand the
// peer's port to anyone on the network. localPort may be 0, in which case the
// operating system picks one and Addr reports it.
//
// One failed connection does not stop the forward: if the peer refuses a
// channel, or a pump dies, that connection is dropped and the listener keeps
// accepting. The forward stops when the returned Closer is closed or when ctx
// is cancelled, whichever happens first; both paths wait for every goroutine to
// finish, so a test can assert there is nothing left running.
//
// Payload bytes are copied, never inspected, logged or wrapped into an error.
func (c *Client) LocalForward(ctx context.Context, localPort int, remoteHost string, remotePort int) (
	io.Closer, error,
) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("starting a local forward: %w", err)
	}
	if remoteHost == "" {
		return nil, errors.New("sshx: LocalForward needs a remote host")
	}
	if !validPort(localPort, true) || !validPort(remotePort, false) {
		return nil, fmt.Errorf("sshx: LocalForward got an out-of-range port pair %d -> %d",
			localPort, remotePort)
	}

	// The bind goes through a ListenConfig so that ctx governs it too. That is
	// the smaller half of the story: a listener returned by ListenConfig is not
	// tied to the context that created it, and cancelling ctx does not close
	// it. What actually shuts this forward down when ctx ends is the watcher
	// goroutine below, which is why both exist.
	local := net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", local)
	if err != nil {
		return nil, fmt.Errorf("listening on %s for a forward to %s:%d: %w",
			local, remoteHost, remotePort, err)
	}

	f := &Forward{
		listener: listener,
		client:   c,
		remote:   net.JoinHostPort(remoteHost, strconv.Itoa(remotePort)),
		done:     make(chan struct{}),
		conns:    make(map[io.Closer]struct{}),
	}

	f.wg.Add(1)
	go f.accept()

	// This watcher is deliberately outside wg: it calls Close, which waits on
	// wg, so counting it there would deadlock. It always ends, because Close
	// closes done.
	go func() {
		select {
		case <-ctx.Done():
			_ = f.Close() // the caller is no longer listening for this error
		case <-f.done:
		}
	}()

	return f, nil
}

// validPort accepts 1-65535, and also 0 when the port is the local one, where 0
// means "let the OS choose".
func validPort(p int, allowZero bool) bool {
	if p == 0 {
		return allowZero
	}
	return p > 0 && p <= 65535
}

// Addr reports the address the forward actually listens on, which is how a
// caller that passed port 0 learns the port it got.
func (f *Forward) Addr() net.Addr { return f.listener.Addr() }

// Close stops the forward and waits for every goroutine it started. It is safe
// to call from several goroutines and more than once; every caller blocks until
// the shutdown is complete and then sees the same result.
func (f *Forward) Close() error {
	f.closeOnce.Do(func() {
		close(f.done)
		if err := f.listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			f.closeErr = fmt.Errorf("closing the forward listener on %s: %w", f.listener.Addr(), err)
		}
		f.breakConns()
		f.wg.Wait()
	})
	return f.closeErr
}

// breakConns closes every socket a pump might be blocked reading from.
func (f *Forward) breakConns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.conns {
		_ = c.Close() // best effort: we are tearing down and about to drop the set
	}
	f.conns = make(map[io.Closer]struct{})
}

// track registers c so Close can break it, reporting false if the forward is
// already shutting down - in which case the caller must close c itself.
func (f *Forward) track(c io.Closer) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	select {
	case <-f.done:
		return false
	default:
	}
	f.conns[c] = struct{}{}
	return true
}

// untrack forgets c once its pump has finished.
func (f *Forward) untrack(c io.Closer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.conns, c)
}

// acceptRetryDelay keeps a listener that keeps failing from spinning the CPU.
// It is short enough to be invisible to a user retrying by hand.
const acceptRetryDelay = 20 * time.Millisecond

// accept runs the listener loop until Close breaks it.
func (f *Forward) accept() {
	defer f.wg.Done()
	timer := time.NewTimer(acceptRetryDelay)
	defer timer.Stop()

	for {
		local, err := f.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			// Any other Accept error is about this one attempt, not the
			// listener, so keep serving - after a pause, so a listener that
			// fails every time cannot spin.
			timer.Reset(acceptRetryDelay)
			select {
			case <-f.done:
				return
			case <-timer.C:
			}
			continue
		}

		// Adding here is safe while Close waits: this goroutine is itself
		// counted in wg, so the counter cannot have reached zero.
		f.wg.Add(1)
		go f.serve(local)
	}
}

// serve opens the SSH channel for one accepted connection and pumps it. A
// failure here drops this connection only.
func (f *Forward) serve(local net.Conn) {
	defer f.wg.Done()

	if !f.track(local) {
		_ = local.Close() // shutting down already; nothing will read from it
		return
	}
	defer func() {
		f.untrack(local)
		_ = local.Close() // the pump is done with it either way
	}()

	remote, err := f.client.conn.Dial("tcp", f.remote)
	if err != nil {
		// Dropping this connection is the documented behaviour: the listener
		// stays up so the next attempt can succeed.
		return
	}
	if !f.track(remote) {
		_ = remote.Close() // shutting down already
		return
	}
	defer func() {
		f.untrack(remote)
		_ = remote.Close()
	}()

	pump(local, remote)
}

// pump copies in both directions until either side ends, then unblocks the
// other by closing both sockets. Errors are discarded on purpose: a forwarded
// stream ends by being closed, and the bytes must never reach a log.
func pump(local, remote net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		_, _ = io.Copy(remote, local) // payload; a copy error just ends the stream
		closeWrite(remote)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, remote) // payload; a copy error just ends the stream
		closeWrite(local)
	}()

	wg.Wait()
}

// closeWrite half-closes c when it can, so the peer sees EOF instead of a
// reset. Not every conn supports it; those are closed by serve's defers.
func closeWrite(c net.Conn) {
	type writeCloser interface{ CloseWrite() error }
	if wc, ok := c.(writeCloser); ok {
		_ = wc.CloseWrite() // best effort: a failure here only costs a clean EOF
	}
}
