package v1alpha1

import (
	"os"
	"reflect"
	"strings"
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

func TestGrouping(t *testing.T) {
	// Default: lifecycle commands grouped under defaultGroupID.
	root := New[any]().FromCobra(newInner()).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != defaultGroupID {
		t.Errorf("default start GroupID = %q, want %q", g, defaultGroupID)
	}
	if len(root.Groups()) == 0 {
		t.Error("default: expected a help group to be registered")
	}

	// WithGroup(nil): ungrouped (empty GroupID).
	root = New[any]().FromCobra(newInner()).WithGroup(nil).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != "" {
		t.Errorf("WithGroup(nil) start GroupID = %q, want empty", g)
	}

	// WithGroup(&title): custom group.
	title := "Lifecycle"
	root = New[any]().FromCobra(newInner()).WithGroup(&title).DetachOn(nil)
	if g := cmdByName(root, "start").GroupID; g != defaultGroupID {
		t.Errorf("WithGroup(&title) start GroupID = %q, want %q", g, defaultGroupID)
	}
}

func TestBuildHasLifecycleCommands(t *testing.T) {
	root := New[any]().FromCobra(newInner()).DetachOn(nil)
	for _, name := range []string{"start", "stop", "status"} {
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

func TestDetachOnEnrichesCommand(t *testing.T) {
	cmd := newInner()
	origRunE := reflect.ValueOf(cmd.RunE).Pointer()
	origUse := cmd.Use

	got := New[any]().FromCobra(cmd).DetachOn(nil)

	// DetachOn returns the same command, now enriched with lifecycle subcommands.
	if got != cmd {
		t.Error("DetachOn should return the same command it was given")
	}
	if cmd.Use != origUse {
		t.Errorf("DetachOn changed command.Use to %q", cmd.Use)
	}
	if reflect.ValueOf(cmd.RunE).Pointer() == origRunE {
		t.Error("DetachOn should have wrapped command.RunE")
	}
	for _, name := range []string{"start", "stop", "status"} {
		if !hasCmd(cmd, name) {
			t.Errorf("DetachOn did not attach %q as a subcommand", name)
		}
	}
}

func TestIntoBuildsOnce(t *testing.T) {
	d := New[any]().FromCobra(newInner())
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
	New[any]().FromCobra(nil).DetachOn(nil)
}

// isolateCache points os.UserCacheDir at a temp dir for the test.
func isolateCache(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)           // darwin: $HOME/Library/Caches
	t.Setenv("XDG_CACHE_HOME", tmp) // other unix
	t.Setenv("LOCALAPPDATA", tmp)   // windows
}

// newDaemonFiles returns a daemon with state-file paths resolved under an
// isolated cache dir.
func newDaemonFiles(t *testing.T, base string) *DaemonImpl[any] {
	t.Helper()
	isolateCache(t)
	d := &DaemonImpl[any]{}
	d.pidFile, d.logFile = stateFiles("app", base)
	d.platform = newPlatform(d.pidFile, d.logFile)
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

func TestStatusWithoutCobra(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.Status(nil); err != nil { // not running -> nil
		t.Errorf("Status not running: %v", err)
	}
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if err := d.Status(nil); err != nil { // running -> nil
		t.Errorf("Status running: %v", err)
	}
}

// FuzzDaemonEnvFor checks the env-var derivation's invariants under arbitrary
// input: the result always carries the "_DAEMON" suffix, has no hyphens (they
// must be normalized to underscores), and is upper-case.
func FuzzDaemonEnvFor(f *testing.F) {
	for _, seed := range []string{"hello", "widget", "my-server", "with-dashes", "UPPER", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, base string) {
		got := daemonEnvFor(base)
		if !strings.HasSuffix(got, "_DAEMON") {
			t.Errorf("daemonEnvFor(%q) = %q, want suffix _DAEMON", base, got)
		}
		if strings.Contains(got, "-") {
			t.Errorf("daemonEnvFor(%q) = %q, contains hyphen", base, got)
		}
		if got != strings.ToUpper(got) {
			t.Errorf("daemonEnvFor(%q) = %q, want all uppercase", base, got)
		}
	})
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

// Error-message contracts used by the lifecycle exit-code suite (#16). The
// e2e suite asserts the *exit code*; these tests pin the surface text so a
// refactor of cobra.go can't silently change the message users grep for.

func TestEnsurePidErrorIncludesPid(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	err := d.ensurePid(false)(nil, nil) // start gate against a live pid
	if err == nil {
		t.Fatal("ensurePid(false) on live pid: want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "already running") {
		t.Errorf("error message %q missing 'already running'", msg)
	}
	if !strings.Contains(msg, "(pid ") {
		t.Errorf("error message %q missing pid", msg)
	}
}

func TestEnsurePidNotRunningMessage(t *testing.T) {
	d := newDaemonFiles(t, "test")
	err := d.ensurePid(true)(nil, nil) // stop gate, no pid file
	if err == nil {
		t.Fatal("ensurePid(true) without pid file: want error")
	}
	if got, want := err.Error(), "not running"; got != want {
		t.Errorf("error message = %q, want %q", got, want)
	}
}

func TestStatusOutputFormatSet(t *testing.T) {
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"text", false},
		{"json", false},
		{"", true},
		{"yaml", true},
		{"TEXT", true}, // case-sensitive on purpose; users typo lowercase
		{"text,json", true},
	}
	for _, tc := range cases {
		var f statusOutputFormat
		err := f.Set(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("Set(%q) error = %v, wantErr = %v", tc.in, err, tc.wantErr)
		}
		if err != nil && !strings.Contains(err.Error(), "must be one of: text, json") {
			t.Errorf("Set(%q) error = %q, want it to mention the valid set", tc.in, err)
		}
	}
}

func TestStopStaleRemovesPidFile(t *testing.T) {
	d := newDaemonFiles(t, "test")
	// Plant a pid file pointing at a pid that does not exist on either OS.
	// PID 1 belongs to init/launchd, but writing it and then stopping would
	// actually try to signal it; use a sentinel that is guaranteed to fail
	// the liveness probe instead.
	if err := d.writePID(stalePID(t)); err != nil {
		t.Fatal(err)
	}
	if err := d.Stop(); err != nil {
		t.Fatalf("Stop with stale pid: unexpected error %v", err)
	}
	if _, err := os.Stat(d.pidFile); !os.IsNotExist(err) {
		t.Errorf("pid file should be removed after stale Stop; stat err = %v", err)
	}
}
