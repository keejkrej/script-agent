//go:build darwin || linux

package process

import (
	"os/exec"
	"syscall"
)

const platformSupported = true

// configureCommand places the child in its own process group so that
// cancellation can signal the whole tree.
func configureCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup sends SIGKILL to the child's process group. It is safe to call
// before Start (no process) and after the child has been reaped (ESRCH).
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return nil
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}

// cleanupGroup makes a best-effort attempt to terminate any remaining members
// of the child's process group.
func cleanupGroup(cmd *exec.Cmd) {
	_ = killGroup(cmd)
}
