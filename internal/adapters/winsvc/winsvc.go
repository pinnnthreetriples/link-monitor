// Package winsvc is the adapter over the Windows Service Control Manager on
// THIS machine. It answers the three questions the dashboard and the "fix it"
// button need: is a service running, is it installed at all, and please start
// it.
//
// Why it exists: inbound SSH cannot possibly work while this machine's own sshd
// service is stopped, and that is knowable without asking anyone. The program
// used to infer the inbound direction from the outbound one, and that inference
// was wrong in a case that really happened here — outbound worked while this
// side had no OpenSSH Server installed at all. A stopped local service is
// proof, not a guess, which is the whole reason this adapter exists.
//
// A service that is not installed is (false, nil) from both boolean calls, not
// an error: "not installed" is a successful answer, not a failed question.
// [Client.ServiceInstalled] is what tells the two apart, because they lead the
// user to different actions — a missing OpenSSH Server asks for an install, a
// stopped one asks for a start.
//
// Errors here are English and machine-matchable; the Russian the user reads is
// composed upstream from the sentinels [ErrAccessDenied] and [ErrNotInstalled].
//
// Everything impure sits behind the unexported controller interface, so no test
// needs an elevated process, a real service, or any particular service to
// happen to exist on the machine.
package winsvc

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Sentinel errors, matched with errors.Is. They always travel wrapped, so the
// message keeps the service name and the underlying Windows error alongside.
var (
	// ErrAccessDenied means the operation needs administrator rights and this
	// process does not have them. Starting a service does; querying one does
	// not. The UI turns this into "нужны права администратора"; whether to
	// relaunch elevated is the app layer's decision, never this package's.
	ErrAccessDenied = errors.New("winsvc: access denied, administrator rights required")

	// ErrNotInstalled means there is no such service on this machine. The two
	// boolean calls swallow it and answer false; StartService reports it,
	// because a missing OpenSSH Server is exactly what the user must act on.
	ErrNotInstalled = errors.New("winsvc: service is not installed")

	// ErrUnsupported is what every call returns off Windows. The package builds
	// and vets on every platform so that the rest of the program can too.
	ErrUnsupported = errors.New("winsvc: Windows services are unavailable on this platform")

	// errEmptyName guards against a caller passing a name it never filled in.
	errEmptyName = errors.New("winsvc: empty service name")

	// errAlreadyRunning is not a failure to start: the service is already in
	// the state the caller asked for. It stays unexported because no caller
	// ever sees it — StartService turns it into a nil error.
	errAlreadyRunning = errors.New("winsvc: service is already running")
)

// state is what the SCM reports about one service, reduced to the three cases
// this program acts on.
type state int

const (
	// stateStopped covers everything that is neither running nor on its way
	// there: stopped, stopping, pausing, paused. None of them serve
	// connections, and the user's next action is the same for all of them.
	stateStopped state = iota

	// stateRunning is the only state in which inbound SSH can work.
	stateRunning

	// statePending means the service is on its way up: not usable yet, but a
	// start request would have nothing left to do.
	statePending
)

// String reports the state as a stable lowercase token, for tests and logs.
func (s state) String() string {
	switch s {
	case stateStopped:
		return "stopped"
	case stateRunning:
		return "running"
	case statePending:
		return "pending"
	default:
		return fmt.Sprintf("state(%d)", int(s))
	}
}

// controller is the impure part of this package: the actual conversation with
// the Service Control Manager. The real implementation is per-platform; tests
// supply a fake, which is why no test needs elevation or a real service.
type controller interface {
	// query reports the current state of the named service. It returns an error
	// wrapping ErrNotInstalled when no such service exists, and one wrapping
	// ErrAccessDenied when the caller may not even look.
	query(ctx context.Context, name string) (state, error)

	// start asks the SCM to start the named service and waits until it reports
	// running. It returns an error wrapping ErrAccessDenied when the process is
	// not elevated, and one wrapping errAlreadyRunning when there was nothing
	// to do.
	start(ctx context.Context, name string) error
}

// Client answers questions about Windows services on this machine. The zero
// value is not usable; call [New].
type Client struct {
	ctl controller
}

// New returns a Client talking to the real Service Control Manager. It opens no
// handle until a call is made, so constructing one cannot fail and costs
// nothing on a non-Windows build.
func New() *Client {
	return &Client{ctl: newSystemController()}
}

// newWith is the seam tests use to install a fake controller.
func newWith(ctl controller) *Client {
	return &Client{ctl: ctl}
}

// LocalServiceRunning reports whether the named service is running on this
// machine. It satisfies the core.Probe method of the same name.
//
// A service that is not installed is not running, so that is (false, nil): the
// question was answered, not refused. Use [Client.ServiceInstalled] when the
// difference matters.
func (c *Client) LocalServiceRunning(ctx context.Context, name string) (bool, error) {
	st, err := c.state(ctx, name)
	switch {
	case errors.Is(err, ErrNotInstalled):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("checking whether the local service %q is running: %w", name, err)
	}
	return st == stateRunning, nil
}

// ServiceInstalled reports whether the named service exists on this machine at
// all, running or not. This is the call that separates "не установлен" from
// "остановлена": the two lead the user to different actions, so it never
// collapses them into one answer.
func (c *Client) ServiceInstalled(ctx context.Context, name string) (bool, error) {
	_, err := c.state(ctx, name)
	switch {
	case errors.Is(err, ErrNotInstalled):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("checking whether the local service %q is installed: %w", name, err)
	}
	return true, nil
}

// StartService starts the named service and waits until it reports running.
//
// It needs administrator rights: without them it fails with an error wrapping
// [ErrAccessDenied], so the UI can say so plainly instead of showing a raw
// Windows failure. It never tries to elevate — that decision belongs to the app
// layer. Starting a service that is already running succeeds.
func (c *Client) StartService(ctx context.Context, name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	_, err := call(ctx, func() (struct{}, error) {
		return struct{}{}, c.ctl.start(ctx, name)
	})
	switch {
	case errors.Is(err, errAlreadyRunning):
		// Nothing to do is not a failure: the caller wanted it running, and it
		// is running. Upstream never learns the difference.
		return nil
	case err != nil:
		return fmt.Errorf("starting the local service %q: %w", name, err)
	}
	return nil
}

// state is the single query both boolean calls are built on.
func (c *Client) state(ctx context.Context, name string) (state, error) {
	if err := validateName(name); err != nil {
		return stateStopped, err
	}
	return call(ctx, func() (state, error) { return c.ctl.query(ctx, name) })
}

// validateName rejects what the SCM would only reject later, and with a worse
// message. Service names are short identifiers; a control character means the
// caller passed something it should not have, and a NUL would silently truncate
// the name on the way into the Windows API.
func validateName(name string) error {
	if name == "" {
		return errEmptyName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("winsvc: refusing a service name with a control character (%U)", r)
		}
	}
	return nil
}

// call runs fn — a blocking Windows call that knows nothing about contexts —
// and gives up on it the moment the context is done, so every exported call
// honours cancellation even though the SCM API cannot.
//
// fn keeps running in its goroutine and writes to a buffered channel, so an
// abandoned call can neither block nor leak: SCM handles are per-call and the
// syscalls behind them are short.
func call[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, fmt.Errorf("winsvc: %w", err)
	}

	type outcome struct {
		val T
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		val, err := fn()
		done <- outcome{val: val, err: err}
	}()

	select {
	case <-ctx.Done():
		return zero, fmt.Errorf("winsvc: %w", ctx.Err())
	case res := <-done:
		return res.val, res.err
	}
}

// waitRunning polls until the service reports running. StartService only asks
// the SCM to start it; the service is not usable until it answers, and the "fix
// it" button must not report success before then.
//
// poll is the query the caller already holds a handle for, so this function
// needs no Windows API of its own and stays testable on any platform.
func waitRunning(ctx context.Context, name string, every time.Duration,
	poll func() (state, error),
) error {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		st, err := poll()
		if err != nil {
			return fmt.Errorf("waiting for the service %q to start: %w", name, err)
		}
		if st == stateRunning {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for the service %q to start: %w", name, ctx.Err())
		case <-ticker.C:
		}
	}
}
