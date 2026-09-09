package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// on switches sharing on and fails the test if it could not be.
func on(t *testing.T, clip *Clip) {
	t.Helper()

	if err := clip.TurnOn(); err != nil {
		t.Fatalf("TurnOn() = %v", err)
	}
}

// TestNothingIsSharedUntilTheUserSwitchesItOn is rule 1, and it is the first
// test here because it outranks the feature.
func TestNothingIsSharedUntilTheUserSwitchesItOn(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	if clip.On() {
		t.Fatal("the shared clipboard is on before anybody switched it on")
	}

	here.copyText("что-то скопировали")
	clip.tick(context.Background())
	clip.tick(context.Background())
	if got := there.received(); len(got) != 0 {
		t.Fatalf("the peer got %q with sharing off", got)
	}
	if here.looks != 0 {
		t.Errorf("the clipboard was read %d times with sharing off, want 0", here.looks)
	}

	on(t, clip)
	here.copyText("а это уже можно")
	clip.tick(context.Background())
	if got := there.received(); len(got) != 1 || got[0] != "а это уже можно" {
		t.Errorf("the peer got %q, want the one item copied after switching on", got)
	}
}

// Switching the feature on must not send what was already on the clipboard:
// that was copied before the user turned anything on.
func TestSwitchingOnDoesNotShareWhatWasAlreadyOnTheClipboard(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	here.copyText("пароль, скопированный десять минут назад")

	on(t, clip)
	clip.tick(context.Background())
	clip.tick(context.Background())

	if got := there.received(); len(got) != 0 {
		t.Errorf("the peer got %q, want nothing that predates the switch", got)
	}
}

// TestAnItemFromThePeerDoesNotBounceBack is the echo problem, from both sides
// of the defence.
func TestAnItemFromThePeerDoesNotBounceBack(t *testing.T) {
	t.Parallel()

	const arrived = "строка со второй машины"

	t.Run("the baseline moves with the write, so nothing is even read", func(t *testing.T) {
		t.Parallel()

		clip, here, there := clipUnderTest(0)
		on(t, clip)
		if err := clip.Receive([]byte(arrived)); err != nil {
			t.Fatalf("Receive() = %v", err)
		}
		if got := here.delivered(); len(got) != 1 || got[0] != arrived {
			t.Fatalf("this machine's clipboard holds %q, want the arriving item", got)
		}
		before := here.looks
		clip.tick(context.Background())
		if here.looks != before {
			t.Error("the loop read the clipboard after planting an item; the baseline did not move")
		}
		if got := there.received(); len(got) != 0 {
			t.Fatalf("the item bounced straight back: %q", got)
		}
	})

	t.Run("and the fingerprint holds when something else moves the number", func(t *testing.T) {
		t.Parallel()

		clip, here, there := clipUnderTest(0)
		on(t, clip)
		if err := clip.Receive([]byte(arrived)); err != nil {
			t.Fatalf("Receive() = %v", err)
		}
		// Another application touched the clipboard without changing what is
		// on it, so the loop does read — and must recognise its own item.
		here.bump()
		clip.tick(context.Background())
		if got := there.received(); len(got) != 0 {
			t.Fatalf("the item bounced back on a second look: %q", got)
		}
	})

	t.Run("and the next thing the user copies still travels", func(t *testing.T) {
		t.Parallel()

		clip, here, there := clipUnderTest(0)
		on(t, clip)
		if err := clip.Receive([]byte(arrived)); err != nil {
			t.Fatalf("Receive() = %v", err)
		}
		here.copyText("что-то своё")
		clip.tick(context.Background())
		if got := there.received(); len(got) != 1 || got[0] != "что-то своё" {
			t.Errorf("the peer got %q, want the user's own next copy", got)
		}
	})
}

// The same item offered twice goes once: the peer already has it.
func TestTheSameItemIsNotSentTwice(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)

	here.copyText("одно и то же")
	clip.tick(context.Background())
	here.bump()
	clip.tick(context.Background())

	if got := there.received(); len(got) != 1 {
		t.Errorf("the peer got %q, want it once", got)
	}
	if got := clip.Status().Counts.Sent; got != 1 {
		t.Errorf("Counts.Sent = %d, want 1", got)
	}
}

// TestAnItemPastTheCapIsSkippedAndCounted is rule 2: skipped, and visible.
func TestAnItemPastTheCapIsSkippedAndCounted(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(1024)
	on(t, clip)

	here.copyBig(4096)
	clip.tick(context.Background())

	if got := there.received(); len(got) != 0 {
		t.Fatalf("an oversized item was sent: %q", got)
	}
	st := clip.Status()
	if st.Counts.TooBig != 1 {
		t.Errorf("Counts.TooBig = %d, want 1", st.Counts.TooBig)
	}
	if len(st.Events) != 1 || st.Events[0].Kind != ClipTooBig || st.Events[0].Bytes != 4096 {
		t.Errorf("Events = %+v, want one too_big of 4096 bytes", st.Events)
	}
	if st.Err != "" {
		t.Errorf("Err = %q — the cap doing its job is not a failure", st.Err)
	}
}

// TestAnItemMarkedDoNotRecordIsNotTransmitted is rule 3, and the count is what
// lets the user see the rule working.
func TestAnItemMarkedDoNotRecordIsNotTransmitted(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)

	here.copyMarked()
	clip.tick(context.Background())

	if got := there.received(); len(got) != 0 {
		t.Fatalf("an item marked do-not-record was sent: %q", got)
	}
	st := clip.Status()
	if st.Counts.Marked != 1 {
		t.Errorf("Counts.Marked = %d, want 1", st.Counts.Marked)
	}
	if len(st.Events) != 1 || st.Events[0].Kind != ClipMarked {
		t.Fatalf("Events = %+v, want one marked", st.Events)
	}
	// Not even its size: nothing about a refused item is reported.
	if st.Events[0].Bytes != 0 {
		t.Errorf("Events[0].Bytes = %d for a marked item, want 0", st.Events[0].Bytes)
	}
	if st.Err != "" {
		t.Errorf("Err = %q — honouring the marker is not a failure", st.Err)
	}
}

// A file or a picture is counted and no more. Rule 2 carries text only, and a
// line for every screenshot would bury the two refusals that matter.
func TestAFileOrPictureIsCountedAndSaysNothingElse(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)

	here.copyFile()
	clip.tick(context.Background())

	if got := there.received(); len(got) != 0 {
		t.Fatalf("something that is not text was sent: %q", got)
	}
	st := clip.Status()
	if st.Counts.NotText != 1 {
		t.Errorf("Counts.NotText = %d, want 1", st.Counts.NotText)
	}
	if len(st.Events) != 0 {
		t.Errorf("Events = %+v, want none for a picture", st.Events)
	}
}

// An empty clipboard — what a password manager leaves behind when it clears
// itself — is neither shared nor complained about.
func TestAnEmptyClipboardIsNotAnEvent(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)

	here.copyText("")
	clip.tick(context.Background())

	if got := there.received(); len(got) != 0 {
		t.Fatalf("an empty clipboard was sent: %q", got)
	}
	st := clip.Status()
	if len(st.Events) != 0 || st.Err != "" {
		t.Errorf("Status() = %+v, want nothing said about an empty clipboard", st)
	}
}

// TestThePeerNotRunningIsANormalState is the state this feature has that the
// shared folder does not: the program has to be running on both machines.
func TestThePeerNotRunningIsANormalState(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)
	there.refuse(peerNotRunning())

	here.copyText("не дойдёт")
	clip.tick(context.Background())

	st := clip.Status()
	if !st.PeerMissing {
		t.Error("PeerMissing = false, want the window to be able to say why nothing arrived")
	}
	if st.Err != "" {
		t.Errorf("Err = %q — the peer not running is not a fault", st.Err)
	}
	if st.Counts.Failed != 0 {
		t.Errorf("Counts.Failed = %d, want 0", st.Counts.Failed)
	}

	// And it clears the moment the peer answers again.
	there.refuse(nil)
	here.copyText("а это дойдёт")
	clip.tick(context.Background())
	if clip.Status().PeerMissing {
		t.Error("PeerMissing stayed set after a successful delivery")
	}
}

func TestADeliveryThatFailedIsCountedAndSaid(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)
	there.refuse(errBroken)

	here.copyText("не ушло")
	clip.tick(context.Background())

	st := clip.Status()
	if st.Counts.Failed != 1 {
		t.Errorf("Counts.Failed = %d, want 1", st.Counts.Failed)
	}
	if st.Err != msgClipSendFailed {
		t.Errorf("Err = %q, want the Russian sentence about a failed delivery", st.Err)
	}
	if len(st.Events) != 1 || st.Events[0].Kind != ClipFailed {
		t.Errorf("Events = %+v, want one failed", st.Events)
	}
}

func TestAClipboardThatCannotBeReadIsSaidPlainly(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		set  func(*fakeClipboard)
		want string
	}{
		"the sequence number cannot be read": {
			set:  func(f *fakeClipboard) { f.breakSequence(errBroken) },
			want: msgClipNoClipboard,
		},
		"the clipboard is held by somebody else": {
			set:  func(f *fakeClipboard) { f.breakLook(errBroken) },
			want: msgClipReadFailed,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clip, here, there := clipUnderTest(0)
			on(t, clip)
			here.copyText("что-то")
			tc.set(here)

			clip.tick(context.Background())
			if got := there.received(); len(got) != 0 {
				t.Fatalf("something was sent from a clipboard that cannot be read: %q", got)
			}
			if got := clip.Status().Err; got != tc.want {
				t.Errorf("Err = %q, want %q", got, tc.want)
			}
		})
	}
}

// Rule 5: one action, and it cannot fail.
func TestTurningItOffStopsSharingAtOnce(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)
	clip.TurnOff()

	if clip.On() {
		t.Fatal("On() = true after TurnOff()")
	}
	here.copyText("после выключения")
	clip.tick(context.Background())
	if got := there.received(); len(got) != 0 {
		t.Errorf("the peer got %q after the feature was switched off", got)
	}
	// And an arriving item is refused too: off means nothing happens.
	if err := clip.Receive([]byte("оттуда")); !errors.Is(err, ErrClipOff) {
		t.Errorf("Receive() = %v, want ErrClipOff", err)
	}
	if got := here.delivered(); len(got) != 0 {
		t.Errorf("something was written to this clipboard while off: %q", got)
	}
}

func TestReceiveRefusesWhatItShould(t *testing.T) {
	t.Parallel()

	t.Run("an item past this machine's own cap", func(t *testing.T) {
		t.Parallel()

		clip, here, _ := clipUnderTest(16)
		on(t, clip)

		err := clip.Receive([]byte(strings.Repeat("x", 64)))
		if !errors.Is(err, ErrClipTooBig) {
			t.Errorf("Receive() = %v, want ErrClipTooBig", err)
		}
		if got := here.delivered(); len(got) != 0 {
			t.Errorf("an oversized arriving item was written anyway: %q", got)
		}
		if got := clip.Status().Counts.TooBig; got != 1 {
			t.Errorf("Counts.TooBig = %d, want 1", got)
		}
	})

	t.Run("a clipboard that will not take it", func(t *testing.T) {
		t.Parallel()

		clip, here, _ := clipUnderTest(0)
		on(t, clip)
		here.breakPut(errBroken)

		if err := clip.Receive([]byte("оттуда")); err == nil {
			t.Error("Receive() = nil when the clipboard refused the item")
		}
		if got := clip.Status().Err; got != msgClipPutFailed {
			t.Errorf("Err = %q, want the Russian sentence about a failed write", got)
		}
	})

	t.Run("a machine with no clipboard at all", func(t *testing.T) {
		t.Parallel()

		clip := NewClip(ClipConfig{}, ClipDeps{})
		if err := clip.Receive([]byte("оттуда")); !errors.Is(err, ErrClipUnavailable) {
			t.Errorf("Receive() = %v, want ErrClipUnavailable", err)
		}
	})
}

func TestAReceivedItemIsCountedAndSeen(t *testing.T) {
	t.Parallel()

	clip, _, _ := clipUnderTest(0)
	on(t, clip)

	const arrived = "пришло со второй машины"
	if err := clip.Receive([]byte(arrived)); err != nil {
		t.Fatalf("Receive() = %v", err)
	}
	st := clip.Status()
	if st.Counts.Received != 1 {
		t.Errorf("Counts.Received = %d, want 1", st.Counts.Received)
	}
	if len(st.Events) != 1 || st.Events[0].Kind != ClipReceived || st.Events[0].Bytes != len(arrived) {
		t.Errorf("Events = %+v, want one received of %d bytes", st.Events, len(arrived))
	}
}

// A machine with no clipboard to share reports the feature as unavailable,
// which is not the same as switched off, and cannot be switched on.
func TestAMachineWithNoClipboardSaysSoRatherThanPretending(t *testing.T) {
	t.Parallel()

	clip := NewClip(ClipConfig{PeerName: "win-sttm11d02rd"}, ClipDeps{})
	if clip.Available() {
		t.Error("Available() = true with no clipboard and no peer")
	}
	if err := clip.TurnOn(); !errors.Is(err, ErrClipUnavailable) {
		t.Errorf("TurnOn() = %v, want ErrClipUnavailable", err)
	}
	st := clip.Status()
	if st.Available || st.On {
		t.Errorf("Status() = %+v, want unavailable and off", st)
	}
	// Starting is a no-op, and there is nothing to wait for.
	clip.Start(context.Background())
	clip.Wait()
}

// A clipboard that cannot even report its sequence number cannot be switched
// on: a feature that says «включён» and then never notices a copy is worse
// than one that refuses.
func TestSwitchingOnFailsWhenTheClipboardCannotBeRead(t *testing.T) {
	t.Parallel()

	clip, here, _ := clipUnderTest(0)
	here.breakSequence(errBroken)

	if err := clip.TurnOn(); !errors.Is(err, ErrClipUnavailable) {
		t.Errorf("TurnOn() = %v, want ErrClipUnavailable", err)
	}
	if clip.On() {
		t.Error("On() = true after TurnOn failed")
	}
}

func TestTheEventListStaysShort(t *testing.T) {
	t.Parallel()

	clip, here, _ := clipUnderTest(1024)
	on(t, clip)

	for i := range clipEventsKept + 5 {
		here.copyBig(2048 + i)
		clip.tick(context.Background())
	}
	st := clip.Status()
	if len(st.Events) != clipEventsKept {
		t.Fatalf("Events has %d lines, want %d", len(st.Events), clipEventsKept)
	}
	// The oldest are dropped, so the last line is the last thing that happened.
	if want := 2048 + clipEventsKept + 4; st.Events[len(st.Events)-1].Bytes != want {
		t.Errorf("the last event is %d bytes, want %d", st.Events[len(st.Events)-1].Bytes, want)
	}
	if st.Counts.TooBig != clipEventsKept+5 {
		t.Errorf("Counts.TooBig = %d, want every one of them counted", st.Counts.TooBig)
	}
}

// Nothing in what the window is told carries the content. This is the same
// promise tools/gates asserts about the source; here it is asserted about the
// values that actually leave this package.
func TestNothingTheWindowIsToldCarriesTheContent(t *testing.T) {
	t.Parallel()

	const secret = "correct-horse-battery-staple-и-по-русски"
	clip, here, _ := clipUnderTest(0)
	on(t, clip)

	here.copyText(secret)
	clip.tick(context.Background())
	if err := clip.Receive([]byte(secret + "-обратно")); err != nil {
		t.Fatalf("Receive() = %v", err)
	}
	rendered := fmt.Sprintf("%+v", clip.Status())
	if strings.Contains(rendered, "correct-horse") || strings.Contains(rendered, "staple") {
		t.Errorf("the status carries the clipboard content: %s", rendered)
	}
}

// The loop must not hold the clipboard while it is talking to the peer, or one
// slow delivery would lock every other application out of the clipboard.
func TestTheClipboardIsNotHeldAcrossTheNetwork(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)
	there.block = make(chan struct{})

	here.copyText("медленная отправка")
	done := make(chan struct{})
	go func() {
		defer close(done)
		clip.tick(context.Background())
	}()

	// While that delivery is in flight, an arriving item must still be able to
	// reach this machine's clipboard.
	deadline := time.After(2 * time.Second)
	planted := make(chan error, 1)
	go func() { planted <- clip.Receive([]byte("пришло тем временем")) }()
	select {
	case err := <-planted:
		if err != nil {
			t.Errorf("Receive() during a delivery = %v", err)
		}
	case <-deadline:
		t.Error("Receive() blocked behind a delivery in flight")
	}

	close(there.block)
	<-done
}

// Shutdown leaves no goroutine behind and no window claiming the feature is on.
func TestTheLoopStopsWithTheContextAndLeavesNothingRunning(t *testing.T) {
	t.Parallel()

	here := newFakeClipboard()
	there := newFakeInbox()
	clip := NewClip(ClipConfig{Poll: time.Millisecond}, ClipDeps{Here: here, There: there})

	ctx, cancel := context.WithCancel(context.Background())
	clip.Start(ctx)
	clip.Start(ctx) // a second Start must not begin a second loop
	on(t, clip)
	here.copyText("пока работает")

	cancel()
	clip.Wait()

	if clip.On() {
		t.Error("On() = true after the loop stopped")
	}
	// And a Start after the loop has gone does not raise a new one.
	clip.Start(context.Background())
	clip.Wait()
}

// The peer's own name is taken from the peer when the configuration names none,
// so the window never has to guess what to call the other machine.
func TestThePeerNameFallsBackToWhatThePeerCallsItself(t *testing.T) {
	t.Parallel()

	here, there := newFakeClipboard(), newFakeInbox()
	clip := NewClip(ClipConfig{}, ClipDeps{Here: here, There: there})
	if got := clip.Status().PeerName; got != there.Name() {
		t.Errorf("PeerName = %q, want %q", got, there.Name())
	}
	named := NewClip(ClipConfig{PeerName: "другая"}, ClipDeps{Here: here, There: there})
	if got := named.Status().PeerName; got != "другая" {
		t.Errorf("PeerName = %q, want the configured name", got)
	}
}

// A delivery abandoned because the program is shutting down says nothing: a
// «не удалось» on the way out would read as a fault.
func TestAnItemAbandonedAtShutdownIsNotReportedAsAFailure(t *testing.T) {
	t.Parallel()

	clip, here, there := clipUnderTest(0)
	on(t, clip)
	there.refuse(errBroken)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	here.copyText("на выходе")
	clip.tick(ctx)

	st := clip.Status()
	if st.Err != "" || st.Counts.Failed != 0 {
		t.Errorf("Status() = %+v, want nothing said on the way out", st)
	}
}
