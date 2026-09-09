package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"sync"
)

// maxPort is the highest TCP port there is.
const maxPort = 65535

var (
	// ErrNoSSHSession means there is no SSH transport to forward through.
	ErrNoSSHSession = errors.New("app: no SSH session to forward through")

	// ErrNoServe means the tailscale serve side was never wired up.
	ErrNoServe = errors.New("app: tailscale serve is not available")

	// ErrForwardNotFound means no live forward goes by that id.
	ErrForwardNotFound = errors.New("app: no such forward")
)

// Forwarder opens an ssh -L style tunnel. *sshx.Client implements it, and the
// io.Closer it returns is what stops the tunnel.
type Forwarder interface {
	LocalForward(ctx context.Context, localPort int, remoteHost string, remotePort int) (io.Closer, error)
}

// Publisher exposes a local port to the tailnet. *tailscale.Client implements it.
type Publisher interface {
	Serve(ctx context.Context, port int) (string, error)
	ServeReset(ctx context.Context) error
}

// Forward is one live tunnel, as the UI sees it.
type Forward struct {
	ID         string
	LocalPort  int
	RemoteHost string
	RemotePort int
	// Addr is what the tunnel actually listens on, which is how a caller that
	// asked for port 0 learns the port it got.
	Addr string
}

// Forwards is the registry of live tunnels. Everything it opens it can close,
// and CloseAll at shutdown leaves nothing listening.
type Forwards struct {
	// base is the process lifetime. A forward must outlive the HTTP request
	// that started it, so it is opened against this context and not that one —
	// which is why a context is stored here rather than passed in each time.
	base context.Context
	fwd  Forwarder
	pub  Publisher

	mu   sync.Mutex
	live map[string]*liveForward
	seq  int
}

// liveForward pairs the description with the thing that stops it. seq is the
// order it was opened in, which is the order the UI lists them in.
type liveForward struct {
	seq    int
	desc   Forward
	closer io.Closer
}

// NewForwards builds the registry. ctx is the process lifetime: when it ends,
// every forward opened here stops too. Either dependency may be nil, in which
// case the calls that needed it report why.
func NewForwards(ctx context.Context, fwd Forwarder, pub Publisher) *Forwards {
	return &Forwards{base: ctx, fwd: fwd, pub: pub, live: make(map[string]*liveForward)}
}

// Start opens a tunnel from 127.0.0.1:localPort to remoteHost:remotePort.
//
// reqCtx bounds the setup only — a cancelled request must not take a running
// tunnel with it. The tunnel itself lives until Stop, CloseAll, or the end of
// the lifetime context given to NewForwards.
func (f *Forwards) Start(reqCtx context.Context, localPort int, remoteHost string, remotePort int) (
	Forward, error,
) {
	if err := reqCtx.Err(); err != nil {
		return Forward{}, fmt.Errorf("opening a forward: %w", err)
	}
	if f.fwd == nil {
		return Forward{}, fmt.Errorf("opening a forward to %s:%d: %w", remoteHost, remotePort, ErrNoSSHSession)
	}
	if err := validForwardPorts(localPort, remoteHost, remotePort); err != nil {
		return Forward{}, err
	}

	closer, err := f.fwd.LocalForward(f.base, localPort, remoteHost, remotePort)
	if err != nil {
		return Forward{}, fmt.Errorf("opening a forward to %s:%d: %w", remoteHost, remotePort, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.seq++
	desc := Forward{
		ID:         "fwd-" + strconv.Itoa(f.seq),
		LocalPort:  localPort,
		RemoteHost: remoteHost,
		RemotePort: remotePort,
		Addr:       listenAddr(closer, localPort),
	}
	f.live[desc.ID] = &liveForward{seq: f.seq, desc: desc, closer: closer}
	return desc, nil
}

// Stop closes one tunnel and forgets it. Stopping an unknown id is an error
// wrapping ErrForwardNotFound, never a silent success.
func (f *Forwards) Stop(id string) error {
	f.mu.Lock()
	entry, ok := f.live[id]
	delete(f.live, id)
	f.mu.Unlock()

	if !ok {
		return fmt.Errorf("stopping the forward %q: %w", id, ErrForwardNotFound)
	}
	if err := entry.closer.Close(); err != nil {
		return fmt.Errorf("stopping the forward %q: %w", id, err)
	}
	return nil
}

// List returns the live tunnels, oldest first so the order does not jump.
func (f *Forwards) List() []Forward {
	f.mu.Lock()
	defer f.mu.Unlock()

	entries := make([]*liveForward, 0, len(f.live))
	for _, entry := range f.live {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].seq < entries[j].seq })

	out := make([]Forward, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.desc)
	}
	return out
}

// CloseAll stops every tunnel and reports every failure together, so one
// stubborn listener does not hide the rest.
func (f *Forwards) CloseAll() error {
	f.mu.Lock()
	entries := make([]*liveForward, 0, len(f.live))
	for id, entry := range f.live {
		entries = append(entries, entry)
		delete(f.live, id)
	}
	f.mu.Unlock()

	var errs []error
	for _, entry := range entries {
		if err := entry.closer.Close(); err != nil {
			errs = append(errs, fmt.Errorf("stopping the forward %q: %w", entry.desc.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Serve publishes a local port to the tailnet over HTTPS and returns the URL
// peers can open. Unlike a forward this is the daemon's job, not ours: it
// survives us, and ServeReset is what withdraws it.
func (f *Forwards) Serve(ctx context.Context, port int) (string, error) {
	if f.pub == nil {
		return "", fmt.Errorf("publishing port %d to the tailnet: %w", port, ErrNoServe)
	}
	if port < 1 || port > maxPort {
		return "", fmt.Errorf("app: port %d is outside 1-%d", port, maxPort)
	}
	url, err := f.pub.Serve(ctx, port)
	if err != nil {
		return "", fmt.Errorf("publishing port %d to the tailnet: %w", port, err)
	}
	return url, nil
}

// ServeReset withdraws everything this machine publishes to the tailnet.
func (f *Forwards) ServeReset(ctx context.Context) error {
	if f.pub == nil {
		return fmt.Errorf("withdrawing what is published to the tailnet: %w", ErrNoServe)
	}
	if err := f.pub.ServeReset(ctx); err != nil {
		return fmt.Errorf("withdrawing what is published to the tailnet: %w", err)
	}
	return nil
}

// validForwardPorts rejects what the adapter would only reject later.
func validForwardPorts(localPort int, remoteHost string, remotePort int) error {
	if remoteHost == "" {
		return errors.New("app: a forward needs a remote host")
	}
	if localPort < 0 || localPort > maxPort {
		return fmt.Errorf("app: local port %d is outside 0-%d", localPort, maxPort)
	}
	if remotePort < 1 || remotePort > maxPort {
		return fmt.Errorf("app: remote port %d is outside 1-%d", remotePort, maxPort)
	}
	return nil
}

// listenAddr asks the forward what it actually bound to. A forward that cannot
// say falls back to the port the caller asked for, which is right for every
// case except port 0 — and a forward that cannot report its address could not
// have been given 0 in the first place.
func listenAddr(closer io.Closer, localPort int) string {
	if a, ok := closer.(interface{ Addr() net.Addr }); ok && a.Addr() != nil {
		return a.Addr().String()
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
}
