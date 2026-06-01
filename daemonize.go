// Package daemonize wraps a cobra command with Unix daemon lifecycle controls —
// start, stop, status, and reload — by re-execing the binary as a detached
// background process. The wrapped command runs in the foreground; the
// daemon manages backgrounding, a pid file, log streaming during startup and
// shutdown, and signal-based readiness, all without mutating the command.
//
// The package is split into three pieces:
//
//   - daemonize (this package) — thin façade exposing NewDaemon and
//     FromCobra. Stable surface for application code.
//   - github.com/cnuss/daemonize/v1 — the stable Daemon[T] interface and
//     StatusResult type. Application code that wants to declare types
//     against the interface imports this.
//   - github.com/cnuss/daemonize/v1alpha1 — the current implementation.
//     Internals (DaemonImpl, helpers, cobra wiring) may change between
//     alpha revisions; pin only if you need direct access to the struct.
package daemonize

import (
	"github.com/cnuss/daemonize/v1"
	"github.com/cnuss/daemonize/v1alpha1"
	"github.com/spf13/cobra"
)

// NewDaemon returns an unconfigured builder. Call FromCobra to wrap a command.
func NewDaemon() v1.Daemon[any] {
	return v1alpha1.New[any]()
}

// FromCobra is a shorthand for NewDaemon().FromCobra(command). Use it when you
// already know you are wrapping a cobra command and don't need the untyped
// Daemon[any] bootstrap:
//
//	cmd := daemonize.FromCobra(serve).WithReload(syscall.SIGHUP).DetachOn(ready)
func FromCobra(command *cobra.Command) v1.Daemon[*cobra.Command] {
	return NewDaemon().FromCobra(command)
}
