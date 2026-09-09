package ui

import (
	"context"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
)

func TestStatusOfNarrowsASnapshot(t *testing.T) {
	snap := core.Snapshot{
		Taken:   time.Now(),
		Overall: core.StateWarn,
		Summary: "Связь работает с перебоями",
		Detail:  "Потери пакетов 12 %",
		Latency: 42 * time.Millisecond,
		Checks:  []core.Check{{ID: core.CheckTailscale, State: core.StateOK}},
	}

	got := StatusOf(snap)
	want := Status{
		Overall: core.StateWarn,
		Summary: "Связь работает с перебоями",
		Detail:  "Потери пакетов 12 %",
	}
	if got != want {
		t.Errorf("StatusOf = %+v, want %+v", got, want)
	}
}

func TestTheFuncAdaptersForward(t *testing.T) {
	ch := make(chan Status)
	source := SourceFunc(func(context.Context) <-chan Status { return ch })
	if got := source.Subscribe(t.Context()); got != (<-chan Status)(ch) {
		t.Error("SourceFunc did not return the channel its function gave it")
	}

	called := false
	actions := ActionsFunc(func(context.Context) error { called = true; return nil })
	if err := actions.CheckNow(t.Context()); err != nil {
		t.Errorf("ActionsFunc: %v", err)
	}
	if !called {
		t.Error("ActionsFunc did not call its function")
	}

	var seen Notification
	notifier := NotifierFunc(func(_ context.Context, n Notification) error { seen = n; return nil })
	if err := notifier.Notify(t.Context(), Notification{Title: "Связь потеряна"}); err != nil {
		t.Errorf("NotifierFunc: %v", err)
	}
	if seen.Title != "Связь потеряна" {
		t.Errorf("NotifierFunc passed on %+v", seen)
	}

	raised := 0
	var window Windower = WindowerFunc(func() { raised++ })
	window.Show()
	if raised != 1 {
		t.Errorf("WindowerFunc called its function %d times, want 1", raised)
	}
}
