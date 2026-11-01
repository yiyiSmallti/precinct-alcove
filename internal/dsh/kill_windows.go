package dsh

import (
	"os/exec"
	"strconv"
)

// killTree kills cmd and its descendants on cancel (taskkill /T).
func killTree(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if err := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid)).Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
