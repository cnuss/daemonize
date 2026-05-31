# daemonize

[![Go Reference](https://pkg.go.dev/badge/github.com/cnuss/daemonize.svg)](https://pkg.go.dev/github.com/cnuss/daemonize)
[![Go Report Card](https://goreportcard.com/badge/github.com/cnuss/daemonize)](https://goreportcard.com/report/github.com/cnuss/daemonize)
[![CI](https://github.com/cnuss/daemonize/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/cnuss/daemonize/actions/workflows/ci.yml)
[![CodeQL](https://github.com/cnuss/daemonize/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/cnuss/daemonize/actions/workflows/codeql.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/cnuss/daemonize/badge)](https://scorecard.dev/viewer/?uri=github.com/cnuss/daemonize)
[![Latest release](https://img.shields.io/github/v/release/cnuss/daemonize?sort=semver)](https://github.com/cnuss/daemonize/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](./LICENSE)

`daemonize` wraps any [cobra](https://github.com/spf13/cobra) command with Unix
daemon lifecycle controls — `start`, `stop`, `status`, `reload` — by re-execing
the binary as a detached background process. The wrapped command runs in the
foreground; the daemon manages backgrounding, a pid file, log streaming during
startup and shutdown, and signal-based readiness, all without mutating the
command.

## Quick Start

Add the dependency, the `daemonize` import, and wrap `cmd.Execute()`:

```sh
go get github.com/cnuss/daemonize
```

```diff
 import (
 	...
+	"github.com/cnuss/daemonize"
 	"github.com/spf13/cobra"
 )

 func main() {
 	...
+	ready := make(chan struct{})

 	cmd := &cobra.Command{
 		...
 		RunE: func(cmd *cobra.Command, args []string) error {
 			fmt.Printf("hello %s\n", message)
+			close(ready)
 			<-cmd.Context().Done()
 			...
 		},
 	}
 	...
-	if err := cmd.Execute(); err != nil {
+	if err := daemonize.FromCobra(cmd).DetachOn(ready).Execute(); err != nil {
 		fmt.Fprintln(os.Stderr, "error:", err)
 		os.Exit(1)
 	}
 }
```

(Full source: [`examples/hello/main.go`](./examples/hello/main.go).)

### Before

A basic cobra command:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	var message string
	cmd := &cobra.Command{
		Use:   "hello",
		Short: "Say hello",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("hello %s\n", message)
			<-cmd.Context().Done()
			fmt.Println("stopping")
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "world", "who to greet")
	cmd.SetContext(ctx)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

```
$ ./hello --help
Say hello

Usage:
  hello [flags]

Flags:
  -h, --help             help for hello
  -m, --message string   who to greet (default "world")
```

### After

```
$ ./hello --help
Say hello

Usage:
  hello [flags]
  hello [command]

Daemon Commands:
  start       Start `hello` in the background
  status      Report whether `hello` is running
  stop        Stop the running `hello`

Additional Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command

Flags:
  -h, --help             help for hello
  -m, --message string   who to greet (default "world")

Use "hello [command] --help" for more information about a command.
```

The wrapped flags (`-m`) carry through to `start`, so `hello start -m there`
forwards exactly what the foreground worker would have seen:

```
$ ./hello help start
Start `hello` in the background

Usage:
  hello start [flags]

Flags:
  -h, --help             help for start
  -m, --message string   who to greet (default "world")
```

The command's own `Short` and flags stay as written; daemonize attaches the
lifecycle subcommands and wraps `RunE` to own the pid file. Then:

```
./hello start         # daemonize (streams startup output, detaches when ready)
./hello status        # running (pid N)
./hello stop          # SIGTERM (Ctrl+C escalates to SIGKILL)
./hello               # run the wrapped command in the foreground
./hello -m there      # forward -m to the foreground run
./hello start -m there  # forward -m to the daemonized run
```

Add `.WithReload(syscall.SIGHUP)` before `DetachOn` to register a `reload`
subcommand that signals the running process.

## Features

- **In-place enrichment**: `FromCobra(cmd).DetachOn(ready)` returns the same
  `*cobra.Command`, now with `start`/`stop`/`status` (and optionally `reload`)
  attached as subcommands. Running the command directly still invokes its
  original `RunE` (the foreground worker); the wrapped `RunE` owns the pid
  file and relays readiness, so `stop`/`status` work against foreground runs
  too.
- **Channel-based readiness relay**: the wrapped command closes a
  `chan struct{}` when bound/ready; the daemon translates that to `SIGUSR1`
  internally so the parent can stop streaming and detach. Opaque to the wrapped
  command — it never sees a signal.
- **Streaming**: `start` tails the child's log so the user sees real startup
  output until ready; `stop` tails it during graceful shutdown.
- **Ctrl+C handling**: `start` sends `SIGTERM` to the child on the first
  Ctrl+C and waits for it to exit; a second Ctrl+C escalates to `SIGKILL`.
  `stop` follows the same pattern: first interrupt is the implicit
  `SIGTERM`, second escalates to `SIGKILL`.
- **Per-daemon state files**: pid/log live under
  `<UserCacheDir>/.<command-name>/<base>.{pid,log}`. Override with `WithName`.
- **Help grouping**: lifecycle subcommands are grouped (`Daemon Commands:` by
  default). Customize or disable with `WithGroup`.
- **Nestable**: the enriched command can be mounted under a larger cobra
  tree — `start` re-execs along the full command path (`foo run …`).
- **Generic builder**: `Daemon[T]` is parameterized; today `T == *cobra.Command`
  via `FromCobra`. Future backends can plug in.

## Install

```sh
go get github.com/cnuss/daemonize
```

Module floor is `go 1.21` / `cobra v1.6.0`.

## API at a glance

```go
type Daemon[T any] interface {
    FromCobra(inner *cobra.Command) Daemon[*cobra.Command]
    DetachOn(detachSig <-chan struct{}) T  // terminal: builds and returns T

    WithReload(sig syscall.Signal) Daemon[T] // enables the "reload" subcommand
    WithName(name string) Daemon[T]          // override state-file base name
    WithGroup(name *string) Daemon[T]        // help-group title (nil = ungroup)

    // Runtime accessors / actions (usable without building the cobra tree)
    Stop() error
    Status() error
    Reload() error
    PID() (int, error)
    IsAlive() bool
    PIDFile() (string, error)
    LogFile() (string, error)
    Name() (string, error)
}

func NewDaemon() Daemon[any]                                       // untyped bootstrap
func FromCobra(command *cobra.Command) Daemon[*cobra.Command]      // shorthand
```

## Examples

Self-contained programs in [`./examples`](./examples):

| Example          | Demonstrates                                                 |
| ---------------- | ------------------------------------------------------------ |
| `hello`          | Smallest wiring (`FromCobra` + `DetachOn`).                  |
| `reload`         | `WithReload(SIGHUP)` and a worker that handles it.           |
| `named`          | `WithName("widget")` for custom pid/log file names.          |
| `grouped`        | `WithGroup(&"Lifecycle")` for a custom help-group title.     |
| `ungrouped`      | `WithGroup(nil)` to put lifecycle under Additional Commands. |
| `with-args`      | Flag + positional forwarding through `start` to the child.   |
| `slow-start`     | Streaming a multi-second startup until ready.                |
| `slow-shutdown`  | Streaming a multi-second graceful shutdown.                  |
| `start-error`    | Daemon detects a child that fails before signaling ready.    |
| `shutdown-error` | Daemon streams a failure during shutdown; still stops.       |
| `pid-cleanup`    | Worker exits early without signaling ready; pid file gone.   |
| `stubborn`       | Worker ignores SIGTERM; demonstrates Ctrl+C → SIGKILL.       |
| `subcommand`     | Daemon mounted under a larger cobra tree (e.g. `app run`).   |

Run one locally:

```
make run hello start
make run hello status
make run hello stop
```

## Testing

```
make test   # library unit tests (fast, in-package)
make e2e    # end-to-end harness: builds + drives every example binary
```

`make e2e` runs `go test -count=1 -v ./e2e`, with an isolated cache
(`HOME`/`XDG_CACHE_HOME`) per test so pid/log files never collide.

## Contributing

PRs welcome. See [CONTRIBUTING.md](./CONTRIBUTING.md) for the local dev
loop, release process, and what makes a good example.

## License

[MIT](./LICENSE)
