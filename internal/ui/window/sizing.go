package window

import "math"

// The size of this window is arithmetic over the approved design, not a pair
// of remembered numbers.
//
// frontend/styles.css lays the interface out as one fixed column — `.app {
// width: 520px }` — with a stacked variant below 519. Those are CSS pixels,
// the unit a page is written in, and a CSS pixel is a physical pixel only on a
// 96-DPI display. On this user's 144-DPI display it is 1.5 physical pixels,
// so a window measured in physical pixels has to be told the display's DPI
// before it can hold the design.
//
// Every number in this file is therefore in CSS pixels, and [scaleCSS] is the
// one place they become physical. Nothing here knows what an HWND is, which is
// what lets a build agent with no desktop check the arithmetic.

const (
	// baselineDPI is the DPI a CSS pixel is defined against. At 96 one CSS
	// pixel is one physical pixel; at 144 — 150 % — it is 1.5.
	baselineDPI = 96

	// maxDPI is the largest DPI a caller can mean.
	//
	// Windows delivers a window's new DPI in the low word of WM_DPICHANGED's
	// wParam, so 65535 is the largest value the window manager can express at
	// all, and no display comes near it: 500 % — the most scaling Windows
	// offers — is 480. A number above this one did not come from the window
	// manager, and [scaleCSS] treats it the way it treats a number below one,
	// as a DPI it could not read.
	//
	// It is a bound rather than a preference, and it is here because every
	// length in this file gets multiplied by it. Without it a display that
	// reports something odd turns the design's 800 CSS pixels into a length no
	// int32 holds, and the two places that number is handed back to Windows as
	// an int32 — the window rect, and the minimum track size WM_GETMINMAXINFO
	// reports — would take a negative. [asInt32] is the other half of the guard.
	maxDPI = 0xFFFF

	// designColumnCSS is the width of the content column in
	// frontend/styles.css. It is the approved design, not a preference, and the
	// window has to fit it rather than the other way round.
	designColumnCSS = 520

	// scrollbarCSS is the room a vertical scrollbar needs beside the column.
	//
	// Measured in the real window: window.innerWidth minus
	// document.documentElement.clientWidth is 15 at 96 DPI and 15 again at
	// 144, so Chromium's scrollbar is 15 CSS pixels whatever the display
	// scale. The column needs that room next to it: a viewport of exactly
	// designColumnCSS loses 15 of it to the scrollbar, `max-width: 100%`
	// shrinks the column to 505, and the design is narrower than approved.
	scrollbarCSS = 15

	// designHeightCSS is how tall the column's own content is, measured in the
	// real window at designColumnCSS on the tab that opens («Связь»): 800 CSS
	// pixels, with nothing left to scroll to.
	//
	// It is a ceiling rather than a target. No laptop work area is 800 CSS
	// pixels tall once the title bar is counted — this user's is 680 — so
	// [fitWithin] is what usually decides the height, and this number only
	// stops a 4K desktop from opening an absurdly tall column.
	designHeightCSS = 800

	// minColumnCSS is the narrowest the column may be dragged to.
	//
	// Below 519 the stacked variant in the @media block takes over, and 360 is
	// the width that variant is for: measured in the real window at 360 CSS
	// pixels the column fills the viewport exactly, the machine cards stack,
	// and nothing wraps into itself. The old minimum was 520 *physical*
	// pixels, which on this display left the page 332 CSS pixels wide —
	// inside the stacked variant, but by accident rather than by intent.
	minColumnCSS = 360

	// minHeightCSS is the shortest window the design still answers a question
	// in. Measured in the stacked variant at minColumnCSS, the header ends at
	// 50 CSS pixels, the tab strip at 105 and the whole first status card at
	// 454, so 460 is where «связь есть или нет» is still readable without
	// scrolling. Below it the window is a title bar and a scrollbar.
	minHeightCSS = 460

	// marginCSS is the gap kept between the window and the edge of the work
	// area when the design is taller than the display. Without it a clamped
	// window sits flush against the taskbar and reads as broken rather than as
	// placed.
	marginCSS = 16
)

// size is a width and a height in physical pixels.
type size struct{ w, h int }

// insets is how much wider and taller a window is than its client area — the
// resize border and the title bar — in physical pixels at one DPI. Per-Monitor
// V2 scales the frame with the rest of the window, so these change with the
// monitor and cannot be a constant.
type insets struct{ w, h int }

// rect is a Win32 RECT in physical pixels: left and top inclusive, right and
// bottom exclusive. It is declared here rather than beside the syscalls so
// that the placement arithmetic can be tested where there are no windows.
type rect struct{ left, top, right, bottom int32 }

// size returns how big the rect is.
//
// Each edge is widened before it is subtracted, not after: int32 arithmetic on
// two edges the operating system supplied can leave int32, and the difference
// is wanted as an int in any case.
func (r rect) size() size {
	return size{w: int(r.right) - int(r.left), h: int(r.bottom) - int(r.top)}
}

// windowLayout is the geometry one window opens with: where it goes, and the
// size the user may not drag it below.
type windowLayout struct {
	bounds rect
	min    size
}

// scaleCSS converts a length in CSS pixels to physical pixels at dpi,
// rounding to the nearest pixel.
//
// A dpi of zero or less means the caller could not read one, and the baseline
// is then the only honest answer: it is also the right one, because a process
// whose windows report 96 DPI is a process whose windows Windows is scaling
// itself, and whose page renders at devicePixelRatio 1. Reading the DPI from
// the window rather than from the monitor is what keeps this from
// double-scaling a page WebView2 has already scaled.
//
// A dpi above [maxDPI] is the same failure from the other end — a number no
// display and no window message can mean — and gets the same answer. The two
// bounds together keep the multiplication below to a size the rest of this
// file can hand back to Windows as an int32.
func scaleCSS(css, dpi int) int {
	if dpi <= 0 || dpi > maxDPI {
		dpi = baselineDPI
	}
	return (css*dpi + baselineDPI/2) / baselineDPI
}

// asInt32 fits a coordinate or a length into the int32 that a Win32 RECT field
// and a MINMAXINFO POINT hold, saturating rather than wrapping.
//
// Every number that reaches one of those fields is the sum of two things the
// operating system supplied: a coordinate is an edge of a monitor's work area
// plus a window size, and a size is a DPI-scaled design length plus the frame
// AdjustWindowRectExForDpi measured. Each addend is an int32 on its own, so
// either sum can leave the range although neither part did — and a plain
// conversion would wrap, giving a too-wide window a right edge to the left of
// its left edge, and a minimum track size a negative that WM_GETMINMAXINFO
// reads as a limit. Saturating leaves the number wrong in the direction it was
// already wrong in rather than inverting it.
//
// On a machine that reports its display honestly neither branch is reachable;
// [maxDPI] is what keeps the DPI from being the thing that overflows. The two
// ends are pinned by test anyway, because "no machine does that" is the
// assumption this guard exists to stop relying on.
func asInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	// Inside int32 by the two branches above. On a 32-bit build neither can
	// fire, because there the two types are the same width.
	return int32(n) //nolint:gosec // G115: bounded by the two branches above
}

// designClientCSS is the client area the design wants, in CSS pixels: the
// column, plus the room the scrollbar takes beside it.
func designClientCSS() size {
	return size{w: designColumnCSS + scrollbarCSS, h: designHeightCSS}
}

// minClientCSS is the smallest client area the design still works in.
func minClientCSS() size {
	return size{w: minColumnCSS + scrollbarCSS, h: minHeightCSS}
}

// windowFor turns a client area given in CSS pixels into a window size in
// physical pixels at dpi, with the frame added on.
func windowFor(clientCSS size, dpi int, f insets) size {
	return size{
		w: scaleCSS(clientCSS.w, dpi) + f.w,
		h: scaleCSS(clientCSS.h, dpi) + f.h,
	}
}

// fitWithin shrinks want so that it fits inside limit, and never below floor.
//
// A limit dimension of zero or less means the display would not say how big it
// is, and an unknown work area must not shrink a window to nothing, so it
// clamps nothing. A floor larger than the limit would win, which is why
// [layoutFor] passes the floor through here first: on a display too small for
// the design's own minimum the work area has to win, or the user would be left
// with a window they cannot fit on their screen.
func fitWithin(want, limit, floor size) size {
	out := want
	if limit.w > 0 && out.w > limit.w {
		out.w = limit.w
	}
	if limit.h > 0 && out.h > limit.h {
		out.h = limit.h
	}
	return size{w: max(out.w, floor.w), h: max(out.h, floor.h)}
}

// centreIn puts s in the middle of the work area, never off its top-left
// corner. A window wider or taller than the work area is pinned to the corner
// rather than centred into negative coordinates, where its title bar would be
// off-screen and unreachable.
func centreIn(s size, work rect) rect {
	return at(s,
		int(work.left)+(work.size().w-s.w)/2,
		int(work.top)+(work.size().h-s.h)/2,
		work)
}

// at places s with its top-left at (x, y), pulled back inside work if that
// would hang the window off an edge.
func at(s size, x, y int, work rect) rect {
	left, top := x, y
	left = min(left, int(work.right)-s.w)
	top = min(top, int(work.bottom)-s.h)
	left = max(left, int(work.left))
	top = max(top, int(work.top))
	// Saturating rather than converting: left and top are bounded by work,
	// which is an int32 rect already, but the two sums below are not — see
	// [asInt32].
	return rect{
		left:   asInt32(left),
		top:    asInt32(top),
		right:  asInt32(left + s.w),
		bottom: asInt32(top + s.h),
	}
}

// layoutFor works out the geometry of a window opening on one display.
//
//	dpi   the DPI of the monitor the window is on, in that window's own
//	      coordinate space — see [scaleCSS]
//	f     the non-client frame at that DPI, in physical pixels
//	work  the work area of that monitor, in physical pixels: the screen minus
//	      the taskbar, which is what a window may occupy
//
// cfg's sizes are overrides in physical pixels, and zero means "derive it".
// The design decides otherwise, so nothing here is a remembered number.
func layoutFor(cfg Config, dpi int, f insets, work rect) windowLayout {
	margin := 2 * scaleCSS(marginCSS, dpi)
	limit := size{w: work.size().w - margin, h: work.size().h - margin}

	// The minimum is clamped to the display before anything is measured
	// against it. A minimum bigger than the screen is a window the user cannot
	// drag down to a size that fits, which is worse than a window narrower
	// than the design.
	minimum := windowFor(minClientCSS(), dpi, f)
	minimum.w = positiveOr(cfg.MinWidth, minimum.w)
	minimum.h = positiveOr(cfg.MinHeight, minimum.h)
	minimum = fitWithin(minimum, limit, size{})

	want := windowFor(designClientCSS(), dpi, f)
	want.w = positiveOr(cfg.Width, want.w)
	want.h = positiveOr(cfg.Height, want.h)

	return windowLayout{bounds: centreIn(fitWithin(want, limit, minimum), work), min: minimum}
}

// layoutForDPIChange is what WM_DPICHANGED asks for.
//
// Dragging the window from a 100 % monitor to a 150 % one is an ordinary thing
// to do, and Windows suggests a rect that has merely been scaled. The size is
// re-derived from the design at the new DPI instead — the page relaid out at
// the new devicePixelRatio the moment the message arrived, so the column is
// now a different number of physical pixels wide — while the position Windows
// suggests is kept, because the window is where the user dragged it and
// re-centring it under their mouse would be rude.
func layoutForDPIChange(cfg Config, suggested rect, dpi int, f insets, work rect) windowLayout {
	fresh := layoutFor(cfg, dpi, f, work)
	moved := at(fresh.bounds.size(), int(suggested.left), int(suggested.top), work)
	return windowLayout{bounds: moved, min: fresh.min}
}

// positiveOr treats zero and negative sizes as "not set". A caller that means
// "as small as possible" says so through MinWidth, not through a negative.
func positiveOr(v, fallback int) int {
	if v > 0 {
		return v
	}
	return fallback
}
