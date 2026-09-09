//go:build windows

package window

import (
	"testing"
	"unsafe"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
)

// TestMinMaxInfoMatchesTheWindowsLayout is the one thing about the struct that
// has to be right. WM_GETMINMAXINFO hands over the address of Windows' own
// MINMAXINFO and reads it back the moment the procedure returns; a field at
// the wrong offset would not fail, it would quietly scribble on a neighbour.
func TestMinMaxInfoMatchesTheWindowsLayout(t *testing.T) {
	t.Parallel()

	const pointSize = 8 // two LONGs

	if got := unsafe.Sizeof(minMaxInfo{}); got != 5*pointSize {
		t.Errorf("sizeof(MINMAXINFO) = %d, want %d", got, 5*pointSize)
	}
	if got := unsafe.Sizeof(point{}); got != pointSize {
		t.Errorf("sizeof(POINT) = %d, want %d", got, pointSize)
	}

	var info minMaxInfo
	for _, tc := range []struct {
		name string
		got  uintptr
		want uintptr
	}{
		{name: "ptReserved", got: unsafe.Offsetof(info.reserved), want: 0},
		{name: "ptMaxSize", got: unsafe.Offsetof(info.maxSize), want: pointSize},
		{name: "ptMaxPosition", got: unsafe.Offsetof(info.maxPosition), want: 2 * pointSize},
		{name: "ptMinTrackSize", got: unsafe.Offsetof(info.minTrackSize), want: 3 * pointSize},
		{name: "ptMaxTrackSize", got: unsafe.Offsetof(info.maxTrackSize), want: 4 * pointSize},
	} {
		if tc.got != tc.want {
			t.Errorf("%s is at offset %d, want %d", tc.name, tc.got, tc.want)
		}
	}
}

// TestClampMinSizeWritesOnlyTheMinimum exercises the message handler against a
// MINMAXINFO of our own. It is the closest a machine with no desktop can get
// to dragging the window's edge.
func TestClampMinSizeWritesOnlyTheMinimum(t *testing.T) {
	t.Parallel()

	b := &nativeBackend{minSize: point{x: 520, y: 560}}
	info := minMaxInfo{
		maxSize:      point{x: 1920, y: 1080},
		maxPosition:  point{x: 1, y: 2},
		minTrackSize: point{x: 100, y: 100},
		maxTrackSize: point{x: 4096, y: 4096},
	}

	b.clampMinSize(unsafe.Pointer(&info))

	if info.minTrackSize != b.minSize {
		t.Errorf("ptMinTrackSize = %+v, want %+v", info.minTrackSize, b.minSize)
	}
	if info.maxSize != (point{x: 1920, y: 1080}) || info.maxTrackSize != (point{x: 4096, y: 4096}) {
		t.Errorf("the handler overwrote a field it does not own: %+v", info)
	}
}

func TestClampMinSizeLeavesTheStructureAloneWhenItHasNothingToSay(t *testing.T) {
	t.Parallel()

	original := minMaxInfo{minTrackSize: point{x: 100, y: 100}}

	// A backend with no minimum configured, and a message with no structure.
	info := original
	(&nativeBackend{}).clampMinSize(unsafe.Pointer(&info))
	if info != original {
		t.Errorf("a backend with no minimum wrote %+v", info)
	}

	// A nil lParam must not be dereferenced. Reaching this line is the test.
	(&nativeBackend{minSize: point{x: 520, y: 560}}).clampMinSize(nil)
}

// TestTheWindowProcedureCanBeMadeIntoACallback checks the signature Windows is
// handed. windows.NewCallback panics on a signature it cannot marshal, and it
// would do so at the moment the window is created, on a real desktop, where
// nothing would be left to report it.
func TestTheWindowProcedureCanBeMadeIntoACallback(t *testing.T) {
	t.Parallel()

	if got := sharedProc(); got == 0 {
		t.Fatal("sharedProc() = 0, want the address of a compiled callback")
	}
	if first, second := sharedProc(), sharedProc(); first != second {
		t.Errorf("sharedProc() returned %#x then %#x; it must be built once", first, second)
	}
}

// TestAMessageForAnUnknownWindowGoesToTheDefaultProcedure covers the path
// taken while a window is being torn down. It uses a handle no window has.
func TestAMessageForAnUnknownWindowGoesToTheDefaultProcedure(t *testing.T) {
	t.Parallel()

	// WM_NULL against a handle that is not a window: DefWindowProcW answers
	// zero and the process is none the worse. What is being checked is that
	// the lookup misses without panicking.
	if got := windowProc(0, 0x0000, 0, nil); got != 0 {
		t.Errorf("windowProc for an unknown window = %d, want 0", got)
	}
}

func TestRegisteringAndForgettingABackend(t *testing.T) {
	t.Parallel()

	// A handle no real window will have, so this test cannot collide with a
	// window another test made.
	const hwnd = 0xDEAD0001
	b := &nativeBackend{}

	registerBackend(hwnd, b)
	if got := lookupBackend(hwnd); got != b {
		t.Fatalf("lookupBackend() = %p, want %p", got, b)
	}
	forgetBackend(hwnd)
	if got := lookupBackend(hwnd); got != nil {
		t.Errorf("lookupBackend() after forgetting = %p, want nil", got)
	}
}

// TestSetWindowProcRefusesAHandleThatIsNotAWindow proves the error path is
// wired up: the call fails, GetLastError says why, and we report it rather
// than carrying on with a zero "previous procedure".
func TestSetWindowProcRefusesAHandleThatIsNotAWindow(t *testing.T) {
	t.Parallel()

	if _, err := setWindowProc(0, sharedProc()); err == nil {
		t.Error("setWindowProc(0, _) succeeded; want a failure for a handle that is not a window")
	}
}

// TestCreateIconFromTheTrayIconBytes is the only part of the icon path a
// machine with no desktop can check, and it is the part most likely to be
// wrong: whether CreateIconFromResourceEx accepts a PNG-compressed .ico image,
// which is what internal/ui/trayicon produces.
func TestCreateIconFromTheTrayIconBytes(t *testing.T) {
	t.Parallel()

	ico, err := trayicon.Render(core.StateOK)
	if err != nil {
		t.Fatalf("Render() = %v, want the tray icon", err)
	}

	for _, side := range []int{16, 20, 32, 48} {
		icon, err := createIcon(ico, side, side)
		if err != nil {
			t.Errorf("createIcon(_, %d, %d) = %v, want an HICON", side, side, err)
			continue
		}
		if call(procDestroyIcon, icon) == 0 {
			t.Errorf("DestroyIcon failed for the %d px icon", side)
		}
	}
}

func TestCreateIconRefusesBytesThatAreNotAnIcon(t *testing.T) {
	t.Parallel()

	if _, err := createIcon([]byte("not an icon"), 16, 16); err == nil {
		t.Error("createIcon accepted bytes that are not an .ico")
	}
}

func TestSystemMetricFallsBackWhenTheDisplayWillNotSay(t *testing.T) {
	t.Parallel()

	// Index 0xFFFF is not a metric, so GetSystemMetrics answers zero.
	if got := systemMetric(0xFFFF, fallbackBig); got != fallbackBig {
		t.Errorf("systemMetric(nonsense) = %d, want the fallback %d", got, fallbackBig)
	}
	// The real small-icon metric is a sensible number on any display.
	if got := systemMetric(smCxSmIcon, fallbackSmall); got <= 0 {
		t.Errorf("systemMetric(SM_CXSMICON) = %d, want a positive size", got)
	}
}

// TestTheRuntimeOnThisMachineIsSeen reads the real registry. It says nothing
// about a machine without the runtime, which is the case that cannot be tested
// here — only that a machine with one is recognised.
func TestTheRuntimeOnThisMachineIsSeen(t *testing.T) {
	t.Parallel()

	pv, err := readRuntimeVersion()
	if err != nil {
		t.Skipf("this machine records no WebView2 runtime: %v", err)
	}
	t.Logf("WebView2 runtime version on this machine: %q", pv)

	if !validRuntimeVersion(pv) {
		t.Fatalf("the registry says %q, which validRuntimeVersion rejects", pv)
	}
	if !RuntimeInstalled() {
		t.Error("RuntimeInstalled() = false on a machine whose registry names a version")
	}
}

// TestGwlpWndProcIsMinusFour guards the constant that had to be written the
// long way round.
func TestGwlpWndProcIsMinusFour(t *testing.T) {
	t.Parallel()

	index := gwlpWndProc // a variable, so the conversion is not a constant one
	if got := int64(int32(index)); got != -4 {
		t.Errorf("gwlpWndProc = %#x, which is %d, want -4", index, got)
	}
}
