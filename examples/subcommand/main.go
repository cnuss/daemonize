// Command subcommand shows daemonize mounted under a larger cobra tree: the app has its
// own init/create/delete subcommands, and "subcommand run" is the daemonized entry
// point (so "subcommand run start", "subcommand run stop", "subcommand run serve", ... all work).
package main

import (
	"fmt"
	"math"
	"os"
	"time"

	"github.com/cnuss/daemonize"
	"github.com/spf13/cobra"
)

func main() {
	root := &cobra.Command{Use: "subcommand", Short: "subcommand CLI"}
	ready := make(chan struct{})

	root.AddCommand(
		&cobra.Command{Use: "init", Short: "Initialize subcommand", RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("initialized")
			return nil
		}},
		&cobra.Command{Use: "create", Short: "Create a thing", RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("created")
			return nil
		}},
		&cobra.Command{Use: "delete", Short: "Delete a thing", RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("deleted")
			return nil
		}},
		daemonize.FromCobra(&cobra.Command{Use: "run", Short: "Run a thing", RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("running")
			time.Sleep(5 * time.Second)
			close(ready)
			// Block "forever" without signal handling. select{} would deadlock
			// once the daemon's relay goroutine exits — the pending timer keeps
			// Go's deadlock detector happy.
			time.Sleep(time.Duration(math.MaxInt64))
			return nil
		}}).DetachOn(ready),
	)

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
