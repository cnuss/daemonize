# CLAUDE.md

Orientation file for Claude Code sessions. Skim once; everything else
lives in the linked sources.

## What this is

`github.com/cnuss/daemonize` — wraps any
[`cobra`](https://github.com/spf13/cobra) command with Unix daemon
lifecycle controls (start / stop / status / reload). Library spec and
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
| `start` subcommand (fork + exec, `streamUntilReady`) | [`v1alpha1/start.go`](./v1alpha1/start.go)                         |
| `Stop` / `Status` / `Reload`, `computeStatus`        | [`v1alpha1/lifecycle.go`](./v1alpha1/lifecycle.go)                 |
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
  [`v1alpha1/start.go`](./v1alpha1/start.go)).
- **Positional args need an explicit `Args` validator.** Once
  start/stop/status are attached as children of the wrapped command,
  cobra rejects unknown positionals as missing subcommands. The
  library defaults `command.Args` to `cobra.ArbitraryArgs` when
  unset; set a stricter one if you want validation. The defaulting
  lives in `buildCobra`
  ([`v1alpha1/cobra.go`](./v1alpha1/cobra.go)).
- **godoc example funcs can't bind to generic types.** `go vet`
  rejects `ExampleDaemon_WithReload` in `v1` because `Daemon` is
  parameterized — its example checker hasn't caught up with
  generics. We work around it by using package-level example names
  (`Example_withReload`) instead. See
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

Spelled out in [CONTRIBUTING.md](./CONTRIBUTING.md). One-liner:

```sh
git switch -c <type>/<topic>
# ... edits, commit ...
git push -u origin <type>/<topic>
gh pr create --title "<type>: …" --body "Closes #<n>. …"
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
