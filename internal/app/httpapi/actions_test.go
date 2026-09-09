package httpapi

import (
	"net/http"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

func TestFilesSendUsesTheDefaultPeerWhenNoneIsNamed(t *testing.T) {
	t.Parallel()

	files := &fakeFiles{}
	h := New(Deps{Files: files, DefaultPeer: "win-sttm11d02rd"})

	rec := do(t, h, http.MethodPost, "/api/files/send", `{"path":"C:\\tmp\\отчёт.pdf"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}
	var got okMessage
	decode(t, rec, &got)
	if !got.OK || !isRussian(got.Message) {
		t.Errorf("response = %+v", got)
	}
	if files.sentPath != `C:\tmp\отчёт.pdf` || files.sentPeer != "win-sttm11d02rd" {
		t.Errorf("Send got (%q, %q)", files.sentPath, files.sentPeer)
	}
}

func TestFilesSendRejectsWhatItCannotActuponAndReportsFailures(t *testing.T) {
	t.Parallel()

	h := New(Deps{Files: &fakeFiles{}, DefaultPeer: "peer"})
	if rec := do(t, h, http.MethodPost, "/api/files/send", `{"path":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty path code = %d", rec.Code)
	}

	noPeer := New(Deps{Files: &fakeFiles{}})
	rec := do(t, noPeer, http.MethodPost, "/api/files/send", `{"path":"C:\\a.bin"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("no peer code = %d", rec.Code)
	}

	failing := New(Deps{Files: &fakeFiles{sendErr: errBoom}, DefaultPeer: "peer"})
	rec = do(t, failing, http.MethodPost, "/api/files/send", `{"path":"C:\\a.bin"}`)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("failed send code = %d", rec.Code)
	}
	var got okMessage
	decode(t, rec, &got)
	if got.OK || !isRussian(got.Message) {
		t.Errorf("response = %+v", got)
	}

	none := New(Deps{DefaultPeer: "peer"})
	rec = do(t, none, http.MethodPost, "/api/files/send", `{"path":"C:\\a.bin"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no file service code = %d", rec.Code)
	}

	rec = do(t, h, http.MethodPost, "/api/files/send", `{`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON code = %d", rec.Code)
	}
}

func TestFilesReceiveDrainsTheInbox(t *testing.T) {
	t.Parallel()

	h := New(Deps{Files: &fakeFiles{received: []string{`C:\in\а.txt`, `C:\in\b.txt`}}})
	rec := do(t, h, http.MethodPost, "/api/files/receive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}

	var got receiveResponse
	decode(t, rec, &got)
	if !got.OK || len(got.Files) != 2 {
		t.Fatalf("response = %+v", got)
	}
	// Base names only: where the inbox lives is not something to put on a screen.
	if got.Files[0] != "а.txt" || got.Files[1] != "b.txt" {
		t.Errorf("files = %v", got.Files)
	}
	if !isRussian(got.Message) {
		t.Errorf("message is not Russian: %q", got.Message)
	}
}

func TestFilesReceiveOnAnEmptyInboxAndOnFailure(t *testing.T) {
	t.Parallel()

	var got receiveResponse
	decode(t, do(t, New(Deps{Files: &fakeFiles{}}), http.MethodPost, "/api/files/receive", ""), &got)
	if !got.OK || len(got.Files) != 0 || got.Message != msgReceiveEmpty {
		t.Errorf("empty inbox response = %+v", got)
	}

	failing := New(Deps{Files: &fakeFiles{received: []string{`C:\in\a.txt`}, receiveErr: errBoom}})
	rec := do(t, failing, http.MethodPost, "/api/files/receive", "")
	if rec.Code != http.StatusBadGateway {
		t.Errorf("code = %d", rec.Code)
	}
	decode(t, rec, &got)
	if got.OK || len(got.Files) != 1 {
		t.Errorf("a file that landed before the error was dropped: %+v", got)
	}

	rec = do(t, New(Deps{}), http.MethodPost, "/api/files/receive", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no file service code = %d", rec.Code)
	}
}

func TestFilesListRendersRecentTransfers(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)
	h := New(Deps{Files: &fakeFiles{items: []app.Transfer{
		{Name: "отчёт.pdf", Size: 2_411_724, At: at, Dir: app.TransferTo, State: app.TransferOK, Pct: 100},
		{Name: "a.bin", Size: 512, At: at, Dir: app.TransferFrom, State: app.TransferErr, Pct: 30},
	}}})

	var got filesResponse
	decode(t, do(t, h, http.MethodGet, "/api/files", ""), &got)
	if len(got.Transfers) != 2 {
		t.Fatalf("transfers = %+v", got.Transfers)
	}
	if got.Transfers[0].Size != "2,3 МБ" || got.Transfers[0].Dir != "to" {
		t.Errorf("first transfer = %+v", got.Transfers[0])
	}
	if got.Transfers[1].Size != "512 Б" || got.Transfers[1].State != "err" || got.Transfers[1].Pct != 30 {
		t.Errorf("second transfer = %+v", got.Transfers[1])
	}
	if got.Transfers[0].At != "2026-09-07T11:00:00Z" {
		t.Errorf("At = %q", got.Transfers[0].At)
	}

	decode(t, do(t, New(Deps{}), http.MethodGet, "/api/files", ""), &got)
	if got.Transfers == nil || len(got.Transfers) != 0 {
		t.Errorf("without a file service the list must be empty, got %v", got.Transfers)
	}
}

func TestForwardStartOpensATunnel(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwards{started: app.Forward{
		ID: "fwd-1", LocalPort: 13389, RemoteHost: "100.127.188.87", RemotePort: 3389,
		Addr: "127.0.0.1:13389",
	}}
	h := New(Deps{Forwards: fwd})

	body := `{"localPort":13389,"remoteHost":"100.127.188.87","remotePort":3389}`
	rec := do(t, h, http.MethodPost, "/api/forward", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, body %s", rec.Code, rec.Body)
	}

	var got forwardResponse
	decode(t, rec, &got)
	if !got.OK || got.ID != "fwd-1" || got.Addr != "127.0.0.1:13389" || !isRussian(got.Message) {
		t.Errorf("response = %+v", got)
	}
	if fwd.lastHost != "100.127.188.87" || fwd.lastArgs[0] != 13389 || fwd.lastArgs[1] != 3389 {
		t.Errorf("Start got %v to %q", fwd.lastArgs, fwd.lastHost)
	}
}

func TestForwardStartRejectsBadRequests(t *testing.T) {
	t.Parallel()

	h := New(Deps{Forwards: &fakeForwards{}})
	cases := []struct {
		name string
		body string
	}{
		{"no host", `{"localPort":0,"remoteHost":"","remotePort":22}`},
		{"negative local port", `{"localPort":-1,"remoteHost":"p","remotePort":22}`},
		{"huge local port", `{"localPort":70000,"remoteHost":"p","remotePort":22}`},
		{"remote port zero", `{"localPort":0,"remoteHost":"p","remotePort":0}`},
		{"huge remote port", `{"localPort":0,"remoteHost":"p","remotePort":70000}`},
		{"rubbish", `{`},
	}
	for _, tc := range cases {
		if rec := do(t, h, http.MethodPost, "/api/forward", tc.body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", tc.name, rec.Code)
		}
	}

	failing := New(Deps{Forwards: &fakeForwards{startErr: errBoom}})
	body := `{"localPort":0,"remoteHost":"p","remotePort":22}`
	if rec := do(t, failing, http.MethodPost, "/api/forward", body); rec.Code != http.StatusBadGateway {
		t.Errorf("failed start code = %d", rec.Code)
	}
	rec := do(t, New(Deps{}), http.MethodPost, "/api/forward", body)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no forward service code = %d", rec.Code)
	}
}

func TestForwardStopClosesATunnel(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwards{}
	rec := do(t, New(Deps{Forwards: fwd}), http.MethodDelete, "/api/forward", `{"id":"fwd-1"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got okMessage
	decode(t, rec, &got)
	if !got.OK || !isRussian(got.Message) || fwd.stoppedI != "fwd-1" {
		t.Errorf("response = %+v, stopped %q", got, fwd.stoppedI)
	}
}

func TestForwardStopHandlesTheAwkwardCases(t *testing.T) {
	t.Parallel()

	h := New(Deps{Forwards: &fakeForwards{}})
	if rec := do(t, h, http.MethodDelete, "/api/forward", `{"id":""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("empty id code = %d", rec.Code)
	}
	if rec := do(t, h, http.MethodDelete, "/api/forward", `{`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad JSON code = %d", rec.Code)
	}

	missing := New(Deps{Forwards: &fakeForwards{stopErr: app.ErrForwardNotFound}})
	rec := do(t, missing, http.MethodDelete, "/api/forward", `{"id":"fwd-9"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown id code = %d", rec.Code)
	}

	// A socket that complained on the way out still counts as stopped: it is out
	// of the registry either way, and the user is told what actually happened.
	noisy := New(Deps{Forwards: &fakeForwards{stopErr: errBoom}})
	rec = do(t, noisy, http.MethodDelete, "/api/forward", `{"id":"fwd-1"}`)
	var got okMessage
	decode(t, rec, &got)
	if rec.Code != http.StatusOK || !got.OK || got.Message != msgForwardStopBad {
		t.Errorf("code %d, response %+v", rec.Code, got)
	}

	rec = do(t, New(Deps{}), http.MethodDelete, "/api/forward", `{"id":"fwd-1"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no forward service code = %d", rec.Code)
	}
}

func TestForwardListRendersTheLiveTunnels(t *testing.T) {
	t.Parallel()

	h := New(Deps{Forwards: &fakeForwards{items: []app.Forward{
		{ID: "fwd-1", LocalPort: 13389, RemoteHost: "100.127.188.87", RemotePort: 3389},
	}}})

	var got forwardsResponse
	decode(t, do(t, h, http.MethodGet, "/api/forward", ""), &got)
	if len(got.Forwards) != 1 || got.Forwards[0].LocalPort != 13389 {
		t.Errorf("forwards = %+v", got.Forwards)
	}

	decode(t, do(t, New(Deps{}), http.MethodGet, "/api/forward", ""), &got)
	if got.Forwards == nil || len(got.Forwards) != 0 {
		t.Errorf("without a forward service the list must be empty, got %v", got.Forwards)
	}
}

func TestServePublishesAPortToTheTailnet(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwards{url: "https://workspace-claude-pc.tail1234.ts.net/"}
	rec := do(t, New(Deps{Forwards: fwd}), http.MethodPost, "/api/serve", `{"port":8080}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d", rec.Code)
	}
	var got serveResponse
	decode(t, rec, &got)
	if !got.OK || got.URL != fwd.url || !isRussian(got.Message) || fwd.servePor != 8080 {
		t.Errorf("response = %+v, port %d", got, fwd.servePor)
	}
}

func TestServeRejectsBadPortsAndReportsFailures(t *testing.T) {
	t.Parallel()

	h := New(Deps{Forwards: &fakeForwards{}})
	for _, body := range []string{`{"port":0}`, `{"port":70000}`, `{`} {
		if rec := do(t, h, http.MethodPost, "/api/serve", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code = %d", body, rec.Code)
		}
	}

	failing := New(Deps{Forwards: &fakeForwards{serveErr: errBoom}})
	if rec := do(t, failing, http.MethodPost, "/api/serve", `{"port":8080}`); rec.Code != http.StatusBadGateway {
		t.Errorf("failed serve code = %d", rec.Code)
	}
	rec := do(t, New(Deps{}), http.MethodPost, "/api/serve", `{"port":8080}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no forward service code = %d", rec.Code)
	}
}

func TestHumanSizeSpeaksRussian(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   int64
		want string
	}{
		{-1, "0 Б"},
		{0, "0 Б"},
		{999, "999 Б"},
		{1024, "1,0 КБ"},
		{1536, "1,5 КБ"},
		{2_411_724, "2,3 МБ"},
		{5 * 1024 * 1024 * 1024, "5,0 ГБ"},
		{9 * 1024 * 1024 * 1024 * 1024, "9,0 ТБ"},
		{4096 * 1024 * 1024 * 1024 * 1024, "4096,0 ТБ"},
	}
	for _, tc := range cases {
		if got := humanSize(tc.in); got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
