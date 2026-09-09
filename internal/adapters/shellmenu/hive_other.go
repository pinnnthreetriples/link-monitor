//go:build !windows

package shellmenu

import (
	"errors"
	"fmt"
	"runtime"
)

// errUnsupportedPlatform is what the stub below returns. It lives in this file
// because only this build has any use for it: on Windows the registry is there
// and there is no such outcome to report.
var errUnsupportedPlatform = errors.New("unsupported platform")

// realHive returns the stub.
func realHive() hive { return noHive{} }

// noHive refuses everything away from Windows. Explorer's right-click menu is
// the Windows shell reading the Windows registry; there is nothing here to
// approximate, and a stub that reported "installed: false" and then silently
// succeeded would be a lie a caller could act on. The stub exists so that
// cross-builds and `go vet` stay clean.
type noHive struct{}

func (noHive) set(path string, _ map[string]string) error { return unsupported("writing", path) }

func (noHive) exists(path string) (bool, error) { return false, unsupported("reading", path) }

func (noHive) vacant(path string) (bool, error) { return false, unsupported("reading", path) }

func (noHive) del(path string) error { return unsupported("deleting", path) }

func unsupported(what, path string) error {
	return fmt.Errorf("%s the registry key %s on %s: %w", what, path, runtime.GOOS, errUnsupportedPlatform)
}
