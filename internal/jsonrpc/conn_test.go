package jsonrpc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// A peer that stops reading must not hold a call past its context.
func TestCallRespectsDeadlineWhenWriteBlocks(t *testing.T) {
	r, _ := io.Pipe()
	_, w := io.Pipe() // nobody reads: every write blocks
	c := New(r, w, nil)
	go c.Serve(context.Background())
	for i := 0; i < 2; i++ { // the second call queues behind the stuck write
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		err := c.Call(ctx, "big", map[string]string{"x": "y"}, nil)
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("call %d: err = %v", i, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Fatalf("call %d took %v", i, d)
		}
	}
}
