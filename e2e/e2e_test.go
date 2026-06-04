package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// runner builds an example binary once and runs its subcommands against an
// isolated cache dir (HOME/XDG_CACHE_HOME), so pid/log files never collide.
type runner struct {
	name string
	bin  string
	home string
}

func newRunner(t *testing.T, name string) *runner {
	t.Helper()
	bin := filepath.Join(t.TempDir(), name)
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "../examples/"+name).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	r := &runner{name: name, bin: bin, home: t.TempDir()}
	t.Cleanup(func() { _, _ = r.execRaw("stop") }) // best-effort: never leave a daemon running
	return r
}

// run executes the example with args, logs the command + output (visible under
// `go test -v`), and returns combined output. Exit status is ignored; tests
// that need the exit code use runC instead.
func (r *runner) run(t *testing.T, args ...string) string {
	t.Helper()
	out, _ := r.runC(t, args...)
	return out
}

// runC is the exit-code-aware variant: returns (output, exitCode). Code is
// the process's exit status as reported by exec.Cmd.ProcessState; -1 if the
// command could not be started. A signal-terminated child reports -1 too —
// callers that care about signals should use runInterrupt.
func (r *runner) runC(t *testing.T, args ...string) (string, int) {
	t.Helper()
	out, code := r.execRaw(args...)
	t.Logf("$ %s %s (exit %d)\n%s", r.name, strings.Join(args, " "), code, out)
	return out, code
}

func (r *runner) execRaw(args ...string) (string, int) {
	c := exec.Command(r.bin, args...)
	// HOME drives os.UserCacheDir on darwin; XDG_CACHE_HOME on linux/*bsd;
	// LOCALAPPDATA on windows. Setting all three keeps every test isolated
	// regardless of the runner OS.
	c.Env = append(os.Environ(),
		"HOME="+r.home,
		"XDG_CACHE_HOME="+r.home,
		"LOCALAPPDATA="+r.home,
	)
	out, err := c.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	return string(out), code
}

// runInterrupt starts the example asynchronously, sends a Ctrl+C signal at
// each of the given offsets (measured from launch), then waits for it to
// exit and returns the combined output. configureInterruptable + sendInterrupt
// are platform-specific shims (see interrupt_unix.go / interrupt_windows.go)
// that pick the right OS primitive — POSIX SIGINT on Unix,
// GenerateConsoleCtrlEvent(CTRL_BREAK_EVENT) on Windows.
func (r *runner) runInterrupt(t *testing.T, args []string, sendAt ...time.Duration) string {
	t.Helper()
	out, _ := r.runInterruptC(t, args, sendAt...)
	return out
}

// runInterruptC is the exit-code-aware variant of runInterrupt.
func (r *runner) runInterruptC(t *testing.T, args []string, sendAt ...time.Duration) (string, int) {
	t.Helper()
	c := exec.Command(r.bin, args...)
	configureInterruptable(c)
	c.Env = append(os.Environ(),
		"HOME="+r.home,
		"XDG_CACHE_HOME="+r.home,
		"LOCALAPPDATA="+r.home,
	)
	var buf bytes.Buffer
	c.Stdout = &buf
	c.Stderr = &buf
	if err := c.Start(); err != nil {
		t.Fatalf("start %s %v: %v", r.name, args, err)
	}
	started := time.Now()
	for _, when := range sendAt {
		if d := time.Until(started.Add(when)); d > 0 {
			time.Sleep(d)
		}
		_ = sendInterrupt(c.Process)
	}
	err := c.Wait()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = -1
		}
	}
	out := buf.String()
	t.Logf("$ %s %s (interrupts: %v, exit %d)\n%s", r.name, strings.Join(args, " "), sendAt, code, out)
	return out, code
}

func wants(t *testing.T, out string, subs ...string) {
	t.Helper()
	for _, s := range subs {
		if !strings.Contains(out, s) {
			t.Errorf("output missing %q in:\n%s", s, out)
		}
	}
}

func rejects(t *testing.T, out, sub string) {
	t.Helper()
	if strings.Contains(out, sub) {
		t.Errorf("output unexpectedly contains %q in:\n%s", sub, out)
	}
}

// lifecycleExamples drive start/status/stop end-to-end across the
// representative example binaries. All three use WithShutdownSignal +
// <-cmd.Context().Done() so the worker's "stopping" line lands on both
// Unix (SIGTERM) and Windows (named-pipe shutdown signal).
var lifecycleExamples = []string{"named", "grouped", "ungrouped"}

func TestLifecycle(t *testing.T) {
	for _, name := range lifecycleExamples {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t, name)

			start := r.run(t, "start", "-m", "hi")
			wants(t, start, "ready: hi", "started") // -m flag forwarded to the child

			wants(t, r.run(t, "status"), "running")

			stop := r.run(t, "stop")
			wants(t, stop, "stopping", "stopped") // worker line (streamed) + daemon line

			wants(t, r.run(t, "status"), "not running")
		})
	}
}

func TestSlowShutdownStreams(t *testing.T) {
	r := newRunner(t, "slow-shutdown")
	wants(t, r.run(t, "start", "--step", "10ms"), "started")
	out := r.run(t, "stop")
	wants(t, out, "shutdown [1/3]", "shutdown [3/3]", "stopped cleanly")
}

func TestShutdownError(t *testing.T) {
	r := newRunner(t, "shutdown-error")
	wants(t, r.run(t, "start"), "started")
	out := r.run(t, "stop")
	wants(t, out, "shutdown error", "stopped") // failure streamed, but still stops
	wants(t, r.run(t, "status"), "not running")
}

func TestStopIdempotent(t *testing.T) {
	r := newRunner(t, "hello")
	// stop when never started: no error, exit 0 path prints "not running".
	wants(t, r.run(t, "stop"), "not running")
}

func TestNamedStateFile(t *testing.T) {
	r := newRunner(t, "named")
	wants(t, r.run(t, "start"), "widget.pid") // WithName("widget")
}

func TestGroupedHelp(t *testing.T) {
	wants(t, newRunner(t, "grouped").run(t, "--help"), "Lifecycle:")
}

func TestUngroupedHelp(t *testing.T) {
	out := newRunner(t, "ungrouped").run(t, "--help")
	rejects(t, out, "Daemon Commands:") // WithGroup(nil)
	wants(t, out, "start", "stop", "status")
}

func TestSlowStartStreams(t *testing.T) {
	r := newRunner(t, "slow-start")
	out := r.run(t, "start", "--step", "10ms")
	wants(t, out, "startup [1/3]", "startup [3/3]", "ready", "started")
}

func TestWithArgs(t *testing.T) {
	r := newRunner(t, "with-args")
	out := r.run(t, "start", "--port", "9000", "-v", "alpha", "beta")
	wants(t, out, "config: port=9000 verbose=true args=[alpha beta]")
}

func TestSubcommand(t *testing.T) {
	r := newRunner(t, "subcommand")

	// Plain sibling subcommands.
	wants(t, r.run(t, "init"), "initialized")
	wants(t, r.run(t, "create"), "created")
	wants(t, r.run(t, "delete"), "deleted")

	// The "run" subtree is the daemonized one (mounted under the app root).
	// The worker has no signal handling: stop relies on the OS-level
	// terminate (SIGTERM default kill on Unix; TerminateProcess on Windows),
	// so there is no "stopping" line from the worker side.
	wants(t, r.run(t, "run", "start"), "started")
	wants(t, r.run(t, "run", "status"), "running")
	wants(t, r.run(t, "run", "stop"), "stopped")
	wants(t, r.run(t, "run", "status"), "not running")
}

func TestStartError(t *testing.T) {
	r := newRunner(t, "start-error")
	out := r.run(t, "start")
	wants(t, out, "exited during startup")      // daemon detected the failed child
	wants(t, r.run(t, "status"), "not running") // nothing left behind
}

func TestPidCleanup(t *testing.T) {
	r := newRunner(t, "pid-cleanup")

	// The worker never closes ready; it sleeps 5s, prints "hello world",
	// and returns. The daemon treats that as "exited during startup", but
	// the pid file should still be removed and the log file should have
	// captured the worker's output.
	out := r.run(t, "start")
	wants(t, out, "hello world", "exited during startup")

	logPath := between(out, "exited during startup (see ", ")")
	if logPath == "" {
		t.Fatalf("could not find log path in output:\n%s", out)
	}
	pidPath := strings.TrimSuffix(logPath, ".log") + ".pid"

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file %s should be cleaned up after exit, stat err = %v", pidPath, err)
	}

	contents, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log %s: %v", logPath, err)
	}
	if !strings.Contains(string(contents), "hello world") {
		t.Errorf("log file %s missing \"hello world\":\n%s", logPath, contents)
	}
}

func TestStatusShowsLogFile(t *testing.T) {
	r := newRunner(t, "hello")
	wants(t, r.run(t, "start", "-m", "world"), "started")
	out := r.run(t, "status")
	wants(t, out, "running (pid ", "pid file:", "log file:")
}

func TestStatusJSONRunning(t *testing.T) {
	r := newRunner(t, "hello")
	wants(t, r.run(t, "start", "-m", "world"), "started")

	out := r.run(t, "status", "-o", "json")
	var s map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &s); err != nil {
		t.Fatalf("status -o json did not parse:\n%s\n%v", out, err)
	}
	if s["state"] != "running" {
		t.Errorf("state = %v, want \"running\"", s["state"])
	}
	if pid, ok := s["pid"].(float64); !ok || pid <= 0 {
		t.Errorf("pid = %v, want positive number", s["pid"])
	}
	for _, k := range []string{"name", "pid_file", "log_file"} {
		if v, ok := s[k].(string); !ok || v == "" {
			t.Errorf("%q = %v, want non-empty string", k, s[k])
		}
	}
}

func TestStatusJSONNotRunning(t *testing.T) {
	r := newRunner(t, "hello")
	out := r.run(t, "status", "-o", "json")
	var s map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &s); err != nil {
		t.Fatalf("status -o json did not parse:\n%s\n%v", out, err)
	}
	if s["state"] != "not_running" {
		t.Errorf("state = %v, want \"not_running\"", s["state"])
	}
	if _, present := s["pid"]; present {
		t.Errorf("pid should be omitted when not running, got %v", s["pid"])
	}
}

func TestStartInterruptGraceful(t *testing.T) {
	r := newRunner(t, "slow-start")

	// Single Ctrl+C while start is still streaming startup output sends the
	// child a graceful-shutdown signal — SIGTERM on Unix, the named-pipe
	// byte on Windows (delivered because slow-start uses WithShutdownSignal).
	// The worker prints "stopping" and exits cleanly; no escalation step.
	out := r.runInterrupt(t, []string{"start", "--step", "200ms"}, 300*time.Millisecond)
	wants(t, out, "startup interrupted", "startup cancelled")
	rejects(t, out, "forcing kill")
	wants(t, r.run(t, "status"), "not running")
}

func TestStartInterruptEscalates(t *testing.T) {
	r := newRunner(t, "stubborn")

	// stubborn doesn't honor the graceful shutdown signal — on Unix it
	// installs a no-op SIGTERM handler; on Windows it doesn't configure
	// WithShutdownSignal so the pipe listener's cancel is a no-op. Either
	// way the first Ctrl+C during start triggers the graceful path
	// (ignored), and the second escalates to a hard kill. stubborn delays
	// close(ready) by ~2s to keep the parent in streamUntilReady long
	// enough for both interrupts to land mid-stream.
	out := r.runInterrupt(t, []string{"start"},
		400*time.Millisecond, 900*time.Millisecond)
	wants(t, out, "startup interrupted", "forcing kill", "startup cancelled")
	wants(t, r.run(t, "status"), "not running")
}

func TestStopInterruptEscalates(t *testing.T) {
	r := newRunner(t, "stubborn")

	wants(t, r.run(t, "start"), "ready", "started")

	// stop signals the worker (SIGTERM on Unix, pipe byte on Windows) and
	// waits forever; stubborn ignores both, so a single Ctrl+C in the stop
	// console escalates to a hard kill and reports it.
	out := r.runInterrupt(t, []string{"stop"}, 300*time.Millisecond)
	wants(t, out, "shutting down", "killed", "on interrupt")
	wants(t, r.run(t, "status"), "not running")
}

func TestStatusJSONStale(t *testing.T) {
	r := newRunner(t, "hello")
	wants(t, r.run(t, "start", "-m", "world"), "started")

	// Pull the worker's pid out of the status text, then kill it out-of-band
	// so the pid file is left stale. killPID + waitForExit are
	// platform-specific shims (Unix: syscall.Kill + signal-0 probe;
	// Windows: os.Process.Kill / OpenProcess + WaitForSingleObject).
	pidStr := between(r.run(t, "status"), "running (pid ", ")")
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		t.Fatalf("parse pid from status: %v", err)
	}
	if err := killPID(pid); err != nil {
		t.Fatalf("kill %d: %v", pid, err)
	}
	waitForExit(pid)

	out := r.run(t, "status", "-o", "json")
	var s map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &s); err != nil {
		t.Fatalf("status -o json did not parse:\n%s\n%v", out, err)
	}
	if s["state"] != "stale" {
		t.Errorf("state = %v, want \"stale\"", s["state"])
	}

	// The pid file should now be cleared; a follow-up status reports not_running.
	out = r.run(t, "status", "-o", "json")
	_ = json.Unmarshal([]byte(strings.TrimSpace(out)), &s)
	if s["state"] != "not_running" {
		t.Errorf("after stale cleanup, state = %v, want \"not_running\"", s["state"])
	}
}

func TestStatusUnknownFormat(t *testing.T) {
	r := newRunner(t, "hello")
	out := r.run(t, "status", "-o", "yaml")
	// pflag.Value's Set returns the error before RunE; cobra wraps it as
	// "invalid argument ... must be one of: text, json".
	wants(t, out, "invalid argument", "must be one of: text, json")
}

// between returns the substring of s that sits between the first occurrence
// of prefix and the next occurrence of suffix after it, or "" if either is
// missing.
func between(s, prefix, suffix string) string {
	_, rest, ok := strings.Cut(s, prefix)
	if !ok {
		return ""
	}
	mid, _, ok := strings.Cut(rest, suffix)
	if !ok {
		return ""
	}
	return mid
}

// wantExit fails the test (without aborting) if got != want, with the output
// for context.
func wantExit(t *testing.T, want, got int, out string) {
	t.Helper()
	if got != want {
		t.Errorf("exit code = %d, want %d. output:\n%s", got, want, out)
	}
}

// Exit-code contract across the lifecycle. One scenario per test so a
// regression points at exactly the failed transition. See #16.

func TestExitStartSuccess(t *testing.T) {
	r := newRunner(t, "hello")
	_, code := r.runC(t, "start")
	wantExit(t, 0, code, "")
}

func TestExitStartAlreadyRunning(t *testing.T) {
	r := newRunner(t, "hello")
	r.run(t, "start")
	out, code := r.runC(t, "start")
	wantExit(t, 1, code, out)
	wants(t, out, "already running")
}

func TestExitStartWorkerExited(t *testing.T) {
	r := newRunner(t, "start-error")
	out, code := r.runC(t, "start")
	wantExit(t, 1, code, out)
	wants(t, out, "exited during startup")
}

func TestExitStartInterrupted(t *testing.T) {
	r := newRunner(t, "slow-start")
	out, code := r.runInterruptC(t,
		[]string{"start", "--step", "200ms"}, 300*time.Millisecond)
	wantExit(t, 1, code, out)
	wants(t, out, "startup cancelled")
}

func TestExitStopGraceful(t *testing.T) {
	r := newRunner(t, "hello")
	r.run(t, "start")
	out, code := r.runC(t, "stop")
	wantExit(t, 0, code, out)
	wants(t, out, "stopped")
}

func TestExitStopNotRunning(t *testing.T) {
	r := newRunner(t, "hello")
	out, code := r.runC(t, "stop")
	wantExit(t, 0, code, out)
	wants(t, out, "not running")
}

func TestExitStopStalePid(t *testing.T) {
	r := newRunner(t, "hello")
	r.run(t, "start")
	pid := pidFromStatus(t, r)
	if err := killPID(pid); err != nil {
		t.Fatalf("killPID(%d): %v", pid, err)
	}
	waitForExit(pid)
	out, code := r.runC(t, "stop")
	wantExit(t, 0, code, out)
	wants(t, out, "cleared stale pid")
}

func TestExitStopWorkerShutdownErrored(t *testing.T) {
	r := newRunner(t, "shutdown-error")
	r.run(t, "start")
	out, code := r.runC(t, "stop")
	// Worker errored during teardown, but stop still completes — pid file is
	// gone, exit 0. The worker's error line is streamed for visibility.
	wantExit(t, 0, code, out)
	wants(t, out, "shutdown error", "stopped")
}

func TestExitStopInterruptEscalated(t *testing.T) {
	r := newRunner(t, "stubborn")
	r.run(t, "start")
	out, code := r.runInterruptC(t,
		[]string{"stop"}, 300*time.Millisecond)
	// Forced kill via Ctrl+C escalation still exits 0 from the parent —
	// the daemon successfully terminated its child.
	wantExit(t, 0, code, out)
	wants(t, out, "killed", "on interrupt")
}

func TestExitStatusRunning(t *testing.T) {
	r := newRunner(t, "hello")
	r.run(t, "start")
	_, code := r.runC(t, "status")
	wantExit(t, 0, code, "")
}

func TestExitStatusNotRunning(t *testing.T) {
	r := newRunner(t, "hello")
	_, code := r.runC(t, "status")
	wantExit(t, 0, code, "")
}

func TestExitStatusStalePid(t *testing.T) {
	r := newRunner(t, "hello")
	r.run(t, "start")
	pid := pidFromStatus(t, r)
	if err := killPID(pid); err != nil {
		t.Fatalf("killPID(%d): %v", pid, err)
	}
	waitForExit(pid)
	out, code := r.runC(t, "status")
	wantExit(t, 0, code, out)
	wants(t, out, "stale")
}

func TestExitStatusInvalidFormat(t *testing.T) {
	r := newRunner(t, "hello")
	out, code := r.runC(t, "status", "--output=yaml")
	wantExit(t, 1, code, out)
}

func TestExitInvalidFlag(t *testing.T) {
	r := newRunner(t, "hello")
	// cobra rejects unknown flags before any RunE; exits 1.
	// Note: unknown POSITIONAL args are not rejected — buildCobra defaults
	// command.Args to cobra.ArbitraryArgs so they forward to the worker
	// (see TestWithArgs).
	out, code := r.runC(t, "--no-such-flag")
	wantExit(t, 1, code, out)
}

// pidFromStatus reads the daemon pid by parsing JSON status output. Used by
// the stale-pid tests so they don't have to know where the pid file lives.
func pidFromStatus(t *testing.T, r *runner) int {
	t.Helper()
	out, code := r.runC(t, "status", "-o", "json")
	if code != 0 {
		t.Fatalf("status -o json: exit %d, output: %s", code, out)
	}
	var s struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &s); err != nil {
		t.Fatalf("parse status JSON: %v\n%s", err, out)
	}
	if s.PID == 0 {
		t.Fatalf("status JSON had no pid: %s", out)
	}
	return s.PID
}
