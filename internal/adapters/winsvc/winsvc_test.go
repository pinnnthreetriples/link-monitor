package winsvc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// sshdService is the service this program actually asks about: without it
// running, inbound SSH to this machine cannot work at all.
const sshdService = "sshd"

// errTransport stands for a failure to ask the question at all, as opposed to
// an answer of "no". Nothing upstream may confuse the two.
var errTransport = errors.New("the service control manager is not reachable")

// The method must keep the shape core.Probe declares for it. Declaring that one
// method here means a change to the port breaks this test rather than only the
// app wiring, and it keeps the adapter from importing core just to be checked.
type localServiceProbe interface {
	LocalServiceRunning(ctx context.Context, name string) (bool, error)
}

var _ localServiceProbe = (*Client)(nil)

// scmSays wraps a sentinel the way the real controller does, so the tests
// exercise errors.Is through a wrap rather than against a bare sentinel.
func scmSays(sentinel error) error {
	return fmt.Errorf("openservice failed: %w", sentinel)
}

// fakeController stands in for the Service Control Manager. Every field is
// optional; the zero value reports every service as stopped and starts
// everything successfully.
type fakeController struct {
	mu sync.Mutex

	// states is what successive query calls report; the last entry repeats, so
	// a one-element slice is a service that never changes.
	states []state
	// queryErr, when set, is what query returns instead of a state.
	queryErr error
	// startErr, when set, is what start returns.
	startErr error
	// startBlocks makes start wait for the context instead of returning, which
	// is how a slow SCM call is simulated without sleeping.
	startBlocks bool

	queried []string
	started []string
}

func (f *fakeController) query(_ context.Context, name string) (state, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.queried = append(f.queried, name)
	if f.queryErr != nil {
		return stateStopped, f.queryErr
	}
	if len(f.states) == 0 {
		return stateStopped, nil
	}
	st := f.states[0]
	if len(f.states) > 1 {
		f.states = f.states[1:]
	}
	return st, nil
}

func (f *fakeController) start(ctx context.Context, name string) error {
	f.mu.Lock()
	f.started = append(f.started, name)
	blocks, err := f.startBlocks, f.startErr
	f.mu.Unlock()

	if blocks {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeController) calls() (queried, started int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queried), len(f.started)
}

func TestStateString(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   state
		want string
	}{
		{stateStopped, "stopped"},
		{stateRunning, "running"},
		{statePending, "pending"},
		{state(42), "state(42)"},
	} {
		if got := tc.in.String(); got != tc.want {
			t.Errorf("state(%d).String() = %q, want %q", int(tc.in), got, tc.want)
		}
	}
}

func TestNewBuildsAClientWithTheSystemController(t *testing.T) {
	t.Parallel()

	c := New()
	if c == nil || c.ctl == nil {
		t.Fatalf("New() = %+v, want a client with a controller", c)
	}
}

func TestLocalServiceRunning(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		ctl      *fakeController
		want     bool
		wantErr  error
		wantFail bool
	}{
		{
			name: "a running service is running",
			ctl:  &fakeController{states: []state{stateRunning}},
			want: true,
		},
		{
			name: "a stopped service is not running",
			ctl:  &fakeController{states: []state{stateStopped}},
			want: false,
		},
		{
			name: "a starting service is not running yet",
			ctl:  &fakeController{states: []state{statePending}},
			want: false,
		},
		{
			name: "a service that is not installed is not running, and that is not an error",
			ctl:  &fakeController{queryErr: scmSays(ErrNotInstalled)},
			want: false,
		},
		{
			name:     "being refused the answer is an error, not a no",
			ctl:      &fakeController{queryErr: scmSays(ErrAccessDenied)},
			wantErr:  ErrAccessDenied,
			wantFail: true,
		},
		{
			name:     "an unreachable SCM is an error, not a no",
			ctl:      &fakeController{queryErr: errTransport},
			wantErr:  errTransport,
			wantFail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := newWith(tc.ctl).LocalServiceRunning(t.Context(), sshdService)

			switch {
			case tc.wantFail && err == nil:
				t.Fatalf("LocalServiceRunning() = %v, nil; want an error", got)
			case !tc.wantFail && err != nil:
				t.Fatalf("LocalServiceRunning() error = %v, want none", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("LocalServiceRunning() error = %v, want one wrapping %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("LocalServiceRunning() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMissingAndStoppedAreDifferentAnswers is the distinction the UI is built
// on: "OpenSSH Server не установлен" asks the user to install it, "служба
// остановлена" asks them to start it. Both look identical through
// LocalServiceRunning, so LocalServiceInstalled must separate them.
func TestMissingAndStoppedAreDifferentAnswers(t *testing.T) {
	t.Parallel()

	missing := newWith(&fakeController{queryErr: scmSays(ErrNotInstalled)})
	stopped := newWith(&fakeController{states: []state{stateStopped}})

	for _, c := range []*Client{missing, stopped} {
		running, err := c.LocalServiceRunning(t.Context(), sshdService)
		if err != nil || running {
			t.Fatalf("LocalServiceRunning() = %v, %v; want false, nil for both cases", running, err)
		}
	}

	installed, err := missing.LocalServiceInstalled(t.Context(), sshdService)
	if err != nil {
		t.Fatalf("LocalServiceInstalled() on a missing service: unexpected error %v", err)
	}
	if installed {
		t.Error("LocalServiceInstalled() = true for a service that is not installed")
	}

	installed, err = stopped.LocalServiceInstalled(t.Context(), sshdService)
	if err != nil {
		t.Fatalf("LocalServiceInstalled() on a stopped service: unexpected error %v", err)
	}
	if !installed {
		t.Error("LocalServiceInstalled() = false for a service that is installed but stopped")
	}
}

func TestServiceInstalled(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		ctl      *fakeController
		want     bool
		wantErr  error
		wantFail bool
	}{
		{
			name: "a running service is installed",
			ctl:  &fakeController{states: []state{stateRunning}},
			want: true,
		},
		{
			name: "a starting service is installed",
			ctl:  &fakeController{states: []state{statePending}},
			want: true,
		},
		{
			name: "a missing service is not installed, and that is not an error",
			ctl:  &fakeController{queryErr: scmSays(ErrNotInstalled)},
			want: false,
		},
		{
			name:     "being refused the answer is an error",
			ctl:      &fakeController{queryErr: scmSays(ErrAccessDenied)},
			wantErr:  ErrAccessDenied,
			wantFail: true,
		},
		{
			name:     "an unreachable SCM is an error",
			ctl:      &fakeController{queryErr: errTransport},
			wantErr:  errTransport,
			wantFail: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := newWith(tc.ctl).LocalServiceInstalled(t.Context(), sshdService)

			switch {
			case tc.wantFail && err == nil:
				t.Fatalf("LocalServiceInstalled() = %v, nil; want an error", got)
			case !tc.wantFail && err != nil:
				t.Fatalf("LocalServiceInstalled() error = %v, want none", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("LocalServiceInstalled() error = %v, want one wrapping %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("LocalServiceInstalled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStartService(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		ctl     *fakeController
		wantErr error
	}{
		{
			name: "a service that starts reports success",
			ctl:  &fakeController{},
		},
		{
			name: "a service already running is success, because it is running",
			ctl:  &fakeController{startErr: scmSays(errAlreadyRunning)},
		},
		{
			name:    "an unelevated process is told so, not handed a raw failure",
			ctl:     &fakeController{startErr: scmSays(ErrAccessDenied)},
			wantErr: ErrAccessDenied,
		},
		{
			name:    "a missing service is reported, not swallowed",
			ctl:     &fakeController{startErr: scmSays(ErrNotInstalled)},
			wantErr: ErrNotInstalled,
		},
		{
			name:    "any other failure is wrapped and passed on",
			ctl:     &fakeController{startErr: errTransport},
			wantErr: errTransport,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := newWith(tc.ctl).StartService(t.Context(), sshdService)

			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("StartService() error = %v, want none", err)
				}
				if _, started := tc.ctl.calls(); started != 1 {
					t.Errorf("StartService() asked the SCM %d times, want 1", started)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("StartService() error = %v, want one wrapping %v", err, tc.wantErr)
			}
			if !strings.Contains(err.Error(), sshdService) {
				t.Errorf("StartService() error = %q, want it to name the service", err)
			}
		})
	}
}

// TestStartServiceDoesNotElevate pins the division of labour: this package
// reports that rights are missing and stops there. Deciding to relaunch
// elevated belongs to the app layer.
func TestStartServiceDoesNotElevate(t *testing.T) {
	t.Parallel()

	ctl := &fakeController{startErr: scmSays(ErrAccessDenied)}
	err := newWith(ctl).StartService(t.Context(), sshdService)

	if !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("StartService() error = %v, want one wrapping ErrAccessDenied", err)
	}
	if _, started := ctl.calls(); started != 1 {
		t.Errorf("StartService() made %d attempts, want exactly 1 with no retry or elevation", started)
	}
}

func TestBadServiceNamesAreRefusedBeforeAnyCall(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		service string
		wantErr error
	}{
		{name: "empty", service: "", wantErr: errEmptyName},
		{name: "embedded NUL", service: "ssh\x00d"},
		{name: "newline", service: "sshd\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctl := &fakeController{}
			c := newWith(ctl)

			if _, err := c.LocalServiceRunning(t.Context(), tc.service); err == nil {
				t.Error("LocalServiceRunning() accepted an invalid service name")
			} else if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Errorf("LocalServiceRunning() error = %v, want one wrapping %v", err, tc.wantErr)
			}
			if _, err := c.LocalServiceInstalled(t.Context(), tc.service); err == nil {
				t.Error("LocalServiceInstalled() accepted an invalid service name")
			}
			if err := c.StartService(t.Context(), tc.service); err == nil {
				t.Error("StartService() accepted an invalid service name")
			}

			if queried, started := ctl.calls(); queried != 0 || started != 0 {
				t.Errorf("controller was called %d/%d times for an invalid name, want 0/0", queried, started)
			}
		})
	}
}

func TestACancelledContextStopsEveryCallBeforeItStarts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ctl := &fakeController{}
	c := newWith(ctl)

	if _, err := c.LocalServiceRunning(ctx, sshdService); !errors.Is(err, context.Canceled) {
		t.Errorf("LocalServiceRunning() error = %v, want context.Canceled", err)
	}
	if _, err := c.LocalServiceInstalled(ctx, sshdService); !errors.Is(err, context.Canceled) {
		t.Errorf("LocalServiceInstalled() error = %v, want context.Canceled", err)
	}
	if err := c.StartService(ctx, sshdService); !errors.Is(err, context.Canceled) {
		t.Errorf("StartService() error = %v, want context.Canceled", err)
	}

	if queried, started := ctl.calls(); queried != 0 || started != 0 {
		t.Errorf("controller was called %d/%d times on a cancelled context, want 0/0", queried, started)
	}
}

// TestCancellationCutsShortACallInFlight covers the reason call() exists: the
// SCM API takes no deadline, so a request already under way has to be abandoned
// rather than waited out.
func TestCancellationCutsShortACallInFlight(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	ctl := &fakeController{startBlocks: true}

	done := make(chan error, 1)
	go func() { done <- newWith(ctl).StartService(ctx, sshdService) }()

	// Let the call reach the controller before pulling the rug out.
	for {
		if _, started := ctl.calls(); started > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("StartService() error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StartService() did not return after its context was cancelled")
	}
}

func TestWaitRunningReturnsOnceTheServiceIsUp(t *testing.T) {
	t.Parallel()

	states := []state{stateStopped, statePending, statePending, stateRunning}
	var polls int

	err := waitRunning(t.Context(), sshdService, time.Millisecond, func() (state, error) {
		st := states[polls]
		polls++
		return st, nil
	})
	if err != nil {
		t.Fatalf("waitRunning() error = %v, want none", err)
	}
	if polls != len(states) {
		t.Errorf("waitRunning() polled %d times, want %d", polls, len(states))
	}
}

func TestWaitRunningReportsAFailedPoll(t *testing.T) {
	t.Parallel()

	err := waitRunning(t.Context(), sshdService, time.Millisecond, func() (state, error) {
		return stateStopped, errTransport
	})
	if !errors.Is(err, errTransport) {
		t.Fatalf("waitRunning() error = %v, want one wrapping errTransport", err)
	}
	if !strings.Contains(err.Error(), sshdService) {
		t.Errorf("waitRunning() error = %q, want it to name the service", err)
	}
}

// TestWaitRunningGivesUpWhenTheContextDoes matters for the UI: a service that
// hangs in "starting" must not hold the "fix it" button forever.
func TestWaitRunningGivesUpWhenTheContextDoes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	err := waitRunning(ctx, sshdService, time.Millisecond, func() (state, error) {
		return statePending, nil
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitRunning() error = %v, want context.DeadlineExceeded", err)
	}
}

// TestCallReturnsTheValueAndTheError checks the generic helper directly, since
// every exported method funnels through it.
func TestCallReturnsTheValueAndTheError(t *testing.T) {
	t.Parallel()

	got, err := call(t.Context(), func() (state, error) { return stateRunning, nil })
	if got != stateRunning || err != nil {
		t.Fatalf("call() = %v, %v; want running, nil", got, err)
	}

	got, err = call(t.Context(), func() (state, error) { return stateStopped, errTransport })
	if got != stateStopped || !errors.Is(err, errTransport) {
		t.Fatalf("call() = %v, %v; want stopped, errTransport", got, err)
	}
}
