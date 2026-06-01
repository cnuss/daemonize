package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

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
		// cmd is the "start" subcommand; cmd.Parent() is the wrapped daemon
		// root. Passing it in keeps DaemonImpl free of a *cobra.Command
		// field and resolves CommandPath at execution time (so subcommand
		// mounts under a larger cobra tree see the right path).
		return d.startCobra(cmd.Parent(), forwardArgs(cmd))
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

// startCobra runs the "start" subcommand: it re-execs this binary as a
// detached child along the wrapped command's path (so a daemon root mounted
// under a larger cobra tree still resolves correctly), writes the child's
// pid file, then either waits for the readiness signal (channel + SIGUSR1)
// or gives the child a brief window to crash before declaring success.
//
// daemonCmd is the wrapped command (the start subcommand's Parent()), passed
// at call time so DaemonImpl doesn't have to hold a *cobra.Command field.
// extra is the user's tail of argv (flags + positionals) forwarded to the
// re-exec'd child verbatim.
func (d *DaemonImpl[T]) startCobra(daemonCmd *cobra.Command, extra []string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}

	// Fresh log file per run so we stream only this child's output.
	logf, err := os.OpenFile(d.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}

	// Re-exec along the wrapped command's full path (minus the binary), so the
	// daemon root can be mounted under a larger cobra tree (e.g. "foo run serve").
	subPath := strings.Fields(daemonCmd.CommandPath())
	if len(subPath) > 0 {
		subPath = subPath[1:]
	}
	serveName := daemonCmd.Name()

	cmd := exec.Command(exe, append(subPath, extra...)...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = append(os.Environ(), daemonEnvFor(d.base)+"=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} // own session; survives parent exit

	// Install handlers before starting the child so we cannot miss SIGUSR1
	// (child ready) or SIGCHLD (child died) fired right after exec. SIGINT/SIGTERM
	// let Ctrl+C during the long startup cancel the launch.
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGCHLD, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := cmd.Start(); err != nil {
		// Parent's fd; the child never inherited it because exec failed.
		// Returning the original Start error is the useful signal; close
		// errors here are noise.
		_ = logf.Close()
		return err
	}
	// The child holds its own stdout/stderr fds via fork+exec inheritance.
	// Closing the parent's handle is best-effort cleanup; a failure here
	// doesn't break the child, so surface it on stderr rather than aborting
	// a successful start.
	if err := logf.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: close log file: %v\n", err)
	}

	pid := cmd.Process.Pid
	if err := d.writePID(pid); err != nil {
		return err
	}

	// Without a readiness channel the daemon has no "I'm ready" signal to wait
	// for, so just give the child ~100ms to crash before declaring success.
	hasReadiness := d.detachSig != nil && *d.detachSig != nil
	if !hasReadiness {
		select {
		case s := <-sigCh:
			if s == syscall.SIGCHLD {
				var ws syscall.WaitStatus
				if wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
					os.Remove(d.pidFile)
					return fmt.Errorf("%s exited during startup (see %s)", serveName, d.logFile)
				}
			}
		case <-time.After(100 * time.Millisecond):
		}
		fmt.Printf("pid file: %s\n", d.pidFile)
		fmt.Printf("log file: %s\n", d.logFile)
		fmt.Printf("started (pid %d), now running in the background\n", pid)
		return nil
	}

	fmt.Printf("starting %s (pid %d)...\n", serveName, pid)

	switch d.streamUntilReady(sigCh, pid) {
	case startInterrupted:
		fmt.Printf("\nstartup interrupted; stopping %s (pid %d)...\n", serveName, pid)
		_ = syscall.Kill(pid, syscall.SIGTERM)

		// Wait for the child to exit on SIGTERM. A second Ctrl+C (or SIGTERM
		// to this process) escalates to SIGKILL; otherwise we wait
		// indefinitely so the child can clean up on its own schedule.
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
	graceLoop:
		for {
			select {
			case <-done:
				break graceLoop
			case s := <-sigCh:
				if s == syscall.SIGINT || s == syscall.SIGTERM {
					fmt.Printf("forcing kill (pid %d)\n", pid)
					_ = syscall.Kill(pid, syscall.SIGKILL)
					<-done
					break graceLoop
				}
				// SIGCHLD and any other signal are noise here; cmd.Wait
				// detects the actual exit via done.
			}
		}

		os.Remove(d.pidFile)
		return fmt.Errorf("startup cancelled")
	case startFailed:
		_ = cmd.Wait() // reap the child that died during startup
		os.Remove(d.pidFile)
		return fmt.Errorf("%s exited during startup (see %s)", serveName, d.logFile)
	}

	// Child is up. Don't Wait: it outlives us via its own session, and the OS
	// reparents it when this process exits.
	fmt.Printf("pid file: %s\n", d.pidFile)
	fmt.Printf("log file: %s\n", d.logFile)
	fmt.Printf("started (pid %d), now running in the background\n", pid)
	return nil
}
