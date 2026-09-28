//go:build windows

package clipboard

import (
	"encoding/binary"
	"errors"
	"os"
	"strings"
	"testing"
	"unsafe"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// These tests talk to the real clipboard of the session they run in. They are
// opt-in so an ordinary test run never changes a user's clipboard or sends test
// values through a running Link Monitor. Pause sharing before opting in. They
// restore supported contents and never run in parallel.
//
// A session with no reachable clipboard skips them and says so. A build agent
// in session 0 is the case that matters; the decision every one of these
// checks is separately and fully covered in internal/core/clipshare, which
// needs no clipboard at all.

// requireClipboard skips the test when this session has no clipboard to open.
func requireClipboard(t *testing.T) {
	t.Helper()
	if os.Getenv("LINKMON_TEST_REAL_CLIPBOARD") != "1" {
		t.Skip("set LINKMON_TEST_REAL_CLIPBOARD=1 to test the desktop clipboard")
	}

	if err := openClipboard(); err != nil {
		t.Skipf("no clipboard in this session: %v", err)
	}
	closeClipboard()
}

// keepClipboard restores text or an image after a test. Unknown formats are
// left alone: replacing something we cannot restore would lose user data.
func keepClipboard(t *testing.T) {
	t.Helper()

	c := New()
	before, err := c.Look(1 << 20)
	if err != nil {
		t.Skipf("cannot read the clipboard to put it back afterwards: %v", err)
	}
	if !before.Recordable ||
		(before.Format == "png" && len(before.Text) == 0) ||
		(before.Format != "png" && !before.HasText) {
		t.Skip("clipboard contains an item this test cannot restore")
	}
	t.Cleanup(func() {
		if before.Format == "png" {
			if err := c.PutImage(before.Text); err != nil {
				t.Errorf("putting the user's screenshot back: %v", err)
			}
			return
		}
		if err := c.Put(before.Text); err != nil {
			t.Errorf("putting the user's clipboard back: %v", err)
		}
	})
}

// putRaw replaces the clipboard with one text item plus any number of extra
// formats, in one clipboard session — which is the only way a marker and the
// text it is about can be on the clipboard at the same time. It is what a
// password manager does.
func putRaw(t *testing.T, text string, extra map[uint32][]byte) {
	t.Helper()

	if err := openClipboard(); err != nil {
		t.Fatalf("opening the clipboard: %v", err)
	}
	defer closeClipboard()

	if ok, err := call(procEmptyClipboard); ok == 0 {
		t.Fatalf("emptying the clipboard: %v", err)
	}
	handOverOrFail(t, cfUnicodeText, asBytes(toUTF16([]byte(text))))
	for id, payload := range extra {
		handOverOrFail(t, id, payload)
	}
}

// handOverOrFail puts one raw payload on the already-open clipboard.
func handOverOrFail(t *testing.T, id uint32, payload []byte) {
	t.Helper()

	block, err := globalAlloc(uintptr(len(payload)))
	if err != nil {
		t.Fatalf("allocating %d bytes: %v", len(payload), err)
	}
	addr, err := globalLock(block)
	if err != nil {
		globalFree(block)
		t.Fatalf("locking the block: %v", err)
	}
	copyOutBytes2(addr, payload)
	globalUnlock(block)

	if ok, callErr := call(procSetClipboardData, uintptr(id), block); ok == 0 {
		globalFree(block)
		t.Fatalf("setting clipboard format %d: %v", id, callErr)
	}
}

// copyOutBytes2 is [copyInto] for a byte payload, and lives here because only
// a test ever writes a format that is not text.
func copyOutBytes2(dst uintptr, src []byte) {
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on callPtr
	_, _ = callPtr(procRtlMoveMemory, dst, uintptr(unsafe.Pointer(&src[0])), uintptr(len(src)))
}

// asBytes reinterprets UTF-16 units as the bytes the clipboard block holds.
func asBytes(units []uint16) []byte {
	out := make([]byte, 0, len(units)*2)
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8)) //nolint:gosec // G115: a deliberate byte split
	}
	return out
}

// dword is a serialised DWORD, the payload both flag formats carry.
func dword(v uint32) []byte {
	out := make([]byte, dwordBytes)
	binary.LittleEndian.PutUint32(out, v)
	return out
}

func TestTextRoundTripsThroughTheClipboard(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	// Cyrillic and a surrogate pair, because the conversion in both directions
	// is written out by hand and a pair is where hand-written conversions
	// break.
	const want = "Привет, вторая машина 🙂"
	c := New()
	if err := c.Put([]byte(want)); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	got, err := c.Look(1 << 20)
	if err != nil {
		t.Fatalf("Look() = %v", err)
	}
	if !got.HasText || !got.Recordable {
		t.Fatalf("Look() = %+v, want text that may travel", got)
	}
	if string(got.Text) != want {
		t.Errorf("Look().Text = %q, want %q", got.Text, want)
	}
	if got.Bytes != len(want) {
		t.Errorf("Look().Bytes = %d, want %d", got.Bytes, len(want))
	}
}

func TestTheSequenceNumberMovesWhenTheClipboardChanges(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	c := New()
	if err := c.Put([]byte("первое")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	before, err := c.Sequence()
	if err != nil {
		t.Fatalf("Sequence() = %v", err)
	}
	if again, err := c.Sequence(); err != nil || again != before {
		t.Errorf("Sequence() = %d, %v on an unchanged clipboard, want %d and no error", again, err, before)
	}
	if err := c.Put([]byte("второе")); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	after, err := c.Sequence()
	if err != nil {
		t.Fatalf("Sequence() = %v", err)
	}
	if after == before {
		t.Error("the sequence number did not move after a copy; the loop would never notice one")
	}
}

// TestAnItemPastTheCapIsMeasuredAndNotCopiedOut is rule 2's half of the
// contract with the loop: the item is reported, its size is reported, and its
// content never leaves the clipboard.
func TestAnItemPastTheCapIsMeasuredAndNotCopiedOut(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	c := New()
	if err := c.Put([]byte(strings.Repeat("a", 4096))); err != nil {
		t.Fatalf("Put() = %v", err)
	}
	got, err := c.Look(1024)
	if err != nil {
		t.Fatalf("Look() = %v", err)
	}
	if !got.HasText || got.Text != nil {
		t.Errorf("Look() = %+v, want text reported and nothing copied out", got)
	}
	if got.Bytes <= 1024 {
		t.Errorf("Look().Bytes = %d, want more than the cap of 1024", got.Bytes)
	}
	if why, _ := clipshare.Decide(got, clipshare.Memory{}, 1024); why != clipshare.WhyTooBig {
		t.Errorf("the decision about it = %q, want %q", why, clipshare.WhyTooBig)
	}
}

// TestWindowsOwnDoNotRecordMarkersAreHonoured is rule 3, one case per marker.
//
// Each case puts ordinary text on the clipboard together with the marker a
// password manager would set, and asserts that this package reports an item
// that may not be recorded — at which point the decision refuses to transmit
// it. The last case is the other direction: a marker that says yes must not
// stop anything.
func TestWindowsOwnDoNotRecordMarkersAreHonoured(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	ids, err := registerFormats()
	if err != nil {
		t.Fatalf("registering the three formats: %v", err)
	}
	cases := []struct {
		name  string
		extra map[uint32][]byte
		want  bool
	}{
		{formatExclude + " present", map[uint32][]byte{ids.exclude: {0}}, false},
		{formatHistory + " = 0", map[uint32][]byte{ids.history: dword(0)}, false},
		{formatCloud + " = 0", map[uint32][]byte{ids.cloud: dword(0)}, false},
		{"both flags = 1", map[uint32][]byte{ids.history: dword(1), ids.cloud: dword(1)}, true},
		{"a flag too short to be a DWORD", map[uint32][]byte{ids.history: {1}}, false},
		{"no marker at all", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			putRaw(t, "секрет из менеджера паролей", tc.extra)

			got, err := New().Look(1 << 20)
			if err != nil {
				t.Fatalf("Look() = %v", err)
			}
			if got.Recordable != tc.want {
				t.Fatalf("Look().Recordable = %v, want %v", got.Recordable, tc.want)
			}
			why, _ := clipshare.Decide(got, clipshare.Memory{}, 1<<20)
			if tc.want != why.Sends() {
				t.Errorf("the decision about it = %q, sends = %v, want sends = %v", why, why.Sends(), tc.want)
			}
			if !tc.want && got.Text != nil {
				t.Error("a marked item was copied out of the clipboard; it must not be read at all")
			}
		})
	}
}

// A refused item is refused before its size is even taken, so that nothing
// about it — not its length — leaves the clipboard.
func TestAMarkedItemIsNotEvenMeasured(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	ids, err := registerFormats()
	if err != nil {
		t.Fatalf("registering the three formats: %v", err)
	}
	putRaw(t, "довольно длинный секрет, чтобы длина что-то значила", map[uint32][]byte{
		ids.exclude: {0},
	})
	got, err := New().Look(1 << 20)
	if err != nil {
		t.Fatalf("Look() = %v", err)
	}
	if got.Bytes != 0 {
		t.Errorf("Look().Bytes = %d for a marked item, want 0", got.Bytes)
	}
}

func TestAnEmptyClipboardIsNotText(t *testing.T) {
	requireClipboard(t)
	keepClipboard(t)

	putRaw(t, "", nil)
	got, err := New().Look(1 << 20)
	if err != nil {
		t.Fatalf("Look() = %v", err)
	}
	// An empty string is still CF_UNICODETEXT: one terminator and no content.
	if !got.HasText || got.Bytes != 0 || len(got.Text) != 0 {
		t.Errorf("Look() = %+v, want text with nothing in it", got)
	}
	if why, _ := clipshare.Decide(got, clipshare.Memory{}, 0); why != clipshare.WhyEmpty {
		t.Errorf("the decision about it = %q, want %q", why, clipshare.WhyEmpty)
	}
}

// The three format names must be registered exactly as Windows knows them: a
// typo would register a private format of our own, which nothing else would
// ever set, and rule 3 would then be a check that always passes.
func TestTheThreeFormatNamesAreRegisteredUnderTheirDocumentedSpelling(t *testing.T) {
	ids, err := registerFormats()
	if err != nil {
		t.Fatalf("registering the three formats: %v", err)
	}
	if ids.exclude == 0 || ids.history == 0 || ids.cloud == 0 {
		t.Fatalf("registerFormats() = %+v, want three non-zero ids", ids)
	}
	if ids.exclude == ids.history || ids.history == ids.cloud || ids.exclude == ids.cloud {
		t.Errorf("registerFormats() = %+v, want three different ids", ids)
	}
	// Asking twice must give the same answer, or the ids cached for the life of
	// the process would stop matching what a password manager sets.
	again, err := registerFormats()
	if err != nil || again != ids {
		t.Errorf("registerFormats() = %+v, %v on the second call, want %+v", again, err, ids)
	}
	checkFormatNames(t)
}

// checkFormatNames is the other half of the same rule: a name with a stray
// space, or the wrong spelling, would register a private format of our own
// that nothing else ever sets — and rule 3 would become a check that always
// passes.
func checkFormatNames(t *testing.T) {
	t.Helper()

	want := map[string]string{
		"exclude": "ExcludeClipboardContentFromMonitorProcessing",
		"history": "CanIncludeInClipboardHistory",
		"cloud":   "CanUploadToCloudClipboard",
	}
	got := map[string]string{
		"exclude": formatExclude,
		"history": formatHistory,
		"cloud":   formatCloud,
	}
	for which, name := range want {
		if got[which] != name {
			t.Errorf("the %s format is spelled %q, want %q — Windows knows it by that name "+
				"and by no other", which, got[which], name)
		}
		if strings.TrimSpace(name) != name {
			t.Errorf("the %s format name %q has whitespace around it", which, name)
		}
	}
}

func TestConvertingBetweenUTF8AndUTF16(t *testing.T) {
	t.Parallel()

	cases := []string{"", "ascii", "Привет", "🙂 и ещё", strings.Repeat("Ы", 300)}
	for _, want := range cases {
		units := toUTF16([]byte(want))
		if units[len(units)-1] != 0 {
			t.Errorf("toUTF16(%q) is not NUL-terminated", want)
		}
		got, whole := toUTF8(units)
		if !whole {
			t.Errorf("toUTF8 did not find the terminator in %q", want)
		}
		if string(got) != want {
			t.Errorf("round trip of %q = %q", want, got)
		}
	}
}

// A half surrogate is what a truncated read leaves behind, and it must produce
// the replacement character rather than a panic or a silent shift.
func TestAHalfSurrogateBecomesTheReplacementCharacter(t *testing.T) {
	t.Parallel()

	got, whole := toUTF8([]uint16{0xD83D, 0})
	if !whole || string(got) != "�" {
		t.Errorf("toUTF8(lone high surrogate) = %q, %v, want the replacement character", got, whole)
	}
	got, whole = toUTF8([]uint16{0xDE00, 0x41, 0})
	if !whole || string(got) != "�A" {
		t.Errorf("toUTF8(lone low surrogate) = %q, %v, want the replacement character then A", got, whole)
	}
}

// A buffer with no terminator in it is an item the reader stopped short of,
// which is how the cap is enforced without copying the whole item.
func TestAnUnterminatedBufferIsReportedAsIncomplete(t *testing.T) {
	t.Parallel()

	got, whole := toUTF8([]uint16{0x41, 0x42})
	if whole {
		t.Error("toUTF8 claimed to have found a terminator that is not there")
	}
	if string(got) != "AB" {
		t.Errorf("toUTF8() = %q, want AB", got)
	}
}

func TestZeroingABufferLeavesNothingBehind(t *testing.T) {
	t.Parallel()

	buf := toUTF16([]byte("секрет"))
	zero16(buf)
	for i, u := range buf {
		if u != 0 {
			t.Fatalf("zero16 left %d at index %d", u, i)
		}
	}
}

// Away from Windows every call refuses, and the error says so; here the same
// sentinel must not be what a working clipboard answers with.
func TestAWorkingClipboardDoesNotReportItselfUnsupported(t *testing.T) {
	requireClipboard(t)

	if _, err := New().Sequence(); errors.Is(err, ErrUnsupported) {
		t.Error("Sequence() reported the platform as unsupported on Windows")
	}
}
