// Command shutdown-error demonstrates a wrapped command that starts fine but
// errors during shutdown. The daemon streams the failure while "stop" waits;
// the process still exits, so stop completes and the pid file is removed.
package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Worker that errors on shutdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("ready")
			close(ready)

			<-cmd.Context().Done()

			fmt.Println("shutdown error: failed to flush pending writes")
			return fmt.Errorf("shutdown failed")
		},
	}

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
