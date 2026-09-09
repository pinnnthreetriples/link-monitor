//go:build windows

package window

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// The window-manager entry points that answer "how big should this window be
// on the display it is actually on". They are the same documented exception
// internal/ui/window already lives under — sizing a window is pixels, not
// something the domain has an opinion about — and every decision they feed is
// lifted out into sizing.go, which is where the arithmetic is tested.
var (
	procGetDpiForWindow          = user32.NewProc("GetDpiForWindow")
	procGetDpiForMonitor         = shcore.NewProc("GetDpiForMonitor")
	procMonitorFromWindow        = user32.NewProc("MonitorFromWindow")
	procMonitorFromPoint         = user32.NewProc("MonitorFromPoint")
	procGetMonitorInfo           = user32.NewProc("GetMonitorInfoW")
	procAdjustWindowRectExForDpi = user32.NewProc("AdjustWindowRectExForDpi")
	procAdjustWindowRectEx       = user32.NewProc("AdjustWindowRectEx")
	procGetWindowLongPtr         = user32.NewProc("GetWindowLongPtrW")
	procGetWindowLong            = user32.NewProc("GetWindowLongW")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procGetDC                    = user32.NewProc("GetDC")
	procReleaseDC                = user32.NewProc("ReleaseDC")

	gdi32             = windows.NewLazySystemDLL("gdi32.dll")
	procGetDeviceCaps = gdi32.NewProc("GetDeviceCaps")
)

const (
	// monitorDefaultToNearest and monitorDefaultToPrimary are the two ways of
	// asking MonitorFrom* what to do when the point or window is nowhere.
	monitorDefaultToPrimary = 1
	monitorDefaultToNearest = 2

	// mdtEffectiveDPI is MDT_EFFECTIVE_DPI: the DPI the desktop is actually
	// scaled by, which is the one a window has to match. The other two —
	// angular and raw — describe the panel, not the desktop.
	mdtEffectiveDPI = 0

	// logPixelsX is the GetDeviceCaps index for the DPI of a device context.
	// It is the pre-8.1 way of asking, and the answer is desktop-wide.
	logPixelsX = 88

	// gwlStyle and gwlExStyle are GWL_STYLE (-16) and GWL_EXSTYLE (-20),
	// written as their unsigned forms for the same reason as gwlpWndProc.
	gwlStyle   = ^uintptr(15)
	gwlExStyle = ^uintptr(19)

	// wsOverlappedWindow is the style the WebView2 library gives the window it
	// creates. It is only needed for the guess made before the window exists;
	// afterwards the real bits are read off the window itself.
	wsOverlappedWindow = 0x00CF0000

	// The SetWindowPos flags: move and size, but do not reorder or steal
	// focus. A resize that raised the window would fight the tray.
	swpNoZOrder      = 0x0004
	swpNoActivate    = 0x0010
	swpNoOwnerZOrder = 0x0200
)

// monitorInfo is a Win32 MONITORINFO. rcWork is the part that matters: the
// monitor minus the taskbar, which is the space a window may occupy.
type monitorInfo struct {
	cbSize    uint32
	rcMonitor rect
	rcWork    rect
	flags     uint32
}

// display is what one monitor says about itself, in the coordinate space of
// this process. On a DPI-unaware process both numbers come back virtualised —
// 96 and a work area in logical pixels — and that is the right answer for
// such a process, because its windows live in that space too.
type display struct {
	dpi  int
	work rect
}

// displayFor returns the display a window is on. An hwnd of zero means the
// primary monitor, which is the best guess available before the window exists
// and is where the WebView2 library centres the one it creates.
func displayFor(hwnd uintptr) display {
	monitor := monitorFor(hwnd)
	return display{dpi: dpiFor(hwnd, monitor), work: workAreaOf(monitor)}
}

// monitorFor returns the HMONITOR a window is on, or the primary one when
// there is no window yet. SPI_GETWORKAREA is deliberately not used anywhere
// here: it only ever answers for the primary monitor, and a window that opens
// on the second screen would be clamped to the wrong one.
func monitorFor(hwnd uintptr) uintptr {
	if hwnd != 0 {
		return call(procMonitorFromWindow, hwnd, monitorDefaultToNearest)
	}
	// MonitorFromPoint with the origin and DEFAULTTOPRIMARY: the origin is by
	// definition the primary monitor's top-left, so this names it without
	// having to ask which one it is.
	return call(procMonitorFromPoint, 0, 0, monitorDefaultToPrimary)
}

// dpiFor returns the DPI to lay the design out against, taking the newest of
// the three ways of asking that this Windows has.
func dpiFor(hwnd, monitor uintptr) int {
	if hwnd != 0 && procGetDpiForWindow.Find() == nil {
		if dpi := int(call(procGetDpiForWindow, hwnd)); dpi > 0 {
			return dpi
		}
	}
	if monitor != 0 && procGetDpiForMonitor.Find() == nil {
		var x, y uint32
		// GetDpiForMonitor writes both UINTs before it returns and keeps
		// neither address, so x and y have to outlive nothing but this call —
		// which is exactly what [callPtr] is for, and why it is not [call].
		hr := callPtr(procGetDpiForMonitor, monitor, mdtEffectiveDPI,
			uintptr(unsafe.Pointer(&x)), uintptr(unsafe.Pointer(&y))) //nolint:gosec // G103: pinned by callPtr
		if hr == 0 && x > 0 {
			return int(x)
		}
	}
	return desktopDPI()
}

// desktopDPI is the pre-8.1 answer, and the last one available: the DPI of the
// screen device context, which is desktop-wide.
func desktopDPI() int {
	hdc := call(procGetDC, 0)
	if hdc == 0 {
		return baselineDPI
	}
	// ReleaseDC's result says only whether the DC was released, and a screen
	// DC that failed to release is not something this program can act on.
	defer call(procReleaseDC, 0, hdc)

	if dpi := int(call(procGetDeviceCaps, hdc, logPixelsX)); dpi > 0 {
		return dpi
	}
	return baselineDPI
}

// workAreaOf returns the monitor's work area. A zero rect means the display
// would not say, and [fitWithin] treats that as "do not clamp" rather than as
// "clamp to nothing".
func workAreaOf(monitor uintptr) rect {
	if monitor == 0 {
		return rect{}
	}
	info := monitorInfo{cbSize: uint32(unsafe.Sizeof(monitorInfo{}))}
	// GetMonitorInfoW fills the struct in place — cbSize is how it knows how
	// much of it to fill — and forgets the address on return, so info has to
	// survive only this call. [callPtr], not [call], is what makes that true.
	//nolint:gosec // G103: the struct is heap-pinned for the call by callPtr
	if callPtr(procGetMonitorInfo, monitor, uintptr(unsafe.Pointer(&info))) == 0 {
		return rect{}
	}
	return info.rcWork
}

// frameFor returns how much wider and taller than its client area a window
// with these styles is, at dpi.
//
// AdjustWindowRectExForDpi is asked first because it is the only one that can
// answer for a DPI other than the one the calling thread is on, which is what
// WM_DPICHANGED needs. Windows before 10 1607 has only the DPI-blind form,
// and on such a machine the whole desktop has one scale factor anyway.
func frameFor(style, exStyle uintptr, dpi int) insets {
	r := rect{left: 0, top: 0, right: 1000, bottom: 1000}
	if !adjustWindowRect(&r, style, exStyle, dpi) {
		return insets{}
	}
	return insets{
		w: int(r.right-r.left) - 1000,
		h: int(r.bottom-r.top) - 1000,
	}
}

// adjustWindowRect grows r from a client rect into a window rect, and reports
// whether Windows answered.
//
// Both forms take the RECT in place, read it, widen it and return, holding on
// to nothing — so r has to outlive only the call it is passed to. Both go
// through [callPtr] to make that the truth rather than the hope: r is a
// caller's local, and the directive on callPtr is what moves it to the heap
// and keeps it there for the syscall.
func adjustWindowRect(r *rect, style, exStyle uintptr, dpi int) bool {
	if procAdjustWindowRectExForDpi.Find() == nil {
		ok := callPtr(procAdjustWindowRectExForDpi,
			uintptr(unsafe.Pointer(r)), style, 0, exStyle, uintptr(dpi)) //nolint:gosec // G103: pinned by callPtr
		if ok != 0 {
			return true
		}
	}
	//nolint:gosec // G103: the same rect, pinned for the call by callPtr
	return callPtr(procAdjustWindowRectEx, uintptr(unsafe.Pointer(r)), style, 0, exStyle) != 0
}

// windowStyles reads the style bits off a live window, so the frame is
// measured against the window Windows actually made rather than against the
// style the WebView2 library is believed to pass.
func windowStyles(hwnd uintptr) (style, exStyle uintptr) {
	get := procGetWindowLongPtr
	if get.Find() != nil {
		// Only 64-bit Windows exports GetWindowLongPtrW; on 32-bit the two are
		// the same call at the same index. Same story as setWindowProc.
		get = procGetWindowLong
	}
	return call(get, hwnd, gwlStyle), call(get, hwnd, gwlExStyle)
}

// layoutAt works out this window's geometry: the frame measured against the
// window's real styles, and the work area of the monitor it is on right now.
// A dpi of zero means "ask the display".
func (b *nativeBackend) layoutAt(dpi int) (windowLayout, display) {
	d := displayFor(b.hwnd)
	if dpi <= 0 {
		dpi = d.dpi
	}
	style, exStyle := windowStyles(b.hwnd)
	return layoutFor(b.cfg, dpi, frameFor(style, exStyle, dpi), d.work), d
}

// applyLayout sizes and places the window from the approved design.
//
// It runs once, immediately after the WebView2 library has created the window
// at whatever size it was handed and before the page has finished loading.
// Only the real HWND can say which monitor the window landed on and at what
// DPI, so the size the library was given is a guess and this is the correction.
func (b *nativeBackend) applyLayout() {
	l, d := b.layoutAt(0)
	b.place(l, "opening", d.dpi)
}

// changeDPI answers WM_DPICHANGED: the window has moved to a display with a
// different scale, WebView2 has already relaid the page out at the new
// devicePixelRatio, and the frame around it has to follow.
//
// lParam points at the rect Windows suggests — its own scaling of where the
// window was. Its position is kept and its size replaced, because the design
// says how many CSS pixels wide the column is and only the DPI has changed.
func (b *nativeBackend) changeDPI(wparam uintptr, lparam unsafe.Pointer) {
	// LOWORD(wParam) is the window's new DPI: WM_DPICHANGED puts the same
	// value in both words, and the low one is where its documentation says to
	// read it. Truncating to uint16 is that documented decode, not a narrowing
	// of a number that might be wider — the high word carries no DPI bits.
	dpi := int(uint16(wparam)) //nolint:gosec // G115: LOWORD is the field's own width
	suggested := rect{}
	if lparam != nil {
		suggested = *(*rect)(lparam)
	}

	d := displayFor(b.hwnd)
	if dpi <= 0 {
		dpi = d.dpi
	}
	style, exStyle := windowStyles(b.hwnd)
	frame := frameFor(style, exStyle, dpi)
	l := layoutForDPIChange(b.cfg, suggested, dpi, frame, d.work)
	b.place(l, "the display scale changed", dpi)
}

// place moves the window to a layout and records the minimum the window
// procedure reports for WM_GETMINMAXINFO.
//
// It runs on the window thread — SetWindowPos belongs to the thread that owns
// the window — which is also the only thread that reads minSize, so neither
// needs a lock.
func (b *nativeBackend) place(l windowLayout, why string, dpi int) {
	// The minimum is derived from a DPI and a frame that both came from the
	// operating system, so it goes through the saturating conversion rather
	// than a cast: a wrapped negative here is a minimum track size Windows
	// would enforce, and the user would meet it as a window that refuses to
	// resize. [asInt32] and [maxDPI] together are why that cannot happen.
	b.minSize = point{x: asInt32(l.min.w), y: asInt32(l.min.h)}

	s := l.bounds.size()
	// SetWindowPos takes X, Y, cx and cy as C ints, which the calling
	// convention passes in the low 32 bits of each register — so a negative
	// left, which is ordinary on a monitor to the left of the primary one,
	// has to arrive as its two's-complement form, and that is precisely what
	// converting an int32 to uintptr produces. Widening it any other way would
	// send Windows a window two billion pixels off the right of the desktop.
	//nolint:gosec // G115: the Win32 ABI's own signed 32-bit argument slots
	if call(procSetWindowPos, b.hwnd, 0,
		uintptr(l.bounds.left), uintptr(l.bounds.top), uintptr(s.w), uintptr(s.h),
		swpNoZOrder|swpNoActivate|swpNoOwnerZOrder) == 0 {
		// Nothing to recover: the window keeps whatever size it had, which is a
		// window at the wrong size rather than no window at all.
		b.log.Warn("sizing the window", "why", why, "width", s.w, "height", s.h)
		return
	}
	b.log.Debug("sized the window", "why", why, "dpi", dpi,
		"width", s.w, "height", s.h, "left", l.bounds.left, "top", l.bounds.top,
		"minWidth", l.min.w, "minHeight", l.min.h)
}
