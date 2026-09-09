//go:build !windows

package window

import (
	"fmt"
	"runtime"
)

// readRuntimeVersion has nothing to read away from Windows: WebView2 is an
// Edge component registered in the Windows registry. The stub keeps
// cross-builds and `go vet` clean rather than claiming a runtime is present.
func readRuntimeVersion() (string, error) {
	return "", fmt.Errorf("reading the WebView2 runtime version on %s: %w", runtime.GOOS, ErrRuntimeMissing)
}
