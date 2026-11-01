package dsh

import (
	"os/exec"
	"strconv"
)

// OwnProcessGroup is a no-op on Windows: taskkill /T walks the tree.
func OwnProcessGroup(cmd *exec.Cmd) {}

// KillTree kills a started cmd and its descendants (taskkill /T).
func KillTree(cmd *exec.Cmd) error {
	if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
