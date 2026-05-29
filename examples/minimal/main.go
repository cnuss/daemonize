// Command minimal is the smallest daemonize example: wrap a worker, stream its
// startup via a readiness channel, and get start/stop/status. No reload.
package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func serve(ready chan struct{}) *cobra.Command {
	var message string
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the worker in the foreground (Ctrl-C to stop)",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("ready: %s\n", message)
			close(ready)

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
			<-stop
			fmt.Println("stopping")
			return nil
		},
	}
	cmd.Flags().StringVarP(&message, "message", "m", "hello", "message printed when ready")
	return cmd
}

func main() {
	ready := make(chan struct{})
	cmd := daemonize.NewDaemon().
		FromCobra(serve(ready)).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
