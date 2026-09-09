package httpapi

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// The frontend is written against these exact names. This test is here so that
// renaming a field fails a test rather than a screen.

// keysOf lists the top-level keys of a JSON object body.
func keysOf(t *testing.T, body string) []string {
	t.Helper()

	var obj map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// wantKeys asserts an exact key set.
func wantKeys(t *testing.T, what, body string, want ...string) {
	t.Helper()

	sort.Strings(want)
	got := keysOf(t, body)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s keys = %v, want %v", what, got, want)
	}
}

// contractHandler is one handler with every dependency present.
func contractHandler() http.Handler {
	src := newFakeStatus()
	src.latest, src.hasLatest = sampleResult(), true
	src.checkRes = sampleResult()
	at := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)

	return New(Deps{
		Status:  src,
		Fixes:   &fakeFixes{out: app.FixOutcome{OK: true, Message: "Готово"}},
		History: &fakeHistory{points: []app.Point{{At: at, State: core.StateOK}}},
		Peers:   &fakePeers{peers: []app.Peer{{Name: "win-sttm11d02rd", Addr: "100.127.188.87"}}},
		Files: &fakeFiles{
			received: []string{`C:\in\a.txt`},
			items: []app.Transfer{{
				Name: "a.txt", Size: 10, At: at,
				Dir: app.TransferTo, State: app.TransferOK, Pct: 100,
			}},
		},
		Forwards: &fakeForwards{
			started: app.Forward{ID: "fwd-1", LocalPort: 1, RemoteHost: "p", RemotePort: 2, Addr: "a"},
			items:   []app.Forward{{ID: "fwd-1", LocalPort: 1, RemoteHost: "p", RemotePort: 2}},
			url:     "https://x.ts.net/",
		},
		Link: &fakeLink{out: app.LinkOutcome{OK: true, Message: "Tailscale подключён."}},
		// The shared folder, with one row in each of its two lists, so the
		// nested shapes below have something real to pin.
		Sync: &fakeSync{status: syncedStatus()},
		// The shared clipboard, switched on and with two lines in its list,
		// for the same reason.
		Clip:        &fakeClip{status: clipOn()},
		DefaultPeer: "win-sttm11d02rd",
	})
}

func TestStatusShapeIsTheAgreedContract(t *testing.T) {
	t.Parallel()

	h := contractHandler()
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/status"},
		{http.MethodPost, "/api/check"},
	} {
		body := do(t, h, route.method, route.path, "").Body.String()
		// peerState was added deliberately, and this line is the pin moving with
		// it. It carries what the diagnosis established about the peer machine,
		// so that the machine card has one server-stated fact to draw from
		// instead of joining «online» from /api/peers with a verdict from here
		// and getting them the wrong way round.
		wantKeys(t, route.path, body,
			"takenAt", "overall", "summary", "detail", "latencyMs", "checking",
			"peerState", "checks", "fixes")

		var st struct {
			Checks []json.RawMessage `json:"checks"`
			Fixes  []json.RawMessage `json:"fixes"`
		}
		if err := json.Unmarshal([]byte(body), &st); err != nil {
			t.Fatalf("decoding %s: %v", route.path, err)
		}
		wantKeys(t, "check row", string(st.Checks[0]), "id", "state", "label", "note")
		wantKeys(t, "fix", string(st.Fixes[0]), "id", "title", "explanation", "needsAdmin")
	}
}

func TestEveryOtherRouteKeepsItsAgreedShape(t *testing.T) {
	t.Parallel()

	h := contractHandler()
	cases := []struct {
		method, path, body string
		keys               []string
	}{
		{
			http.MethodPost, "/api/fix", `{"id":"start_tailscale"}`,
			[]string{"ok", "message", "needsAdmin"},
		},
		{http.MethodGet, "/api/history", "", []string{"points"}},
		{http.MethodGet, "/api/peers", "", []string{"peers", "configuredPeer"}},
		{
			http.MethodPost, "/api/files/send", `{"path":"C:\\a.bin","peer":"p"}`,
			[]string{"ok", "message"},
		},
		{http.MethodPost, "/api/files/receive", "", []string{"ok", "files", "message"}},
		{http.MethodGet, "/api/files", "", []string{"transfers"}},
		{
			http.MethodPost, "/api/forward", `{"localPort":1,"remoteHost":"p","remotePort":2}`,
			[]string{"ok", "id", "addr", "message"},
		},
		{http.MethodDelete, "/api/forward", `{"id":"fwd-1"}`, []string{"ok", "message"}},
		{http.MethodGet, "/api/forward", "", []string{"forwards"}},
		{http.MethodPost, "/api/serve", `{"port":8080}`, []string{"ok", "url", "message"}},
		{
			http.MethodPost, "/api/link", `{"action":"up"}`,
			[]string{"ok", "message", "loginURL", "needsAdmin"},
		},
		// The shared folder, added deliberately. GET /api/sync is the whole of
		// what rule 6 asks the Файлы tab to show: whether it is on, both
		// folders, when the last pass ran and how long it took, what moved,
		// what was skipped, one sentence about the pass, and the standing note
		// that a deletion never travels. POST /api/sync/run asks for a pass
		// and answers at once, in the ok/message shape the other actions use.
		{
			http.MethodGet, "/api/sync", "",
			[]string{
				"on", "folder", "peerFolder", "peer", "maxSize", "running",
				"historyLost", "at", "tookMs", "failed", "moved", "skipped", "message", "note",
			},
		},
		{http.MethodPost, "/api/sync/run", "", []string{"ok", "message"}},
		// The shared clipboard, added deliberately. GET /api/clip is the whole
		// of what the Файлы tab shows about it: whether this machine can share
		// a clipboard at all, whether the user has switched it on, the peer and
		// the cap, whether the program is running over there, whether anything
		// went wrong, the tally, the recent lines, one sentence, and the
		// standing note about what is and is not carried. There is no field for
		// the content and there never may be — see tools/gates.
		{
			http.MethodGet, "/api/clip", "",
			[]string{
				"available", "on", "peer", "maxSize", "peerMissing", "failed",
				"counts", "events", "message", "note",
			},
		},
		{http.MethodPost, "/api/clip/on", "", []string{"ok", "message"}},
		{http.MethodPost, "/api/clip/off", "", []string{"ok", "message"}},
		// The route the peer's own instance posts an item to, through a port
		// forwarded over the SSH session. Its answer says what happened and
		// never repeats the item back.
		{
			http.MethodPost, "/api/clip/receive", `{"text":"привет"}`,
			[]string{"ok", "message"},
		},
	}
	for _, tc := range cases {
		body := do(t, h, tc.method, tc.path, tc.body).Body.String()
		wantKeys(t, tc.method+" "+tc.path, body, tc.keys...)
	}
}

func TestNestedListShapesAreTheAgreedContract(t *testing.T) {
	t.Parallel()

	h := contractHandler()
	cases := []struct {
		path, field string
		keys        []string
	}{
		{"/api/history", "points", []string{"at", "state"}},
		{"/api/peers", "peers", []string{"name", "addr", "online", "self"}},
		{"/api/files", "transfers", []string{"name", "size", "at", "dir", "state", "pct"}},
		{"/api/forward", "forwards", []string{"id", "localPort", "remoteHost", "remotePort"}},
		// A moved row says which way the file went and, when it is a conflict
		// copy, the name it landed under — the one thing rule 6 says must be
		// impossible to miss.
		{"/api/sync", "moved", []string{"name", "size", "way", "conflict", "saved"}},
		// A skipped row carries both the token and the sentence: the UI groups
		// by the first and shows the second.
		{"/api/sync", "skipped", []string{"name", "size", "why", "reason"}},
		// One line of the shared clipboard's list: when, which way or which
		// refusal, how big, and the sentence the window shows. Four fields, and
		// none of them is what was copied.
		{"/api/clip", "events", []string{"at", "kind", "size", "label"}},
	}
	for _, tc := range cases {
		body := do(t, h, http.MethodGet, tc.path, "").Body.String()
		// The body is decoded field by field rather than as one map of lists:
		// a response may carry scalars alongside its list, as /api/peers does
		// with configuredPeer.
		var wrapper map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
			t.Fatalf("decoding %s: %v", tc.path, err)
		}
		var items []json.RawMessage
		if err := json.Unmarshal(wrapper[tc.field], &items); err != nil {
			t.Fatalf("decoding %s.%s: %v", tc.path, tc.field, err)
		}
		if len(items) == 0 {
			t.Fatalf("%s returned no %s to check", tc.path, tc.field)
		}
		wantKeys(t, tc.path+"."+tc.field, string(items[0]), tc.keys...)
	}
}

// The shared clipboard's tally is a nested object rather than a list, so it is
// pinned on its own. Every one of these is a number the window shows; two of
// them — tooBig and marked — are the whole of how a user sees rules 2 and 3
// doing their work.
func TestTheClipboardTallyIsTheAgreedContract(t *testing.T) {
	t.Parallel()

	body := do(t, contractHandler(), http.MethodGet, "/api/clip", "").Body.String()
	var wrapper map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &wrapper); err != nil {
		t.Fatalf("decoding /api/clip: %v", err)
	}
	wantKeys(t, "/api/clip.counts", string(wrapper["counts"]),
		"sent", "received", "tooBig", "marked", "notText", "failed")
}

func TestErrorBodiesCarryARussianMessage(t *testing.T) {
	t.Parallel()

	h := contractHandler()
	cases := []struct{ method, path string }{
		{http.MethodGet, "/api/нет"},
		{http.MethodDelete, "/api/status"},
	}
	for _, tc := range cases {
		body := do(t, h, tc.method, tc.path, "").Body.String()
		wantKeys(t, tc.method+" "+tc.path, body, "message")

		var got errorBody
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatalf("decoding %s: %v", tc.path, err)
		}
		if !isRussian(got.Message) || strings.Contains(got.Message, "boom") {
			t.Errorf("%s message = %q", tc.path, got.Message)
		}
	}
}
