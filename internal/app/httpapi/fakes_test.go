package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/core/diagnose"
)

// errBoom is the generic "the service below said no" of these tests.
var errBoom = errors.New("boom")

// sampleResult is one plausible diagnosis, with the real machines in it.
func sampleResult() app.Result {
	taken := time.Date(2026, 9, 7, 10, 30, 0, 0, time.UTC)
	return app.Result{
		Snapshot: core.Snapshot{
			Taken:   taken,
			Overall: core.StateWarn,
			Summary: "Частичная связь",
			Detail:  "Отсюда подключиться можно, а к нам — нет",
			Latency: 17 * time.Millisecond,
			// The peer answers; what is broken on this run is our own server. A
			// realistic value here is what makes the wire field meaningful.
			Peer: core.PeerStateAnswers,
			Checks: []core.Check{
				{ID: core.CheckTailscale, State: core.StateOK, Label: "Tailscale", Note: "v1.102.3 · 2 узла"},
				{
					ID: core.CheckSSHIn, State: core.StateFail,
					Label: "SSH: win-sttm11d02rd → workspace-claude-pc", Note: "SSH-сервер не запущен",
				},
			},
		},
		Fixes: []diagnose.Fix{{
			ID: diagnose.FixStartLocalSSHD, Title: "Поднять SSH-сервер на этой машине",
			Explanation: "Запущу службу sshd.", NeedsAdmin: true,
		}},
	}
}

// fakeStatus stands in for the poller.
type fakeStatus struct {
	mu sync.Mutex

	latest    app.Result
	hasLatest bool
	checking  bool
	checkErr  error
	checkRes  app.Result

	updates chan app.Result
	subs    int
	unsubs  int
}

func newFakeStatus() *fakeStatus {
	return &fakeStatus{updates: make(chan app.Result)}
}

func (f *fakeStatus) Latest() (app.Result, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.latest, f.hasLatest
}

func (f *fakeStatus) Check(ctx context.Context) (app.Result, error) {
	if err := ctx.Err(); err != nil {
		return app.Result{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.checkRes, f.checkErr
}

func (f *fakeStatus) Subscribe() (<-chan app.Result, func()) {
	f.mu.Lock()
	f.subs++
	f.mu.Unlock()

	return f.updates, func() {
		f.mu.Lock()
		defer f.mu.Unlock()

		f.unsubs++
	}
}

func (f *fakeStatus) Checking() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.checking
}

func (f *fakeStatus) unsubscribed() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.unsubs
}

// fakeFixes stands in for the fix executor.
type fakeFixes struct {
	out    app.FixOutcome
	lastID diagnose.FixID
}

func (f *fakeFixes) Apply(_ context.Context, id diagnose.FixID) app.FixOutcome {
	f.lastID = id
	return f.out
}

// fakeHistory stands in for the uptime ring.
type fakeHistory struct{ points []app.Point }

func (f *fakeHistory) Points() []app.Point { return f.points }

// fakePeers stands in for the tailnet listing.
type fakePeers struct {
	peers []app.Peer
	err   error
}

func (f *fakePeers) List(context.Context) ([]app.Peer, error) { return f.peers, f.err }

// fakeFiles stands in for the transfer service.
type fakeFiles struct {
	sendErr    error
	received   []string
	receiveErr error
	items      []app.Transfer

	sentPath string
	sentPeer string
}

func (f *fakeFiles) Send(_ context.Context, path, peer string) error {
	f.sentPath, f.sentPeer = path, peer
	return f.sendErr
}

func (f *fakeFiles) Receive(context.Context) ([]string, error) { return f.received, f.receiveErr }
func (f *fakeFiles) List() []app.Transfer                      { return f.items }

// fakeForwards stands in for the tunnel registry.
type fakeForwards struct {
	started  app.Forward
	startErr error
	stopErr  error
	items    []app.Forward
	url      string
	serveErr error

	lastArgs [3]int
	lastHost string
	stoppedI string
	servePor int
}

func (f *fakeForwards) Start(_ context.Context, localPort int, host string, remotePort int) (
	app.Forward, error,
) {
	f.lastArgs = [3]int{localPort, remotePort, 0}
	f.lastHost = host
	return f.started, f.startErr
}

func (f *fakeForwards) Stop(id string) error {
	f.stoppedI = id
	return f.stopErr
}

func (f *fakeForwards) List() []app.Forward { return f.items }

func (f *fakeForwards) Serve(_ context.Context, port int) (string, error) {
	f.servePor = port
	return f.url, f.serveErr
}

// do sends one request to the handler and returns the recorder.
func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()

	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}
