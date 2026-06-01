package v1_test

import (
	"syscall"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

// FromCobra wraps a cobra command with start/stop/status lifecycle
// subcommands. The wrapped command closes a readiness channel when it is up;
// the daemon translates that into a signal so "start" stops streaming the
// child's log and detaches.
func ExampleFromCobra() {
	ready := make(chan struct{})

	serve := &cobra.Command{
		Use: "serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			// ... slow startup work (bind a port, warm caches, etc.) ...
			close(ready) // tell the daemon: I'm ready, you may detach
			select {}    // block in the foreground (real code would wait on signals)
		},
	}

	root := daemonize.FromCobra(serve).DetachOn(ready)
	_ = root.Execute()
}

// NewDaemon returns the untyped Daemon[any] bootstrap. Prefer FromCobra when
// you already know you are wrapping a cobra command; NewDaemon is for code
// that picks the backend at runtime.
func ExampleNewDaemon() {
	ready := make(chan struct{})
	serve := &cobra.Command{Use: "serve"}

	root := daemonize.NewDaemon().FromCobra(serve).DetachOn(ready)
	_ = root.Execute()
}

// WithReload enables the "reload" subcommand and sets the signal it sends to
// the running process. The wrapped command must listen for that signal.
func Example_withReload() {
	ready := make(chan struct{})
	serve := &cobra.Command{Use: "serve"}

	root := daemonize.FromCobra(serve).
		WithReload(syscall.SIGHUP).
		DetachOn(ready)
	_ = root.Execute()
}

// WithName overrides the base name used for the pid and log files. By default
// it is derived from the wrapped command's path (e.g. "server-serve");
// WithName("widget") yields widget.pid and widget.log instead.
func Example_withName() {
	ready := make(chan struct{})
	serve := &cobra.Command{Use: "serve"}

	root := daemonize.FromCobra(serve).
		WithName("widget").
		DetachOn(ready)
	_ = root.Execute()
}

// WithGroup sets the lifecycle help-group title. The lifecycle subcommands
// (start/stop/status/reload) appear under "Lifecycle:" in --help output.
func Example_withGroup() {
	ready := make(chan struct{})
	serve := &cobra.Command{Use: "serve"}

	title := "Lifecycle"
	root := daemonize.FromCobra(serve).
		WithGroup(&title).
		DetachOn(ready)
	_ = root.Execute()
}

// Passing nil to WithGroup ungroups the lifecycle subcommands; they appear
// under "Additional Commands" rather than their own labelled section.
func Example_withGroupUngrouped() {
	ready := make(chan struct{})
	serve := &cobra.Command{Use: "serve"}

	root := daemonize.FromCobra(serve).
		WithGroup(nil).
		DetachOn(ready)
	_ = root.Execute()
}

// DetachOn is the terminal builder step. The channel is closed by the wrapped
// command once it is ready to serve; the daemon waits on it before stopping
// the startup-log stream and reporting "started" to the user. Pass nil if the
// wrapped command has no readiness signal (start gives the child ~100ms to
// crash, then declares success).
func Example_detachOn() {
	ready := make(chan struct{})
	serve := &cobra.Command{
		Use: "serve",
		RunE: func(cmd *cobra.Command, args []string) error {
			close(ready)
			select {}
		},
	}

	root := daemonize.FromCobra(serve).DetachOn(ready)
	_ = root.Execute()
}
