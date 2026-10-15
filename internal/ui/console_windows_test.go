//go:build windows

package ui

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

func TestHideConsoleAsksForNoWindow(t *testing.T) {
	cmd := exec.Command("powershell.exe")
	hideConsole(cmd)
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Fatal("a console program started from the GUI build would flash a window")
	}
}
