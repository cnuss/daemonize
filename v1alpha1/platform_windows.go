//go:build windows

// Windows implementation of the platform interface defined in cobra.go. The
// companion file is platform.go (//go:build !windows); keep the two in step.
//
// Notes on the chosen primitives:
//
//   - Detach: exec.Command + SysProcAttr{CreationFlags: DETACHED_PROCESS |
//     CREATE_NEW_PROCESS_GROUP, HideWindow: true}.
//   - Readiness relay: the daemon child touches a "<base>.ready" sentinel
//     file alongside the pid file; the parent's pollStartupState stats for
//     it.
//   - Graceful shutdown: a named pipe at \\.\pipe\daemonize-<base>. The
//     child opens it as a server in installShutdownListener; the parent's
//     Stop writes a single byte to it and waits for the child to exit. When
//     the byte arrives the listener goroutine calls the cancel func that
//     was returned by signal.NotifyContext (via WithShutdownSignal), and
//     the worker's <-cmd.Context().Done() fires the same as a SIGTERM
//     would on Unix.
//   - Liveness: OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION) +
//     GetExitCodeProcess; STILL_ACTIVE (259) means running.
//   - Force kill (Ctrl+C escalation, no pipe listener): OpenProcess
//     (PROCESS_TERMINATE | SYNCHRONIZE) + TerminateProcess +
//     WaitForSingleObject. The child gets no defer execution along this
//     path, which is the same as the Unix SIGKILL escalation.

package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// stillActive is the exit code GetExitCodeProcess returns for a process that
// has not yet exited (winnt.h: STILL_ACTIVE = STATUS_PENDING = 259).
const stillActive = 259

// platformImpl satisfies platform on Windows.
type platformImpl struct {
	pidFile string // for ready-file derivation
	logFile string // for "exited during startup" error messages
}

// newPlatform builds the platform shim for Windows. Called from buildCobra
// once the state-file paths are resolved.
func newPlatform(pidFile, logFile string) platform {
	return &platformImpl{pidFile: pidFile, logFile: logFile}
}

// readyFile is the path the child touches when it becomes ready, derived
// from the pid-file path so it sits in the same state-file directory.
func (p *platformImpl) readyFile() string {
	return strings.TrimSuffix(p.pidFile, ".pid") + ".ready"
}

func (p *platformImpl) applyDetachAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
		HideWindow:    true,
	}
}

func (p *platformImpl) installStartSignals() (chan os.Signal, func()) {
	// Clear any stale ready sentinel up front so a previous run's file
	// doesn't fool pollStartupState into reporting success immediately.
	_ = os.Remove(p.readyFile())

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	return sigCh, func() { signal.Stop(sigCh) }
}

func (p *platformImpl) waitForEarlyExit(_ chan os.Signal, pid int, serveName string) error {
	time.Sleep(100 * time.Millisecond)
	if !p.isAlive(pid) {
		return fmt.Errorf("%s exited during startup (see %s)", serveName, p.logFile)
	}
	return nil
}

// killChildOnInterrupt mirrors the Unix two-step escalation:
//
//  1. Try the graceful shutdown signal (named-pipe byte). If the child
//     listens and its WithShutdownSignal is wired through to cancel its
//     context, it will tear down on its own.
//  2. Poll for the child to exit. A second Ctrl+C in the parent's console
//     (delivered through sigCh as os.Interrupt) escalates to a hard
//     TerminateProcess.
//
// Children that didn't configure a shutdown context — like the stubborn
// example — install the listener with a no-op cancel, so the pipe byte
// reaches them but does nothing; the second interrupt is what kills them.
func (p *platformImpl) killChildOnInterrupt(_ *exec.Cmd, sigCh chan os.Signal, pid int, serveName string) {
	fmt.Printf("\nstartup interrupted; stopping %s (pid %d)...\n", serveName, pid)
	// signalShutdownPipe is racy here: the child's listener is only created
	// after detachSig fires (i.e. after close(ready)), so an interrupt that
	// lands mid-startup will fail the first write because the pipe doesn't
	// exist yet. Retry each tick until either it lands (worker exits
	// gracefully) or the user mashes Ctrl+C a second time to force a kill.
	signaled := p.signalShutdownPipe() == nil

	for {
		if !p.isAlive(pid) {
			return
		}
		if !signaled {
			signaled = p.signalShutdownPipe() == nil
		}
		select {
		case <-sigCh:
			fmt.Printf("forcing kill (pid %d)\n", pid)
			_ = p.terminate(pid)
			return
		case <-time.After(stopPollEach):
		}
	}
}

func (p *platformImpl) afterStartupReady() {
	_ = os.Remove(p.readyFile())
}

func (p *platformImpl) pollStartupState(sigCh chan os.Signal, pid int) (startResult, bool) {
	if _, err := os.Stat(p.readyFile()); err == nil {
		return startReady, true
	}
	if !p.isAlive(pid) {
		return startFailed, true
	}
	select {
	case <-sigCh:
		return startInterrupted, true
	default:
	}
	return 0, false
}

func (p *platformImpl) isAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return false
	}
	return code == stillActive
}

func (p *platformImpl) notifyParentReady(_ int) {
	f, err := os.Create(p.readyFile())
	if err != nil {
		return
	}
	_ = f.Close()
}

// shutdownPipeName returns the named-pipe path the child listens on for the
// parent's graceful-shutdown trigger. Derived from the pid-file basename so
// it matches the rest of the daemon's state-file namespace.
func (p *platformImpl) shutdownPipeName() string {
	base := strings.TrimSuffix(filepath.Base(p.pidFile), ".pid")
	return `\\.\pipe\daemonize-` + base
}

// installShutdownListener opens the shutdown pipe as a server (synchronously,
// so it is connectable the instant the function returns) and hands the
// connect+read+cancel work off to a goroutine. cancel is the func returned
// by signal.NotifyContext earlier in startup; calling it surfaces as
// <-cmd.Context().Done() to the worker.
func (p *platformImpl) installShutdownListener(cancel context.CancelFunc) {
	if cancel == nil {
		return
	}
	name, err := windows.UTF16PtrFromString(p.shutdownPipeName())
	if err != nil {
		return
	}
	h, err := windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_INBOUND,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT,
		1, 256, 256, 0, nil,
	)
	if err != nil {
		return
	}
	go func() {
		defer windows.CloseHandle(h)
		if err := windows.ConnectNamedPipe(h, nil); err != nil && err != windows.ERROR_PIPE_CONNECTED {
			return
		}
		var b [1]byte
		var n uint32
		_ = windows.ReadFile(h, b[:], &n, nil)
		cancel()
	}()
}

// signalShutdownPipe is the parent-side counterpart: open the same pipe as a
// client and write one byte. Returns nil on a successful write; any error
// means the child never installed (or already tore down) the listener.
// Callers fall back to terminate in that case.
func (p *platformImpl) signalShutdownPipe() error {
	name, err := windows.UTF16PtrFromString(p.shutdownPipeName())
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(
		name, windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, 0, 0,
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	var n uint32
	return windows.WriteFile(h, []byte{1}, &n, nil)
}

// stop tries graceful shutdown via the named-pipe signal first, then waits
// indefinitely for the child to exit (with Ctrl+C in the stop console
// escalating to TerminateProcess). If the pipe write fails — child never
// installed the listener or has already crashed — we go straight to the
// hard kill.
func (p *platformImpl) stop(pid int, tickTail func()) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	if err := p.signalShutdownPipe(); err != nil {
		tickTail()
		if err := p.terminate(pid); err != nil {
			return err
		}
		tickTail()
		return nil
	}

	for {
		tickTail()
		if !p.isAlive(pid) {
			tickTail()
			return nil
		}
		select {
		case <-sigCh:
			tickTail()
			_ = p.terminate(pid)
			fmt.Printf("\nkilled (pid %d) on interrupt\n", pid)
			return nil
		case <-time.After(stopPollEach):
		}
	}
}

// terminate opens a process handle with TERMINATE + SYNCHRONIZE rights,
// asks the OS to kill it, then waits up to five seconds for the handle to
// become signaled. Used by both stop and the startup-interrupt path.
func (p *platformImpl) terminate(pid int) error {
	h, err := windows.OpenProcess(
		windows.PROCESS_TERMINATE|windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION,
		false, uint32(pid),
	)
	if err != nil {
		return fmt.Errorf("OpenProcess(%d): %w", pid, err)
	}
	defer windows.CloseHandle(h)

	if err := windows.TerminateProcess(h, 1); err != nil {
		return fmt.Errorf("TerminateProcess(%d): %w", pid, err)
	}
	_, _ = windows.WaitForSingleObject(h, 5000)
	return nil
}
