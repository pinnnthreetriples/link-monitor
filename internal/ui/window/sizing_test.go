package window

import (
	"math"
	"testing"
)

// The two displays these tests are written against.
//
// laptop144 is this user's real machine, measured rather than imagined: a
// 1920x1080 panel at 144 DPI (150 %), whose work area is 1920x1020 once the
// taskbar is taken off, and whose window frame at that DPI is 22 physical
// pixels wide and 56 tall — GetWindowRect 1040x720 against GetClientRect
// 1018x664 on the real window.
//
// desk96 is the same panel at 100 %, which is what a build agent, a second
// monitor or a user who moves the slider has.
var (
	laptop144work  = rect{left: 0, top: 0, right: 1920, bottom: 1020}
	laptop144frame = insets{w: 22, h: 56}

	desk96work  = rect{left: 0, top: 0, right: 1920, bottom: 1040}
	desk96frame = insets{w: 16, h: 39}
)

func TestScaleCSSTurnsTheDesignIntoPixels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		css  int
		dpi  int
		want int
	}{
		{name: "at the baseline a CSS pixel is a pixel", css: 520, dpi: 96, want: 520},
		{name: "125 % rounds to the nearest pixel", css: 520, dpi: 120, want: 650},
		{name: "150 %, this user's display", css: 520, dpi: 144, want: 780},
		{name: "175 % rounds up from 910.0", css: 520, dpi: 168, want: 910},
		{name: "200 %", css: 520, dpi: 192, want: 1040},
		{name: "the column plus its scrollbar at 150 %", css: 535, dpi: 144, want: 803},
		{name: "the scrollbar itself stays 15 CSS pixels", css: 15, dpi: 144, want: 23},
		{name: "half a pixel rounds up, not down", css: 1, dpi: 144, want: 2},
		{name: "an unreadable DPI falls back to the baseline", css: 535, dpi: 0, want: 535},
		{name: "a negative DPI does too", css: 535, dpi: -1, want: 535},
		{name: "zero stays zero", css: 0, dpi: 144, want: 0},
		// The upper bound. 500 % — the most scaling Windows offers — is 480,
		// so the last DPI accepted is three orders of magnitude past any real
		// display and still multiplies out to a length int32 holds.
		{name: "the largest DPI a message carries", css: 535, dpi: maxDPI, want: (535*maxDPI + 48) / 96},
		{name: "one past it is a DPI that was not read", css: 535, dpi: maxDPI + 1, want: 535},
		{name: "and so is a wildly out-of-range one", css: 535, dpi: math.MaxInt32, want: 535},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := scaleCSS(tc.css, tc.dpi); got != tc.want {
				t.Errorf("scaleCSS(%d, %d) = %d, want %d", tc.css, tc.dpi, got, tc.want)
			}
		})
	}
}

// TestTheDesignsClientWidthLeavesRoomForTheScrollbar is the arithmetic the
// whole fix turns on, stated on its own: the client area is the column plus
// the scrollbar, never just the column.
func TestTheDesignsClientWidthLeavesRoomForTheScrollbar(t *testing.T) {
	t.Parallel()

	if got := designClientCSS().w; got != designColumnCSS+scrollbarCSS {
		t.Errorf("designClientCSS().w = %d, want %d + %d",
			got, designColumnCSS, scrollbarCSS)
	}
	// Measured in the real window at this client width: clientWidth came back
	// 521 CSS pixels and `.app` rendered at 520 — 99.8 % of the viewport.
	if got := scaleCSS(designClientCSS().w, 144); got != 803 {
		t.Errorf("the design's client width at 144 DPI = %d, want the measured 803", got)
	}
	if got := minClientCSS().w; got != minColumnCSS+scrollbarCSS {
		t.Errorf("minClientCSS().w = %d, want %d + %d", got, minColumnCSS, scrollbarCSS)
	}
	// The minimum has to sit inside the stacked variant on purpose, which means
	// the column it leaves must be at or below the media query's 519.
	if minColumnCSS > 519 {
		t.Errorf("minColumnCSS = %d, which is wider than the stacked variant's 519",
			minColumnCSS)
	}
}

func TestWindowForAddsTheFrameToTheScaledClientArea(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		client size
		dpi    int
		frame  insets
		want   size
	}{
		{
			name:   "the design on this user's display",
			client: size{w: 535, h: 800}, dpi: 144, frame: laptop144frame,
			want: size{w: 803 + 22, h: 1200 + 56},
		},
		{
			name:   "the design at 100 %",
			client: size{w: 535, h: 800}, dpi: 96, frame: desk96frame,
			want: size{w: 535 + 16, h: 800 + 39},
		},
		{
			name:   "no frame at all, which is what a failed AdjustWindowRect gives",
			client: size{w: 535, h: 800}, dpi: 96, frame: insets{},
			want: size{w: 535, h: 800},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := windowFor(tc.client, tc.dpi, tc.frame); got != tc.want {
				t.Errorf("windowFor(%+v, %d, %+v) = %+v, want %+v",
					tc.client, tc.dpi, tc.frame, got, tc.want)
			}
		})
	}
}

func TestFitWithinClampsToTheWorkAreaButNeverBelowTheMinimum(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name               string
		want, limit, floor size
		expect             size
	}{
		{
			name: "a window that already fits is left alone",
			want: size{w: 825, h: 700}, limit: size{w: 1872, h: 972},
			floor: size{w: 562, h: 690}, expect: size{w: 825, h: 700},
		},
		{
			name: "the design is taller than this display, so the height gives way",
			want: size{w: 825, h: 1256}, limit: size{w: 1872, h: 972},
			floor: size{w: 562, h: 690}, expect: size{w: 825, h: 972},
		},
		{
			name: "both dimensions can be clamped at once",
			want: size{w: 2000, h: 1256}, limit: size{w: 1872, h: 972},
			floor: size{w: 562, h: 690}, expect: size{w: 1872, h: 972},
		},
		{
			name: "the minimum wins over a work area too small for the design",
			want: size{w: 825, h: 1256}, limit: size{w: 400, h: 300},
			floor: size{w: 562, h: 690}, expect: size{w: 562, h: 690},
		},
		{
			name: "a size below the minimum is raised to it",
			want: size{w: 200, h: 200}, limit: size{w: 1872, h: 972},
			floor: size{w: 562, h: 690}, expect: size{w: 562, h: 690},
		},
		{
			name: "an unknown work area does not clamp anything",
			want: size{w: 825, h: 1256}, limit: size{},
			floor: size{w: 562, h: 690}, expect: size{w: 825, h: 1256},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := fitWithin(tc.want, tc.limit, tc.floor); got != tc.expect {
				t.Errorf("fitWithin(%+v, %+v, %+v) = %+v, want %+v",
					tc.want, tc.limit, tc.floor, got, tc.expect)
			}
		})
	}
}

func TestCentreInPutsTheWindowInTheWorkArea(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		s    size
		work rect
		want rect
	}{
		{
			name: "the design on this user's display",
			s:    size{w: 825, h: 972}, work: laptop144work,
			want: rect{left: 547, top: 24, right: 1372, bottom: 996},
		},
		{
			name: "a work area that does not start at the origin — a second monitor",
			s:    size{w: 800, h: 600}, work: rect{left: 1920, top: 0, right: 3200, bottom: 1000},
			want: rect{left: 2160, top: 200, right: 2960, bottom: 800},
		},
		{
			name: "a taskbar down the left edge shifts the work area",
			s:    size{w: 800, h: 600}, work: rect{left: 100, top: 0, right: 1900, bottom: 1000},
			want: rect{left: 600, top: 200, right: 1400, bottom: 800},
		},
		{
			name: "a window taller than the work area is pinned, not centred off-screen",
			s:    size{w: 825, h: 1200}, work: laptop144work,
			want: rect{left: 547, top: 0, right: 1372, bottom: 1200},
		},
		{
			name: "an unknown work area still puts the window somewhere reachable",
			s:    size{w: 825, h: 972}, work: rect{},
			want: rect{left: 0, top: 0, right: 825, bottom: 972},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := centreIn(tc.s, tc.work); got != tc.want {
				t.Errorf("centreIn(%+v, %+v) = %+v, want %+v", tc.s, tc.work, got, tc.want)
			}
		})
	}
}

// TestLayoutForDerivesTheWholeGeometry is the test the fix is judged by: given
// a DPI, a frame and a work area, what window comes out. No HWND anywhere.
func TestLayoutForDerivesTheWholeGeometry(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		cfg   Config
		dpi   int
		frame insets
		work  rect
		want  windowLayout
	}{
		{
			name: "this user's 144-DPI laptop: 520 CSS pixels of column, clamped in height",
			dpi:  144, frame: laptop144frame, work: laptop144work,
			// 535 CSS px of client at 144 DPI is 803, plus 22 of frame = 825.
			// The design's 800 CSS px of content would want 1256 tall, which is
			// more than the 1020-pixel work area less a 2x24 margin, so 972.
			want: windowLayout{
				bounds: rect{left: 547, top: 24, right: 1372, bottom: 996},
				min:    size{w: 585, h: 746},
			},
		},
		{
			name: "the same panel at 100 %, where the design fits whole",
			dpi:  96, frame: desk96frame, work: desk96work,
			// 535 + 16 = 551 wide, 800 + 39 = 839 tall, inside 1920x1040 - 32.
			want: windowLayout{
				bounds: rect{left: 684, top: 100, right: 1235, bottom: 939},
				min:    size{w: 391, h: 499},
			},
		},
		{
			name: "an explicit size is honoured, and still centred",
			cfg:  Config{Width: 900, Height: 800},
			dpi:  144, frame: laptop144frame, work: laptop144work,
			want: windowLayout{
				bounds: rect{left: 510, top: 110, right: 1410, bottom: 910},
				min:    size{w: 585, h: 746},
			},
		},
		{
			name: "an explicit size below the minimum is raised to it",
			cfg:  Config{Width: 200, Height: 200},
			dpi:  144, frame: laptop144frame, work: laptop144work,
			want: windowLayout{
				bounds: rect{left: 667, top: 137, right: 1252, bottom: 883},
				min:    size{w: 585, h: 746},
			},
		},
		{
			name: "an explicit minimum overrides the design's",
			cfg:  Config{MinWidth: 640, MinHeight: 600},
			dpi:  144, frame: laptop144frame, work: laptop144work,
			want: windowLayout{
				bounds: rect{left: 547, top: 24, right: 1372, bottom: 996},
				min:    size{w: 640, h: 600},
			},
		},
		{
			name: "a DPI-unaware process, where the window's own space really is 96 DPI",
			dpi:  96, frame: insets{w: 16, h: 39},
			work: rect{left: 0, top: 0, right: 1280, bottom: 680},
			want: windowLayout{
				bounds: rect{left: 364, top: 16, right: 915, bottom: 664},
				min:    size{w: 391, h: 499},
			},
		},
		{
			name: "a display too small for the design's own minimum: the work area wins",
			dpi:  288, frame: insets{w: 48, h: 117},
			work: rect{left: 0, top: 0, right: 1280, bottom: 680},
			want: windowLayout{
				bounds: rect{left: 48, top: 48, right: 1232, bottom: 632},
				min:    size{w: 1173, h: 584},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := layoutFor(tc.cfg, tc.dpi, tc.frame, tc.work)
			if got != tc.want {
				t.Errorf("layoutFor(%+v, %d, %+v, %+v) =\n\t%+v\nwant\n\t%+v",
					tc.cfg, tc.dpi, tc.frame, tc.work, got, tc.want)
			}
		})
	}
}

// TestLayoutForNeverOpensLargerThanTheWorkArea is the third fault stated as an
// invariant rather than as one example: whatever the DPI, whatever the display,
// the window fits on the monitor it appears on with room to spare.
func TestLayoutForNeverOpensLargerThanTheWorkArea(t *testing.T) {
	t.Parallel()

	for _, dpi := range []int{96, 120, 144, 168, 192, 240, 288} {
		for _, work := range []rect{
			laptop144work,
			desk96work,
			{left: 0, top: 0, right: 1280, bottom: 680},
			{left: 1920, top: -200, right: 3840, bottom: 880},
			{left: 0, top: 0, right: 3840, bottom: 2100},
		} {
			frame := insets{w: scaleCSS(16, dpi), h: scaleCSS(39, dpi)}
			got := layoutFor(Config{}, dpi, frame, work).bounds

			if got.left < work.left || got.top < work.top {
				t.Errorf("at %d DPI in %+v the window starts off the work area: %+v",
					dpi, work, got)
			}
			if got.right > work.right || got.bottom > work.bottom {
				t.Errorf("at %d DPI in %+v the window runs past the work area: %+v",
					dpi, work, got)
			}
		}
	}
}

// TestTheMinimumScalesWithTheDisplay is the fourth fault: the old minimum was
// 520x560 physical pixels whatever the display, which at 150 % left the page
// about 347 CSS pixels wide — inside the stacked variant by accident. The
// minimum now says what it means in CSS pixels and is scaled.
func TestTheMinimumScalesWithTheDisplay(t *testing.T) {
	t.Parallel()

	frame := func(dpi int) insets { return insets{w: scaleCSS(16, dpi), h: scaleCSS(39, dpi)} }

	for _, dpi := range []int{96, 120, 144, 192} {
		// A work area big enough that the clamp in layoutFor never bites, so
		// what is being read here is the design's minimum and nothing else.
		roomy := rect{left: 0, top: 0, right: 3840, bottom: 2160}
		floor := layoutFor(Config{}, dpi, frame(dpi), roomy).min
		clientCSS := (floor.w - frame(dpi).w) * baselineDPI / dpi

		if clientCSS < minColumnCSS || clientCSS > minColumnCSS+scrollbarCSS+2 {
			t.Errorf("at %d DPI the minimum leaves %d CSS pixels of client area, "+
				"want about %d", dpi, clientCSS, minColumnCSS+scrollbarCSS)
		}
	}

	// And it really does grow: the same minimum is more physical pixels on a
	// denser display, which is the whole point.
	roomy := rect{left: 0, top: 0, right: 3840, bottom: 2160}
	low := layoutFor(Config{}, 96, frame(96), roomy).min
	high := layoutFor(Config{}, 192, frame(192), roomy).min
	if high.w <= low.w || high.h <= low.h {
		t.Errorf("the minimum did not scale: %+v at 96 DPI, %+v at 192", low, high)
	}
}

// TestLayoutForDPIChangeKeepsThePlaceAndTakesTheNewSize covers dragging the
// window to a monitor with a different scale — the message that used to reach
// the default procedure and do nothing.
func TestLayoutForDPIChangeKeepsThePlaceAndTakesTheNewSize(t *testing.T) {
	t.Parallel()

	// The window was on the 96-DPI monitor to the right and is dragged onto
	// this user's 144-DPI one. Windows suggests a rect at the new scale; the
	// position is taken from it and the size from the design.
	suggested := rect{left: 300, top: 200, right: 1126, bottom: 1058}
	got := layoutForDPIChange(Config{}, suggested, 144, laptop144frame, laptop144work)

	if want := (size{w: 825, h: 972}); got.bounds.size() != want {
		t.Errorf("size after the DPI change = %+v, want the design's %+v at 144 DPI",
			got.bounds.size(), want)
	}
	if got.bounds.left != 300 {
		t.Errorf("left = %d, want Windows' suggested 300 kept", got.bounds.left)
	}
	if want := (size{w: 585, h: 746}); got.min != want {
		t.Errorf("minimum after the DPI change = %+v, want %+v rescaled to 144 DPI",
			got.min, want)
	}
	// The suggested top of 200 plus 972 would run 152 pixels past the work
	// area, so the window is pulled back up rather than left hanging.
	if got.bounds.bottom > laptop144work.bottom {
		t.Errorf("the window was left hanging off the work area: %+v", got.bounds)
	}
	if got.bounds.top != laptop144work.bottom-972 {
		t.Errorf("top = %d, want it pulled back to %d",
			got.bounds.top, laptop144work.bottom-972)
	}
}

// TestAsInt32Saturates pins the guard that keeps window-size arithmetic from
// wrapping. Both branches are unreachable on a machine that reports its
// display honestly, which is the reason to pin them here rather than to trust
// that no machine reports otherwise.
func TestAsInt32Saturates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		n    int
		want int32
	}{
		{name: "an ordinary window width passes through", n: 825, want: 825},
		{name: "a negative left is a real coordinate, not an overflow", n: -1080, want: -1080},
		{name: "the top of the range is itself", n: math.MaxInt32, want: math.MaxInt32},
		{name: "the bottom of the range is itself", n: math.MinInt32, want: math.MinInt32},
		{name: "one past the top saturates, not wraps", n: math.MaxInt32 + 1, want: math.MaxInt32},
		{name: "one below the bottom does too", n: math.MinInt32 - 1, want: math.MinInt32},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := asInt32(tc.n); got != tc.want {
				t.Errorf("asInt32(%d) = %d, want %d", tc.n, got, tc.want)
			}
		})
	}
}

// TestAtNeverInvertsTheRect is the guard reached through the code rather than
// directly. A window wider than the distance from the work area's left edge to
// the end of the coordinate space used to get a right edge to the left of its
// left edge — a RECT Windows would read as a window of negative width.
func TestAtNeverInvertsTheRect(t *testing.T) {
	t.Parallel()

	// A work area pushed against the end of the coordinate space, so that
	// left + s.w cannot fit. No desktop looks like this; a lying monitor can.
	work := rect{
		left: math.MaxInt32 - 10, top: math.MaxInt32 - 10,
		right: math.MaxInt32, bottom: math.MaxInt32,
	}
	got := at(size{w: math.MaxInt32, h: math.MaxInt32}, 0, 0, work)

	if got.right < got.left || got.bottom < got.top {
		t.Errorf("at() inverted the rect: %+v", got)
	}
	if got.right != math.MaxInt32 || got.bottom != math.MaxInt32 {
		t.Errorf("at() = %+v, want both far edges saturated to the end of the range", got)
	}
}

// TestAnAbsurdDPIDoesNotWrapTheMinimumTrackSize is the third fault this file
// guards, and the one with a user-visible symptom: the minimum is reported to
// Windows through WM_GETMINMAXINFO as a pair of int32s, so a DPI large enough
// to overflow the multiplication would have been enforced as a negative — or,
// with the sign bit landing elsewhere, as a window that will not resize.
//
// The answer is the same as for a DPI of zero: a number that far outside the
// range is a DPI that was not read, and the baseline is what is left.
func TestAnAbsurdDPIDoesNotWrapTheMinimumTrackSize(t *testing.T) {
	t.Parallel()

	for _, dpi := range []int{maxDPI + 1, 1 << 24, math.MaxInt32} {
		got := layoutFor(Config{}, dpi, desk96frame, desk96work)

		if got.min.w <= 0 || got.min.h <= 0 {
			t.Errorf("at %d DPI the minimum is %+v, want a positive size", dpi, got.min)
		}
		if got.min.w > math.MaxInt32 || got.min.h > math.MaxInt32 {
			t.Errorf("at %d DPI the minimum is %+v, which no MINMAXINFO can carry", dpi, got.min)
		}
		if got.bounds.right < got.bounds.left || got.bounds.bottom < got.bounds.top {
			t.Errorf("at %d DPI the window rect is inverted: %+v", dpi, got.bounds)
		}
		// A DPI that could not be read lays the design out at the baseline, so
		// the result is the same window a 96-DPI display gets.
		if want := layoutFor(Config{}, baselineDPI, desk96frame, desk96work); got != want {
			t.Errorf("at %d DPI the layout is %+v, want the baseline's %+v", dpi, got, want)
		}
	}
}
