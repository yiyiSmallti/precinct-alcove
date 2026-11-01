// Package identity creates the local identity on first start (PRD 3.2).
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
)

// Identity is this installation's local identity. It never leaves the
// machine; organisation plugins may bind it to an account later.
type Identity struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"createdAt"`
	Device    string    `json:"device"`
}

// LoadOrCreate reads the identity at path, creating it on first start.
func LoadOrCreate(path string) (*Identity, error) {
	b, err := os.ReadFile(path)
	if err == nil {
		var id Identity
		if err := json.Unmarshal(b, &id); err != nil || id.ID == "" {
			return nil, fmt.Errorf("identity file %s is damaged: %v", path, err)
		}
		return &id, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	raw := make([]byte, 16)
	rand.Read(raw)
	host, _ := os.Hostname()
	id := &Identity{ID: hex.EncodeToString(raw), CreatedAt: time.Now().UTC(), Device: host}
	if err := fsutil.WriteJSON(path, id, 0o600); err != nil {
		return nil, err
	}
	return id, nil
}
