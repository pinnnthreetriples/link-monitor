//go:build !windows

package window

import (
	"fmt"
	"runtime"
)

// newNativeBackend has no window to create away from Windows: WebView2 is an
// Edge component, and this program is a Windows tray utility. The stub keeps
// cross-builds and `go vet` clean, and it fails the same way a Windows machine
// without the runtime does, so the caller needs only one fallback path.
func newNativeBackend(cfg Config, _ func()) (backend, error) {
	return nil, fmt.Errorf("opening the window %q on %s: %w", cfg.Title, runtime.GOOS, ErrRuntimeMissing)
}
