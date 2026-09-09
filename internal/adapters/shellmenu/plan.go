package shellmenu

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// verbLabel is the one string in this package the user reads, and it is
// therefore in Russian. It is written as MUIVerb rather than as the verb key's
// default value: MUIVerb is the value Explorer looks at first, it is where a
// localised string belongs, and it leaves the key's default value free.
const verbLabel = "Отправить на ПК"

// verbName is the key the verb lives under. Nobody reads it: it only has to be
// unique among the verbs registered on this machine and recognisable to a
// person poking about in regedit, which is why it carries the program's name.
const verbName = "LinkMonitor.SendToPC"

// classesRoot is the per-user class registrations, relative to HKCU.
//
// HKCU and never HKLM. Under HKLM the verb would need elevation to install, and
// it would appear for every account on the machine — including accounts with no
// Tailscale login and no key, for whom the item could only ever fail. Under
// HKCU it installs with no prompt and belongs to the one user who asked for it.
const classesRoot = `Software\Classes`

// allFiles is the class every file belongs to in addition to its own progid, so
// one registration covers .txt, .zip, .mkv and the extensions nobody has
// invented yet.
//
// Only files. Folders — the `Directory` class — are deliberately left out:
// Taildrop moves one file at a time and the adapter refuses a directory
// outright ("... is a directory: Taildrop sends files"), so a folder verb could
// only ever raise a failure notification. An item that cannot work is worse
// than an item that is not there. Zipping a folder first would be a different
// feature, with a different name in the menu.
const allFiles = "*"

// commandKey is the subkey holding what Explorer actually runs.
const commandKey = "command"

// sendFlag is the flag the verb hands the path in. See cmd/linkmon.
const sendFlag = "-send"

// commandTarget is the placeholder Explorer replaces with the full path of the
// item that was right-clicked.
const commandTarget = "%1"

// The values written into the verb key.
const (
	muiVerbValue     = "MUIVerb"
	iconValue        = "Icon"
	multiSelectValue = "MultiSelectModel"
)

// multiSelectModel states what Explorer does with more than one file selected,
// rather than leaving it to be inferred.
//
// "Document" means the verb is invoked once per selected item — a separate
// process for each file, each with its own `-send`. That is the right shape for
// this program: each file becomes its own transfer in the Файлы tab, and the
// first process to find the running instance hands its file over, so the copies
// do not fight. It is also the default Explorer assumes for a verb implemented
// as a command line, so writing it changes nothing and documents everything.
//
// What it is not is a way to send a hundred files. Explorer caps a
// once-per-item verb at its multiple-invoke limit — the MultipleInvokePromptMinimum
// policy, sixteen items out of the box — and above that it prompts or drops the
// verb from the menu rather than starting a hundred processes. The honest
// summary is: a handful of files at a time, and the drop zone in the window for
// more.
const multiSelectModel = "Document"

// errBadExePath rejects an executable path that could not be written into a
// command Explorer will run years from now.
var errBadExePath = errors.New("invalid executable path")

// entry is one registry key and the values to write into it. The registry names
// a key's unnamed value the "default" value and addresses it by the empty
// string, which is exactly how it is spelled here.
type entry struct {
	path   string
	values map[string]string
}

// plan is the whole of the installation, decided with no registry in sight: the
// keys, the values, and the refusal of an executable path that would not work.
// Everything below it only writes what this returns.
func plan(root, exePath string) ([]entry, error) {
	if err := checkExe(exePath); err != nil {
		return nil, fmt.Errorf("planning the Explorer menu item: %w", err)
	}
	verb := verbPath(root)
	return []entry{
		{path: verb, values: map[string]string{
			muiVerbValue:     verbLabel,
			iconValue:        icon(exePath),
			multiSelectValue: multiSelectModel,
		}},
		{path: verb + `\` + commandKey, values: map[string]string{"": commandLine(exePath)}},
	}, nil
}

// verbPath is where the verb's own key sits under root.
func verbPath(root string) string {
	return root + `\` + allFiles + `\shell\` + verbName
}

// commandLine is what Explorer runs when the item is chosen.
//
// Both the exe and the placeholder are quoted, and that is the whole of the
// quoting problem: Explorer substitutes the clicked file's path into %1 and
// hands the result to CreateProcess, with no command interpreter anywhere in
// the chain. So a name with spaces arrives as one argument, and `&`, `^`, `|`
// and `%` arrive as themselves rather than as something cmd.exe would have
// reinterpreted. A double quote cannot appear in a Windows path at all, which
// is why nothing here has to escape one — and why [checkExe] refuses an exe
// path containing one rather than trying.
func commandLine(exePath string) string {
	return `"` + exePath + `" ` + sendFlag + ` "` + commandTarget + `"`
}

// icon points Explorer at the first icon in the program's own exe, so the menu
// item carries the program's face without this package shipping an .ico or the
// exe growing a resource section.
func icon(exePath string) string {
	return `"` + exePath + `",0`
}

// checkExe refuses an executable path that would make a broken command.
//
// The path is written into the registry once and read back by Explorer for as
// long as the item is installed, so a fault here is a menu item that silently
// does nothing months later. Each refusal below is a way that happens.
func checkExe(exePath string) error {
	switch {
	case strings.TrimSpace(exePath) == "":
		return fmt.Errorf("%w: it is empty", errBadExePath)
	case !filepath.IsAbs(exePath):
		// Explorer runs the verb with the clicked file's folder as the working
		// directory, so a relative path would resolve somewhere different every
		// time — usually nowhere.
		return fmt.Errorf("%w: %q is not absolute", errBadExePath, exePath)
	case strings.Contains(exePath, `"`):
		// A quote cannot occur in a Windows path, and one here would end the
		// quoting early and turn the rest of the path into arguments.
		return fmt.Errorf("%w: it contains a double quote", errBadExePath)
	case strings.Contains(exePath, "%"):
		// Explorer expands %1..%9, %*, %L, %D, %V and %W in a command template.
		// A per cent sign in the program's own path is legal on disk and would
		// be eaten here, so it is refused rather than mangled.
		return fmt.Errorf("%w: it contains a per cent sign", errBadExePath)
	case strings.ContainsAny(exePath, "\r\n\x00"):
		return fmt.Errorf("%w: it contains a control character", errBadExePath)
	case !strings.EqualFold(filepath.Ext(exePath), ".exe"):
		return fmt.Errorf("%w: %q is not an .exe", errBadExePath, filepath.Base(exePath))
	default:
		return nil
	}
}

// ancestors returns the keys between path and root, deepest first, so removal
// can offer each of them back in the order the registry will accept.
//
// root itself is never returned: Software\Classes is Windows's, not ours.
func ancestors(root, path string) []string {
	var out []string
	for {
		cut := strings.LastIndex(path, `\`)
		if cut <= 0 {
			return out
		}
		path = path[:cut]
		if len(path) <= len(root) {
			return out
		}
		out = append(out, path)
	}
}
