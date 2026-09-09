//go:build windows

package window

import (
	"fmt"
	"unsafe"
)

var (
	procSendMessage              = user32.NewProc("SendMessageW")
	procGetSystemMetrics         = user32.NewProc("GetSystemMetrics")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
	procDestroyIcon              = user32.NewProc("DestroyIcon")
)

const (
	// wmSetIcon gives a window its title-bar and taskbar icons.
	wmSetIcon = 0x0080
	// iconSmall is the title-bar icon; iconBig is the one Alt-Tab and the
	// taskbar use.
	iconSmall = 0
	iconBig   = 1

	// The GetSystemMetrics indices for the two icon sizes this display wants.
	smCxIcon   = 11
	smCyIcon   = 12
	smCxSmIcon = 49
	smCySmIcon = 50

	// iconVersion is the icon-image format CreateIconFromResourceEx is being
	// handed: 0x00030000 is the Win32 form, which is what an .ico holds.
	iconVersion = 0x00030000
	// lrDefaultColor takes the display's own colour format.
	lrDefaultColor = 0x0000

	// fallbackSmall and fallbackBig stand in when GetSystemMetrics answers
	// zero, which it does only if the call fails outright.
	fallbackSmall = 16
	fallbackBig   = 32
)

// iconRequest is one icon a window needs: which slot it fills and how the
// display is asked how big it should be.
type iconRequest struct {
	which    uintptr
	cx, cy   uintptr
	fallback int
}

// iconRequests are the two icons every window gets.
var iconRequests = []iconRequest{
	{which: iconSmall, cx: smCxSmIcon, cy: smCySmIcon, fallback: fallbackSmall},
	{which: iconBig, cx: smCxIcon, cy: smCyIcon, fallback: fallbackBig},
}

// applyIcon gives the window its icons, built from the .ico bytes the caller
// passed.
//
// Doing it at runtime is what keeps the icon out of the build: the alternative
// is a resource section in the exe, which means a .syso and a resource
// compiler step, for a picture the program already holds in memory.
func (b *nativeBackend) applyIcon(ico []byte) {
	if len(ico) == 0 {
		return
	}

	for _, want := range iconRequests {
		cx := systemMetric(want.cx, want.fallback)
		cy := systemMetric(want.cy, want.fallback)

		icon, err := createIcon(ico, cx, cy)
		if err != nil {
			// An icon is the one part of a window whose absence costs nothing
			// but looks: the window still opens, with the system default.
			b.log.Warn("setting the window icon", "width", cx, "height", cy, "error", err)
			continue
		}
		b.icons = append(b.icons, icon)
		call(procSendMessage, b.hwnd, wmSetIcon, want.which, icon)
	}
}

// releaseIcons destroys the icons this window was given. WM_SETICON transfers
// no ownership, so nothing else will.
func (b *nativeBackend) releaseIcons() {
	for _, icon := range b.icons {
		if call(procDestroyIcon, icon) == 0 {
			b.log.Warn("destroying a window icon", "icon", icon)
		}
	}
	b.icons = nil
}

// systemMetric asks the display for one metric, falling back when it will not
// say.
func systemMetric(index uintptr, fallback int) int {
	// GetSystemMetrics returns a C int, and the calling convention delivers it
	// in the low 32 bits of the register this uintptr came from — the upper
	// bits are not part of the answer. Truncating to int32 is how that return
	// value is read, not a narrowing of it: without the int32 step a metric
	// Windows returned as a negative would come back as a huge positive and
	// pass the `n > 0` test below.
	if n := int(int32(call(procGetSystemMetrics, index))); n > 0 { //nolint:gosec // G115: the ABI's own int
		return n
	}
	return fallback
}

// createIcon builds an HICON from .ico bytes at the size Windows asked for.
func createIcon(ico []byte, cx, cy int) (uintptr, error) {
	image, err := icoImage(ico, cx)
	if err != nil {
		return 0, fmt.Errorf("choosing a %dx%d image: %w", cx, cy, err)
	}

	// CreateIconFromResourceEx copies the bits it is given, so the slice has
	// to outlive nothing but this call. icoImage never returns an empty one.
	//
	// The conversion is written directly in LazyProc.Call's argument list, and
	// that placement is the whole of what makes it safe: Call carries
	// //go:uintptrescapes, so the compiler moves the backing array to the heap
	// and keeps it alive until the call returns. Going through the package's
	// own [call] wrapper instead would silently drop that guarantee — see
	// [callPtr], which exists because three other sites had done exactly that.
	//nolint:gosec // G103: pinned for the call by //go:uintptrescapes on Call
	handle, _, callErr := procCreateIconFromResourceEx.Call(
		uintptr(unsafe.Pointer(&image[0])),
		uintptr(len(image)),
		1, // TRUE: an icon rather than a cursor
		iconVersion,
		uintptr(cx),
		uintptr(cy),
		lrDefaultColor,
	)
	if handle == 0 {
		return 0, fmt.Errorf("creating a %dx%d icon from %d bytes: %w", cx, cy, len(image), callErr)
	}
	return handle, nil
}
