package app

import (
	"sync"
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
)

type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Set(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}

func (s *memSecrets) Get(k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return nil, credentials.ErrNotFound
}

func (s *memSecrets) Delete(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}

func TestPluginSecrets(t *testing.T) {
	sec := &memSecrets{m: map[string][]byte{}}
	a, err := New(Config{DataDir: t.TempDir(), Secrets: sec})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.keychainRef("@p/a", "token"); err == nil {
		t.Error("ref for a secret never stored")
	}
	ref, err := a.SetPluginSecret("@p/a", "token", []byte("v1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.SetPluginSecret("@p/b", "token", []byte("other")); err != nil {
		t.Fatal(err)
	}
	if got, err := a.keychainRef("@p/a", "token"); err != nil || got != ref {
		t.Errorf("keychain.ref = %q, %v; want %q", got, err, ref)
	}
	// Plugins see their own names only.
	if v, _ := sec.Get(ref); string(v) != "v1" {
		t.Errorf("stored %q", v)
	}
	if err := a.forgetPluginSecrets("@p/a"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.keychainRef("@p/a", "token"); err == nil {
		t.Error("secret kept after uninstall")
	}
	if _, err := a.keychainRef("@p/b", "token"); err != nil {
		t.Errorf("another plugin's secret removed: %v", err)
	}
	if err := a.forgetPluginSecrets("@p/never"); err != nil {
		t.Error(err)
	}
}
