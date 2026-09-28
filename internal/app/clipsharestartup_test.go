package app

import (
	"context"
	"testing"
)

func TestConfiguredStartupEnablesOnlyNewCopiesAndPreservesPause(t *testing.T) {
	c, here, there := clipUnderTest(0)
	c.cfg.EnableOnStart = true
	here.copyText("before startup")
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	if !c.On() {
		t.Fatal("startup left clipboard off")
	}
	c.tick(ctx)
	if len(there.received()) != 0 {
		t.Fatal("shared pre-start clipboard")
	}
	here.copyText("new copy")
	c.tick(ctx)
	if got := there.received(); len(got) != 1 || got[0] != "new copy" {
		t.Fatal(got)
	}
	c.TurnOff()
	c.Start(ctx)
	if c.On() {
		t.Fatal("repeated start undid pause")
	}
}

func TestConfiguredStartupHandlesMissingClipboard(t *testing.T) {
	c, here, _ := clipUnderTest(0)
	c.cfg.EnableOnStart = true
	here.breakSequence(errBroken)
	ctx, cancel := context.WithCancel(context.Background())
	c.Start(ctx)
	cancel()
	c.Wait()
	if c.On() || c.Status().Err == "" {
		t.Fatal("missing clipboard not reported")
	}
}

func TestConfiguredStartupRetriesTransientClipboardFailure(t *testing.T) {
	c, here, _ := clipUnderTest(0)
	c.cfg.EnableOnStart = true
	here.breakSequence(errBroken)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); c.Wait() }()
	c.Start(ctx)
	if c.On() {
		t.Fatal("clipboard should wait for a usable sequence")
	}
	here.breakSequence(nil)
	c.enableAtStartup()
	if !c.On() {
		t.Fatal("clipboard did not recover")
	}
	c.TurnOff()
	c.enableAtStartup()
	if c.On() {
		t.Fatal("startup retry overrode the user's pause")
	}
}
