package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// errSomethingElse is a failure with no meaning of its own, for the cases where
// the classification has to fall back on the ping.
var errSomethingElse = errors.New("the local Tailscale daemon is not answering")

func TestClassifySeparatesASleepingPeerFromABrokenMachine(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		sendErr   error
		reachable bool
		want      sendOutcome
	}{
		{
			name:      "the file went",
			sendErr:   nil,
			reachable: true,
			want:      sendDone,
		},
		{
			name:      "the adapter already established the peer is silent",
			sendErr:   fmt.Errorf("pinging: %w", tailscale.ErrPeerUnreachable),
			reachable: true, // not consulted: the error is conclusive
			want:      sendUnreachable,
		},
		{
			name:      "the peer is not in the tailnet at all",
			sendErr:   fmt.Errorf("taildrop target: %w", tailscale.ErrPeerNotFound),
			reachable: true,
			want:      sendUnreachable,
		},
		{
			name:      "an opaque failure and a peer that does not answer",
			sendErr:   errSomethingElse,
			reachable: false,
			want:      sendUnreachable,
		},
		{
			name:      "an opaque failure and a peer that does answer",
			sendErr:   errSomethingElse,
			reachable: true,
			want:      sendFailed,
		},
		{
			name: "the API's own refusal, with the peer up",
			sendErr: errors.New(
				"the running instance refused the file: 502 Bad Gateway: Не удалось отправить файл"),
			reachable: true,
			want:      sendFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := classify(tc.sendErr, tc.reachable); got != tc.want {
				t.Errorf("classify(%v, reachable=%t) = %d, want %d",
					tc.sendErr, tc.reachable, got, tc.want)
			}
		})
	}
}

// TestClassifyLooksThroughTheDomainsOwnTimeout is the case a real switched-off
// peer produces: the Tailscale adapter wraps ErrPeerUnreachable inside
// *core.TimeoutError, and the sentinel has to still be found through it.
func TestClassifyLooksThroughTheDomainsOwnTimeout(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("%w: %w", &core.TimeoutError{Addr: "100.127.188.87"},
		tailscale.ErrPeerUnreachable)
	if got := classify(wrapped, true); got != sendUnreachable {
		t.Errorf("classify(%v) = %d, want sendUnreachable", wrapped, got)
	}
}

func TestEachOutcomeGetsItsOwnRussianNotification(t *testing.T) {
	t.Parallel()

	const name = "Отчёт за сентябрь.docx"
	seen := map[string]sendOutcome{}

	for outcome, wantTitle := range map[sendOutcome]string{
		sendDone:        sendOKTitle,
		sendUnreachable: sendUnreachableTitle,
		sendFailed:      sendFailedTitle,
	} {
		note := noticeFor(outcome, name)
		if note.Title != wantTitle {
			t.Errorf("noticeFor(%d).Title = %q, want %q", outcome, note.Title, wantTitle)
		}
		if !strings.Contains(note.Body, name) {
			t.Errorf("noticeFor(%d).Body = %q, does not name the file", outcome, note.Body)
		}
		if previous, ok := seen[note.Body]; ok {
			t.Errorf("outcomes %d and %d say the same thing: %q", previous, outcome, note.Body)
		}
		seen[note.Body] = outcome
	}
}

// TestNoNotificationCarriesTheFullPath is the standing check on what goes into
// the Action Centre, where notifications are kept and can be read later: the
// file's name, never the folders above it.
func TestNoNotificationCarriesTheFullPath(t *testing.T) {
	t.Parallel()

	const full = `C:\Users\pnj\Личное\Документы\паспорт.pdf`
	name := filepath.Base(full)

	notes := []struct {
		what string
		body string
	}{
		{what: "success", body: noticeFor(sendDone, name).Body},
		{what: "an unreachable peer", body: noticeFor(sendUnreachable, name).Body},
		{what: "a failure", body: noticeFor(sendFailed, name).Body},
		{what: "the start of a large transfer", body: startedNotice(name).Body},
		{what: "a path that is not a file", body: noFileNotice(name).Body},
	}
	for _, note := range notes {
		if strings.Contains(note.body, `Личное`) || strings.Contains(note.body, `C:\`) {
			t.Errorf("the notification for %s carries the folders above the file: %q", note.what, note.body)
		}
	}
}

func TestCheckFileAcceptsAFileAndRefusesEverythingElse(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	file := filepath.Join(dir, "заметки & план (копия).txt")
	if err := os.WriteFile(file, []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}

	t.Run("a real file", func(t *testing.T) {
		t.Parallel()

		path, info, err := checkFile(file)
		if err != nil {
			t.Fatalf("checkFile(%q) = %v, want the file", file, err)
		}
		if path != file {
			t.Errorf("checkFile returned %q, want %q", path, file)
		}
		if info.Size() != 5 {
			t.Errorf("size = %d, want 5", info.Size())
		}
	})

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "a folder", path: dir},
		{name: "a file that is not there", path: filepath.Join(dir, "нет такого.txt")},
		{name: "nothing at all", path: ""},
		{name: "whitespace", path: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, _, err := checkFile(tc.path); err == nil {
				t.Errorf("checkFile(%q) = nil, want a refusal", tc.path)
			}
		})
	}
}

// TestCheckFileResolvesARelativePath matters because Explorer runs the verb
// with the clicked file's folder as the working directory: a bare name has to
// become the file it means. It cannot run in parallel — it changes the process's
// working directory.
func TestCheckFileResolvesARelativePath(t *testing.T) {
	dir := t.TempDir()
	const name = "заметки & план (копия).txt"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("hello"), 0o600); err != nil {
		t.Fatalf("writing the test file: %v", err)
	}
	t.Chdir(dir)

	path, _, err := checkFile(name)
	if err != nil {
		t.Fatalf("checkFile(%q) = %v, want the file", name, err)
	}
	if !filepath.IsAbs(path) {
		t.Errorf("checkFile returned %q, want an absolute path", path)
	}
	if filepath.Base(path) != name {
		t.Errorf("checkFile returned %q, want it to name %q", path, name)
	}
}

func TestReadHandOffBelievesOnlyThisAPIsOwnYes(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		status   int
		body     string
		wantErr  bool
		wantText string
	}{
		{
			name:   "the instance took the file",
			status: http.StatusOK,
			body:   `{"ok":true,"message":"Файл отправлен"}`,
		},
		{
			name:     "the instance could not send it",
			status:   http.StatusBadGateway,
			body:     `{"ok":false,"message":"Не удалось отправить файл"}`,
			wantErr:  true,
			wantText: "Не удалось отправить файл",
		},
		{
			name:    "a 200 that still says no",
			status:  http.StatusOK,
			body:    `{"ok":false,"message":"Нужен путь к файлу"}`,
			wantErr: true,
		},
		{
			name:     "something that is not this API",
			status:   http.StatusOK,
			body:     "<html>hello</html>",
			wantErr:  true,
			wantText: "not this API",
		},
		{
			name:    "an empty answer",
			status:  http.StatusOK,
			body:    "",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{
				StatusCode: tc.status,
				Status:     fmt.Sprintf("%d test", tc.status),
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			err := readHandOff(resp)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("readHandOff() = %v, want no error", err)
				}
				return
			}
			if err == nil {
				t.Fatal("readHandOff() = nil, want an error")
			}
			if tc.wantText != "" && !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("readHandOff() = %v, want it to mention %q", err, tc.wantText)
			}
		})
	}
}

func TestHandOffPostsTheFileToTheRunningInstance(t *testing.T) {
	t.Parallel()

	type request struct {
		Path string `json:"path"`
		Peer string `json:"peer"`
	}
	var got request
	var gotPath, gotType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decoding the request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if _, err := fmt.Fprint(w, `{"ok":true,"message":"Файл отправлен"}`); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	const file = `C:\Мои файлы\счёт & акт (копия).pdf`
	rec := localendpoint.Record{BaseURL: server.URL + "/"}

	handled, err := handOff(t.Context(), rec, "win-sttm11d02rd", file)
	if !handled || err != nil {
		t.Fatalf("handOff() = (%t, %v), want (true, nil)", handled, err)
	}
	if gotPath != "/api/files/send" {
		t.Errorf("posted to %q, want /api/files/send", gotPath)
	}
	if !strings.HasPrefix(gotType, "application/json") {
		t.Errorf("Content-Type = %q, want JSON", gotType)
	}
	if got.Path != file {
		t.Errorf("sent path %q, want %q — the path must survive verbatim", got.Path, file)
	}
	if got.Peer != "win-sttm11d02rd" {
		t.Errorf("sent peer %q, want the configured one", got.Peer)
	}
}

// TestHandOffReportsAnInstanceThatWentAway is the race the endpoint record
// cannot close: it was confirmed a moment ago and the instance has since quit.
// The caller has to be told to do the job itself rather than to report a
// failure.
func TestHandOffReportsAnInstanceThatWentAway(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close()

	handled, err := handOff(t.Context(), localendpoint.Record{BaseURL: url + "/"},
		"win-sttm11d02rd", `C:\tmp\notes.txt`)
	if handled {
		t.Fatalf("handOff() = (true, %v), want handled=false so the caller sends it itself", err)
	}
	if err == nil {
		t.Error("handOff() reported no error for a listener that is gone")
	}
}

// TestHandOffRefusesToFollowARedirect keeps the loopback check meaningful. The
// base URL is validated as loopback before it is used; a redirect the client
// followed would be a way straight past that.
func TestHandOffRefusesToFollowARedirect(t *testing.T) {
	t.Parallel()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("the hand-off followed a redirect")
		if _, err := fmt.Fprint(w, `{"ok":true}`); err != nil {
			t.Errorf("writing the answer: %v", err)
		}
	}))
	t.Cleanup(elsewhere.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/api/files/send", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	handled, err := handOff(t.Context(), localendpoint.Record{BaseURL: server.URL},
		"win-sttm11d02rd", `C:\tmp\notes.txt`)
	if err == nil {
		t.Fatalf("handOff() = (%t, nil), want the redirect refused", handled)
	}
}

func TestRunMenuActionRefusesAnActionItDoesNotKnow(t *testing.T) {
	t.Parallel()

	if _, err := runMenuAction(nil, "уберииии"); err == nil {
		t.Fatal("runMenuAction accepted an unknown action")
	}
	if _, err := runMenuAction(nil, ""); err == nil {
		t.Fatal("runMenuAction accepted an empty action")
	}
}
