package diagnose

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// laptop and workPC are the two real machines this program was written for.
var (
	laptop = core.Machine{
		WindowsName: "DESKTOP-L9DJSE9",
		TailnetName: "workspace-claude-pc",
		Addr:        "100.124.47.73",
		User:        "pnj",
	}
	workPC = core.Machine{
		WindowsName: "WIN-STTM11D02RD",
		TailnetName: "win-sttm11d02rd",
		Addr:        "100.127.188.87",
		User:        "user",
	}
)

// fakeProbe answers from canned values and records what was asked of it. It
// touches nothing: no network, no filesystem, no process.
type fakeProbe struct {
	up       bool
	note     string
	upErr    error
	presence core.Presence
	// presenceErr is the daemon refusing to say. The engine then falls back to
	// the ping, which is what it did before it could ask.
	presenceErr       error
	latency           time.Duration
	peerErr           error
	tcpErr            error
	serviceUp         bool
	serviceErr        error
	localServiceUp    bool
	localServiceErr   error
	localInstalled    bool
	localInstalledErr error
	dialBackErr       error

	calls []string
}

var _ core.Probe = (*fakeProbe)(nil)

func (f *fakeProbe) TailscaleUp(_ context.Context) (bool, string, error) {
	f.calls = append(f.calls, "TailscaleUp")
	return f.up, f.note, f.upErr
}

func (f *fakeProbe) PeerReachable(_ context.Context, addr string) (time.Duration, error) {
	f.calls = append(f.calls, "PeerReachable:"+addr)
	return f.latency, f.peerErr
}

func (f *fakeProbe) PeerPresence(_ context.Context, peer core.Machine) (core.Presence, error) {
	f.calls = append(f.calls, "PeerPresence:"+peer.TailnetName)
	return f.presence, f.presenceErr
}

func (f *fakeProbe) TCPReachable(_ context.Context, addr string, port int) error {
	f.calls = append(f.calls, fmt.Sprintf("TCPReachable:%s:%d", addr, port))
	return f.tcpErr
}

func (f *fakeProbe) ServiceRunning(_ context.Context, name string) (bool, error) {
	f.calls = append(f.calls, "ServiceRunning:"+name)
	return f.serviceUp, f.serviceErr
}

func (f *fakeProbe) LocalServiceRunning(_ context.Context, name string) (bool, error) {
	f.calls = append(f.calls, "LocalServiceRunning:"+name)
	return f.localServiceUp, f.localServiceErr
}

func (f *fakeProbe) LocalServiceInstalled(_ context.Context, name string) (bool, error) {
	f.calls = append(f.calls, "LocalServiceInstalled:"+name)
	return f.localInstalled, f.localInstalledErr
}

func (f *fakeProbe) PeerCanReachUs(_ context.Context, localAddr string, port int) error {
	f.calls = append(f.calls, fmt.Sprintf("PeerCanReachUs:%s:%d", localAddr, port))
	return f.dialBackErr
}

// healthy is the probe of a link with nothing wrong with it — including an
// OpenSSH server on this side, which is what makes the inbound row provable,
// and a peer the tailnet reports as connected.
func healthy() fakeProbe {
	return fakeProbe{
		up:             true,
		note:           "v1.102.3 · 3 узла",
		presence:       core.PresenceOnline,
		latency:        12 * time.Millisecond,
		serviceUp:      true,
		localServiceUp: true,
		localInstalled: true,
	}
}

// The calls the fake records for the two questions asked by name, spelled once
// so a scenario's expectations read as a sequence rather than as formatting.
var (
	presence = "PeerPresence:" + workPC.TailnetName
	dialBack = fmt.Sprintf("PeerCanReachUs:%s:22", laptop.Addr)
)

type scenario struct {
	name      string
	probe     fakeProbe
	states    []core.State // in the order ports.go declares the checks
	overall   core.State
	summary   string
	detail    string   // exact match when set
	detailHas []string // substrings that must appear
	fixes     []FixID
	latency   time.Duration
	calls     []string
}

func TestRun(t *testing.T) {
	t.Parallel()

	blocked := &core.BlockedError{Addr: workPC.Addr, Port: sshPort}
	dialTimeout := &core.TimeoutError{Addr: workPC.Addr, Port: sshPort}
	pingTimeout := &core.TimeoutError{Addr: workPC.Addr, Port: 0}
	keyRefusal := refusedByPeer()

	tests := []scenario{
		{
			name:    "healthy link",
			probe:   healthy(),
			states:  []core.State{core.StateOK, core.StateOK, core.StateOK, core.StateOK, core.StateOK},
			overall: core.StateOK,
			summary: "Связь установлена",
			detail:  "Обе машины видят друг друга",
			latency: 12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
				dialBack,
			},
		},
		{
			name:  "tailscale is off",
			probe: fakeProbe{up: false},
			states: []core.State{
				core.StateFail, core.StateUnknown, core.StateUnknown,
				core.StateUnknown, core.StateUnknown,
			},
			overall: core.StateFail,
			summary: "Связи нет",
			detail:  "Tailscale отключён",
			fixes:   []FixID{FixStartTailscale},
			calls:   []string{"TailscaleUp"},
		},
		{
			name:  "tailscale probe cannot answer",
			probe: fakeProbe{upErr: errors.New("ipn: connection refused")},
			states: []core.State{
				core.StateUnknown, core.StateUnknown, core.StateUnknown,
				core.StateUnknown, core.StateUnknown,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"Tailscale"},
			calls:     []string{"TailscaleUp"},
		},
		{
			name: "local packet filter refuses the socket",
			probe: func() fakeProbe {
				p := healthy()
				p.tcpErr = blocked
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateUnknown, core.StateFail,
			},
			overall:   core.StateFail,
			summary:   "Трафик блокируется",
			detailHas: []string{"AmneziaVPN", "100.64.0.0/10"},
			fixes:     []FixID{FixDisableKillSwitch, FixSplitTunnelSSH},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"LocalServiceRunning:sshd",
			},
		},
		{
			name: "peer is up but sshd is not running",
			probe: func() fakeProbe {
				p := healthy()
				p.tcpErr = dialTimeout
				p.serviceUp = false
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateWarn, core.StateUnknown,
				core.StateWarn, core.StateOK,
			},
			overall: core.StateWarn,
			summary: "Частичная связь",
			detail:  "Рабочий ПК не принимает подключения",
			fixes:   []FixID{FixStartSSHD},
			latency: 12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
			},
		},
		{
			name: "sshd runs but the port stays silent",
			probe: func() fakeProbe {
				p := healthy()
				p.tcpErr = dialTimeout
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateWarn, core.StateUnknown,
				core.StateWarn, core.StateOK,
			},
			overall:   core.StateWarn,
			summary:   "Частичная связь",
			detailHas: []string{"брандмауэр"},
			latency:   12 * time.Millisecond,
		},
		{
			name: "port answers while the service reports stopped",
			probe: func() fakeProbe {
				p := healthy()
				p.serviceUp = false
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateOK,
				core.StateWarn, core.StateOK,
			},
			overall:   core.StateWarn,
			summary:   "Частичная связь",
			detailHas: []string{"sshd"},
			latency:   12 * time.Millisecond,
		},
		{
			name: "tcp probe fails with an untyped transport error",
			probe: func() fakeProbe {
				p := healthy()
				p.tcpErr = errors.New("wsarecv: an existing connection was closed")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateUnknown, core.StateUnknown,
				core.StateUnknown, core.StateUnknown,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"не завершилась"},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
			},
		},
		{
			// A peer that does not answer stops the two rows that need a login
			// to it and nothing else: the kill-switch row is a fact about this
			// machine, and the short local dial still answers it.
			name: "peer does not answer in the tunnel",
			probe: func() fakeProbe {
				p := healthy()
				p.latency = 0
				p.peerErr = pingTimeout
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateFail,
				core.StateUnknown, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Связи нет",
			detailHas: []string{"не отвечает"},
			fixes:     []FixID{FixCheckPeerOnline},
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				tcpProbe,
			},
		},
		{
			name: "tunnel probe cannot answer",
			probe: func() fakeProbe {
				p := healthy()
				p.latency = 0
				p.peerErr = errors.New("tailscaled: no route")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateUnknown, core.StateUnknown,
				core.StateUnknown, core.StateUnknown,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"не завершилась"},
		},
		{
			name: "service probe cannot answer",
			probe: func() fakeProbe {
				p := healthy()
				p.serviceErr = errors.New("ssh: handshake failed")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateOK,
				core.StateUnknown, core.StateOK,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"sshd"},
			latency:   12 * time.Millisecond,
		},
		{
			// The case these machines actually lived through: outbound worked
			// for hours while this laptop had no OpenSSH Server at all.
			name: "outbound works but we have no ssh server of our own",
			probe: func() fakeProbe {
				p := healthy()
				p.localServiceUp, p.localInstalled = false, false
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateFail,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Частичная связь",
			detailHas: []string{"нет вовсе"},
			fixes:     []FixID{FixInstallSSHServer},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
				"LocalServiceInstalled:sshd",
			},
		},
		{
			// The other half of the same row, and the reason it is two rows'
			// worth of wording: the component is there and the service is
			// stopped, which is a start and not an install.
			name: "our own ssh server is installed but stopped",
			probe: func() fakeProbe {
				p := healthy()
				p.localServiceUp = false
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateFail,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Частичная связь",
			detailHas: []string{"установлен, но не запущен"},
			fixes:     []FixID{FixStartLocalSSHD},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
				"LocalServiceInstalled:sshd",
			},
		},
		{
			// And the case where the service manager will not say which of the
			// two it is: the direction is still proven broken, so the row is a
			// failure with the weaker sentence and the smaller action.
			name: "our own server is down and we cannot tell whether it exists",
			probe: func() fakeProbe {
				p := healthy()
				p.localServiceUp = false
				p.localInstalledErr = errors.New("OpenSCManager: access denied")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateFail,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Частичная связь",
			detailHas: []string{"не запущен свой SSH-сервер"},
			fixes:     []FixID{FixStartLocalSSHD},
			latency:   12 * time.Millisecond,
		},
		{
			// What the work PC really printed, and printed as "unknown": the
			// tunnel is up, port 22 answers, and the laptop declines the key
			// because it is not in administrators_authorized_keys.
			name: "the peer answers, speaks ssh and refuses our key",
			probe: func() fakeProbe {
				p := healthy()
				p.serviceErr = keyRefusal
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Ключ не принят",
			detailHas: []string{"ключ", workPC.TailnetName},
			fixes:     []FixID{FixAuthorizeKey},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
			},
		},
		{
			// The same refusal arriving one probe later, when the session that
			// answered about sshd has died and the redial is declined.
			name: "the key is refused when the dial-back needs a session",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = keyRefusal
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Ключ не принят",
			detailHas: []string{"ключ"},
			fixes:     []FixID{FixAuthorizeKey},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
				dialBack,
			},
		},
		{
			name: "the port stays silent and the key is refused as well",
			probe: func() fakeProbe {
				p := healthy()
				p.tcpErr = dialTimeout
				p.serviceErr = keyRefusal
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateFail, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Ключ не принят",
			detailHas: []string{"ключ"},
			fixes:     []FixID{FixAuthorizeKey},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the peer cannot dial back: a filter refuses it",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = &core.BlockedError{Addr: laptop.Addr, Port: sshPort}
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateFail,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateFail,
			summary:   "Частичная связь",
			detailHas: []string{"обратно"},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the peer cannot dial back: nothing answers",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = &core.TimeoutError{Addr: laptop.Addr, Port: sshPort}
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateWarn,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateWarn,
			summary:   "Частичная связь",
			detailHas: []string{"брандмауэр"},
			latency:   12 * time.Millisecond,
		},
		{
			name: "the dial-back probe cannot answer",
			probe: func() fakeProbe {
				p := healthy()
				p.dialBackErr = errors.New("ssh: session channel closed")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"Обратное подключение"},
			latency:   12 * time.Millisecond,
		},
		{
			name: "our own service probe cannot answer",
			probe: func() fakeProbe {
				p := healthy()
				p.localServiceErr = errors.New("OpenSCManager: access denied")
				return p
			}(),
			states: []core.State{
				core.StateOK, core.StateOK, core.StateUnknown,
				core.StateOK, core.StateOK,
			},
			overall:   core.StateUnknown,
			summary:   "Состояние неизвестно",
			detailHas: []string{"своего SSH-сервера"},
			latency:   12 * time.Millisecond,
			calls: []string{
				"TailscaleUp",
				presence,
				"PeerReachable:" + workPC.Addr,
				fmt.Sprintf("TCPReachable:%s:22", workPC.Addr),
				"ServiceRunning:sshd",
				"LocalServiceRunning:sshd",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			runScenario(t, tt)
		})
	}
}

func runScenario(t *testing.T, tt scenario) {
	t.Helper()

	probe := tt.probe
	snap, fixes := Run(context.Background(), &probe, laptop, workPC)

	assertInvariants(t, snap)
	assertStates(t, snap, tt.states)

	if snap.Overall != tt.overall {
		t.Errorf("Overall = %v, want %v", snap.Overall, tt.overall)
	}
	if snap.Summary != tt.summary {
		t.Errorf("Summary = %q, want %q", snap.Summary, tt.summary)
	}
	if tt.detail != "" && snap.Detail != tt.detail {
		t.Errorf("Detail = %q, want %q", snap.Detail, tt.detail)
	}
	for _, want := range tt.detailHas {
		if !strings.Contains(snap.Detail, want) {
			t.Errorf("Detail = %q, want it to mention %q", snap.Detail, want)
		}
	}
	if snap.Latency != tt.latency {
		t.Errorf("Latency = %v, want %v", snap.Latency, tt.latency)
	}
	assertFixes(t, fixes, tt.fixes)
	if tt.calls != nil {
		assertCalls(t, probe.calls, tt.calls)
	}
}

// assertInvariants holds for every snapshot the engine can produce.
func assertInvariants(t *testing.T, snap core.Snapshot) {
	t.Helper()

	if len(snap.Checks) != len(checkOrder) {
		t.Fatalf("got %d checks, want all %d", len(snap.Checks), len(checkOrder))
	}
	worst := core.StateOK
	for i, want := range checkOrder {
		got := snap.Checks[i]
		if got.ID != want {
			t.Errorf("Checks[%d].ID = %q, want %q", i, got.ID, want)
		}
		if got.Label == "" {
			t.Errorf("Checks[%d] (%s) has no label", i, got.ID)
		}
		if got.Note == "" {
			t.Errorf("Checks[%d] (%s) has no note", i, got.ID)
		}
		if got.State > worst {
			worst = got.State
		}
	}
	if snap.Overall != worst {
		t.Errorf("Overall = %v, want the worst check state %v", snap.Overall, worst)
	}
	if snap.Taken.IsZero() {
		t.Error("Taken is zero; the snapshot should be stamped")
	}
	if snap.Summary == "" || snap.Detail == "" {
		t.Errorf("Summary/Detail must never be empty, got %q / %q", snap.Summary, snap.Detail)
	}
}

func assertStates(t *testing.T, snap core.Snapshot, want []core.State) {
	t.Helper()

	if len(want) != len(checkOrder) {
		t.Fatalf("the scenario lists %d states, want %d", len(want), len(checkOrder))
	}
	for i, w := range want {
		if snap.Checks[i].State != w {
			t.Errorf("%s = %v, want %v (note %q)",
				snap.Checks[i].ID, snap.Checks[i].State, w, snap.Checks[i].Note)
		}
	}
}

func assertFixes(t *testing.T, got []Fix, want []FixID) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("got %d fixes (%v), want %d (%v)", len(got), fixIDs(got), len(want), want)
	}
	for i, w := range want {
		if got[i].ID != w {
			t.Errorf("fix[%d] = %q, want %q", i, got[i].ID, w)
		}
		if got[i].Title == "" || got[i].Explanation == "" {
			t.Errorf("fix %q must carry a title and an explanation", got[i].ID)
		}
	}
}

func assertCalls(t *testing.T, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("probe calls = %v, want %v", got, want)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("probe call %d = %q, want %q", i, got[i], w)
		}
	}
}

func fixIDs(fixes []Fix) []FixID {
	ids := make([]FixID, 0, len(fixes))
	for _, f := range fixes {
		ids = append(ids, f.ID)
	}
	return ids
}
