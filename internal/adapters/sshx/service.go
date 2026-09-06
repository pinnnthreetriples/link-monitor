package sshx

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// serviceToken prefixes the one line the service query prints. See token.go for
// why the answer is a token line rather than the whole of stdout.
const serviceToken = "LINKMON-SERVICE"

// The values that line can carry.
const (
	// serviceStatusMissing means the service is not installed. It is negative so
	// it cannot collide with a real ServiceControllerStatus.
	serviceStatusMissing = -1
	// serviceStatusRunning is System.ServiceProcess.ServiceControllerStatus.Running.
	// We compare the numeric value on purpose: the names of the enum members are
	// stable, but nothing else PowerShell prints is, and these machines run a
	// Russian Windows.
	serviceStatusRunning = 4
)

// ServiceRunning reports whether a Windows service is running on the peer -
// "sshd" being the one this program cares about most.
//
// A service that is not installed is reported as not running, with no error: to
// everything upstream, "sshd is absent" and "sshd is stopped" lead to the same
// dashboard row. An error means the question could not be asked.
//
// The peer's verdict wins over its exit code. If the token line is on the
// stream we trust it, because a status is a status however the shell felt about
// the run; the exit code and stderr only become the answer when no verdict
// arrived at all.
func (c *Client) ServiceRunning(ctx context.Context, name string) (bool, error) {
	script, err := serviceStatusScript(name)
	if err != nil {
		return false, err
	}

	stdout, stderr, code, err := c.RunPowerShell(ctx, script)
	if err != nil {
		return false, fmt.Errorf("querying the service %q on %s: %w", name, c.cfg.hostPort(), err)
	}

	values, parseErr := tokenLineFields(stdout, serviceToken, 1)
	if parseErr != nil {
		if code != 0 {
			return false, fmt.Errorf("querying the service %q on %s: powershell exited with %d: %s",
				name, c.cfg.hostPort(), code, strings.TrimSpace(stderr))
		}
		return false, fmt.Errorf("reading the status of the service %q on %s: %w",
			name, c.cfg.hostPort(), parseErr)
	}

	status := values[0]
	if status == serviceStatusMissing {
		return false, nil
	}
	return status == serviceStatusRunning, nil
}

// serviceStatusScript builds the PowerShell that prints the numeric service
// status, or the missing marker. It is passed to the peer as -EncodedCommand,
// so the newlines and quotes below never meet cmd.exe.
func serviceStatusScript(name string) (string, error) {
	quoted, err := quotePowerShellLiteral(name)
	if err != nil {
		return "", err
	}
	missing := serviceToken + " " + strconv.Itoa(serviceStatusMissing)
	return strings.Join([]string{
		"$ErrorActionPreference = 'Stop'",
		"$s = Get-Service -Name " + quoted + " -ErrorAction SilentlyContinue",
		"if ($null -eq $s) { Write-Output '" + missing + "'; exit 0 }",
		"Write-Output ('" + serviceToken + " ' + [int]$s.Status)",
	}, "\n"), nil
}

// quotePowerShellLiteral renders s as a PowerShell single-quoted string, in
// which the only special character is the quote itself, doubled. Control
// characters are refused rather than escaped: no Windows service is named with
// one, so their presence means the caller passed something it should not have.
func quotePowerShellLiteral(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("sshx: empty PowerShell literal")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("sshx: refusing a PowerShell literal with a control character (%U)", r)
		}
	}
	return "'" + strings.ReplaceAll(s, "'", "''") + "'", nil
}
