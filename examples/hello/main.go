// Command hello is the smallest daemonize example: wrap a worker, stream its
// startup via a readiness channel, and get start/stop/status. No reload.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
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
	cmd.SetContext(ctx)

	if err := daemonize.FromCobra(cmd).DetachOn(ready).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
