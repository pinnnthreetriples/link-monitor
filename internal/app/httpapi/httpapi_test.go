package httpapi

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// decode reads a JSON response body into dst.
func decode(t *testing.T, rec *httptest.ResponseRecorder, dst any) {
	t.Helper()

	if err := json.Unmarshal(rec.Body.Bytes(), dst); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body.String(), err)
	}
}

// isRussian reports whether s contains Cyrillic, which is what every message
// this API returns has to be.
func isRussian(s string) bool {
	for _, r := range s {
		if r >= 'а' && r <= 'я' || r >= 'А' && r <= 'Я' || r == 'ё' || r == 'Ё' {
			return true
		}
	}
	return false
}

func TestStatusBeforeTheFirstProbe(t *testing.T) {
	t.Parallel()

	h := New(Deps{Status: newFakeStatus()})
	rec := do(t, h, http.MethodGet, "/api/status", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	var got Status
	decode(t, rec, &got)
	if got.Overall != core.StateUnknown.String() {
		t.Errorf("Overall = %q", got.Overall)
	}
	if !isRussian(got.Summary) || !isRussian(got.Detail) {
		t.Errorf("summary/detail are not Russian: %q / %q", got.Summary, got.Detail)
	}
	if got.Checks == nil || got.Fixes == nil {
		t.Error("checks and fixes must be empty arrays, never null")
	}
	// Empty, not "unknown". Nothing has been diagnosed, so there is no finding
	// about the peer at all — which is what tells the machine card that the
	// tailnet list is all the evidence there is.
	if got.PeerState != "" {
		t.Errorf("PeerState = %q before the first probe, want it empty", got.PeerState)
	}
}

// TestStatusCarriesTheDiagnosisOwnWordAboutThePeer is the wire half of the
// defect a user found by switching a machine off: the machine card drew «В
// сети» from /api/peers's control-plane flag while the headline here said the
// machine did not answer. The card now draws from this field, so the field has
// to arrive, and it has to be the diagnosis's own token rather than anything
// re-derived on the way out.
func TestStatusCarriesTheDiagnosisOwnWordAboutThePeer(t *testing.T) {
	t.Parallel()

	for _, want := range []core.PeerState{
		core.PeerStateAnswers, core.PeerStateSilentListed, core.PeerStateSilent,
		core.PeerStateOffline, core.PeerStateAbsent, core.PeerStateUnknown,
	} {
		src := newFakeStatus()
		res := sampleResult()
		res.Snapshot.Peer = want
		src.latest, src.hasLatest = res, true

		var got Status
		decode(t, do(t, New(Deps{Status: src}), http.MethodGet, "/api/status", ""), &got)
		if got.PeerState != want.String() {
			t.Errorf("PeerState = %q, want %q", got.PeerState, want)
		}
	}
}

// lastDiagnosis is GET /api/status with one finished diagnosis behind it, read
// back as the DTO. Two tests share it: the headline fields, and the rows.
func lastDiagnosis(t *testing.T) Status {
	t.Helper()

	src := newFakeStatus()
	src.latest, src.hasLatest, src.checking = sampleResult(), true, true

	var got Status
	decode(t, do(t, New(Deps{Status: src}), http.MethodGet, "/api/status", ""), &got)
	return got
}

func TestStatusRendersTheLastDiagnosis(t *testing.T) {
	t.Parallel()

	got := lastDiagnosis(t)
	if got.TakenAt != "2026-09-07T10:30:00Z" {
		t.Errorf("TakenAt = %q", got.TakenAt)
	}
	if got.Overall != "warn" || got.LatencyMs != 17 || !got.Checking {
		t.Errorf("status = %+v", got)
	}
	if !isRussian(got.Summary) {
		t.Errorf("the summary is not Russian: %q", got.Summary)
	}
}

func TestStatusRendersTheChecksAndFixes(t *testing.T) {
	t.Parallel()

	got := lastDiagnosis(t)
	if len(got.Checks) != 2 || got.Checks[0].ID != "tailscale" || got.Checks[1].State != "fail" {
		t.Fatalf("checks = %+v", got.Checks)
	}
	if len(got.Fixes) != 1 || got.Fixes[0].ID != "start_local_sshd" || !got.Fixes[0].NeedsAdmin {
		t.Errorf("fixes = %+v", got.Fixes)
	}
	// Labels and notes are passed through verbatim: they were written in Russian
	// upstream, apart from proper names like "Tailscale".
	if got.Checks[1].Note != "SSH-сервер не запущен" {
		t.Errorf("the Russian text did not survive the round trip: %+v", got.Checks[1])
	}
}

func TestStatusWithoutAPoller(t *testing.T) {
	t.Parallel()

	var got Status
	decode(t, do(t, New(Deps{}), http.MethodGet, "/api/status", ""), &got)
	if got.Overall != "unknown" || got.Checking {
		t.Errorf("status = %+v", got)
	}
}

func TestCheckProbesAndReturnsTheResult(t *testing.T) {
	t.Parallel()

	src := newFakeStatus()
	src.checkRes = sampleResult()
	h := New(Deps{Status: src})

	rec := do(t, h, http.MethodPost, "/api/check", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got Status
	decode(t, rec, &got)
	if got.Summary != "Частичная связь" || got.Checking {
		t.Errorf("status = %+v", got)
	}
}

func TestCheckReportsAStoppedPollerAndATimeout(t *testing.T) {
	t.Parallel()

	stopped := newFakeStatus()
	stopped.checkErr = app.ErrPollerStopped
	rec := do(t, New(Deps{Status: stopped}), http.MethodPost, "/api/check", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("stopped poller code = %d", rec.Code)
	}

	slow := newFakeStatus()
	slow.checkErr = errBoom
	rec = do(t, New(Deps{Status: slow}), http.MethodPost, "/api/check", "")
	if rec.Code != http.StatusGatewayTimeout {
		t.Errorf("failed check code = %d", rec.Code)
	}
	var body errorBody
	decode(t, rec, &body)
	if !isRussian(body.Message) {
		t.Errorf("message is not Russian: %q", body.Message)
	}

	rec = do(t, New(Deps{}), http.MethodPost, "/api/check", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no poller code = %d", rec.Code)
	}
}

func TestFixRunsTheRepairAndReportsIt(t *testing.T) {
	t.Parallel()

	fixes := &fakeFixes{out: app.FixOutcome{OK: false, Message: "Нужны права", NeedsAdmin: true}}
	h := New(Deps{Fixes: fixes})

	rec := do(t, h, http.MethodPost, "/api/fix", `{"id":"start_tailscale"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got fixResponse
	decode(t, rec, &got)
	if got.OK || !got.NeedsAdmin || got.Message != "Нужны права" {
		t.Errorf("fix response = %+v", got)
	}
	if fixes.lastID != "start_tailscale" {
		t.Errorf("fixer got id %q", fixes.lastID)
	}
}

func TestFixRejectsRubbishAndReportsAMissingFixer(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{Fixes: &fakeFixes{}}), http.MethodPost, "/api/fix", `{"id":`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON code = %d", rec.Code)
	}
	var body errorBody
	decode(t, rec, &body)
	if !isRussian(body.Message) {
		t.Errorf("message is not Russian: %q", body.Message)
	}

	rec = do(t, New(Deps{Fixes: &fakeFixes{}}), http.MethodPost, "/api/fix", `{"неизвестно":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field code = %d", rec.Code)
	}

	rec = do(t, New(Deps{}), http.MethodPost, "/api/fix", `{"id":"x"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no fixer code = %d", rec.Code)
	}
}

func TestFixRejectsAnOversizedBody(t *testing.T) {
	t.Parallel()

	body := `{"id":"` + strings.Repeat("x", maxBodyBytes+16) + `"}`
	rec := do(t, New(Deps{Fixes: &fakeFixes{}}), http.MethodPost, "/api/fix", body)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code = %d, want 413", rec.Code)
	}
}

func TestHistoryRendersTheUptimeStrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 7, 9, 0, 0, 0, time.UTC)
	h := New(Deps{History: &fakeHistory{points: []app.Point{
		{At: at, State: core.StateOK},
		{At: at.Add(time.Minute), State: core.StateFail},
	}}})

	var got historyResponse
	decode(t, do(t, h, http.MethodGet, "/api/history", ""), &got)
	if len(got.Points) != 2 {
		t.Fatalf("points = %+v", got.Points)
	}
	if got.Points[0].At != "2026-09-07T09:00:00Z" || got.Points[1].State != "fail" {
		t.Errorf("points = %+v", got.Points)
	}

	decode(t, do(t, New(Deps{}), http.MethodGet, "/api/history", ""), &got)
	if got.Points == nil || len(got.Points) != 0 {
		t.Errorf("without a history the strip must be an empty array, got %v", got.Points)
	}
}

func TestPeersListsTheTailnet(t *testing.T) {
	t.Parallel()

	h := New(Deps{
		Peers: &fakePeers{peers: []app.Peer{
			{Name: "workspace-claude-pc", Addr: "100.124.47.73", Online: true, Self: true},
			{Name: "iphone181", Addr: "100.64.6.3"},
			{Name: "win-sttm11d02rd", Addr: "100.127.188.87", Online: true},
		}},
		DefaultPeer: "win-sttm11d02rd",
	})

	var got peersResponse
	decode(t, do(t, h, http.MethodGet, "/api/peers", ""), &got)
	if len(got.Peers) != 3 || !got.Peers[0].Self || got.Peers[1].Online {
		t.Errorf("peers = %+v", got.Peers)
	}
	// The configured machine is not the first non-self entry here — a phone is.
	// The name is what lets the UI pick the right card instead of guessing.
	if got.ConfiguredPeer != "win-sttm11d02rd" {
		t.Errorf("configuredPeer = %q, want the configured machine", got.ConfiguredPeer)
	}
}

// The alarming case: the machine the user configured is not in the tailnet at
// all. The name must still come back, so the window can say whose absence it is
// reporting instead of showing whatever node happens to be listed.
func TestPeersNamesTheConfiguredPeerEvenWhenItIsAbsent(t *testing.T) {
	t.Parallel()

	h := New(Deps{
		Peers: &fakePeers{peers: []app.Peer{
			{Name: "workspace-claude-pc", Addr: "100.124.47.73", Online: true, Self: true},
			{Name: "iphone181", Addr: "100.64.6.3"},
		}},
		DefaultPeer: "win-sttm11d02rd",
	})

	var got peersResponse
	decode(t, do(t, h, http.MethodGet, "/api/peers", ""), &got)
	if got.ConfiguredPeer != "win-sttm11d02rd" {
		t.Errorf("configuredPeer = %q, want it named even though it is missing", got.ConfiguredPeer)
	}
	for _, p := range got.Peers {
		if p.Name == got.ConfiguredPeer {
			t.Fatalf("this test needs the configured peer absent, got %+v", got.Peers)
		}
	}
}

// With no peer configured the field is present and empty rather than absent:
// the UI branches on the value, and a missing key would look like an old build.
func TestPeersReportsAnEmptyConfiguredPeerWhenNoneIsSet(t *testing.T) {
	t.Parallel()

	h := New(Deps{Peers: &fakePeers{peers: []app.Peer{{Name: "iphone181", Addr: "100.64.6.3"}}}})

	rec := do(t, h, http.MethodGet, "/api/peers", "")
	if !strings.Contains(rec.Body.String(), `"configuredPeer":""`) {
		t.Errorf("body = %s, want an explicit empty configuredPeer", rec.Body.String())
	}
}

func TestPeersReportsFailures(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{Peers: &fakePeers{err: errBoom}}), http.MethodGet, "/api/peers", "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("code = %d", rec.Code)
	}
	rec = do(t, New(Deps{}), http.MethodGet, "/api/peers", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no peer source code = %d", rec.Code)
	}
	var body errorBody
	decode(t, rec, &body)
	if !isRussian(body.Message) {
		t.Errorf("message is not Russian: %q", body.Message)
	}
}

// A web page must not be able to drive this API, which is the one thing binding
// loopback does not stop by itself.
func TestOriginGuard(t *testing.T) {
	t.Parallel()

	cases := []struct {
		origin string
		want   int
	}{
		{"", http.StatusOK},
		{"http://127.0.0.1:8731", http.StatusOK},
		{"http://localhost:8731", http.StatusOK},
		{"http://[::1]:8731", http.StatusOK},
		{"null", http.StatusForbidden},
		{"https://example.com", http.StatusForbidden},
		{"http://100.127.188.87", http.StatusForbidden},
		{"http://127.0.0.1.evil.com", http.StatusForbidden},
		{"://", http.StatusForbidden},
	}
	h := New(Deps{Status: newFakeStatus()})
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("Origin %q = %d, want %d", tc.origin, rec.Code, tc.want)
		}
		if tc.want == http.StatusForbidden {
			var body errorBody
			decode(t, rec, &body)
			if !isRussian(body.Message) {
				t.Errorf("Origin %q message is not Russian: %q", tc.origin, body.Message)
			}
		}
	}
}

func TestUnknownPathAndWrongMethodAnswerInJSON(t *testing.T) {
	t.Parallel()

	h := New(Deps{Status: newFakeStatus()})

	rec := do(t, h, http.MethodGet, "/api/нет-такого", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path code = %d", rec.Code)
	}
	var body errorBody
	decode(t, rec, &body)
	if !isRussian(body.Message) {
		t.Errorf("message is not Russian: %q", body.Message)
	}

	rec = do(t, h, http.MethodDelete, "/api/status", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("wrong method code = %d", rec.Code)
	}
	decode(t, rec, &body)
	if !isRussian(body.Message) {
		t.Errorf("message is not Russian: %q", body.Message)
	}
}

func TestListenRefusesAnythingButLoopback(t *testing.T) {
	t.Parallel()

	if _, err := Listen("0.0.0.0:0"); err == nil {
		t.Error("the API agreed to answer on every interface")
	}
	if _, err := Listen("100.124.47.73:0"); err == nil {
		t.Error("the API agreed to answer on the tailnet address")
	}
	if _, err := Listen("not-an-address"); err == nil {
		t.Error("a malformed address was accepted")
	}

	// Port 0: the operating system picks one, so no test ever needs a fixed port.
	ln, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	host, _, splitErr := net.SplitHostPort(ln.Addr().String())
	if splitErr != nil || host != "127.0.0.1" {
		t.Errorf("listening on %s", ln.Addr())
	}
}
