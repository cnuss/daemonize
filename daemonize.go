// Package daemonize wraps a cobra command with Unix daemon lifecycle controls —
// start, stop, status, and reload — by re-execing the binary as a detached
// background process. The wrapped command runs in the foreground; the
// daemon manages backgrounding, a pid file, log streaming during startup and
// shutdown, and signal-based readiness, all without mutating the command.
//
// The package is split across several files for navigability — they all
// belong to the same `daemonize` package:
//
//   - api.go        — public Daemon[T] interface and StatusResult.
//   - impl.go       — DaemonImpl[T] struct and the With* / DetachOn builders.
//   - cobra.go      — buildCobra wires lifecycle subcommands onto the
//     caller's *cobra.Command.
//   - start.go      — start subcommand: fork + exec the detached child,
//     stream its startup log.
//   - lifecycle.go  — Stop / Status / Reload runtime methods.
//   - accessors.go  — IsAlive / PIDFile / LogFile / Name / PID / writePID.
//   - util.go       — shared helpers (state-file paths, env-var derivation,
//     log tailing).
package daemonize

import "time"

const (
	// stopPollEach is how often Stop checks whether the child has exited.
	stopPollEach = 100 * time.Millisecond

	// defaultGroupID is the cobra group ID for the lifecycle subcommands;
	// defaultGroupName is its title (a ":" is appended on render).
	defaultGroupID   = "daemonize"
	defaultGroupName = "Daemon Commands"
)
