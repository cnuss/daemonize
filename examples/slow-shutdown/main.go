// Command slow-shutdown demonstrates a wrapped command with a slow, graceful
// shutdown. Startup is instant; on shutdown the command runs several drain
// steps before exiting, and the daemon streams them to the terminal while
// "stop" waits (indefinitely, unless Ctrl+C escalates to a hard kill).
//
// The --step delay is set at start (forwarded to the worker); stop just signals
// it. Try: go run . start --step 500ms, then in another shell: go run . stop
package main

import (
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})
	var step time.Duration

	serve := &cobra.Command{
		Use:   "serve",
		Short: "Worker with a slow graceful shutdown",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("ready")
			close(ready)

			<-cmd.Context().Done()

			steps := []string{"draining requests", "flushing buffers", "closing connections"}
			for i, s := range steps {
				fmt.Printf("shutdown [%d/%d] %s (%s)...\n", i+1, len(steps), s, step)
				time.Sleep(step)
			}
			fmt.Println("stopped cleanly")
			return nil
		},
	}
	serve.Flags().DurationVar(&step, "step", time.Second, "delay per shutdown step")

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		WithShutdownSignal(os.Interrupt, syscall.SIGTERM).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
