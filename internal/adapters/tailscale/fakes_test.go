package tailscale

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

// The two real machines this program watches, per CLAUDE.md.
const (
	laptopName = "workspace-claude-pc"
	laptopHost = "DESKTOP-L9DJSE9"
	laptopAddr = "100.124.47.73"

	workName = "win-sttm11d02rd"
	workHost = "WIN-STTM11D02RD"
	workAddr = "100.127.188.87"

	tailnetSuffix = ".tail1a2b.ts.net."
	longVersion   = "1.102.3-t01234abcd-g0123456789"
)

var errBoom = errors.New("daemon socket closed")

// pushRecord is one PushFile call, contents included so a test can assert what
// was streamed. Nothing here is ever logged.
type pushRecord struct {
	target tailcfg.StableNodeID
	size   int64
	name   string
	body   string
}

// fakeDaemon stands in for the Tailscale LocalAPI. Every field is optional;
// the zero value answers every call with an empty result.
type fakeDaemon struct {
	mu sync.Mutex

	status    *ipnstate.Status
	statusErr error
	statusN   int

	pingFn func(n int, ip netip.Addr, t tailcfg.PingType) (*ipnstate.PingResult, error)
	pingN  int

	targets    []apitype.FileTarget
	targetsErr error
	pushErr    error
	pushed     []pushRecord

	waiting    []apitype.WaitingFile
	waitingErr error
	getFn      func(name string) (io.ReadCloser, int64, error)
	deleteErr  error
	deleted    []string
}

func (f *fakeDaemon) Status(ctx context.Context) (*ipnstate.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statusN++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.status, f.statusErr
}

func (f *fakeDaemon) Ping(ctx context.Context, ip netip.Addr,
	t tailcfg.PingType,
) (*ipnstate.PingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pingN++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.pingFn == nil {
		return &ipnstate.PingResult{IP: ip.String(), LatencySeconds: 0.012}, nil
	}
	return f.pingFn(f.pingN, ip, t)
}

func (f *fakeDaemon) FileTargets(ctx context.Context) ([]apitype.FileTarget, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.targets, f.targetsErr
}

func (f *fakeDaemon) PushFile(ctx context.Context, target tailcfg.StableNodeID,
	size int64, name string, r io.Reader,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	body, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pushErr != nil {
		return f.pushErr
	}
	f.pushed = append(f.pushed, pushRecord{target: target, size: size, name: name, body: string(body)})
	return nil
}

func (f *fakeDaemon) WaitingFiles(ctx context.Context) ([]apitype.WaitingFile, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.waiting, f.waitingErr
}

func (f *fakeDaemon) GetWaitingFile(ctx context.Context, name string) (io.ReadCloser, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if f.getFn != nil {
		return f.getFn(name)
	}
	body := "contents of " + name
	return io.NopCloser(strings.NewReader(body)), int64(len(body)), nil
}

func (f *fakeDaemon) DeleteWaitingFile(ctx context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeDaemon) records() []pushRecord {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]pushRecord(nil), f.pushed...)
}

func (f *fakeDaemon) deletedNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.deleted...)
}

// fakeRunner stands in for the tailscale CLI.
type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func(args []string) (CommandResult, error)
}

func (r *fakeRunner) Run(ctx context.Context, args ...string) (CommandResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	fn := r.fn
	r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if fn == nil {
		return CommandResult{}, nil
	}
	return fn(args)
}

func (r *fakeRunner) lastCall() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

// runningStatus is the tailnet as it really looks: this laptop plus the work
// PC, both online, on 1.102.3.
func runningStatus(t *testing.T) *ipnstate.Status {
	t.Helper()
	return &ipnstate.Status{
		Version:      longVersion,
		BackendState: "Running",
		Self: &ipnstate.PeerStatus{
			ID:           "nSELF",
			DNSName:      laptopName + tailnetSuffix,
			HostName:     laptopHost,
			TailscaleIPs: []netip.Addr{netip.MustParseAddr(laptopAddr)},
		},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			nodeKey(t): {
				ID:           "nWORK",
				DNSName:      workName + tailnetSuffix,
				HostName:     workHost,
				TailscaleIPs: []netip.Addr{netip.MustParseAddr(workAddr)},
				Online:       true,
			},
		},
	}
}

func nodeKey(t *testing.T) key.NodePublic {
	t.Helper()
	return key.NewNode().Public()
}

// workTarget is the work PC as Taildrop sees it.
func workTarget() apitype.FileTarget {
	return apitype.FileTarget{
		Node: &tailcfg.Node{
			StableID:     "nWORK",
			Name:         workName + tailnetSuffix,
			ComputedName: workName,
			Addresses:    []netip.Prefix{netip.MustParsePrefix(workAddr + "/32")},
		},
		PeerAPIURL: "http://" + workAddr + ":1234",
	}
}

// newTestClient builds a Client with both sides faked and pings that do not
// sleep, so tests stay fast.
func newTestClient(d Daemon, r Runner) *Client {
	return New(WithDaemon(d), WithRunner(r), WithPingPolicy(3, 0))
}
