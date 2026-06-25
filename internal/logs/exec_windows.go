//go:build windows

package logs

import (
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in a new process group. Windows has no
// setpgid; CREATE_NEW_PROCESS_GROUP is the closest equivalent and lets the
// child be terminated independently of this process's console group.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// killProcessGroup terminates the child. Windows has no negative-pid group
// kill; Process.Kill() ends the process, which is sufficient for the CLI tools
// (kubectl/flyctl/etc.) we tail here.
func killProcessGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
