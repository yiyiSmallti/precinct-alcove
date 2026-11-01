//go:build !windows

package dsh

import (
	"os/exec"
	"syscall"
)

// killTree runs cmd in its own process group and kills the group on cancel.
func killTree(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
