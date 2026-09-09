package app

import (
	"context"
	"errors"
	"testing"
)

func TestForwardsStartRegistersATunnel(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{withAddr: true}
	f := NewForwards(context.Background(), fwd, nil)

	got, err := f.Start(context.Background(), 0, "100.127.188.87", 3389)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.ID == "" || got.RemoteHost != "100.127.188.87" || got.RemotePort != 3389 {
		t.Fatalf("Start = %+v", got)
	}
	if got.Addr != "127.0.0.1:40000" {
		t.Errorf("Addr = %q, want the address the forward actually bound", got.Addr)
	}

	list := f.List()
	if len(list) != 1 || list[0].ID != got.ID {
		t.Errorf("List = %+v", list)
	}
}

func TestForwardsFallBackToTheRequestedPortWhenTheTunnelCannotSayWhereItBound(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{}, nil)
	got, err := f.Start(context.Background(), 5555, "100.127.188.87", 22)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got.Addr != "127.0.0.1:5555" {
		t.Errorf("Addr = %q", got.Addr)
	}
}

func TestForwardsListIsInTheOrderTheyWereOpened(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{withAddr: true}, nil)
	for _, port := range []int{22, 3389, 5900} {
		if _, err := f.Start(context.Background(), 0, "100.127.188.87", port); err != nil {
			t.Fatalf("Start %d: %v", port, err)
		}
	}
	list := f.List()
	if len(list) != 3 {
		t.Fatalf("got %d forwards", len(list))
	}
	for i, want := range []int{22, 3389, 5900} {
		if list[i].RemotePort != want {
			t.Errorf("forward %d is to port %d, want %d", i, list[i].RemotePort, want)
		}
	}
}

func TestForwardsStopClosesExactlyOne(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{withAddr: true}
	f := NewForwards(context.Background(), fwd, nil)
	first, err := f.Start(context.Background(), 0, "100.127.188.87", 22)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, err := f.Start(context.Background(), 0, "100.127.188.87", 3389); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := f.Stop(first.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if fwd.opened[0].closes() != 1 || fwd.opened[1].closes() != 0 {
		t.Errorf("closed the wrong tunnel: %d, %d", fwd.opened[0].closes(), fwd.opened[1].closes())
	}
	if len(f.List()) != 1 {
		t.Errorf("the stopped forward is still listed: %+v", f.List())
	}
}

func TestForwardsStopReportsAnUnknownID(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{}, nil)
	if err := f.Stop("fwd-99"); !errors.Is(err, ErrForwardNotFound) {
		t.Errorf("Stop err = %v", err)
	}
}

func TestForwardsStopSurfacesACloseFailureButStillForgetsTheTunnel(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{withAddr: true}
	f := NewForwards(context.Background(), fwd, nil)
	got, err := f.Start(context.Background(), 0, "100.127.188.87", 22)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	fwd.opened[0].closeErr = errBoom

	if err := f.Stop(got.ID); !errors.Is(err, errBoom) {
		t.Errorf("Stop err = %v", err)
	}
	if len(f.List()) != 0 {
		t.Error("a forward that failed to close is still listed")
	}
}

func TestForwardsCloseAllStopsEverythingAndJoinsTheFailures(t *testing.T) {
	t.Parallel()

	fwd := &fakeForwarder{withAddr: true}
	f := NewForwards(context.Background(), fwd, nil)
	for range 3 {
		if _, err := f.Start(context.Background(), 0, "100.127.188.87", 22); err != nil {
			t.Fatalf("Start: %v", err)
		}
	}
	fwd.opened[1].closeErr = errBoom

	if err := f.CloseAll(); !errors.Is(err, errBoom) {
		t.Errorf("CloseAll err = %v", err)
	}
	for i, c := range fwd.opened {
		if c.closes() != 1 {
			t.Errorf("forward %d was closed %d times", i, c.closes())
		}
	}
	if len(f.List()) != 0 {
		t.Error("CloseAll left something in the registry")
	}
	if err := f.CloseAll(); err != nil {
		t.Errorf("a second CloseAll = %v, want nil", err)
	}
}

func TestForwardsRejectNonsense(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{}, nil)
	ctx := context.Background()

	if _, err := f.Start(ctx, 0, "", 22); err == nil {
		t.Error("an empty remote host was accepted")
	}
	if _, err := f.Start(ctx, -1, "peer", 22); err == nil {
		t.Error("a negative local port was accepted")
	}
	if _, err := f.Start(ctx, 70000, "peer", 22); err == nil {
		t.Error("an out-of-range local port was accepted")
	}
	if _, err := f.Start(ctx, 0, "peer", 0); err == nil {
		t.Error("remote port 0 was accepted")
	}
	if _, err := f.Start(ctx, 0, "peer", 70000); err == nil {
		t.Error("an out-of-range remote port was accepted")
	}
}

func TestForwardsHonourTheRequestContext(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := f.Start(ctx, 0, "peer", 22); !errors.Is(err, context.Canceled) {
		t.Errorf("Start with a dead request context = %v", err)
	}
}

func TestForwardsSurfaceAnAdapterFailure(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), &fakeForwarder{err: errBoom}, nil)
	if _, err := f.Start(context.Background(), 0, "peer", 22); !errors.Is(err, errBoom) {
		t.Errorf("Start err = %v", err)
	}
}

func TestForwardsWithoutAnSSHSessionSayNo(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), nil, nil)
	if _, err := f.Start(context.Background(), 0, "peer", 22); !errors.Is(err, ErrNoSSHSession) {
		t.Errorf("Start err = %v", err)
	}
}

func TestForwardsServePublishesAPort(t *testing.T) {
	t.Parallel()

	pub := &fakePublisher{url: "https://workspace-claude-pc.ts.net/"}
	f := NewForwards(context.Background(), nil, pub)

	url, err := f.Serve(context.Background(), 8080)
	if err != nil || url != pub.url {
		t.Fatalf("Serve = (%q, %v)", url, err)
	}
	if pub.lastPort != 8080 {
		t.Errorf("Serve got port %d", pub.lastPort)
	}
	if err := f.ServeReset(context.Background()); err != nil || pub.resets != 1 {
		t.Errorf("ServeReset = %v after %d resets", err, pub.resets)
	}
}

func TestForwardsServeRejectsBadPortsAndFailures(t *testing.T) {
	t.Parallel()

	f := NewForwards(context.Background(), nil, &fakePublisher{err: errBoom, resetErr: errBoom})
	if _, err := f.Serve(context.Background(), 0); err == nil {
		t.Error("port 0 was accepted")
	}
	if _, err := f.Serve(context.Background(), 70000); err == nil {
		t.Error("an out-of-range port was accepted")
	}
	if _, err := f.Serve(context.Background(), 8080); !errors.Is(err, errBoom) {
		t.Errorf("Serve err = %v", err)
	}
	if err := f.ServeReset(context.Background()); !errors.Is(err, errBoom) {
		t.Errorf("ServeReset err = %v", err)
	}

	none := NewForwards(context.Background(), nil, nil)
	if _, err := none.Serve(context.Background(), 8080); !errors.Is(err, ErrNoServe) {
		t.Errorf("Serve without a publisher = %v", err)
	}
	if err := none.ServeReset(context.Background()); !errors.Is(err, ErrNoServe) {
		t.Errorf("ServeReset without a publisher = %v", err)
	}
}
