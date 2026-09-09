package main

import (
	"fmt"
	"strings"
	"testing"
)

// An unquoted path with a space in it is the likeliest mistake in a Windows
// shortcut, and Go's flag package answers it by truncating the value and
// discarding every flag that follows. The program refuses to start; this
// asserts that what the user is told is enough to fix it, because the
// -H=windowsgui build has no console and this notification is all they get.
func TestTheStrayArgumentNoticeSaysEnoughToFixIt(t *testing.T) {
	t.Parallel()

	// Exactly what `-sync-folder C:\Users\pnj\Общая папка -listen …` leaves
	// behind: the tail of the path, and the flag that was silently dropped.
	stray := []string{`папка`, "-listen", "127.0.0.1:8899"}
	body := fmt.Sprintf(strayBody, strings.Join(stray, " "))

	for _, want := range stray {
		if !strings.Contains(body, want) {
			t.Errorf("the notice does not name the stray argument %q:\n%s", want, body)
		}
	}
	// Naming the argument is not enough on its own: the cause is the missing
	// quotes, and without that word the user has no way to know what to change.
	if !strings.Contains(body, "кавычк") {
		t.Errorf("the notice does not mention quoting, which is the actual fix:\n%s", body)
	}
	if !strings.Contains(strayTitle, "Link Monitor") {
		t.Errorf("the title does not say which program failed to start: %q", strayTitle)
	}
}
