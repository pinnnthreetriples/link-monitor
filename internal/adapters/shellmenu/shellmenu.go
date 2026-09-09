// Package shellmenu installs and removes this program's entry in Explorer's
// right-click menu: «Отправить на ПК», which hands the file that was clicked to
// cmd/linkmon's send mode.
//
// It writes under HKCU\Software\Classes and nowhere else, so installing needs
// no elevation and affects nobody but the user who asked for it. Three keys are
// involved at most — the class, its `shell` container and the verb with its
// `command` child — and removal offers every one of them back, deleting a
// container only while it is empty so that another program's verb is never
// taken down with ours. A user who turns the item on must be able to turn it
// off and find the registry as they left it.
//
// The whole of the decision — which keys, which values, what the command line
// says, which executable paths are refusable — is in plan.go with no registry
// in sight, so it can be tested. The registry itself is behind the unexported
// [hive] seam, with an in-memory fake for the tests and one build-tagged
// implementation per platform.
package shellmenu

import (
	"fmt"
	"os"
)

// hive is the registry as this package needs it. Every method takes a path
// relative to HKCU.
type hive interface {
	// set creates path, and the keys above it, and writes values into it.
	set(path string, values map[string]string) error
	// exists reports whether path is there.
	exists(path string) (bool, error)
	// vacant reports whether there is nothing at path worth keeping: either it
	// does not exist, or it holds no subkey and no value.
	vacant(path string) (bool, error)
	// del deletes path, which must hold no subkeys. Deleting a key that is not
	// there is not an error: removal has to be safe to run twice.
	del(path string) error
}

// Menu is the Explorer verb for one executable. Build it with [New] or, for the
// program that is running, with [Current].
//
// None of its methods takes a context. A registry write under HKCU is a handful
// of microseconds against a local hive with no network and no lock to wait on;
// a context here would be a cancellation nothing could honour, offered to a
// caller who would then believe it.
type Menu struct {
	exe  string
	root string
	hive hive
}

// New returns the menu item that would run exePath. The path must be absolute
// and must end in .exe; the error for a path that cannot work arrives from
// [Menu.Install] rather than here, so that a tray built on a machine where
// os.Executable() is odd still comes up.
func New(exePath string) *Menu {
	return &Menu{exe: exePath, root: classesRoot, hive: realHive()}
}

// Current returns the menu item for the executable that is running now.
func Current() (*Menu, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("finding this program's own path: %w", err)
	}
	return New(exe), nil
}

// ExePath reports the executable the item would run. It is what a log line or
// a report needs; nothing decides anything by it.
func (m *Menu) ExePath() string { return m.exe }

// Label is «Отправить на ПК», the text Explorer shows. It is exported so the
// tray can name in its own menu the thing the user is switching on, without
// two copies of the string drifting apart.
func (m *Menu) Label() string { return verbLabel }

// Install puts the item in Explorer's menu for this user. Installing over an
// existing item is how a moved or upgraded exe repairs its own command, so it
// is not an error.
func (m *Menu) Install() error { return install(m.hive, m.root, m.exe) }

// Remove takes the item out and leaves no key of ours behind.
func (m *Menu) Remove() error { return remove(m.hive, m.root) }

// Installed reports whether the item is in Explorer's menu.
func (m *Menu) Installed() (bool, error) { return installed(m.hive, m.root) }

// install writes the plan, shallowest key first, so that a failure part way
// through leaves a verb without a command rather than a command without a
// verb — the first is invisible in the menu, the second would be an item that
// does nothing.
func install(h hive, root, exePath string) error {
	entries, err := plan(root, exePath)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := h.set(e.path, e.values); err != nil {
			return fmt.Errorf("installing the Explorer menu item: %w", err)
		}
	}
	return nil
}

// remove deletes the verb and then offers back the containers the install had
// to create.
//
// Deepest key first, because the registry refuses to delete a key that still
// has subkeys. `command` is the only child this package ever creates, so two
// deletions are the whole of it.
func remove(h hive, root string) error {
	verb := verbPath(root)
	for _, path := range []string{verb + `\` + commandKey, verb} {
		if err := h.del(path); err != nil {
			return fmt.Errorf("removing the Explorer menu item: %w", err)
		}
	}
	return prune(h, root, verb)
}

// prune deletes the keys above the verb while they are empty, stopping at the
// first one that still holds something.
//
// This is what "no orphan keys" means in practice. Software\Classes\*\shell may
// well have existed before this program, and it may hold verbs belonging to
// other programs; deleting it because we are leaving would be vandalism. An
// empty one, on the other hand, is ours by elimination — it holds nothing, so
// nothing is lost — and leaving it behind is the litter the user asked us not
// to leave. The first non-empty ancestor ends the walk: everything above it
// holds it, so nothing above it is empty either.
func prune(h hive, root, path string) error {
	for _, ancestor := range ancestors(root, path) {
		vacant, err := h.vacant(ancestor)
		if err != nil {
			return fmt.Errorf("removing the Explorer menu item: %w", err)
		}
		if !vacant {
			return nil
		}
		if err := h.del(ancestor); err != nil {
			return fmt.Errorf("removing the Explorer menu item: %w", err)
		}
	}
	return nil
}

// installed reports whether the verb key is there. The command is not checked
// against the running exe on purpose: an item pointing at a copy of the program
// that has since moved is still installed, and the repair for it is to install
// again, which is exactly what a user who sees the item ticked and clicks it
// twice will do.
func installed(h hive, root string) (bool, error) {
	ok, err := h.exists(verbPath(root))
	if err != nil {
		return false, fmt.Errorf("looking for the Explorer menu item: %w", err)
	}
	return ok, nil
}
