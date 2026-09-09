package quickopen

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// realish is a machine with every tool in the usual place. Windows Terminal is
// deliberately absent: it is not installed on the laptop this was written for,
// so the cmd.exe branch is the one that really runs and the one every test
// that does not say otherwise exercises.
var realish = tools{
	ssh:      `C:\Windows\System32\OpenSSH\ssh.exe`,
	conhost:  `C:\Windows\System32\conhost.exe`,
	shell:    `C:\Windows\System32\cmd.exe`,
	explorer: `C:\Windows\explorer.exe`,
	rdp:      `C:\Windows\System32\mstsc.exe`,
}

// peerConfig is the real pair of machines: the work PC as the laptop reaches
// it. The folder carries a space and Cyrillic on purpose — that is what
// -sync-folder actually holds on these machines.
var peerConfig = Config{
	PeerName: "WIN-STTM11D02RD",
	PeerUser: "user",
	PeerAddr: "100.127.188.87",
	KeyPath:  `C:\Users\pnj\.ssh\id_ed25519`,
	Folder:   `C:\Users\pnj\Общая папка`,
}

func TestTheTerminalLogsInAsTheProgramItselfDoes(t *testing.T) {
	t.Parallel()

	got, err := terminalCommand(realish, peerConfig)
	if err != nil {
		t.Fatalf("terminalCommand: %v", err)
	}

	// conhost.exe launches it, because that is the only way the shell inside
	// gets handles onto the window it is sitting in. See terminalCommand.
	if got.name != realish.conhost {
		t.Errorf("the terminal was launched by %q, want conhost.exe", got.name)
	}

	line := strings.Join(append([]string{got.name}, got.args...), " ")
	for _, want := range []string{
		`C:\Windows\System32\cmd.exe`,         // the shell that keeps the window
		keepOpen,                              // ... open after ssh has gone
		`C:\Windows\System32\OpenSSH\ssh.exe`, // the client Windows ships
		`-i C:\Users\pnj\.ssh\id_ed25519`,     // the program's own key
		"-o " + optIdentitiesOnly,             // and only that key
		"-o " + optStrictHostKey,              // sshx's TrustOnFirstUse
		"-o " + optConnectTimeout,             // a sleeping peer is not a hang
		"user@100.127.188.87",                 // the peer's own account
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the terminal command is missing %q:\n%s", want, line)
		}
	}
}

func TestWindowsTerminalIsPreferredWhenItIsInstalled(t *testing.T) {
	t.Parallel()

	withWT := realish
	withWT.terminal = `C:\Users\pnj\AppData\Local\Microsoft\WindowsApps\wt.exe`

	got, err := terminalCommand(withWT, peerConfig)
	if err != nil {
		t.Fatalf("terminalCommand: %v", err)
	}

	if got.name != withWT.terminal {
		t.Errorf("terminal host = %q, want Windows Terminal", got.name)
	}
	if want := []string{wtNewTab, wtTitle, terminalTitle, realish.ssh}; !slices.Equal(got.args[:4], want) {
		t.Errorf("the tab was opened with %q, want %q", got.args[:4], want)
	}
	// Windows Terminal keeps a tab whose process failed open by itself, so
	// neither cmd.exe nor a console host is in the way.
	if slices.Contains(got.args, keepOpen) {
		t.Error("Windows Terminal was handed cmd.exe /k, which it does not need")
	}
}

func TestATerminalNothingCanHostIsRefusedRatherThanGuessedAt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		tools tools
		want  error
	}{
		{name: "no ssh client at all", tools: tools{shell: `C:\cmd.exe`}, want: errNoSSH},
		{name: "nothing to host it", tools: tools{ssh: `C:\ssh.exe`}, want: errNoTerminal},
		{
			name:  "a console host but no shell to keep the window open",
			tools: tools{ssh: `C:\ssh.exe`, conhost: `C:\conhost.exe`},
			want:  errNoTerminal,
		},
		{
			name:  "a shell but no console host to give it a window",
			tools: tools{ssh: `C:\ssh.exe`, shell: `C:\cmd.exe`},
			want:  errNoTerminal,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := terminalCommand(tc.tools, peerConfig); !errors.Is(err, tc.want) {
				t.Errorf("terminalCommand error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestTheFolderIsHandedToExplorerAsOneArgument(t *testing.T) {
	t.Parallel()

	got, err := folderCommand(realish, `C:\Users\pnj\Общая папка\`)
	if err != nil {
		t.Fatalf("folderCommand: %v", err)
	}

	if got.name != realish.explorer {
		t.Errorf("folder opener = %q, want explorer.exe", got.name)
	}
	if len(got.args) != 1 {
		t.Fatalf("explorer was handed %d arguments, want exactly 1: %q", len(got.args), got.args)
	}
	// One argument, so the space and the Cyrillic in the path are nobody's
	// business but Explorer's.
	if want := filepath.Clean(`C:\Users\pnj\Общая папка`); got.args[0] != want {
		t.Errorf("explorer was pointed at %q, want %q", got.args[0], want)
	}
}

func TestAFolderThatWasNeverConfiguredOpensNothing(t *testing.T) {
	t.Parallel()

	for _, folder := range []string{"", "   ", "\t"} {
		if _, err := folderCommand(realish, folder); !errors.Is(err, errNoFolder) {
			t.Errorf("folderCommand(%q) error = %v, want errNoFolder", folder, err)
		}
	}
}

func TestAMissingToolIsNamedRatherThanSubstituted(t *testing.T) {
	t.Parallel()

	if _, err := folderCommand(tools{}, peerConfig.Folder); !errors.Is(err, errNoExplorer) {
		t.Errorf("folderCommand with no explorer.exe = %v, want errNoExplorer", err)
	}
	if _, err := desktopCommand(tools{}, peerConfig.PeerAddr); !errors.Is(err, errNoRDP) {
		t.Errorf("desktopCommand with no mstsc.exe = %v, want errNoRDP", err)
	}
}

func TestTheDesktopIsOpenedOnTheAddressTheMonitorWatches(t *testing.T) {
	t.Parallel()

	got, err := desktopCommand(realish, peerConfig.PeerAddr)
	if err != nil {
		t.Fatalf("desktopCommand: %v", err)
	}

	if got.name != realish.rdp {
		t.Errorf("desktop client = %q, want mstsc.exe", got.name)
	}
	if want := []string{"/v:100.127.188.87"}; !slices.Equal(got.args, want) {
		t.Errorf("mstsc was handed %q, want %q", got.args, want)
	}
}

func TestAValueACommandLineWouldReadAgainIsRefused(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "an ordinary key path", value: `C:\Users\pnj\.ssh\id_ed25519`, ok: true},
		{name: "a Cyrillic path with a space", value: `C:\Users\Пётр Иванов\.ssh\ключ`, ok: true},
		{name: "a user name", value: "user", ok: true},
		{name: "empty", value: ""},
		{name: "blank", value: "   "},
		{name: "a quote", value: `C:\a"b\key`},
		{name: "an ampersand", value: `C:\a&calc\key`},
		{name: "a pipe", value: `C:\a|b\key`},
		{name: "a redirect", value: `C:\a>b\key`},
		{name: "a caret", value: `C:\a^b\key`},
		{name: "a percent sign", value: `C:\%TEMP%\key`},
		{name: "a parenthesis", value: `C:\a(b)\key`},
		{name: "a bang", value: `C:\a!b!\key`},
		{name: "a Windows Terminal separator", value: `C:\a;b\key`},
		{name: "a comma", value: `C:\a,b\key`},
		{name: "a newline", value: "C:\\a\nb"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := checkValue("the SSH key", tc.value)
			if tc.ok {
				if err != nil {
					t.Fatalf("checkValue rejected a value it should accept: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("checkValue accepted a value a command line would read again")
			}
			// The message may name the field and the one offending character,
			// and must never repeat the value: one of the three fields it
			// guards is the path of a private key.
			if strings.Contains(err.Error(), tc.value) && strings.TrimSpace(tc.value) != "" {
				t.Errorf("the error repeats the value it was refusing: %v", err)
			}
		})
	}
}
