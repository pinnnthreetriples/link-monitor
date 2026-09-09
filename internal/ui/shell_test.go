package ui

import (
	"testing"
)

// withShellMenu wires a fake Explorer item into the harness's Config.
func withShellMenu(m *fakeShellMenu) func(*Config) {
	return func(cfg *Config) { cfg.ShellMenu = m }
}

// lastCheck returns the tick mark the controller set most recently.
func lastCheck(t *testing.T, tray *fakeTray) bool {
	t.Helper()

	got := tray.checks()
	if len(got) == 0 {
		t.Fatal("the controller never set the Explorer item's tick mark")
	}
	return got[len(got)-1]
}

func TestTheTickMarkStartsFromTheRegistryRatherThanFromAGuess(t *testing.T) {
	for _, installed := range []bool{true, false} {
		t.Run(map[bool]string{true: "already installed", false: "not installed"}[installed], func(t *testing.T) {
			shell := &fakeShellMenu{installed: installed}
			h := newHarness(t, nil, withShellMenu(shell))

			h.c.syncShell()

			if got := lastCheck(t, h.tray); got != installed {
				t.Errorf("the tick mark is %t, want %t", got, installed)
			}
			if _, _, asks := shell.counts(); asks != 1 {
				t.Errorf("asked the registry %d times, want exactly 1", asks)
			}
		})
	}
}

func TestClickingTheItemInstallsItAndSaysSo(t *testing.T) {
	shell := &fakeShellMenu{installed: false}
	h := newHarness(t, nil, withShellMenu(shell))

	h.c.toggleShell(t.Context())

	installs, removes, _ := shell.counts()
	if installs != 1 || removes != 0 {
		t.Errorf("installs=%d removes=%d, want 1 and 0", installs, removes)
	}
	if !lastCheck(t, h.tray) {
		t.Error("the item was installed but left unticked")
	}
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want 1: switching a menu item leaves nothing on screen",
			h.notifier.count())
	}
	if got := h.notifier.shown[0]; got.Title != shellOnTitle {
		t.Errorf("notification = %q, want %q", got.Title, shellOnTitle)
	}
}

func TestClickingTheItemAgainRemovesItAndSaysSo(t *testing.T) {
	shell := &fakeShellMenu{installed: true}
	h := newHarness(t, nil, withShellMenu(shell))

	h.c.toggleShell(t.Context())

	installs, removes, _ := shell.counts()
	if installs != 0 || removes != 1 {
		t.Errorf("installs=%d removes=%d, want 0 and 1", installs, removes)
	}
	if lastCheck(t, h.tray) {
		t.Error("the item was removed but left ticked")
	}
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want 1", h.notifier.count())
	}
	if got := h.notifier.shown[0]; got.Title != shellOffTitle {
		t.Errorf("notification = %q, want %q", got.Title, shellOffTitle)
	}
}

// TestTheToggleAsksTheRegistryRatherThanItsOwnTickMark is the reason the
// registry is read on every click: between the tray coming up and the click,
// the item may have been switched by the exe's -menu flag or by hand.
func TestTheToggleAsksTheRegistryRatherThanItsOwnTickMark(t *testing.T) {
	shell := &fakeShellMenu{installed: false}
	h := newHarness(t, nil, withShellMenu(shell))

	h.c.syncShell() // the tray comes up: unticked

	shell.mu.Lock()
	shell.installed = true // somebody installed it behind the program's back
	shell.mu.Unlock()

	h.c.toggleShell(t.Context())

	installs, removes, _ := shell.counts()
	if installs != 0 || removes != 1 {
		t.Errorf("installs=%d removes=%d: the click acted on a stale tick mark", installs, removes)
	}
	if lastCheck(t, h.tray) {
		t.Error("the tick mark still claims the item is installed")
	}
}

func TestAFailureToSwitchTheItemIsReportedAndTheTickMarkStaysHonest(t *testing.T) {
	for _, tc := range []struct {
		name      string
		shell     *fakeShellMenu
		wantCheck bool
	}{
		{
			name:      "the install failed",
			shell:     &fakeShellMenu{installed: false, installErr: errFake},
			wantCheck: false,
		},
		{
			name:      "the removal failed",
			shell:     &fakeShellMenu{installed: true, removeErr: errFake},
			wantCheck: true,
		},
		{
			name:      "the registry could not even be read",
			shell:     &fakeShellMenu{askErr: errFake},
			wantCheck: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, withShellMenu(tc.shell))

			h.c.toggleShell(t.Context())

			if h.notifier.count() != 1 {
				t.Fatalf("showed %d notifications, want 1: a silent failure is the one unacceptable result",
					h.notifier.count())
			}
			if got := h.notifier.shown[0]; got.Title != shellFailTitle {
				t.Errorf("notification = %q, want %q", got.Title, shellFailTitle)
			}

			// A read that failed leaves the tick mark where it was drawn,
			// because nothing better is known; a failed write re-reads and
			// reports what the registry really says.
			if tc.shell.askErr != nil {
				if got := h.tray.checks(); len(got) != 0 {
					t.Errorf("the tick mark was set to %v from an unreadable registry", got)
				}
				return
			}
			if got := lastCheck(t, h.tray); got != tc.wantCheck {
				t.Errorf("the tick mark is %t, want %t — it must agree with the registry", got, tc.wantCheck)
			}
		})
	}
}

func TestTheItemIsHarmlessWhenNothingIsWiredToIt(t *testing.T) {
	h := newHarness(t, nil)

	h.c.syncShell()              // must not panic
	h.c.toggleShell(t.Context()) // must not panic

	if got := h.tray.checks(); len(got) != 0 {
		t.Errorf("the tick mark was set to %v with no Explorer item behind it", got)
	}
	if h.notifier.count() != 0 {
		t.Error("a menu item nobody can use notified the user")
	}
}

func TestTheMenuCarriesTheItemOnlyWhenThereIsOneToSwitch(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []func(*Config)
		want bool
	}{
		{name: "with an Explorer item", opts: []func(*Config){withShellMenu(&fakeShellMenu{})}, want: true},
		{name: "without one", want: false},
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

			if got := tray.askedFor().shell; got != tc.want {
				t.Errorf("AddMenu asked for the Explorer item = %t, want %t", got, tc.want)
			}

			tray.quit <- struct{}{}
			assertStopped(t, done)
		})
	}
}

// TestClickingTheItemThroughTheRunningTray drives the whole path the user
// takes: the tray is up, the menu is built, and the click arrives on the
// channel the real systray would have sent it on.
func TestClickingTheItemThroughTheRunningTray(t *testing.T) {
	shell := &fakeShellMenu{installed: false}
	tray := newFakeTray()
	notifier := &fakeNotifier{}
	cfg := config(statusSource(nil), notifier, &fakeOpener{})
	cfg.ShellMenu = shell

	done := runIn(t.Context(), cfg, tray)
	waitFor(t, "the tick mark to be read from the registry", func() bool {
		return len(tray.checks()) == 1
	})

	tray.shell <- struct{}{}
	waitFor(t, "the item to be installed", func() bool {
		installs, _, _ := shell.counts()
		return installs == 1
	})
	waitFor(t, "the user to be told", func() bool { return notifier.count() == 1 })

	if !lastCheck(t, tray) {
		t.Error("the item was installed but the menu still shows it unticked")
	}

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

// TestTheRealTrayToleratesAMenuWithNoExplorerItem covers the contract the tray
// shell promises the controller: a tray built without the checkable item still
// answers SetShellChecked, because the controller calls it unconditionally as
// soon as the menu arrives.
func TestTheRealTrayToleratesAMenuWithNoExplorerItem(t *testing.T) {
	t.Parallel()

	tray := newSystrayTray()
	if tray.icon == nil {
		t.Fatal("newSystrayTray built a tray with no channel for a click on the icon")
	}

	// No menu has been built, so there is no item to tick. Neither call may
	// reach into the tray library, and neither may panic.
	tray.SetShellChecked(true)
	tray.SetShellChecked(false)
}

func TestTheItemsRussianIsWhatTheUserWillRead(t *testing.T) {
	t.Parallel()

	if menuShell != "Пункт «Отправить на ПК» в Проводнике" {
		t.Errorf("menuShell = %q", menuShell)
	}
	if got := ShellMenuNotice(true); got.Title != shellOnTitle || got.Body != shellOnBody {
		t.Errorf("ShellMenuNotice(true) = %+v, want the «пункт добавлен» pair", got)
	}
	if got := ShellMenuNotice(false); got.Title != shellOffTitle || got.Body != shellOffBody {
		t.Errorf("ShellMenuNotice(false) = %+v, want the «пункт убран» pair", got)
	}
}
