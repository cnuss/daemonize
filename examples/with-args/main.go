// Command with-args shows that flags and positional args are forwarded verbatim
// to the wrapped command. "start --port 9000 -v a b" re-execs the worker as
// "serve --port 9000 -v a b", so the worker sees exactly what was typed.
package main

import (
	"fmt"
	"os"
	"os/signal"
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
		// Because daemonize attaches start/stop/status as children, the
		// foreground command needs an explicit Args validator to accept
		// arbitrary positionals (cobra would otherwise treat the first
		// positional as an unknown subcommand).
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("config: port=%d verbose=%t args=%v\n", port, verbose, args)
			close(ready)

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
			<-stop
			fmt.Println("stopping")
			return nil
		},
	}
	serve.Flags().IntVarP(&port, "port", "p", 8080, "port to listen on")
	serve.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose logging")

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
