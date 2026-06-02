package v1alpha1

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// buildCobra enriches the caller's command in place with start/stop/status
// as subcommands, wrapping its RunE so that running the command directly in
// the foreground still owns the pid file and relays readiness — making
// stop/status work against both foreground and daemonized runs.
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
	d.platform = newPlatform(d.pidFile, d.logFile)

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
					// Install the shutdown listener BEFORE notifying the
					// parent so the parent's Stop never races a not-yet-
					// listening child. Pass nil when the caller did not wire
					// real shutdown signals: ctxCancel is the default no-op
					// in that case, and installing a listener with a no-op
					// cancel would let Stop write the named-pipe byte and
					// then hang polling for a child that has no plan to
					// exit. Skipping the listener forces Stop to fall
					// through to TerminateProcess on Windows.
					var cancel context.CancelFunc
					if d.shutdownSigsSet && len(d.shutdownSigs) > 0 {
						cancel = d.ctxCancel
					}
					d.platform.installShutdownListener(cancel)
					d.notifyParentReady()
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
	return command
}

// ensurePid returns a PreRunE that gates on the daemon's running state:
// mustRun=true requires a live process (stop); mustRun=false requires
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

// startResult is the outcome of waiting for a child to become ready. Both
// platforms produce the same three states from streamUntilReady; the
// reactions to each state stay in the cross-platform startCobra below.
type startResult int

const (
	startReady       startResult = iota // child signaled readiness (Unix: SIGUSR1; Windows: ready file)
	startFailed                         // child exited during startup
	startInterrupted                    // user interrupted the parent (Unix: SIGINT/SIGTERM; Windows: os.Interrupt)
)

// platform is the per-OS shim that startCobra, streamUntilReady, IsAlive,
// and Stop route through. unixPlatform lives in platform.go;
// windowsPlatform in platform_windows.go. newPlatform (also per-OS) builds
// the right impl during buildCobra once the state-file paths are known.
type platform interface {
	// applyDetachAttrs sets cmd.SysProcAttr so the child survives parent
	// exit. Unix uses Setsid; Windows uses DETACHED_PROCESS +
	// CREATE_NEW_PROCESS_GROUP.
	applyDetachAttrs(cmd *exec.Cmd)
	// installStartSignals subscribes the parent to the signals it needs
	// during the startup window. The returned func releases the
	// subscription.
	installStartSignals() (chan os.Signal, func())
	// waitForEarlyExit gives the child ~100ms to crash before declaring
	// success on the no-readiness path; returns an error if the child died.
	waitForEarlyExit(sigCh chan os.Signal, pid int, serveName string) error
	// killChildOnInterrupt handles the startInterrupted branch of
	// streamUntilReady — kill the child according to the platform's rules.
	killChildOnInterrupt(cmd *exec.Cmd, sigCh chan os.Signal, pid int, serveName string)
	// afterStartupReady runs on the success path of startCobra; on Windows
	// it removes the ready-file sentinel.
	afterStartupReady()
	// pollStartupState is the per-tick check inside streamUntilReady: did
	// the child signal ready, die, or did the user interrupt? Returns
	// (state, true) when there is a decision; (_, false) to keep polling.
	pollStartupState(sigCh chan os.Signal, pid int) (startResult, bool)
	// isAlive reports whether the given pid is currently running.
	isAlive(pid int) bool
	// notifyParentReady runs inside the daemon child once the wrapped
	// command closes its readiness channel.
	notifyParentReady(ppid int)
	// installShutdownListener runs in the daemon child after notifyParentReady.
	// On Windows it opens a server-side named pipe that the parent's Stop
	// writes to, calling cancel when the byte arrives — that surfaces as
	// <-cmd.Context().Done() to the worker (assuming WithShutdownSignal /
	// WithContext was configured). On Unix it is a no-op because POSIX
	// signals already wake the worker's context.
	installShutdownListener(cancel context.CancelFunc)
	// stop terminates pid. tickTail is called once per polling iteration so
	// callers can interleave log streaming with the kill+wait loop.
	stop(pid int, tickTail func()) error
}

// startCobra runs the "start" subcommand: it re-execs this binary as a
// detached child along the wrapped command's path (so a daemon root mounted
// under a larger cobra tree still resolves correctly), writes the child's
// pid file, then either waits for the readiness signal or gives the child a
// brief window to crash before declaring success.
//
// The body stays cross-platform by routing every OS-touching step through a
// dispatcher method. platform.go (!windows) and platform_windows.go each
// implement the same set:
//
//	applyDetachAttrs        — set SysProcAttr for the detach mode
//	installStartSignals     — install the parent-side signal channel
//	waitForEarlyExit        — sleep ~100ms; return an error if the child died
//	killChildOnInterrupt    — handle the streamUntilReady startInterrupted branch
//	afterStartupReady       — post-ready cleanup (Windows removes the sentinel)
//	streamUntilReady        — block until ready / failed / interrupted
//
// daemonCmd is the wrapped command (the "start" subcommand's Parent()), passed
// at call time so DaemonImpl doesn't have to hold a *cobra.Command field.
// extra is the user's tail of argv (flags + positionals) forwarded to the
// re-exec'd child verbatim.
func (d *DaemonImpl[T]) startCobra(daemonCmd *cobra.Command, extra []string) error {
	cmd, logf, serveName, err := d.prepareStart(daemonCmd, extra)
	if err != nil {
		return err
	}
	d.platform.applyDetachAttrs(cmd)

	sigCh, stopSignals := d.platform.installStartSignals()
	defer stopSignals()

	startErr := cmd.Start()
	d.closeStartedLog(logf, startErr)
	if startErr != nil {
		return startErr
	}

	pid := cmd.Process.Pid
	if err := d.writePID(pid); err != nil {
		return err
	}

	hasReadiness := d.detachSig != nil && *d.detachSig != nil
	if !hasReadiness {
		if err := d.platform.waitForEarlyExit(sigCh, pid, serveName); err != nil {
			os.Remove(d.pidFile)
			return err
		}
		d.announceStarted(pid)
		return nil
	}

	fmt.Printf("starting %s (pid %d)...\n", serveName, pid)

	switch d.streamUntilReady(sigCh, pid) {
	case startInterrupted:
		d.platform.killChildOnInterrupt(cmd, sigCh, pid, serveName)
		os.Remove(d.pidFile)
		return fmt.Errorf("startup cancelled")
	case startFailed:
		_ = cmd.Wait()
		os.Remove(d.pidFile)
		return fmt.Errorf("%s exited during startup (see %s)", serveName, d.logFile)
	}

	d.platform.afterStartupReady()
	d.announceStarted(pid)
	return nil
}

// streamUntilReady tails the child's log file and asks the platform once per
// 50ms whether the startup has resolved.
func (d *DaemonImpl[T]) streamUntilReady(sigCh chan os.Signal, pid int) startResult {
	tail := d.openLog(false)
	defer tail.Close()

	for {
		tail.copy()
		if state, done := d.platform.pollStartupState(sigCh, pid); done {
			tail.copy()
			return state
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// IsAlive reports whether the process named by the pid file is running.
func (d *DaemonImpl[T]) IsAlive() bool {
	pid, err := d.PID()
	if err != nil {
		return false
	}
	return d.platform.isAlive(pid)
}

// notifyParentReady runs inside the daemon child once the wrapped command
// closes its readiness channel; it delegates to the platform so the relay
// path stays uniform across OSes.
func (d *DaemonImpl[T]) notifyParentReady() {
	d.platform.notifyParentReady(os.Getppid())
}

// Stop sends a termination signal to the running process and waits for it
// to exit. The log is tailed throughout so the worker's shutdown output
// reaches the user.
func (d *DaemonImpl[T]) Stop() error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	pid, err := d.PID()
	if err != nil {
		fmt.Println("not running")
		return nil
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		fmt.Printf("not running (cleared stale pid %d)\n", pid)
		return nil
	}
	fmt.Printf("shutting down (pid %d)...\n", pid)

	tail := d.openLog(true)
	defer tail.Close()

	if err := d.platform.stop(pid, tail.copy); err != nil {
		return err
	}

	tail.copy()
	os.Remove(d.pidFile)
	fmt.Printf("stopped (pid %d)\n", pid)
	return nil
}
