package sidecar

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Settings stores plugin settings, one JSON file per namespace under Dir.
// Namespace access rules live in the API (mayUse); this is only storage.
type Settings struct {
	Dir string
	mu  sync.Mutex
}

// MaxSettingsSize bounds one namespace's value.
const MaxSettingsSize = 1 << 20

func (s *Settings) path(ns string) string {
	// Namespaces contain "@" and "/"; hex keeps file names flat and safe.
	return filepath.Join(s.Dir, hex.EncodeToString([]byte(ns))+".json")
}

// Read returns the stored value, or JSON null when nothing is stored.
func (s *Settings) Read(ns string) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path(ns))
	if errors.Is(err, os.ErrNotExist) {
		return json.RawMessage("null"), nil
	}
	if err != nil {
		return nil, err
	}
	var rec struct {
		NS    string          `json:"ns"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(b, &rec); err != nil {
		return nil, fmt.Errorf("settings %s: %w", ns, err)
	}
	return rec.Value, nil
}

// Write replaces the value atomically.
func (s *Settings) Write(ns string, v json.RawMessage) error {
	if !json.Valid(v) {
		return errors.New("settings value is not JSON")
	}
	if len(v) > MaxSettingsSize {
		return fmt.Errorf("settings value over %d bytes", MaxSettingsSize)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"ns": ns, "value": v})
	tmp, err := os.CreateTemp(s.Dir, ".settings-*")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.path(ns))
	}
	if err != nil {
		os.Remove(tmp.Name())
	}
	return err
}
