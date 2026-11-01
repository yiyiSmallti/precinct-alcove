// Package profile assembles the dsh profiles Alcove runs (SPEC 3.3): the
// shipped sdk template, then the alcove-bridge bundle, then the installed
// plugins that target the profile. Everything goes through `dsh plugin`;
// Alcove never edits a profile's package.json or node_modules.
package profile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"

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
