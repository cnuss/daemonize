package v1

import (
	"context"
	"os"

	"github.com/spf13/cobra"
)

// Daemon is the builder for a background-lifecycle wrapper around a foreground
// command. Configure it with the With* methods, then call DetachOn. Obtain one
// from FromCobra.
type Daemon[T any] interface {
	// FromCobra wraps a cobra command, retyping the builder to *cobra.Command so
	// Into returns the assembled root command.
	FromCobra(inner *cobra.Command) Daemon[*cobra.Command]
	// DetachOn assembles the lifecycle wrapper and returns it (as T). detachSig is
	// closed by the wrapped command once it is ready; the daemon relays that so
	// "start" stops streaming and detaches. Pass nil for no readiness relay.
	// After FromCobra, T is *cobra.Command (the root command to Execute).
	DetachOn(detachSig <-chan struct{}) T
	// WithName overrides the state-file base name. By default it is derived from
	// the wrapped command's path (e.g. "server-serve"); WithName("foo") yields
	// ".foo.pid"/".foo.log" instead.
	WithName(name string) Daemon[T]
	// WithGroup sets the lifecycle help group's title (a trailing ":" is added);
	// pass nil to ungroup (list them under Additional Commands). Unset, they are
	// grouped under "Daemon Commands:".
	WithGroup(name *string) Daemon[T]
	// WithContext sets the parent context that DetachOn installs on the
	// wrapped command. Pass context.Background() for the conventional
	// rooted context, or another context to inherit deadlines, values, or
	// cancellation from an outer scope.
	//
	// Pass nil to opt out of any context auto-wiring entirely: in that case
	// DetachOn will not call cmd.SetContext, and WithShutdownSignal is
	// ignored. The caller takes full responsibility for the command's
	// context, just as if neither method had been called.
	WithContext(parent context.Context) Daemon[T]
	// WithShutdownSignal installs a signal.NotifyContext that cancels the
	// command's context when any of the listed signals fire. DetachOn
	// composes it with the WithContext parent (or context.Background() if
	// WithContext is unset):
	//   ctx, cancel := signal.NotifyContext(parent, sigs...)
	//   cmd.SetContext(ctx)
	// The wrapped command can then use <-cmd.Context().Done() to react
	// without wiring signal.Notify itself. Stop, Status, and the wrapped
	// RunE all call cancel on exit so the underlying goroutine is released.
	// If WithContext(nil) was called, this is a no-op.
	WithShutdownSignal(sigs ...os.Signal) Daemon[T]
	// Stop sends SIGTERM to the running process and waits indefinitely for
	// it to exit. A Ctrl+C (or SIGTERM to this process) during the wait
	// escalates to SIGKILL. Usable without building the cobra tree.
	Stop() error
	// Status reports whether the process is running and clears a stale pid
	// file as a side effect. If marshal is nil, Status writes the default
	// text rendering to stdout (pid + pid file + log file paths). Otherwise
	// it hands the resolved StatusResult to marshal and prints whatever
	// bytes come back — the signature matches json.Marshal and yaml.Marshal
	// so they drop in directly.
	Status(marshal func(any) ([]byte, error)) error
	// PID reads the process ID from the pid file.
	PID() (int, error)
	// IsAlive reports whether the process named by the pid file is running.
	IsAlive() bool
	// PIDFile returns the path to the pid file, or an error if it is not yet
	// resolved (DetachOn has not run).
	PIDFile() (string, error)
	// LogFile returns the path to the log file, or an error if it is not yet
	// resolved (DetachOn has not run).
	LogFile() (string, error)
	// Name returns the effective state-file base name (WithName override or the
	// derived command path), or an error if it is not yet resolved.
	Name() (string, error)
}

// StatusResult is the structured form of the daemon's current state.
// Pass-through marshallers handed to Status receive this; the json tags
// match the schema documented in the status -o json command.
type StatusResult struct {
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Name    string `json:"name,omitempty"`
	PIDFile string `json:"pid_file,omitempty"`
	LogFile string `json:"log_file,omitempty"`
}
