package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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
	if out, err := exec.Command("go", "build", "-o", bin, "../examples/"+name).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, out)
	}
	r := &runner{name: name, bin: bin, home: t.TempDir()}
	t.Cleanup(func() { _ = r.execRaw("stop") }) // best-effort: never leave a daemon running
	return r
}

// run executes the example with args, logs the command + output (visible under
// `go test -v`), and returns combined output. Exit status is ignored; tests
// assert on output (some commands exit non-zero by design).
func (r *runner) run(t *testing.T, args ...string) string {
	t.Helper()
	out := r.execRaw(args...)
	t.Logf("$ %s %s\n%s", r.name, strings.Join(args, " "), out)
	return out
}

func (r *runner) execRaw(args ...string) string {
	c := exec.Command(r.bin, args...)
	c.Env = append(os.Environ(), "HOME="+r.home, "XDG_CACHE_HOME="+r.home)
	out, _ := c.CombinedOutput()
	return string(out)
}

// runInterrupt starts the example asynchronously, sends SIGINT at each of the
// given offsets (measured from launch), then waits for it to exit and returns
// the combined output. Used to exercise Ctrl+C escalation paths.
func (r *runner) runInterrupt(t *testing.T, args []string, sendAt ...time.Duration) string {
	t.Helper()
	c := exec.Command(r.bin, args...)
	c.Env = append(os.Environ(), "HOME="+r.home, "XDG_CACHE_HOME="+r.home)
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
		_ = c.Process.Signal(syscall.SIGINT)
	}
	_ = c.Wait()
	out := buf.String()
	t.Logf("$ %s %s (interrupts: %v)\n%s", r.name, strings.Join(args, " "), sendAt, out)
	return out
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

// reloadExamples support the full lifecycle including reload.
var reloadExamples = []string{"reload", "named", "grouped", "ungrouped"}

func TestLifecycle(t *testing.T) {
	for _, name := range reloadExamples {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t, name)

			start := r.run(t, "start", "-m", "hi")
			wants(t, start, "ready: hi", "started") // -m flag forwarded to the child

			wants(t, r.run(t, "status"), "running")
			wants(t, r.run(t, "reload"), "reload signal sent")

			stop := r.run(t, "stop")
			wants(t, stop, "stopping", "stopped") // worker line (streamed) + daemon line

			wants(t, r.run(t, "status"), "not running")
		})
	}
}

func TestStopIdempotent(t *testing.T) {
	r := newRunner(t, "hello")
	// stop when never started: no error, exit 0 path prints "not running".
	wants(t, r.run(t, "stop"), "not running")
}

func TestHelloHasNoReload(t *testing.T) {
	r := newRunner(t, "hello")
	// hello didn't call WithReload, so the reload subcommand should not be
	// attached. (Invoking `reload` would now be a positional under the
	// default ArbitraryArgs validator, so we check the help output instead.)
	rejects(t, r.run(t, "--help"), "reload")
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

func TestSlowShutdownStreams(t *testing.T) {
	r := newRunner(t, "slow-shutdown")
	wants(t, r.run(t, "start", "--step", "10ms"), "started")
	out := r.run(t, "stop")
	wants(t, out, "shutdown [1/3]", "shutdown [3/3]", "stopped cleanly")
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
	// The worker has no signal handling: stop relies on SIGTERM's default kill,
	// so there's no "stopping" line.
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

func TestShutdownError(t *testing.T) {
	r := newRunner(t, "shutdown-error")
	wants(t, r.run(t, "start"), "started")
	out := r.run(t, "stop")
	wants(t, out, "shutdown error", "stopped") // failure streamed, but still stops
	wants(t, r.run(t, "status"), "not running")
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

func TestStartInterruptGraceful(t *testing.T) {
	r := newRunner(t, "slow-start")

	// Single Ctrl+C while start is still streaming startup output sends SIGTERM
	// to the child; the slow-start worker's signal handler picks that up,
	// prints "stopping", and exits cleanly. No SIGKILL escalation needed.
	out := r.runInterrupt(t, []string{"start", "--step", "200ms"}, 300*time.Millisecond)
	wants(t, out, "startup interrupted", "startup cancelled")
	rejects(t, out, "forcing kill")
	wants(t, r.run(t, "status"), "not running")
}

func TestStartInterruptEscalates(t *testing.T) {
	r := newRunner(t, "stubborn")

	// stubborn ignores SIGTERM; the first Ctrl+C during start triggers SIGTERM
	// (which the worker drops), and a second Ctrl+C escalates to SIGKILL.
	// stubborn delays close(ready) by ~2s to keep the parent in
	// streamUntilReady long enough for both interrupts to land mid-stream.
	out := r.runInterrupt(t, []string{"start"},
		400*time.Millisecond, 900*time.Millisecond)
	wants(t, out, "startup interrupted", "forcing kill", "startup cancelled")
	wants(t, r.run(t, "status"), "not running")
}

func TestStopInterruptEscalates(t *testing.T) {
	r := newRunner(t, "stubborn")

	wants(t, r.run(t, "start"), "ready", "started")

	// stop sends SIGTERM and waits forever; stubborn ignores it. A single
	// Ctrl+C during the wait escalates to SIGKILL and reports the kill.
	out := r.runInterrupt(t, []string{"stop"}, 300*time.Millisecond)
	wants(t, out, "shutting down", "killed", "on interrupt")
	wants(t, r.run(t, "status"), "not running")
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
