package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core/clipshare"
)

type fakeSavedShots struct {
	shot   clipshare.SavedScreenshot
	raw    []byte
	marked bool
}

func (f *fakeSavedShots) Baseline() error { return nil }
func (f *fakeSavedShots) Next() (clipshare.SavedScreenshot, bool, error) {
	return f.shot, !f.marked && f.shot.ID != "", nil
}

func (f *fakeSavedShots) Read(clipshare.SavedScreenshot) ([]byte, error) {
	return append([]byte(nil), f.raw...), nil
}
func (f *fakeSavedShots) Mark(clipshare.SavedScreenshot) { f.marked = true }

type savedImageInbox struct {
	images [][]byte
	texts  [][]byte
	err    error
}

func (f *savedImageInbox) Name() string { return "peer" }
func (f *savedImageInbox) Deliver(_ context.Context, b []byte) error {
	if f.err != nil {
		return f.err
	}
	f.texts = append(f.texts, append([]byte(nil), b...))
	return nil
}

func (f *savedImageInbox) DeliverImage(_ context.Context, b []byte) error {
	if f.err != nil {
		return f.err
	}
	f.images = append(f.images, append([]byte(nil), b...))
	return nil
}

func newSavedShot(data []byte, modified time.Time) *fakeSavedShots {
	return &fakeSavedShots{
		shot: clipshare.SavedScreenshot{ID: "new.png", Modified: modified, Size: int64(len(data))},
		raw:  data,
	}
}

func TestSavedScreenshotReachesPeerWhenWindowsClipboardDoesNotChange(t *testing.T) {
	here, there := newFakeClipboard(), &savedImageInbox{}
	saved := newSavedShot(testImage(t), time.Now().Add(-time.Second))
	c := NewClip(ClipConfig{}, ClipDeps{Here: here, There: there, Saved: saved})
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	c.tick(context.Background())
	if len(there.images) != 1 || c.Status().Counts.Sent != 1 {
		t.Fatalf("saved screenshot was not delivered: images=%d status=%+v", len(there.images), c.Status().Counts)
	}
	c.tick(context.Background())
	if len(there.images) != 1 {
		t.Fatal("saved screenshot was sent twice")
	}
}

func TestClipboardCopySupersedesSavedScreenshotFallback(t *testing.T) {
	here, there := newFakeClipboard(), &savedImageInbox{}
	saved := newSavedShot(testImage(t), time.Now().Add(-time.Second))
	c := NewClip(ClipConfig{}, ClipDeps{Here: here, There: there, Saved: saved})
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	here.copyText("newer copy")
	c.tick(context.Background())
	if len(there.texts) != 1 || len(there.images) != 0 || !saved.marked {
		t.Fatalf("older screenshot displaced clipboard copy: text=%d images=%d",
			len(there.texts), len(there.images))
	}
}

func TestSavedScreenshotDoesNotDuplicateClipboardImage(t *testing.T) {
	now := time.Now()
	here, there := &imageClipboard{newFakeClipboard()}, &savedImageInbox{}
	data := testImage(t)
	saved := &fakeSavedShots{raw: data}
	c := NewClip(ClipConfig{}, ClipDeps{
		Here: here, There: there, Saved: saved, Now: func() time.Time { return now },
	})
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	if err := here.PutImage(data); err != nil {
		t.Fatal(err)
	}
	c.tick(context.Background())
	saved.shot = clipshare.SavedScreenshot{
		ID: "same.png", Modified: now.Add(time.Second), Size: int64(len(data)),
	}
	now = now.Add(2 * time.Second)
	c.tick(context.Background())
	if len(there.images) != 1 {
		t.Fatalf("same screenshot sent %d times", len(there.images))
	}
}

func TestSavedScreenshotRetriesAfterPeerRecovers(t *testing.T) {
	now := time.Now()
	here, there := newFakeClipboard(), &savedImageInbox{err: errors.New("offline")}
	saved := newSavedShot(testImage(t), now.Add(-time.Second))
	c := NewClip(ClipConfig{}, ClipDeps{
		Here: here, There: there, Saved: saved, Now: func() time.Time { return now },
	})
	if err := c.TurnOn(); err != nil {
		t.Fatal(err)
	}
	c.tick(context.Background())
	there.err = nil
	now = now.Add(3 * time.Second)
	c.tick(context.Background())
	if len(there.images) != 1 || c.Status().Counts.Sent != 1 {
		t.Fatalf("saved screenshot was lost after a failed delivery: images=%d counts=%+v",
			len(there.images), c.Status().Counts)
	}
}
