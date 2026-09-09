package shellmenu

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// fakeHive is the registry in a map: a key exists when it has an entry, and its
// entry is its values. Subkeys are not stored separately — a key's children are
// the entries whose paths begin with it — which is how a real hive behaves when
// CreateKey makes the whole path at once.
type fakeHive struct {
	keys    map[string]map[string]string
	setErr  error
	delErr  error
	readErr error
}

func newFakeHive() *fakeHive {
	return &fakeHive{keys: map[string]map[string]string{}}
}

func (h *fakeHive) set(path string, values map[string]string) error {
	if h.setErr != nil {
		return h.setErr
	}
	if h.keys[path] == nil {
		h.keys[path] = map[string]string{}
	}
	maps.Copy(h.keys[path], values)
	return nil
}

func (h *fakeHive) exists(path string) (bool, error) {
	if h.readErr != nil {
		return false, h.readErr
	}
	if _, ok := h.keys[path]; ok {
		return true, nil
	}
	return h.hasChildren(path), nil
}

func (h *fakeHive) vacant(path string) (bool, error) {
	if h.readErr != nil {
		return false, h.readErr
	}
	return len(h.keys[path]) == 0 && !h.hasChildren(path), nil
}

func (h *fakeHive) del(path string) error {
	if h.delErr != nil {
		return h.delErr
	}
	if h.hasChildren(path) {
		return errors.New("the key still has subkeys")
	}
	delete(h.keys, path)
	return nil
}

func (h *fakeHive) hasChildren(path string) bool {
	prefix := path + `\`
	for key := range h.keys {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// paths returns every key the fake holds, sorted, for an assertion that reads.
func (h *fakeHive) paths() []string {
	out := slices.Collect(maps.Keys(h.keys))
	sort.Strings(out)
	return out
}

func TestInstallWritesThePlanAndNothingMore(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	if err := install(h, testRoot, theExe); err != nil {
		t.Fatalf("install() = %v, want the item installed", err)
	}

	entries, err := plan(testRoot, theExe)
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}
	want := map[string]map[string]string{}
	for _, e := range entries {
		want[e.path] = e.values
	}
	if !reflect.DeepEqual(h.keys, want) {
		t.Errorf("install() wrote\n%+v\nwant\n%+v", h.keys, want)
	}

	ok, err := installed(h, testRoot)
	if err != nil || !ok {
		t.Errorf("installed() = (%t, %v), want (true, nil)", ok, err)
	}
}

func TestInstallRemoveInstallLeavesTheSameRegistryEachTime(t *testing.T) {
	t.Parallel()

	h := newFakeHive()

	if err := install(h, testRoot, theExe); err != nil {
		t.Fatalf("the first install() = %v", err)
	}
	first := h.paths()

	if err := remove(h, testRoot); err != nil {
		t.Fatalf("remove() = %v", err)
	}
	if len(h.keys) != 0 {
		t.Fatalf("remove() left %v behind; the user must be able to take it back off", h.paths())
	}
	ok, err := installed(h, testRoot)
	if err != nil || ok {
		t.Errorf("installed() after remove = (%t, %v), want (false, nil)", ok, err)
	}

	if err := install(h, testRoot, theExe); err != nil {
		t.Fatalf("the second install() = %v", err)
	}
	if second := h.paths(); !reflect.DeepEqual(second, first) {
		t.Errorf("installing again gave %v, want the same keys as the first time (%v)", second, first)
	}
}

// TestRemoveKeepsAContainerAnotherProgramIsUsing is the difference between
// tidying up and vandalism: Software\Classes\*\shell is shared, and another
// program's verb in it must survive our removal.
func TestRemoveKeepsAContainerAnotherProgramIsUsing(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	const neighbour = `Software\Classes\*\shell\SomeoneElse.DoAThing`
	if err := h.set(neighbour, map[string]string{"MUIVerb": "Что-то ещё"}); err != nil {
		t.Fatalf("planting a neighbour: %v", err)
	}
	if err := install(h, testRoot, theExe); err != nil {
		t.Fatalf("install() = %v", err)
	}

	if err := remove(h, testRoot); err != nil {
		t.Fatalf("remove() = %v", err)
	}
	if want := []string{neighbour}; !reflect.DeepEqual(h.paths(), want) {
		t.Errorf("remove() left %v, want exactly %v", h.paths(), want)
	}
}

// TestRemoveOnAMachineWithNothingInstalledIsQuiet covers the tray item being
// unticked twice, and an uninstaller running on a machine that never had the
// item.
func TestRemoveOnAMachineWithNothingInstalledIsQuiet(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	for range 2 {
		if err := remove(h, testRoot); err != nil {
			t.Fatalf("remove() with nothing installed = %v, want no error", err)
		}
	}
	if len(h.keys) != 0 {
		t.Errorf("remove() created %v out of nothing", h.paths())
	}
}

// TestRemoveTidiesTheContainersItsInstallCreated checks the pruning walk
// directly: with the verb gone, the empty class and its shell container are
// offered back rather than left as litter.
func TestRemoveTidiesTheContainersItsInstallCreated(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	if err := install(h, testRoot, theExe); err != nil {
		t.Fatalf("install() = %v", err)
	}
	// The real registry creates the whole path, so the containers exist as
	// keys of their own even though install never wrote a value into them.
	for _, container := range []string{`Software\Classes\*`, `Software\Classes\*\shell`} {
		if err := h.set(container, nil); err != nil {
			t.Fatalf("planting %s: %v", container, err)
		}
	}

	if err := remove(h, testRoot); err != nil {
		t.Fatalf("remove() = %v", err)
	}
	if len(h.keys) != 0 {
		t.Errorf("remove() left %v behind", h.paths())
	}
}

func TestInstallRefusesABadExeBeforeTouchingTheRegistry(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	if err := install(h, testRoot, "linkmon.exe"); !errors.Is(err, errBadExePath) {
		t.Fatalf("install() with a relative exe = %v, want an errBadExePath", err)
	}
	if len(h.keys) != 0 {
		t.Errorf("install() wrote %v after refusing the path", h.paths())
	}
}

func TestTheRegistryFailuresReachTheCaller(t *testing.T) {
	t.Parallel()

	boom := errors.New("the hive is read-only")

	t.Run("a failed write", func(t *testing.T) {
		t.Parallel()

		h := newFakeHive()
		h.setErr = boom
		if err := install(h, testRoot, theExe); !errors.Is(err, boom) {
			t.Errorf("install() = %v, want the write failure", err)
		}
	})

	t.Run("a failed delete", func(t *testing.T) {
		t.Parallel()

		h := newFakeHive()
		if err := install(h, testRoot, theExe); err != nil {
			t.Fatalf("install() = %v", err)
		}
		h.delErr = boom
		if err := remove(h, testRoot); !errors.Is(err, boom) {
			t.Errorf("remove() = %v, want the delete failure", err)
		}
	})

	t.Run("a failed read while pruning", func(t *testing.T) {
		t.Parallel()

		h := newFakeHive()
		if err := install(h, testRoot, theExe); err != nil {
			t.Fatalf("install() = %v", err)
		}
		h.readErr = boom
		if err := remove(h, testRoot); !errors.Is(err, boom) {
			t.Errorf("remove() = %v, want the read failure", err)
		}
	})

	t.Run("a failed read while asking", func(t *testing.T) {
		t.Parallel()

		h := newFakeHive()
		h.readErr = boom
		if _, err := installed(h, testRoot); !errors.Is(err, boom) {
			t.Errorf("installed() = %v, want the read failure", err)
		}
	})
}

// TestTheMenuMethodsDriveTheSameLogic exercises the type the tray actually
// holds, against the fake hive and a scratch root, so the three one-line
// methods are not the untested part of the package.
func TestTheMenuMethodsDriveTheSameLogic(t *testing.T) {
	t.Parallel()

	h := newFakeHive()
	m := &Menu{exe: theExe, root: testRoot, hive: h}

	if ok, err := m.Installed(); err != nil || ok {
		t.Fatalf("Installed() before installing = (%t, %v), want (false, nil)", ok, err)
	}
	if err := m.Install(); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if ok, err := m.Installed(); err != nil || !ok {
		t.Fatalf("Installed() after installing = (%t, %v), want (true, nil)", ok, err)
	}
	if err := m.Remove(); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if ok, err := m.Installed(); err != nil || ok {
		t.Fatalf("Installed() after removing = (%t, %v), want (false, nil)", ok, err)
	}
	if len(h.keys) != 0 {
		t.Errorf("Remove() left %v behind", h.paths())
	}
}

func TestNewCarriesThePathItWasGiven(t *testing.T) {
	t.Parallel()

	m := New(theExe)
	if m.ExePath() != theExe {
		t.Errorf("ExePath() = %q, want %q", m.ExePath(), theExe)
	}
	if m.root != classesRoot {
		t.Errorf("New() writes under %q, want %q", m.root, classesRoot)
	}
	if m.hive == nil {
		t.Error("New() built a menu with no registry behind it")
	}
}
