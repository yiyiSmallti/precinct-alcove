//go:build !windows

package dsh

import (
	"os/exec"
	"syscall"
)

// OwnProcessGroup makes cmd the leader of a new process group so KillTree
// can end it together with its descendants.
func OwnProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// Contain is a no-op here: the process group from OwnProcessGroup already
// holds the tree. On Windows it puts the started cmd in a job object.
func Contain(cmd *exec.Cmd) {}

// KillTree kills a started cmd and its descendants.
func KillTree(cmd *exec.Cmd) error {
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
