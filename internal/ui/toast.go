package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

// defaultToastAppID is the identity notifications are shown under.
//
// Windows only displays a toast for an application it knows, which in practice
// means one with a Start menu shortcut carrying an AppUserModelID. Rather than
// install a shortcut behind the user's back, we borrow the one Windows already
// ships for PowerShell — the same identity the notification is actually being
// raised from. The cost is cosmetic: the toast is filed under "Windows
// PowerShell" in the Action Centre.
const defaultToastAppID = `{1AC14E77-02E7-4E5D-B744-2EB1AE5198B7}\WindowsPowerShell\v1.0\powershell.exe`

// ToastNotifier shows a Windows toast by asking the Windows Runtime
// notification manager for one. It shells out rather than binding WinRT
// directly: a toast is a rare event, and a hundred lines of COM plumbing would
// be a hundred lines more of this program that only runs on a real desktop.
type ToastNotifier struct {
	runner runner
	appID  string
}

// NewToastNotifier returns a [Notifier] that shows Windows toasts. An empty
// appID means [defaultToastAppID].
func NewToastNotifier(appID string) *ToastNotifier {
	if appID == "" {
		appID = defaultToastAppID
	}
	return &ToastNotifier{runner: execRunner{}, appID: appID}
}

// Notify shows one toast.
func (n *ToastNotifier) Notify(ctx context.Context, note Notification) error {
	name, args, err := toastCommand(toastScript(n.appID, note))
	if err != nil {
		return fmt.Errorf("showing the notification %q: %w", note.Title, err)
	}
	if err := n.runner.run(ctx, name, args...); err != nil {
		return fmt.Errorf("showing the notification %q: %w", note.Title, err)
	}
	return nil
}

// toastScript builds the PowerShell that raises one toast.
//
// Both the title and the body reach it inside a literal here-string, and every
// character PowerShell or XML could take an interest in — including the quote
// that would end the here-string — is escaped by [escapeXML] first. The script
// itself is then handed over base64-encoded (see [toastCommand]), so no shell
// ever parses the Russian text.
func toastScript(appID string, note Notification) string {
	var b strings.Builder
	b.WriteString("$ErrorActionPreference = 'Stop'\n")
	b.WriteString("[Windows.UI.Notifications.ToastNotificationManager, Windows.UI.Notifications," +
		" ContentType = WindowsRuntime] > $null\n")
	b.WriteString("[Windows.Data.Xml.Dom.XmlDocument, Windows.Data.Xml.Dom.XmlDocument," +
		" ContentType = WindowsRuntime] > $null\n")
	b.WriteString("$xml = New-Object Windows.Data.Xml.Dom.XmlDocument\n")
	b.WriteString("$xml.LoadXml(@'\n")
	b.WriteString(toastXML(note))
	b.WriteString("\n'@)\n")
	b.WriteString("$toast = New-Object Windows.UI.Notifications.ToastNotification $xml\n")
	fmt.Fprintf(&b, "[Windows.UI.Notifications.ToastNotificationManager]::CreateToastNotifier('%s')"+
		".Show($toast)\n", escapeXML(appID))
	return b.String()
}

// toastXML is the toast's ToastGeneric payload: a headline and a line under it.
func toastXML(note Notification) string {
	return `<toast><visual><binding template="ToastGeneric">` +
		"<text>" + escapeXML(note.Title) + "</text>" +
		"<text>" + escapeXML(note.Body) + "</text>" +
		`</binding></visual></toast>`
}

// escapeXML escapes the five XML metacharacters and drops the control
// characters XML cannot carry. Escaping the apostrophe is not optional here:
// it is what stops a title from closing the PowerShell here-string around it.
func escapeXML(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '&':
			b.WriteString("&amp;")
		case r == '<':
			b.WriteString("&lt;")
		case r == '>':
			b.WriteString("&gt;")
		case r == '"':
			b.WriteString("&quot;")
		case r == '\'':
			b.WriteString("&apos;")
		case r < 0x20 && r != '\t':
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// encodePowerShellCommand encodes a script the way powershell.exe's
// -EncodedCommand wants it: UTF-16LE, then base64. Going through base64 is
// what lets Cyrillic reach PowerShell intact whatever the console code page
// happens to be, and removes command-line quoting from the picture entirely.
func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		// Splitting one UTF-16 code unit into its two bytes, low first. The
		// truncation is the whole point: byte(u) is the low half by
		// definition and byte(u>>8) the high one, and together they lose
		// nothing.
		raw = append(raw, byte(u), byte(u>>8)) //nolint:gosec // G115: deliberate byte split
	}
	return base64.StdEncoding.EncodeToString(raw)
}
