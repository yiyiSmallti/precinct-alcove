//go:build !windows

package dsh

import (
	"context"
	"testing"
	"time"
)

// A descendant holding the output pipes must not stretch the timeout.
func TestRunTimeoutKillsTree(t *testing.T) {
	c := &CLI{Node: "/bin/sh", Bin: "-c", Timeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := c.Run(context.Background(), "sleep 5 & sleep 5")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Run took %v with a 100ms timeout", d)
	}
}
