# Contributing

Thanks for considering a contribution! This document covers what you need to
know to send a PR.

## Local development

Requires Go 1.21 or later.

```sh
git clone https://github.com/cnuss/daemonize.git
cd daemonize
make test   # library unit tests (fast, in-package)
make e2e    # builds and drives every example binary
```

Run a specific example locally:

```sh
make run hello start
make run with-args start --port 9000
```

## Before you push

- `gofmt -w .`
- `go vet ./...`
- `make test`
- `make e2e`

CI runs the same on every PR.

## Adding an example

Examples live in `./examples/<name>/main.go`. Keep each example self-contained
(there's no shared internal package — the duplication is intentional, so each
example is copy-pasteable on its own).

Match the existing output conventions so the e2e harness can grep for them:

- Print `ready` (or `ready: <message>`) and `close(readyChan)` when up.
- Print `stopping` (or your own line) on SIGTERM if you handle it.
- Single `fmt.Println` lines are enough — assertions are substring-based.

Add a matching test to `e2e/e2e_test.go` (`TestYourExample`), and a row to
the README's example table.

## Pull requests

- Keep PRs focused. One feature or fix per PR.
- Include test coverage for behavior changes — lib tests (`daemonize_test.go`)
  for API changes, e2e tests (`e2e/e2e_test.go`) for example-visible changes.
- Update `README.md` if you change public API or add an example.
- Signed commits preferred. The repo enables commit signing locally; CI does
  not enforce signatures.

## Commit messages

Short subject (≤ 72 chars), imperative mood ("Add X", not "Added X").
Wrap body at ~72 cols. Explain the *why*; the diff covers the *what*.

## Releasing

Patch releases are automatic. Every push to `main` runs the `Release`
workflow, which bumps the patch component of the latest `v*` tag,
re-runs `go vet`, `go build`, `make test`, and `make e2e` against that
ref, then:

- pushes the new tag,
- creates a GitHub Release with auto-generated notes, and
- warms `proxy.golang.org` so [pkg.go.dev](https://pkg.go.dev/github.com/cnuss/daemonize)
  surfaces the new version without manual prodding.

To opt a commit out of the auto-bump, put `[skip-release]` on its own
line in the commit body. (It must be the only thing on its line, so
prose mentioning the token inline doesn't accidentally suppress.)

For a minor or major bump, tag locally and push the tag — the workflow
treats a manual tag as the version of record and skips the bump:

```sh
git tag v0.2.0
git push --tags
```

Tags must follow `vMAJOR.MINOR.PATCH` (Go module semver).

## License

By contributing you agree your contributions are licensed under the
[MIT License](./LICENSE).
