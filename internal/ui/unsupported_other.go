//go:build !windows

package ui

import "errors"

// errUnsupportedPlatform is what this package's platform stubs return where
// they cannot work. Nothing in this program runs there; the stubs exist so
// that `go vet` and a cross-build stay clean, which is also why the sentinel
// lives here rather than beside the Windows code: it is used by openCommand
// and toastCommand in this build and by nothing at all in the other one.
var errUnsupportedPlatform = errors.New("unsupported platform")
