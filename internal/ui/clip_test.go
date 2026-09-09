package ui

import (
	"testing"
)

// withClip wires a fake shared clipboard into the harness's Config.
func withClip(c *fakeClip) func(*Config) {
	return func(cfg *Config) { cfg.Clip = c }
}

// lastClipTick returns the tick mark the controller set on the shared-clipboard
// item most recently.
func lastClipTick(t *testing.T, tray *fakeTray) bool {
	t.Helper()

	got := tray.clipTicks()
	if len(got) == 0 {
		t.Fatal("the controller never set the shared clipboard's tick mark")
	}
	return got[len(got)-1]
}

func TestTheClipboardTickStartsFromTheFeatureRatherThanFromAGuess(t *testing.T) {
	for _, on := range []bool{true, false} {
		t.Run(map[bool]string{true: "already on", false: "off"}[on], func(t *testing.T) {
			clip := newFakeClip(on)
			h := newHarness(t, nil, withClip(clip))

			h.c.syncClip()

			if got := lastClipTick(t, h.tray); got != on {
				t.Errorf("the tick mark is %t, want %t", got, on)
			}
			if _, _, asks := clip.counts(); asks != 1 {
				t.Errorf("asked the feature %d times, want exactly 1", asks)
			}
		})
	}
}

func TestClickingTheClipboardItemTurnsItOnAndSaysSo(t *testing.T) {
	clip := newFakeClip(false)
	h := newHarness(t, nil, withClip(clip))

	h.c.toggleClip(t.Context())

	ons, offs, _ := clip.counts()
	if ons != 1 || offs != 0 {
		t.Errorf("TurnOn=%d TurnOff=%d, want 1 and 0", ons, offs)
	}
	if !lastClipTick(t, h.tray) {
		t.Error("sharing was switched on but the item was left unticked")
	}
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want 1: switching a menu item leaves nothing on screen",
			h.notifier.count())
	}
	if got := h.notifier.shown[0]; got.Title != clipOnTitle {
		t.Errorf("notification = %q, want %q", got.Title, clipOnTitle)
	}
}

func TestClickingTheClipboardItemAgainTurnsItOffAndSaysSo(t *testing.T) {
	clip := newFakeClip(true)
	h := newHarness(t, nil, withClip(clip))

	h.c.toggleClip(t.Context())

	ons, offs, _ := clip.counts()
	if ons != 0 || offs != 1 {
		t.Errorf("TurnOn=%d TurnOff=%d, want 0 and 1", ons, offs)
	}
	if lastClipTick(t, h.tray) {
		t.Error("sharing was switched off but the item was left ticked")
	}
	if h.notifier.count() != 1 {
		t.Fatalf("showed %d notifications, want 1", h.notifier.count())
	}
	if got := h.notifier.shown[0]; got.Title != clipOffTitle {
		t.Errorf("notification = %q, want %q", got.Title, clipOffTitle)
	}
}

// TestTurningTheSharedClipboardOffCannotFail is the one property this item has
// that the other two do not. Off is the urgent direction: a clipboard that
// cannot be reached, a peer that has gone away, a machine in whatever state —
// none of it may stand between the click and sharing stopping.
func TestTurningTheSharedClipboardOffCannotFail(t *testing.T) {
	clip := newFakeClip(true)
	// The clipboard itself is out of reach, which is what would refuse a
	// TurnOn. Switching off must not consult it at all.
	clip.onErr = errFake
	h := newHarness(t, nil, withClip(clip))

	h.c.toggleClip(t.Context())

	ons, offs, _ := clip.counts()
	if offs != 1 {
		t.Errorf("TurnOff was called %d times, want exactly 1", offs)
	}
	if ons != 0 {
		t.Errorf("switching off called TurnOn %d times: it must ask nothing", ons)
	}
	if clip.On() {
		t.Error("the shared clipboard is still on after one click on the switch")
	}
	if lastClipTick(t, h.tray) {
		t.Error("the item is still ticked after sharing was switched off")
	}
	if got := h.notifier.shown[0]; got.Title != clipOffTitle {
		t.Errorf("notification = %q, want the «выключен» pair", got.Title)
	}
}

// TestTheClipboardToggleAsksTheFeatureRatherThanItsOwnTickMark is why On() is
// read on every click: the window and the local API carry the same switch, so
// between the tray coming up and the click it may have moved.
func TestTheClipboardToggleAsksTheFeatureRatherThanItsOwnTickMark(t *testing.T) {
	clip := newFakeClip(false)
	h := newHarness(t, nil, withClip(clip))

	h.c.syncClip() // the tray comes up: unticked

	clip.set(true) // somebody switched it on in the window

	h.c.toggleClip(t.Context())

	ons, offs, _ := clip.counts()
	if ons != 0 || offs != 1 {
		t.Errorf("TurnOn=%d TurnOff=%d: the click acted on a stale tick mark", ons, offs)
	}
	if lastClipTick(t, h.tray) {
		t.Error("the tick mark still claims the shared clipboard is on")
	}
}

// TestTheClipboardTickIsReReadRatherThanComputed goes one further: the tick the
// controller draws after a click comes from asking the feature again, not from
// negating what it believed a moment ago.
func TestTheClipboardTickIsReReadRatherThanComputed(t *testing.T) {
	clip := newFakeClip(false)
	h := newHarness(t, nil, withClip(clip))

	h.c.toggleClip(t.Context()) // switches on, then re-reads

	if _, _, asks := clip.counts(); asks != 2 {
		t.Errorf("asked On() %d times, want 2: once to choose the direction, "+
			"once to draw the tick", asks)
	}
	if !lastClipTick(t, h.tray) {
		t.Error("the tick mark does not agree with the feature")
	}
}

func TestAFailureToTurnTheClipboardOnIsReportedAndTheTickStaysHonest(t *testing.T) {
	for _, tc := range []struct {
		name string
		clip *fakeClip
	}{
		{
			name: "the clipboard is out of reach",
			clip: &fakeClip{available: true, onErr: errFake},
		},
		{
			// Unreachable through the menu — the item is left out — but these
			// methods are also reachable directly, so the answer has to be the
			// honest one here too.
			name: "there is no clipboard to share at all",
			clip: &fakeClip{available: false, onErr: errFake},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, nil, withClip(tc.clip))

			h.c.toggleClip(t.Context())

			if h.notifier.count() != 1 {
				t.Fatalf("showed %d notifications, want 1: a silent failure is the one "+
					"unacceptable result", h.notifier.count())
			}
			if got := h.notifier.shown[0]; got.Title != clipFailTitle {
				t.Errorf("notification = %q, want %q", got.Title, clipFailTitle)
			}
			if lastClipTick(t, h.tray) {
				t.Error("the item was ticked although sharing never started")
			}
		})
	}
}

func TestTheClipboardItemIsHarmlessWhenNothingIsWiredToIt(t *testing.T) {
	h := newHarness(t, nil)

	h.c.syncClip()              // must not panic
	h.c.toggleClip(t.Context()) // must not panic

	if got := h.tray.clipTicks(); len(got) != 0 {
		t.Errorf("the tick mark was set to %v with no shared clipboard behind it", got)
	}
	if h.notifier.count() != 0 {
		t.Error("a menu item nobody can use notified the user")
	}
}

// TestTheMenuCarriesTheClipboardItemOnlyWhenThereIsOneToShare covers the choice
// documented on Config.Clip: an unavailable clipboard is left out of the menu
// rather than drawn greyed, because Available never changes while the program
// runs and a permanently dead control is worse than none.
func TestTheMenuCarriesTheClipboardItemOnlyWhenThereIsOneToShare(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts []func(*Config)
		want bool
	}{
		{
			name: "with an available clipboard",
			opts: []func(*Config){withClip(newFakeClip(false))},
			want: true,
		},
		{
			name: "with the feature unwired by -no-clipboard",
			opts: []func(*Config){withClip(&fakeClip{available: false})},
			want: false,
		},
		{name: "with no clipboard at all", want: false},
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

			if got := tray.askedFor().clip; got != tc.want {
				t.Errorf("AddMenu asked for the shared-clipboard item = %t, want %t", got, tc.want)
			}

			tray.quit <- struct{}{}
			assertStopped(t, done)
		})
	}
}

// TestClickingTheClipboardItemThroughTheRunningTray drives the whole path the
// user takes. Nobody clicked anything and nobody saw a menu: the tray is the
// fake, and the click arrives on the channel the real systray would have sent
// it on.
func TestClickingTheClipboardItemThroughTheRunningTray(t *testing.T) {
	clip := newFakeClip(false)
	tray := newFakeTray()
	notifier := &fakeNotifier{}
	cfg := config(statusSource(nil), notifier, &fakeOpener{})
	cfg.Clip = clip

	done := runIn(t.Context(), cfg, tray)
	waitFor(t, "the tick mark to be read from the feature", func() bool {
		return len(tray.clipTicks()) == 1
	})

	tray.clip <- struct{}{}
	waitFor(t, "sharing to be switched on", func() bool {
		ons, _, _ := clip.counts()
		return ons == 1
	})
	waitFor(t, "the user to be told", func() bool { return notifier.count() == 1 })

	if !lastClipTick(t, tray) {
		t.Error("sharing is on but the menu still shows the item unticked")
	}

	// And off again, which is the click that matters.
	tray.clip <- struct{}{}
	waitFor(t, "sharing to be switched off", func() bool {
		_, offs, _ := clip.counts()
		return offs == 1
	})
	waitFor(t, "the tick mark to come off", func() bool {
		ticks := tray.clipTicks()
		return len(ticks) > 0 && !ticks[len(ticks)-1]
	})

	tray.quit <- struct{}{}
	assertStopped(t, done)
}

// TestTheRealTrayToleratesAMenuWithNoClipboardItem covers the contract the tray
// shell promises the controller, which calls SetClipChecked as soon as it has
// the menu whatever the menu turned out to contain.
func TestTheRealTrayToleratesAMenuWithNoClipboardItem(t *testing.T) {
	t.Parallel()

	tray := newSystrayTray()

	// No menu has been built, so there is no item to tick. Neither call may
	// reach into the tray library, and neither may panic.
	tray.SetClipChecked(true)
	tray.SetClipChecked(false)
}

// fakeCheckable is a menu item's tick mark without a menu behind it, so that
// the one decision in setChecked can be checked on a machine with no desktop
// session: a boolean the wrong way round would put a tick beside a feature
// that is off, which is the worst thing either of these two items could do.
type fakeCheckable struct {
	checks   int
	unchecks int
}

func (f *fakeCheckable) Check() { f.checks++ }

func (f *fakeCheckable) Uncheck() { f.unchecks++ }

func TestATickMeansOnAndNoTickMeansOff(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		checked                  bool
		wantChecks, wantUnchecks int
	}{
		{checked: true, wantChecks: 1, wantUnchecks: 0},
		{checked: false, wantChecks: 0, wantUnchecks: 1},
	} {
		item := &fakeCheckable{}

		setChecked(item, tc.checked)

		if item.checks != tc.wantChecks || item.unchecks != tc.wantUnchecks {
			t.Errorf("setChecked(item, %t) checked %d times and unchecked %d, want %d and %d",
				tc.checked, item.checks, item.unchecks, tc.wantChecks, tc.wantUnchecks)
		}
	}
}

func TestTheClipboardItemsRussianIsWhatTheUserWillRead(t *testing.T) {
	t.Parallel()

	if menuClip != "Общий буфер обмена" {
		t.Errorf("menuClip = %q", menuClip)
	}
	if got := clipNotice(true); got.Title != clipOnTitle || got.Body != clipOnBody {
		t.Errorf("clipNotice(true) = %+v, want the «включён» pair", got)
	}
	if got := clipNotice(false); got.Title != clipOffTitle || got.Body != clipOffBody {
		t.Errorf("clipNotice(false) = %+v, want the «выключен» pair", got)
	}
	if got := clipFailure(); got.Title != clipFailTitle || got.Body != clipFailBody {
		t.Errorf("clipFailure() = %+v, want the «недоступен» pair", got)
	}
}

// TestTheTwoSwitchesDescribeTheSameStateInTheSameWords keeps the tray's wording
// pinned to what internal/app/httpapi/text.go answers the window for the same
// three states — msgClipTurnedOn, msgClipTurnedOff and msgClipNoClipboard. The
// strings are written out rather than imported: internal/ui does not depend on
// an HTTP transport, so this test is the seam that notices if one side is
// reworded and the other is not.
func TestTheTwoSwitchesDescribeTheSameStateInTheSameWords(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ name, got, want string }{
		{
			name: "switched on",
			got:  clipOnTitle + ". " + clipOnBody,
			want: "Общий буфер обмена включён. Скопированный здесь текст будет " +
				"появляться в буфере обмена второй машины. " +
				"Передаётся только текст, и нигде не сохраняется.",
		},
		{
			name: "switched off",
			got:  clipOffTitle + ". " + clipOffBody,
			want: "Общий буфер обмена выключен. Программа больше не читает буфер " +
				"обмена и ничего никуда не передаёт.",
		},
		{
			// The one difference from the window's msgClipNoClipboard is the
			// capital the body needs to start a sentence of its own, and the
			// second sentence, which is clipsharetext.go's own explanation of
			// this state put into the tray's «ты».
			name: "no clipboard to reach",
			got:  clipFailTitle + ": " + clipFailBody,
			want: "Общий буфер обмена недоступен: Программа не может обратиться " +
				"к буферу обмена этой машины. Общий буфер работает только в сеансе, " +
				"в котором ты работаешь за компьютером: разблокируй экран и попробуй ещё раз.",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("the tray says\n  %q\nwhere the window says\n  %q", tc.got, tc.want)
			}
		})
	}
}
