//go:build windows

package window

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// The three ways Windows has been told about DPI awareness, newest first.
// shcore.dll arrived with 8.1 and holds the per-monitor call; the context form
// in user32 arrived with 10 1703 and is the only one that knows about V2.
var (
	shcore = windows.NewLazySystemDLL("shcore.dll")

	procSetProcessDpiAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness        = shcore.NewProc("SetProcessDpiAwareness")
	procSetProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
)

const (
	// dpiPerMonitorV2 is DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2, which is
	// the handle value -4, written as its unsigned form because Go refuses to
	// convert a negative constant to uintptr.
	//
	// V2 rather than V1 for two reasons this window depends on: V2 scales the
	// non-client area — the title bar and the resize border grow with the
	// display instead of staying 96-DPI sized on a 144-DPI screen — and V2
	// sends WM_DPICHANGED to child windows, which is what the WebView2
	// control is.
	dpiPerMonitorV2 = ^uintptr(3)

	// processPerMonitorDPIAware is PROCESS_PER_MONITOR_DPI_AWARE, the 8.1-era
	// spelling of the same wish, with no V2 to ask for.
	processPerMonitorDPIAware = 2
)

// setPerMonitorDPIAwareness declares the awareness for the whole process,
// taking the newest call this Windows has.
func setPerMonitorDPIAwareness() error {
	for _, declare := range []func() error{
		setDPIAwarenessContext,
		setDPIAwarenessShcore,
		setDPIAwareLegacy,
	} {
		err := declare()
		if err == nil || errors.Is(err, errDPIAlreadyDeclared) {
			return err
		}
		if !errors.Is(err, errNoPerMonitorDPI) {
			return err
		}
	}
	return errNoPerMonitorDPI
}

// setDPIAwarenessContext is the Windows 10 1703 call, and the only one that
// can ask for V2.
func setDPIAwarenessContext() error {
	if err := procSetProcessDpiAwarenessContext.Find(); err != nil {
		return fmt.Errorf("%w: %w", errNoPerMonitorDPI, err)
	}

	ok, _, err := procSetProcessDpiAwarenessContext.Call(dpiPerMonitorV2)
	if ok != 0 {
		return nil
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return errDPIAlreadyDeclared
	}
	return fmt.Errorf("SetProcessDpiAwarenessContext(PER_MONITOR_AWARE_V2): %w", err)
}

// setDPIAwarenessShcore is the Windows 8.1 call. It returns an HRESULT rather
// than a BOOL, so the error to look at is the return value.
func setDPIAwarenessShcore() error {
	if err := procSetProcessDpiAwareness.Find(); err != nil {
		return fmt.Errorf("%w: %w", errNoPerMonitorDPI, err)
	}

	hr, _, _ := procSetProcessDpiAwareness.Call(processPerMonitorDPIAware)
	switch hr {
	case 0: // S_OK
		return nil
	case 0x80070005: // E_ACCESSDENIED — set once already
		return errDPIAlreadyDeclared
	default:
		return fmt.Errorf("SetProcessDpiAwareness(PER_MONITOR): HRESULT %#08x", hr)
	}
}

// setDPIAwareLegacy is the last resort, and buys system awareness rather than
// per-monitor: one scale factor for the whole desktop, chosen at logon, and no
// WM_DPICHANGED. On a Windows old enough to need it that is all there is — the
// desktop had one scale factor too.
func setDPIAwareLegacy() error {
	if err := procSetProcessDPIAware.Find(); err != nil {
		return fmt.Errorf("%w: %w", errNoPerMonitorDPI, err)
	}

	if ok, _, err := procSetProcessDPIAware.Call(); ok == 0 {
		return fmt.Errorf("SetProcessDPIAware: %w", err)
	}
	return nil
}
