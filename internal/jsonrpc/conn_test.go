package jsonrpc

import (
	"context"
	"errors"
	"io"
	"runtime"
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

// Closed connections must not leave their writer goroutine behind.
func TestWriterExitsOnClose(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		r, w := io.Pipe()
		c := New(r, io.Discard, nil)
		done := make(chan struct{})
		go func() { c.Serve(context.Background()); close(done) }()
		w.Close()
		<-done
	}
	time.Sleep(100 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > before+5 {
		t.Fatalf("goroutines: %d before, %d after 50 closed connections", before, after)
	}
}
