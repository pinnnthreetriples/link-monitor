---
name: agents
description: Link Monitor — hard rules, layout, and commands. Always loaded.
last_updated: 2026-09-06
---

# Link Monitor

Windows tray utility that watches the Tailscale + SSH link between two machines,
diagnoses why it broke, fixes what it can, sends files over Taildrop, and
forwards ports.

Known machines (real, used as defaults — never invent others):

| Role       | Windows name      | tailnet name          | address         | user |
|------------|-------------------|-----------------------|-----------------|------|
| laptop     | `DESKTOP-L9DJSE9` | `workspace-claude-pc` | `100.124.47.73` | `pnj` |
| work PC    | `WIN-STTM11D02RD` | `win-sttm11d02rd`     | `100.127.188.87`| `user` |

## Hard rules

- **Layers.** `cmd → ui → app → {core, adapters}`. `core` is pure: it imports
  nothing from `ui`, `app`, or `adapters`, and nothing from `os/exec`, `net`,
  `syscall`, or any Tailscale/SSH package. Enforced by `depguard`, not by hope.
- **The outside world lives in `adapters/` only.** Running a process, opening a
  socket, touching the registry or a Windows service — all of it belongs in an
  adapter. Everywhere else, take an interface.
- **Every adapter has an interface and a fake.** The interface is declared where
  it is *consumed* (`core` or `app`), not where it is implemented. Tests use the
  fake; no test may need a live tailnet, a real SSH server, or the network.
- **Tests accompany behavior changes.** `core` must stay at or above 90 %
  statement coverage. `ui` has no coverage floor — do not write hollow tests to
  raise a number.
- **`go test ./... -race` must pass.** This program runs several goroutines
  (poller, tray, window, transfers); a data race is a real defect, not noise.
- **Size limits.** Source file ≤ 400 lines, test file ≤ 700 lines, function ≤ 60
  lines, line ≤ 110 columns. A file that outgrows the limit wants splitting, not
  a bigger limit.
- **Never log or persist secrets**: private key material, key paths beyond their
  directory, auth keys, or the contents of transferred files.
- **Errors carry context.** Wrap with `fmt.Errorf("...: %w", err)`. Never discard
  an error with `_`; if it truly cannot matter, say why in a comment.
- **Language.** Code, comments, commit messages and test names in English. Every
  string the user reads in the UI is in Russian.

## Layout

```
cmd/linkmon         entry point, wiring only
internal/core       pure domain logic — status model, diagnosis, fix planning
internal/adapters   tailscale, ssh, windows services — the only impure code
internal/app        orchestration: polling loop, state fan-out, actions
internal/ui         tray icon and window
frontend            HTML/CSS/JS lifted from the approved prototype
```

## Commands

- Build: `go build ./...`
- Test: `go test ./... -race -cover`
- Lint: `golangci-lint run`
- Format: `gofumpt -l -w .`
- Vulnerabilities: `govulncheck ./...`
- Everything CI runs: see `.github/workflows/ci.yml`

## Design source

The UI follows the approved prototype exactly: dark and light themes, tabs
Связь / Файлы / Настройки, the status rows, the drop zone. Do not redesign it.
Scope agreed with the user: tray icon, diagnosis, one-click fix, Taildrop file
transfer, Explorer context menu, quick actions, 24-hour history strip, port
forwarding (`ssh -L`, plus a `tailscale serve` button). Wake-on-LAN is out.
