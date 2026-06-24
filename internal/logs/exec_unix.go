//go:build !windows

package logs

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group so cancellation can
// signal the whole tree (kubectl/flyctl spawn helpers) rather than orphaning it.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup sends SIGKILL to the child's entire process group (negative
// pid), so descendants die with it.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
