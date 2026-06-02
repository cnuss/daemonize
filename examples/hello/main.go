// Command hello is the smallest daemonize example: wrap a worker, stream its
// startup via a readiness channel, and get start/stop/status.
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

	var message string
	cmd := &cobra.Command{
		Use:   "hello",
		Short: "Say hello",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("hello %s\n", message)
			close(ready)
			<-cmd.Context().Done()
			fmt.Println("stopping")
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "world", "who to greet")

	if err := daemonize.FromCobra(cmd).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready).
		Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
