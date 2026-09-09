//go:build windows

package window

import (
	"testing"
	"unsafe"
)

// TestMonitorInfoMatchesTheWindowsLayout is the same guard as the one over
// MINMAXINFO, for the same reason: GetMonitorInfoW checks cbSize and then
// writes two RECTs at fixed offsets. A field in the wrong place would not
// fail, it would hand back a work area made of the wrong words.
func TestMonitorInfoMatchesTheWindowsLayout(t *testing.T) {
	t.Parallel()

	const rectSize = 16 // four LONGs

	if got := unsafe.Sizeof(rect{}); got != rectSize {
		t.Errorf("sizeof(RECT) = %d, want %d", got, rectSize)
	}
	var info monitorInfo
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "cbSize", got: unsafe.Offsetof(info.cbSize), want: 0},
		{name: "rcMonitor", got: unsafe.Offsetof(info.rcMonitor), want: 4},
		{name: "rcWork", got: unsafe.Offsetof(info.rcWork), want: 4 + rectSize},
		{name: "dwFlags", got: unsafe.Offsetof(info.flags), want: 4 + 2*rectSize},
	} {
		if tc.got != tc.want {
			t.Errorf("%s is at offset %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestTheDisplayThisMachineHasIsReadable asks the real display the three
// questions the window's size depends on. It cannot assert what the answers
// are — a build agent's display is not this user's — only that they are
// answers rather than zeros, which is what the arithmetic needs.
func TestTheDisplayThisMachineHasIsReadable(t *testing.T) {
	t.Parallel()

	monitor := monitorFor(0)
	if monitor == 0 {
		t.Skip("this machine has no monitor to ask about")
	}

	d := displayFor(0)
	if d.dpi <= 0 {
		t.Errorf("displayFor(0).dpi = %d, want a positive DPI", d.dpi)
	}
	if d.dpi != dpiFor(0, monitor) {
		t.Errorf("displayFor and dpiFor disagree: %d and %d", d.dpi, dpiFor(0, monitor))
	}
	if got := desktopDPI(); got <= 0 {
		t.Errorf("desktopDPI() = %d, want a positive DPI", got)
	}

	work := workAreaOf(monitor)
	if work.size().w <= 0 || work.size().h <= 0 {
		t.Errorf("workAreaOf(primary) = %+v, want a work area with room in it", work)
	}
	t.Logf("this machine: %d DPI, work area %+v", d.dpi, work)

	// A handle that is not a monitor has to answer "I do not know" rather than
	// scribble on the struct, because [fitWithin] reads a zero rect as
	// "do not clamp".
	if got := workAreaOf(0); got != (rect{}) {
		t.Errorf("workAreaOf(0) = %+v, want the zero rect", got)
	}
}

// TestTheFrameGrowsWithTheDisplay is the part of the size arithmetic that only
// Windows can answer: how much wider and taller than its client area an
// overlapped window is. Per-Monitor V2 scales the title bar and the border, so
// the frame at 144 DPI has to be bigger than the frame at 96 — if it were not,
// the window would be sized as though the design were the only thing scaling.
func TestTheFrameGrowsWithTheDisplay(t *testing.T) {
	t.Parallel()

	at96 := frameFor(wsOverlappedWindow, 0, 96)
	at144 := frameFor(wsOverlappedWindow, 0, 144)

	if at96.w <= 0 || at96.h <= 0 {
		t.Fatalf("frameFor(_, _, 96) = %+v, want a frame with a title bar in it", at96)
	}
	t.Logf("overlapped frame: %+v at 96 DPI, %+v at 144", at96, at144)

	if procAdjustWindowRectExForDpi.Find() != nil {
		t.Skip("this Windows has no AdjustWindowRectExForDpi, so it has one frame size")
	}
	if at144.w <= at96.w || at144.h <= at96.h {
		t.Errorf("the frame did not scale: %+v at 96 DPI, %+v at 144", at96, at144)
	}
}

// TestAdjustWindowRectGrowsAClientRect checks the wrapper both frame lookups
// go through, against a rect this test can read back.
func TestAdjustWindowRectGrowsAClientRect(t *testing.T) {
	t.Parallel()

	r := rect{left: 0, top: 0, right: 500, bottom: 400}
	if !adjustWindowRect(&r, wsOverlappedWindow, 0, 96) {
		t.Fatal("adjustWindowRect said Windows would not answer for an ordinary window")
	}
	if r.top >= 0 {
		t.Errorf("top = %d, want it negative: the title bar sits above the client area", r.top)
	}
	if r.size().w <= 500 || r.size().h <= 400 {
		t.Errorf("the window rect %+v is no bigger than the client rect it came from", r)
	}
}

// TestWindowStylesOfAHandleThatIsNotAWindow covers the read that only a live
// window can answer properly. Zero styles are what a failed read gives, and
// [frameFor] then measures a frameless window — the wrong frame, not a crash.
func TestWindowStylesOfAHandleThatIsNotAWindow(t *testing.T) {
	t.Parallel()

	style, exStyle := windowStyles(0)
	if style != 0 || exStyle != 0 {
		t.Errorf("windowStyles(0) = %#x, %#x, want zeros", style, exStyle)
	}
}

// TestApplyLayoutAgainstNoWindow is as close as a machine with no window of
// ours can get to opening one: every query runs for real against the real
// display, and only SetWindowPos fails. What is being checked is that the
// minimum still lands where WM_GETMINMAXINFO will read it, and that a failed
// move is survived rather than panicked over.
func TestApplyLayoutAgainstNoWindow(t *testing.T) {
	t.Parallel()

	b := &nativeBackend{log: quiet(), cfg: Config{}}
	b.applyLayout()

	if b.minSize.x <= 0 || b.minSize.y <= 0 {
		t.Fatalf("minSize = %+v, want the design's minimum scaled to this display", b.minSize)
	}
	// The minimum has to be at least the column the stacked variant needs.
	// Below the baseline that would mean the design cannot be shown at all.
	if int(b.minSize.x) < minColumnCSS {
		t.Errorf("minSize.x = %d, which is narrower than the %d CSS pixel column",
			b.minSize.x, minColumnCSS)
	}
	t.Logf("this display would open the window with a %dx%d minimum", b.minSize.x, b.minSize.y)
}

// TestChangeDPIRereadsTheMinimum exercises the WM_DPICHANGED handler with a
// MINMAXINFO-style rect of our own, the way clampMinSize is exercised. The
// window is not real, so the move fails; the minimum being re-derived at the
// new DPI is the part that has to be right.
func TestChangeDPIRereadsTheMinimum(t *testing.T) {
	t.Parallel()

	b := &nativeBackend{log: quiet(), cfg: Config{}}
	suggested := rect{left: 100, top: 100, right: 900, bottom: 700}

	b.changeDPI(96, unsafe.Pointer(&suggested))
	at96 := b.minSize

	b.changeDPI(192, unsafe.Pointer(&suggested))
	at192 := b.minSize

	if at96.x <= 0 || at192.x <= 0 {
		t.Fatalf("the minimum was not set: %+v at 96 DPI, %+v at 192", at96, at192)
	}
	if at192.x <= at96.x || at192.y <= at96.y {
		t.Errorf("the minimum did not follow the DPI: %+v at 96, %+v at 192", at96, at192)
	}
	// Windows can send the message without a rect only if something is very
	// wrong, and a nil lParam must not be dereferenced. Reaching the next line
	// is the test. A zero DPI is the other thing it must survive: the handler
	// asks the display instead of scaling by nothing.
	b.changeDPI(0, nil)
	if b.minSize.x <= 0 {
		t.Errorf("a DPI-less WM_DPICHANGED left no minimum: %+v", b.minSize)
	}
}
