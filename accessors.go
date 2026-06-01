package daemonize

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func (d *DaemonImpl[T]) IsAlive() bool {
	pid, err := d.PID()
	return err == nil && syscall.Kill(pid, 0) == nil
}

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
