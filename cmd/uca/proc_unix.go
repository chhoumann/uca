//go:build !windows

package main

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup starts the command in its own process group so that a
// timeout or cancellation can signal the entire tree (the child plus any helper
// processes it forks), not just the direct child. Without this, exec only kills
// the direct child; a forked grandchild keeps running (leaking) and, if it
// inherited the stdout pipe, keeps that pipe open so Wait blocks until it exits.
func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// A negative pid targets the whole process group led by the child.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			// Fall back to killing just the process if the group is gone.
			return cmd.Process.Kill()
		}
		return nil
	}
}
