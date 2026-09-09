package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/core/foldersync"
)

// fakeSync stands in for the shared folder.
type fakeSync struct {
	mu     sync.Mutex
	status app.SyncStatus
	asked  int
}

func (f *fakeSync) On() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.status.On
}

func (f *fakeSync) Status() app.SyncStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.status
}

func (f *fakeSync) SyncNow() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.asked++
}

func (f *fakeSync) asks() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.asked
}

// syncedStatus is a shared folder that has run one pass and done a bit of
// everything: a file each way, a conflict, and one of each kind of skip.
func syncedStatus() app.SyncStatus {
	return app.SyncStatus{
		On:         true,
		Folder:     `C:\Users\pnj\Shared`,
		PeerFolder: `C:\Users\user\LinkMonitor\shared`,
		PeerName:   "win-sttm11d02rd",
		MaxBytes:   100 << 20,
		HasRun:     true,
		At:         time.Date(2026, 9, 8, 21, 4, 0, 0, time.UTC),
		Took:       1200 * time.Millisecond,
		Moved: []app.SyncMoved{
			{Name: "notes.txt", Size: 2048, Way: foldersync.ToPeer},
			{Name: "plan.md", Size: 1024, Way: foldersync.ToUs},
			{
				Name: "заметки.txt", Size: 4096, Way: foldersync.ToUs,
				Conflict: true, Saved: "заметки (с win-sttm11d02rd, 21-04).txt",
			},
		},
		Skipped: []app.SyncSkipped{
			{Name: "big.iso", Size: 5 << 30, Why: foldersync.WhyTooBig},
			{Name: "report.docx", Size: 100, Why: foldersync.WhyBusy},
			{Name: "notes.txt", Size: 2048, Why: foldersync.WhyUnresolved},
			{Name: "../evil.dll", Size: 9, Why: foldersync.WhyUnsafeName},
			{Name: "mystery.txt", Size: 1, Why: foldersync.Why("something new")},
		},
	}
}

// syncHandler is a handler with one shared folder in the state given.
func syncHandler(st app.SyncStatus) (http.Handler, *fakeSync) {
	folder := &fakeSync{status: st}
	return New(Deps{Sync: folder}), folder
}

func TestTheSharedFolderIsReportedInFull(t *testing.T) {
	t.Parallel()

	h, _ := syncHandler(syncedStatus())
	body := do(t, h, http.MethodGet, "/api/sync", "").Body.String()

	var got syncResponse
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}

	switch {
	case !got.On:
		t.Error("on = false for a folder that is switched on")
	case got.Folder != `C:\Users\pnj\Shared`:
		t.Errorf("folder = %q", got.Folder)
	case got.PeerFolder != `C:\Users\user\LinkMonitor\shared`:
		t.Errorf("peerFolder = %q", got.PeerFolder)
	case got.Peer != "win-sttm11d02rd":
		t.Errorf("peer = %q", got.Peer)
	case got.MaxSize != "100,0 МБ":
		t.Errorf("maxSize = %q, want the cap in Russian units", got.MaxSize)
	case got.At != "2026-09-08T21:04:00Z":
		t.Errorf("at = %q", got.At)
	case got.TookMs != 1200:
		t.Errorf("tookMs = %d", got.TookMs)
	case len(got.Moved) != 3:
		t.Errorf("moved = %+v, want three", got.Moved)
	case len(got.Skipped) != 5:
		t.Errorf("skipped = %+v, want five", got.Skipped)
	}
}

// Rule 6: a conflict must be impossible to miss. It comes first in the
// sentence at the top of the section, ahead of anything that merely moved.
func TestAConflictIsWhatTheSectionSaysFirst(t *testing.T) {
	t.Parallel()

	h, _ := syncHandler(syncedStatus())
	var got syncResponse
	decodeSync(t, h, &got)

	if !strings.HasPrefix(got.Message, "Расхождений: 1.") {
		t.Errorf("message = %q, want it to lead with the conflict", got.Message)
	}
	if !isRussian(got.Message) {
		t.Errorf("message = %q, want Russian", got.Message)
	}
	// The copy's own name is on the wire, so the user can go and find it.
	var conflict SyncMovedDTO
	for _, m := range got.Moved {
		if m.Conflict {
			conflict = m
		}
	}
	if conflict.Saved != "заметки (с win-sttm11d02rd, 21-04).txt" {
		t.Errorf("saved = %q, want the conflict copy named", conflict.Saved)
	}
}

// Rule 1's sentence. It is served rather than written into the page, because
// the promise and the code that keeps it belong together.
func TestTheGuaranteeAboutDeletionsIsAlwaysSaid(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		st   app.SyncStatus
	}{
		{"switched on", syncedStatus()},
		{"switched off", app.SyncStatus{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h, _ := syncHandler(tc.st)
			var got syncResponse
			decodeSync(t, h, &got)

			if !strings.Contains(got.Note, "Удаление не передаётся") {
				t.Errorf("note = %q, want the guarantee said out loud", got.Note)
			}
			if !isRussian(got.Note) {
				t.Errorf("note = %q, want Russian", got.Note)
			}
		})
	}
}

func TestEverySkipCarriesAReasonAPersonCanRead(t *testing.T) {
	t.Parallel()

	h, _ := syncHandler(syncedStatus())
	var got syncResponse
	decodeSync(t, h, &got)

	want := map[string]string{
		"too_big":       msgSkipTooBig,
		"busy":          msgSkipBusy,
		"unresolved":    msgSkipUnresolved,
		"unsafe_name":   msgSkipUnsafeName,
		"something new": msgSkipUnknown,
	}
	for _, skip := range got.Skipped {
		if skip.Reason != want[skip.Why] {
			t.Errorf("%s (%s) reads %q, want %q", skip.Name, skip.Why, skip.Reason, want[skip.Why])
		}
		if !isRussian(skip.Reason) {
			t.Errorf("%s reads %q, want Russian", skip.Name, skip.Reason)
		}
	}
	if len(got.Skipped) != len(want) {
		t.Errorf("skipped %d kinds, want one of each of %d", len(got.Skipped), len(want))
	}
}

// A folder nobody chose is off, and says so with the flag that switches it on.
// It is not an error and does not answer like one.
func TestASharedFolderNobodyChoseAnswersThatItIsOff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		deps Deps
	}{
		{"never wired up", Deps{}},
		{"wired but with no folder", Deps{Sync: &fakeSync{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res := do(t, New(tc.deps), http.MethodGet, "/api/sync", "")
			if res.Code != http.StatusOK {
				t.Errorf("status = %d, want 200: off is not a fault", res.Code)
			}
			var got syncResponse
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if got.On {
				t.Error("on = true")
			}
			if !strings.Contains(got.Message, "-sync-folder") {
				t.Errorf("message = %q, want it to name the flag that switches it on", got.Message)
			}
			if got.Moved == nil || got.Skipped == nil {
				t.Errorf("moved = %v, skipped = %v; want empty lists, not null",
					got.Moved, got.Skipped)
			}
		})
	}
}

func TestTheSectionSaysNothingHasRunYet(t *testing.T) {
	t.Parallel()

	h, _ := syncHandler(app.SyncStatus{On: true, Folder: `C:\Shared`})
	var got syncResponse
	decodeSync(t, h, &got)

	if got.Message != msgSyncNotYet {
		t.Errorf("message = %q, want %q", got.Message, msgSyncNotYet)
	}
	if got.At != "" {
		t.Errorf("at = %q, want it empty until a pass has finished", got.At)
	}
}

func TestTheSectionSaysWhatOnePassCameTo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		st   app.SyncStatus
		want string
	}{
		{
			"nothing to do",
			app.SyncStatus{On: true, HasRun: true},
			msgSyncSame,
		},
		{
			"two files moved",
			app.SyncStatus{On: true, HasRun: true, Moved: []app.SyncMoved{{}, {}}},
			"Перенесено файлов: 2.",
		},
		{
			"nothing moved but something skipped",
			app.SyncStatus{On: true, HasRun: true, Skipped: []app.SyncSkipped{{}}},
			"Ничего не перенесено, пропущено файлов: 1.",
		},
		{
			"a failure outranks the tally",
			app.SyncStatus{
				On: true, HasRun: true, Err: "Не удалось прочитать общую папку на этой машине.",
				Moved: []app.SyncMoved{{}},
			},
			"Не удалось прочитать общую папку на этой машине.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			h, _ := syncHandler(tc.st)
			var got syncResponse
			decodeSync(t, h, &got)

			if got.Message != tc.want {
				t.Errorf("message = %q, want %q", got.Message, tc.want)
			}
			// The window must not have to read the sentence to know whether the
			// pass went wrong, so the answer says so in a field of its own.
			if got.Failed != (tc.st.Err != "") {
				t.Errorf("failed = %v, want %v", got.Failed, tc.st.Err != "")
			}
		})
	}
}

func TestALostHistoryReachesTheWindow(t *testing.T) {
	t.Parallel()

	st := syncedStatus()
	st.HistoryLost = true
	h, _ := syncHandler(st)

	var got syncResponse
	decodeSync(t, h, &got)
	if !got.HistoryLost {
		t.Error("historyLost = false, want the window able to say so")
	}
}

func TestAskingForAPassNudgesTheFolder(t *testing.T) {
	t.Parallel()

	h, folder := syncHandler(syncedStatus())

	res := do(t, h, http.MethodPost, "/api/sync/run", "")
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if folder.asks() != 1 {
		t.Errorf("the folder was asked %d times, want once", folder.asks())
	}

	var got okMessage
	if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if !got.OK || got.Message != msgSyncStarted {
		t.Errorf("body = %+v, want the pass acknowledged", got)
	}
}

// The endpoint answers at once rather than waiting out the pass: a request
// that hangs for minutes is one the window has already given up on.
func TestAskingForAPassDoesNotWaitForIt(t *testing.T) {
	t.Parallel()

	h, folder := syncHandler(syncedStatus())
	done := make(chan struct{})
	go func() {
		defer close(done)
		do(t, h, http.MethodPost, "/api/sync/run", "")
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("POST /api/sync/run did not answer")
	}
	if folder.asks() != 1 {
		t.Errorf("the folder was asked %d times, want once", folder.asks())
	}
}

func TestAskingForAPassOnAFolderThatIsOffIsRefusedPolitely(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		deps Deps
	}{
		{"never wired up", Deps{}},
		{"wired but with no folder", Deps{Sync: &fakeSync{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			res := do(t, New(tc.deps), http.MethodPost, "/api/sync/run", "")
			if res.Code != http.StatusServiceUnavailable {
				t.Errorf("status = %d, want 503", res.Code)
			}
			var got okMessage
			if err := json.Unmarshal(res.Body.Bytes(), &got); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			if got.OK || !isRussian(got.Message) {
				t.Errorf("body = %+v, want a Russian refusal", got)
			}
		})
	}
}

func TestTheSyncRoutesRefuseTheWrongMethod(t *testing.T) {
	t.Parallel()

	h, _ := syncHandler(syncedStatus())
	for _, tc := range []struct{ method, path string }{
		{http.MethodDelete, "/api/sync"},
		{http.MethodGet, "/api/sync/run"},
	} {
		res := do(t, h, tc.method, tc.path, "")
		if res.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, res.Code)
		}
	}
}

// decodeSync reads GET /api/sync into dst.
func decodeSync(t *testing.T, h http.Handler, dst *syncResponse) {
	t.Helper()

	body := do(t, h, http.MethodGet, "/api/sync", "").Body.Bytes()
	if err := json.Unmarshal(body, dst); err != nil {
		t.Fatalf("decoding %q: %v", body, err)
	}
}
