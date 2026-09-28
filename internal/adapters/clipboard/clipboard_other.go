//go:build !windows

package clipboard

import (
	"fmt"
	"runtime"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// This file is the twin that keeps every GOOS building, and it refuses rather
// than pretending. The clipboard this package is about is a Windows session's
// clipboard, and the three markers it has to honour are Windows' own
// registered formats; something that read another system's selection buffer
// and shared it without any of those checks would be a different feature
// wearing this one's name.
//
// A build agent with no Windows therefore compiles the whole program, runs
// every test of the decision in internal/core/clipshare, and finds these three
// calls refusing — which is what the loop above reports as the feature being
// unavailable on this machine.

// Sequence refuses: there is no clipboard sequence number to poll here.
func (c *Clipboard) Sequence() (uint32, error) {
	return 0, fmt.Errorf("reading the clipboard sequence number on %s: %w", runtime.GOOS, ErrUnsupported)
}

// Look refuses, so nothing is ever read and nothing is ever shared.
func (c *Clipboard) Look(int) (clipshare.Snapshot, error) {
	return clipshare.Snapshot{}, fmt.Errorf("reading the clipboard on %s: %w", runtime.GOOS, ErrUnsupported)
}

// Put refuses, so a received item is reported as undelivered rather than
// silently dropped.
func (c *Clipboard) Put([]byte) error {
	return fmt.Errorf("writing the clipboard on %s: %w", runtime.GOOS, ErrUnsupported)
}

func (c *Clipboard) PutImage([]byte) error {
	return fmt.Errorf("writing an image on %s: %w", runtime.GOOS, ErrUnsupported)
}
