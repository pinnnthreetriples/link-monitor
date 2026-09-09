package quickopen

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// The SSH options the terminal logs in with. They are the decisions
// internal/adapters/sshx already makes for its own in-process client, written
// in OpenSSH's configuration language so that the window the user gets is the
// same identity the program itself uses:
//
//   - IdentitiesOnly=yes offers the key from this program's configuration and
//     nothing else. Without it OpenSSH also tries every key an agent holds,
//     and the point of the item is that it logs in the way the monitor does.
//   - StrictHostKeyChecking=accept-new is sshx's TrustOnFirstUse: a host
//     known_hosts has never seen is recorded, a host whose key changed is
//     still refused. Anything looser would be a weakening this program has
//     deliberately never made.
//   - ConnectTimeout bounds the dial to a machine that is asleep, so the
//     window says what went wrong instead of sitting blank for two minutes.
const (
	optIdentitiesOnly = "IdentitiesOnly=yes"
	optStrictHostKey  = "StrictHostKeyChecking=accept-new"
	optConnectTimeout = "ConnectTimeout=10"
)

// terminalTitle names the Windows Terminal tab. It is the one string in this
// package the user reads, and it is therefore in Russian — the same argument
// internal/adapters/shellmenu makes for its verb label.
const terminalTitle = "ПК по SSH"

// keepOpen is cmd.exe's "run this and then stay". It is what makes a failed
// login readable: ssh prints its refusal and exits, and without /k the console
// window would close over the message before anyone could read it.
const keepOpen = "/k"

// The Windows Terminal words for "open a tab running this".
const (
	wtNewTab = "new-tab"
	wtTitle  = "--title"
)

// rdpTarget is mstsc.exe's "connect to this machine" flag. The address is
// appended to it with no space, which is how mstsc spells it.
const rdpTarget = "/v:"

// rdpPort is the port Remote Desktop listens on, and rdpProbeTimeout is how
// long the peer is given to answer on it before the action gives up.
//
// The probe is there because mstsc.exe is patient to a fault. Pointed at an
// address nothing answers on, it puts up its «Подключение к удалённому
// рабочему столу» window and sits on it: measured on the laptop on 2026-09-08
// it was still spinning after two minutes, with no message and no hint that
// the machine is simply not there. A menu item that behaves like that is the
// hang this feature was asked not to ship, so the port is dialled first and a
// peer that does not answer becomes a notification the user can read at once.
// The cost on the happy path is one round trip over the tailnet — 0.3 s to the
// work PC — before a window that takes longer than that to draw.
const (
	rdpPort         = "3389"
	rdpProbeTimeout = 3 * time.Second
)

// errPeerHasNoDesktop reports that the peer is not answering on the Remote
// Desktop port.
var errPeerHasNoDesktop = errors.New("the peer is not answering on the remote-desktop port")

// reparsed are the characters a host that reads its own command line again
// would act on rather than pass through. cmd.exe re-reads `&` `|` `<` `>` `^`
// `(` `)` and `%`, treats `!` as a variable when delayed expansion is on, and
// takes `"` as the end of an argument; Windows Terminal takes `;` and `,` as
// its own separators between commands and between an option's values.
//
// A value holding one of them is refused rather than escaped. Escaping would
// mean writing a second, private copy of two command-line parsers and being
// right about both; refusing is one line and cannot be subtly wrong, and the
// only values that reach here are a key path, a user name and an address the
// user typed on this program's command line.
const reparsed = `"&|<>^%()!;,`

// errUnsafeValue reports a configured value that cannot be handed to a
// terminal host safely. It names the offending character and never the value:
// one of the three values it guards is a private key's path.
var errUnsafeValue = errors.New("holds a character a command line would read again")

// errMissingValue reports a piece of configuration the actions cannot work
// without.
var errMissingValue = errors.New("is not configured")

// The tools an action needs and this machine turned out not to have. Each is
// reported at the click rather than at startup, so a machine missing one tool
// still offers the other two actions.
var (
	errNoSSH      = errors.New("no ssh.exe was found on this machine")
	errNoTerminal = errors.New("no terminal host was found on this machine")
	errNoExplorer = errors.New("explorer.exe was not found on this machine")
	errNoRDP      = errors.New("mstsc.exe was not found on this machine")
	errNoFolder   = errors.New("no shared folder is configured")
)

// command is one process to start.
type command struct {
	name string
	args []string
}

// checkValue refuses a configured value that is empty or that a terminal host
// would re-read. what names the field, never its contents.
func checkValue(what, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("%s %w", what, errMissingValue)
	}
	if i := strings.IndexAny(value, reparsed); i >= 0 {
		// Only the one character is quoted. Every byte in [reparsed] is ASCII,
		// so this cannot cut a rune in half.
		return fmt.Errorf("%s %w: %q", what, errUnsafeValue, value[i:i+1])
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%s %w: a control character", what, errUnsafeValue)
		}
	}
	return nil
}

// sshArgs is the argument vector that logs in to the peer as the program does.
func sshArgs(cfg Config) []string {
	return []string{
		"-i", cfg.KeyPath,
		"-o", optIdentitiesOnly,
		"-o", optStrictHostKey,
		"-o", optConnectTimeout,
		cfg.PeerUser + "@" + cfg.PeerAddr,
	}
}

// terminalCommand is what «Открыть терминал на ПК» starts.
//
// Windows Terminal hosts the session when it is installed; a console hosts it
// when it is not. The difference between them is not cosmetic — it is what
// keeps a failed login on screen. Windows Terminal's default closeOnExit is
// "graceful", which leaves a tab whose process exited non-zero open with the
// message still in it, so ssh can be run directly. A console has no such rule
// and disappears with its process, so there cmd.exe /k holds the window open
// after ssh has gone.
//
// # Why conhost.exe is in the chain
//
// Starting cmd.exe with CREATE_NEW_CONSOLE looks like the obvious way to get a
// console window from a program that has none, and it does not work. Go's
// os/exec always sets STARTF_USESTDHANDLES and, when Stdin, Stdout and Stderr
// are nil, points all three at the null device. The new console is created and
// its window appears, but the shell inside it is reading and writing NUL: it
// prints its prompt nowhere and sees end-of-file on the first read, so
// `cmd /k` exits within milliseconds and the user is left looking at a window
// that closed for no reason. Measured on the laptop on 2026-09-08 — cmd.exe
// with CREATE_NEW_CONSOLE was gone before the check two seconds later, while
// the same command line under conhost.exe was still sitting at its prompt.
//
// conhost.exe is the console host Windows itself uses. Given a command line it
// creates the console and hands its own child the handles that belong to it,
// which is exactly the part os/exec cannot do for us. Nothing else about the
// launch changes, and no process-creation flag is needed: the window comes
// from conhost, not from a flag of ours.
func terminalCommand(t tools, cfg Config) (command, error) {
	if t.ssh == "" {
		return command{}, errNoSSH
	}
	args := sshArgs(cfg)

	if t.terminal != "" {
		return command{
			name: t.terminal,
			args: append([]string{wtNewTab, wtTitle, terminalTitle, t.ssh}, args...),
		}, nil
	}
	if t.conhost == "" || t.shell == "" {
		return command{}, errNoTerminal
	}
	return command{
		name: t.conhost,
		args: append([]string{t.shell, keepOpen, t.ssh}, args...),
	}, nil
}

// folderCommand is what «Открыть общую папку» starts.
//
// explorer.exe takes the path as one argument of its own, so nothing re-reads
// it — which is why the folder is not checked by [checkValue] the way the SSH
// values are. It is the folder the user named with -sync-folder, and on these
// machines it is «C:\Users\pnj\Общая папка»: a path with a space and Cyrillic
// in it, and exactly the kind of value a re-parsing host would mangle.
func folderCommand(t tools, folder string) (command, error) {
	if strings.TrimSpace(folder) == "" {
		return command{}, errNoFolder
	}
	if t.explorer == "" {
		return command{}, errNoExplorer
	}
	return command{name: t.explorer, args: []string{filepath.Clean(folder)}}, nil
}

// desktopCommand is what «Открыть рабочий стол ПК» starts.
func desktopCommand(t tools, addr string) (command, error) {
	if t.rdp == "" {
		return command{}, errNoRDP
	}
	return command{name: t.rdp, args: []string{rdpTarget + addr}}, nil
}
