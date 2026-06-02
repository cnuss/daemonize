// Command stubborn is a misbehaving worker that catches the graceful-shutdown
// signal and ignores it — only a hard kill stops it (SIGKILL on Unix,
// TerminateProcess on Windows). Used by the e2e suite to exercise the Ctrl+C
// escalation path in start and stop.
package main

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})

	cmd := &cobra.Command{
		Use:   "stubborn",
		Short: "Worker that ignores the graceful-shutdown signal",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Slow startup window — gives "start" callers a chance to
			// exercise the interrupt path before readiness fires.
			fmt.Println("warming up...")
			time.Sleep(2 * time.Second)

			fmt.Println("ready")
			close(ready)

			// WithShutdownSignal below installs a signal.NotifyContext on
			// Unix and a named-pipe listener on Windows. Both cancel
			// cmd.Context() when the parent signals a graceful stop. We
			// deliberately ignore the cancellation here so the parent's
			// stop / killChildOnInterrupt has to escalate to a hard kill.
			<-time.After(time.Hour) // never fires; OS-level kill ends us
			return nil
		},
	}

	d := daemonize.FromCobra(cmd).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := d.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
