// Command slow-start demonstrates a wrapped command with a slow startup. The
// daemon streams each startup step to the terminal while "start" waits, then
// detaches once the command closes its readiness channel.
//
// Try: go run . start --step 500ms
package main

import (
	"fmt"
	"os"
	"os/signal"
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
		Short: "Worker with a slow startup",
		RunE: func(cmd *cobra.Command, args []string) error {
			steps := []string{"loading config", "warming cache", "opening listeners"}
			for i, s := range steps {
				fmt.Printf("startup [%d/%d] %s (%s)...\n", i+1, len(steps), s, step)
				time.Sleep(step)
			}
			fmt.Println("ready")
			close(ready) // bound and serving — daemon may detach

			stop := make(chan os.Signal, 1)
			signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
			<-stop
			fmt.Println("stopping")
			return nil
		},
	}
	serve.Flags().DurationVar(&step, "step", time.Second, "delay per startup step")

	cmd := daemonize.NewDaemon().
		FromCobra(serve).
		DetachOn(ready)
	if err := cmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
