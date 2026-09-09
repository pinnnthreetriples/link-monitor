package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
)

// sendTimeout bounds one transfer, whichever path it takes.
//
// It is long because a Taildrop of a film over a slow link legitimately takes
// an hour, and finite because this process has no console and no window: a
// request that hung forever would leave a linkmon.exe in the task list that
// nobody can see and nothing will ever stop.
const sendTimeout = 2 * time.Hour

// reachTimeout bounds the Tailscale ping that decides whether a failure gets
// «Второй компьютер недоступен» or the general one. The user is already waiting
// for an answer at this point, so it is short.
const reachTimeout = 10 * time.Second

// largeFile is the size above which the transfer announces itself before
// starting. Below it the transfer is over before a notification would have
// finished appearing, and two toasts for one small file is noise.
const largeFile = 8 << 20 // 8 MiB

// sendPath is the API route the running instance is handed the file on. It
// already exists and already does the transfer; nothing here reimplements it.
const sendPath = "api/files/send"

// sendMode is the whole of `linkmon -send <file>`: the mode Explorer's
// «Отправить на ПК» starts.
//
// It never takes the single-instance lock. Whether an instance is running is
// not something this process needs a lock to learn — the endpoint record says
// so, and says it with proof — and taking the lock would mean the verb and the
// tray could not coexist, which is the opposite of the point.
//
// Every path out of here ends in exactly one notification. Explorer gives the
// verb no console, so a return code is written to nobody; a silent failure
// would be a file the user believes was sent.
func sendMode(o options) int {
	// Ctrl+C matters when this is run from a terminal for debugging; Explorer
	// sends nothing. Either way the transfer must be interruptible.
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(signalCtx, sendTimeout)
	defer cancel()

	log := slog.Default()
	notifier := ui.NewToastNotifier("")

	path, info, err := checkFile(o.send)
	if err != nil {
		log.Error("the file named on the command line cannot be sent", "err", err)
		tell(notifier, log, noFileNotice(filepath.Base(o.send)))
		return 1
	}
	name := filepath.Base(path)

	if info.Size() >= largeFile {
		tell(notifier, log, startedNotice(name))
	}

	outcome := deliver(ctx, o, path, log)
	tell(notifier, log, noticeFor(outcome, name))
	if outcome == sendDone {
		return 0
	}
	return 1
}

// deliver moves the file and works out which of the three things to say about
// it. The ping that separates "the peer is off" from "something here is wrong"
// is only asked for after a failure — see [classify].
func deliver(ctx context.Context, o options, path string, log *slog.Logger) sendOutcome {
	sendErr := send(ctx, o, path, log)
	if sendErr == nil {
		return sendDone
	}
	log.Error("sending a file from the Explorer menu", "file", filepath.Base(path), "err", sendErr)
	return classify(sendErr, peerReachable(ctx, o, log))
}

// checkFile turns the command line's argument into an absolute path and
// refuses anything that is not a file this program can send.
//
// A folder is refused here rather than at the far end because the message can
// be better: Taildrop's own answer is "... is a directory", and what the user
// needs to hear is that the menu item sends one file at a time.
func checkFile(raw string) (string, os.FileInfo, error) {
	if strings.TrimSpace(raw) == "" {
		return "", nil, errors.New("no file was named")
	}
	path, err := filepath.Abs(raw)
	if err != nil {
		return "", nil, fmt.Errorf("resolving %s: %w", filepath.Base(raw), err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, fmt.Errorf("looking at %s: %w", filepath.Base(path), err)
	}
	if info.IsDir() {
		return "", nil, fmt.Errorf("%s is a folder", filepath.Base(path))
	}
	if !info.Mode().IsRegular() {
		return "", nil, fmt.Errorf("%s is not an ordinary file", filepath.Base(path))
	}
	return path, info, nil
}

// send moves the file, preferring the instance that is already running.
//
// Handing it over matters twice. The transfer then appears in the window's
// Файлы tab, where the user can watch it, and only one process is talking to
// the local Tailscale daemon about it — two copies pushing the same file would
// each report a transfer nobody asked for twice.
func send(ctx context.Context, o options, path string, log *slog.Logger) error {
	rec, err := localendpoint.Find()
	switch {
	case err == nil:
		log.Info("handing the file to the instance that is already running",
			"url", rec.BaseURL, "instance", rec.PID)
		handled, handErr := handOff(ctx, rec, o.peer.TailnetName, path)
		if handled {
			return handErr
		}
		// The record was confirmed a moment ago, so this is the instance
		// shutting down between the check and the request. Doing the job here
		// is then exactly right, and there is no longer anybody to race.
		log.Warn("the running instance stopped answering; sending the file from here",
			"err", handErr)
	case localendpoint.NotRunning(err):
		// The user right-clicked a file. Making them start the program first
		// would be a worse answer than doing the work.
		log.Info("no instance is running; sending the file from here", "reason", err)
	default:
		// A broken environment or an unreadable directory. The file still has
		// to go somewhere, and this process can send it.
		log.Warn("could not look for a running instance; sending the file from here", "err", err)
	}
	return sendDirect(ctx, o, path)
}

// handOff posts the file to the running instance.
//
// It reports whether the instance answered at all. A refused connection means
// the endpoint went away between the record being confirmed and the request
// being made, and the caller should do the work itself; anything the instance
// actually said — including a refusal — is its answer and is returned as one.
func handOff(ctx context.Context, rec localendpoint.Record, peer, path string) (bool, error) {
	body, err := json.Marshal(map[string]string{"path": path, "peer": peer})
	if err != nil {
		return true, fmt.Errorf("building the hand-off request: %w", err)
	}
	url := strings.TrimSuffix(rec.BaseURL, "/") + "/" + sendPath

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return true, fmt.Errorf("building the hand-off request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := handOffClient().Do(req)
	if err != nil {
		return false, fmt.Errorf("asking the running instance to send the file: %w", err)
	}
	defer func() {
		// The body has been read to the cap below; closing it cannot lose
		// anything, and there is nothing to tell a user with no console.
		_ = resp.Body.Close()
	}()

	return true, readHandOff(resp)
}

// handOffClient is the client the hand-off uses.
//
// No timeout of its own: the transfer takes as long as the file takes, and the
// caller's context already bounds it at [sendTimeout]. Redirects are refused
// because the address came out of a file on disk — the record is validated as
// loopback before it is used, and a redirect would be a way around that check.
func handOffClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("refusing to follow a redirect away from the loopback endpoint")
		},
	}
}

// readHandOff turns the instance's answer into an error or a nil.
//
// The API answers every send with {"ok": ..., "message": "..."} and one Russian
// sentence. The sentence is not shown to the user from here — this process
// raises its own notification, and two differently worded messages about one
// transfer would be worse than one — but it goes in the log, because it is the
// only thing the instance has to say about why.
func readHandOff(resp *http.Response) error {
	var answer struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	// 64 KiB is what the API accepts as a request and far more than it ever
	// answers with; a body larger than that is not this API on the far end.
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return fmt.Errorf("reading the running instance's answer: %w", err)
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return fmt.Errorf("the running instance answered with %s, which is not this API: %w",
			resp.Status, err)
	}
	if resp.StatusCode != http.StatusOK || !answer.OK {
		return fmt.Errorf("the running instance refused the file: %s: %s", resp.Status, answer.Message)
	}
	return nil
}

// sendDirect does the transfer in this process, which is what happens when no
// instance is running. It talks to the local Tailscale daemon exactly as the
// running instance would.
func sendDirect(ctx context.Context, o options, path string) error {
	if err := tailscale.New().SendFile(ctx, path, o.peer.TailnetName); err != nil {
		return fmt.Errorf("sending %s over Taildrop: %w", filepath.Base(path), err)
	}
	return nil
}

// peerReachable asks Tailscale whether the peer answers, which is what turns a
// bare failure into «Второй компьютер недоступен».
//
// A ping that cannot even be attempted — no daemon, no tailnet — counts as
// unreachable, because from the user's side it is: the file is not going
// anywhere, and the peer is the thing they can check.
func peerReachable(ctx context.Context, o options, log *slog.Logger) bool {
	ctx, cancel := context.WithTimeout(ctx, reachTimeout)
	defer cancel()

	if _, err := tailscale.New().PeerReachable(ctx, o.peer.Addr); err != nil {
		log.Info("the peer did not answer a Tailscale ping", "peer", o.peer.TailnetName, "err", err)
		return false
	}
	return true
}

// tell raises one notification and waits for it.
//
// Waiting is deliberate: this process is about to exit, and a toast whose
// PowerShell has not finished starting dies with its parent. A failure to show
// it is logged and nothing more — there is nowhere else left to say anything.
//
// The context is its own rather than the transfer's. The transfer's may well be
// cancelled or expired by the time there is something to report, and that is
// precisely the moment the user most needs to be told; [noticeTimeout] keeps it
// from becoming a process nothing will stop.
func tell(notifier ui.Notifier, log *slog.Logger, note ui.Notification) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()

	if err := notifier.Notify(ctx, note); err != nil {
		log.Error("showing a notification", "title", note.Title, "err", err)
	}
}
