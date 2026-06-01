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
2. [`api.go`](./api.go) — public `Daemon[T]` interface + `StatusResult`
3. [`examples/hello/main.go`](./examples/hello/main.go) — minimal call
   site

## Where to find things

The library is split across one file per concern, all in package
`daemonize`. Deep-link to filenames; line numbers will drift.

| Topic                       | Source                                                       |
| --------------------------- | ------------------------------------------------------------ |
| Package doc + constants     | [`daemonize.go`](./daemonize.go)                             |
| Public `Daemon[T]` + `StatusResult` | [`api.go`](./api.go)                                 |
| Builder methods, `DaemonImpl[T]` struct | [`impl.go`](./impl.go)                           |
| Cobra wiring (`buildCobra`, `ensurePid`, `--output` flag) | [`cobra.go`](./cobra.go) |
| `start` subcommand (fork + exec, stream until ready) | [`start.go`](./start.go)        |
| `Stop` / `Status` / `Reload`, `computeStatus` | [`lifecycle.go`](./lifecycle.go)      |
| `IsAlive` / `PIDFile` / `LogFile` / `Name` / `PID` / `writePID` | [`accessors.go`](./accessors.go) |
| State-file paths, env-var derivation, log tail | [`util.go`](./util.go)              |
| Build / lint / test commands | [`Makefile`](./Makefile)                                     |
| Release + skip-release regex | [`.github/workflows/ci.yml`](./.github/workflows/ci.yml)     |
| CodeQL scan                 | [`.github/workflows/codeql.yml`](./.github/workflows/codeql.yml) |
| OpenSSF Scorecard scan      | [`.github/workflows/scorecard.yml`](./.github/workflows/scorecard.yml) |
| Dependabot config           | [`.github/dependabot.yml`](./.github/dependabot.yml)         |
| Cosign verification recipe  | [`SECURITY.md`](./SECURITY.md)                               |
| Dev loop + release docs     | [`CONTRIBUTING.md`](./CONTRIBUTING.md)                       |
| Worked examples             | [`examples/`](./examples)                                    |
| e2e harness + runner        | [`e2e/e2e_test.go`](./e2e/e2e_test.go)                       |
| godoc examples              | [`example_test.go`](./example_test.go)                       |

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
  [`lifecycle.go`](./lifecycle.go) and the start-interrupt branch in
  [`start.go`](./start.go)).
- **Positional args need an explicit `Args` validator.** Once
  start/stop/status are attached as children of the wrapped command,
  cobra rejects unknown positionals as missing subcommands. The
  library defaults `command.Args` to `cobra.ArbitraryArgs` when
  unset; set a stricter one if you want validation. The defaulting
  lives in `buildCobra` ([`cobra.go`](./cobra.go)).
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
