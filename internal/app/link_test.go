package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/tailscale"
)

// fakeLinkControl stands in for the Tailscale connect/disconnect commands.
type fakeLinkControl struct {
	upErr   error
	downErr error
	ups     int
	downs   int
	// block, when set, holds the call until it is closed, which is how the
	// timeout is exercised without waiting for a real one.
	block chan struct{}
}

func (f *fakeLinkControl) Up(ctx context.Context) error {
	f.ups++
	return f.wait(ctx, f.upErr)
}

func (f *fakeLinkControl) Down(ctx context.Context) error {
	f.downs++
	return f.wait(ctx, f.downErr)
}

// wait blocks when the test asked it to, and reports the context's verdict
// rather than the canned error when the deadline wins.
func (f *fakeLinkControl) wait(ctx context.Context, err error) error {
	if f.block == nil {
		return err
	}
	select {
	case <-f.block:
		return err
	case <-ctx.Done():
		return fmt.Errorf("tailscale up: %w", ctx.Err())
	}
}

// linkState is a canned answer to "is the daemon connected".
type linkState struct {
	up  bool
	err error
}

func (s linkState) TailscaleUp(context.Context) (bool, string, error) {
	return s.up, "", s.err
}

func TestParseLinkAction(t *testing.T) {
	t.Parallel()

	if a, ok := ParseLinkAction("up"); !ok || a != LinkUp {
		t.Errorf(`ParseLinkAction("up") = (%q, %v)`, a, ok)
	}
	if a, ok := ParseLinkAction("down"); !ok || a != LinkDown {
		t.Errorf(`ParseLinkAction("down") = (%q, %v)`, a, ok)
	}
	for _, bad := range []string{"", "UP", "подключить", "toggle"} {
		if _, ok := ParseLinkAction(bad); ok {
			t.Errorf("ParseLinkAction(%q) was accepted", bad)
		}
	}
}

func TestLinkConnectsAndDisconnects(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		action LinkAction
		state  linkState
		want   string
		ups    int
		downs  int
	}{
		{"up from down", LinkUp, linkState{up: false}, msgLinkUpDone, 1, 0},
		{"down from up", LinkDown, linkState{up: true}, msgLinkDownDone, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctl := &fakeLinkControl{}
			out := NewLink(ctl, tc.state, nil, 0).Apply(context.Background(), tc.action)
			if !out.OK || out.Message != tc.want || out.LoginURL != "" || out.NeedsAdmin {
				t.Fatalf("Apply = %+v", out)
			}
			if ctl.ups != tc.ups || ctl.downs != tc.downs {
				t.Errorf("ups %d, downs %d", ctl.ups, ctl.downs)
			}
		})
	}
}

// Pressing a button twice must be harmless, and must say so honestly rather
// than running a command that would do nothing.
func TestLinkPressingTheSameButtonTwiceIsASuccess(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		action LinkAction
		state  linkState
		want   string
	}{
		{"already up", LinkUp, linkState{up: true}, msgLinkAlreadyUp},
		{"already down", LinkDown, linkState{up: false}, msgLinkAlreadyDown},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctl := &fakeLinkControl{}
			out := NewLink(ctl, tc.state, nil, 0).Apply(context.Background(), tc.action)
			if !out.OK || out.Message != tc.want {
				t.Fatalf("Apply = %+v", out)
			}
			if ctl.ups != 0 || ctl.downs != 0 {
				t.Error("a link already in the asked-for state was commanded anyway")
			}
		})
	}
}

// A daemon that will not say where it stands must not stop the action: acting
// on a missing answer is better than refusing to act at all.
func TestLinkActsAnywayWhenTheStateCannotBeRead(t *testing.T) {
	t.Parallel()

	ctl := &fakeLinkControl{}
	out := NewLink(ctl, linkState{err: errBoom}, nil, 0).Apply(context.Background(), LinkUp)
	if !out.OK || out.Message != msgLinkUpDone || ctl.ups != 1 {
		t.Errorf("Apply = %+v after %d ups", out, ctl.ups)
	}

	ctl = &fakeLinkControl{}
	out = NewLink(ctl, nil, nil, 0).Apply(context.Background(), LinkDown)
	if !out.OK || out.Message != msgLinkDownDone || ctl.downs != 1 {
		t.Errorf("Apply without a state source = %+v", out)
	}
}

func TestLinkOffersTheLoginURLWhenTheDaemonWantsOne(t *testing.T) {
	t.Parallel()

	const url = "https://login.tailscale.com/a/0123456789abcdef"
	ctl := &fakeLinkControl{upErr: &tailscale.LoginRequiredError{URL: url}}

	out := NewLink(ctl, linkState{up: false}, nil, 0).Apply(context.Background(), LinkUp)
	if out.OK || out.LoginURL != url || out.Message != msgLinkLoginRequired {
		t.Fatalf("Apply = %+v", out)
	}
	if !strings.Contains(out.Message, "браузере") {
		t.Errorf("the message does not explain the browser login: %q", out.Message)
	}
}

func TestLinkHandlesALoginRequestWithNoURL(t *testing.T) {
	t.Parallel()

	// Wrapped, and with no URL to offer: the sentinel still has to be matched.
	err := fmt.Errorf("tailscale up: %w", tailscale.ErrLoginRequired)
	ctl := &fakeLinkControl{upErr: err}

	out := NewLink(ctl, linkState{up: false}, nil, 0).Apply(context.Background(), LinkUp)
	if out.OK || out.Message != msgLinkLoginRequired || out.LoginURL != "" {
		t.Errorf("Apply = %+v", out)
	}
}

func TestLinkReportsWhenAdminRightsAreTheObstacle(t *testing.T) {
	t.Parallel()

	err := fmt.Errorf("tailscale down: %w", tailscale.ErrAccessDenied)
	ctl := &fakeLinkControl{downErr: err}

	out := NewLink(ctl, linkState{up: true}, nil, 0).Apply(context.Background(), LinkDown)
	if out.OK || !out.NeedsAdmin || out.Message != msgLinkNeedsAdmin {
		t.Errorf("Apply = %+v", out)
	}
}

func TestLinkTranslatesTheOtherFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
		want string
	}{
		{"cancelled", fmt.Errorf("tailscale up: %w", context.Canceled), msgCancelled},
		{"timed out", fmt.Errorf("tailscale up: %w", context.DeadlineExceeded), msgLinkTimedOut},
		{"anything else", errBoom, msgLinkUpFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctl := &fakeLinkControl{upErr: tc.err}
			out := NewLink(ctl, linkState{up: false}, nil, 0).Apply(context.Background(), LinkUp)
			if out.OK || out.Message != tc.want {
				t.Errorf("Apply = %+v, want message %q", out, tc.want)
			}
		})
	}

	ctl := &fakeLinkControl{downErr: errBoom}
	out := NewLink(ctl, linkState{up: true}, nil, 0).Apply(context.Background(), LinkDown)
	if out.OK || out.Message != msgLinkDownFailed {
		t.Errorf("a failed disconnect = %+v", out)
	}
}

// A "tailscale up" that never finishes must fail rather than wedge the request.
func TestLinkGivesUpOnACommandThatNeverFinishes(t *testing.T) {
	t.Parallel()

	ctl := &fakeLinkControl{block: make(chan struct{})}
	defer close(ctl.block)

	out := NewLink(ctl, linkState{up: false}, nil, 20*time.Millisecond).
		Apply(context.Background(), LinkUp)
	if out.OK || out.Message != msgLinkTimedOut {
		t.Errorf("Apply = %+v", out)
	}
}

func TestLinkNudgesThePollerAfterAChange(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	<-probe.runs // the startup probe

	out := NewLink(&fakeLinkControl{}, linkState{up: false}, p, 0).Apply(ctx, LinkUp)
	if !out.OK {
		t.Fatalf("Apply = %+v", out)
	}
	select {
	case <-probe.runs:
	case <-time.After(2 * time.Second):
		t.Fatal("connecting did not nudge the poller")
	}

	cancel()
	p.Wait()
}

func TestLinkDoesNotNudgeAfterAFailure(t *testing.T) {
	t.Parallel()

	probe := newStubProbe()
	p := newTestPoller(t, probe, time.Hour, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	<-probe.runs

	ctl := &fakeLinkControl{upErr: errBoom}
	if out := NewLink(ctl, linkState{up: false}, p, 0).Apply(ctx, LinkUp); out.OK {
		t.Fatalf("Apply = %+v", out)
	}
	time.Sleep(20 * time.Millisecond)
	if len(probe.runs) != 0 {
		t.Error("a failed change nudged the poller anyway")
	}

	cancel()
	p.Wait()
}

func TestLinkWithoutAControllerOrWithAnUnknownAction(t *testing.T) {
	t.Parallel()

	if out := NewLink(nil, nil, nil, 0).Apply(context.Background(), LinkUp); out.Message != msgNoLinkControl {
		t.Errorf("no controller = %+v", out)
	}
	out := NewLink(&fakeLinkControl{}, nil, nil, 0).Apply(context.Background(), "toggle")
	if out.OK || out.Message != msgUnknownLinkAction {
		t.Errorf("unknown action = %+v", out)
	}
}

func TestNewLinkFillsInTheDefaultTimeout(t *testing.T) {
	t.Parallel()

	if got := NewLink(nil, nil, nil, 0).timeout; got != DefaultLinkTimeout {
		t.Errorf("timeout = %v, want %v", got, DefaultLinkTimeout)
	}
	if got := NewLink(nil, nil, nil, -time.Second).timeout; got != DefaultLinkTimeout {
		t.Errorf("timeout = %v, want %v", got, DefaultLinkTimeout)
	}
	if got := NewLink(nil, nil, nil, time.Minute).timeout; got != time.Minute {
		t.Errorf("timeout = %v, want a minute", got)
	}
}

func TestLoginURLIsReadFromTheTypedErrorOnly(t *testing.T) {
	t.Parallel()

	// The adapter keeps the URL out of the error text on purpose, so an error
	// that only mentions one must not have it scraped back out.
	if got := loginURL(errBoom); got != "" {
		t.Errorf("loginURL of an unrelated error = %q", got)
	}
	err := fmt.Errorf("wrapped: %w", &tailscale.LoginRequiredError{URL: "https://login.tailscale.com/a/x"})
	if got := loginURL(err); got != "https://login.tailscale.com/a/x" {
		t.Errorf("loginURL = %q", got)
	}
}
