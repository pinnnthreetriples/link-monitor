//go:build windows

package localendpoint

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOsProcessDescribesThisVeryProcess(t *testing.T) {
	t.Parallel()

	exe, startedAt, err := osProcess{}.info(os.Getpid())
	if err != nil {
		t.Fatalf("info(%d) = %v, want this process", os.Getpid(), err)
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() = %v", err)
	}
	if !sameFile(self, exe) {
		t.Errorf("info() reported %q, want %q", exe, self)
	}
	if startedAt.IsZero() || startedAt.After(time.Now()) {
		t.Errorf("info() reported a start time of %s, which is not in the past", startedAt)
	}
	// A test binary that Windows says started more than a day ago would mean
	// the FILETIME conversion is wrong rather than that the machine is slow.
	if time.Since(startedAt) > 24*time.Hour {
		t.Errorf("info() reported a start time of %s, %s ago", startedAt, time.Since(startedAt))
	}
}

func TestOsProcessRefusesAProcessIdThatCannotBeOne(t *testing.T) {
	t.Parallel()

	for _, pid := range []int{0, -1, -os.Getpid()} {
		if _, _, err := (osProcess{}).info(pid); !errors.Is(err, errNoSuchProcess) {
			t.Errorf("info(%d) = %v, want errNoSuchProcess", pid, err)
		}
	}
}

// TestOsProcessRefusesAProcessThatHasExited covers the case a stale record
// actually presents: the process is gone. It is arranged with a real child so
// that the exit-time guard in creationTime is exercised rather than described —
// this test holds the child's handle open (it never calls Wait until cleanup),
// which is exactly the situation where Windows still answers OpenProcess and
// still reports the old creation time and image path.
func TestOsProcessRefusesAProcessThatHasExited(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("cmd.exe", "/c", "exit", "0") //nolint:gosec // G204: a literal
	if err := cmd.Start(); err != nil {
		t.Skipf("cmd.exe could not be started on this machine: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() {
		if err := cmd.Wait(); err != nil {
			t.Logf("reaping the child: %v", err)
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	for {
		_, _, err := osProcess{}.info(pid)
		if errors.Is(err, errNoSuchProcess) {
			return // the child has exited and info says so, which is the point
		}
		if err != nil {
			t.Fatalf("info(%d) = %v, want either this process or errNoSuchProcess", pid, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("info(%d) still reports a live process after the child exited", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestTheWholeMechanismAgainstThisMachine is the end-to-end one: the real
// filesystem, the real Windows process lookup, and this test binary standing in
// for the instance. It is what proves publish and find agree about what the
// operating system says, which no fake can.
func TestTheWholeMechanismAgainstThisMachine(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), dirName, fileName)
	d := realDeps()

	remove, err := publish(d, path, os.Getpid(), "http://127.0.0.1:52341/")
	if err != nil {
		t.Fatalf("publish() = %v, want a record", err)
	}

	rec, err := find(d, path)
	if err != nil {
		t.Fatalf("find() = %v, want the record this process just published", err)
	}
	if rec.PID != os.Getpid() {
		t.Errorf("find() = pid %d, want %d", rec.PID, os.Getpid())
	}
	if !strings.EqualFold(filepath.Base(rec.Exe), filepath.Base(os.Args[0])) {
		t.Errorf("find() = exe %q, want this test binary (%q)", rec.Exe, os.Args[0])
	}

	// A record whose pid belongs to a process that is not this one must be
	// refused. Pid 4 is the System process: it exists on every Windows machine,
	// it is certainly not this test binary, and confirming against it is how a
	// reused pid would look.
	planted := rec
	planted.PID = 4
	if err := d.files.writeFile(path, mustJSON(t, planted)); err != nil {
		t.Fatalf("planting a record: %v", err)
	}
	if _, err := find(d, path); !errors.Is(err, ErrStale) {
		t.Fatalf("find() on a record naming another process = %v, want ErrStale", err)
	}

	if err := remove(); err != nil {
		t.Fatalf("remove() = %v, want no error", err)
	}
	if _, err := find(d, path); !errors.Is(err, ErrNoRecord) {
		t.Fatalf("find() after remove = %v, want ErrNoRecord", err)
	}
}

// TestTheExportedPairAgreeAboutWhereTheRecordLives drives Publish and Find
// themselves rather than their injected forms, which is the only way to catch
// the two disagreeing about the location. %LOCALAPPDATA% is pointed at a
// temporary directory for the duration, so the real one is left alone — and
// that is also why this test does not run in parallel.
func TestTheExportedPairAgreeAboutWhereTheRecordLives(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())

	remove, err := Publish("http://127.0.0.1:52341/")
	if err != nil {
		t.Fatalf("Publish() = %v, want a record", err)
	}
	t.Cleanup(func() {
		if err := remove(); err != nil {
			t.Errorf("remove() = %v", err)
		}
	})

	rec, err := Find()
	if err != nil {
		t.Fatalf("Find() = %v, want the record Publish just wrote", err)
	}
	if rec.PID != os.Getpid() || rec.BaseURL != "http://127.0.0.1:52341/" {
		t.Errorf("Find() = %+v, want this process and the address published", rec)
	}
}

// TestTheExportedPairRefuseAMachineWithNoLocalAppData covers the one
// environment failure there is: without %LOCALAPPDATA% there is nowhere to
// publish, and that is a fault rather than "nothing is running".
func TestTheExportedPairRefuseAMachineWithNoLocalAppData(t *testing.T) {
	t.Setenv("LOCALAPPDATA", "")

	if _, err := Publish("http://127.0.0.1:52341/"); !errors.Is(err, errNoLocalAppData) {
		t.Errorf("Publish() = %v, want errNoLocalAppData", err)
	}
	_, err := Find()
	if !errors.Is(err, errNoLocalAppData) {
		t.Errorf("Find() = %v, want errNoLocalAppData", err)
	}
	if NotRunning(err) {
		t.Errorf("NotRunning(%v) = true; a broken environment is not a missing instance", err)
	}
}

// TestPathLivesUnderLocalAppData checks the one thing about the location that
// is a promise rather than a detail: the record is under %LOCALAPPDATA%, which
// is the directory whose ACL keeps it to this user.
func TestPathLivesUnderLocalAppData(t *testing.T) {
	t.Parallel()

	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		t.Skip("this machine has no %LOCALAPPDATA%")
	}

	path, err := Path()
	if err != nil {
		t.Fatalf("Path() = %v", err)
	}
	want := filepath.Join(base, dirName, fileName)
	if path != want {
		t.Errorf("Path() = %q, want %q", path, want)
	}
}
