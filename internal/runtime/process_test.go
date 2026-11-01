//go:build !windows

package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
)

// A runtime that never answers and leaves a child holding stdout must still
// fail within the ready budget, child included.
func TestReadyTimeoutKillsTree(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-dsh.sh")
	pidfile := filepath.Join(dir, "child.pid")
	os.WriteFile(script, []byte("sleep 30 &\necho $! > "+pidfile+"\nsleep 30\n"), 0o755)
	cli := &dsh.CLI{Node: "/bin/sh", Bin: script, Home: dir}
	start := time.Now()
	_, err := Launch(context.Background(), cli, LaunchSpec{Profile: "p", Cwd: dir, ReadyTimeout: 300 * time.Millisecond, IdleTimeout: -1})
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 4*time.Second {
		t.Fatalf("Launch took %v with a 300ms budget", d)
	}
	pid, _ := os.ReadFile(pidfile)
	if len(pid) > 0 && !gone(strings.TrimSpace(string(pid))) {
		t.Fatalf("child %s still running", pid)
	}
}

// gone reports whether pid has exited (absent, or a zombie awaiting reaping).
func gone(pid string) bool {
	for i := 0; i < 20; i++ {
		stat, err := os.ReadFile("/proc/" + pid + "/stat")
		if err != nil {
			return true
		}
		// state is the field after the parenthesised command name
		if f := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(f) > 0 && f[0] == "Z" {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}
