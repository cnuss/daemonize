# Security Policy

## Supported Versions

`daemonize` is pre-1.0 (`0.x`). Only the latest tagged release receives
security fixes. Once `1.x` ships, the support window will move to "latest
minor" of the current major.

| Version | Supported          |
| ------- | ------------------ |
| latest `0.x` | :white_check_mark: |
| older `0.x`  | :x:                |

## Reporting a Vulnerability

Please report security vulnerabilities **privately** via GitHub's
[private vulnerability reporting](https://github.com/cnuss/daemonize/security/advisories/new)
on the Security tab. That opens a draft advisory only the maintainers can see.

Please do **not** open a public issue for a suspected vulnerability.

### What to include

- A clear description of the issue and its impact.
- Steps to reproduce (a minimal example, version/commit, OS).
- Whether the issue is exploitable with default configuration.
- Any suggested fix or mitigation, if you have one.

### Expectations

- Acknowledgement within 7 days.
- A status update within 30 days, including a plan and rough timeline.
- A coordinated disclosure once a fix or workaround is available; we will
  credit you in the advisory unless you ask otherwise.

## Verifying releases

Source archives for every tagged release are signed with
[cosign](https://github.com/sigstore/cosign) in keyless mode (Sigstore
Fulcio cert, Rekor transparency log). Each release ships four extra
assets alongside the source archives:

- `vX.Y.Z.tar.gz.sig` / `vX.Y.Z.tar.gz.pem`
- `vX.Y.Z.zip.sig` / `vX.Y.Z.zip.pem`

To verify:

```sh
TAG=v0.1.7
REPO=cnuss/daemonize

curl -fsSL "https://github.com/${REPO}/archive/refs/tags/${TAG}.tar.gz"           -o "${TAG}.tar.gz"
curl -fsSL "https://github.com/${REPO}/releases/download/${TAG}/${TAG}.tar.gz.sig" -o "${TAG}.tar.gz.sig"
curl -fsSL "https://github.com/${REPO}/releases/download/${TAG}/${TAG}.tar.gz.pem" -o "${TAG}.tar.gz.pem"

cosign verify-blob \
  --certificate           "${TAG}.tar.gz.pem" \
  --signature             "${TAG}.tar.gz.sig" \
  --certificate-identity-regexp '^https://github.com/cnuss/daemonize/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  "${TAG}.tar.gz"
```

`Verified OK` means the archive matches what the release workflow signed.

## Scope

In-scope: anything in this repository's library code (`daemonize.go`,
`example_test.go`) or its release artifacts.

Out of scope: vulnerabilities in `cobra` or the Go standard library itself
(report those to their respective projects), and issues that require an
attacker to already have local execution as the same user.
