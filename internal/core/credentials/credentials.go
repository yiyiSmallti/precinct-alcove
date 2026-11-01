// Package credentials manages model API credentials (PRD 3.2, F2). The
// provider, base URL and label live in credentials.json; the key lives only
// in the system credential store (Windows Credential Manager, macOS login
// Keychain) and reaches dsh as an environment variable when a runtime starts.
package credentials

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
)

// SecretStore is the system credential store. MygoSecrets is the real one.
type SecretStore interface {
	Set(key string, value []byte) error
	Get(key string) ([]byte, error)
	Delete(key string) error
}

// ErrNotFound is returned by a SecretStore for a missing key.
var ErrNotFound = errors.New("secret not found")

// Provider says how a dsh provider takes its key and base URL.
type Provider struct {
	ID      string // dsh provider id, e.g. deepseek-official
	KeyEnv  string
	BaseEnv string
}

// Providers dsh 0.2.1-rc.5 accepts through environment variables. Only the
// DeepSeek adapter is verified end to end; OpenAI-compatible endpoints
// (Gatehouse) go through the pi-ai route, added with profile assembly.
var Providers = map[string]Provider{
	"deepseek-official": {ID: "deepseek-official", KeyEnv: "DEEPSEEK_API_KEY", BaseEnv: "DEEPSEEK_BASE_URL"},
}

// Credential is one stored credential, without its key.
type Credential struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Provider  string    `json:"provider"`
	BaseURL   string    `json:"baseUrl,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store keeps credential records in Path and keys in Secrets.
type Store struct {
	Path    string
	Secrets SecretStore
	mu      sync.Mutex
}

func secretKey(id string) string { return "credential/" + id }

// Add stores a new credential. The key goes to the secret store first, so
// a record never points at a missing key.
func (s *Store) Add(label, provider, baseURL, key string) (*Credential, error) {
	if _, ok := Providers[provider]; !ok {
		return nil, fmt.Errorf("unknown provider %q", provider)
	}
	if strings.TrimSpace(key) == "" {
		return nil, errors.New("key is empty")
	}
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return nil, fmt.Errorf("base URL %q is not an http(s) URL", baseURL)
		}
	}
	raw := make([]byte, 8)
	rand.Read(raw)
	c := Credential{ID: hex.EncodeToString(raw), Label: label, Provider: provider, BaseURL: baseURL, CreatedAt: time.Now().UTC()}
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return nil, err
	}
	if err := s.Secrets.Set(secretKey(c.ID), []byte(key)); err != nil {
		return nil, fmt.Errorf("store key: %w", err)
	}
	all = append(all, c)
	if err := fsutil.WriteJSON(s.Path, all, 0o600); err != nil {
		s.Secrets.Delete(secretKey(c.ID))
		return nil, err
	}
	return &c, nil
}

// List returns the records sorted by label.
func (s *Store) List() ([]Credential, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	sort.Slice(all, func(i, j int) bool { return all[i].Label < all[j].Label })
	return all, err
}

// Remove deletes the record, then the key.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	all, err := s.load()
	if err != nil {
		return err
	}
	keep := all[:0]
	for _, c := range all {
		if c.ID != id {
			keep = append(keep, c)
		}
	}
	if len(keep) == len(all) {
		return fmt.Errorf("no credential %s", id)
	}
	if err := fsutil.WriteJSON(s.Path, keep, 0o600); err != nil {
		return err
	}
	if err := s.Secrets.Delete(secretKey(id)); err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	return nil
}

// LaunchEnv returns the provider id and the KEY=value pairs that give dsh
// this credential. Call it only when starting a runtime; the result must not
// be logged or written anywhere.
func (s *Store) LaunchEnv(id string) (provider string, env []string, err error) {
	s.mu.Lock()
	all, err := s.load()
	s.mu.Unlock()
	if err != nil {
		return "", nil, err
	}
	for _, c := range all {
		if c.ID != id {
			continue
		}
		p := Providers[c.Provider]
		key, err := s.Secrets.Get(secretKey(id))
		if err != nil {
			return "", nil, fmt.Errorf("read key for %s: %w", c.Label, err)
		}
		env = []string{p.KeyEnv + "=" + string(key)}
		if c.BaseURL != "" {
			env = append(env, p.BaseEnv+"="+c.BaseURL)
		}
		return p.ID, env, nil
	}
	return "", nil, fmt.Errorf("no credential %s", id)
}

func (s *Store) load() ([]Credential, error) {
	var all []Credential
	if err := fsutil.ReadJSON(s.Path, &all); err != nil {
		return nil, fmt.Errorf("credentials file %s: %w", s.Path, err)
	}
	return all, nil
}
