package clipshare

import (
	"bytes"
	"testing"
)

// text is a clipboard item that is allowed to travel, unless a test says
// otherwise. It is deliberately ordinary: none of these tests needs a secret.
func text(s string) Snapshot {
	return Snapshot{HasText: true, Bytes: len(s), Text: []byte(s), Recordable: true}
}

func TestFingerprintIsTheSameForTheSameBytesAndDiffersOtherwise(t *testing.T) {
	t.Parallel()

	a := Fingerprint([]byte("одно и то же"))
	if b := Fingerprint([]byte("одно и то же")); a != b {
		t.Error("the same bytes produced two different prints")
	}
	if c := Fingerprint([]byte("одно и то же ")); a == c {
		t.Error("a trailing space produced the same print")
	}
	// An empty item still has a print; Decide never asks for one, but a caller
	// that did must not get a zero value it could mistake for "no print".
	if Fingerprint(nil) == (Print{}) {
		t.Error("the print of nothing is the zero value, which reads as absent")
	}
}

func TestZeroOverwritesEveryByte(t *testing.T) {
	t.Parallel()

	buf := []byte("пароль")
	Zero(buf)
	if !bytes.Equal(buf, make([]byte, len(buf))) {
		t.Errorf("Zero left %v behind", buf)
	}
	Zero(nil) // must not panic; an item past the cap was never copied out
}

func TestOnlySendSends(t *testing.T) {
	t.Parallel()

	if !WhySend.Sends() {
		t.Error("WhySend.Sends() = false")
	}
	for _, w := range []Why{WhyNotText, WhyEmpty, WhyMarked, WhyTooBig, WhyEcho, WhySame} {
		if w.Sends() {
			t.Errorf("%q.Sends() = true — that verdict must not transmit", w)
		}
	}
}

func TestAnOrdinaryPieceOfTextIsSent(t *testing.T) {
	t.Parallel()

	why, mark := Decide(text("привет"), Memory{}, 0)
	if why != WhySend {
		t.Errorf("Decide() = %q, want %q", why, WhySend)
	}
	if mark != Fingerprint([]byte("привет")) {
		t.Error("Decide returned a print that is not the item's")
	}
}

// The two refusals that protect the user are checked before anything else, so
// that what the UI shows about an item is the rule that stopped it rather than
// its size or its history.
func TestTheProtectiveRefusalsComeFirst(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		snap Snapshot
		want Why
	}{
		{
			"a file or an image, marked and oversized as well",
			Snapshot{HasText: false, Bytes: 1 << 20, Recordable: false},
			WhyNotText,
		},
		{
			"marked do-not-record, and oversized as well",
			Snapshot{HasText: true, Bytes: 1 << 20, Text: []byte("x"), Recordable: false},
			WhyMarked,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			why, mark := Decide(tc.snap, Memory{}, 16)
			if why != tc.want {
				t.Errorf("Decide() = %q, want %q", why, tc.want)
			}
			if mark != (Print{}) {
				t.Error("Decide fingerprinted an item it had already refused")
			}
		})
	}
}

func TestAnItemPastTheCapIsRefusedWithoutItsContent(t *testing.T) {
	t.Parallel()

	// The reader measured 4 KiB and copied nothing out, which is what it does
	// past the cap: the item is never read into this process at all.
	big := Snapshot{HasText: true, Bytes: 4 << 10, Recordable: true}
	if why, _ := Decide(big, Memory{}, 1024); why != WhyTooBig {
		t.Errorf("Decide() = %q, want %q", why, WhyTooBig)
	}
	// Exactly at the cap is allowed: the cap is a maximum, not a limit to stay
	// under.
	edge := text("0123456789")
	if why, _ := Decide(edge, Memory{}, 10); why != WhySend {
		t.Errorf("an item exactly at the cap: Decide() = %q, want %q", why, WhySend)
	}
}

// A caller that passes no cap gets the default rather than no cap at all.
func TestAMissingCapMeansTheDefaultAndNeverNoCap(t *testing.T) {
	t.Parallel()

	huge := Snapshot{HasText: true, Bytes: DefaultMaxBytes + 1, Recordable: true}
	for _, cap := range []int{0, -1} {
		if why, _ := Decide(huge, Memory{}, cap); why != WhyTooBig {
			t.Errorf("Decide(cap=%d) = %q, want %q", cap, why, WhyTooBig)
		}
	}
}

func TestAnEmptyItemGoesNowhere(t *testing.T) {
	t.Parallel()

	cases := map[string]Snapshot{
		"an empty string on the clipboard": text(""),
		"a size with no content behind it": {HasText: true, Bytes: 12, Recordable: true},
	}
	for name, snap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			if why, _ := Decide(snap, Memory{}, 1<<20); why != WhyEmpty {
				t.Errorf("Decide() = %q, want %q", why, WhyEmpty)
			}
		})
	}
}

// TestWhatWePlantedDoesNotBounceBack is the echo problem, and it is the reason
// Memory exists at all.
func TestWhatWePlantedDoesNotBounceBack(t *testing.T) {
	t.Parallel()

	arrived := text("текст со второй машины")
	mem := Memory{}.AfterPlanting(Fingerprint(arrived.Text))

	why, _ := Decide(arrived, mem, 0)
	if why != WhyEcho {
		t.Fatalf("Decide() = %q, want %q — this is the item bouncing back", why, WhyEcho)
	}
	// Something the user copies afterwards is new and must still travel, or
	// the echo defence would have switched the feature off.
	if next, _ := Decide(text("что-то своё"), mem, 0); next != WhySend {
		t.Errorf("the next local copy: Decide() = %q, want %q", next, WhySend)
	}
}

func TestWhatWeAlreadySentIsNotSentTwice(t *testing.T) {
	t.Parallel()

	item := text("одно и то же")
	mem := Memory{}.AfterSending(Fingerprint(item.Text))

	if why, _ := Decide(item, mem, 0); why != WhySame {
		t.Errorf("Decide() = %q, want %q", why, WhySame)
	}
}

// The two halves of the memory are independent: a planted item is recognised
// even after something else has been sent, and the other way round.
func TestTheTwoHalvesOfTheMemoryDoNotOverwriteEachOther(t *testing.T) {
	t.Parallel()

	planted := text("пришло оттуда")
	sent := text("ушло туда")
	mem := Memory{}.
		AfterPlanting(Fingerprint(planted.Text)).
		AfterSending(Fingerprint(sent.Text))

	if why, _ := Decide(planted, mem, 0); why != WhyEcho {
		t.Errorf("the planted item: Decide() = %q, want %q", why, WhyEcho)
	}
	if why, _ := Decide(sent, mem, 0); why != WhySame {
		t.Errorf("the sent item: Decide() = %q, want %q", why, WhySame)
	}
}

// A fresh memory recognises nothing, which is what makes the first copy after
// switching the feature on travel rather than be taken for an echo.
func TestAFreshMemoryRecognisesNothing(t *testing.T) {
	t.Parallel()

	item := text("первый раз")
	if why, _ := Decide(item, Memory{}, 0); why != WhySend {
		t.Errorf("Decide() = %q, want %q", why, WhySend)
	}
}
