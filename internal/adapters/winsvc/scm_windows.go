//go:build windows

package winsvc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// startPollInterval is how often a pending start is re-checked. sshd reaches
// running in well under a second; polling much slower would make the "fix it"
// button feel stuck, and much faster buys nothing.
const startPollInterval = 200 * time.Millisecond

// Access rights are asked for one at a time on purpose.
//
// mgr.Connect and mgr.OpenService request SC_MANAGER_ALL_ACCESS and
// SERVICE_ALL_ACCESS, which an unelevated process cannot be granted — using
// them would make even *reading* a service's state fail with access denied.
// Reading a state must work without administrator rights, since that is the
// probe the dashboard runs every poll, so we open the handles ourselves with
// the least right that answers the question at hand.
const (
	scmConnectAccess   = uint32(windows.SC_MANAGER_CONNECT)
	serviceQueryAccess = uint32(windows.SERVICE_QUERY_STATUS)
	serviceStartAccess = uint32(windows.SERVICE_START | windows.SERVICE_QUERY_STATUS)
)

// systemController is the real Service Control Manager on this machine.
type systemController struct{}

// newSystemController returns the controller [New] uses on Windows.
func newSystemController() controller { return systemController{} }

// query reports the state of one service. The context is honoured by the caller
// in call(), which abandons this goroutine if it takes too long; the SCM API
// itself has nowhere to put a deadline.
func (systemController) query(_ context.Context, name string) (state, error) {
	h, err := openService(name, serviceQueryAccess)
	if err != nil {
		return stateStopped, err
	}
	defer h.close()

	return queryHandle(h.svc)
}

// start asks the SCM to start the service, then waits for it to report running.
// Opening the handle with SERVICE_START is where an unelevated process is
// turned away, so ErrAccessDenied usually arrives before the request is even
// made — which is fine, since it is the same answer either way.
func (systemController) start(ctx context.Context, name string) error {
	h, err := openService(name, serviceStartAccess)
	if err != nil {
		return err
	}
	defer h.close()

	if err := h.svc.Start(); err != nil {
		return fmt.Errorf("requesting a start of the service %q: %w", name, classify(err))
	}
	return waitRunning(ctx, name, startPollInterval, func() (state, error) {
		return queryHandle(h.svc)
	})
}

// handles is one SCM connection and one open service, closed together.
type handles struct {
	scm *mgr.Mgr
	svc *mgr.Service
}

// close releases both handles. Closing can only fail on a handle that is
// already invalid, which leaves a caller nothing to do and nothing worth
// reporting, so those errors are dropped deliberately rather than ignored.
func (h handles) close() {
	if h.svc != nil {
		_ = h.svc.Close()
	}
	if h.scm != nil {
		_ = h.scm.Disconnect()
	}
}

// openService connects to the SCM and opens one service with the given access
// right. The caller closes what it gets back; on error nothing is left open.
func openService(name string, access uint32) (handles, error) {
	namePtr, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return handles{}, fmt.Errorf("encoding the service name %q: %w", name, err)
	}

	scm, err := windows.OpenSCManager(nil, nil, scmConnectAccess)
	if err != nil {
		return handles{}, fmt.Errorf("connecting to the service control manager: %w", classify(err))
	}
	h := handles{scm: &mgr.Mgr{Handle: scm}}

	svcHandle, err := windows.OpenService(h.scm.Handle, namePtr, access)
	if err != nil {
		h.close()
		return handles{}, fmt.Errorf("opening the service %q: %w", name, classify(err))
	}
	h.svc = &mgr.Service{Name: name, Handle: svcHandle}
	return h, nil
}

// queryHandle reads the current state through an already-open service handle.
func queryHandle(s *mgr.Service) (state, error) {
	status, err := s.Query()
	if err != nil {
		return stateStopped, fmt.Errorf("querying the service %q: %w", s.Name, classify(err))
	}
	return fromSvcState(status.State), nil
}

// fromSvcState reduces the SCM's seven states to the three this program acts
// on. Stopping, pausing and paused are lumped in with stopped: none of them
// serve connections, and they lead the user to the same next action.
func fromSvcState(s svc.State) state {
	switch s {
	case svc.Running:
		return stateRunning
	case svc.StartPending, svc.ContinuePending:
		return statePending
	default:
		return stateStopped
	}
}

// classify maps a Windows error code onto one of this package's sentinels,
// keeping the original in the chain so the message still names the real cause.
//
// It matches on the numeric code and never on the message: these machines run a
// Russian Windows, where the text is localised and the code is not.
func classify(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST):
		return fmt.Errorf("%w: %w", ErrNotInstalled, err)
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return fmt.Errorf("%w: %w", ErrAccessDenied, err)
	case errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING):
		return fmt.Errorf("%w: %w", errAlreadyRunning, err)
	default:
		return err
	}
}
