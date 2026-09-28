// Package upload stages browser-provided bytes for a path-based file sender.
// Only the submitted stream is read; a browser filename is never a source path.
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

// Sender is the path-based transport consumed by this adapter.
type Sender interface {
	SendFile(ctx context.Context, path, peer string) error
}

// Adapter owns temporary uploads until their transport finishes.
type Adapter struct {
	sender   Sender
	tempRoot string
}

// New builds an uploader; no files are created until SendUpload is called.
func New(sender Sender) *Adapter { return &Adapter{sender: sender} }

// SendUpload copies a stream to a private directory, sends it, and removes it.
// A failed or cancelled copy never reaches the sender. Cleanup errors are
// returned alongside the original error rather than silently leaking data.
func (a *Adapter) SendUpload(ctx context.Context, name string, src io.Reader, peer string) (n int64, err error) {
	if err := validName(name); err != nil {
		return 0, err
	}
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("staging upload: %w", err)
	}
	if a.sender == nil || src == nil {
		return 0, errors.New("upload sender or stream is missing")
	}
	dir, err := os.MkdirTemp(a.tempRoot, "linkmon-upload-")
	if err != nil {
		return 0, fmt.Errorf("creating upload directory: %w", err)
	}
	defer func() {
		if cleanup := os.RemoveAll(dir); cleanup != nil {
			err = errors.Join(err, fmt.Errorf("removing staged upload: %w", cleanup))
		}
	}()
	path := filepath.Join(dir, name)
	n, err = stage(ctx, path, src, core.MaxUploadBytes)
	if err != nil {
		return n, err
	}
	if err := a.sender.SendFile(ctx, path, peer); err != nil {
		return n, fmt.Errorf("sending uploaded file: %w", err)
	}
	return n, nil
}

func stage(ctx context.Context, path string, src io.Reader, maxBytes int64) (int64, error) {
	// path is inside our new private directory; validName refuses all separators.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // G304: see above
	if err != nil {
		return 0, fmt.Errorf("creating staged upload: %w", err)
	}
	n, copyErr := io.Copy(f, io.LimitReader(contextReader{ctx: ctx, src: src}, maxBytes+1))
	if err := errors.Join(copyErr, f.Close(), ctx.Err()); err != nil {
		return n, fmt.Errorf("writing staged upload: %w", err)
	}
	if n > maxBytes {
		return n, core.ErrUploadTooLarge
	}
	return n, nil
}

type contextReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.src.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("reading upload: %w", err)
	}
	return n, err //nolint:wrapcheck // io.EOF must retain its stream sentinel semantics.
}

func validName(name string) error {
	if name == "" || strings.TrimRight(name, " .") != name || strings.ContainsAny(name, `/\:"<>|?*`) {
		return errors.New("invalid upload filename")
	}
	for _, c := range name {
		if c < 32 || c == 127 {
			return errors.New("control character in upload filename")
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	reserved := stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL"
	device := len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT"))
	if reserved || (device && stem[3] >= '1' && stem[3] <= '9') {
		return errors.New("reserved upload filename")
	}
	return nil
}
