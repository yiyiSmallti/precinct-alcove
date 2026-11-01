package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
)

// maxLog is when alcove.log is rotated to alcove.log.1 at start.
const maxLog = 5 << 20

// setupLogs sends Alcove's log to logs/alcove.log (and stderr), and a Go
// crash's report to logs/crash.log, both inside the diagnostics bundle.
func setupLogs(dir string) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	path := filepath.Join(dir, "alcove.log")
	if st, err := os.Stat(path); err == nil && st.Size() > maxLog {
		os.Rename(path, path+".1")
	}
	if f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		log.SetOutput(io.MultiWriter(os.Stderr, f))
	}
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	if f, err := os.OpenFile(filepath.Join(dir, "crash.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		debug.SetCrashOutput(f, debug.CrashOptions{})
		f.Close() // SetCrashOutput keeps its own descriptor
	}
}
