package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

// buildCobra enriches the caller's command in place with start/stop/status
// (and optionally reload) as subcommands, wrapping its RunE so that running
// the command directly in the foreground still owns the pid file and relays
// readiness — making stop/reload/status work against both foreground and
// daemonized runs.
//
// "start" re-execs this binary as a detached child running the wrapped
// command (forwarding its flags), writing the child PID to the pid file.
func (d *DaemonImpl[T]) buildCobra() *cobra.Command {
	command, ok := any(d.inner).(*cobra.Command)
	if !ok || command == nil {
		panic("daemonize: no command provided")
	}
	var detachSig <-chan struct{}
	if d.detachSig != nil {
		detachSig = *d.detachSig
	}
	serveName := command.Name()
	d.daemonCmd = command

	// Attaching start/stop/status as children would otherwise make cobra
	// reject any unknown first positional as a missing subcommand. Default
	// to ArbitraryArgs so the foreground worker keeps accepting positionals
	// (the typical case); callers that want strict validation can set their
	// own Args before calling DetachOn.
	if command.Args == nil {
		command.Args = cobra.ArbitraryArgs
	}

	// Context auto-wiring. Three knobs feed into this:
	//   - WithContext(nil) explicitly opts out: skip everything.
	//   - WithContext(parent) sets the parent; absent it, context.Background().
	//   - WithShutdownSignal(sigs...) wraps the parent in signal.NotifyContext.
	// At least one of WithContext / WithShutdownSignal must be set for any
	// wiring to happen; otherwise the wrapped command's context is left as
	// the caller arranged it (cobra defaults it to context.Background()).
	d.ctxCancel = func() {}
	optedOut := d.ctxParentSet && d.ctxParent == nil
	if !optedOut && (d.ctxParentSet || d.shutdownSigsSet) {
		parent := d.ctxParent
		if parent == nil {
			parent = context.Background()
		}
		if d.shutdownSigsSet && len(d.shutdownSigs) > 0 {
			ctx, cancel := signal.NotifyContext(parent, d.shutdownSigs...)
			d.ctxCancel = cancel
			command.SetContext(ctx)
		} else {
			command.SetContext(parent)
		}
	}

	// State files: WithName wins, else derive from the command path so every
	// lifecycle subcommand we attach targets the same files.
	base := ""
	if d.name != nil {
		base = *d.name
	}
	if base == "" {
		base = commandFileBase(command)
	}
	d.base = base
	d.pidFile, d.logFile = stateFiles(serveName, base)

	// Wrap PreRunE: gate foreground runs the same way "start" is gated, so
	// running the command directly while a daemon is alive fails fast instead
	// of clobbering the pid file. Skip the gate for the daemon-launched child:
	// "start" already wrote its pid before exec, so the gate would otherwise
	// see the child as "already running" against its own entry.
	origPreRunE := command.PreRunE
	startGate := d.ensurePid(false)
	command.PreRunE = func(cmd *cobra.Command, args []string) error {
		if os.Getenv(daemonEnvFor(d.base)) == "" {
			if err := startGate(cmd, args); err != nil {
				return err
			}
		}
		if origPreRunE != nil {
			return origPreRunE(cmd, args)
		}
		return nil
	}

	// Wrap RunE: own the pid file for the lifetime of the foreground run and
	// relay readiness to the parent as SIGUSR1 (opaque to the wrapped command,
	// which only closes detachSig). The relay is a no-op outside the daemon.
	origRunE := command.RunE
	origRun := command.Run
	command.Run = nil
	command.RunE = func(cmd *cobra.Command, args []string) error {
		defer d.ctxCancel()
		if err := d.writePID(os.Getpid()); err != nil {
			return err
		}
		defer os.Remove(d.pidFile)

		if detachSig != nil {
			go func() {
				<-detachSig
				if os.Getenv(daemonEnvFor(d.base)) != "" {
					syscall.Kill(os.Getppid(), syscall.SIGUSR1)
				}
			}()
		}
		if origRunE != nil {
			return origRunE(cmd, args)
		}
		if origRun != nil {
			origRun(cmd, args)
		}
		return nil
	}

	// Lifecycle commands are grouped by default. WithGroup(nil) ungroups them
	// (groupID stays "", so cobra lists them under Additional Commands);
	// WithGroup(&title) overrides the title.
	groupID := ""
	title := defaultGroupName
	grouped := true
	if d.groupSet {
		if d.group == nil {
			grouped = false
		} else {
			title = *d.group
		}
	}
	if grouped {
		groupID = defaultGroupID
		command.AddGroup(&cobra.Group{ID: groupID, Title: title + ":"})
	}

	startRun := func(cmd *cobra.Command, args []string) error {
		return d.start(forwardArgs(cmd))
	}

	startCmd := &cobra.Command{
		Use:     "start",
		Short:   "Start `" + serveName + "` in the background",
		GroupID: groupID,
		PreRunE: d.ensurePid(false),
		RunE:    startRun,
	}
	startCmd.Flags().AddFlagSet(command.Flags())

	stopCmd := &cobra.Command{
		Use:     "stop",
		Short:   "Stop the running `" + serveName + "`",
		GroupID: groupID,
		RunE:    func(cmd *cobra.Command, args []string) error { return d.Stop() },
	}

	statusFormat := statusOutputFormat("text")
	statusCmd := &cobra.Command{
		Use:     "status",
		Short:   "Report whether `" + serveName + "` is running",
		GroupID: groupID,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch string(statusFormat) {
			case "json":
				return d.Status(json.Marshal)
			default:
				return d.Status(nil)
			}
		},
	}
	statusCmd.Flags().VarP(&statusFormat, "output", "o", "output format (text|json)")
	_ = statusCmd.RegisterFlagCompletionFunc("output", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"text", "json"}, cobra.ShellCompDirectiveNoFileComp
	})

	command.AddCommand(startCmd, stopCmd, statusCmd)

	// reload is registered only when a signal was configured via WithReload.
	if d.reloadSig != nil {
		sig := *d.reloadSig
		command.AddCommand(&cobra.Command{
			Use:     "reload",
			Short:   fmt.Sprintf("Signal the running `%s` to reload (%s)", serveName, sig),
			GroupID: groupID,
			PreRunE: d.ensurePid(true),
			RunE:    func(cmd *cobra.Command, args []string) error { return d.Reload() },
		})
	}
	return command
}

// ensurePid returns a PreRunE that gates on the daemon's running state:
// mustRun=true requires a live process (stop, reload); mustRun=false requires
// none (start, and the bare alias). A stale pid file counts as not running.
func (d *DaemonImpl[T]) ensurePid(mustRun bool) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		running := d.IsAlive()
		switch {
		case mustRun && !running:
			return fmt.Errorf("not running")
		case !mustRun && running:
			pid, _ := d.PID()
			return fmt.Errorf("already running (pid %d)", pid)
		}
		return nil
	}
}

// forwardArgs borrows the user's own argv after the matched command path, so the
// daemon child sees exactly what was typed (flags, shorthands, positionals) —
// not a normalized rebuild. cmd.CommandPath() counts the leading tokens (program
// + subcommand) to drop.
func forwardArgs(cmd *cobra.Command) []string {
	n := len(strings.Fields(cmd.CommandPath()))
	if n < len(os.Args) {
		return os.Args[n:]
	}
	return nil
}

// statusOutputFormat is the pflag.Value backing the status subcommand's
// --output flag. It rejects anything other than "text" or "json" at parse
// time so RunE never has to defend against unknown values.
type statusOutputFormat string

func (s *statusOutputFormat) String() string { return string(*s) }
func (s *statusOutputFormat) Type() string   { return "string" }
func (s *statusOutputFormat) Set(v string) error {
	switch v {
	case "text", "json":
		*s = statusOutputFormat(v)
		return nil
	default:
		return fmt.Errorf("must be one of: text, json")
	}
}
