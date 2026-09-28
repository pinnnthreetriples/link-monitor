package app

import (
	"context"
	"errors"
	"time"
)

// ErrReceiveBusy prevents manual and automatic drains from reading one inbox
// at the same time. It is a transient state, not a transfer failure.
var ErrReceiveBusy = errors.New("a receive pass is already running")

// Start begins one receive loop for the process lifetime, including tray mode.
// Each pass has a deadline; the interval starts after it finishes, preventing
// both overlapping drains and a tight retry loop during daemon failures.
func (t *Transfers) Start(ctx context.Context) {
	t.startOnce.Do(func() {
		t.receiveWG.Add(1)
		go func() {
			defer t.receiveWG.Done()
			t.receiveLoop(ctx)
		}()
	})
}

func (t *Transfers) receiveLoop(ctx context.Context) {
	for ctx.Err() == nil {
		pass, cancel := context.WithTimeout(ctx, t.receiveTimeout)
		// Receive retains failures for the UI. Busy means the manual action owns
		// this pass; retrying after the interval is all the background loop owes.
		_, err := t.Receive(pass)
		cancel()
		if err != nil && !errors.Is(err, ErrReceiveBusy) {
			t.mu.Lock()
			t.receiveErr = err
			t.mu.Unlock()
		}
		timer := time.NewTimer(t.receiveInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Wait joins the receive loop after its lifetime context has been cancelled.
func (t *Transfers) Wait() { t.receiveWG.Wait() }

// ReceiveError reports the latest completed drain failure for a safe UI message.
func (t *Transfers) ReceiveError() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.receiveErr
}
