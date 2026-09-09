package window

import (
	"errors"
	"strings"
)

// ErrRuntimeMissing reports that the Edge WebView2 runtime is not installed,
// so no window can be shown and the caller should fall back to a browser.
var ErrRuntimeMissing = errors.New("the Edge WebView2 runtime is not installed")

// RuntimeInstalled reports whether the Edge WebView2 runtime is present.
//
// It is a cheap registry read, so the caller can ask before deciding whether
// to open a window at all rather than finding out from a failed [Window.Run].
func RuntimeInstalled() bool {
	return runtimeInstalled(readRuntimeVersion)
}

// runtimeInstalled is [RuntimeInstalled] with the registry lifted out, so the
// three answers that matter — a version, no value, and a value that is not a
// version — can each be tested.
func runtimeInstalled(read func() (string, error)) bool {
	pv, err := read()
	if err != nil {
		// Every reason the read can fail — the key is absent, the hive is not
		// readable — means the same thing here: we cannot count on a runtime.
		return false
	}
	return validRuntimeVersion(pv)
}

// validRuntimeVersion reports whether pv is a version EdgeUpdate would write
// for an installed runtime.
//
// Two shapes have to be rejected rather than merely parsed. A value that is
// not a dotted number at all means something other than EdgeUpdate owns the
// key, and guessing from it would be worse than admitting we do not know. A
// leading zero — `0.0.0.0` — is what EdgeUpdate leaves behind after an
// uninstall: the key survives, the runtime does not.
func validRuntimeVersion(pv string) bool {
	fields := strings.Split(strings.TrimSpace(pv), ".")
	if len(fields) < 2 {
		return false
	}
	for _, f := range fields {
		if f == "" || strings.TrimLeft(f, "0123456789") != "" {
			return false
		}
	}
	return strings.TrimLeft(fields[0], "0") != ""
}
