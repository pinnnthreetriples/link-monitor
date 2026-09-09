//go:build !windows

package winlock

import (
	"errors"
	"fmt"
	"runtime"
)

// errUnsupportedPlatform is what the stub below returns where named kernel
// objects do not exist. It lives in this file rather than beside Acquire
// because this is the only build that has any use for it: on Windows the lock
// is taken for real and there is no such outcome to report.
var errUnsupportedPlatform = errors.New("unsupported platform")

// acquire has no answer away from Windows: a named mutex in a session
// namespace exists nowhere else, and inventing a process-local substitute
// would report "lock held" for a lock that guards nothing.
func acquire(name string) (func() error, error) {
	return nil, fmt.Errorf("acquiring the lock %q on %s: %w", name, runtime.GOOS, errUnsupportedPlatform)
}
