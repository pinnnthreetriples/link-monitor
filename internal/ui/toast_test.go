package ui

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestEscapeXML(t *testing.T) {
	tests := map[string]string{
		"Связь потеряна": "Связь потеряна",
		"a & b":          "a &amp; b",
		"<text>":         "&lt;text&gt;",
		`say "no"`:       "say &quot;no&quot;",
		"it's":           "it&apos;s",
		"a\x00b":         "a b",
	}
	for in, want := range tests {
		if got := escapeXML(in); got != want {
			t.Errorf("escapeXML(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToastXMLCarriesBothLines(t *testing.T) {
	got := toastXML(Notification{Title: "Связь потеряна", Body: "Tailscale не запущен"})

	if !strings.Contains(got, "<text>Связь потеряна</text>") {
		t.Errorf("XML has no title: %s", got)
	}
	if !strings.Contains(got, "<text>Tailscale не запущен</text>") {
		t.Errorf("XML has no body: %s", got)
	}
	if !strings.Contains(got, `template="ToastGeneric"`) {
		t.Errorf("XML is not a ToastGeneric binding: %s", got)
	}
}

func TestToastScriptCannotBeEscapedByTheNotificationText(t *testing.T) {
	// The one sequence that could close the PowerShell here-string is a quote
	// followed by an at-sign. It must not survive into the script.
	hostile := Notification{
		Title: "'@\n[Environment]::Exit(1)\n@'",
		Body:  "$(Get-Process) & `whoami`",
	}
	script := toastScript(defaultToastAppID, hostile)

	body := script[strings.Index(script, "@'\n")+3:]
	// Not named `real`: that is a builtin, and shadowing one reads as a bug.
	first, terminator := strings.Index(body, "'@"), strings.Index(body, "\n'@)")+1
	if first != terminator {
		t.Fatalf("the here-string closes at offset %d, not at its terminator %d:\n%s",
			first, terminator, body)
	}
	// The text survives as text — escaped, on one line, inside the XML.
	if xml := body[:terminator-1]; strings.Count(xml, "\n") != 0 {
		t.Errorf("a newline from the title reached the script body:\n%s", xml)
	}
	if !strings.Contains(script, "&amp;") || !strings.Contains(script, "&apos;") {
		t.Error("the hostile text was not escaped at all")
	}
	if !strings.Contains(script, "$(Get-Process)") {
		t.Error("the body was mangled rather than merely escaped")
	}
}

func TestEncodePowerShellCommandIsUTF16LEBase64(t *testing.T) {
	const script = "Write-Output 'Связь'"

	raw, err := base64.StdEncoding.DecodeString(encodePowerShellCommand(script))
	if err != nil {
		t.Fatalf("the encoded command is not base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("decoded %d bytes, which is not whole UTF-16 units", len(raw))
	}

	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	if got := string(utf16.Decode(units)); got != script {
		t.Errorf("decoded %q, want %q", got, script)
	}
}

func TestNewToastNotifierDefaultsToThePowerShellIdentity(t *testing.T) {
	if got := NewToastNotifier("").appID; got != defaultToastAppID {
		t.Errorf("appID = %q, want the default", got)
	}
	if got := NewToastNotifier("Link.Monitor").appID; got != "Link.Monitor" {
		t.Errorf("appID = %q, want the one that was asked for", got)
	}
	if _, ok := NewToastNotifier("").runner.(execRunner); !ok {
		t.Error("NewToastNotifier did not wire in the real runner")
	}
}

func TestToastNotifierReportsAFailure(t *testing.T) {
	n := &ToastNotifier{runner: &fakeRunner{err: errFake}, appID: defaultToastAppID}

	got := n.Notify(t.Context(), Notification{Title: "Связь потеряна"})
	if got == nil {
		t.Fatal("Notify hid the failure")
	}
	if !errors.Is(got, errFake) {
		t.Errorf("Notify = %v, want it to wrap the runner's error", got)
	}
	if !strings.Contains(got.Error(), "Связь потеряна") {
		t.Errorf("Notify = %v, want the title in the message", got)
	}
}
