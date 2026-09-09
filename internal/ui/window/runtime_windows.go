//go:build windows

package window

import (
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// runtimeClientKey is the EdgeUpdate client the WebView2 Evergreen runtime
// registers itself under. The GUID is Microsoft's, and documented: it is the
// only supported way to ask whether the runtime is there without loading it.
const runtimeClientKey = `Microsoft\EdgeUpdate\Clients\{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}`

// runtimeVersionValue is the value holding the installed version.
const runtimeVersionValue = "pv"

// runtimeLocations are the three places the runtime can be recorded, in the
// order Microsoft documents checking them: a machine-wide install seen from a
// 64-bit process, the same install on a 32-bit system, and a per-user install.
var runtimeLocations = []struct {
	root registry.Key
	path string
}{
	{root: registry.LOCAL_MACHINE, path: `SOFTWARE\WOW6432Node\` + runtimeClientKey},
	{root: registry.LOCAL_MACHINE, path: `SOFTWARE\` + runtimeClientKey},
	{root: registry.CURRENT_USER, path: `SOFTWARE\` + runtimeClientKey},
}

// readRuntimeVersion returns the version string of the installed runtime. The
// first location that answers wins; an error means none of them did.
func readRuntimeVersion() (string, error) {
	var firstErr error
	for _, loc := range runtimeLocations {
		pv, err := readRegistryString(loc.root, loc.path, runtimeVersionValue)
		if err == nil {
			return pv, nil
		}
		if firstErr == nil {
			firstErr = err
		}
	}
	return "", fmt.Errorf("reading the WebView2 runtime version: %w", firstErr)
}

// readRegistryString reads one string value, closing the key it opened.
func readRegistryString(root registry.Key, path, value string) (string, error) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer func() {
		// Nothing can be done about a failed close of a read-only key, and it
		// must not mask the value we came for.
		_ = key.Close()
	}()

	s, _, err := key.GetStringValue(value)
	if err != nil {
		return "", fmt.Errorf(`reading %s\%s: %w`, path, value, err)
	}
	return s, nil
}
