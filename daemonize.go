// Package daemonize wraps a cobra command with Unix daemon lifecycle controls —
// start, stop, status, and reload — by re-execing the binary as a detached
// background process. The wrapped command runs in the foreground; the
// daemon manages backgrounding, a pid file, log streaming during startup and
// shutdown, and signal-based readiness, all without mutating the command.
package daemonize

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	// stopPollEach is how often Stop checks whether the child has exited.
	stopPollEach = 100 * time.Millisecond

	// defaultGroupID is the cobra group ID for the lifecycle subcommands;
	// defaultGroupName is its title (a ":" is appended on render).
	defaultGroupID   = "daemonize"
	defaultGroupName = "Daemon Commands"
)

// daemonEnvFor derives the env-var name used to mark a child launched by start,
// from the daemon's resolved Name (e.g. "widget" -> "WIDGET_DAEMON"). The
// readiness relay reads it to decide whether to signal the parent. Parent and
// child compute it identically, so they always agree.
func daemonEnvFor(base string) string {
	return strings.ToUpper(strings.ReplaceAll(base, "-", "_")) + "_DAEMON"
}

// stateFiles builds the pid/log paths under <cache>/.<name>/ (name is the
// wrapped command's name), e.g. ".../.serve/server-serve.{pid,log}". Falls back
// to the working directory if the cache dir is unavailable.
func stateFiles(name, base string) (pid, log string) {
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = "."
	} else {
		dir = filepath.Join(dir, "."+name)
		os.MkdirAll(dir, 0o755)
	}
	return filepath.Join(dir, base+".pid"), filepath.Join(dir, base+".log")
}

// commandFileBase joins a command's name with its ancestors', root first
// (e.g. "server-serve"), for use as a state-file base name. It recurses up the
// parent chain until reaching the root (Parent() == nil).
func commandFileBase(cmd *cobra.Command) string {
	if cmd.Parent() == nil {
		return cmd.Name()
	}
	return commandFileBase(cmd.Parent()) + "-" + cmd.Name()
}

// Daemon is the builder for a background-lifecycle wrapper around a foreground
// command. Configure it with the With* methods, then call DetachOn. Obtain one
// from FromCobra.
type Daemon[T any] interface {
	// FromCobra wraps a cobra command, retyping the builder to *cobra.Command so
	// Into returns the assembled root command.
	FromCobra(inner *cobra.Command) Daemon[*cobra.Command]
	// DetachOn assembles the lifecycle wrapper and returns it (as T). detachSig is
	// closed by the wrapped command once it is ready; the daemon relays that so
	// "start" stops streaming and detaches. Pass nil for no readiness relay.
	// After FromCobra, T is *cobra.Command (the root command to Execute).
	DetachOn(detachSig <-chan struct{}) T
	// WithReload enables the "reload" subcommand and sets the signal it sends to
	// the running process. It must match the signal the wrapped command listens
	// on. Without it, no reload subcommand is registered.
	WithReload(sig syscall.Signal) Daemon[T]
	// WithName overrides the state-file base name. By default it is derived from
	// the wrapped command's path (e.g. "server-serve"); WithName("foo") yields
	// ".foo.pid"/".foo.log" instead.
	WithName(name string) Daemon[T]
	// WithGroup sets the lifecycle help group's title (a trailing ":" is added);
	// pass nil to ungroup (list them under Additional Commands). Unset, they are
	// grouped under "Daemon Commands:".
	WithGroup(name *string) Daemon[T]
	// WithContext sets the parent context that DetachOn installs on the
	// wrapped command. Pass context.Background() for the conventional
	// rooted context, or another context to inherit deadlines, values, or
	// cancellation from an outer scope.
	//
	// Pass nil to opt out of any context auto-wiring entirely: in that case
	// DetachOn will not call cmd.SetContext, and WithShutdownSignal is
	// ignored. The caller takes full responsibility for the command's
	// context, just as if neither method had been called.
	WithContext(parent context.Context) Daemon[T]
	// WithShutdownSignal installs a signal.NotifyContext that cancels the
	// command's context when any of the listed signals fire. DetachOn
	// composes it with the WithContext parent (or context.Background() if
	// WithContext is unset):
	//   ctx, cancel := signal.NotifyContext(parent, sigs...)
	//   cmd.SetContext(ctx)
	// The wrapped command can then use <-cmd.Context().Done() to react
	// without wiring signal.Notify itself. Stop, Reload, Status, and the
	// wrapped RunE all call cancel on exit so the underlying goroutine is
	// released. If WithContext(nil) was called, this is a no-op.
	WithShutdownSignal(sigs ...os.Signal) Daemon[T]
	// Stop sends SIGTERM to the running process and waits indefinitely for
	// it to exit. A Ctrl+C (or SIGTERM to this process) during the wait
	// escalates to SIGKILL. Usable without building the cobra tree.
	Stop() error
	// Status reports whether the process is running and clears a stale pid
	// file as a side effect. If marshal is nil, Status writes the default
	// text rendering to stdout (pid + pid file + log file paths). Otherwise
	// it hands the resolved StatusResult to marshal and prints whatever
	// bytes come back — the signature matches json.Marshal and yaml.Marshal
	// so they drop in directly.
	Status(marshal func(any) ([]byte, error)) error
	// Reload sends the configured reload signal (WithReload, default SIGHUP) to
	// the running process.
	Reload() error
	// PID reads the process ID from the pid file.
	PID() (int, error)
	// IsAlive reports whether the process named by the pid file is running.
	IsAlive() bool
	// PIDFile returns the path to the pid file, or an error if it is not yet
	// resolved (DetachOn has not run).
	PIDFile() (string, error)
	// LogFile returns the path to the log file, or an error if it is not yet
	// resolved (DetachOn has not run).
	LogFile() (string, error)
	// Name returns the effective state-file base name (WithName override or the
	// derived command path), or an error if it is not yet resolved.
	Name() (string, error)
}

// DaemonImpl is the default Daemon implementation. T is the wrapped value's
// type (and what Into returns).
type DaemonImpl[T any] struct {
	inner     T
	detachSig *(<-chan struct{}) // nil = unset
	reloadSig *syscall.Signal    // nil = reload disabled
	name      *string            // nil = derive from command path
	group     *string            // group title; nil (with groupSet) = ungrouped
	groupSet  bool               // true once WithGroup was called

	// ctxParent is the parent context set via WithContext. ctxParentSet
	// distinguishes "WithContext(nil)" (opt-out) from "WithContext never
	// called" (use defaults).
	ctxParent    context.Context
	ctxParentSet bool

	// shutdownSigs are the signals registered via WithShutdownSignal;
	// shutdownSigsSet records whether the method was called.
	shutdownSigs    []os.Signal
	shutdownSigsSet bool

	// ctxCancel releases the signal.NotifyContext goroutine. Set during
	// buildCobra when context auto-wiring is configured; defaults to a no-op so callers
	// of Stop/Reload/Status can defer it unconditionally.
	ctxCancel context.CancelFunc

	// State-file paths and the base name they derive from, resolved in buildCobra.
	pidFile string
	logFile string
	base    string

	// daemonCmd is the daemon-owned foreground command; start re-execs along its
	// full path so the daemon root can be mounted under a larger cobra tree.
	daemonCmd *cobra.Command

	// Into builds once; subsequent calls return the cached result.
	builtOnce sync.Once
	built     T
}

// NewDaemon returns an unconfigured builder. Call FromCobra to wrap a command.
func NewDaemon() Daemon[any] {
	return &DaemonImpl[any]{}
}

// FromCobra is a shorthand for NewDaemon().FromCobra(command). Use it when you
// already know you are wrapping a cobra command and don't need the untyped
// Daemon[any] bootstrap:
//
//	cmd := daemonize.FromCobra(serve).WithReload(syscall.SIGHUP).DetachOn(ready)
func FromCobra(command *cobra.Command) Daemon[*cobra.Command] {
	return NewDaemon().FromCobra(command)
}

// FromCobra wraps command, retyping the builder to *cobra.Command so Into
// returns the assembled root command. Configure with the With* methods after
// this call; reload is disabled until WithReload sets a signal.
func (d *DaemonImpl[T]) FromCobra(command *cobra.Command) Daemon[*cobra.Command] {
	return &DaemonImpl[*cobra.Command]{inner: command}
}

// DetachOn records the readiness channel, then assembles the lifecycle wrapper
// for the wrapped value and returns it (as T), dispatching on inner's type.
func (d *DaemonImpl[T]) DetachOn(detachSig <-chan struct{}) T {
	d.detachSig = &detachSig
	switch any(d.inner).(type) {
	case *cobra.Command:
		d.builtOnce.Do(func() { d.built = any(d.buildCobra()).(T) })
		return d.built
	default:
		panic("daemonize: unsupported wrapped type")
	}
}

func (d *DaemonImpl[T]) WithReload(sig syscall.Signal) Daemon[T] {
	d.reloadSig = &sig
	return d
}

func (d *DaemonImpl[T]) WithName(name string) Daemon[T] {
	d.name = &name
	return d
}

func (d *DaemonImpl[T]) WithGroup(name *string) Daemon[T] {
	d.group = name
	d.groupSet = true
	return d
}

func (d *DaemonImpl[T]) WithContext(parent context.Context) Daemon[T] {
	d.ctxParent = parent
	d.ctxParentSet = true
	return d
}

func (d *DaemonImpl[T]) WithShutdownSignal(sigs ...os.Signal) Daemon[T] {
	d.shutdownSigs = sigs
	d.shutdownSigsSet = true
	return d
}

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

func (d *DaemonImpl[T]) start(extra []string) error {
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
	subPath := strings.Fields(d.daemonCmd.CommandPath())
	if len(subPath) > 0 {
		subPath = subPath[1:]
	}
	serveName := d.daemonCmd.Name()

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
		logf.Close()
		return err
	}
	logf.Close() // the child holds its own stdout/stderr fds

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

// startResult is the outcome of waiting for a child to become ready.
type startResult int

const (
	startReady       startResult = iota // child signaled SIGUSR1
	startFailed                         // child exited during startup
	startInterrupted                    // SIGINT/SIGTERM received (Ctrl+C)
)

// streamUntilReady echoes the child's log file to stdout until the child sends
// SIGUSR1 (ready), dies, or the parent is interrupted (SIGINT/SIGTERM).
func (d *DaemonImpl[T]) streamUntilReady(sigCh chan os.Signal, pid int) startResult {
	tail := d.openLog(false) // from the start of the file: show all startup output
	defer tail.Close()

	for {
		tail.copy()
		select {
		case s := <-sigCh:
			switch s {
			case syscall.SIGUSR1:
				tail.copy() // drain remaining startup output
				return startReady
			case syscall.SIGINT, syscall.SIGTERM:
				return startInterrupted
			case syscall.SIGCHLD:
				// Reap without blocking. A zombie still answers kill(pid,0), so
				// kill(pid,0) can't tell death from stop; Wait4 confirms exit.
				var ws syscall.WaitStatus
				if wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
					tail.copy()
					return startFailed
				}
				// Stopped/continued or an unrelated child: keep waiting.
			}
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

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

	// Reattach to the log and stream only output produced from here on, so the
	// user sees the child's shutdown steps while we wait.
	tail := d.openLog(true)
	defer tail.Close()

	// Ctrl+C during the wait force-kills immediately.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return err
	}

	// Wait indefinitely for the child to exit on SIGTERM. The caller is
	// already in control: a second Ctrl+C (or SIGTERM to this process)
	// escalates to SIGKILL immediately.
	for {
		tail.copy()
		if !d.IsAlive() {
			tail.copy() // drain any final shutdown output
			os.Remove(d.pidFile)
			fmt.Printf("stopped (pid %d)\n", pid)
			return nil
		}
		select {
		case <-sigCh:
			tail.copy()
			_ = syscall.Kill(pid, syscall.SIGKILL)
			os.Remove(d.pidFile)
			fmt.Printf("\nkilled (pid %d) on interrupt\n", pid)
			return nil
		case <-time.After(stopPollEach):
		}
	}
}

// logTail streams newly appended log bytes to stdout. A nil file (log missing,
// e.g. a foreground server) makes copy/Close no-ops.
type logTail struct {
	f   *os.File
	buf []byte
}

// openLog opens the log for streaming. seekEnd=true starts at the current end
// (stop: only new shutdown output); false starts at the beginning (start: all
// startup output).
func (d *DaemonImpl[T]) openLog(seekEnd bool) *logTail {
	fh, err := os.Open(d.logFile)
	if err != nil {
		return &logTail{}
	}
	if seekEnd {
		fh.Seek(0, io.SeekEnd)
	}
	return &logTail{f: fh, buf: make([]byte, 4096)}
}

func (t *logTail) copy() {
	if t.f == nil {
		return
	}
	for {
		n, _ := t.f.Read(t.buf)
		if n == 0 {
			return
		}
		os.Stdout.Write(t.buf[:n])
	}
}

func (t *logTail) Close() {
	if t.f != nil {
		t.f.Close()
	}
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

// StatusResult is the structured form of the daemon's current state.
// Pass-through marshallers handed to Status receive this; the json tags
// match the schema documented in the status -o json command.
type StatusResult struct {
	State   string `json:"state"`
	PID     int    `json:"pid,omitempty"`
	Name    string `json:"name,omitempty"`
	PIDFile string `json:"pid_file,omitempty"`
	LogFile string `json:"log_file,omitempty"`
}

// computeStatus reads the pid file and resolves the daemon's current state.
// A stale pid file is removed here as a side effect — Status relies on that
// cleanup.
func (d *DaemonImpl[T]) computeStatus() StatusResult {
	pid, err := d.PID()
	if err != nil {
		return StatusResult{State: "not_running"}
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		return StatusResult{
			State:   "stale",
			PID:     pid,
			Name:    d.base,
			PIDFile: d.pidFile,
			LogFile: d.logFile,
		}
	}
	return StatusResult{
		State:   "running",
		PID:     pid,
		Name:    d.base,
		PIDFile: d.pidFile,
		LogFile: d.logFile,
	}
}

func (d *DaemonImpl[T]) Status(marshal func(any) ([]byte, error)) error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	if marshal == nil {
		// Default to the human-readable text rendering. Stays on the same
		// marshal-then-print path as the user-supplied case below.
		marshal = func(v any) ([]byte, error) {
			r, ok := v.(StatusResult)
			if !ok {
				return nil, fmt.Errorf("daemonize: status text marshaler: want StatusResult, got %T", v)
			}
			var lines []string
			switch r.State {
			case "not_running":
				lines = []string{"not running"}
			case "stale":
				lines = []string{fmt.Sprintf("not running (cleared stale pid %d)", r.PID)}
			case "running":
				lines = []string{
					fmt.Sprintf("running (pid %d)", r.PID),
					"pid file: " + r.PIDFile,
					"log file: " + r.LogFile,
				}
			default:
				return nil, fmt.Errorf("daemonize: status text marshaler: unknown state %q", r.State)
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
	}
	b, err := marshal(d.computeStatus())
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func (d *DaemonImpl[T]) Reload() error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	sig := syscall.Signal(syscall.SIGHUP)
	if d.reloadSig != nil {
		sig = *d.reloadSig
	}
	pid, err := d.PID()
	if err != nil {
		return fmt.Errorf("not running (no pid file)")
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		return fmt.Errorf("not running (stale pid %d)", pid)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return err
	}
	fmt.Printf("reload signal sent (pid %d)\n", pid)
	return nil
}

func (d *DaemonImpl[T]) IsAlive() bool {
	pid, err := d.PID()
	return err == nil && syscall.Kill(pid, 0) == nil
}

func (d *DaemonImpl[T]) PIDFile() (string, error) {
	if d.pidFile == "" {
		return "", fmt.Errorf("daemonize: pid file not resolved (call DetachOn first)")
	}
	return d.pidFile, nil
}

func (d *DaemonImpl[T]) LogFile() (string, error) {
	if d.logFile == "" {
		return "", fmt.Errorf("daemonize: log file not resolved (call DetachOn first)")
	}
	return d.logFile, nil
}

func (d *DaemonImpl[T]) Name() (string, error) {
	if d.base == "" {
		return "", fmt.Errorf("daemonize: name not resolved (call DetachOn first)")
	}
	return d.base, nil
}

func (d *DaemonImpl[T]) PID() (int, error) {
	b, err := os.ReadFile(d.pidFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func (d *DaemonImpl[T]) writePID(pid int) error {
	return os.WriteFile(d.pidFile, []byte(strconv.Itoa(pid)), 0o644)
}
