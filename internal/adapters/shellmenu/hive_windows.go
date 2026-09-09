//go:build windows

package shellmenu

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"
)

// realHive returns the registry itself.
func realHive() hive { return winHive{} }

// winHive is HKCU, as this package writes to it.
//
// It holds no state: every call opens the key it needs and closes it again.
// That is the cheaper thing to get right, and it matters more than the handful
// of microseconds a cached handle would save on an operation the user performs
// twice in the life of an install.
//
// `*` in a key path is a literal name here and not a wildcard — the registry
// API has no wildcards — which is why the class every file belongs to can be
// addressed like any other key.
type winHive struct{}

// set creates path — and every key above it that is missing — and writes the
// values into it.
//
// The return is named so that a failure to close the key can still be reported
// after the values have gone in: the defer runs after the return value is
// fixed, and a local variable would have been thrown away.
func (winHive) set(path string, values map[string]string) (err error) {
	// The middle result says whether the key was already there. It is ignored
	// deliberately: installing over an existing item is how a moved exe repairs
	// its own command, so "it existed" is not news.
	key, _, openErr := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if openErr != nil {
		return fmt.Errorf(`creating HKCU\%s: %w`, path, openErr)
	}
	defer closeKey(&err, path, key)

	for name, value := range values {
		if setErr := key.SetStringValue(name, value); setErr != nil {
			return fmt.Errorf(`writing HKCU\%s\%s: %w`, path, valueName(name), setErr)
		}
	}
	return err
}

// exists reports whether path is there.
func (winHive) exists(path string) (bool, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf(`opening HKCU\%s: %w`, path, err)
	}
	// The key is there whatever the close does; a failure to close leaks one
	// handle, which is worth saying and not worth changing the answer for.
	if closeErr := key.Close(); closeErr != nil {
		return true, fmt.Errorf(`closing HKCU\%s: %w`, path, closeErr)
	}
	return true, nil
}

// vacant reports whether there is nothing at path worth keeping. A key that is
// not there counts as vacant: removal walks up through keys it may never have
// created, and "it is already gone" is the same outcome as "it is empty".
func (winHive) vacant(path string) (empty bool, err error) {
	key, openErr := registry.OpenKey(registry.CURRENT_USER, path,
		registry.QUERY_VALUE|registry.ENUMERATE_SUB_KEYS)
	if errors.Is(openErr, registry.ErrNotExist) {
		return true, nil
	}
	if openErr != nil {
		return false, fmt.Errorf(`opening HKCU\%s: %w`, path, openErr)
	}
	defer closeKey(&err, path, key)

	info, statErr := key.Stat()
	if statErr != nil {
		return false, fmt.Errorf(`counting what is under HKCU\%s: %w`, path, statErr)
	}
	return info.SubKeyCount == 0 && info.ValueCount == 0, err
}

// del deletes path. A key that is not there is already deleted, which is what
// makes running a removal twice — or after a half-finished install — safe.
func (winHive) del(path string) error {
	err := registry.DeleteKey(registry.CURRENT_USER, path)
	if err == nil || errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return fmt.Errorf(`deleting HKCU\%s: %w`, path, err)
}

// closeKey closes a key and reports a failure through the caller's own named
// error, but only when the caller had nothing worse to say. A leaked registry
// handle is worth a message; it is not worth replacing the reason the operation
// actually failed.
func closeKey(err *error, path string, key registry.Key) {
	if closeErr := key.Close(); closeErr != nil && *err == nil {
		*err = fmt.Errorf(`closing HKCU\%s: %w`, path, closeErr)
	}
}

// valueName names a registry value for an error message. The unnamed value is
// addressed by the empty string, which would read as a missing word.
func valueName(name string) string {
	if name == "" {
		return "(default)"
	}
	return name
}
