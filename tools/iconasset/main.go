// Command iconasset renders the same connected-device mark used by the tray
// as a static Windows executable icon. It is invoked by go generate.
package main

import (
	"fmt"
	"os"

	"github.com/pinnnthreetriples/link-monitor/internal/core"
	"github.com/pinnnthreetriples/link-monitor/internal/ui/trayicon"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: iconasset <output.ico>")
		os.Exit(2)
	}
	data, err := trayicon.Render(core.StateOK)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], data, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
