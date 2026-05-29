// Command start-error demonstrates a wrapped command that fails during startup
// (before signaling readiness). The daemon notices the child exited, reports
// "exited during startup", removes the pid file, and returns non-zero — no
// stray background process is left behind.
package main

import (
	"fmt"
	"os"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Worker that fails to start",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("starting up...")
			// Fails before close(ready): the daemon never sees readiness.
			return fmt.Errorf("startup failed: cannot connect to database")
		},
	}

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
