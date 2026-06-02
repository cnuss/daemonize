# CLAUDE.md

Orientation file for Claude Code sessions. Skim once; everything else
lives in the linked sources.

## What this is

`github.com/cnuss/daemonize` — wraps any
[`cobra`](https://github.com/spf13/cobra) command with Unix daemon
lifecycle controls (start / stop / status). Library spec and
public API live in the [README](./README.md). Reading order for a
fresh session:

1. [README — Quick Start + API at a glance](./README.md)
2. [`v1/api.go`](./v1/api.go) — public `Daemon[T]` interface + `StatusResult`
3. [`examples/hello/main.go`](./examples/hello/main.go) — minimal call site

## Module layout

Three packages, Kubernetes-style versioning:

```
github.com/cnuss/daemonize           — root façade. Stable surface.
github.com/cnuss/daemonize/v1        — stable Daemon[T] interface + StatusResult.
github.com/cnuss/daemonize/v1alpha1  — current implementation. May change
                                       between alpha revisions.
```

Application code imports root (`daemonize.FromCobra(cmd)…`). Code that
needs to declare types against the interface imports `v1`. Direct
access to the `DaemonImpl[T]` struct lives in `v1alpha1`.

## Where to find things

Deep-link by filename; line numbers will drift.

| Topic                                                | Source                                                             |
| ---------------------------------------------------- | ------------------------------------------------------------------ |
| Façade (`NewDaemon`, `FromCobra`)                    | [`daemonize.go`](./daemonize.go)                                   |
| Stable interface (`Daemon[T]` + `StatusResult`)      | [`v1/api.go`](./v1/api.go)                                         |
| Implementation struct + `New[T]` constructor         | [`v1alpha1/impl.go`](./v1alpha1/impl.go)                           |
| Builder methods (`FromCobra`, `DetachOn`, `With*`)   | [`v1alpha1/builder.go`](./v1alpha1/builder.go)                     |
| Cobra wiring (`buildCobra`, `ensurePid`, `--output`) | [`v1alpha1/cobra.go`](./v1alpha1/cobra.go)                         |
| `start` subcommand (fork + exec, `startCobra`)       | [`v1alpha1/cobra.go`](./v1alpha1/cobra.go)                         |
| `streamUntilReady` + `startResult` enum              | [`v1alpha1/util.go`](./v1alpha1/util.go)                           |
| `Stop` (orchestrator)                                | [`v1alpha1/cobra.go`](./v1alpha1/cobra.go)                         |
| `Status` + `computeStatus`                           | [`v1alpha1/lifecycle.go`](./v1alpha1/lifecycle.go)                 |
| `IsAlive` / `PIDFile` / `LogFile` / `Name` / `PID`   | [`v1alpha1/accessors.go`](./v1alpha1/accessors.go)                 |
| State files, env-var derivation, log tail            | [`v1alpha1/util.go`](./v1alpha1/util.go)                           |
| Package constants                                    | [`v1alpha1/consts.go`](./v1alpha1/consts.go)                       |
| Build / lint / test commands                         | [`Makefile`](./Makefile)                                           |
| Release + skip-release regex                         | [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)           |
| CodeQL scan                                          | [`.github/workflows/codeql.yml`](./.github/workflows/codeql.yml)   |
| OpenSSF Scorecard scan                               | [`.github/workflows/scorecard.yml`](./.github/workflows/scorecard.yml) |
| Dependabot config                                    | [`.github/dependabot.yml`](./.github/dependabot.yml)               |
| Cosign verification recipe                           | [`SECURITY.md`](./SECURITY.md)                                     |
| Dev loop + release docs                              | [`CONTRIBUTING.md`](./CONTRIBUTING.md)                             |
| Worked examples                                      | [`examples/`](./examples)                                          |
| e2e harness + runner                                 | [`e2e/e2e_test.go`](./e2e/e2e_test.go)                             |
| godoc examples                                       | [`v1/example_test.go`](./v1/example_test.go)                       |
| In-package unit tests + fuzz target                  | [`v1alpha1/daemon_test.go`](./v1alpha1/daemon_test.go)             |

## Platform layer

`v1alpha1` ships a small `platform` interface (defined in
[`v1alpha1/cobra.go`](./v1alpha1/cobra.go)) with two impls — same
struct name `platformImpl` in both, distinguished by build tags:

- [`v1alpha1/platform.go`](./v1alpha1/platform.go) — `//go:build !windows`
- [`v1alpha1/platform_windows.go`](./v1alpha1/platform_windows.go) — `//go:build windows`

Every step that touches the OS — `applyDetachAttrs`, `installStartSignals`,
`waitForEarlyExit`, `killChildOnInterrupt`, `afterStartupReady`,
`pollStartupState`, `isAlive`, `notifyParentReady`, `installShutdownListener`,
and `stop` — is one method on this interface. `startCobra` and `Stop` in
`cobra.go` orchestrate them; they don't touch syscalls directly.

### Detach + readiness — side-by-side

| Step                  | Unix                                                                 | Windows                                                                                                              |
| --------------------- | -------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| Spawn detach mode     | `SysProcAttr{Setsid: true}` — new session, survives parent exit      | `SysProcAttr{CreationFlags: DETACHED_PROCESS \| CREATE_NEW_PROCESS_GROUP, HideWindow: true}` — no console, own group |
| Parent waits for ready | `signal.Notify(SIGUSR1/SIGCHLD/SIGINT/SIGTERM)` + `Wait4`            | Polls `<base>.ready` sentinel + `IsAlive(child)` every 100 ms, plus `signal.Notify(os.Interrupt)` for Ctrl+C cancel  |
| Child signals ready   | `syscall.Kill(os.Getppid(), syscall.SIGUSR1)`                        | `os.Create("<base>.ready")` next to the pid file                                                                     |
| Liveness probe        | `syscall.Kill(pid, 0)` (returns nil → alive)                         | `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` + `GetExitCodeProcess`; `STILL_ACTIVE (259)` means running          |

### Graceful shutdown — Windows uses a named pipe

Named-pipe path: `\\.\pipe\daemonize-<base>`. `<base>` matches the
state-file basename (`<base>.pid`, `<base>.log`, `<base>.ready`).

Flow:

1. **Child side, in the wrapped `RunE` relay** (Windows only): after
   `close(detachSig)` fires, the relay calls
   `d.platform.installShutdownListener(d.ctxCancel)` BEFORE
   `notifyParentReady`. `installShutdownListener` synchronously
   `CreateNamedPipe`s the pipe (so it's instantly connectable), then
   spawns a goroutine that `ConnectNamedPipe`s, reads one byte, and
   calls the cancel func. `d.ctxCancel` is whatever
   `signal.NotifyContext` returned from `WithShutdownSignal` — calling
   it cancels `cmd.Context()`, which surfaces as
   `<-cmd.Context().Done()` to the worker.
2. **Parent side, `platformImpl.stop`**: opens the same pipe as a
   client with `CreateFile` + `GENERIC_WRITE`, writes one byte. On
   success, polls `isAlive` indefinitely while the worker tears down,
   with `os.Interrupt` in the stop console escalating to
   `TerminateProcess` — same shape as the Unix SIGTERM+poll+escalate
   loop in `platform.go`'s `stop`.
3. **Pipe-write failure** (child crashed before listening, never
   configured `WithShutdownSignal`, pipe vanished): `stop` falls
   straight through to `TerminateProcess`. The child gets no defer
   execution along that path, same as a Unix SIGKILL.

Examples that demo lifecycle (`hello`, `slow-start`, `slow-shutdown`,
`shutdown-error`, `named`, `grouped`, `ungrouped`) all use
`WithShutdownSignal(os.Interrupt, syscall.SIGTERM)` +
`<-cmd.Context().Done()`. The raw `signal.Notify(stop, SIGTERM)`
pattern still works on Unix but bypasses the named-pipe trigger on
Windows — workers that want cross-platform graceful shutdown must
take the cancel-context route.

### Cross-platform Ctrl+C

The e2e harness's `runInterrupt` helper is now cross-platform:
`configureInterruptable` + `sendInterrupt` (in
[`e2e/interrupt_unix.go`](./e2e/interrupt_unix.go) and
[`e2e/interrupt_windows.go`](./e2e/interrupt_windows.go)) pick the
right primitive — `Process.Signal(syscall.SIGINT)` on Unix,
`GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT)` on Windows. The Windows
side spawns the target with `CREATE_NEW_PROCESS_GROUP` so the test
can address it by pid without taking down everything else attached
to the same console. `TestStartInterruptGraceful` and
`TestStopInterruptEscalates` exercise the same flow on both targets
via this helper.

The full e2e suite runs on both targets now: every test in
[`e2e/e2e_test.go`](./e2e/e2e_test.go) is cross-platform. The
`e2e_unix_test.go` file is gone. The two build-tagged shims
([`e2e/interrupt_unix.go`](./e2e/interrupt_unix.go),
[`e2e/interrupt_windows.go`](./e2e/interrupt_windows.go)) host the
small per-OS helpers — `configureInterruptable`, `sendInterrupt`,
`killPID`, `waitForExit` — that the cross-platform tests call into.

## Conventions agents miss

These are easy to get wrong from the diff alone — they're not in the
human-facing docs because human readers already have the muscle
memory.

- **`examples/` is intentionally duplicated.** Each `main.go` is a
  copy-pasteable starter; no shared internal package. Don't refactor
  it into one.
- **Don't use `select{}` to block a worker forever.** Once the
  goroutine running it is the only live one with no pending timers,
  Go's deadlock detector panics. Use `<-cmd.Context().Done()` (after
  `WithShutdownSignal(...)`) or `<-time.After(time.Hour)`. See
  [`examples/subcommand/main.go`](./examples/subcommand/main.go) and
  [`examples/stubborn/main.go`](./examples/stubborn/main.go).
- **The daemon child doesn't receive terminal SIGINT.** It runs in
  its own session via `SysProcAttr{Setsid: true}`. The parent gets
  Ctrl+C and explicitly `SIGTERM`s the child (see `Stop` in
  [`v1alpha1/lifecycle.go`](./v1alpha1/lifecycle.go) and the
  start-interrupt branch in
  [`v1alpha1/cobra.go`](./v1alpha1/cobra.go)).
- **Positional args need an explicit `Args` validator.** Once
  start/stop/status are attached as children of the wrapped command,
  cobra rejects unknown positionals as missing subcommands. The
  library defaults `command.Args` to `cobra.ArbitraryArgs` when
  unset; set a stricter one if you want validation. The defaulting
  lives in `buildCobra`
  ([`v1alpha1/cobra.go`](./v1alpha1/cobra.go)).
- **godoc example funcs can't bind to generic types.** `go vet`
  rejects `ExampleDaemon_WithName` (or any `ExampleDaemon_*`) in `v1`
  because `Daemon` is parameterized — its example checker hasn't
  caught up with generics. We work around it by using package-level
  example names (`Example_withName` etc.). See
  [`v1/example_test.go`](./v1/example_test.go).
- **Skip-release token must be line-anchored.** The regex is in
  [`ci.yml`](./.github/workflows/ci.yml) (`resolve tag` step):
  `^[[:space:]]*\[skip-release\][[:space:]]*$`. Inline prose mentions
  are safe; standalone-line in the commit body opts out.
- **Cosign / Scorecard tags are annotated.** `ossf/scorecard-action`
  publishes annotated tags; pinning the tag-object SHA fails Sigstore
  verification ("imposter commit"). Pin to the commit underneath
  (see existing entries in
  [`scorecard.yml`](./.github/workflows/scorecard.yml)).

## Branch / PR flow

**Every change starts with an issue.** No exceptions, including
retroactive cleanups — if you're refactoring something I noticed
mid-session, open the issue first, *then* the branch + PR. The PR
body always carries a `Closes #<n>` line so the merge auto-closes
the tracking issue and leaves a paper trail for future agents
reading `git log` or `gh issue list`.

Spelled out in [CONTRIBUTING.md](./CONTRIBUTING.md). One-liner:

```sh
gh issue create --title "…" --body "…"              # 1. issue first
git switch -c <type>/<topic>                        # 2. branch
# ... edits, commit ...
git push -u origin <type>/<topic>
gh pr create --title "<type>: …" --body "Closes #<n>. …"  # 3. PR refs the issue
# CI green ⇒
gh pr merge <pr#> --squash --delete-branch
```

`main` is protected (`ci (1.21)` + `ci (stable)` required; no
force-push). Pushing to main auto-bumps a patch tag and signs the
release — see the `Release` job in
[`ci.yml`](./.github/workflows/ci.yml).

## Don't

- Don't push directly to `main` for routine work. PR flow gives CI
  + auto-release a clean audit trail.
- Don't bump `cobra` past the floor in `go.mod` without a reason —
  Dependabot is configured to ignore non-major bumps
  ([`dependabot.yml`](./.github/dependabot.yml)) so the declared
  minimum doesn't drift up.
- Don't commit secrets. [`.gitignore`](./.gitignore) covers `.env*`,
  `.claude/`, etc.
