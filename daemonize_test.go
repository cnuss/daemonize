package daemonize

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/spf13/cobra"
)

func hasCmd(root *cobra.Command, name string) bool {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return true
		}
	}
	return false
}

func newInner() *cobra.Command {
	return &cobra.Command{Use: "serve", RunE: func(*cobra.Command, []string) error { return nil }}
}

func cmdByName(root *cobra.Command, name string) *cobra.Command {
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// deadPID returns a pid that has exited and been reaped, so it is not alive.
func deadPID(t *testing.T) int {
	t.Helper()
	c := exec.Command("true")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	_ = c.Wait()
	return pid
}

func TestReloadOptIn(t *testing.T) {
	root := NewDaemon().FromCobra(newInner()).DetachOn(nil)
	if hasCmd(root, "reload") {
		t.Error("reload should be absent without WithReload")
	}

	root = NewDaemon().FromCobra(newInner()).WithReload(syscall.SIGHUP).DetachOn(nil)
	if !hasCmd(root, "reload") {
		t.Error("reload should be present with WithReload")
	}
}

func TestGrouping(t *testing.T) {
	// Default: lifecycle commands grouped under defaultGroupID.
	root := NewDaemon().FromCobra(newInner()).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != defaultGroupID {
		t.Errorf("default start GroupID = %q, want %q", g, defaultGroupID)
	}
	if len(root.Groups()) == 0 {
		t.Error("default: expected a help group to be registered")
	}

	// WithGroup(nil): ungrouped (empty GroupID).
	root = NewDaemon().FromCobra(newInner()).WithGroup(nil).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != "" {
		t.Errorf("WithGroup(nil) start GroupID = %q, want empty", g)
	}

	// WithGroup(&title): custom group.
	title := "Lifecycle"
	root = NewDaemon().FromCobra(newInner()).WithGroup(&title).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != defaultGroupID {
		t.Errorf("WithGroup(&title) start GroupID = %q, want %q", g, defaultGroupID)
	}
}

func TestBuildHasLifecycleCommands(t *testing.T) {
	root := NewDaemon().FromCobra(newInner()).DetachOn(nil)
	for _, name := range []string{"serve", "start", "stop", "status"} {
		if !hasCmd(root, name) {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestStateFileNames(t *testing.T) {
	// Explicit base (WithName path); files live under the cache dir.
	if pid, log := stateFiles("app", "custom"); !strings.HasSuffix(pid, "custom.pid") || !strings.HasSuffix(log, "custom.log") {
		t.Errorf("stateFiles(custom) = %q, %q", pid, log)
	}

	// Derived base: command name joined with its ancestors, root first.
	root := &cobra.Command{Use: "server"}
	child := &cobra.Command{Use: "serve"}
	root.AddCommand(child)
	if got := commandFileBase(child); got != "server-serve" {
		t.Errorf("commandFileBase = %q, want server-serve", got)
	}
}

func TestIntoCobraDoesNotMutateCommand(t *testing.T) {
	cmd := newInner()
	wantRunE := reflect.ValueOf(cmd.RunE).Pointer()
	wantUse := cmd.Use

	NewDaemon().FromCobra(cmd).WithReload(syscall.SIGHUP).DetachOn(nil)

	if reflect.ValueOf(cmd.RunE).Pointer() != wantRunE {
		t.Error("IntoCobra rewrote command.RunE")
	}
	if cmd.Use != wantUse {
		t.Errorf("IntoCobra changed command.Use to %q", cmd.Use)
	}
	if cmd.HasParent() {
		t.Error("IntoCobra attached the command to a parent")
	}
}

func TestIntoBuildsOnce(t *testing.T) {
	d := NewDaemon().FromCobra(newInner())
	if a, b := d.DetachOn(nil), d.DetachOn(nil); a != b {
		t.Error("Into should return the same command on repeated calls")
	}
}

func TestBuildPanicsWithoutCommand(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("build with nil command should panic")
		}
	}()
	NewDaemon().FromCobra(nil).DetachOn(nil)
}

// isolateCache points os.UserCacheDir at a temp dir for the test.
func isolateCache(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)           // darwin: $HOME/Library/Caches
	t.Setenv("XDG_CACHE_HOME", tmp) // other unix
}

// newDaemonFiles returns a daemon with state-file paths resolved under an
// isolated cache dir.
func newDaemonFiles(t *testing.T, base string) *DaemonImpl[any] {
	t.Helper()
	isolateCache(t)
	d := &DaemonImpl[any]{}
	d.pidFile, d.logFile = stateFiles("app", base)
	return d
}

func TestWriteReadPID(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.writePID(4321); err != nil {
		t.Fatal(err)
	}
	got, err := d.PID()
	if err != nil {
		t.Fatal(err)
	}
	if got != 4321 {
		t.Errorf("readPID = %d, want 4321", got)
	}
}

func TestReadPIDInvalid(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := os.WriteFile(d.pidFile, []byte("notanumber"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PID(); err == nil {
		t.Error("readPID on garbage: want error")
	}
}

func TestIsAlive(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if !d.IsAlive() {
		t.Error("self pid should be alive")
	}
	if err := d.writePID(deadPID(t)); err != nil {
		t.Fatal(err)
	}
	if d.IsAlive() {
		t.Error("reaped pid should not be alive")
	}
}

func TestEnsurePid(t *testing.T) {
	d := newDaemonFiles(t, "test")
	startGate := d.ensurePid(false) // start: must NOT be running
	stopGate := d.ensurePid(true)   // stop/reload: must be running

	// No pid file.
	if err := startGate(nil, nil); err != nil {
		t.Errorf("startGate, no pidfile: unexpected error %v", err)
	}
	if err := stopGate(nil, nil); err == nil {
		t.Error("stopGate, no pidfile: want error")
	}

	// Live process.
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := startGate(nil, nil); err == nil {
		t.Error("startGate, running: want error")
	}
	if err := stopGate(nil, nil); err != nil {
		t.Errorf("stopGate, running: unexpected error %v", err)
	}

	// Stale pid file (counts as not running).
	if err := d.writePID(deadPID(t)); err != nil {
		t.Fatal(err)
	}
	if err := startGate(nil, nil); err != nil {
		t.Errorf("startGate, stale: unexpected error %v", err)
	}
	if err := stopGate(nil, nil); err == nil {
		t.Error("stopGate, stale: want error")
	}
}

func TestStopReloadNotRunning(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.Stop(); err != nil { // stop is idempotent: no-op when not running
		t.Errorf("Stop with no pid file: unexpected error %v", err)
	}
	if err := d.Reload(); err == nil {
		t.Error("Reload with no pid file: want error")
	}
}

func TestStatusWithoutCobra(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.Status(); err != nil { // not running -> nil
		t.Errorf("Status not running: %v", err)
	}
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := d.Status(); err != nil { // running -> nil
		t.Errorf("Status running: %v", err)
	}
}

func TestStopLive(t *testing.T) {
	d := newDaemonFiles(t, "test")
	c := exec.Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	go c.Wait() // reap on death so the zombie doesn't fool the liveness check
	if err := d.writePID(c.Process.Pid); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if syscall.Kill(c.Process.Pid, 0) == nil {
		t.Error("process still alive after Stop")
	}
}

func TestForwardArgs(t *testing.T) {
	root := &cobra.Command{Use: "server"}
	start := &cobra.Command{Use: "start"}
	root.AddCommand(start)

	old := os.Args
	defer func() { os.Args = old }()
	os.Args = []string{"/path/to/server", "start", "-p", "9000", "extra"}

	if got, want := forwardArgs(start), []string{"-p", "9000", "extra"}; !equalStrs(got, want) {
		t.Errorf("forwardArgs = %v, want %v", got, want)
	}
}

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
