package window

import (
	"errors"
	"fmt"
)

// errDPIAlreadyDeclared reports that this process's DPI awareness was fixed
// before we asked — by an application manifest, or by a library that got there
// first. Windows lets it be set once.
var errDPIAlreadyDeclared = errors.New("the process DPI awareness was already declared")

// errNoPerMonitorDPI reports a Windows too old to be told about per-monitor
// DPI at all. Per-monitor awareness arrived in 8.1 and the V2 context in 10
// 1703; there is nothing to declare before that, and nothing lost either,
// because such a machine has one scale factor for the whole desktop.
var errNoPerMonitorDPI = errors.New("this Windows has no per-monitor DPI awareness to declare")

// DeclarePerMonitorDPI tells Windows that this process positions and paints
// its own windows in physical pixels, per monitor, and takes WM_DPICHANGED
// when one of them moves to a display with a different scale.
//
// It must be called before the process owns a window, which is why it is a
// package function rather than something [New] or [Window.Run] does: the tray
// icon owns a window too, and it is created moments later.
//
// Why it is needed, measured on this user's 144-DPI (150 %) display rather
// than reasoned about. The WebView2 library declares no awareness and Go
// links no manifest, so the process was DPI-unaware: GetDeviceCaps(LOGPIXELSX)
// answered 96, GetDpiForWindow answered 96, and the page reported
// devicePixelRatio 1 — one CSS pixel drawn as one physical pixel, the design
// laid out at a third of the size everything else on the desktop is drawn at,
// and then bitmap-stretched by the compositor to 1560x1080 physical pixels: a
// blurry window 60 physical pixels taller than the whole 1020-pixel work area,
// with the 520-pixel column filling 51 % of it and dead space either side.
//
// With this call the same page reports devicePixelRatio 1.5 and WebView2 lays
// the design out at the display's own scale. That is the reason nothing in
// this package scales the page by hand: the fallback of zooming the document
// from the host would double-scale exactly here, where it is not needed.
func DeclarePerMonitorDPI() error {
	return declareDPI(setPerMonitorDPIAwareness)
}

// declareDPI is [DeclarePerMonitorDPI] with the syscall lifted out, so the
// three answers that matter can each be tested: it worked, something declared
// an awareness first, and this Windows has none to declare.
//
// None of them is fatal, and the caller is expected to log rather than give
// up. A process that stays DPI-unaware still gets a window that fits: the
// arithmetic in sizing.go reads the DPI of the window itself, which on an
// unaware process is 96, and 96 is the DPI its coordinate space really has.
func declareDPI(set func() error) error {
	err := set()
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errDPIAlreadyDeclared), errors.Is(err, errNoPerMonitorDPI):
		return err
	default:
		return fmt.Errorf("declaring per-monitor DPI awareness: %w", err)
	}
}
