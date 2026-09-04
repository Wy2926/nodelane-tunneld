# NodeLane Tunnel CLI Console and Interactive Startup Design

**Date:** 2026-09-04

**Status:** Approved for implementation

## Goal

Make the `nt` client readable and predictable in Linux terminals, Windows CMD, and Windows PowerShell. Replace handcrafted ANSI output with maintained terminal libraries, start an interactive form whenever a positional argument is missing, keep complete positional commands available for automation, and provide short parameter-free bootstrap commands for each supported shell.

## Confirmed Product Decisions

- Use Huh v2 for the interactive form and Lip Gloss v2 for semantic terminal styling.
- Replace the multiline ASCII logo with a single highlighted `NodeLane Tunnel` brand line.
- Keep `nt <http|tcp|udp> <host> <port>` as the complete non-interactive interface.
- Enter the interactive form whenever any positional argument is missing.
- Complete but invalid positional arguments fail immediately instead of opening the form.
- The protocol field is an arrow-key selection with HTTP selected by default.
- The host field explains that an empty value means `localhost`.
- The port field starts at `3000` and validates `1–65535` while the form is active.
- Bootstrap commands carry no tunnel arguments; the installed client opens the form.
- Windows has separate PowerShell and CMD commands. The CMD path must never invoke PowerShell.

## Argument and Interaction Model

Control commands such as `--help`, `--version`, and `languages` bypass the form.

Positional argument behavior is:

| Invocation | Behavior |
| --- | --- |
| `nt` | Open the full form with HTTP, `localhost`, and `3000` defaults. |
| `nt http` | Prefill HTTP and focus the host field. |
| `nt http localhost` | Prefill HTTP and `localhost`, then focus the port field. |
| `nt http localhost 3000` | Validate all fields and connect without opening the form. |
| Any complete but invalid triple | Print one concise error plus usage and exit nonzero. |
| More than three positional arguments | Print concise usage and exit nonzero. |

Existing positional values remain editable when the form opens. The first missing field receives focus. An empty host is normalized to `localhost`. The port default is `3000`; validation rejects non-numeric values and values outside `1–65535` before submission. Submitting the final valid field immediately begins connection without a separate confirmation screen.

The form runs inline rather than in the terminal alternate screen so previous shell output remains visible. `Ctrl+C` cancels cleanly, restores the terminal, and does not produce a red error. If arguments are incomplete and no interactive terminal is available, the client exits promptly with a localized message and the complete invocation form.

## Terminal Rendering Architecture

`consoleUI` remains the single semantic output boundary, but it no longer constructs ANSI escape sequences. It owns separately adapted stdout and stderr writers and a small set of Lip Gloss styles. Huh owns interactive field rendering through Bubble Tea.

The selected dependency versions are:

- `charm.land/huh/v2` v2.0.3
- `charm.land/lipgloss/v2` v2.0.6

The output adapter detects capabilities for stdout and stderr independently. It must:

- enable the library-supported Windows console path instead of assuming that a character device accepts ANSI;
- honor `NO_COLOR`, `CLICOLOR=0`, `TERM=dumb`, and `FORCE_COLOR`;
- emit plain text when the destination is not an interactive terminal;
- avoid cursor rewrites and spinners when output is redirected;
- never print a styled string through an unadapted writer.

Semantic styles are:

| Meaning | Style |
| --- | --- |
| Brand | bright cyan and bold |
| Progress step | cyan |
| Success | bright green and bold |
| Recoverable warning | yellow |
| Fatal error | bright red and bold |
| Labels, timestamps, help | dim or subdued |
| Public address | bright green and bold |
| HTTP GET | blue/cyan |
| HTTP POST, PUT, PATCH | yellow |
| HTTP DELETE | red |

The banner becomes one stable line containing `NodeLane Tunnel`. Terminal URLs are written as raw text, never Markdown links or backslash-escaped URLs.

Human-facing form content, progress, success, details, request logs, and recoverable warnings use stdout. Stderr is reserved for fatal failures and explicit FRP debug logging. This prevents PowerShell from presenting ordinary warnings as red pipeline errors.

## Error Classification and Noise Reduction

Expected shutdown errors such as `context.Canceled`, `net.ErrClosed`, and server-close sentinels are suppressed.

A real local-service forwarding failure remains visible as one yellow line. Consecutive identical failures are deduplicated; the suppression state resets after a successful forwarded request so a later regression is visible again. HTTP still returns a localized `502 Bad Gateway` response to the caller.

Input validation, installation failure, authentication failure, and failure to establish the tunnel are fatal. They use one red CLI line with an actionable cause. Shell installers forward the client exit code and do not wrap it in another exception or stack trace.

## Bootstrap Commands and Installers

The public commands are parameter-free:

Linux:

```sh
curl -fsSL https://tunnel.nodelane.net/run.sh | sh
```

PowerShell:

```powershell
irm https://tunnel.nodelane.net/run.ps1 | iex
```

CMD:

```bat
curl -fsSL https://tunnel.nodelane.net/run.cmd | cmd
```

`run.sh` and `run.ps1` continue to install or update the correct AMD64/ARM64 archive, verify its SHA-256 digest, maintain a versioned installation, install an `nt` launcher, and then run the client with no bootstrap arguments.

`run.cmd` is a short, linear, pipe-safe bootstrap. It uses only CMD and Windows inbox executables to download a full batch installer into `%TEMP%`, call it, preserve its exit code, and clean up. The full CMD installer uses `curl.exe`, `tar.exe`, and a Windows system hashing command; it never invokes PowerShell. The supported CMD baseline is a mainstream Windows 10/11 installation that includes `curl.exe` and `tar.exe`.

The PowerShell installer must not `throw` or call `exit` when the client exits nonzero. The native invocation leaves its code in `$LASTEXITCODE` after the CLI renders the error without terminating the user's current PowerShell session.

## Website and Documentation

The quick-start component keeps Linux and Windows as the top-level choices. Windows reveals a PowerShell/CMD selector. The protocol and port command builder controls are removed because protocol, host, and port are now selected in the CLI.

The copy button copies the literal shell command. The supporting line tells users that the next screen selects a protocol, an empty host means `localhost`, and the default port is `3000`. All twelve web locales and CLI locales receive the new copy.

README examples show the three parameter-free bootstrap commands first, then document the complete direct command for automation:

```text
nt http localhost 3000
```

## Testing and Acceptance

Implementation follows red-green-refactor. Tests cover:

- the full argument decision table, including missing arguments, prefill values, focus selection, invalid complete triples, and too many arguments;
- host normalization and the port boundaries `1`, `65535`, `0`, `65536`, empty, and non-numeric input;
- form configuration and validation independently from terminal rendering;
- brand and semantic output, separate stdout/stderr behavior, color fallback, and the absence of raw ANSI in plain mode;
- expected proxy error suppression, successful-request reset, and repeated-warning deduplication;
- installer download, checksum, architecture, launcher, and exit-code behavior where executable integration is practical;
- the `/run.sh`, `/run.ps1`, `/run.cmd`, and full CMD installer server routes;
- localized website command selection and a clean Astro check/build;
- Go tests, Windows native client execution, Windows cross-architecture builds, Linux builds, POSIX shell syntax, and CMD/PowerShell parser checks.

Existing SHA-256 verification and versioned rollback behavior are security requirements and may not be weakened to shorten a command.
