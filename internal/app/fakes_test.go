package app

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// errBoom is the generic "the outside world said no" of these tests.
var errBoom = errors.New("boom")

// fakeTailnet answers the two Tailscale questions from canned values.
type fakeTailnet struct {
	mu sync.Mutex

	up      bool
	note    string
	upErr   error
	latency time.Duration
	pingErr error

	presence    core.Presence
	presenceErr error
	// lastPeer is the machine the presence question was asked about, so a test
	// can prove the composite passed the configured peer through whole rather
	// than an address of its own making.
	lastPeer core.Machine

	// lastAddr is the address of the most recent ping, so a test can prove the
	// composite passed the peer's address through rather than one of its own.
	lastAddr string
}

func (f *fakeTailnet) TailscaleUp(ctx context.Context) (bool, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return false, "", err
	}
	return f.up, f.note, f.upErr
}

func (f *fakeTailnet) PeerReachable(_ context.Context, addr string) (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastAddr = addr
	return f.latency, f.pingErr
}

func (f *fakeTailnet) PeerPresence(_ context.Context, peer core.Machine) (core.Presence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastPeer = peer
	return f.presence, f.presenceErr
}

// fakeRemote answers the three questions the SSH adapter answers.
type fakeRemote struct {
	mu sync.Mutex

	tcpErr      error
	running     bool
	runningErr  error
	dialBackErr error

	lastService string
	lastPort    int
	lastAddr    string
}

func (f *fakeRemote) TCPReachable(_ context.Context, addr string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastAddr, f.lastPort = addr, port
	return f.tcpErr
}

func (f *fakeRemote) ServiceRunning(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastService = name
	return f.running, f.runningErr
}

func (f *fakeRemote) PeerCanReachUs(_ context.Context, localAddr string, port int) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastAddr, f.lastPort = localAddr, port
	return f.dialBackErr
}

// fakeLocalProbe answers about services on this machine.
type fakeLocalProbe struct {
	mu sync.Mutex

	running      bool
	err          error
	installed    bool
	installedErr error
	last         string
}

func (f *fakeLocalProbe) LocalServiceRunning(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.last = name
	return f.running, f.err
}

func (f *fakeLocalProbe) LocalServiceInstalled(_ context.Context, name string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.last = name
	return f.installed, f.installedErr
}

// fakeServiceControl stands in for the Windows service manager.
type fakeServiceControl struct {
	mu sync.Mutex

	err     error
	started []string
}

func (f *fakeServiceControl) StartService(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.started = append(f.started, name)
	return f.err
}

func (f *fakeServiceControl) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.started...)
}

// fakeShell stands in for a PowerShell session on the peer.
type fakeShell struct {
	mu sync.Mutex

	stdout string
	stderr string
	code   int
	err    error
	script string
}

func (f *fakeShell) RunPowerShell(_ context.Context, script string) (string, string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.script = script
	return f.stdout, f.stderr, f.code, f.err
}

// fakeMover stands in for Taildrop.
type fakeMover struct {
	mu sync.Mutex

	sendErr    error
	received   []string
	receiveErr error

	sentPath string
	sentPeer string
	inbox    string
	// block, when set, holds SendFile until it is closed, so a test can look at
	// a transfer while it is still in flight.
	block chan struct{}
}

func (f *fakeMover) SendFile(_ context.Context, path, peer string) error {
	f.mu.Lock()
	f.sentPath, f.sentPeer = path, peer
	block, err := f.block, f.sendErr
	f.mu.Unlock()

	if block != nil {
		<-block
	}
	return err
}

func (f *fakeMover) ReceiveFiles(_ context.Context, dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.inbox = dir
	return f.received, f.receiveErr
}

// fakeCloser is a stopped-or-not port forward that can report an address.
type fakeCloser struct {
	mu       sync.Mutex
	addr     net.Addr
	closeErr error
	closed   int
}

func (c *fakeCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed++
	return c.closeErr
}

func (c *fakeCloser) closes() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.closed
}

// addrCloser adds the Addr method the registry looks for.
type addrCloser struct{ *fakeCloser }

func (c addrCloser) Addr() net.Addr { return c.addr }

// fakeForwarder hands out fake tunnels.
type fakeForwarder struct {
	mu sync.Mutex

	err      error
	withAddr bool
	opened   []*fakeCloser
	lastArgs [3]int
	lastHost string
}

func (f *fakeForwarder) LocalForward(_ context.Context, localPort int, host string, remotePort int) (
	io.Closer, error,
) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.lastArgs = [3]int{localPort, remotePort, len(f.opened)}
	f.lastHost = host
	if f.err != nil {
		return nil, f.err
	}
	c := &fakeCloser{addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40000 + len(f.opened)}}
	f.opened = append(f.opened, c)
	if f.withAddr {
		return addrCloser{c}, nil
	}
	return c, nil
}

// fakePublisher stands in for tailscale serve.
type fakePublisher struct {
	url      string
	err      error
	resetErr error
	lastPort int
	resets   int
}

func (f *fakePublisher) Serve(_ context.Context, port int) (string, error) {
	f.lastPort = port
	return f.url, f.err
}

func (f *fakePublisher) ServeReset(context.Context) error {
	f.resets++
	return f.resetErr
}

// fakePeerLister stands in for the tailnet status.
type fakePeerLister struct {
	peers []tailscale.Peer
	err   error
}

func (f *fakePeerLister) Peers(context.Context) ([]tailscale.Peer, error) {
	return f.peers, f.err
}
