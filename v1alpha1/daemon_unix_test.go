//go:build !windows

package v1alpha1

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

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

// stalePID is the cross-platform name used by daemon_test.go. On Unix it
// delegates to deadPID; the Windows side has its own implementation in
// daemon_windows_test.go.
func stalePID(t *testing.T) int { return deadPID(t) }

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
	stopGate := d.ensurePid(true)   // stop: must be running

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

func TestStopNotRunning(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.Stop(); err != nil { // stop is idempotent: no-op when not running
		t.Errorf("Stop with no pid file: unexpected error %v", err)
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
