package v1alpha1

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func (d *DaemonImpl[T]) PIDFile() (string, error) {
	if d.pidFile == "" {
		return "", fmt.Errorf("daemonize: pid file not resolved (call DetachOn first)")
	}
	return d.pidFile, nil
}

func (d *DaemonImpl[T]) LogFile() (string, error) {
	if d.logFile == "" {
		return "", fmt.Errorf("daemonize: log file not resolved (call DetachOn first)")
	}
	return d.logFile, nil
}

func (d *DaemonImpl[T]) Name() (string, error) {
	if d.base == "" {
		return "", fmt.Errorf("daemonize: name not resolved (call DetachOn first)")
	}
	return d.base, nil
}

func (d *DaemonImpl[T]) PID() (int, error) {
	b, err := os.ReadFile(d.pidFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(b)))
}

func (d *DaemonImpl[T]) writePID(pid int) error {
	return os.WriteFile(d.pidFile, []byte(strconv.Itoa(pid)), 0o644)
}

// prepareStart builds the exec.Cmd that startCobra uses to re-exec this
// binary along the wrapped command's path, with stdout/stderr pointing at a
// fresh log file and the daemon env var set so the child knows it is the
// daemonized run. SysProcAttr is left for the platform-specific caller (set
// to Setsid on Unix; DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP on Windows).
//
// The third return value is the wrapped command's leaf name (used in
// "started X (pid N)" messages).
func (d *DaemonImpl[T]) prepareStart(daemonCmd *cobra.Command, extra []string) (*exec.Cmd, *os.File, string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, nil, "", err
	}

	logf, err := os.OpenFile(d.logFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, nil, "", err
	}

	subPath := strings.Fields(daemonCmd.CommandPath())
	if len(subPath) > 0 {
		subPath = subPath[1:]
	}

	cmd := exec.Command(exe, append(subPath, extra...)...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Env = append(os.Environ(), daemonEnvFor(d.base)+"=1")
	return cmd, logf, daemonCmd.Name(), nil
}

// closeStartedLog handles the parent's after-Start close: if Start failed the
// child never inherited the fd and the close error is noise (we already have
// a more useful error to return); on success we surface a close failure on
// stderr but don't abort, because the child has its own copy of the fd via
// fork+exec inheritance.
func (d *DaemonImpl[T]) closeStartedLog(logf *os.File, startErr error) {
	if startErr != nil {
		_ = logf.Close()
		return
	}
	if err := logf.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "daemonize: close log file: %v\n", err)
	}
}

// announceStarted prints the standard "started" success block to stdout.
func (d *DaemonImpl[T]) announceStarted(pid int) {
	fmt.Printf("pid file: %s\n", d.pidFile)
	fmt.Printf("log file: %s\n", d.logFile)
	fmt.Printf("started (pid %d), now running in the background\n", pid)
}
