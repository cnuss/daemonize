//go:build !windows

// Unix implementation of the platform interface defined in cobra.go. The
// companion file is platform_windows.go; keep the two in step.

package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// platformImpl satisfies platform on Linux, macOS, and the *BSDs by routing
// every step through standard POSIX signal/syscall primitives.
//
// pidFile is unused on Unix at the moment — the readiness relay is SIGUSR1,
// not a file — but the field is here so the struct stays uniform with the
// Windows impl and a future ready-file fallback or Windows-style debug
// affordance doesn't require a struct surgery.
type platformImpl struct {
	pidFile string
	logFile string
}

// newPlatform builds the platform shim for non-Windows targets. Called from
// buildCobra after the state-file paths are resolved.
func newPlatform(pidFile, logFile string) platform {
	return &platformImpl{pidFile: pidFile, logFile: logFile}
}

func (p *platformImpl) applyDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func (p *platformImpl) installStartSignals() (chan os.Signal, func()) {
	sigCh := make(chan os.Signal, 4)
	signal.Notify(sigCh, syscall.SIGUSR1, syscall.SIGCHLD, syscall.SIGINT, syscall.SIGTERM)
	return sigCh, func() { signal.Stop(sigCh) }
}

func (p *platformImpl) waitForEarlyExit(sigCh chan os.Signal, pid int, serveName string) error {
	select {
	case s := <-sigCh:
		if s == syscall.SIGCHLD {
			var ws syscall.WaitStatus
			if wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
				return fmt.Errorf("%s exited during startup (see %s)", serveName, p.logFile)
			}
		}
	case <-time.After(100 * time.Millisecond):
	}
	return nil
}

func (p *platformImpl) killChildOnInterrupt(cmd *exec.Cmd, sigCh chan os.Signal, pid int, serveName string) {
	fmt.Printf("\nstartup interrupted; stopping %s (pid %d)...\n", serveName, pid)
	_ = syscall.Kill(pid, syscall.SIGTERM)

	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	for {
		select {
		case <-done:
			return
		case s := <-sigCh:
			if s == syscall.SIGINT || s == syscall.SIGTERM {
				fmt.Printf("forcing kill (pid %d)\n", pid)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				<-done
				return
			}
		}
	}
}

func (p *platformImpl) afterStartupReady() {}

func (p *platformImpl) pollStartupState(sigCh chan os.Signal, pid int) (startResult, bool) {
	select {
	case s := <-sigCh:
		switch s {
		case syscall.SIGUSR1:
			return startReady, true
		case syscall.SIGINT, syscall.SIGTERM:
			return startInterrupted, true
		case syscall.SIGCHLD:
			var ws syscall.WaitStatus
			if wpid, _ := syscall.Wait4(pid, &ws, syscall.WNOHANG, nil); wpid == pid {
				return startFailed, true
			}
		}
	default:
	}
	return 0, false
}

func (p *platformImpl) isAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func (p *platformImpl) notifyParentReady(ppid int) {
	syscall.Kill(ppid, syscall.SIGUSR1)
}

// installShutdownListener is a no-op on Unix because POSIX signals already
// drive the worker's shutdown — signal.NotifyContext (or the worker's own
// signal.Notify) handles SIGTERM directly. Windows has no equivalent for a
// detached child, so it implements a named-pipe shim instead.
func (p *platformImpl) installShutdownListener(_ context.CancelFunc) {}

func (p *platformImpl) stop(pid int, tickTail func()) error {
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
		tickTail()
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		select {
		case <-sigCh:
			tickTail()
			_ = syscall.Kill(pid, syscall.SIGKILL)
			fmt.Printf("\nkilled (pid %d) on interrupt\n", pid)
			return nil
		case <-time.After(stopPollEach):
		}
	}
}
