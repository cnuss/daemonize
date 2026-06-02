// Command named overrides the state-file base name with WithName, so the pid
// and log files are ".../<cache>/.serve/widget.{pid,log}" instead of being
// derived from the command path.
package main

import (
	"fmt"
	"os"
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
			<-cmd.Context().Done()
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
		WithName("widget").
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
