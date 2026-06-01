package v1alpha1

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

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
