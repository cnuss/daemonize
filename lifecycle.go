package daemonize

import (
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func (d *DaemonImpl[T]) Stop() error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	pid, err := d.PID()
	if err != nil {
		fmt.Println("not running")
		return nil
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		fmt.Printf("not running (cleared stale pid %d)\n", pid)
		return nil
	}
	fmt.Printf("shutting down (pid %d)...\n", pid)

	// Reattach to the log and stream only output produced from here on, so the
	// user sees the child's shutdown steps while we wait.
	tail := d.openLog(true)
	defer tail.Close()

	// Ctrl+C during the wait force-kills immediately.
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
		tail.copy()
		if !d.IsAlive() {
			tail.copy() // drain any final shutdown output
			os.Remove(d.pidFile)
			fmt.Printf("stopped (pid %d)\n", pid)
			return nil
		}
		select {
		case <-sigCh:
			tail.copy()
			_ = syscall.Kill(pid, syscall.SIGKILL)
			os.Remove(d.pidFile)
			fmt.Printf("\nkilled (pid %d) on interrupt\n", pid)
			return nil
		case <-time.After(stopPollEach):
		}
	}
}

// computeStatus reads the pid file and resolves the daemon's current state.
// A stale pid file is removed here as a side effect — Status relies on that
// cleanup.
func (d *DaemonImpl[T]) computeStatus() StatusResult {
	pid, err := d.PID()
	if err != nil {
		return StatusResult{State: "not_running"}
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		return StatusResult{
			State:   "stale",
			PID:     pid,
			Name:    d.base,
			PIDFile: d.pidFile,
			LogFile: d.logFile,
		}
	}
	return StatusResult{
		State:   "running",
		PID:     pid,
		Name:    d.base,
		PIDFile: d.pidFile,
		LogFile: d.logFile,
	}
}

func (d *DaemonImpl[T]) Status(marshal func(any) ([]byte, error)) error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	if marshal == nil {
		// Default to the human-readable text rendering. Stays on the same
		// marshal-then-print path as the user-supplied case below.
		marshal = func(v any) ([]byte, error) {
			r, ok := v.(StatusResult)
			if !ok {
				return nil, fmt.Errorf("daemonize: status text marshaler: want StatusResult, got %T", v)
			}
			var lines []string
			switch r.State {
			case "not_running":
				lines = []string{"not running"}
			case "stale":
				lines = []string{fmt.Sprintf("not running (cleared stale pid %d)", r.PID)}
			case "running":
				lines = []string{
					fmt.Sprintf("running (pid %d)", r.PID),
					"pid file: " + r.PIDFile,
					"log file: " + r.LogFile,
				}
			default:
				return nil, fmt.Errorf("daemonize: status text marshaler: unknown state %q", r.State)
			}
			return []byte(strings.Join(lines, "\n")), nil
		}
	}
	b, err := marshal(d.computeStatus())
	if err != nil {
		return err
	}
	fmt.Println(string(b))
	return nil
}

func (d *DaemonImpl[T]) Reload() error {
	if d.ctxCancel != nil {
		defer d.ctxCancel()
	}
	sig := syscall.Signal(syscall.SIGHUP)
	if d.reloadSig != nil {
		sig = *d.reloadSig
	}
	pid, err := d.PID()
	if err != nil {
		return fmt.Errorf("not running (no pid file)")
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		return fmt.Errorf("not running (stale pid %d)", pid)
	}
	if err := syscall.Kill(pid, sig); err != nil {
		return err
	}
	fmt.Printf("reload signal sent (pid %d)\n", pid)
	return nil
}
