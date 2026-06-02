//go:build windows

package v1alpha1

import (
	"os"
	"testing"
)

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
