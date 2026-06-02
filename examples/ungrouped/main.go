// Command ungrouped passes WithGroup(nil) so the lifecycle subcommands are not
// grouped — they appear under cobra's default "Additional Commands:".
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
		WithGroup(nil).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
