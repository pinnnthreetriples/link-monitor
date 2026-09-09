//go:build windows

package window

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows"
)

// The user32 entry points this package needs. x/sys/windows wraps the service
// manager and the registry but not the window manager, so they are declared
// here rather than reached for through the WebView2 library's own internal
// bindings, which are not ours to depend on.
var (
	user32 = windows.NewLazySystemDLL("user32.dll")

	procSetWindowLongPtr    = user32.NewProc("SetWindowLongPtrW")
	procSetWindowLong       = user32.NewProc("SetWindowLongW")
	procCallWindowProc      = user32.NewProc("CallWindowProcW")
	procDefWindowProc       = user32.NewProc("DefWindowProcW")
	procShowWindow          = user32.NewProc("ShowWindow")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procIsIconic            = user32.NewProc("IsIconic")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
)

// gwlpWndProc is the GWLP_WNDPROC index, which is -4, written as its unsigned
// form because Go refuses to convert a negative constant to uintptr.
const gwlpWndProc = ^uintptr(3)

// point is a Win32 POINT.
type point struct {
	x, y int32
}

// minMaxInfo is a Win32 MINMAXINFO, the structure WM_GETMINMAXINFO carries in
// its lParam for the window to fill in.
type minMaxInfo struct {
	reserved     point
	maxSize      point
	maxPosition  point
	minTrackSize point
	maxTrackSize point
}

// nativeBackend is the real window: a WebView2 control, its HWND, and the
// window procedure we put in front of the library's own.
type nativeBackend struct {
	wv       webview2.WebView
	hwnd     uintptr
	origProc uintptr
	onHide   func()
	log      *slog.Logger

	// cfg is kept because the size has to be worked out again every time the
	// display scale changes, and cfg carries the caller's overrides.
	cfg Config
	// minSize is what WM_GETMINMAXINFO reports. It is re-derived at each DPI,
	// and only the window thread touches it — see [nativeBackend.place].
	minSize point

	// icons are the HICONs handed to WM_SETICON. Setting an icon transfers no
	// ownership, so they stay ours to destroy.
	icons       []uintptr
	destroyOnce sync.Once
}

// backends maps an HWND to the backend that owns it.
//
// One shared callback and a lookup, rather than a callback per window: a
// callback made with windows.NewCallback can never be freed and a process is
// limited to a couple of thousand of them, so a window that were ever rebuilt
// would leak a resource nothing can reclaim. The map costs one read lock per
// message and cannot leak.
var (
	backendsMu sync.RWMutex
	backends   = map[uintptr]*nativeBackend{}

	// sharedProc is the single trampoline Windows calls. It is built on first
	// use and lives for the life of the process.
	sharedProc = sync.OnceValue(func() uintptr { return windows.NewCallback(windowProc) })
)

// newNativeBackend creates the window and everything hanging off it. It must
// be called on the thread that will pump the message loop: the control, the
// HWND and the loop all belong to that one thread.
// What the page in frontend/desktop.js asks of the control, and what of it
// can be delivered from here:
//
//   - Default context menus ENABLED, so the input fields keep cut, copy and
//     paste. Not deliverable. The library ties AreDefaultContextMenusEnabled
//     and AreDevToolsEnabled to its single Debug flag, so context menus can
//     only be bought together with F12 opening developer tools — a browser
//     tell in a window whose whole point is not to look like a browser.
//     Debug stays false: no menus, no developer tools.
//   - Status bar DISABLED, so a hovered link cannot draw a browser bubble.
//     Not deliverable either: IsStatusBarEnabled lives on
//     ICoreWebView2Settings, which the library keeps behind an unexported
//     field with no accessor, and it stays at the WebView2 default.
//   - Browser accelerator keys ENABLED, so F5 and Ctrl+Tab reach the page.
//     This one is the WebView2 default and the library never touches it.
//
// Reaching the settings would mean building the control through the public
// pkg/edge package with our own window class, message pump and binding
// plumbing — a reimplementation of the library's own webview.go, and several
// hundred lines of syscalls that no test on a build agent can exercise.
func newNativeBackend(cfg Config, onHide func()) (backend, error) {
	// The size handed to the library is a guess, made against the primary
	// monitor because that is the one the library centres on. It is a guess
	// because only a real HWND can be asked which monitor it landed on and at
	// what DPI, and the library creates and shows the window in one call.
	// [nativeBackend.applyLayout] corrects it a few lines below, before the
	// page has painted.
	primary := displayFor(0)
	guess := layoutFor(cfg, primary.dpi,
		frameFor(wsOverlappedWindow, 0, primary.dpi), primary.work).bounds.size()

	wv := webview2.NewWithOptions(webview2.WebViewOptions{
		DataPath:  cfg.DataDir,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  cfg.Title,
			Width:  uint(guess.w),
			Height: uint(guess.h),
			Center: true,
		},
	})
	// NewWithOptions reports failure by returning nil rather than an error.
	// The only failure that is not a programming mistake is a runtime that is
	// not there: the loader is found neither on disk nor in the bundled copy.
	if wv == nil {
		return nil, fmt.Errorf("creating the WebView2 control: %w", ErrRuntimeMissing)
	}

	b := &nativeBackend{
		wv:     wv,
		hwnd:   uintptr(wv.Window()),
		onHide: onHide,
		cfg:    cfg,
		log:    cfg.Logger,
	}
	if b.hwnd == 0 {
		wv.Destroy()
		return nil, fmt.Errorf("the WebView2 control has no window: %w", ErrRuntimeMissing)
	}

	if err := b.subclass(); err != nil {
		b.destroy()
		return nil, err
	}
	// The window exists now, so it can be asked what it is sitting on. This is
	// where the design becomes pixels; everything before it was a guess.
	b.applyLayout()
	b.applyIcon(cfg.Icon)

	// Binding has to happen before the first navigation: it works by adding a
	// script that runs when a document is created, and the document about to
	// be created is the one that will call it.
	if err := wv.Bind(externalBindName, newExternalLinks(cfg).handle); err != nil {
		b.destroy()
		return nil, fmt.Errorf("binding window.%s: %w", externalBindName, err)
	}

	wv.Navigate(cfg.URL)
	return b, nil
}

// subclass puts our window procedure in front of the library's, keeping the
// library's so that every message we do not handle still reaches it — resizing
// the browser widget, moving it, handing it focus.
func (b *nativeBackend) subclass() error {
	registerBackend(b.hwnd, b)

	prev, err := setWindowProc(b.hwnd, sharedProc())
	if err != nil {
		forgetBackend(b.hwnd)
		return fmt.Errorf("subclassing the window: %w", err)
	}
	b.origProc = prev
	return nil
}

func (b *nativeBackend) run() { b.wv.Run() }

func (b *nativeBackend) dispatch(f func()) { b.wv.Dispatch(f) }

// terminate makes run return, from any goroutine.
//
// The library's Terminate is PostQuitMessage, which posts to the *calling*
// thread's queue: called straight from a background goroutine it would quit a
// thread that is pumping nothing, and the window would stay up. Dispatch hops
// to the window thread first, which is the only queue run is reading. A
// terminate that arrives before run starts is fine — the thread message waits
// in the queue until it does.
func (b *nativeBackend) terminate() { b.wv.Dispatch(b.wv.Terminate) }

// show makes the window visible and brings it to the front. A window the user
// minimised has to be restored rather than merely shown, or it would come back
// as a taskbar button with nothing behind it.
func (b *nativeBackend) show() {
	command := uintptr(windows.SW_SHOW)
	if call(procIsIconic, b.hwnd) != 0 {
		command = windows.SW_RESTORE
	}
	call(procShowWindow, b.hwnd, command)
	call(procSetForegroundWindow, b.hwnd)
}

func (b *nativeBackend) hide() { call(procShowWindow, b.hwnd, windows.SW_HIDE) }

// destroy releases the window. It runs on the window thread, after run has
// returned, and does its work once however often it is called.
func (b *nativeBackend) destroy() {
	b.destroyOnce.Do(func() {
		if b.origProc != 0 {
			// Put the library's procedure back before the window goes away, so
			// the teardown messages reach the code that built the control.
			if _, err := setWindowProc(b.hwnd, b.origProc); err != nil {
				b.log.Warn("restoring the window procedure", "error", err)
			}
		}
		forgetBackend(b.hwnd)

		// The library's Destroy only posts WM_CLOSE, which needs a message
		// loop that has already returned — and which this window's procedure
		// answers by hiding. DestroyWindow is the thing itself, and this is
		// the thread that owns the window, the only one allowed to call it.
		call(procDestroyWindow, b.hwnd)
		b.releaseIcons()
	})
}

// windowProc is the trampoline Windows calls for every subclassed window.
//
// lParam is declared as an unsafe.Pointer rather than a uintptr because for
// WM_GETMINMAXINFO that is what it is — the address of a MINMAXINFO on
// Windows' own stack. Taking it as a pointer from the start means this package
// never converts an integer into a pointer, which is a thing no amount of
// commentary makes safe to read.
func windowProc(hwnd, msg, wparam uintptr, lparam unsafe.Pointer) uintptr {
	b := lookupBackend(hwnd)
	if b == nil {
		// The window has already been handed back: there is no original
		// procedure left to reach and nothing of ours left to decide.
		return call(procDefWindowProc, hwnd, msg, wparam, uintptr(lparam))
	}
	return b.handle(hwnd, msg, wparam, lparam)
}

// handle acts on one message. The decision itself is [decideProcAction], which
// knows nothing about windows and is tested on its own.
func (b *nativeBackend) handle(hwnd, msg, wparam uintptr, lparam unsafe.Pointer) uintptr {
	// A window message id is a UINT. Windows passes it in a register that the
	// callback signature spells uintptr, so on 64-bit the upper half is
	// padding rather than message: truncating to uint32 is reading the field
	// Windows filled in, and [decideProcAction] takes it in that width so the
	// decision can be tested on a platform with no windows at all.
	switch decideProcAction(uint32(msg)) { //nolint:gosec // G115: a UINT message id in a register
	case actionHide:
		b.hide()
		if b.onHide != nil {
			b.onHide()
		}
		return 0
	case actionMinSize:
		b.clampMinSize(lparam)
		return 0
	case actionResize:
		b.changeDPI(wparam, lparam)
		return 0
	case actionDefault:
		return b.callOriginal(hwnd, msg, wparam, lparam)
	default:
		return b.callOriginal(hwnd, msg, wparam, lparam)
	}
}

// clampMinSize fills in the smallest size the user may drag the window to.
//
// Windows has already put its own defaults in the structure it points us at —
// a MINMAXINFO on its own stack, which it reads back the moment this procedure
// returns — so writing one field and returning zero is the whole of handling
// the message.
func (b *nativeBackend) clampMinSize(lparam unsafe.Pointer) {
	if lparam == nil || b.minSize.x <= 0 || b.minSize.y <= 0 {
		return
	}
	(*minMaxInfo)(lparam).minTrackSize = b.minSize
}

// callOriginal hands a message to the procedure we subclassed.
func (b *nativeBackend) callOriginal(hwnd, msg, wparam uintptr, lparam unsafe.Pointer) uintptr {
	if b.origProc == 0 {
		return call(procDefWindowProc, hwnd, msg, wparam, uintptr(lparam))
	}
	return call(procCallWindowProc, b.origProc, hwnd, msg, wparam, uintptr(lparam))
}

// setWindowProc installs a window procedure and returns the previous one.
func setWindowProc(hwnd, proc uintptr) (uintptr, error) {
	set := procSetWindowLongPtr
	if err := set.Find(); err != nil {
		// Only 64-bit Windows exports SetWindowLongPtrW. On 32-bit the two are
		// the same call, at the same index, with a pointer of the same width.
		set = procSetWindowLong
	}

	prev, _, err := set.Call(hwnd, gwlpWndProc, proc)
	if prev == 0 && !errors.Is(err, windows.ERROR_SUCCESS) {
		return 0, fmt.Errorf("setting the window procedure: %w", err)
	}
	return prev, nil
}

// call invokes a user32 entry point and returns its result.
//
// The error is dropped on purpose: LazyProc.Call always hands back a
// syscall.Errno, zero-valued on success, and every caller here either reads
// the return value itself or is making a window-state change with no recovery
// and nothing to report — the window is either still up, in which case the
// call worked, or already gone, in which case the message loop is on its way
// out.
// It must not be handed the address of Go memory. Use [callPtr] for that, and
// read its comment before deciding which one a new call site wants.
func call(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

// callPtr is [call] for an entry point that writes through an address of ours.
// It exists for its directive and nothing else; the error is dropped for the
// same reason [call] drops it.
//
// //go:uintptrescapes tells the compiler that a uintptr argument may be a
// pointer converted at the call site, so the value it names has to be moved to
// the heap and kept alive until the call returns — the guarantee that makes
// `uintptr(unsafe.Pointer(&x))` legal at all. x/sys/windows puts that
// directive on LazyProc.Call, but it only reaches conversions written in the
// annotated function's own argument list: route one through [call], an
// ordinary variadic function, and escape analysis sees a plain integer, leaves
// x on the goroutine's stack, and keeps nothing alive. `go build -gcflags=-m`
// says so — the locals here moved to the heap only once this wrapper existed.
// Windows would then be writing a DPI or a MONITORINFO through an address the
// runtime is free to have abandoned, because a stack growth anywhere between
// the conversion and the syscall relocates the frame it points into.
//
// Neither go vet nor gosec catches it: the code looks like every other
// syscall, and the rule it breaks is one about which function the conversion
// is written inside.
//
//go:uintptrescapes
func callPtr(p *windows.LazyProc, args ...uintptr) uintptr {
	r, _, _ := p.Call(args...)
	return r
}

func registerBackend(hwnd uintptr, b *nativeBackend) {
	backendsMu.Lock()
	defer backendsMu.Unlock()

	backends[hwnd] = b
}

func forgetBackend(hwnd uintptr) {
	backendsMu.Lock()
	defer backendsMu.Unlock()

	delete(backends, hwnd)
}

func lookupBackend(hwnd uintptr) *nativeBackend {
	backendsMu.RLock()
	defer backendsMu.RUnlock()

	return backends[hwnd]
}
