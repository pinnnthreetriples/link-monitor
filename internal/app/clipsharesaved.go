package app

import (
	"context"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

// SavedScreenshots is the filesystem seam for captures Snipping Tool saved
// without copying them to Windows' clipboard.
type SavedScreenshots interface {
	Baseline() error
	Next() (clipshare.SavedScreenshot, bool, error)
	Read(clipshare.SavedScreenshot) ([]byte, error)
	Mark(clipshare.SavedScreenshot)
}

func (c *Clip) baselineScreenshots() {
	if c.deps.Saved == nil {
		return
	}
	if err := c.deps.Saved.Baseline(); err != nil {
		c.failure = msgClipReadFailed
	}
}

func (c *Clip) markScreenshot(shot clipshare.SavedScreenshot) {
	c.deps.Saved.Mark(shot)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.screenshotsRetryAt, c.screenshotsRetryDelay = time.Time{}, 0
	c.screenshotsRetryName = ""
}

func (c *Clip) finishScreenshot(shot clipshare.SavedScreenshot) {
	c.access.Lock()
	defer c.access.Unlock()
	c.markScreenshot(shot)
}

// tickSavedScreenshot uses a saved PNG only when no newer clipboard copy took
// precedence. The adapter has already waited for the file to finish writing.
func (c *Clip) tickSavedScreenshot(ctx context.Context) {
	if !c.On() || c.deps.Saved == nil {
		return
	}
	c.access.Lock()
	shot, ok, err := c.deps.Saved.Next()
	c.access.Unlock()
	if err != nil {
		c.fail(msgClipReadFailed)
		return
	}
	if !ok {
		return
	}

	c.mu.Lock()
	newerCopy := c.lastLocalChange.After(shot.Modified)
	if shot.ID != c.screenshotsRetryName {
		c.screenshotsRetryName = shot.ID
		c.screenshotsRetryAt, c.screenshotsRetryDelay = time.Time{}, 0
	}
	wait := c.deps.Now().Before(c.screenshotsRetryAt)
	c.mu.Unlock()
	if newerCopy {
		c.finishScreenshot(shot)
		return
	}
	if wait {
		return
	}
	if shot.Size > clipshare.MaxImageBytes {
		c.note(clipshare.WhyTooBig, int(shot.Size))
		c.finishScreenshot(shot)
		return
	}
	c.processSavedScreenshot(ctx, shot)
}

func (c *Clip) processSavedScreenshot(ctx context.Context, shot clipshare.SavedScreenshot) {
	raw, err := c.deps.Saved.Read(shot)
	if err != nil {
		c.retryScreenshot()
		return
	}
	defer clipshare.Zero(raw)
	if len(raw) > clipshare.MaxImageBytes {
		c.note(clipshare.WhyTooBig, len(raw))
		c.finishScreenshot(shot)
		return
	}
	png, err := clipshare.NormalizePNG(raw)
	if err != nil {
		c.retryScreenshot()
		return
	}
	defer clipshare.Zero(png)
	snap := clipshare.Snapshot{Format: "png", Recordable: true, Bytes: len(png), Text: png}
	why, mark := clipshare.Decide(snap, c.memory(), c.cfg.MaxBytes)
	if !why.Sends() {
		c.finishScreenshot(shot)
		return
	}
	c.deliverSavedScreenshot(ctx, shot, png, mark)
}

func (c *Clip) deliverSavedScreenshot(ctx context.Context, shot clipshare.SavedScreenshot,
	png []byte, mark clipshare.Print,
) {
	seq, err := c.deps.Here.Sequence()
	if err != nil {
		c.retryScreenshot()
		return
	}
	c.mu.Lock()
	changed := c.haveBaseline && seq != c.baseline
	c.mu.Unlock()
	if changed {
		return
	}
	peer, ok := c.deps.There.(ImageInbox)
	if !ok {
		c.retryScreenshot()
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	if !c.On() {
		return
	}
	if err := peer.DeliverImage(sendCtx, png); err != nil {
		c.failed(msgClipSendFailed, len(png))
		c.retryScreenshot()
		return
	}
	c.recordSent(mark, len(png), false)
	c.finishScreenshot(shot)
}

func (c *Clip) retryScreenshot() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.screenshotsRetryDelay == 0 {
		c.screenshotsRetryDelay = clipRetryInitial
	} else {
		c.screenshotsRetryDelay = min(c.screenshotsRetryDelay*2, clipRetryMaximum)
	}
	c.screenshotsRetryAt = c.deps.Now().Add(c.screenshotsRetryDelay)
}
