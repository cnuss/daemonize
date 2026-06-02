//go:build windows

package e2e

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

// configureInterruptable spawns the target with CREATE_NEW_PROCESS_GROUP so
// it becomes its own console process group leader. That lets the test
// process address it by pid with GenerateConsoleCtrlEvent without taking
// down everything else attached to the same console.
func configureInterruptable(c *exec.Cmd) {
	if c.SysProcAttr == nil {
		c.SysProcAttr = &syscall.SysProcAttr{}
	}
	c.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP
}

// sendInterrupt delivers a Ctrl+Break to the target's process group. Go's
// signal package surfaces CTRL_BREAK_EVENT as os.Interrupt on the
// receiving side, matching what Process.Signal(syscall.SIGINT) does on
// Unix.
func sendInterrupt(p *os.Process) error {
	return windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(p.Pid))
}

// killPID kills a process by pid, out-of-band from anything daemonize does.
// Used by the stale-pid e2e test to simulate a worker crashing without
// going through Stop. os.Process.Kill on Windows lowers to TerminateProcess.
func killPID(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Kill()
}

// waitForExit waits up to two seconds for the pid to enter the signaled
// state, then returns. The handle is closed before return regardless.
func waitForExit(pid int) {
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return
	}
	defer windows.CloseHandle(h)
	_, _ = windows.WaitForSingleObject(h, 2000)
}
