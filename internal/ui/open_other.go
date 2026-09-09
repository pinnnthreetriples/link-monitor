//go:build !windows

package ui

import (
	"fmt"
	"runtime"
)

// openCommand has no answer away from Windows. This program is a Windows tray
// utility; the stub keeps cross-builds and `go vet` honest rather than
// pretending some other desktop is available.
func openCommand(rawURL string) (name string, args []string, err error) {
	return "", nil, fmt.Errorf("opening %q on %s: %w", rawURL, runtime.GOOS, errUnsupportedPlatform)
}
