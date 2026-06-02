// Command with-args shows that flags and positional args are forwarded verbatim
// to the wrapped command. "start --port 9000 -v a b" re-execs the worker as
// "serve --port 9000 -v a b", so the worker sees exactly what was typed.
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
	var (
		port    int
		verbose bool
	)
	serve := &cobra.Command{
		Use:   "serve [args...]",
		Short: "Worker that echoes its flags and positional args",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("config: port=%d verbose=%t args=%v\n", port, verbose, args)
			close(ready)
			<-cmd.Context().Done()
			fmt.Println("stopping")
			return nil
		},
	}
	serve.Flags().IntVarP(&port, "port", "p", 8080, "port to listen on")
	serve.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
