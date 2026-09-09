package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
)

// fakeClip is the shared clipboard, as this package needs it. It lives here
// rather than in fakes_test.go so that the clipboard's own fake sits beside
// the clipboard's own tests.
type fakeClip struct {
	mu     sync.Mutex
	status app.ClipStatus
	onErr  error
	putErr error

	turnedOn  int
	turnedOff int
	got       []string
}

func (f *fakeClip) On() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.status.On
}

func (f *fakeClip) TurnOn() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.onErr != nil {
		return f.onErr
	}
	f.turnedOn++
	f.status.On = true
	return nil
}

func (f *fakeClip) TurnOff() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.turnedOff++
	f.status.On = false
}

func (f *fakeClip) Status() app.ClipStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.status
}

func (f *fakeClip) Receive(text []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.putErr != nil {
		return f.putErr
	}
	f.got = append(f.got, string(text))
	return nil
}

func (f *fakeClip) arrived() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.got...)
}

// clipOn is a shared clipboard switched on, with one line of each kind that
// carries a size and one that must not.
func clipOn() app.ClipStatus {
	at := time.Date(2026, 9, 8, 21, 15, 0, 0, time.UTC)
	return app.ClipStatus{
		Available: true,
		On:        true,
		PeerName:  "win-sttm11d02rd",
		MaxBytes:  64 << 10,
		Counts:    app.ClipCounts{Sent: 3, Received: 1, TooBig: 1, Marked: 2, NotText: 4},
		Events: []app.ClipEvent{
			{At: at, Kind: app.ClipMarked, Bytes: 0},
			{At: at.Add(time.Second), Kind: app.ClipSent, Bytes: 128},
		},
	}
}

// clipHandler is one handler with a shared clipboard in the given state.
func clipHandler(st app.ClipStatus) (http.Handler, *fakeClip) {
	clip := &fakeClip{status: st}
	return New(Deps{Clip: clip}), clip
}

func TestClipStatusSaysWhichOfTheThreeStatesItIsIn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		deps        Deps
		wantOn      bool
		wantAvail   bool
		wantMessage string
	}{
		{
			name: "no clipboard on this machine at all",
			deps: Deps{Clip: &fakeClip{}},
			// Available false is the whole answer: the switch is not offered.
			wantMessage: msgClipNoClipboard,
		},
		{
			name:        "the feature was never wired up",
			deps:        Deps{},
			wantMessage: msgClipNoClipboard,
		},
		{
			name: "available and off, which is where it starts",
			deps: Deps{Clip: &fakeClip{status: app.ClipStatus{
				Available: true, PeerName: "win-sttm11d02rd", MaxBytes: 1024,
			}}},
			wantAvail:   true,
			wantMessage: msgClipOff,
		},
		{
			name:        "on, with nothing having travelled yet",
			deps:        Deps{Clip: &fakeClip{status: app.ClipStatus{Available: true, On: true}}},
			wantAvail:   true,
			wantOn:      true,
			wantMessage: msgClipOnNothingYet,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, New(tc.deps), http.MethodGet, "/api/clip", "")
			if rec.Code != http.StatusOK {
				t.Fatalf("GET /api/clip = %d, want 200", rec.Code)
			}
			var got clipResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding %s: %v", rec.Body.String(), err)
			}
			if got.On != tc.wantOn || got.Available != tc.wantAvail {
				t.Errorf("available/on = %v/%v, want %v/%v",
					got.Available, got.On, tc.wantAvail, tc.wantOn)
			}
			if got.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", got.Message, tc.wantMessage)
			}
			if !isRussian(got.Note) {
				t.Errorf("note = %q, want the Russian guarantee", got.Note)
			}
		})
	}
}

// The message the user reads when the program is not running on the other
// machine has to name that machine and has to read as a normal state.
func TestTheMessageAboutThePeersProgramNamesTheMachine(t *testing.T) {
	t.Parallel()

	st := clipOn()
	st.PeerMissing = true
	h, _ := clipHandler(st)

	var got clipResponse
	body := do(t, h, http.MethodGet, "/api/clip", "").Body
	if err := json.Unmarshal(body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %s: %v", body.String(), err)
	}
	if !got.PeerMissing {
		t.Error("peerMissing = false, want the window able to draw the state")
	}
	if got.Failed {
		t.Error("failed = true — the peer's program not running is not a fault")
	}
	if !strings.Contains(got.Message, "win-sttm11d02rd") {
		t.Errorf("message = %q, want it to name the machine", got.Message)
	}
	if got.Message != fmt.Sprintf(msgClipPeerMissingFmt, "win-sttm11d02rd") {
		t.Errorf("message = %q, want the peer-missing sentence", got.Message)
	}
}

func TestAFailureIsFlaggedRatherThanLeftToTheWordingToConvey(t *testing.T) {
	t.Parallel()

	st := clipOn()
	st.Err = "Не удалось передать содержимое на вторую машину."
	h, _ := clipHandler(st)

	var got clipResponse
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/api/clip", "").Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !got.Failed || got.Message != st.Err {
		t.Errorf("failed/message = %v/%q, want true and the app's own sentence", got.Failed, got.Message)
	}
}

// A tally is what rules 2 and 3 ask to be visible.
func TestTheTallyIsCarriedWholeAndTheLinesAreNewestFirst(t *testing.T) {
	t.Parallel()

	h, _ := clipHandler(clipOn())

	var got clipResponse
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/api/clip", "").Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	want := ClipCountsDTO{Sent: 3, Received: 1, TooBig: 1, Marked: 2, NotText: 4}
	if got.Counts != want {
		t.Errorf("counts = %+v, want %+v", got.Counts, want)
	}
	if len(got.Events) != 2 {
		t.Fatalf("events = %+v, want two", got.Events)
	}
	if got.Events[0].Kind != string(app.ClipSent) {
		t.Errorf("events[0].kind = %q, want the newest line first", got.Events[0].Kind)
	}
	if got.Events[0].Label != msgClipEventSent || got.Events[0].Size == "" {
		t.Errorf("events[0] = %+v, want the sent label and a size", got.Events[0])
	}
	// A refused item carries no size: it is never measured, and printing a
	// length for it would be reporting something about it after all.
	if got.Events[1].Kind != string(app.ClipMarked) || got.Events[1].Size != "" {
		t.Errorf("events[1] = %+v, want the marked line with no size", got.Events[1])
	}
	if got.Events[1].Label != msgClipEventMarked {
		t.Errorf("events[1].label = %q, want the marker sentence", got.Events[1].Label)
	}
}

// Neither refusal carries a size, and for different reasons — see
// clipEventSize. An item past the cap is never read out, so the only number
// there is to report is a lower bound, and a lower bound printed as a size is
// a wrong number in the window.
func TestNeitherRefusalCarriesASize(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 8, 21, 15, 0, 0, time.UTC)
	h, _ := clipHandler(app.ClipStatus{
		Available: true, On: true, PeerName: "win-sttm11d02rd", MaxBytes: 64,
		Events: []app.ClipEvent{
			{At: at, Kind: app.ClipTooBig, Bytes: 130},
			{At: at, Kind: app.ClipMarked, Bytes: 0},
		},
	})
	var got clipResponse
	if err := json.Unmarshal(do(t, h, http.MethodGet, "/api/clip", "").Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	for _, e := range got.Events {
		if e.Size != "" {
			t.Errorf("a %s line carries the size %q, want none", e.Kind, e.Size)
		}
		if !isRussian(e.Label) {
			t.Errorf("a %s line has no Russian label", e.Kind)
		}
	}
}

// Every kind the app can report must have a sentence, and an unknown one must
// be visible rather than blank.
func TestEveryKindOfLineHasARussianSentence(t *testing.T) {
	t.Parallel()

	kinds := []app.ClipEventKind{
		app.ClipSent, app.ClipReceived, app.ClipTooBig, app.ClipMarked, app.ClipFailed,
	}
	seen := map[string]bool{}
	for _, kind := range kinds {
		label := clipEventLabel(kind)
		if !isRussian(label) {
			t.Errorf("clipEventLabel(%q) = %q, want Russian", kind, label)
		}
		if seen[label] {
			t.Errorf("clipEventLabel(%q) = %q, which another kind already uses", kind, label)
		}
		seen[label] = true
	}
	if got := clipEventLabel("something new"); got != msgClipEventUnknown {
		t.Errorf("an unrecognised kind = %q, want the honest fallback", got)
	}
}

func TestSwitchingOnAndOff(t *testing.T) {
	t.Parallel()

	h, clip := clipHandler(app.ClipStatus{Available: true})

	rec := do(t, h, http.MethodPost, "/api/clip/on", "")
	if rec.Code != http.StatusOK || clip.turnedOn != 1 {
		t.Fatalf("POST /api/clip/on = %d after %d calls, want 200 and one", rec.Code, clip.turnedOn)
	}
	var answer okMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !answer.OK || answer.Message != msgClipTurnedOn {
		t.Errorf("answer = %+v, want ok and the switched-on sentence", answer)
	}

	rec = do(t, h, http.MethodPost, "/api/clip/off", "")
	if rec.Code != http.StatusOK || clip.turnedOff != 1 {
		t.Fatalf("POST /api/clip/off = %d after %d calls, want 200 and one", rec.Code, clip.turnedOff)
	}
}

// Rule 5: turning it off is one action that cannot fail, even when there was
// nothing there to turn off.
func TestTurningItOffAlwaysSucceeds(t *testing.T) {
	t.Parallel()

	rec := do(t, New(Deps{}), http.MethodPost, "/api/clip/off", "")
	if rec.Code != http.StatusOK {
		t.Errorf("POST /api/clip/off with no clipboard = %d, want 200", rec.Code)
	}
}

func TestSwitchingOnFailsWhenThereIsNoClipboard(t *testing.T) {
	t.Parallel()

	cases := map[string]Deps{
		"never wired up":               {},
		"the clipboard cannot be read": {Clip: &fakeClip{onErr: app.ErrClipUnavailable}},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, New(deps), http.MethodPost, "/api/clip/on", "")
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("POST /api/clip/on = %d, want 503", rec.Code)
			}
			var answer okMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if answer.OK || !isRussian(answer.Message) {
				t.Errorf("answer = %+v, want a refusal in Russian", answer)
			}
		})
	}
}

// TestTheReceivingEndpointTakesOneItemAndSaysWhatItDid is the route the peer's
// instance posts to through the forwarded port.
func TestTheReceivingEndpointTakesOneItemAndSaysWhatItDid(t *testing.T) {
	t.Parallel()

	h, clip := clipHandler(clipOn())
	const item = "текст со второй машины"

	rec := do(t, h, http.MethodPost, "/api/clip/receive", `{"text":"`+item+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/clip/receive = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := clip.arrived(); len(got) != 1 || got[0] != item {
		t.Fatalf("the clipboard got %q, want the one item", got)
	}
	var answer okMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !answer.OK || answer.Message != msgClipReceived {
		t.Errorf("answer = %+v, want ok and the accepted sentence", answer)
	}
	// The answer must not repeat the item back: an endpoint that echoed would
	// be a way to read this machine's clipboard from the loopback.
	if strings.Contains(rec.Body.String(), item) {
		t.Errorf("the answer carries the item back: %s", rec.Body.String())
	}
}

func TestTheReceivingEndpointRefusesWhatItShould(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		deps Deps
		body string
		want int
	}{
		{"no clipboard here", Deps{}, `{"text":"x"}`, http.StatusServiceUnavailable},
		{
			"sharing is switched off",
			Deps{Clip: &fakeClip{putErr: app.ErrClipOff}},
			`{"text":"x"}`, http.StatusServiceUnavailable,
		},
		{
			"the item is past the cap",
			Deps{Clip: &fakeClip{putErr: app.ErrClipTooBig}},
			`{"text":"x"}`, http.StatusRequestEntityTooLarge,
		},
		{
			"the clipboard would not take it",
			Deps{Clip: &fakeClip{putErr: app.ErrClipUnavailable}},
			`{"text":"x"}`, http.StatusInternalServerError,
		},
		{"not JSON", Deps{Clip: &fakeClip{}}, `{`, http.StatusBadRequest},
		{
			"a field this API does not have",
			Deps{Clip: &fakeClip{}},
			`{"text":"x","alsoFiles":true}`, http.StatusBadRequest,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := do(t, New(tc.deps), http.MethodPost, "/api/clip/receive", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("POST /api/clip/receive = %d, want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			var answer errorBody
			if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if !isRussian(answer.Message) {
				t.Errorf("message = %q, want Russian", answer.Message)
			}
		})
	}
}

// The receiving route has its own, larger body limit because it carries an
// item rather than a handful of fields — and it is still a limit.
func TestTheReceivingEndpointHasALimitOfItsOwn(t *testing.T) {
	t.Parallel()

	h, clip := clipHandler(clipOn())

	// Larger than the ordinary limit and smaller than this route's: accepted.
	big := strings.Repeat("я", maxBodyBytes)
	rec := do(t, h, http.MethodPost, "/api/clip/receive", `{"text":"`+big+`"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("an item over the ordinary limit = %d, want 200", rec.Code)
	}
	if got := clip.arrived(); len(got) != 1 {
		t.Errorf("the clipboard got %d items, want one", len(got))
	}

	// Past this route's own limit: refused, and not read.
	huge := strings.Repeat("x", maxClipBodyBytes+1)
	rec = do(t, h, http.MethodPost, "/api/clip/receive", `{"text":"`+huge+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("an item past this route's limit = %d, want 413", rec.Code)
	}
}

// A page in the user's browser must not be able to drive any of these routes,
// and the receiving one least of all.
func TestAWebPageCannotDriveTheSharedClipboard(t *testing.T) {
	t.Parallel()

	h, clip := clipHandler(clipOn())
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/clip", ""},
		{http.MethodPost, "/api/clip/on", ""},
		{http.MethodPost, "/api/clip/off", ""},
		{http.MethodPost, "/api/clip/receive", `{"text":"из браузера"}`},
	} {
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		req.Header.Set("Origin", "https://example.com")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s from a web page = %d, want 403", route.method, route.path, rec.Code)
		}
	}
	if got := clip.arrived(); len(got) != 0 {
		t.Errorf("a web page put %q on the clipboard", got)
	}
	if clip.turnedOn != 0 || clip.turnedOff != 0 {
		t.Error("a web page switched the shared clipboard")
	}
}

func TestTheClipRoutesRefuseTheWrongMethod(t *testing.T) {
	t.Parallel()

	h, _ := clipHandler(clipOn())
	for _, route := range []struct{ method, path string }{
		{http.MethodDelete, "/api/clip"},
		{http.MethodGet, "/api/clip/on"},
		{http.MethodGet, "/api/clip/off"},
		{http.MethodGet, "/api/clip/receive"},
	} {
		rec := do(t, h, route.method, route.path, "")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", route.method, route.path, rec.Code)
		}
	}
}

// Nothing this package serves about the shared clipboard can carry what was
// copied. The gate in tools/gates asserts it about the source; this asserts it
// about the bytes that actually go out.
func TestNothingServedAboutTheClipboardCarriesTheContent(t *testing.T) {
	t.Parallel()

	const secret = "correct-horse-battery-staple"
	h, _ := clipHandler(clipOn())

	if rec := do(t, h, http.MethodPost, "/api/clip/receive", `{"text":"`+secret+`"}`); rec.Code != 200 {
		t.Fatalf("POST /api/clip/receive = %d", rec.Code)
	}
	body := do(t, h, http.MethodGet, "/api/clip", "").Body.String()
	if strings.Contains(body, secret) {
		t.Errorf("GET /api/clip carries the content: %s", body)
	}
}
