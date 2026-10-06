package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"strings"

	"github.com/pinnnthreetriples/link-monitor/internal/adapters/sshx"
)

// askMode hands one task to Claude Code on the peer and prints its answer:
// `linkmon -ask "задача"`, or `-ask -` to read the task from stdin. It is how
// Claude Code on this machine gives work to the one on the other.
//
// The task goes over SFTP into a file rather than onto a command line, because
// cmd.exe stops at 8191 characters and a task is often longer. The answer goes
// to stdout, the session id to stderr as `[session <id>]`, which
// -ask-resume takes to continue the same conversation. The build has no
// console, so this only prints when stdout is redirected — which is how a
// script or another program calls it.
func askMode(o options) int {
	prompt, err := askPrompt(o.ask, os.Stdin)
	if err == nil && o.askResume != "" && !sessionID.MatchString(o.askResume) {
		err = errors.New("-ask-resume: это не id сессии Claude")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ssh := newSSH(o)
	defer func() { _ = ssh.Close() }() // the process is exiting; nothing is left to tell

	stdout, stderr, err := ask(ctx, ssh, prompt, o.askDir, o.askResume)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	answer, err := readAskAnswer(stdout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n%s\n%s\n", err, stdout, stderr)
		return 1
	}
	fmt.Fprintln(os.Stdout, answer.Result)
	fmt.Fprintf(os.Stderr, "[session %s]\n", answer.SessionID)
	if answer.IsError {
		return 1
	}
	return 0
}

// sessionID is the shape of a Claude Code session id. It is checked because
// the value is written into the script run on the peer.
var sessionID = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)

func askPrompt(flagValue string, stdin io.Reader) (string, error) {
	if flagValue != "-" {
		return flagValue, nil
	}
	data, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("reading the task from stdin: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", errors.New("-ask -: на stdin пусто")
	}
	return string(data), nil
}

// ask uploads the prompt and runs Claude Code on the peer over it.
func ask(ctx context.Context, ssh *sshx.Lazy, prompt, dir, resume string) (stdout, stderr string, err error) {
	name, err := randomName()
	if err != nil {
		return "", "", err
	}
	// Relative to the peer user's home, which is where Windows OpenSSH's
	// sftp-server starts; the script finds it at the same place via $HOME.
	rel := "AppData/Local/Temp/" + name
	if err := uploadTask(ctx, ssh, rel, prompt); err != nil {
		return "", "", err
	}
	stdout, stderr, _, err = ssh.RunPowerShell(ctx, askScript(rel, dir, resume))
	if err != nil {
		return "", "", fmt.Errorf("running Claude on the peer: %w", err)
	}
	return stdout, stderr, nil
}

func randomName() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("naming the task file: %w", err)
	}
	return "linkmon-ask-" + hex.EncodeToString(b) + ".txt", nil
}

func uploadTask(ctx context.Context, ssh *sshx.Lazy, rel, prompt string) error {
	client, err := ssh.SFTP(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }() // the file is already written and closed below
	f, err := client.Create(rel)
	if err != nil {
		return fmt.Errorf("creating the task file on the peer: %w", err)
	}
	if _, err := f.Write([]byte(prompt)); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return fmt.Errorf("writing the task file on the peer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the task file on the peer: %w", err)
	}
	return nil
}

// askScript finds claude.exe on the peer — on PATH, or the newest copy the
// VS Code-style extension ships — and pipes the task file into `claude -p`.
// Every value from outside arrives base64-encoded, so none of it is parsed as
// PowerShell.
func askScript(rel, dir, resume string) string {
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	return strings.NewReplacer("@REL@", b64(rel), "@DIR@", b64(dir), "@RESUME@", b64(resume)).Replace(`
function D($s) { [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($s)) }
$f = Join-Path $HOME (D '@REL@')
$dir = D '@DIR@'
$resume = D '@RESUME@'
$c = (Get-Command claude -CommandType Application -ErrorAction SilentlyContinue |
  Select-Object -First 1).Source
if (-not $c) {
  $ext = "$HOME\.*\extensions\anthropic.claude-code-*\resources\native-binary\claude.exe"
  $c = Get-ChildItem $ext -ErrorAction SilentlyContinue |
    Sort-Object { [version]($_.FullName -replace '.*claude-code-([\d.]+)-.*', '$1') } |
    Select-Object -Last 1 -ExpandProperty FullName
}
if (-not $c) { Remove-Item -LiteralPath $f; Write-Output 'NOCLAUDE'; exit 3 }
if ($dir) { Set-Location -LiteralPath $dir }
$a = @('-p', '--output-format', 'json', '--dangerously-skip-permissions')
if ($resume) { $a += @('--resume', $resume) }
$out = Get-Content -Raw -Encoding UTF8 -LiteralPath $f | & $c @a
$code = $LASTEXITCODE
Remove-Item -LiteralPath $f -ErrorAction SilentlyContinue
$out
exit $code
`)
}

type askAnswer struct {
	Result    string `json:"result"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
}

// readAskAnswer picks Claude's JSON line out of the script's stdout, which can
// also carry PowerShell's noise.
func readAskAnswer(stdout string) (askAnswer, error) {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "NOCLAUDE" {
			return askAnswer{}, errors.New("на второй машине не найден claude.exe")
		}
		var a askAnswer
		if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &a) == nil && a.SessionID != "" {
			return a, nil
		}
	}
	return askAnswer{}, errors.New("Claude на второй машине не ответил JSON")
}
