package peerclip

import (
	"context"
	"net/http"
	"testing"
)

// The second item must not start PowerShell on the peer again: that start is
// the seconds-long delay that made the first paste on the other machine still
// show the old text.
func TestASecondItemReusesTheConfirmedRecord(t *testing.T) {
	t.Parallel()

	peer, shell, _, in := wired(t)
	for range 3 {
		if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
			t.Fatalf("Deliver() = %v", err)
		}
	}
	if len(shell.ran) != 1 {
		t.Errorf("the record script ran %d times for three items, want 1", len(shell.ran))
	}
	if in.posts != 3 {
		t.Errorf("%d items arrived, want 3", in.posts)
	}
}

// A remembered port now held by another process — or by a restarted instance
// — gets no item: the record is read and confirmed again first.
func TestAPidMismatchRereadsTheRecordBeforeSending(t *testing.T) {
	t.Parallel()

	for name, pid := range map[string]int{"other pid": peerPID + 1, "older build": 0} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			peer, shell, _, in := wired(t)
			if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
				t.Fatalf("Deliver() = %v", err)
			}
			in.pid = pid
			if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
				t.Fatalf("Deliver() = %v", err)
			}
			if len(shell.ran) != 2 {
				t.Errorf("the record script ran %d times, want 2", len(shell.ran))
			}
			if in.posts != 2 {
				t.Errorf("%d items arrived, want 2", in.posts)
			}
		})
	}
}

// An instance that refused the item after naming the right pid has seen it;
// sending it again would be a second copy, so the refusal is the answer.
func TestARefusalAfterThePidMatchedIsNotRetried(t *testing.T) {
	t.Parallel()

	peer, shell, _, in := wired(t)
	if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
		t.Fatalf("Deliver() = %v", err)
	}
	in.status, in.answer = http.StatusServiceUnavailable, `{"ok":false,"message":"Выключено."}`
	if err := peer.Deliver(context.Background(), []byte(theItem)); err == nil {
		t.Fatal("Deliver() = nil after the instance refused the item")
	}
	if in.posts != 2 || len(shell.ran) != 1 {
		t.Errorf("posts = %d, scripts = %d; want 2 and 1", in.posts, len(shell.ran))
	}
	// The refusal also drops the remembered record, so the next item checks again.
	in.status, in.answer = http.StatusOK, `{"ok":true,"message":"Принято."}`
	if err := peer.Deliver(context.Background(), []byte(theItem)); err != nil {
		t.Fatalf("Deliver() = %v", err)
	}
	if len(shell.ran) != 2 {
		t.Errorf("the record script ran %d times, want 2", len(shell.ran))
	}
}
