//go:build windows

package ui

// openCommand returns the command that hands a URL to the user's default
// browser.
//
// rundll32 is used rather than `cmd /c start` because it takes the URL as one
// argument with no shell in the way: nothing re-reads `&`, `^` or `%` on the
// way through, and no console window flashes up.
func openCommand(rawURL string) (name string, args []string, err error) {
	return "rundll32.exe", []string{"url.dll,FileProtocolHandler", rawURL}, nil
}
