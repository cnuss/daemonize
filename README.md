# daemonize

[![Go Reference](https://pkg.go.dev/badge/github.com/cnuss/daemonize.svg)](https://pkg.go.dev/github.com/cnuss/daemonize)

`daemonize` wraps any [cobra](https://github.com/spf13/cobra) command with Unix
daemon lifecycle controls — `start`, `stop`, `status`, `reload` — by re-execing
the binary as a detached background process. The wrapped command runs in the
foreground; the daemon manages backgrounding, a pid file, log streaming during
startup and shutdown, and signal-based readiness, all without mutating the
command.

## Features

- **Mutation-free**: the caller's `*cobra.Command` is wrapped, never modified
  (`RunE`, flags, `Use`, etc. all untouched).
- **Channel-based readiness relay**: the wrapped command closes a
  `chan struct{}` when bound/ready; the daemon translates that to `SIGUSR1`
  internally so the parent can stop streaming and detach. Opaque to the wrapped
  command — it never sees a signal.
- **Streaming**: `start` tails the child's log so the user sees real startup
  output until ready; `stop` tails it during graceful shutdown.
- **Ctrl+C handling**: `start` cancels (kills the child) if interrupted
  mid-startup; `stop` escalates to `SIGKILL` on interrupt.
- **Per-daemon state files**: pid/log live under
  `<UserCacheDir>/.<command-name>/<base>.{pid,log}`. Override with `WithName`.
- **Help grouping**: lifecycle subcommands are grouped (`Daemon Commands:` by
  default). Customize or disable with `WithGroup`.
- **Nestable**: the assembled root command can be mounted under a larger cobra
  tree — `start` re-execs along the full command path (`foo run serve …`).
- **Generic builder**: `Daemon[T]` is parameterized; today `T == *cobra.Command`
  via `FromCobra`. Future backends can plug in.

## Install

```sh
go get github.com/cnuss/daemonize
```

Module floor is `go 1.21` / `cobra v1.6.0`.

## Quick start

```go
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Run the server in the foreground",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ... slow startup work ...
			close(ready) // signal the daemon: I'm up

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
			<-stop
			return nil
		},
	}

	cmd := daemonize.FromCobra(serve).
		WithReload(syscall.SIGHUP).
		DetachOn(ready)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
```

Then:

```
./app start       # daemonize (streams startup output, detaches when ready)
./app status      # running (pid N)
./app reload      # SIGHUP to the running process
./app stop        # SIGTERM, escalating to SIGKILL on timeout
./app serve       # run the wrapped command in the foreground
./app             # bare alias for "start"
```

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

| Example                          | Demonstrates                                                |
| -------------------------------- | ----------------------------------------------------------- |
| `minimal`                        | Smallest wiring (`FromCobra` + `DetachOn`).                 |
| `reload`                         | `WithReload(SIGHUP)` and a worker that handles it.          |
| `named`                          | `WithName("widget")` for custom pid/log file names.         |
| `grouped`                        | `WithGroup(&"Lifecycle")` for a custom help-group title.    |
| `ungrouped`                      | `WithGroup(nil)` to put lifecycle under Additional Commands.|
| `with-args`                      | Flag + positional forwarding through `start` to the child.  |
| `slow-start`                     | Streaming a multi-second startup until ready.               |
| `slow-shutdown`                  | Streaming a multi-second graceful shutdown.                 |
| `start-error`                    | Daemon detects a child that fails before signaling ready.   |
| `shutdown-error`                 | Daemon streams a failure during shutdown; still stops.      |
| `subcommand`                     | Daemon mounted under a larger cobra tree (e.g. `app run`).  |

Run one locally:

```
make run minimal start
make run minimal status
make run minimal stop
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
