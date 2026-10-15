//go:build !windows

package tailscale

import "os/exec"

// hideConsole is a no-op off Windows, where a started process gets no window.
func hideConsole(*exec.Cmd) {}
