package app

import (
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

func TestHistoryKeepsOrderAndCopies(t *testing.T) {
	t.Parallel()

	h := NewHistory(time.Hour)
	base := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	for i := range 3 {
		h.Add(Point{At: base.Add(time.Duration(i) * time.Minute), State: core.StateOK})
	}

	points := h.Points()
	if len(points) != 3 || h.Len() != 3 {
		t.Fatalf("got %d points, Len %d", len(points), h.Len())
	}
	if !points[0].At.Equal(base) || !points[2].At.Equal(base.Add(2*time.Minute)) {
		t.Errorf("points are out of order: %v", points)
	}

	points[0].State = core.StateFail
	if h.Points()[0].State != core.StateOK {
		t.Error("Points handed out the ring itself, not a copy")
	}
}

func TestHistoryDropsPointsOlderThanTheWindow(t *testing.T) {
	t.Parallel()

	h := NewHistory(24 * time.Hour)
	base := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	h.Add(Point{At: base, State: core.StateFail})
	h.Add(Point{At: base.Add(12 * time.Hour), State: core.StateWarn})
	h.Add(Point{At: base.Add(23 * time.Hour), State: core.StateOK})
	if h.Len() != 3 {
		t.Fatalf("within the window: %d points", h.Len())
	}

	// A point a day and an hour after the first pushes the first out.
	h.Add(Point{At: base.Add(25 * time.Hour), State: core.StateOK})
	points := h.Points()
	if len(points) != 3 {
		t.Fatalf("after the window moved: %d points", len(points))
	}
	if points[0].At.Equal(base) {
		t.Error("the point that fell out of the window is still there")
	}
}

func TestHistoryZeroWindowUsesTheDefault(t *testing.T) {
	t.Parallel()

	if got := NewHistory(0).Window(); got != DefaultHistoryWindow {
		t.Errorf("Window = %v, want %v", got, DefaultHistoryWindow)
	}
	if got := NewHistory(-time.Hour).Window(); got != DefaultHistoryWindow {
		t.Errorf("Window = %v, want %v", got, DefaultHistoryWindow)
	}
}

func TestHistoryHonoursTheCountCap(t *testing.T) {
	t.Parallel()

	h := NewHistory(24 * time.Hour)
	h.max = 4
	base := time.Date(2026, 9, 7, 0, 0, 0, 0, time.UTC)
	for i := range 10 {
		h.Add(Point{At: base.Add(time.Duration(i) * time.Second), State: core.StateOK})
	}

	points := h.Points()
	if len(points) != 4 {
		t.Fatalf("got %d points, want the cap of 4", len(points))
	}
	if !points[0].At.Equal(base.Add(6 * time.Second)) {
		t.Errorf("the oldest kept point is %v, want the seventh", points[0].At)
	}
}

func TestHistoryEmptyRingReadsEmpty(t *testing.T) {
	t.Parallel()

	h := NewHistory(time.Hour)
	if h.Len() != 0 || len(h.Points()) != 0 {
		t.Errorf("a fresh ring is not empty: %d", h.Len())
	}
}

func TestHistoryIsSafeUnderConcurrentUse(t *testing.T) {
	t.Parallel()

	h := NewHistory(time.Hour)
	base := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 200 {
			h.Add(Point{At: base.Add(time.Duration(i) * time.Second), State: core.StateOK})
		}
	}()
	for range 200 {
		_ = h.Points()
		_ = h.Len()
	}
	<-done
}
