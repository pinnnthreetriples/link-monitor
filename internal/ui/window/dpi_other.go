//go:build !windows

package window

import (
	"fmt"
	"runtime"
)

// setPerMonitorDPIAwareness has nothing to declare away from Windows: DPI
// awareness is a Windows process attribute. The stub keeps cross-builds and
// `go vet` clean, and it answers the same way a Windows too old to have the
// call does, so the caller needs only one path.
func setPerMonitorDPIAwareness() error {
	return fmt.Errorf("declaring per-monitor DPI on %s: %w", runtime.GOOS, errNoPerMonitorDPI)
}
