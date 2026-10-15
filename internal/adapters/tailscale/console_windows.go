//go:build windows

package tailscale

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// hideConsole starts cmd without a console window.
//
// This program is built with -H=windowsgui, so it has no console of its own,
// and Windows gives every console program it starts a brand-new console
// window. PowerShell's own -WindowStyle Hidden only hides that window once
// PowerShell is running, which is after it has already been drawn: the user
// saw a black window flash up and vanish on every notification. Asking
// CreateProcess for no window at all is the only way not to show one.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
}
