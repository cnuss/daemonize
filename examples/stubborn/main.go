// Command stubborn is a misbehaving worker that catches SIGTERM and ignores
// it — only SIGKILL stops it. Used by the e2e suite to exercise the Ctrl+C
// escalation path in stop.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})

	cmd := &cobra.Command{
		Use:   "stubborn",
		Short: "Worker that ignores SIGTERM (only SIGKILL stops it)",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Install a no-op handler for SIGTERM before anything else so
			// the default-action terminate is suppressed for the lifetime
			// of the worker. Only SIGKILL ends the process.
			ignore := make(chan os.Signal, 1)
			signal.Notify(ignore, syscall.SIGTERM)
			go func() {
				for range ignore {
					fmt.Println("ignored SIGTERM")
				}
			}()

			// Slow startup window — gives "start" callers a chance to
			// exercise the interrupt path before readiness fires.
			fmt.Println("warming up...")
			time.Sleep(2 * time.Second)

			fmt.Println("ready")
			close(ready)

			<-time.After(time.Hour) // killed by SIGKILL long before this fires
			return nil
		},
	}

	if err := daemonize.FromCobra(cmd).DetachOn(ready).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
