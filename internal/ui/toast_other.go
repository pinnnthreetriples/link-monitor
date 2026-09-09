//go:build !windows

package ui

import (
	"fmt"
	"runtime"
)

// toastCommand has no answer away from Windows: the notification is raised
// through the Windows Runtime, which exists nowhere else. The stub keeps
// cross-builds and `go vet` clean.
func toastCommand(script string) (name string, args []string, err error) {
	return "", nil, fmt.Errorf("showing a notification on %s (%d bytes of script): %w",
		runtime.GOOS, len(script), errUnsupportedPlatform)
}
