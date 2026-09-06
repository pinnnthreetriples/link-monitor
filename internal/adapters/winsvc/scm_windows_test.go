//go:build windows

package winsvc

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// absentServiceName is a name no machine can have a service under. Building it
// from the pid and the clock is what keeps these tests independent of which
// services happen to be installed: they assert against a service that provably
// is not there, and so never start, stop, or even open a real one.
func absentServiceName() string {
	return fmt.Sprintf("linkmon-absent-%d-%d", os.Getpid(), time.Now().UnixNano())
}

// requireSCM skips when this machine will not let us connect to the service
// control manager at all. Connecting needs no elevation, so this should never
// fire; skipping rather than failing keeps a locked-down agent from reporting a
// defect in this package.
func requireSCM(t *testing.T) {
	t.Helper()

	h, err := windows.OpenSCManager(nil, nil, scmConnectAccess)
	if err != nil {
		t.Skipf("cannot connect to the service control manager: %v", err)
	}
	if err := windows.CloseServiceHandle(h); err != nil {
		t.Logf("closing the probe SCM handle: %v", err)
	}
}

func TestClassifyMapsWindowsCodesToSentinels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   error
		want error
	}{
		{name: "no error stays no error", in: nil, want: nil},
		{
			name: "1060 is a service that is not installed",
			in:   windows.ERROR_SERVICE_DOES_NOT_EXIST,
			want: ErrNotInstalled,
		},
		{
			name: "5 is missing administrator rights",
			in:   windows.ERROR_ACCESS_DENIED,
			want: ErrAccessDenied,
		},
		{
			name: "1056 is nothing left to do",
			in:   windows.ERROR_SERVICE_ALREADY_RUNNING,
			want: errAlreadyRunning,
		},
		{
			name: "anything else is passed through untouched",
			in:   windows.ERROR_SERVICE_DISABLED,
			want: windows.ERROR_SERVICE_DISABLED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tc.in)
			if tc.want == nil {
				if got != nil {
					t.Fatalf("classify(nil) = %v, want nil", got)
				}
				return
			}
			if !errors.Is(got, tc.want) {
				t.Fatalf("classify(%v) = %v, want one wrapping %v", tc.in, got, tc.want)
			}
			// The original code must survive the mapping, so a log still names
			// the real cause rather than only our summary of it.
			if !errors.Is(got, tc.in) {
				t.Errorf("classify(%v) = %v, want the original error kept in the chain", tc.in, got)
			}
		})
	}
}

func TestFromSvcStateReducesTheSCMStates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		in   svc.State
		want state
	}{
		{svc.Running, stateRunning},
		{svc.StartPending, statePending},
		{svc.ContinuePending, statePending},
		{svc.Stopped, stateStopped},
		{svc.StopPending, stateStopped},
		{svc.PausePending, stateStopped},
		{svc.Paused, stateStopped},
		{svc.State(99), stateStopped},
	} {
		if got := fromSvcState(tc.in); got != tc.want {
			t.Errorf("fromSvcState(%d) = %v, want %v", uint32(tc.in), got, tc.want)
		}
	}
}

func TestOpenServiceRejectsANameWindowsCannotEncode(t *testing.T) {
	t.Parallel()

	// A NUL would silently truncate the name inside the Windows API, so it is
	// refused at the encoding step rather than sent on.
	if _, err := openService("ssh\x00d", serviceQueryAccess); err == nil {
		t.Fatal("openService() accepted a name containing a NUL")
	}
}

func TestHandlesCloseToleratesEmptyHandles(t *testing.T) {
	t.Parallel()

	// A failed open returns a zero handles value; closing it must be harmless.
	handles{}.close()
}

func TestQueryingAnAbsentServiceSaysNotInstalled(t *testing.T) {
	t.Parallel()
	requireSCM(t)

	name := absentServiceName()
	got, err := systemController{}.query(t.Context(), name)

	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("query(%q) error = %v, want one wrapping ErrNotInstalled", name, err)
	}
	if got != stateStopped {
		t.Errorf("query(%q) = %v, want the zero state alongside the error", name, got)
	}
}

// TestStartingAnAbsentServiceSaysNotInstalled exercises the start path without
// touching a real service: the SCM resolves the name before it checks rights,
// so this needs no elevation and starts nothing.
func TestStartingAnAbsentServiceSaysNotInstalled(t *testing.T) {
	t.Parallel()
	requireSCM(t)

	name := absentServiceName()
	err := systemController{}.start(t.Context(), name)

	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("start(%q) error = %v, want one wrapping ErrNotInstalled", name, err)
	}
}

// TestQueryingAnyRealServiceAnswersWithoutError covers the success path
// through the real SCM. It deliberately does not name a service: it asks the
// SCM which ones exist and takes the first that answers, so the test depends on
// Windows having services at all and on nothing else. It only reads status —
// never starts or stops anything — and reading needs no elevation.
func TestQueryingAnyRealServiceAnswersWithoutError(t *testing.T) {
	t.Parallel()
	requireSCM(t)

	name := anyQueryableService(t)
	got, err := systemController{}.query(t.Context(), name)
	if err != nil {
		t.Fatalf("query(%q) error = %v, want none", name, err)
	}

	// Which state it is in is the machine's business, not this test's. That it
	// is one of the three we model is ours.
	switch got {
	case stateStopped, stateRunning, statePending:
	default:
		t.Errorf("query(%q) = %v, want one of stopped, running, pending", name, got)
	}
}

// anyQueryableService returns the name of some installed service this process
// may read the status of, or skips the test when there is none.
func anyQueryableService(t *testing.T) string {
	t.Helper()

	const enumAccess = uint32(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	h, err := windows.OpenSCManager(nil, nil, enumAccess)
	if err != nil {
		t.Skipf("cannot enumerate services: %v", err)
	}
	scm := &mgr.Mgr{Handle: h}
	defer func() {
		if err := scm.Disconnect(); err != nil {
			t.Logf("disconnecting the enumeration handle: %v", err)
		}
	}()

	names, err := scm.ListServices()
	if err != nil {
		t.Skipf("cannot list services: %v", err)
	}
	for _, name := range names {
		if _, err := (systemController{}).query(t.Context(), name); err == nil {
			return name
		}
	}
	t.Skip("no installed service is readable by this process")
	return ""
}

// TestQueryHandleReportsABrokenHandle covers the failure half of the same call.
// An invalid handle is the one way to make QueryServiceStatusEx fail that needs
// no special rights and touches no real service.
func TestQueryHandleReportsABrokenHandle(t *testing.T) {
	t.Parallel()

	got, err := queryHandle(&mgr.Service{Name: sshdService, Handle: windows.InvalidHandle})
	if err == nil {
		t.Fatalf("queryHandle() = %v, nil; want an error for an invalid handle", got)
	}
	if got != stateStopped {
		t.Errorf("queryHandle() = %v, want the zero state alongside the error", got)
	}
}

// TestTheRealClientTreatsAnAbsentServiceAsFalse is the end-to-end shape of the
// contract, run against the machine's own SCM rather than a fake: an absent
// service is a plain false from both boolean calls, and an error from neither.
func TestTheRealClientTreatsAnAbsentServiceAsFalse(t *testing.T) {
	t.Parallel()
	requireSCM(t)

	name := absentServiceName()
	c := New()

	running, err := c.LocalServiceRunning(t.Context(), name)
	if err != nil {
		t.Fatalf("LocalServiceRunning(%q) error = %v, want none", name, err)
	}
	if running {
		t.Errorf("LocalServiceRunning(%q) = true for a service that does not exist", name)
	}

	installed, err := c.ServiceInstalled(t.Context(), name)
	if err != nil {
		t.Fatalf("ServiceInstalled(%q) error = %v, want none", name, err)
	}
	if installed {
		t.Errorf("ServiceInstalled(%q) = true for a service that does not exist", name)
	}
}

// TestTheRealClientReportsAnAbsentServiceOnStart is the other half: what the
// boolean calls swallow, StartService must say out loud, because a missing
// OpenSSH Server needs installing rather than starting.
func TestTheRealClientReportsAnAbsentServiceOnStart(t *testing.T) {
	t.Parallel()
	requireSCM(t)

	name := absentServiceName()
	err := New().StartService(t.Context(), name)

	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("StartService(%q) error = %v, want one wrapping ErrNotInstalled", name, err)
	}
}
