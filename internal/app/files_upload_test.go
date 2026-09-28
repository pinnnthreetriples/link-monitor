package app

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type fakeUpload struct {
	err              error
	name, peer, body string
}

func (u *fakeUpload) SendUpload(_ context.Context, name string, src io.Reader, peer string) (int64, error) {
	u.name, u.peer = name, peer
	b, err := io.ReadAll(src)
	if err != nil {
		return 0, err
	}
	u.body = string(b)
	return int64(len(b)), u.err
}

func TestTransfersUploadRecordsBytesAndOutcome(t *testing.T) {
	for _, failure := range []error{nil, errBoom, context.Canceled} {
		u := &fakeUpload{err: failure}
		tr := newTestTransfers(&fakeMover{}, 0)
		tr.SetUploader(u)
		err := tr.Upload(t.Context(), "image.png", strings.NewReader("pixels"), "other")
		if !errors.Is(err, failure) {
			t.Fatalf("Upload = %v", err)
		}
		got := tr.List()[0]
		want := TransferOK
		if failure != nil {
			want = failureState(failure)
		}
		if got.Name != "image.png" || got.Size != 6 || got.State != want ||
			u.peer != "other" || u.body != "pixels" {
			t.Fatalf("transfer = %+v; upload = %+v", got, u)
		}
	}
}

func TestTransfersUploadWithoutAdapterIsUnavailable(t *testing.T) {
	tr := newTestTransfers(&fakeMover{}, 0)
	err := tr.Upload(t.Context(), "image.png", strings.NewReader("pixels"), "other")
	if !errors.Is(err, ErrNoTaildrop) {
		t.Fatalf("Upload = %v", err)
	}
}
