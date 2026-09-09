//go:build windows

package winlock

import (
	"errors"
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

// namePrefix puts the object in the caller's own session namespace.
//
// Global\ would be the wrong choice twice over: creating an object there needs
// the SeCreateGlobalPrivilege an ordinary user does not have, and the question
// this lock answers is "is this user already running the program?", not "is
// anybody on this machine running it?". Two people logged into the same box
// each get their own tray icon and their own lock.
const namePrefix = `Local\`

// acquire creates the named mutex and reports whether it already existed.
//
// The mutex is created without initial ownership on purpose. Ownership of a
// Windows mutex belongs to a thread, and a Go caller has no control over which
// OS thread it is on — a later ReleaseMutex could arrive from a different
// thread and fail with ERROR_NOT_OWNER. Nothing here ever waits on the mutex:
// the lock is the *existence* of the named object, which lasts exactly as long
// as some process holds a handle to it, and which the kernel cleans up if we
// are killed.
func acquire(name string) (func() error, error) {
	full, err := windows.UTF16PtrFromString(namePrefix + name)
	if err != nil {
		return nil, fmt.Errorf("acquiring the lock %q: encoding the name: %w", name, err)
	}

	handle, err := windows.CreateMutex(nil, false, full)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		// The handle is valid even so, and leaking it would keep the object
		// alive after we exit — which would lock out the *next* honest start.
		if handle != 0 {
			if closeErr := windows.CloseHandle(handle); closeErr != nil {
				return nil, fmt.Errorf("acquiring the lock %q: closing the losing handle: %w",
					name, closeErr)
			}
		}
		return nil, fmt.Errorf("acquiring the lock %q: %w", name, ErrAlreadyRunning)
	}
	if err != nil {
		return nil, fmt.Errorf("acquiring the lock %q: %w", name, err)
	}

	return releaser(name, handle), nil
}

// releaser closes the handle exactly once and returns the same answer to every
// later call, so a shutdown path that runs twice — or from two goroutines at
// once — neither double-closes a handle nor shows the user a second error.
func releaser(name string, handle windows.Handle) func() error {
	var (
		once sync.Once
		err  error
	)
	return func() error {
		once.Do(func() {
			if closeErr := windows.CloseHandle(handle); closeErr != nil {
				err = fmt.Errorf("releasing the lock %q: %w", name, closeErr)
			}
		})
		return err
	}
}
