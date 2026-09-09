package trayicon

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

func hex(c color.NRGBA) string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

func TestColorMatchesTheApprovedPalette(t *testing.T) {
	want := map[core.State]string{
		core.StateOK:      "#3fbf7f",
		core.StateWarn:    "#d9a13a",
		core.StateFail:    "#e05c5c",
		core.StateUnknown: "#646c7c",
	}
	for state, hexWant := range want {
		got := Color(state)
		if hex(got) != hexWant {
			t.Errorf("Color(%s) = %s, want %s", state, hex(got), hexWant)
		}
		if got.A != 0xff {
			t.Errorf("Color(%s) alpha = %d, want 255", state, got.A)
		}
	}
}

func TestColorFallsBackToGreyForAnUnknownState(t *testing.T) {
	got := Color(core.State(42))
	if hex(got) != hex(Color(core.StateUnknown)) {
		t.Errorf("Color(State(42)) = %s, want the unknown grey %s", hex(got), hex(Color(core.StateUnknown)))
	}
}

func TestDrawSizeAndShape(t *testing.T) {
	for _, size := range Sizes {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			img, err := Draw(core.StateOK, size)
			if err != nil {
				t.Fatalf("Draw: %v", err)
			}
			if b := img.Bounds(); b.Dx() != size || b.Dy() != size {
				t.Fatalf("image is %dx%d, want %[3]dx%[3]d", b.Dx(), b.Dy(), size)
			}

			// The middle is the state's colour, opaque.
			mid := img.NRGBAAt(size/2, size/2)
			if hex(mid) != "#3fbf7f" || mid.A != 0xff {
				t.Errorf("centre pixel = %s alpha %d, want #3fbf7f alpha 255", hex(mid), mid.A)
			}
			// The corners are outside the disc and must stay transparent, or
			// the icon would show as a square block on the taskbar.
			for _, p := range [][2]int{{0, 0}, {size - 1, 0}, {0, size - 1}, {size - 1, size - 1}} {
				if a := img.NRGBAAt(p[0], p[1]).A; a != 0 {
					t.Errorf("corner (%d,%d) alpha = %d, want 0", p[0], p[1], a)
				}
			}
		})
	}
}

func TestDrawRingIsDarkerThanTheFill(t *testing.T) {
	const size = 32
	img, err := Draw(core.StateFail, size)
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}

	side := float64(img.Bounds().Dy())
	fill := img.NRGBAAt(size/2, size/2)
	// A pixel just inside the outer edge lands on the ring.
	ring := img.NRGBAAt(size/2, int(side*insetRatio)+1)
	if ring.A == 0 {
		t.Fatalf("expected the ring at the top of the disc, found a transparent pixel")
	}
	if int(ring.R)+int(ring.G)+int(ring.B) >= int(fill.R)+int(fill.G)+int(fill.B) {
		t.Errorf("ring %s is not darker than the fill %s", hex(ring), hex(fill))
	}
}

func TestDrawAntiAliasesTheEdge(t *testing.T) {
	// Somewhere along a row through the centre there must be a pixel that is
	// neither fully transparent nor fully opaque, or the disc has hard stairs.
	const size = 32
	img, err := Draw(core.StateOK, size)
	if err != nil {
		t.Fatalf("Draw: %v", err)
	}
	partial := 0
	for x := range size {
		if a := img.NRGBAAt(x, size/2).A; a > 0 && a < 0xff {
			partial++
		}
	}
	if partial == 0 {
		t.Error("no partially transparent pixels across the disc: the edge is not anti-aliased")
	}
}

func TestDrawEachStateLooksDifferent(t *testing.T) {
	seen := map[string]core.State{}
	for _, s := range []core.State{core.StateOK, core.StateUnknown, core.StateWarn, core.StateFail} {
		img, err := Draw(s, 16)
		if err != nil {
			t.Fatalf("Draw(%s): %v", s, err)
		}
		got := hex(img.NRGBAAt(8, 8))
		if other, dup := seen[got]; dup {
			t.Errorf("%s and %s both draw %s in the centre", s, other, got)
		}
		seen[got] = s
	}
}

func TestDrawRejectsUnusableSizes(t *testing.T) {
	for _, size := range []int{-1, 0, 7, 257} {
		if _, err := Draw(core.StateOK, size); err == nil {
			t.Errorf("Draw at %d px succeeded, want an error", size)
		}
	}
}

func TestRenderPacksEverySize(t *testing.T) {
	b, err := Render(core.StateFail)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	entries := parseICO(t, b)
	if len(entries) != len(Sizes) {
		t.Fatalf("got %d entries, want %d", len(entries), len(Sizes))
	}
	for i, e := range entries {
		if int(e.width) != Sizes[i] || int(e.height) != Sizes[i] {
			t.Errorf("entry %d is %dx%d, want %[4]dx%[4]d", i, e.width, e.height, Sizes[i])
		}
	}
}

func TestRenderIsCachedAndStableAcrossStates(t *testing.T) {
	first, err := Render(core.StateOK)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	second, err := Render(core.StateOK)
	if err != nil {
		t.Fatalf("Render again: %v", err)
	}
	if &first[0] != &second[0] {
		t.Error("Render re-encoded an icon it had already produced")
	}

	warn, err := Render(core.StateWarn)
	if err != nil {
		t.Fatalf("Render(warn): %v", err)
	}
	if string(warn) == string(first) {
		t.Error("the ok and warn icons are byte-identical")
	}
}

func TestRenderIsSafeFromSeveralGoroutines(t *testing.T) {
	// The tray renders from its own goroutine while tests hold the same cache.
	states := []core.State{core.StateOK, core.StateWarn, core.StateFail, core.StateUnknown}
	done := make(chan error, len(states)*4)
	for range 4 {
		for _, s := range states {
			go func() {
				_, err := Render(s)
				done <- err
			}()
		}
	}
	for range cap(done) {
		if err := <-done; err != nil {
			t.Fatalf("Render: %v", err)
		}
	}
}

func TestShade(t *testing.T) {
	got := shade(color.NRGBA{R: 200, G: 100, B: 50, A: 255}, 0.5)
	want := color.NRGBA{R: 100, G: 50, B: 25, A: 255}
	if got != want {
		t.Errorf("shade = %v, want %v", got, want)
	}
}

func TestDiscIsAnAlphaMask(t *testing.T) {
	// draw.DrawMask only treats the mask as coverage if it says it is alpha.
	d := disc{cx: 8, cy: 8, r: 6, size: 16}
	if d.ColorModel() != color.AlphaModel {
		t.Errorf("ColorModel = %v, want color.AlphaModel", d.ColorModel())
	}
	if got := d.Bounds().Dx(); got != 16 {
		t.Errorf("Bounds width = %d, want 16", got)
	}
	if a, _, _, _ := d.At(8, 8).RGBA(); a == 0 {
		t.Error("the centre of the disc is not covered")
	}
	if a, _, _, _ := d.At(0, 0).RGBA(); a != 0 {
		t.Error("the corner of the disc is covered")
	}
}
