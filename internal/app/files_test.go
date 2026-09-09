package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixedSize is a size lookup that needs no filesystem.
func fixedSize(n int64) func(string) (int64, error) {
	return func(string) (int64, error) { return n, nil }
}

func newTestTransfers(mover FileMover, keep int) *Transfers {
	tr := NewTransfers(mover, `C:\Users\pnj\Downloads\linkmon`, keep)
	tr.size = fixedSize(2048)
	tr.now = func() time.Time { return time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC) }
	return tr
}

func TestTransfersSendRecordsTheFile(t *testing.T) {
	t.Parallel()

	mover := &fakeMover{}
	tr := newTestTransfers(mover, 0)

	if err := tr.Send(context.Background(), `C:\tmp\отчёт.pdf`, "win-sttm11d02rd"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if mover.sentPath != `C:\tmp\отчёт.pdf` || mover.sentPeer != "win-sttm11d02rd" {
		t.Errorf("SendFile got (%q, %q)", mover.sentPath, mover.sentPeer)
	}

	list := tr.List()
	if len(list) != 1 {
		t.Fatalf("got %d transfers", len(list))
	}
	got := list[0]
	if got.Name != "отчёт.pdf" || got.Dir != TransferTo || got.State != TransferOK || got.Pct != 100 {
		t.Errorf("transfer = %+v", got)
	}
	if got.Size != 2048 {
		t.Errorf("Size = %d, want 2048", got.Size)
	}
}

func TestTransfersSendMarksAFailure(t *testing.T) {
	t.Parallel()

	tr := newTestTransfers(&fakeMover{sendErr: errBoom}, 0)
	err := tr.Send(context.Background(), `C:\tmp\a.bin`, "peer")
	if !errors.Is(err, errBoom) {
		t.Fatalf("Send err = %v", err)
	}
	if got := tr.List()[0].State; got != TransferErr {
		t.Errorf("State = %q, want %q", got, TransferErr)
	}
}

func TestTransfersSendMarksACancellation(t *testing.T) {
	t.Parallel()

	wrapped := fmt.Errorf("taildrop send: %w", context.Canceled)
	tr := newTestTransfers(&fakeMover{sendErr: wrapped}, 0)
	if err := tr.Send(context.Background(), `C:\tmp\a.bin`, "peer"); err == nil {
		t.Fatal("Send reported success")
	}
	if got := tr.List()[0].State; got != TransferCancel {
		t.Errorf("State = %q, want %q", got, TransferCancel)
	}
}

// While a push is in flight the entry is already in the list, at zero percent,
// which is what lets the UI draw it moving.
func TestTransfersSendIsVisibleWhileItRuns(t *testing.T) {
	t.Parallel()

	mover := &fakeMover{block: make(chan struct{})}
	tr := newTestTransfers(mover, 0)

	done := make(chan error, 1)
	go func() { done <- tr.Send(context.Background(), `C:\tmp\big.iso`, "peer") }()

	waitFor(t, "the transfer to appear", func() bool { return len(tr.List()) == 1 })
	inFlight := tr.List()[0]
	if inFlight.Pct != 0 || inFlight.State != TransferOK {
		t.Errorf("in-flight transfer = %+v", inFlight)
	}
	if !tr.Progress(inFlight.ID, 47) {
		t.Fatal("Progress did not find the transfer")
	}
	if got := tr.List()[0].Pct; got != 47 {
		t.Errorf("Pct = %d, want 47", got)
	}

	close(mover.block)
	if err := <-done; err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := tr.List()[0].Pct; got != 100 {
		t.Errorf("finished Pct = %d", got)
	}
}

func TestTransfersProgressClampsAndReportsUnknownIDs(t *testing.T) {
	t.Parallel()

	tr := newTestTransfers(&fakeMover{}, 0)
	if err := tr.Send(context.Background(), `C:\tmp\a.bin`, "peer"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	id := tr.List()[0].ID

	tr.Progress(id, -5)
	if got := tr.List()[0].Pct; got != 0 {
		t.Errorf("Pct after -5 = %d", got)
	}
	tr.Progress(id, 500)
	if got := tr.List()[0].Pct; got != 100 {
		t.Errorf("Pct after 500 = %d", got)
	}
	if tr.Progress("tr-нет", 10) {
		t.Error("Progress claimed to find a transfer that is not there")
	}
}

func TestTransfersReceiveRecordsEachFile(t *testing.T) {
	t.Parallel()

	mover := &fakeMover{received: []string{`C:\in\a.txt`, `C:\in\б.txt`}}
	tr := newTestTransfers(mover, 0)

	paths, err := tr.Receive(context.Background())
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(paths) != 2 || mover.inbox != tr.Inbox() {
		t.Fatalf("Receive = %v, inbox %q", paths, mover.inbox)
	}

	list := tr.List()
	if len(list) != 2 {
		t.Fatalf("got %d transfers", len(list))
	}
	// Newest first: the second file is at the front.
	if list[0].Name != "б.txt" || list[0].Dir != TransferFrom || list[0].Pct != 100 {
		t.Errorf("newest transfer = %+v", list[0])
	}
}

func TestTransfersReceiveReportsWhatLandedBeforeAnError(t *testing.T) {
	t.Parallel()

	mover := &fakeMover{received: []string{`C:\in\a.txt`}, receiveErr: errBoom}
	tr := newTestTransfers(mover, 0)

	paths, err := tr.Receive(context.Background())
	if !errors.Is(err, errBoom) {
		t.Fatalf("Receive err = %v", err)
	}
	if len(paths) != 1 || len(tr.List()) != 1 {
		t.Errorf("the file that arrived was dropped: %v", paths)
	}
}

func TestTransfersCapTheList(t *testing.T) {
	t.Parallel()

	tr := newTestTransfers(&fakeMover{}, 2)
	for i := range 5 {
		if err := tr.Send(context.Background(), fmt.Sprintf(`C:\tmp\%d.bin`, i), "peer"); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	list := tr.List()
	if len(list) != 2 {
		t.Fatalf("got %d transfers, want the cap of 2", len(list))
	}
	if list[0].Name != "4.bin" || list[1].Name != "3.bin" {
		t.Errorf("the wrong entries survived: %+v", list)
	}
}

func TestTransfersWithoutAMoverSayNo(t *testing.T) {
	t.Parallel()

	tr := NewTransfers(nil, `C:\in`, 0)
	if err := tr.Send(context.Background(), `C:\a.bin`, "peer"); !errors.Is(err, ErrNoTaildrop) {
		t.Errorf("Send err = %v", err)
	}
	if _, err := tr.Receive(context.Background()); !errors.Is(err, ErrNoTaildrop) {
		t.Errorf("Receive err = %v", err)
	}
	if len(tr.List()) != 0 {
		t.Error("a refused transfer was recorded")
	}
}

func TestTransfersSendNeedsAPath(t *testing.T) {
	t.Parallel()

	tr := newTestTransfers(&fakeMover{}, 0)
	if err := tr.Send(context.Background(), "", "peer"); err == nil {
		t.Error("an empty path was accepted")
	}
}

func TestTransfersSurviveAFileTheyCannotMeasure(t *testing.T) {
	t.Parallel()

	tr := NewTransfers(&fakeMover{}, `C:\in`, 0)
	if err := tr.Send(context.Background(), `C:\ничего\такого\нет.bin`, "peer"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got := tr.List()[0].Size; got != 0 {
		t.Errorf("Size = %d, want 0 for a file that cannot be measured", got)
	}
}

func TestFileSizeReadsARealFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "sample.bin")
	if err := os.WriteFile(path, []byte("привет"), 0o600); err != nil {
		t.Fatalf("writing the sample: %v", err)
	}
	got, err := fileSize(path)
	if err != nil || got != 12 {
		t.Errorf("fileSize = (%d, %v), want 12 bytes", got, err)
	}
	if _, err := fileSize(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing file reported a size")
	}
}
