// Command shutdown-timeout demonstrates WithGracePeriod: the worker stalls
// inside its shutdown handler past the configured grace period, so stop
// escalates to SIGKILL instead of waiting for a graceful exit that never
// comes.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	ready := make(chan struct{})

	cmd := &cobra.Command{
		Use:   "shutdown-timeout",
		Short: "Worker whose shutdown stalls past the grace period",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("ready")
			close(ready)
			<-cmd.Context().Done()
			fmt.Println("draining (this hangs past the grace period)...")
			<-time.After(time.Hour) // killed by daemon long before this fires
			return nil
		},
	}
	cmd.SetContext(ctx)

	if err := daemonize.FromCobra(cmd).
		WithGracePeriod(200 * time.Millisecond).
		DetachOn(ready).
		Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
