package peerclip

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/localendpoint"
)

// The three prefixes the script below answers with. They are tokens rather
// than prose because PowerShell on these machines writes in Russian and a
// reader that parsed its sentences would break the first time Windows was
// updated — the same reason sshx.PeerCanReachUs scans for a token of its own.
const (
	markAbsent  = "ABSENT"
	markRecord  = "RECORD "
	markProcess = "PROCESS "
	markNoProc  = "NOPROCESS "
)

// stampFormat is how the script writes the peer's process creation time: .NET's
// round-trip format, which carries seven fractional digits — the same
// 100-nanosecond tick Windows reports a creation time at, and the resolution
// localendpoint compares two of them with.
const stampFormat = "2006-01-02T15:04:05.9999999Z07:00"

// recordScript reads the peer's endpoint record and asks the peer's own
// operating system about the process it names, in one round trip.
//
// %LOCALAPPDATA% is expanded on the peer rather than assembled here from a user
// name: the peer knows where its own profile is, and a path guessed from this
// side would be wrong the first time somebody's account was named differently.
//
// $id rather than $pid: $pid is one of PowerShell's automatic variables and
// assigning to it is an error.
var recordScript = strings.Join([]string{
	`$path = Join-Path $env:LOCALAPPDATA '` + localendpoint.RelativePath() + `'`,
	`if (-not (Test-Path -LiteralPath $path)) { Write-Output '` + markAbsent + `'; exit 0 }`,
	`$raw = [IO.File]::ReadAllBytes($path)`,
	`Write-Output ('` + markRecord + `' + [Convert]::ToBase64String($raw))`,
	`try {`,
	`  $id = [int](ConvertFrom-Json ([Text.Encoding]::UTF8.GetString($raw))).pid`,
	`  $p = Get-Process -Id $id -ErrorAction Stop`,
	`  Write-Output ('` + markProcess + `' + $id + '|' + $p.Path + '|' +`,
	`    $p.StartTime.ToUniversalTime().ToString('o'))`,
	`} catch { Write-Output ('` + markNoProc + `' + $id) }`,
}, "\n")

// peerProcess is what the peer said about the process behind the pid.
type peerProcess struct {
	pid       int
	exe       string
	startedAt time.Time
}

// Record returns the record the peer's instance published, confirmed against
// the peer's own view of the process that published it.
//
// Both halves matter and both come from the peer. The bytes go through
// [localendpoint.Parse], so a record that is malformed, of an unknown schema
// version, or names an address that is not that machine's own loopback is
// refused before anything is dialled. The process goes through
// [localendpoint.Record.Confirm], so a record left behind by an instance that
// has since exited — or whose pid Windows has since handed to something else —
// is refused too. Every refusal carries [localendpoint.ErrNoRecord] or
// [localendpoint.ErrStale], which [localendpoint.NotRunning] answers for.
func (p *Peer) Record(ctx context.Context) (localendpoint.Record, error) {
	stdout, stderr, code, err := p.shell.RunPowerShell(ctx, recordScript)
	if err != nil {
		return localendpoint.Record{}, fmt.Errorf("asking %s where its instance listens: %w", p.name, err)
	}
	if code != 0 {
		return localendpoint.Record{}, fmt.Errorf(
			"asking %s where its instance listens: the script exited with %d: %s",
			p.name, code, firstLine(stderr))
	}
	return p.readRecord(stdout)
}

// readRecord turns the script's answer into a confirmed record.
func (p *Peer) readRecord(stdout string) (localendpoint.Record, error) {
	rec, found, proc, err := scanAnswer(stdout)
	switch {
	case err != nil:
		return localendpoint.Record{}, fmt.Errorf("reading %s's endpoint record: %w", p.name, err)
	case !found:
		return localendpoint.Record{}, fmt.Errorf(
			"%s has published no endpoint record — the program is not running there: %w",
			p.name, localendpoint.ErrNoRecord)
	case proc == nil:
		return localendpoint.Record{}, fmt.Errorf(
			"%s is not running process %d any more: %w", p.name, rec.PID, localendpoint.ErrStale)
	case proc.pid != rec.PID:
		// The script looked up a different pid from the one this side read out
		// of the same bytes. That cannot happen with a record either end
		// accepts, and confirming the record against the wrong process is the
		// one mistake that would make the whole check meaningless.
		return localendpoint.Record{}, fmt.Errorf(
			"%s described process %d while its record names %d: %w",
			p.name, proc.pid, rec.PID, localendpoint.ErrStale)
	}
	if err := rec.Confirm(proc.exe, proc.startedAt); err != nil {
		return localendpoint.Record{}, fmt.Errorf("confirming %s's endpoint record: %w", p.name, err)
	}
	return rec, nil
}

// scanAnswer picks the script's three possible lines out of stdout.
//
// The lines are looked for rather than the stream being trusted as a whole:
// started from cmd.exe, PowerShell can print a "preparing modules" blob on a
// cold run before the first line of script executes, and nothing in the script
// can stop it.
func scanAnswer(stdout string) (rec localendpoint.Record, found bool, proc *peerProcess, err error) {
	for _, raw := range strings.Split(stdout, "\n") {
		line := strings.TrimSpace(raw)
		switch {
		case line == markAbsent:
			return localendpoint.Record{}, false, nil, nil
		case strings.HasPrefix(line, markRecord):
			rec, err = decodeRecord(strings.TrimPrefix(line, markRecord))
			if err != nil {
				return localendpoint.Record{}, false, nil, err
			}
			found = true
		case strings.HasPrefix(line, markNoProc):
			proc = nil
		case strings.HasPrefix(line, markProcess):
			proc, err = decodeProcess(strings.TrimPrefix(line, markProcess))
			if err != nil {
				return localendpoint.Record{}, false, nil, err
			}
		}
	}
	if !found {
		return localendpoint.Record{}, false, nil, nil
	}
	return rec, true, proc, nil
}

// decodeRecord turns the base64 the script printed into a validated record.
// Base64 rather than the raw JSON because the record holds a Windows path with
// backslashes and the peer's console is on a Russian code page: one encoding
// nobody has an opinion about is cheaper than two that have to agree.
func decodeRecord(encoded string) (localendpoint.Record, error) {
	data, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return localendpoint.Record{}, fmt.Errorf("%w: %w", err, localendpoint.ErrStale)
	}
	rec, err := localendpoint.Parse(data)
	if err != nil {
		return localendpoint.Record{}, fmt.Errorf("the record the peer published: %w", err)
	}
	return rec, nil
}

// decodeProcess reads "<pid>|<image path>|<creation time>".
//
// The image path may itself hold no bar character on Windows — the character is
// not legal in a path — so splitting into exactly three is safe, and a line
// that does not split into three is a script whose output has changed.
func decodeProcess(line string) (*peerProcess, error) {
	parts := strings.SplitN(strings.TrimSpace(line), "|", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("the peer described its process as %q: %w", line, localendpoint.ErrStale)
	}
	pid, err := parsePID(parts[0])
	if err != nil {
		return nil, err
	}
	startedAt, err := time.Parse(stampFormat, parts[2])
	if err != nil {
		return nil, fmt.Errorf("reading the peer's process start time %q: %w: %w",
			parts[2], err, localendpoint.ErrStale)
	}
	return &peerProcess{pid: pid, exe: parts[1], startedAt: startedAt.UTC()}, nil
}

// parsePID reads the process id the peer looked up.
func parsePID(raw string) (int, error) {
	var pid int
	if _, err := fmt.Sscanf(strings.TrimSpace(raw), "%d", &pid); err != nil || pid <= 0 {
		return 0, fmt.Errorf("the peer named process id %q: %w", raw, localendpoint.ErrStale)
	}
	return pid, nil
}

// firstLine is the head of a stream, for an error message that should not
// contain a whole console transcript.
func firstLine(s string) string {
	line := strings.TrimSpace(strings.SplitN(strings.TrimSpace(s), "\n", 2)[0])
	if len(line) > 200 {
		return line[:200]
	}
	return line
}
