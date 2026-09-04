# NodeLane Tunnel CLI Console and Interactive Startup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a cross-platform `nt` terminal experience with Huh interaction, Lip Gloss semantic output, concise errors, and parameter-free Linux, PowerShell, and CMD bootstrap commands.

**Architecture:** Separate positional draft preparation and Huh form construction from tunnel startup so argument decisions and validation are pure and testable. Keep `consoleUI` as the semantic rendering boundary, route styled text through Lip Gloss's capability-aware writer, and add a keyed warning gate for proxy noise. Serve a pipe-safe CMD bootstrap separately from a full CMD installer while preserving versioned, checksummed installation.

**Tech Stack:** Go 1.27, `charm.land/huh/v2` v2.0.3, `charm.land/lipgloss/v2` v2.0.6, POSIX `sh`, PowerShell 5.1/7, Windows CMD, Astro 7, TypeScript 5.9.

**Spec:** `docs/superpowers/specs/2026-09-04-nt-console-interaction-design.md`

## Global Constraints

- Direct invocation is exactly `nt <http|tcp|udp> <host> <port>`; do not preserve protocol/port shorthand.
- Any missing positional argument opens the form; a complete invalid triple fails without opening it.
- A missing host is an empty field with `localhost` help and normalizes to `localhost`; a missing port starts at `3000`.
- CMD bootstrap and installer never invoke PowerShell.
- Human status and recoverable warnings use stdout; only fatal errors and requested FRP diagnostics use stderr.
- Preserve SHA-256 verification, versioned installation, launcher indirection, and one previous version for rollback.
- All twelve existing CLI and website locales remain buildable and receive the new copy.

## File Responsibility Map

- `cmd/nt/target_form.go`: argument drafts, validation, Huh form construction and execution.
- `cmd/nt/main.go`: command dispatch and tunnel lifecycle.
- `cmd/nt/console.go`: terminal adaptation, Lip Gloss styles, semantic output and warning gate.
- `cmd/nt/target_form_test.go`, `console_test.go`, `monitor_test.go`: behavior-level regression tests.
- `cmd/nt/i18n*.go`: localized form and validation messages.
- `internal/server/assets/run.cmd`: pipe-safe CMD bootstrap.
- `internal/server/assets/install.cmd`: full native CMD installer.
- `internal/server/assets/run.ps1`, `run.sh`: other bootstrap paths.
- `internal/server/server.go`, `server_test.go`: script routes and embedded website assertions.
- `web/src/components/QuickStart.astro`, `web/src/i18n/**`: parameter-free command selector.
- `internal/server/assets/web`: verified Astro build output.
- `README.md`: public bootstrap and direct invocation documentation.

---

### Task 1: Positional Draft and Validation

**Files:**
- Create: `cmd/nt/target_form.go`
- Create: `cmd/nt/target_form_test.go`
- Modify: `cmd/nt/main.go`

**Interfaces:**
- Produces `targetFormValues`, `prepareTarget(args, ui)`, and `targetFormValues.target(ui)`.

- [ ] **Step 1: Write the failing decision-table tests.**

Use literal expectations:

```go
tests := []struct {
    args []string
    want targetFormValues
    interactive bool
}{
    {nil, targetFormValues{Protocol: "http", Host: "", Port: "3000", Focus: targetProtocol}, true},
    {[]string{"http"}, targetFormValues{Protocol: "http", Host: "", Port: "3000", Focus: targetHost}, true},
    {[]string{"http", "localhost"}, targetFormValues{Protocol: "http", Host: "localhost", Port: "3000", Focus: targetPort}, true},
    {[]string{"http", "localhost", "3000"}, targetFormValues{Protocol: "http", Host: "localhost", Port: "3000"}, false},
}
```

Also test a fourth argument and complete invalid protocol, host and port triples.

- [ ] **Step 2: Run `go test ./cmd/nt -run 'TestPrepareTarget|TestTargetFormValues' -count=1`.**

Expected: compilation fails because the new types and functions do not exist.

- [ ] **Step 3: Add the pure input model.**

```go
type targetField uint8
const ( targetProtocol targetField = iota; targetHost; targetPort )
type targetFormValues struct { Protocol, Host, Port string; Focus targetField }
```

`prepareTarget` copies up to three positions, supplies display defaults, returns interactive for lengths zero through two, and validates a length-three input via `target()`. `target()` lowercases protocol, normalizes empty host to `localhost`, and uses existing host/port parsers. Make `argumentsNeedPrompt` return `len(args) < 3` and remove all shorthand branches.

- [ ] **Step 4: Rerun the focused tests and then `go test ./cmd/nt -count=1`; both must pass.**

- [ ] **Step 5: Commit with `git commit -m "refactor: define nt target input model"`.**

### Task 2: Huh Form and Localized Interaction

**Files:**
- Modify: `go.mod`, `go.sum`, `cmd/nt/target_form.go`, `cmd/nt/target_form_test.go`, `cmd/nt/main.go`
- Modify: `cmd/nt/i18n.go`, all twelve `cmd/nt/i18n_catalog_*.go` files, `cmd/nt/i18n_test.go`

**Interfaces:**
- Produces `buildTargetForm(values *targetFormValues, ui *consoleUI) *huh.Form`.
- Produces `runTargetForm(values *targetFormValues, input io.Reader, output io.Writer, ui *consoleUI) error`.
- Produces sentinel `errTargetFormCanceled`.

- [ ] **Step 1: Add `charm.land/huh/v2@v2.0.3` and `charm.land/lipgloss/v2@v2.0.6` with `go get`.**

- [ ] **Step 2: Write failing tests for form focus, defaults, empty-host normalization and port values `1`, `65535`, `0`, `65536`, empty and `abc`.**

- [ ] **Step 3: Run `go test ./cmd/nt -run 'TestBuildTargetForm|TestTargetForm' -count=1` and observe the missing-form failure.**

- [ ] **Step 4: Build the form with these fields.**

```go
protocol := huh.NewSelect[string]().Key("protocol").Title(ui.text(msgChooseProtocol)).
    Options(huh.NewOption("HTTP", "http"), huh.NewOption("TCP", "tcp"), huh.NewOption("UDP", "udp")).
    Value(&values.Protocol).Height(3)
host := huh.NewInput().Key("host").Title(ui.text(msgLocalAddressPrompt)).
    Description(ui.text(msgLocalAddressDefaultHelp)).Placeholder("localhost").Value(&values.Host).
    Validate(func(value string) error { return validateOptionalHost(value, ui) })
port := huh.NewInput().Key("port").Title(ui.text(msgPortPrompt)).
    Description(ui.text(msgPortRangeHelp)).Value(&values.Port).CharLimit(5).
    Validate(func(value string) error { return validatePortText(value, ui) })
```

Create one group, use `WithInput`, `WithOutput(ui.out)`, `WithTheme(nodeLaneFormTheme())`, `WithShowErrors(true)`, and `WithShowHelp(true)`. Move initial focus to `values.Focus` before `Run`. Map `huh.ErrUserAborted` to `errTargetFormCanceled`; `run` treats that sentinel as a quiet successful exit.

- [ ] **Step 5: Add localized IDs for host-default help, port range, navigation and form failure to every catalog, then extend catalog completeness tests.**

- [ ] **Step 6: Run the form/catalog tests and `go test ./cmd/nt -count=1`; both must pass.**

- [ ] **Step 7: Commit with `git commit -m "feat: add interactive nt target form"`.**

### Task 3: Lip Gloss Console and Windows Adaptation

**Files:**
- Create: `cmd/nt/console_test.go`
- Modify: `cmd/nt/console.go`, `cmd/nt/main.go`

**Interfaces:**
- Produces `consoleStyles`, `nodeLaneFormTheme() huh.Theme`, `warningOnce`, and `resetWarning` while preserving existing semantic UI methods.

- [ ] **Step 1: Write failing tests using separate stdout/stderr buffers.** Assert one brand line, warning on stdout, fatal error on stderr, no ESC under `NO_COLOR=1`, and styled output under `FORCE_COLOR=1`.

- [ ] **Step 2: Run `go test ./cmd/nt -run 'TestConsole|TestBanner' -count=1`.** Expected: old warning stream and multiline banner fail.

- [ ] **Step 3: Delete `brandArt`, `paint`, and numeric ANSI codes.** Define semantic styles from `lipgloss.BrightCyan`, `BrightGreen`, `BrightYellow`, `BrightRed`, `BrightBlack`, `BrightBlue`, and write final strings only through `lipgloss.Fprint/Fprintf/Fprintln`.

- [ ] **Step 4: In `newConsoleUI`, call `lipgloss.EnableLegacyWindowsANSI` for `*os.File` stdout and stderr.** Keep cursor-line replacement only for an interactive stdout with color enabled; redirected stats become stable lines.

- [ ] **Step 5: Build `nodeLaneFormTheme` from `huh.ThemeBase`: bright-cyan focus/selector/cursor, bright-red validation, subdued help and bright-green completed values.** Do not enable alternate-screen rendering.

- [ ] **Step 6: Run console tests and `go test ./cmd/nt -count=1`; both must pass.**

- [ ] **Step 7: Commit with `git commit -m "feat: render nt output with lip gloss"`.**

### Task 4: Proxy Warning Classification and Deduplication

**Files:**
- Modify: `cmd/nt/console.go`, `cmd/nt/console_test.go`, `cmd/nt/monitor.go`, `cmd/nt/monitor_test.go`

**Interfaces:**
- Produces `expectedForwardingError(err error) bool`, `warningOnce(key, message)`, and `resetWarning(key)`.

- [ ] **Step 1: Write failing warning-gate tests.** Two warnings with key `http-upstream` produce one line; after reset the next warning produces a second line. `context.Canceled` and `net.ErrClosed` classify as expected.

- [ ] **Step 2: Add a real proxy test.** Two requests to an unavailable upstream receive 502 but produce one warning; a successful forwarded request resets the gate so a later failure produces another warning.

- [ ] **Step 3: Run `go test ./cmd/nt -run 'TestWarningGate|TestExpectedForwardingError|TestHTTPMonitorDeduplicates' -count=1` and observe failure.**

- [ ] **Step 4: Store warning keys under `consoleUI.mu`.** Suppress expected errors in the reverse-proxy handler; otherwise call `warningOnce("http-upstream", message)`. Capture response status and reset only after a non-5xx forwarded response.

- [ ] **Step 5: Run focused monitor tests and `go test ./... -count=1`; both must pass.**

- [ ] **Step 6: Commit with `git commit -m "fix: suppress expected tunnel warnings"`.**

### Task 5: Native CMD Bootstrap and Script Routes

**Files:**
- Create: `internal/server/assets/run.cmd`, `internal/server/assets/install.cmd`
- Modify: `internal/server/assets/run.ps1`, `internal/server/server.go`, `internal/server/server_test.go`

**Interfaces:**
- Serves `GET /run.cmd` as the public pipe-safe bootstrap and `GET /install.cmd` as its full native installer.

- [ ] **Step 1: Extend the route tests with both CMD paths.** Assert status 200, text MIME, inline disposition, no-index header and nonempty body. Execute `/run.cmd` through `cmd /d /q` with `NT_INSTALL_URL` and a fake `curl.exe`/installer in a temporary directory; assert installer exit code 23 is preserved.

- [ ] **Step 2: Run `go test ./internal/server -run 'TestRunScript|TestCMD' -count=1`.** Expected: new routes return frontend/404 behavior and fail.

- [ ] **Step 3: Add the pipe-safe bootstrap.**

```bat
@echo off
set "NT_BOOTSTRAP=%TEMP%\nodelane-tunnel-%RANDOM%.cmd"
if not defined NT_INSTALL_URL set "NT_INSTALL_URL=https://tunnel.nodelane.net/install.cmd"
curl.exe -fsSL "%NT_INSTALL_URL%" -o "%NT_BOOTSTRAP%"
if errorlevel 1 exit /b %errorlevel%
call "%NT_BOOTSTRAP%"
set "NT_EXIT=%errorlevel%"
del /q "%NT_BOOTSTRAP%" >nul 2>&1
exit /b %NT_EXIT%
```

- [ ] **Step 4: Implement `install.cmd`.** Detect AMD64/ARM64; install below `%LOCALAPPDATA%\nodelane`; fetch `stable.txt`; download `nt_<version>_windows_<arch>.zip`; compare the `.sha256` token with `certutil -hashfile`; extract with `tar.exe`; verify `nt.exe --version`; atomically update `current`; write the forwarding `nt.cmd`; update `HKCU\Environment\Path` without duplication; keep the prior version; invoke the client with `%*`. All failures use one `:fail` block and a nonzero exit, never PowerShell.

- [ ] **Step 5: Replace PowerShell's final client `throw` with capture of `$LASTEXITCODE` followed by `exit $clientExitCode`.** Installation failures remain terminating.

- [ ] **Step 6: Register both GET routes, map embedded filenames/MIME, and include both in the no-index header condition.**

- [ ] **Step 7: Run server tests, parse `run.ps1` with `[scriptblock]::Create`, and invoke `install.cmd` with `NT_RELEASE_BASE=http://127.0.0.1:1` to verify one concise controlled failure.**

- [ ] **Step 8: Commit with `git commit -m "feat: add native cmd tunnel installer"`.**

### Task 6: Parameter-Free Website and Documentation

**Files:**
- Modify: `web/src/components/QuickStart.astro`, `web/src/i18n/types.ts`, all twelve `web/src/i18n/locales/*.ts`, `README.md`
- Replace generated output: `internal/server/assets/web`
- Modify: `internal/server/server_test.go`

**Interfaces:**
- Quick-start state becomes `{ os: "linux" | "windows"; shell: "powershell" | "cmd" }`.
- Translation replaces protocol-specific quick-start fields with `shellLabel` and `afterRun`.

- [ ] **Step 1: Change the translation type first.**

```ts
quickstart: {
  ariaLabel: string; title: string; subtitle: string; osLabel: string; shellLabel: string;
  copy: string; copied: string; selected: string; anonymous: string; afterRun: string;
};
```

- [ ] **Step 2: Run `pnpm --dir web check`.** Expected: component and locale objects fail against the new type.

- [ ] **Step 3: Implement exact command literals.**

```ts
const commands = {
  linux: "curl -fsSL https://tunnel.nodelane.net/run.sh | sh",
  powershell: "irm https://tunnel.nodelane.net/run.ps1 | iex",
  cmd: "curl -fsSL https://tunnel.nodelane.net/run.cmd | cmd",
} as const;
```

Always show Linux/Windows; show PowerShell/CMD only for Windows; render `$`, `PS>`, or `>`; copy plain text. Replace forwarding preview with localized guidance: select protocol next, empty host uses `localhost`, port defaults to `3000`.

- [ ] **Step 4: Update all locale objects with native subtitle, shell-label and after-run copy.** Keep protocol cards unchanged because they document direct `nt` usage.

- [ ] **Step 5: Update README with the three public commands and `nt http localhost 3000`; remove shorthand and bootstrap-appended arguments.**

- [ ] **Step 6: Run `pnpm --dir web build`, safely replace only `internal/server/assets/web` with `web/dist`, then run server tests.**

- [ ] **Step 7: Assert served `/` and `/zh-cn/` HTML contain all three raw commands and omit `sh -s --`, `[scriptblock]::Create`, and installer commands ending in `http localhost 3000`.**

- [ ] **Step 8: Commit with `git commit -m "feat: simplify tunnel quick start commands"`.**

### Task 7: Full Verification

**Files:**
- Modify only files above if a reproduced failure requires a test-first correction.

- [ ] **Step 1: Run `gofmt -w cmd/nt internal/server`, `git diff --check`, and inspect `git status --short`.**

- [ ] **Step 2: Run `go test ./... -count=1` and `pnpm --dir web build`; require exit 0 and zero Astro errors.**

- [ ] **Step 3: Build `cmd/nt` with CGO disabled for windows/amd64, windows/arm64, linux/amd64, and linux/arm64 into a temporary directory.**

- [ ] **Step 4: Run `sh -n internal/server/assets/run.sh`, parse `run.ps1`, and execute the controlled-unreachable CMD installer check.**

- [ ] **Step 5: Run native `nt.exe --version` and `--help` from PowerShell and `cmd /d /c`, repeat help under `NO_COLOR=1`, and scan captured bytes for ESC.**

- [ ] **Step 6: Re-read every approved spec requirement and map it to a passing test or inspected runtime output. Any gap becomes a failing regression test before a correction.**

- [ ] **Step 7: If verification required corrections, commit them as `fix: complete nt console verification`; otherwise leave the verified commits unchanged.**
