// Command linkmon watches the Tailscale + SSH link between this machine and its
// peer, says why it broke, and repairs what it can.
//
// This file is wiring and nothing else: it reads configuration, builds the
// adapters, hands them to the application layer, mounts the UI and the API on a
// loopback listener, opens the program's own window on it, and runs the tray
// until the user quits. Every decision worth arguing about lives in
// internal/core.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/sshx"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/winlock"
	"github.com/pinnnthreetriples/link-monitor/internal/adapters/winsvc"
	"github.com/pinnnthreetriples/link-monitor/internal/app"
	"github.com/pinnnthreetriples/link-monitor/internal/app/httpapi"
	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui"
)

// shutdownGrace is how long the HTTP server is given to finish in-flight
// requests once the tray has gone. It is short on purpose: nothing this server
// does is worth making the user wait for a tray icon to disappear.
const shutdownGrace = 3 * time.Second

// noticeTimeout bounds one notification or one hand-off to the browser. It
// matches what internal/ui allows itself: long enough for PowerShell to start,
// short enough that a hung one cannot hold up shutdown.
const noticeTimeout = 15 * time.Second

// lockName identifies this program's single-instance lock. It is not a path
// and not a secret — it only has to be the same string in every copy of the
// program and a string no other program would pick.
const lockName = "link-monitor-single-instance"

// Every string in this block is read by the user and is therefore in Russian.
const (
	runningTitle = "Link Monitor уже запущен"
	runningBody  = "Значок программы — в области уведомлений, справа на панели задач. " +
		"Нажми на него, чтобы открыть окно."

	strayTitle = "Link Monitor не запустился"
	strayBody  = "Программа не поняла часть командной строки: «%s». Скорее всего путь " +
		"с пробелом не взят в кавычки — из-за этого он обрезается, а все настройки " +
		"после него теряются. Поправь ярлык и запусти снова."
)

func main() {
	os.Exit(start())
}

// start is main with the exit code returned rather than taken, so that the
// deferred release of the single-instance lock still runs on the way out.
func start() int {
	o := parseOptions()

	if stray := strayArgs(); len(stray) > 0 {
		// Not a warning: a value the user did not intend is worse than not
		// starting, and everything after the stray argument was dropped.
		reportStrayArguments(stray)
		return 2
	}

	// Two modes that are not the tray, and neither of them wants the
	// single-instance lock: -send hands its file to the instance that holds it,
	// and -menu only writes a registry key. Both return before anything else
	// in this function happens.
	switch {
	case o.send != "":
		return sendMode(o)
	case o.menu != "":
		return menuMode(o.menu)
	}

	release, err := winlock.Acquire(lockName)
	switch {
	case errors.Is(err, winlock.ErrAlreadyRunning):
		// Not a failure: the user launched the exe a second time. Say where
		// the copy that is already running can be found, and leave quietly.
		reportAlreadyRunning()
		return 0
	case err != nil:
		// The lock could not be taken for some other reason. Refusing to run
		// would be worse than the two tray icons the lock guards against: the
		// program still works, it just no longer knows that it is alone.
		slog.Warn("taking the single-instance lock", "err", err)
		release = nil
	}
	defer func() {
		if release == nil {
			return
		}
		if err := release(); err != nil {
			slog.Warn("releasing the single-instance lock", "err", err)
		}
	}()

	if err := run(o); err != nil {
		// The GUI build has no console, so a failure that reaches here is
		// written where it can still be read after the fact.
		slog.Error("link monitor stopped", "err", err)
		return 1
	}
	return 0
}

// reportAlreadyRunning tells the second copy's user where the first one is.
// A -H=windowsgui build has no console, so a notification is the only way to
// say anything at all.
func reportAlreadyRunning() {
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()

	note := ui.Notification{Title: runningTitle, Body: runningBody}
	if err := ui.NewToastNotifier("").Notify(ctx, note); err != nil {
		slog.Warn("telling the user the program is already running", "err", err)
	}
}

// strayArgs reports the arguments left over after flag parsing, which are
// always a mistake and never harmless.
//
// Go's flag package stops at the first argument that is not a flag and hands
// the rest back untouched, so one unquoted path with a space in it — the most
// likely mistake on Windows, and the one that produced
// `-sync-folder C:\Users\pnj\Общая` out of `...\Общая папка` — both truncates
// that value and silently discards every flag after it. Refusing to start is
// the only honest answer: the alternative is a program that runs happily while
// synchronising a folder the user did not name.
func strayArgs() []string { return flag.Args() }

// reportStrayArguments tells the user which argument was not understood. A
// -H=windowsgui build has no console, so a notification is the only way to say
// anything at all.
func reportStrayArguments(stray []string) {
	ctx, cancel := context.WithTimeout(context.Background(), noticeTimeout)
	defer cancel()

	body := fmt.Sprintf(strayBody, strings.Join(stray, " "))
	note := ui.Notification{Title: strayTitle, Body: body}
	if err := ui.NewToastNotifier("").Notify(ctx, note); err != nil {
		slog.Warn("telling the user an argument was not understood", "err", err)
	}
	slog.Error("refusing to start: arguments left over after the flags", "stray", stray)
}

// options is everything the program can be told from outside. The defaults
// describe the two machines this was written for; flags exist so it can be
// pointed at another pair without a rebuild.
type options struct {
	local    core.Machine
	peer     core.Machine
	keyPath  string
	addr     string
	interval time.Duration
	inbox    string
	// browser forces the behaviour this program had before it grew a window
	// of its own: the UI opens in the user's browser. It is the escape hatch
	// if the window misbehaves on a machine, and it is how the UI gets
	// debugged with real developer tools — the embedded control has none.
	browser bool
	// send is one file to send to the peer, after which the program exits. It
	// is what Explorer's «Отправить на ПК» passes, and it is the whole of that
	// mode's input — see send.go.
	send string
	// menu switches Explorer's «Отправить на ПК» item from a command line:
	// install, remove or status. The tray's own checkable item is how a person
	// does it; this is for an installer script and for looking. See menu.go.
	menu string
}

func parseOptions() options {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	o := options{
		local: core.Machine{
			WindowsName: "DESKTOP-L9DJSE9",
			TailnetName: "workspace-claude-pc",
			Addr:        "100.124.47.73",
			User:        "pnj",
		},
		peer: core.Machine{
			WindowsName: "WIN-STTM11D02RD",
			TailnetName: "win-sttm11d02rd",
			Addr:        "100.127.188.87",
			User:        "user",
		},
	}
	flag.StringVar(&o.local.Addr, "local-addr", o.local.Addr, "адрес этой машины в Tailscale")
	flag.StringVar(&o.local.TailnetName, "local-name", o.local.TailnetName, "имя этой машины в tailnet")
	flag.StringVar(&o.peer.Addr, "peer-addr", o.peer.Addr, "адрес второй машины в Tailscale")
	flag.StringVar(&o.peer.TailnetName, "peer-name", o.peer.TailnetName, "имя второй машины в tailnet")
	flag.StringVar(&o.peer.User, "peer-user", o.peer.User, "учётная запись для входа по SSH")
	flag.StringVar(&o.keyPath, "key", filepath.Join(home, ".ssh", "id_ed25519"), "закрытый ключ SSH")
	flag.StringVar(&o.addr, "listen", "127.0.0.1:0", "адрес интерфейса; порт 0 — выбрать свободный")
	flag.DurationVar(&o.interval, "interval", app.DefaultInterval, "как часто проверять связь")
	flag.StringVar(&o.inbox, "inbox", filepath.Join(home, "Downloads"), "куда складывать принятые файлы")
	flag.BoolVar(&o.browser, "browser", false, "открывать интерфейс в браузере, а не в своём окне")
	flag.StringVar(&o.send, "send", "", "отправить этот файл на второй компьютер и выйти")
	flag.StringVar(&o.menu, "menu", "",
		"пункт «Отправить на ПК» в Проводнике: install — добавить, remove — убрать, status — проверить")
	flag.Parse()
	return o
}

func run(o options) error {
	// Ctrl+C matters for `go run`; the tray's «Выход» is the normal path. Both
	// end up cancelling the same context, which is what every goroutine below
	// — poller, window, tray — is watching.
	signalCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSignals()
	ctx, cancel := context.WithCancel(signalCtx)
	defer cancel()

	application, closeApp := startApp(ctx, o)
	defer closeApp()

	listener, err := httpapi.Listen(o.addr)
	if err != nil {
		return fmt.Errorf("opening the interface's port: %w", err)
	}
	uiURL := "http://" + listener.Addr().String() + "/"
	server, serveErr := serve(listener, mount(application, o.peer.TailnetName))

	// The port was chosen by the operating system a moment ago, so nothing
	// outside this process knows it yet. Publishing has to happen after the
	// bind and before anything is invited to look.
	unpublish := publishEndpoint(uiURL, slog.Default())
	defer unpublish()

	notifier := ui.NewToastNotifier("")
	notes := &notices{notifier: notifier, log: slog.Default()}
	win, stopWindow := startWindow(ctx, o, uiURL, notes)

	trayErr := ui.Run(ctx, ui.Config{
		Source:    trayStatuses(application),
		Actions:   ui.ActionsFunc(func(context.Context) error { application.Poller.CheckNow(); return nil }),
		Notifier:  notifier,
		Window:    win,
		ShellMenu: explorerMenu(slog.Default()),
		Quick:     wireQuick(o),
		Clip:      application.Clip,
		UIURL:     uiURL,
	})

	// «Выход» — and a cancelled context — must take the window with them. The
	// reverse is not true: the window's X button only hides it, so a program
	// that outlives a closed window is the point rather than a leak.
	cancel()
	stopWindow()
	notes.wait()

	return finish(server, serveErr, trayErr)
}

// startApp builds the application layer over the real adapters and starts its
// poller. The returned stop closes the application and then the SSH client,
// in that order: the poller must stop asking before the connection goes.
func startApp(ctx context.Context, o options) (*app.App, func()) {
	tailnet := tailscale.New()
	services := winsvc.New()
	ssh := sshx.NewLazy(sshx.Config{
		Addr:    o.peer.Addr,
		User:    o.peer.User,
		KeyPath: o.keyPath,
		// The peer's host key is unknown on a fresh machine and there is no
		// out-of-band channel to learn it from; a changed key is still refused.
		TrustOnFirstUse: true,
	})

	folder := wireFolder(o, ssh)
	clip := wireClip(o, ssh)

	application := app.New(ctx, app.Config{
		Local:    o.local,
		Peer:     o.peer,
		Interval: o.interval,
		InboxDir: o.inbox,
		Sync:     folder.cfg,
		Clip:     clip.cfg,
	}, app.Deps{
		Tailnet:   tailnet,
		Remote:    ssh,
		Local:     services,
		Services:  services,
		Shell:     ssh,
		Files:     tailnet,
		Forwarder: ssh,
		Publisher: tailnet,
		Peers:     tailnet,
		LinkCtl:   tailnet,
		SyncHere:  folder.here,
		SyncPeer:  folder.peer,
		SyncState: folder.state,
		ClipHere:  clip.here,
		ClipPeer:  clip.peer,
	})
	application.Start()

	return application, func() {
		if err := application.Close(); err != nil {
			slog.Warn("closing the application", "err", err)
		}
		if err := ssh.Close(); err != nil {
			slog.Warn("closing the ssh client", "err", err)
		}
	}
}

// trayStatuses adapts the poller's fan-out to what the tray wants. The tray
// does real work per status — repaint, and sometimes spawn a toast — so its
// channel must never be the thing that stalls the poller; the app layer's
// Subscribe is expected to drop rather than block, and this only translates.
func trayStatuses(a *app.App) ui.Source {
	return ui.SourceFunc(func(ctx context.Context) <-chan ui.Status {
		results, unsubscribe := a.Poller.Subscribe()
		out := make(chan ui.Status, 1)
		go func() {
			defer close(out)
			defer unsubscribe()
			for {
				select {
				case <-ctx.Done():
					return
				case r, ok := <-results:
					if !ok {
						return
					}
					select {
					case out <- ui.StatusOf(r.Snapshot):
					case <-ctx.Done():
						return
					}
				}
			}
		}()
		return out
	})
}
