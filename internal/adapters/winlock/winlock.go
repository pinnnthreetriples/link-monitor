// Package winlock holds the machine-wide single-instance lock.
//
// A tray program that runs twice is not merely untidy: there would be two tray
// icons the user cannot tell apart, two pollers probing the same link, and two
// processes fighting over the same Taildrop inbox. The lock is a named kernel
// object, so it disappears with the process that holds it — including a process
// that was killed, which a lock file could not manage.
package winlock

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAlreadyRunning reports that another process already holds the lock, so
// this one is a second copy and should say so and exit.
var ErrAlreadyRunning = errors.New("another instance is already running")

// errBadName rejects a name the kernel would either refuse or, worse, quietly
// resolve somewhere other than the caller meant.
var errBadName = errors.New("invalid lock name")

// nameLimit is the length the object-name namespace allows. The kernel accepts
// up to MAX_PATH characters, and we prepend a short prefix.
const nameLimit = 200

// Acquire takes a machine-wide named lock under the current session. The
// returned release must be called on shutdown; releasing twice is harmless.
// It returns ErrAlreadyRunning when another instance of the program is live.
func Acquire(name string) (release func() error, err error) {
	if err := checkName(name); err != nil {
		return nil, fmt.Errorf("acquiring the lock %q: %w", name, err)
	}
	return acquire(name)
}

// checkName is the whole of the validation, kept apart from the syscall so it
// can be tested without taking a lock.
//
// A backslash is refused rather than passed through because it is the
// namespace separator: a name containing one would silently move the lock into
// Global\ or a session namespace of the caller's choosing, and the point of
// this package is that every copy of the program competes for exactly one
// object.
func checkName(name string) error {
	switch {
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("%w: the name is empty", errBadName)
	case strings.Contains(name, `\`):
		return fmt.Errorf(`%w: the name may not contain a backslash`, errBadName)
	case len(name) > nameLimit:
		return fmt.Errorf("%w: %d bytes, the limit is %d", errBadName, len(name), nameLimit)
	default:
		return nil
	}
}
