package app

import (
	"context"
	"fmt"
	"io"
)

// UploadMover stages submitted bytes and sends them without resolving names
// against the working directory. The adapter owns temporary-file cleanup.
type UploadMover interface {
	SendUpload(ctx context.Context, name string, src io.Reader, peer string) (int64, error)
}

// SetUploader connects the upload adapter. It is normally called during wiring.
func (t *Transfers) SetUploader(uploader UploadMover) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.uploader = uploader
}

// Upload sends exactly the supplied bytes and keeps the outcome in history.
func (t *Transfers) Upload(ctx context.Context, name string, src io.Reader, peer string) error {
	t.mu.Lock()
	uploader := t.uploader
	t.mu.Unlock()
	if uploader == nil {
		return fmt.Errorf("uploading file: %w", ErrNoTaildrop)
	}
	id := t.record(&Transfer{Name: name, At: t.now(), Dir: TransferTo, State: TransferOK})
	n, err := uploader.SendUpload(ctx, name, src, peer)
	t.mu.Lock()
	if item := t.find(id); item != nil {
		item.Size = n
	}
	t.mu.Unlock()
	if err != nil {
		t.finish(id, failureState(err), 0)
		return fmt.Errorf("uploading file: %w", err)
	}
	t.finish(id, TransferOK, 100)
	return nil
}
