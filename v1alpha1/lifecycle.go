package v1alpha1

import (
	"fmt"
	"os"
	"strings"

	"github.com/cnuss/daemonize/v1"
)

// computeStatus reads the pid file and resolves the daemon's current state.
// A stale pid file is removed here as a side effect — Status relies on that
// cleanup.
func (d *DaemonImpl[T]) computeStatus() v1.StatusResult {
	pid, err := d.PID()
	if err != nil {
		return v1.StatusResult{State: "not_running"}
	}
	if !d.IsAlive() {
		os.Remove(d.pidFile)
		return v1.StatusResult{
			State:   "stale",
			PID:     pid,
			Name:    d.base,
			PIDFile: d.pidFile,
			LogFile: d.logFile,
		}
	}
	return v1.StatusResult{
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
			r, ok := v.(v1.StatusResult)
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
