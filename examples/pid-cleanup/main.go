// Command pid-cleanup is a degenerate worker: it never closes the readiness
// channel, sleeps for 5s, prints "hello world", and returns. The daemon
// treats that as "exited during startup" but still removes the pid file —
// this example exists for the e2e harness to assert that cleanup happens.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	ready := make(chan struct{})

	cmd := &cobra.Command{
		Use:   "pid-cleanup",
		Short: "Sleep 5s, print hello world, exit (never closes ready)",
		RunE: func(cmd *cobra.Command, args []string) error {
			time.Sleep(5 * time.Second)
			fmt.Println("hello world")
			return nil
		},
	}

	if err := daemonize.FromCobra(cmd).DetachOn(ready).Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
