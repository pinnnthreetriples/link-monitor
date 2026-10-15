package sshx

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestKeepAliveKillsASessionThatStopsAnswering(t *testing.T) {
	t.Parallel()
	dead := make(chan struct{})
	killed := make(chan struct{})
	hang := make(chan struct{})
	defer close(hang)

	go keepAlive(dead, time.Millisecond, 10*time.Millisecond,
		func() error { <-hang; return nil },
		func() { close(killed) })

	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("a session that never answered was not killed")
	}
}

func TestKeepAliveKillsASessionWhoseTransportFails(t *testing.T) {
	t.Parallel()
	dead := make(chan struct{})
	killed := make(chan struct{})

	go keepAlive(dead, time.Millisecond, time.Second,
		func() error { return errors.New("connection lost") },
		func() { close(killed) })

	select {
	case <-killed:
	case <-time.After(5 * time.Second):
		t.Fatal("a failed keepalive did not kill the session")
	}
}

func TestKeepAliveLeavesAnAnsweringSessionAlone(t *testing.T) {
	t.Parallel()
	dead := make(chan struct{})
	var pings, kills atomic.Int32
	stopped := make(chan struct{})

	go func() {
		keepAlive(dead, time.Millisecond, time.Second,
			func() error { pings.Add(1); return nil },
			func() { kills.Add(1) })
		close(stopped)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for pings.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(dead)
	<-stopped
	if pings.Load() < 3 {
		t.Errorf("sent %d keepalives, want at least 3", pings.Load())
	}
	if kills.Load() != 0 {
		t.Error("a session that answered every keepalive was killed")
	}
}

func TestLazyKeepsAnAnsweringSessionOpen(t *testing.T) {
	lazy, server := newLazyAgainst(t, 50*time.Millisecond)
	lazy.cfg.KeepAlive = 5 * time.Millisecond
	server.setHandler(func(string) execResult {
		return execResult{stdout: serviceSays(serviceStatusRunning)}
	})

	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	time.Sleep(100 * time.Millisecond) // a good few keepalives' worth
	if _, err := lazy.ServiceRunning(t.Context(), "sshd"); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if got := server.dials(); got != 1 {
		t.Errorf("the peer saw %d dials, want 1: keepalives must not break a healthy session", got)
	}
}
