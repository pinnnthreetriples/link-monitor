//go:build !windows

package winsvc

import (
	"context"
	"fmt"
	"runtime"
)

// systemController stands in for the Service Control Manager on platforms that
// have none. It keeps the package honest: everything builds and vets on every
// platform, and every call says plainly that the question cannot be answered
// here rather than inventing an answer that would read as "not running".
type systemController struct{}

// newSystemController returns the controller [New] uses off Windows.
func newSystemController() controller { return systemController{} }

func (systemController) query(_ context.Context, name string) (state, error) {
	return stateStopped, unsupported(name)
}

func (systemController) start(_ context.Context, name string) error {
	return unsupported(name)
}

// unsupported names the service and the platform, so a stray call from a
// cross-build is obvious in the log rather than merely puzzling.
func unsupported(name string) error {
	return fmt.Errorf("service %q on %s: %w", name, runtime.GOOS, ErrUnsupported)
}
