//go:build windows

package ui

// toastCommand returns the command that runs one toast script.
//
// -NoProfile keeps a user's profile from slowing the toast down or failing it,
// -NonInteractive makes sure nothing can wait for input nobody will give, and
// -WindowStyle Hidden keeps a console from flashing over whatever the user is
// doing.
func toastCommand(script string) (name string, args []string, err error) {
	return "powershell.exe", []string{
		"-NoProfile",
		"-NonInteractive",
		"-WindowStyle", "Hidden",
		"-EncodedCommand", encodePowerShellCommand(script),
	}, nil
}
