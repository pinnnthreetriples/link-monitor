---
name: agents
description: Link Monitor — hard rules, layout, and commands. Always loaded.
last_updated: 2026-09-07
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
  - *One documented exception:* `internal/ui` reaches the operating system
    directly. It runs `rundll32` to hand a link to the browser and `powershell`
    to raise a toast; `internal/ui/window` calls `user32` to create the
    program's own window, subclass its procedure, size it and set its icon, and
    reads one EdgeUpdate registry key to answer whether a window is possible on
    this machine at all. The rule those share is not "no syscalls" but *nothing
    the domain could have an opinion about*: a window, an icon and a
    notification are pixels, and an adapter that returned one would be an
    adapter that returned a UI. Each is also confined and faked — the
    unexported `runner` type for the two processes, the unexported `backend`
    interface for the window, a function seam for the registry read — and the
    decisions are lifted out of the syscalls so a build agent with no desktop
    can still test them. Anything the domain *does* have an opinion about — a
    process whose output is parsed, a socket, a service, a named kernel object
    — still belongs in an adapter, which is why the single-instance mutex lives
    in `internal/adapters/winlock` and not here. Do not spread `os/exec` beyond
    the `runner` type, and do not add a fourth kind of syscall to `ui` without
    re-reading this paragraph.
  - *One condition comes with that licence.* A `uintptr(unsafe.Pointer(p))`
    passed to a Win32 entry point must be written in the argument list of a
    function that carries `//go:uintptrescapes` — `LazyProc.Call` itself, or
    the package's own `callPtr`. That directive is the only thing that moves
    `p`'s target to the heap and keeps it alive for the syscall; route the
    conversion through an ordinary wrapper such as `call` and the compiler sees
    a plain integer, leaves the value on the goroutine's stack, and Windows
    writes through an address a stack growth is free to have abandoned. Neither
    `go vet` nor `gosec` reports it — the code looks like every other syscall,
    and the rule it breaks is about which function the conversion is written
    inside. Three of the four sites in `internal/ui/window` had it wrong until
    2026-09-07; `go build -gcflags=-m` naming the local as "moved to heap" is
    how a new site is checked.
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
  - *One exception, and it is narrow:* a pass-through that has nothing to add
    its caller does not already add must not wrap. `app.Probe`'s eight one-line
    delegations are the case — `core/diagnose`, their only caller, already
    prefixes each probe error with the question it was asking, in the same
    words the composite would have used, so wrapping there would print every
    message twice. The same goes for an unexported helper returning `ctx.Err()`
    straight to a caller that wraps it on the next line. Both exemptions live
    in `.golangci.yml`, keyed to those interfaces and that one signature; they
    are not a licence to stop wrapping anywhere else.
- **A suppression is an argument, not a switch.** `//nolint` always names its
  linter and its reason on the same line — `//nolint:gosec // G115: 1..255 by
  the caller's validation` — with the longer reasoning in the comment above it.
  Never a bare `//nolint`, never a whole file, and never a linter turned off in
  the config because it found something inconvenient. If the reason cannot be
  written down, the finding is real: fix the code.
- **Language.** Code, comments, commit messages and test names in English. Every
  string the user reads in the UI is in Russian.

## Layout

```
cmd/linkmon                entry point, wiring only
internal/core              pure domain logic — status model, diagnosis, fix planning
internal/core/foldersync   the shared folder's decisions: copy, skip or conflict — never delete
internal/core/clipshare    the shared clipboard's decisions: send, echo, or refuse
internal/adapters          tailscale, ssh, windows services — the only impure code
internal/adapters/winlock  the single-instance lock: one named mutex, one process
internal/adapters/localendpoint  where the running instance can be reached, for a second copy of the exe
internal/adapters/shellmenu      Explorer's right-click item, under HKCU only
internal/adapters/quickopen      opening a terminal, a folder or a desktop on the other machine
internal/adapters/syncfs         the shared folder's two trees: this one, and the peer's over SFTP
internal/adapters/clipboard      this machine's clipboard, and Windows' own do-not-record markers
internal/adapters/peerclip       handing an item to the peer's instance through a forwarded port
internal/app               orchestration: polling loop, state fan-out, actions
internal/ui                tray icon, menu, notifications
internal/ui/window         the program's own Win32 window, hosting frontend in WebView2
frontend                   HTML/CSS/JS lifted from the approved prototype
```

## Commands

- Build: `go build ./...`
- Test: `go test ./... -race -cover`
- Lint: `golangci-lint run` — the binary must be built with a Go toolchain at
  least as new as the one compiling this module, or its type-checking linters
  degrade silently and it reports "0 issues" while exiting 7. CI pins the
  version for exactly this reason; see the note in `ci.yml` before bumping Go.
- Format: `gofumpt -l -w .`
- Vulnerabilities: `govulncheck ./...`
- Everything CI runs: see `.github/workflows/ci.yml`

## Design source

The UI follows the approved prototype exactly: dark and light themes, tabs
Связь / Файлы / Порты / Настройки, the status rows, the drop zone. Do not
redesign it. The prototype is at
https://claude.ai/code/artifact/53a59df8-3f4a-4cf1-8312-8d699ffa0eb8.

Two changes to it were made deliberately and are not regressions. `--faint`
was raised because the prototype's value failed WCAG at about 3.1:1, with the
original kept in a comment. The typography is honest about a gap in the
approved design rather than papering over it: Space Grotesk has no Cyrillic
subset, so every Russian string has always rendered in the Segoe UI fallback
and only Latin runs — host names, port numbers — use it. Closing that gap
means either a Cyrillic display face or dropping Space Grotesk, and both are
the user's decisions, not an agent's.

## Scope

Agreed with the user, and now all of it is built: tray icon, diagnosis that
names the cause, one-click fix where one exists, Taildrop file transfer, the
24-hour history strip, port forwarding (`ssh -L` plus a `tailscale serve`
button), the program's own window with hide-to-tray, single-instance, the
Explorer right-click item («Отправить на ПК»), the quick actions (a terminal on
the peer, the shared folder, the peer's desktop), the shared folder, and the
shared clipboard.

Wake-on-LAN is out, by the user's decision.

Three deliberate absences, each for a reason worth keeping:

- **The shared folder never deletes.** A file removed on one machine stays on
  the other and may come back. `tools/gates` fails if any code in that feature
  so much as calls a removal function. See the eight rules the feature was
  built to, quoted in `internal/core/foldersync`.
- **The shared clipboard is off until the user turns it on**, carries bounded
  text and PNG screenshots, and honours Windows' own do-not-record markers,
  which is how a password manager says no. `tools/gates` fails if clipboard
  content can reach a log or a file.
- **There is no «Открыть папку ПК»**, because the peer publishes no share for
  it and Explorer would hang for 13 seconds before failing — measured, not
  assumed. The shared folder already mirrors that folder, so the honest button
  is the one that opens the local copy.

One feature needs the program running on **both** machines: the shared
clipboard, because a clipboard belongs to an interactive Windows session and is
unreachable over SSH. Everything else rides `sshd`, which is a service, so the
diagnosis, the shared folder and the Explorer item all work while the other
machine shows nobody a window.
