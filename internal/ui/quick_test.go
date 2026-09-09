package ui

import (
	"slices"
	"testing"
	"time"
)

// withQuick wires a fake set of quick actions into the harness's Config.
func withQuick(q *fakeQuick) func(*Config) {
	return func(cfg *Config) { cfg.Quick = q }
}

// aQuick is a fake with a shared folder configured, which is the ordinary case
// on the machines this program runs on.
func aQuick() *fakeQuick {
	return &fakeQuick{folder: `C:\Users\pnj\Общая папка`}
}

func TestEachQuickItemAsksForItsOwnAction(t *testing.T) {
	for _, tc := range []struct {
		name string
		//nolint:revive // the harness is the only argument worth naming here.
		click func(*harness)
		want  string
	}{
		{name: "the terminal", click: func(h *harness) { h.c.openTerminal(t.Context()) }, want: "terminal"},
		{
			name:  "the shared folder",
			click: func(h *harness) { h.c.openSharedFolder(t.Context()) },
			want:  "folder",
		},
		{
			name:  "the peer's desktop",
			click: func(h *harness) { h.c.openPeerDesktop(t.Context()) },
			want:  "desktop",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quick := aQuick()
			h := newHarness(t, nil, withQuick(quick))

			tc.click(h)

			if got := quick.asked(); !slices.Equal(got, []string{tc.want}) {
				t.Errorf("the click asked for %q, want exactly %q", got, tc.want)
			}
			// A window is about to appear. Saying so as well would be noise.
			if h.notifier.count() != 0 {
				t.Errorf("a successful quick action notified the user %d times", h.notifier.count())
			}
		})
	}
}

func TestAQuickActionThatFailedSaysSoInRussian(t *testing.T) {
	for _, tc := range []struct {
		name  string
		click func(*harness)
		title string
	}{
		{
			name:  "the terminal",
			click: func(h *harness) { h.c.openTerminal(t.Context()) },
			title: quickTermFailTitle,
		},
		{
			name:  "the shared folder",
			click: func(h *harness) { h.c.openSharedFolder(t.Context()) },
			title: quickFolderFailTitle,
		},
		{
			name:  "the peer's desktop",
			click: func(h *harness) { h.c.openPeerDesktop(t.Context()) },
			title: quickDesktopFailTitle,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			quick := aQuick()
			quick.err = errFake
			h := newHarness(t, nil, withQuick(quick))

			tc.click(h)

			// The one unacceptable outcome is a click that does nothing: there
			// is no console in this build and no window necessarily open.
			if h.notifier.count() != 1 {
				t.Fatalf("showed %d notifications, want exactly 1", h.notifier.count())
			}
			if got := h.notifier.shown[0]; got.Title != tc.title {
				t.Errorf("notification title = %q, want %q", got.Title, tc.title)
			}
			if got := h.notifier.shown[0].Body; got == "" {
				t.Error("the notification says what failed but not what to do about it")
			}
		})
	}
}

func TestAFolderNobodyConfiguredSaysSoRatherThanOpeningSomethingElse(t *testing.T) {
	quick := &fakeQuick{folder: "   "}
	h := newHarness(t, nil, withQuick(quick))

	h.c.openSharedFolder(t.Context())

	if got := quick.asked(); len(got) != 0 {
		t.Errorf("the tray opened %q with no folder configured", got)
	}
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want the one that says the folder is not set up",
			h.notifier.count())
	}
	if got := h.notifier.shown[0].Title; got != quickNoFolderTitle {
		t.Errorf("notification title = %q, want %q", got, quickNoFolderTitle)
	}
}

func TestAQuickActionIsBoundedByTheTraysOwnTimeout(t *testing.T) {
	quick := aQuick()
	// The action never finishes on its own. Without a deadline the menu would
	// wedge here, which is what the tray's timeout exists to prevent.
	quick.waitFor = make(chan struct{})
	h := newHarness(t, nil, withQuick(quick), func(cfg *Config) {
		cfg.Timeout = 10 * time.Millisecond
	})

	h.c.openTerminal(t.Context())

	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want the one saying the terminal did not open",
			h.notifier.count())
	}
	if got := h.notifier.shown[0].Title; got != quickTermFailTitle {
		t.Errorf("notification title = %q, want %q", got, quickTermFailTitle)
	}
}

func TestTheQuickItemsAreHarmlessWhenNothingIsWiredToThem(t *testing.T) {
	h := newHarness(t, nil)

	// None of these can happen through the menu — the block is left out when
	// Config.Quick is nil — but none of them may panic either.
	h.c.openTerminal(t.Context())
	h.c.openSharedFolder(t.Context())
	h.c.openPeerDesktop(t.Context())

	if h.notifier.count() != 0 {
		t.Errorf("menu items nobody can use notified the user %d times", h.notifier.count())
	}
}

func TestTheMenuCarriesTheQuickBlockOnlyWhenThereIsSomethingBehindIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []func(*Config)
		want bool
	}{
		{name: "with quick actions", opts: []func(*Config){withQuick(aQuick())}, want: true},
		{name: "without them", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tray := newFakeTray()
			cfg := config(statusSource(nil), &fakeNotifier{}, &fakeOpener{})
			for _, opt := range tc.opts {
				opt(&cfg)
			}

			done := runIn(t.Context(), cfg, tray)
			waitFor(t, "the menu to be built", func() bool {
				tray.mu.Lock()
				defer tray.mu.Unlock()
				return tray.menus == 1
			})

			if got := tray.askedFor().quick; got != tc.want {
				t.Errorf("AddMenu asked for the quick actions = %t, want %t", got, tc.want)
			}

			tray.quit <- struct{}{}
			assertStopped(t, done)
		})
	}
}

// TestClickingAQuickItemThroughTheRunningTray drives the whole path a user
// takes: the tray is up, the menu is built, and the click arrives on the
// channel the real systray would have sent it on. It is the closest this can
// get to a click on a tray icon, which no test can perform.
func TestClickingAQuickItemThroughTheRunningTray(t *testing.T) {
	quick := aQuick()
	tray := newFakeTray()
	cfg := config(statusSource(nil), &fakeNotifier{}, &fakeOpener{})
	cfg.Quick = quick

	done := runIn(t.Context(), cfg, tray)
	waitFor(t, "the menu to be built", func() bool {
		tray.mu.Lock()
		defer tray.mu.Unlock()
		return tray.menus == 1
	})

	for _, click := range []struct {
		ch   chan struct{}
		want string
	}{
		{ch: tray.terminal, want: "terminal"},
		{ch: tray.folder, want: "folder"},
		{ch: tray.desktop, want: "desktop"},
	} {
		click.ch <- struct{}{}
		waitFor(t, "the tray to ask for "+click.want, func() bool {
			return slices.Contains(quick.asked(), click.want)
		})
	}

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

func TestTheQuickItemsRussianIsWhatTheUserWillRead(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ got, want string }{
		{got: menuTerminal, want: "Открыть терминал на ПК"},
		{got: menuFolder, want: "Открыть общую папку"},
		{got: menuDesktop, want: "Открыть рабочий стол ПК"},
	} {
		if tc.got != tc.want {
			t.Errorf("menu label = %q, want %q", tc.got, tc.want)
		}
	}
	// Every hint and every failure sentence is read by the user too, so none
	// of them may be left empty by an edit that only touched the labels.
	for name, text := range map[string]string{
		"hintTerminal":          hintTerminal,
		"hintFolder":            hintFolder,
		"hintDesktop":           hintDesktop,
		"quickTermFailBody":     quickTermFailBody,
		"quickNoFolderBody":     quickNoFolderBody,
		"quickFolderFailBody":   quickFolderFailBody,
		"quickDesktopFailBody":  quickDesktopFailBody,
		"quickNoFolderTitle":    quickNoFolderTitle,
		"quickFolderFailTitle":  quickFolderFailTitle,
		"quickDesktopFailTitle": quickDesktopFailTitle,
	} {
		if text == "" {
			t.Errorf("%s is empty", name)
		}
	}
}
