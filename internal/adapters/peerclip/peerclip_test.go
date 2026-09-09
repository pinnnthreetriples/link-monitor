package peerclip

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
)

// The peer, as these tests pretend it is.
const (
	peerName  = "win-sttm11d02rd"
	peerExe   = `C:\Users\user\LinkMonitor\linkmon.exe`
	peerPID   = 7788
	peerPort  = 51234
	theItem   = "текст, скопированный на ноутбуке"
	scriptOut = "Preparing modules for first use...\r\n" // the noise a cold PowerShell adds
)

var peerStart = time.Date(2026, 9, 8, 19, 30, 15, 1234567, time.UTC)

// fakeShell is the peer's PowerShell, answering whatever a test hands it.
type fakeShell struct {
	stdout string
	stderr string
	code   int
	err    error

	ran []string
}

func (f *fakeShell) RunPowerShell(_ context.Context, script string) (string, string, int, error) {
	f.ran = append(f.ran, script)
	return f.stdout, f.stderr, f.code, f.err
}

// fakeTunnel is what LocalForward hands back, reporting the address of the
// test server standing in for the peer's instance.
type fakeTunnel struct {
	addr   net.Addr
	closed int
}

func (f *fakeTunnel) Close() error {
	f.closed++
	return nil
}

func (f *fakeTunnel) Addr() net.Addr { return f.addr }

// blindTunnel is a forward that will not say where it listens, which is the
// one shape this package cannot use.
type blindTunnel struct{}

func (blindTunnel) Close() error { return nil }

// fakeForwarder records what was forwarded and hands back a tunnel.
type fakeForwarder struct {
	tunnel io.Closer
	err    error

	localPort  int
	remoteHost string
	remotePort int
	opened     int
}

func (f *fakeForwarder) LocalForward(_ context.Context, localPort int, remoteHost string, remotePort int) (
	io.Closer, error,
) {
	f.opened++
	f.localPort, f.remoteHost, f.remotePort = localPort, remoteHost, remotePort
	if f.err != nil {
		return nil, f.err
	}
	return f.tunnel, nil
}

// record is the record the peer publishes in the happy case.
func record() localendpoint.Record {
	return localendpoint.Record{
		Version:   localendpoint.Version,
		BaseURL:   fmt.Sprintf("http://127.0.0.1:%d/", peerPort),
		PID:       peerPID,
		Exe:       peerExe,
		StartedAt: peerStart,
	}
}

// liveAnswer is what the script prints when the peer's instance is running.
func liveAnswer(t *testing.T, rec localendpoint.Record, proc string) string {
	t.Helper()

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshalling the record: %v", err)
	}
	return scriptOut +
		markRecord + base64.StdEncoding.EncodeToString(data) + "\r\n" +
		proc + "\r\n"
}

// liveProcess is the PROCESS line for a running instance.
func liveProcess() string {
	return markProcess + fmt.Sprintf("%d|%s|%s", peerPID, peerExe, peerStart.Format(stampFormat))
}

// inbox is a test server standing in for the peer's instance: it answers on
// the receive route and remembers what arrived.
type inbox struct {
	server *httptest.Server
	body   string
	path   string
	method string
	status int
	answer string
}

func newInbox(t *testing.T) *inbox {
	t.Helper()

	in := &inbox{status: http.StatusOK, answer: `{"ok":true,"message":"Принято."}`}
	in.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		in.body, in.path, in.method = string(body), r.URL.Path, r.Method
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(in.status)
		_, _ = io.WriteString(w, in.answer)
	}))
	t.Cleanup(in.server.Close)
	return in
}

func (in *inbox) addr() net.Addr { return in.server.Listener.Addr() }

// wired is a peer whose script answers with a live record and whose forward
// lands on the test inbox.
func wired(t *testing.T) (*Peer, *fakeShell, *fakeForwarder, *inbox) {
	t.Helper()

	in := newInbox(t)
	shell := &fakeShell{stdout: liveAnswer(t, record(), liveProcess())}
	fwd := &fakeForwarder{tunnel: &fakeTunnel{addr: in.addr()}}
	return New(shell, fwd, peerName), shell, fwd, in
}

// TestDeliverReachesThePeersInstanceThroughTheForwardedPort is the whole path
// in one test: the record is read over SSH, the port named in it is forwarded,
// and the item arrives on the instance's own route.
func TestDeliverReachesThePeersInstanceThroughTheForwardedPort(t *testing.T) {
	t.Parallel()

	peer, shell, fwd, in := wired(t)
	if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
		t.Fatalf("Deliver() = %v", err)
	}

	if len(shell.ran) != 1 || !strings.Contains(shell.ran[0], localendpoint.RelativePath()) {
		t.Errorf("the script did not read the endpoint record: %q", shell.ran)
	}
	if fwd.localPort != 0 || fwd.remoteHost != loopback || fwd.remotePort != peerPort {
		t.Errorf("forwarded %d -> %s:%d, want 0 -> %s:%d",
			fwd.localPort, fwd.remoteHost, fwd.remotePort, loopback, peerPort)
	}
	if in.method != http.MethodPost || in.path != "/"+receivePath {
		t.Errorf("the instance got %s %s, want POST /%s", in.method, in.path, receivePath)
	}

	var got struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(in.body), &got); err != nil {
		t.Fatalf("the body %q is not the agreed shape: %v", in.body, err)
	}
	if got.Text != theItem {
		t.Errorf("the item arrived as %q, want %q", got.Text, theItem)
	}
}

// Nothing of ours is left running: the forward is opened for one item and
// closed again, whatever the outcome.
func TestTheForwardIsClosedAfterEveryItem(t *testing.T) {
	t.Parallel()

	in := newInbox(t)
	tunnel := &fakeTunnel{addr: in.addr()}
	shell := &fakeShell{stdout: liveAnswer(t, record(), liveProcess())}
	peer := New(shell, &fakeForwarder{tunnel: tunnel}, peerName)

	if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
		t.Fatalf("Deliver() = %v", err)
	}
	in.status, in.answer = http.StatusServiceUnavailable, `{"ok":false,"message":"Выключено."}`
	if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
		t.Fatal("Deliver() = nil after the instance refused the item")
	}
	if tunnel.closed != 2 {
		t.Errorf("the forward was closed %d times for two items, want 2", tunnel.closed)
	}
}

// TestAPeerWithNoInstanceRunningIsSaidToBeNotRunning is the state the UI has
// to be able to name: normal, and not a fault.
func TestAPeerWithNoInstanceRunningIsSaidToBeNotRunning(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"no record was ever published":  scriptOut + markAbsent + "\r\n",
		"the record's process has gone": "",
	}
	cases["the record's process has gone"] = liveAnswer(t, record(),
		fmt.Sprintf("%s%d", markNoProc, peerPID))

	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			peer := New(&fakeShell{stdout: stdout}, &fakeForwarder{}, peerName)
			err := peer.Deliver(context.Background(), []byte(theItem))
			if !localendpoint.NotRunning(err) {
				t.Errorf("Deliver() = %v, want an error NotRunning answers for", err)
			}
		})
	}
}

// A stale record is refused before anything is dialled, which is what stops
// this program from posting an item to whatever now holds a recycled port.
func TestAStaleRecordIsRefusedBeforeAnythingIsForwarded(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"the pid now belongs to another program": markProcess +
			fmt.Sprintf("%d|%s|%s", peerPID, `C:\Windows\notepad.exe`, peerStart.Format(stampFormat)),
		"the pid was reused by another copy of us": markProcess +
			fmt.Sprintf("%d|%s|%s", peerPID, peerExe, peerStart.Add(time.Second).Format(stampFormat)),
		"the peer described a different process": markProcess +
			fmt.Sprintf("%d|%s|%s", peerPID+1, peerExe, peerStart.Format(stampFormat)),
	}
	for name, proc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fwd := &fakeForwarder{}
			peer := New(&fakeShell{stdout: liveAnswer(t, record(), proc)}, fwd, peerName)

			err := peer.Deliver(context.Background(), []byte(theItem))
			if !errors.Is(err, localendpoint.ErrStale) {
				t.Errorf("Deliver() = %v, want ErrStale", err)
			}
			if fwd.opened != 0 {
				t.Error("a port was forwarded on the strength of a stale record")
			}
		})
	}
}

// A record that is malformed, of an unknown version, or names an address that
// is not the peer's own loopback is refused by localendpoint's own rules, and
// this proves the remote reader is held to them.
func TestARecordThatWouldBeRefusedLocallyIsRefusedHere(t *testing.T) {
	t.Parallel()

	elsewhere := record()
	elsewhere.BaseURL = "http://10.0.0.9:8080/"
	future := record()
	future.Version = localendpoint.Version + 1

	cases := map[string]string{
		"an address that is not the peer's loopback": liveAnswer(t, elsewhere, liveProcess()),
		"a schema version this build cannot read":    liveAnswer(t, future, liveProcess()),
		"not base64 at all":                          markRecord + "!!!not base64!!!\r\n",
		"base64 of something that is not a record":   markRecord + base64.StdEncoding.EncodeToString([]byte("{}")),
	}
	for name, stdout := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fwd := &fakeForwarder{}
			peer := New(&fakeShell{stdout: stdout}, fwd, peerName)

			if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
				t.Fatal("Deliver() = nil for a record that should have been refused")
			}
			if fwd.opened != 0 {
				t.Error("a port was forwarded on the strength of a refused record")
			}
		})
	}
}

func TestAScriptThatCouldNotRunIsReportedAsItself(t *testing.T) {
	t.Parallel()

	cases := map[string]*fakeShell{
		"no ssh session":            {err: errors.New("dialing 100.127.188.87:22: connection refused")},
		"the script exited badly":   {code: 1, stderr: "Get-Process : не удалось\r\nвторая строка"},
		"an answer with no marks":   {stdout: "какой-то шум\r\n"},
		"a process line in pieces":  {stdout: markRecord + "x\r\n" + markProcess + "7788|only-two\r\n"},
		"an unreadable start time":  {stdout: markProcess + "7788|C:\\x.exe|не время\r\n"},
		"a process id that is not":  {stdout: markProcess + "нет|C:\\x.exe|2026-09-08T00:00:00.0000000Z\r\n"},
		"a process id of zero":      {stdout: markProcess + "0|C:\\x.exe|2026-09-08T00:00:00.0000000Z\r\n"},
		"a record that is not JSON": {stdout: markRecord + base64.StdEncoding.EncodeToString([]byte("nope"))},
	}
	for name, shell := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			peer := New(shell, &fakeForwarder{}, peerName)
			if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
				t.Error("Deliver() = nil, want a failure")
			}
		})
	}
}

// The error a failure produces must not carry the item. It is the one rule
// this package exists under that a reader cannot check by looking at the
// happy path.
func TestNoFailureCarriesTheItem(t *testing.T) {
	t.Parallel()

	const secret = "пароль-который-нельзя-логировать"
	shells := []*fakeShell{
		{err: errors.New("no session")},
		{code: 1, stderr: "boom"},
		{stdout: scriptOut + markAbsent},
		{stdout: markRecord + "not base64"},
	}
	for _, shell := range shells {
		peer := New(shell, &fakeForwarder{err: errors.New("no tunnel")}, peerName)
		err := peer.Deliver(context.Background(), []byte(secret))
		if err == nil {
			t.Fatal("Deliver() = nil, want a failure to inspect")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error carries the clipboard item: %v", err)
		}
	}
}

func TestAForwardThatCannotBeOpenedOrReadIsReported(t *testing.T) {
	t.Parallel()

	cases := map[string]*fakeForwarder{
		"the session is gone":            {err: errors.New("session dropped")},
		"the forward will not say where": {tunnel: blindTunnel{}},
		"the forward listens on nothing": {tunnel: &fakeTunnel{}},
		"the instance is no longer answering": {tunnel: &fakeTunnel{
			addr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1},
		}},
	}
	for name, fwd := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shell := &fakeShell{stdout: liveAnswer(t, record(), liveProcess())}
			peer := New(shell, fwd, peerName)
			if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
				t.Error("Deliver() = nil, want a failure")
			}
		})
	}
}

func TestAnInstanceThatAnswersWithSomethingElseIsNotBelieved(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
		answer string
	}{
		{"not this API at all", http.StatusOK, "<html>hello</html>"},
		{"a refusal", http.StatusServiceUnavailable, `{"ok":false,"message":"Общий буфер выключен."}`},
		{"ok false with a 200", http.StatusOK, `{"ok":false,"message":"Не вышло."}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			peer, _, _, in := wired(t)
			in.status, in.answer = tc.status, tc.answer
			if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
				t.Error("Deliver() = nil, want a failure")
			}
		})
	}
}

func TestTheNameIsWhatTheUICallsThePeer(t *testing.T) {
	t.Parallel()

	if got := New(&fakeShell{}, &fakeForwarder{}, peerName).Name(); got != peerName {
		t.Errorf("Name() = %q, want %q", got, peerName)
	}
}

// A cancelled context stops the delivery rather than the request hanging on a
// tunnel nobody is watching any more.
func TestACancelledContextStopsTheDelivery(t *testing.T) {
	t.Parallel()

	peer, _, _, _ := wired(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := peer.Deliver(ctx, []byte(theItem)); err == nil {
		t.Error("Deliver() = nil for a cancelled context")
	}
}
