package tailscale

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// execRunner runs the real tailscale CLI. It resolves the executable once, on
// first use, so that constructing a Client touches no disk.
type execRunner struct {
	configured string

	once     sync.Once
	resolved string
	resolveE error
}

func newExecRunner(path string) *execRunner {
	return &execRunner{configured: path}
}

// resolve returns the executable to run: the configured path when it exists,
// otherwise whatever "tailscale" the PATH offers, which covers non-default
// installs and development machines.
//
// gosec reads r.configured as tainted because newExecRunner takes it as a
// parameter, and through it [WithExePath] is exported. Nothing outside this
// program ever supplies it: production builds one runner, in [New], from the
// compile-time constant [DefaultExePath], and WithExePath has no caller but
// the tests in this package. No configuration file, environment variable or
// API request reaches either. The fallback resolves the literal name
// "tailscale" through exec.LookPath, which since Go 1.19 refuses a match
// found by way of an empty or relative PATH entry.
func (r *execRunner) resolve() (string, error) {
	r.once.Do(func() {
		//nolint:gosec // G703: the path is a compile-time constant, see above.
		if info, err := os.Stat(r.configured); err == nil && !info.IsDir() {
			r.resolved = r.configured
			return
		}
		onPath, err := exec.LookPath("tailscale")
		if err != nil {
			r.resolveE = fmt.Errorf("tailscale CLI not found at %q nor on PATH: %w", r.configured, err)
			return
		}
		r.resolved = onPath
	})
	return r.resolved, r.resolveE
}

// Run executes the CLI with the given arguments and collects both streams. A
// non-zero exit status is not an error here: it is reported in the result.
func (r *execRunner) Run(ctx context.Context, args ...string) (CommandResult, error) {
	exe, err := r.resolve()
	if err != nil {
		return CommandResult{}, err
	}

	// Every argument list this runner is ever handed is built in this package,
	// from literals plus two values that cannot carry a separator:
	//
	//   Run(ctx, "up", "--timeout="+budget.String())   budget is a Duration
	//   Run(ctx, "down")
	//   Run(ctx, "serve", "--bg", strconv.Itoa(port))  port is an int, 1..65535
	//   Run(ctx, "serve", "reset")
	//
	// The port is the only value a request can influence, it arrives as a JSON
	// number rather than a string, and it is range-checked three times on the
	// way down — httpapi.handleServe, app.Forwards.Serve and Client.Serve —
	// before strconv.Itoa renders it as digits. exec.CommandContext hands argv
	// to CreateProcess as a vector, so there is no shell to re-read any of it.
	//
	//nolint:gosec // G702/G204: argv is built here from literals and a bounded int.
	cmd := exec.CommandContext(ctx, exe, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	res := CommandResult{Stdout: stdout.String(), Stderr: stderr.String()}

	var exitErr *exec.ExitError
	switch {
	case runErr == nil:
	case errors.As(runErr, &exitErr):
		res.Code = exitErr.ExitCode()
	default:
		return res, fmt.Errorf("running %s: %w", filepath.Base(exe), runErr)
	}

	// A killed process reports an exit status, not the cancellation, so ask the
	// context what really happened.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return res, fmt.Errorf("running %s: %w", filepath.Base(exe), ctxErr)
	}
	return res, nil
}

// cliMessage is the most useful one-line explanation a failed command gave,
// trimmed so an error stays readable. CLI diagnostics carry no secrets.
func cliMessage(res CommandResult) string {
	msg := strings.TrimSpace(res.Stderr)
	if msg == "" {
		msg = strings.TrimSpace(res.Stdout)
	}
	if msg == "" {
		return "no output"
	}
	if i := strings.IndexAny(msg, "\r\n"); i >= 0 {
		msg = strings.TrimSpace(msg[:i])
	}
	const maxLen = 200
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return msg
}
