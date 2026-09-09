//go:build windows

package ui

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
)

// decodePowerShellArgs finds the -EncodedCommand payload and reads it back.
func decodePowerShellArgs(t *testing.T, args []string) string {
	t.Helper()

	i := slices.Index(args, "-EncodedCommand")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("args %q carry no -EncodedCommand", args)
	}
	raw, err := base64.StdEncoding.DecodeString(args[i+1])
	if err != nil {
		t.Fatalf("the encoded command is not base64: %v", err)
	}
	units := make([]uint16, 0, len(raw)/2)
	for j := 0; j+1 < len(raw); j += 2 {
		units = append(units, uint16(raw[j])|uint16(raw[j+1])<<8)
	}
	return string(utf16.Decode(units))
}

func TestToastNotifierRunsPowerShellWithTheRussianTextIntact(t *testing.T) {
	r := &fakeRunner{}
	n := &ToastNotifier{runner: r, appID: defaultToastAppID}

	note := Notification{Title: "Связь потеряна", Body: "Tailscale не запущен"}
	if err := n.Notify(t.Context(), note); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if r.runs != 1 {
		t.Fatalf("ran %d commands, want 1", r.runs)
	}
	if !strings.EqualFold(r.name, "powershell.exe") {
		t.Errorf("ran %q, want powershell.exe", r.name)
	}
	for _, want := range []string{"-NoProfile", "-NonInteractive", "-EncodedCommand"} {
		if !slices.Contains(r.args, want) {
			t.Errorf("args %q are missing %s", r.args, want)
		}
	}

	script := decodePowerShellArgs(t, r.args)
	if !strings.Contains(script, "<text>Связь потеряна</text>") {
		t.Errorf("the title did not survive the round trip:\n%s", script)
	}
	if !strings.Contains(script, "<text>Tailscale не запущен</text>") {
		t.Errorf("the body did not survive the round trip:\n%s", script)
	}
	if !strings.Contains(script, "ToastNotificationManager") {
		t.Errorf("the script does not raise a toast:\n%s", script)
	}
	if !strings.Contains(script, defaultToastAppID) {
		t.Errorf("the script does not name the application identity:\n%s", script)
	}
}
