package dsh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// ProfileManifest is the part of a profile's package.json that dsh owns:
// out-of-tree dependencies and the ordered bundle selection.
type ProfileManifest struct {
	Dependencies map[string]string `json:"dependencies"`
	Dsh          struct {
		Profile struct {
			Bundles []string `json:"bundles"`
		} `json:"profile"`
	} `json:"dsh"`
}

// ReadProfile reads a profile's manifest. A profile that does not exist yet
// returns os.ErrNotExist.
func (c *CLI) ReadProfile(profile string) (*ProfileManifest, error) {
	data, err := os.ReadFile(filepath.Join(c.ProfileDir(profile), "package.json"))
	if err != nil {
		return nil, err
	}
	var m ProfileManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("profile %s: package.json: %w", profile, err)
	}
	return &m, nil
}

// HasDependency reports whether the profile declares the package.
func (m *ProfileManifest) HasDependency(name string) bool {
	_, ok := m.Dependencies[name]
	return ok
}

// HasBundle reports whether the package is selected as a bundle layer.
func (m *ProfileManifest) HasBundle(name string) bool {
	for _, b := range m.Dsh.Profile.Bundles {
		if b == name {
			return true
		}
	}
	return false
}

// PackageDir is where pnpm put an installed package; profiles use the hoisted
// node linker, so it is a real directory a sidecar can be started from.
func (c *CLI) PackageDir(profile, name string) string {
	return filepath.Join(c.ProfileDir(profile), "node_modules", filepath.FromSlash(name))
}
