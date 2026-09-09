//go:build !windows

package localendpoint

import (
	"errors"
	"fmt"
	"runtime"
	"time"
)

// errUnsupportedPlatform is what the stub below returns. It lives in this file
// because only this build has any use for it: on Windows the process really is
// looked up and there is no such outcome to report.
var errUnsupportedPlatform = errors.New("unsupported platform")

// osProcess has nothing to ask away from Windows.
type osProcess struct{}

// info refuses rather than guessing. A creation time is the whole basis of the
// staleness check, and every other operating system reports it at a different
// resolution and from a different place; a substitute that answered "probably
// alive" would turn this package's one guarantee into a hope. Away from
// Windows a reader therefore finds every record stale, which is the safe
// answer: the caller does the job itself.
func (osProcess) info(pid int) (string, time.Time, error) {
	return "", time.Time{}, fmt.Errorf("looking up process %d on %s: %w",
		pid, runtime.GOOS, errUnsupportedPlatform)
}
