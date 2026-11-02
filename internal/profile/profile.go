// Package profile assembles the dsh profiles Alcove runs (SPEC 3.3): the
// shipped sdk template, then the alcove-bridge bundle, then the installed
// plugins that target the profile. Everything goes through `dsh plugin`;
// Alcove never edits a profile's package.json or node_modules.
package profile

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
)

// Personal is the alcove.profiles entry meaning every personal workspace.
const Personal = "personal"

// BridgePackage is the bridge bundle's package name.
const BridgePackage = "@precinct/alcove-bridge"

// Assembler builds profiles.
type Assembler struct {
	DSH *dsh.CLI
	// Bridge is the alcove-bridge .tgz shipped with Alcove.
	Bridge string
	// Plugins, when set, supplies the installed plugins to add.
	Plugins *host.Host

	verOnce sync.Once
	ver     string
	verErr  error
}

// RefreshBridge installs the shipped bridge into an existing profile whose
// bridge is older (profiles keep the bridge they were made with). It
// reports whether it changed anything; the profile's runtime must not be
// running.
func (a *Assembler) RefreshBridge(ctx context.Context, profile string) (bool, error) {
	if a.Bridge == "" {
		return false, nil
	}
	want, err := a.bridgeVersion()
	if err != nil {
		return false, err
	}
	var have struct {
		Version string `json:"version"`
	}
	if raw, err := os.ReadFile(filepath.Join(a.DSH.PackageDir(profile, BridgePackage), "package.json")); err == nil {
		json.Unmarshal(raw, &have)
	}
	if have.Version == want {
		return false, nil
	}
	if _, err := a.DSH.PluginAdd(ctx, profile, a.Bridge); err != nil {
		return false, fmt.Errorf("add the bridge to %s: %w", profile, err)
	}
	return true, nil
}

// bridgeVersion reads package/package.json from the bridge .tgz once.
func (a *Assembler) bridgeVersion() (string, error) {
	a.verOnce.Do(func() {
		f, err := os.Open(a.Bridge)
		if err != nil {
			a.verErr = err
			return
		}
		defer f.Close()
		zr, err := gzip.NewReader(f)
		if err != nil {
			a.verErr = err
			return
		}
		tr := tar.NewReader(zr)
		for {
			h, err := tr.Next()
			if err != nil {
				a.verErr = fmt.Errorf("%s: no package.json: %w", a.Bridge, err)
				return
			}
			if h.Name == "package/package.json" {
				var pj struct {
					Version string `json:"version"`
				}
				raw, _ := io.ReadAll(io.LimitReader(tr, 1<<20))
				if err := json.Unmarshal(raw, &pj); err != nil || pj.Version == "" {
					a.verErr = fmt.Errorf("%s: package.json has no version", a.Bridge)
				}
				a.ver = pj.Version
				return
			}
		}
	})
	return a.ver, a.verErr
}

// Base creates profile from the sdk template plus the bridge. It refuses an
// existing profile: the template copy would replace it.
func (a *Assembler) Base(ctx context.Context, profile string) error {
	if _, err := os.Stat(a.DSH.ProfileDir(profile)); err == nil {
		return fmt.Errorf("profile %s already exists", profile)
	}
	if _, err := a.DSH.Run(ctx, "--profile", profile, "--from-default-profile", "sdk", "--dump-config"); err != nil {
		return fmt.Errorf("create %s from the sdk template: %w", profile, err)
	}
	if a.Bridge != "" {
		if _, err := a.DSH.PluginAdd(ctx, profile, a.Bridge); err != nil {
			a.Delete(profile)
			return fmt.Errorf("add the bridge to %s: %w", profile, err)
		}
	}
	return nil
}

// Workspace creates a workspace profile: Base plus every installed plugin
// whose alcove.profiles includes "personal", then checks it with
// --dump-config. On failure the half-built profile is deleted.
func (a *Assembler) Workspace(ctx context.Context, profile string) (err error) {
	if err := a.Base(ctx, profile); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			if a.Plugins != nil {
				a.Plugins.ForgetProfile(profile)
			}
			a.Delete(profile)
		}
	}()
	if a.Plugins != nil {
		recs, err := a.Plugins.List()
		if err != nil {
			return err
		}
		// Installation order, as existing profiles received them, so bundle
		// precedence is the same in new and old workspaces.
		sort.SliceStable(recs, func(i, j int) bool { return recs[i].InstalledAt.Before(recs[j].InstalledAt) })
		for _, r := range recs {
			if r.Incomplete || !has(r.Targets, Personal) {
				continue
			}
			if err := a.Plugins.AddToProfile(ctx, r.Name, profile); err != nil {
				return fmt.Errorf("add plugin %s: %w", r.Name, err)
			}
		}
	}
	if _, err := a.DSH.DumpConfig(ctx, profile); err != nil {
		return fmt.Errorf("check %s: %w", profile, err)
	}
	return nil
}

// Delete removes a profile directory. Call it only after its runtime stopped.
func (a *Assembler) Delete(profile string) error {
	dir := a.DSH.ProfileDir(profile)
	if dir == "" || profile == "" || profile == host.StoreProfile {
		return errors.New("refusing to delete this profile")
	}
	return os.RemoveAll(dir)
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
