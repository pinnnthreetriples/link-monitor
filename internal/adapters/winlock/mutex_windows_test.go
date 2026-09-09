//go:build windows

package winlock

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// nextLockName numbers the locks these tests take.
//
// A clock is not enough on its own: Windows hands out the same nanosecond to
// two goroutines readily, and two parallel tests sharing a lock name would
// each see the other's lock and blame this package for it.
var nextLockName atomic.Uint64

// uniqueName builds a lock name no other process or test can collide with.
// These tests take a real session-local mutex — a local kernel object, no
// network and no tailnet — so the only thing to guard against is one test
// seeing another test's lock.
func uniqueName(t *testing.T) string {
	t.Helper()

	return fmt.Sprintf("linkmon-test-%d-%d-%d",
		os.Getpid(), time.Now().UnixNano(), nextLockName.Add(1))
}

func TestAcquireTakesTheLockAndReleaseGivesItBack(t *testing.T) {
	t.Parallel()

	name := uniqueName(t)

	release, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire(%q) = %v, want the lock", name, err)
	}

	if _, err := Acquire(name); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire(%q) = %v, want ErrAlreadyRunning", name, err)
	}

	if err := release(); err != nil {
		t.Fatalf("release() = %v, want no error", err)
	}

	again, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire(%q) after release = %v, want the lock", name, err)
	}
	t.Cleanup(func() {
		if err := again(); err != nil {
			t.Errorf("releasing the re-taken lock: %v", err)
		}
	})
}

func TestReleaseIsIdempotentAndSafeFromSeveralGoroutines(t *testing.T) {
	t.Parallel()

	name := uniqueName(t)
	release, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire(%q) = %v, want the lock", name, err)
	}

	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = release()
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("release() from goroutine %d = %v, want no error", i, err)
		}
	}
}

func TestADistinctNameIsADistinctLock(t *testing.T) {
	t.Parallel()

	first, second := uniqueName(t), uniqueName(t)+"-other"

	releaseFirst, err := Acquire(first)
	if err != nil {
		t.Fatalf("Acquire(%q) = %v, want the lock", first, err)
	}
	t.Cleanup(func() {
		if err := releaseFirst(); err != nil {
			t.Errorf("releasing %q: %v", first, err)
		}
	})

	releaseSecond, err := Acquire(second)
	if err != nil {
		t.Fatalf("Acquire(%q) = %v: a different name must be a different lock", second, err)
	}
	t.Cleanup(func() {
		if err := releaseSecond(); err != nil {
			t.Errorf("releasing %q: %v", second, err)
		}
	})
}

func TestTheLosingHandleIsNotLeaked(t *testing.T) {
	t.Parallel()

	// A refused Acquire still receives a valid handle from CreateMutexW. If it
	// leaked, the object would outlive the holder's release and the next
	// Acquire would be refused for a lock nobody holds.
	name := uniqueName(t)
	release, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire(%q) = %v, want the lock", name, err)
	}

	for range 3 {
		if _, err := Acquire(name); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Acquire(%q) = %v, want ErrAlreadyRunning", name, err)
		}
	}
	if err := release(); err != nil {
		t.Fatalf("release() = %v, want no error", err)
	}

	final, err := Acquire(name)
	if err != nil {
		t.Fatalf("Acquire(%q) after the refusals and the release = %v, want the lock", name, err)
	}
	t.Cleanup(func() {
		if err := final(); err != nil {
			t.Errorf("releasing the final lock: %v", err)
		}
	})
}
