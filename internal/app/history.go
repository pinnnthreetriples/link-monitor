package app

import (
	"sync"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

const (
	// DefaultHistoryWindow is how far back the uptime strip reaches.
	DefaultHistoryWindow = 24 * time.Hour

	// defaultHistoryMax bounds the ring by count as well as by age. At the
	// default interval a day holds 2880 points; the cap only matters when the
	// interval is turned right down, and it keeps a runaway poller from eating
	// memory. Memory only — nothing here is ever written to disk.
	defaultHistoryMax = 8192
)

// Point is one probe on the uptime strip: when it ran and how the link looked.
type Point struct {
	At    time.Time
	State core.State
}

// History is the last day of probes, oldest first. It is safe for concurrent
// use: the poller appends while the UI reads.
//
// Trimming is relative to the newest point rather than to time.Now, so the ring
// needs no clock of its own and a test can walk it through a simulated day.
type History struct {
	mu     sync.Mutex
	window time.Duration
	max    int
	points []Point
}

// NewHistory returns an empty ring covering window. A window of zero or less
// means DefaultHistoryWindow.
func NewHistory(window time.Duration) *History {
	if window <= 0 {
		window = DefaultHistoryWindow
	}
	return &History{window: window, max: defaultHistoryMax}
}

// Add records one probe and drops everything that has fallen out of the window.
// A point older than the newest one already held is kept in order, so a clock
// that steps backwards cannot scramble the strip.
func (h *History) Add(p Point) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.points = append(h.points, p)
	h.trim()
}

// trim drops points older than the window and enforces the count cap. The
// caller holds the mutex.
func (h *History) trim() {
	newest := h.points[len(h.points)-1].At
	cutoff := newest.Add(-h.window)

	first := 0
	for first < len(h.points) && h.points[first].At.Before(cutoff) {
		first++
	}
	if extra := len(h.points) - first - h.max; extra > 0 {
		first += extra
	}
	if first == 0 {
		return
	}
	// Copy down rather than reslice: reslicing would keep the dropped points
	// alive in the backing array for as long as the ring lives.
	kept := len(h.points) - first
	copy(h.points, h.points[first:])
	clear(h.points[kept:])
	h.points = h.points[:kept]
}

// Points returns a copy of the ring, oldest first, so the caller can hold it
// while the poller keeps writing.
func (h *History) Points() []Point {
	h.mu.Lock()
	defer h.mu.Unlock()

	out := make([]Point, len(h.points))
	copy(out, h.points)
	return out
}

// Len reports how many points the ring currently holds.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()

	return len(h.points)
}

// Window reports the age the ring caps at.
func (h *History) Window() time.Duration { return h.window }
