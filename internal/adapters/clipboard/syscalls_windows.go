//go:build windows

package clipboard

import (
	"fmt"
	"math"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// cfUnicodeText is CF_UNICODETEXT, the only standard format this package reads
// or writes. CF_TEXT and CF_OEMTEXT are synthesised by Windows from it, so a
// program that only offers Unicode text is still pasteable everywhere.
const cfUnicodeText = 13

// gmemMoveable is GMEM_MOVEABLE, the allocation flag SetClipboardData requires
// of the block it takes ownership of.
const gmemMoveable = 0x0002

// dwordBytes is the width of the serialised DWORD the two flag formats carry.
const dwordBytes = 4

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = user32.NewProc("OpenClipboard")
	procCloseClipboard             = user32.NewProc("CloseClipboard")
	procEmptyClipboard             = user32.NewProc("EmptyClipboard")
	procGetClipboardData           = user32.NewProc("GetClipboardData")
	procSetClipboardData           = user32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = user32.NewProc("IsClipboardFormatAvailable")
	procRegisterClipboardFormatW   = user32.NewProc("RegisterClipboardFormatW")
	procGetClipboardSequenceNumber = user32.NewProc("GetClipboardSequenceNumber")

	procGlobalAlloc   = kernel32.NewProc("GlobalAlloc")
	procGlobalFree    = kernel32.NewProc("GlobalFree")
	procGlobalLock    = kernel32.NewProc("GlobalLock")
	procGlobalUnlock  = kernel32.NewProc("GlobalUnlock")
	procGlobalSize    = kernel32.NewProc("GlobalSize")
	procRtlMoveMemory = kernel32.NewProc("RtlMoveMemory")
)

// call invokes an entry point that takes no address of ours, and reports what
// it returned together with the thread's last error, named after the entry
// point that set it.
//
// The error is only meaningful when the return value says the call failed,
// which is how every caller here reads it: LazyProc.Call hands back whatever
// GetLastError held, and after a successful call that is "the operation
// completed successfully". It is wrapped rather than passed through so that a
// failure names the Win32 function, which is the one thing an errno on its own
// does not say.
func call(p *windows.LazyProc, args ...uintptr) (uintptr, error) {
	r, _, err := p.Call(args...)
	return r, fmt.Errorf("%s: %w", p.Name, err)
}

// callPtr is [call] for an entry point that reads or writes through an address
// of ours, and it exists for its directive alone.
//
// //go:uintptrescapes tells the compiler that a uintptr argument may be a
// pointer converted at the call site, so the value it names is moved to the
// heap and kept alive until the call returns. That guarantee only reaches
// conversions written inside the annotated function's own argument list:
// route one through an ordinary wrapper such as [call] and escape analysis
// sees a plain integer, leaves the buffer on the goroutine's stack, and
// RtlMoveMemory then copies clipboard text through an address a stack growth
// is free to have abandoned. Neither go vet nor gosec reports it — see the
// same argument, and the three sites that had it wrong, in
// internal/ui/window/native_windows.go.
//
//go:uintptrescapes
func callPtr(p *windows.LazyProc, args ...uintptr) (uintptr, error) {
	r, _, err := p.Call(args...)
	return r, fmt.Errorf("%s: %w", p.Name, err)
}

const (
	// openAttempts and openRetryDelay bound how long this package waits for an
	// application that is holding the clipboard. Together they are about a
	// third of a second: long enough to outlast the moment Word or a browser
	// holds it while copying, short enough that the polling loop above is
	// never blocked for a human-noticeable time.
	openAttempts   = 12
	openRetryDelay = 25 * time.Millisecond
)

// openClipboard takes the clipboard, retrying while somebody else holds it.
//
// The window handle is deliberately zero. This program's own window belongs to
// internal/ui and is not always there — the clipboard has to work with the
// window hidden, and it has to work in a build with no window at all — and the
// clipboard needs no owner window to be read or written.
func openClipboard() error {
	var last error
	for attempt := range openAttempts {
		if attempt > 0 {
			time.Sleep(openRetryDelay)
		}
		ok, err := call(procOpenClipboard, 0)
		if ok != 0 {
			return nil
		}
		last = err
	}
	return fmt.Errorf("opening the clipboard after %d attempts: %w: %w", openAttempts, last, ErrBusy)
}

// closeClipboard releases the clipboard. A failure here is not reported to
// anybody: it can only mean this thread did not hold it, the caller's answer
// has already been collected, and there is nothing left to undo.
func closeClipboard() { _, _ = call(procCloseClipboard) }

// formatIDs are the three registered formats' ids for this session.
type formatIDs struct {
	exclude uint32
	history uint32
	cloud   uint32
}

// registerFormats registers the three names once per process. Windows returns
// the same id to every application that asks for the same name, and the ids
// are stable for the lifetime of the session, so asking again on every poll
// would be a syscall for a value that cannot have changed.
var registerFormats = sync.OnceValues(func() (formatIDs, error) {
	var ids formatIDs
	for _, pair := range []struct {
		name string
		into *uint32
	}{
		{formatExclude, &ids.exclude},
		{formatHistory, &ids.history},
		{formatCloud, &ids.cloud},
	} {
		id, err := registerFormat(pair.name)
		if err != nil {
			return formatIDs{}, err
		}
		*pair.into = id
	}
	return ids, nil
})

// registerFormat is RegisterClipboardFormatW for one name.
func registerFormat(name string) (uint32, error) {
	wide, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, fmt.Errorf("encoding the clipboard format name %s: %w", name, err)
	}
	// The conversion is written inside callPtr's argument list on purpose; see
	// the directive on callPtr for what that buys and what omitting it costs.
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on callPtr
	id, callErr := callPtr(procRegisterClipboardFormatW, uintptr(unsafe.Pointer(wide)))
	if id == 0 || id > math.MaxUint32 {
		return 0, fmt.Errorf("registering the clipboard format %s: %w", name, callErr)
	}
	// A clipboard format id is a UINT, and the bound above is the whole of this
	// conversion's safety.
	return uint32(id), nil //nolint:gosec // G115: bounded immediately above
}

// formatAvailable reports whether the clipboard currently offers a format. It
// needs no open clipboard, and it is how the presence of a marker is tested.
func formatAvailable(id uint32) bool {
	ok, _ := call(procIsClipboardFormatAvailable, uintptr(id))
	return ok != 0
}

// clipboardData returns the handle the clipboard holds for one format. The
// clipboard must already be open, and the handle belongs to the clipboard: it
// is read from and never freed here.
func clipboardData(id uint32) (uintptr, error) {
	h, err := call(procGetClipboardData, uintptr(id))
	if h == 0 {
		return 0, fmt.Errorf("reading clipboard format %d: %w", id, err)
	}
	return h, nil
}

// globalSize is the size in bytes of a global memory block. Windows may report
// more than was asked for when the block was allocated, so a caller that needs
// the length of a string in it must still look for the string's terminator.
func globalSize(h uintptr) (uintptr, error) {
	n, err := call(procGlobalSize, h)
	if n == 0 {
		return 0, fmt.Errorf("measuring a clipboard block: %w", err)
	}
	return n, nil
}

// globalLock pins a block and returns its address.
func globalLock(h uintptr) (uintptr, error) {
	p, err := call(procGlobalLock, h)
	if p == 0 {
		return 0, fmt.Errorf("locking a clipboard block: %w", err)
	}
	return p, nil
}

// globalUnlock releases a block. Its return value cannot distinguish success
// from failure without a separate GetLastError, and there is nothing either
// answer would change: the copy is already done.
func globalUnlock(h uintptr) { _, _ = call(procGlobalUnlock, h) }

// globalAlloc allocates a moveable block of n bytes, the kind
// SetClipboardData takes ownership of.
func globalAlloc(n uintptr) (uintptr, error) {
	h, err := call(procGlobalAlloc, gmemMoveable, n)
	if h == 0 {
		return 0, fmt.Errorf("allocating %d bytes for the clipboard: %w", n, err)
	}
	return h, nil
}

// globalFree releases a block this package allocated and did not manage to
// hand to the clipboard. A failure leaks one block in a process that is going
// to report the failure that got us here, and must not mask it.
func globalFree(h uintptr) { _, _ = call(procGlobalFree, h) }

// copyOut copies len(dst) UTF-16 code units from the locked block at src.
//
// RtlMoveMemory rather than a Go slice over src, because turning the address
// Windows returned back into a pointer is exactly the conversion go vet's
// unsafeptr check forbids — and it forbids it for a reason the clipboard makes
// concrete: the garbage collector knows nothing about that block, and a slice
// aliasing it would be a Go value pointing into memory Go does not own.
func copyOut(dst []uint16, src uintptr) {
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on callPtr
	_, _ = callPtr(procRtlMoveMemory, uintptr(unsafe.Pointer(&dst[0])), src,
		uintptr(len(dst))*2)
}

// copyOutBytes is [copyOut] for the four bytes of a serialised DWORD.
func copyOutBytes(dst []byte, src uintptr) {
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on callPtr
	_, _ = callPtr(procRtlMoveMemory, uintptr(unsafe.Pointer(&dst[0])), src, uintptr(len(dst)))
}

// copyInto copies src into the locked block at dst, which the caller has
// allocated large enough. See [copyOut] for why this goes through kernel32.
func copyInto(dst uintptr, src []uint16) {
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on callPtr
	_, _ = callPtr(procRtlMoveMemory, dst, uintptr(unsafe.Pointer(&src[0])),
		uintptr(len(src))*2)
}
