//go:build windows

package v1alpha1

import (
	"os"
	"os/exec"
	"testing"

	"golang.org/x/sys/windows"
)

// stalePID returns a pid whose process has exited, so isAlive reports false.
// Spawning cmd.exe with `exit 0` and waiting for it gives us a pid the
// liveness probe will refuse: GetExitCodeProcess returns 0 (not
// STILL_ACTIVE) and platformImpl.isAlive returns false.
func stalePID(t *testing.T) int {
	t.Helper()
	c := exec.Command("cmd.exe", "/c", "exit", "0")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	pid := c.Process.Pid
	_ = c.Wait()
	// Hold the handle open briefly so the kernel does not recycle the pid
	// before the test calls isAlive on it. WaitForSingleObject(0) just
	// confirms the wait state; we drop the handle right after.
	if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid)); err == nil {
		_, _ = windows.WaitForSingleObject(h, 0)
		_ = windows.CloseHandle(h)
	}
	return pid
}

// Phase 2 contract on Windows: Stop and startCobra work; IsAlive uses
// OpenProcess + GetExitCodeProcess; notifyParentReady leaves a sentinel
// file the parent's pollStartupState can stat.

func TestStopWhenNotRunning(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.Stop(); err != nil {
		t.Errorf("Stop with no pid file: unexpected error %v", err)
	}
}

func TestIsAliveSelf(t *testing.T) {
	d := newDaemonFiles(t, "test")
	if err := d.writePID(os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if !d.IsAlive() {
		t.Error("self pid should be alive via OpenProcess + GetExitCodeProcess")
	}
}

func TestNotifyParentReadyDoesNotPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("notifyParentReady panicked: %v", r)
		}
	}()
	d := newDaemonFiles(t, "test")
	d.notifyParentReady()

	// readyFile should now exist next to the pid file. The Windows platform
	// owns the path; type-assert to inspect it.
	wp, ok := d.platform.(*platformImpl)
	if !ok {
		t.Fatalf("expected *platformImpl, got %T", d.platform)
	}
	if _, err := os.Stat(wp.readyFile()); err != nil {
		t.Errorf("ready file missing after notifyParentReady: %v", err)
	}
}
