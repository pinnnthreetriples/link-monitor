//go:build windows

package ui

import (
	"slices"
	"strings"
	"testing"
)

func TestExternalOpenerAcceptsOrdinaryWebAddresses(t *testing.T) {
	// These are the addresses the page actually routes outwards: a Tailscale
	// login URL, a machine on the tailnet serving a page, an ordinary site.
	for _, raw := range []string{
		loginURL,
		"http://win-sttm11d02rd.tailnet.ts.net:8080/",
		"https://example.com",
		"http://100.127.188.87:9/status?x=1#top",
	} {
		o, r, _ := recordingOpener(nil)

		if err := o.Open(t.Context(), raw); err != nil {
			t.Errorf("Open(%q) = %v, want nil", raw, err)
			continue
		}
		if r.runs != 1 {
			t.Errorf("Open(%q) ran %d commands, want 1", raw, r.runs)
		}
		if !strings.EqualFold(r.name, "rundll32.exe") {
			t.Errorf("Open(%q) ran %q, want rundll32.exe", raw, r.name)
		}
		// One argument, no shell in the way: an ampersand in a query must not
		// become a second command.
		if !slices.Contains(r.args, raw) {
			t.Errorf("Open(%q) did not hand the URL over whole: %q", raw, r.args)
		}
	}
}
