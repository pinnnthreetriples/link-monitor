//go:build windows

package localendpoint

import (
	"errors"
	"fmt"
	"math"
	"time"

	"golang.org/x/sys/windows"
)

// errNoSuchProcess reports that nothing is running under that process id: it
// exited, it was never used, or Windows refused to say. All three mean the
// same thing to a reader holding a record — there is nothing to confirm it
// against — so they are one sentinel rather than three.
var errNoSuchProcess = errors.New("no such process is running")

// osProcess asks Windows about a process.
type osProcess struct{}

// info returns the image path and the creation time of pid.
//
// PROCESS_QUERY_LIMITED_INFORMATION is deliberately the whole of the access
// asked for: it is enough for the creation time and the image path, it is
// granted for a process owned by this user, and it cannot read memory, inject a
// thread or terminate anything. A record is confirmed by looking, never by
// touching.
func (osProcess) info(pid int) (string, time.Time, error) {
	if pid <= 0 || pid > math.MaxUint32 {
		return "", time.Time{}, fmt.Errorf("looking up process %d: %w", pid, errNoSuchProcess)
	}
	// A Windows process id is a DWORD, and the two bounds checked on the line
	// above are the whole of this conversion's safety: nothing below zero and
	// nothing past the range a DWORD holds reaches here.
	id := uint32(pid) //nolint:gosec // G115: bounded immediately above
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, id)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("opening process %d: %w: %w", pid, err, errNoSuchProcess)
	}
	defer func() {
		// A failed close of a query-only handle leaks one handle in a process
		// that is about to answer and move on, and there is nothing the caller
		// could do about it; it must not mask the answer we came for.
		_ = windows.CloseHandle(handle)
	}()

	startedAt, err := creationTime(handle, pid)
	if err != nil {
		return "", time.Time{}, err
	}
	exe, err := imagePath(handle, pid)
	if err != nil {
		return "", time.Time{}, err
	}
	return exe, startedAt, nil
}

// creationTime reports when the process behind handle was created, and refuses
// a process that has already exited.
//
// The exit-time check is not redundant. A handle can be opened on a process
// that is gone but not yet reaped — anything still holding a handle to it keeps
// the object alive — and such a process still answers with its old creation
// time and image path, which would confirm a record that is in fact stale.
// Windows leaves the exit time at zero for as long as the process is running,
// so this costs nothing beyond reading a field that was already fetched.
func creationTime(handle windows.Handle, pid int) (time.Time, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return time.Time{}, fmt.Errorf("reading the times of process %d: %w: %w", pid, err, errNoSuchProcess)
	}
	if exit.HighDateTime != 0 || exit.LowDateTime != 0 {
		return time.Time{}, fmt.Errorf("process %d has exited: %w", pid, errNoSuchProcess)
	}
	return time.Unix(0, creation.Nanoseconds()).UTC(), nil
}

// imagePath reports the full path of the running image behind handle.
//
// QueryFullProcessImageName is asked rather than the process's command line
// because the command line is chosen by whoever started the process and the
// image path is chosen by the kernel. The buffer is the long-path maximum, so
// a path is never truncated into something that would compare unequal.
func imagePath(handle windows.Handle, pid int) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	// The buffer's length is the constant above, so the conversion cannot lose
	// anything: MAX_LONG_PATH is 32768.
	size := uint32(len(buf)) //nolint:gosec // G115: len of a fixed-size buffer

	if err := windows.QueryFullProcessImageName(handle, 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("reading the image path of process %d: %w: %w", pid, err, errNoSuchProcess)
	}
	return windows.UTF16ToString(buf[:size]), nil
}
