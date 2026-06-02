//go:build !windows

package e2e

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

// configureInterruptable on Unix is a no-op — Process.Signal(SIGINT) reaches
// the target regardless of process group.
func configureInterruptable(_ *exec.Cmd) {}

// sendInterrupt delivers Ctrl+C via the POSIX signal path.
func sendInterrupt(p *os.Process) error { return p.Signal(syscall.SIGINT) }

// killPID kills a process by pid, out-of-band from anything daemonize does.
// Used by the stale-pid e2e test to simulate a worker crashing without
// going through Stop.
func killPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }

// waitForExit polls until the kernel reports the pid as dead, or gives up
// after a couple of seconds. Used after killPID to make tests deterministic.
func waitForExit(pid int) {
	for i := 0; i < 100; i++ {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
