// Package host installs, lists and removes Alcove plugin packages.
//
// Installation always goes through the official dsh path (Charter ADR 0001):
// Alcove checks the signature and package.json, copies the .tgz into its own
// package store, then runs `dsh plugin --profile <p> add <stored .tgz>` for the
// store profile and for every target profile, checking each with
// `--dump-config`. Any failure undoes the profiles already done, in reverse.
// Alcove never unpacks into node_modules or edits a profile's package.json.
//
// Why the copy: a profile records the absolute `file:` path of the .tgz, and
// once that file is gone every later `dsh plugin add` in the profile fails
// (validation report, section 3). The stored copy lives until uninstall.
package host

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/pkg"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

// StoreProfile is the reserved profile that only holds installed packages; it
// is never booted. Sidecars start from its node_modules.
const StoreProfile = "alcove-plugins"

// Host manages plugin packages on disk. Dir is Alcove's plugin directory
// (%LOCALAPPDATA%\Alcove\plugins): packages/ holds the stored .tgz files and
// installed.json the install records.
type Host struct {
	DSH           *dsh.CLI
	Dir           string
	AlcoveVersion string
	GOOS          string
	// TrustedKeys verify package signatures (built-in first-party key plus
	// organisation keys from policy).
	TrustedKeys []ed25519.PublicKey
	// AllowUnsigned skips the signature check; development builds only.
	AllowUnsigned bool
	// ResolveProfiles maps one alcove.profiles entry to concrete dsh profiles.
	// "personal" means every personal workspace profile. Nil installs into
	// the store profile only.
	ResolveProfiles func(name string) []string
	// PrepareProfile creates a target profile that does not exist yet (from
	// the sdk template plus the bridge), so that adding the package does not
	// leave a bare profile dsh cannot run. Nil adds into whatever is there.
	PrepareProfile func(ctx context.Context, profile string) error

	mu sync.Mutex
}

// Installed is one install record.
type Installed struct {
	Name         string    `json:"name"`
	Version      string    `json:"version"`
	SHA256       string    `json:"sha256"`
	Stored       string    `json:"stored"`   // .tgz inside the package store
	Profiles     []string  `json:"profiles"` // target profiles it was added to (store profile excluded)
	Targets      []string  `json:"targets"`  // alcove.profiles as declared, e.g. personal, daily
	Capabilities []string  `json:"capabilities"`
	Sidecar      string    `json:"sidecar,omitempty"` // relative path for this OS
	Title        string    `json:"title,omitempty"`
	Avatar       string    `json:"avatar,omitempty"` // relative path of a PNG
	SignedBy     int       `json:"signedBy"`         // index into TrustedKeys, -1 when unsigned
	InstalledAt  time.Time `json:"installedAt"`
	// Incomplete marks a failed install whose rollback did not finish: the
	// archive and Profiles are what is left behind. Uninstall cleans it up.
	Incomplete bool `json:"incomplete,omitempty"`
}

// Plan is what the install dialog shows before the user confirms.
type Plan struct {
	Path     string
	Package  *pkg.Package
	SHA256   string
	SignedBy int
	Problems []pkg.Problem
	Profiles []string // concrete target profiles
	// Replaces is the installed version this package updates, nil for a
	// new install. Installing it swaps the versions in every profile the
	// installed one is in.
	Replaces *Installed
}

// OK reports whether the plan can be installed.
func (p *Plan) OK() bool { return len(p.Problems) == 0 }

// StepError says which step failed and carries dsh's diagnostics.
type StepError struct {
	Step        string // signature, inspect, check, store, add, verify, dump-config
	Profile     string
	Err         error
	RolledBack  bool
	Diagnostics []string
}

func (e *StepError) Error() string {
	where := e.Step
	if e.Profile != "" {
		where += " (" + e.Profile + ")"
	}
	return fmt.Sprintf("plugin install failed at %s: %v", where, e.Err)
}

func (e *StepError) Unwrap() error { return e.Err }

// Prepare checks a package file without changing anything: signature,
// package.json rules, host-side requirements, and target profiles.
func (h *Host) Prepare(ctx context.Context, tgz string) (*Plan, error) {
	tgz, err := filepath.Abs(tgz)
	if err != nil {
		return nil, err
	}
	// Work on a private snapshot: the signature check, inspection and
	// install all see the same bytes even if the source changes meanwhile.
	snap, err := h.snapshot(tgz)
	if err != nil {
		return nil, &StepError{Step: "inspect", Err: err}
	}
	plan := &Plan{Path: snap, SignedBy: -1}
	ok := false
	defer func() {
		if !ok {
			os.Remove(snap)
		}
	}()
	digest, err := sig.Digest(snap)
	if err != nil {
		return nil, &StepError{Step: "inspect", Err: err}
	}
	plan.SHA256 = hex.EncodeToString(digest)

	i, err := -1, error(sig.ErrUnsigned)
	if raw, rerr := os.ReadFile(tgz + ".sig"); rerr == nil {
		i, err = sig.VerifyDigest(digest, raw, tgz+".sig", h.TrustedKeys)
	} else if !errors.Is(rerr, os.ErrNotExist) {
		err = rerr
	}
	if err == nil {
		plan.SignedBy = i
	} else if !h.AllowUnsigned {
		return nil, &StepError{Step: "signature", Err: err}
	}

	p, err := pkg.Inspect(snap)
	if err != nil {
		return nil, &StepError{Step: "inspect", Err: err}
	}
	plan.Package = p

	dshVersion, err := h.DSH.Version(ctx)
	if err != nil {
		return nil, &StepError{Step: "check", Err: fmt.Errorf("cannot read dsh version: %w", err)}
	}
	plan.Problems = p.Check(pkg.Env{DshVersion: dshVersion, AlcoveVersion: h.AlcoveVersion, GOOS: h.GOOS})

	h.mu.Lock()
	installed, err := h.load()
	h.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if cur, ok := installed[p.Name]; ok && !cur.Incomplete && cur.Version != p.Version {
		plan.Replaces = &cur
		plan.Problems = append(plan.Problems, dependentProblems(p, installed)...)
	}
	plan.Problems = append(plan.Problems, stateProblems(p, installed, plan.Replaces)...)
	if p.Alcove != nil {
		plan.Profiles = h.targets(p.Alcove.Profiles)
	}
	plan.Problems = append(plan.Problems, h.profileConflicts(p.Name, plan.Profiles, plan.Replaces)...)
	ok = true
	return plan, nil
}

// snapshot copies src into Dir/staging and returns the copy's path.
func (h *Host) snapshot(src string) (string, error) {
	dir := filepath.Join(h.Dir, "staging")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "pkg-*.tgz")
	if err != nil {
		return "", err
	}
	f.Close()
	if err := copyFile(src, f.Name()); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// Discard drops a plan that will not be installed. Install discards its plan
// itself, whatever the outcome.
func (h *Host) Discard(plan *Plan) {
	if plan != nil && filepath.Dir(plan.Path) == filepath.Join(h.Dir, "staging") {
		os.Remove(plan.Path)
	}
}

// stateProblems checks p against the installed records: not installed yet
// (or still at the version it replaces), and every required plugin present
// in a satisfying version.
func stateProblems(p *pkg.Package, installed map[string]Installed, replaces *Installed) []pkg.Problem {
	var out []pkg.Problem
	if cur, ok := installed[p.Name]; ok && (replaces == nil || cur.Incomplete || cur.SHA256 != replaces.SHA256) {
		msg := fmt.Sprintf("%s %s is installed; uninstall it first", cur.Name, cur.Version)
		if cur.Incomplete {
			msg = fmt.Sprintf("%s %s is left over from a failed install; uninstall it first", cur.Name, cur.Version)
		}
		out = append(out, pkg.Problem{Code: "already-installed", Message: msg})
	}
	reqs, _ := p.Requirements()
	for _, r := range reqs {
		cur, ok := installed[r.Name]
		if !ok {
			out = append(out, pkg.Problem{Code: "requires", Message: fmt.Sprintf("install %s@%s first", r.Name, r.Range)})
			continue
		}
		if cur.Incomplete {
			out = append(out, pkg.Problem{Code: "requires", Message: fmt.Sprintf("%s is left over from a failed install; uninstall and reinstall it first", r.Name)})
			continue
		}
		if ok, err := pkg.Satisfies(cur.Version, r.Range); err != nil || !ok {
			out = append(out, pkg.Problem{Code: "requires", Message: fmt.Sprintf("needs %s@%s, installed is %s", r.Name, r.Range, cur.Version)})
		}
	}
	return out
}

// dependentProblems reports installed plugins whose alcove.requires the new
// version of p no longer satisfies.
func dependentProblems(p *pkg.Package, installed map[string]Installed) []pkg.Problem {
	var out []pkg.Problem
	for _, rec := range installed {
		if rec.Name == p.Name || rec.Incomplete {
			continue
		}
		dep, err := pkg.Inspect(rec.Stored)
		if err != nil {
			continue
		}
		reqs, _ := dep.Requirements()
		for _, r := range reqs {
			if r.Name != p.Name {
				continue
			}
			if ok, err := pkg.Satisfies(p.Version, r.Range); err != nil || !ok {
				out = append(out, pkg.Problem{Code: "requires", Message: fmt.Sprintf("%s needs %s@%s; update or uninstall it first", rec.Name, r.Name, r.Range)})
			}
		}
	}
	return out
}

// profileConflicts reports profiles (the store profile and the targets) that
// already reference name outside Alcove, e.g. added with dsh plugin add by
// hand. Alcove does not take them over: rollback would remove them. The
// profiles holding the version an update replaces are Alcove's own.
func (h *Host) profileConflicts(name string, targets []string, replaces *Installed) []pkg.Problem {
	var out []pkg.Problem
	ours := map[string]bool{}
	if replaces != nil {
		ours[StoreProfile] = true
		for _, prof := range replaces.Profiles {
			ours[prof] = true
		}
	}
	for _, prof := range append([]string{StoreProfile}, targets...) {
		if ours[prof] {
			continue
		}
		m, err := h.DSH.ReadProfile(prof)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			out = append(out, pkg.Problem{Code: "profile", Message: fmt.Sprintf("cannot read profile %s: %v", prof, err)})
			continue
		}
		if m.HasDependency(name) || m.HasBundle(name) {
			out = append(out, pkg.Problem{Code: "profile", Message: fmt.Sprintf("profile %s already has %s; remove it there first", prof, name)})
		}
	}
	return out
}

// Install carries out a confirmed plan. On failure everything already done
// is undone and the returned *StepError says where it stopped.
func (h *Host) Install(ctx context.Context, plan *Plan) (*Installed, error) {
	defer h.Discard(plan)
	if !plan.OK() {
		return nil, &StepError{Step: "check", Err: fmt.Errorf("%d problem(s): %s", len(plan.Problems), plan.Problems[0])}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	p := plan.Package

	// Re-check the file did not change between Prepare and Install.
	digest, err := sig.Digest(plan.Path)
	if err != nil || hex.EncodeToString(digest) != plan.SHA256 {
		return nil, &StepError{Step: "inspect", Err: errors.New("package file changed after it was checked")}
	}

	// Another install or uninstall may have run since Prepare: re-check the
	// installed state under the lock before touching files or profiles.
	installed, err := h.load()
	if err != nil {
		return nil, &StepError{Step: "check", Err: err}
	}
	// Workspaces may have been created or removed since Prepare: resolve
	// the targets again so new ones get the plugin and removed ones are
	// not recreated.
	if p.Alcove != nil {
		plan.Profiles = h.targets(p.Alcove.Profiles)
	}
	probs := append(stateProblems(p, installed, plan.Replaces), h.profileConflicts(p.Name, plan.Profiles, plan.Replaces)...)
	if r := plan.Replaces; r != nil {
		if cur, ok := installed[p.Name]; !ok || cur.Incomplete || cur.SHA256 != r.SHA256 {
			probs = append(probs, pkg.Problem{Code: "already-installed", Message: fmt.Sprintf("%s %s is no longer the installed version", r.Name, r.Version)})
		}
	}
	if len(probs) > 0 {
		return nil, &StepError{Step: "check", Err: fmt.Errorf("installed plugins changed after the check: %s", probs[0])}
	}
	if plan.Replaces != nil {
		// Profiles may have been added since Prepare (new workspaces).
		cur := installed[p.Name]
		plan.Replaces = &cur
	}

	stored := filepath.Join(h.Dir, "packages", storeName(p.Name, p.Version, plan.SHA256))
	if err := copyFile(plan.Path, stored); err != nil {
		return nil, &StepError{Step: "store", Err: err}
	}
	// The source may change while it is copied: check the copy itself, which
	// is what dsh installs from.
	if digest, err := sig.Digest(stored); err != nil || hex.EncodeToString(digest) != plan.SHA256 {
		os.Remove(stored)
		return nil, &StepError{Step: "inspect", Err: errors.New("package file changed after it was checked")}
	}

	if plan.Replaces != nil {
		return h.replace(ctx, plan, stored)
	}
	done, step, prof, err := h.addEverywhere(ctx, p.Name, stored, plan.Profiles)
	if err != nil {
		se := stepError(step, prof, err)
		se.RolledBack = h.undo(context.WithoutCancel(ctx), p.Name, done) == nil
		if se.RolledBack {
			os.Remove(stored)
			return nil, se
		}
		// Keep a record of what is left so List shows it and Uninstall can
		// retry the cleanup.
		if all, err := h.load(); err == nil {
			all[p.Name] = Installed{
				Name: p.Name, Version: p.Version, SHA256: plan.SHA256, Stored: stored,
				Profiles: withoutStore(done), SignedBy: plan.SignedBy, InstalledAt: time.Now().UTC(), Incomplete: true,
			}
			h.save(all)
		}
		return nil, se
	}
	return h.record(plan, stored, plan.Profiles)
}

// addEverywhere adds the stored package to the store profile and then to
// each target, creating missing targets and checking each with
// --dump-config. done lists the profiles touched, the failed one included:
// a failed add can leave it half-changed, and undo skips clean profiles.
func (h *Host) addEverywhere(ctx context.Context, name, stored string, profiles []string) (done []string, step, profile string, err error) {
	for _, prof := range append([]string{StoreProfile}, profiles...) {
		if prof != StoreProfile && h.PrepareProfile != nil {
			if _, err := os.Stat(h.DSH.ProfileDir(prof)); errors.Is(err, os.ErrNotExist) {
				if err := h.PrepareProfile(ctx, prof); err != nil {
					return done, "profile", prof, err
				}
			}
		}
		done = append(done, prof)
		if _, err := h.DSH.PluginAdd(ctx, prof, stored); err != nil {
			return done, "add", prof, err
		}
		m, err := h.DSH.ReadProfile(prof)
		if err != nil {
			return done, "verify", prof, err
		}
		if !m.HasBundle(name) {
			return done, "verify", prof, fmt.Errorf("%s was installed but not selected as a bundle", name)
		}
		if prof == StoreProfile {
			continue // never booted
		}
		if _, err := h.DSH.DumpConfig(ctx, prof); err != nil {
			return done, "dump-config", prof, err
		}
	}
	return done, "", "", nil
}

// replace swaps the installed version for the plan's. The old version
// leaves every profile it is in; the new one goes into the store profile,
// the targets, and the profiles the old one was added to later (a
// workspace.attach). If anything fails, the new version is undone and the
// old one put back where it is missing.
func (h *Host) replace(ctx context.Context, plan *Plan, stored string) (*Installed, error) {
	p, old := plan.Package, *plan.Replaces
	profiles := append([]string(nil), plan.Profiles...)
	for _, prof := range old.Profiles {
		if _, err := os.Stat(h.DSH.ProfileDir(prof)); err == nil && !contains(profiles, prof) {
			profiles = append(profiles, prof)
		}
	}
	oldAt := append([]string{StoreProfile}, old.Profiles...)
	bg := context.WithoutCancel(ctx)
	rollback := func(se *StepError, done []string) (*Installed, error) {
		undone := h.undo(bg, p.Name, done) == nil
		if undone {
			os.Remove(stored)
		}
		se.RolledBack = undone && h.restore(bg, old, oldAt) == nil
		if !se.RolledBack {
			// What is installed where is unclear: keep a record Uninstall
			// can clean up from.
			if all, err := h.load(); err == nil {
				rec := old
				rec.Profiles, rec.Incomplete = withoutStore(append(oldAt, profiles...)), true
				all[p.Name] = rec
				h.save(all)
			}
		}
		return nil, se
	}
	if err := h.undo(ctx, p.Name, oldAt); err != nil {
		return rollback(&StepError{Step: "remove", Err: err}, nil)
	}
	done, step, prof, err := h.addEverywhere(ctx, p.Name, stored, profiles)
	if err != nil {
		return rollback(stepError(step, prof, err), done)
	}
	rec, err := h.record(plan, stored, profiles)
	if err == nil && old.Stored != stored {
		os.Remove(old.Stored)
	}
	return rec, err
}

// restore adds the old version back to the profiles in at that lack it.
// Workspace profiles removed meanwhile stay gone.
func (h *Host) restore(ctx context.Context, old Installed, at []string) error {
	for _, prof := range at {
		m, err := h.DSH.ReadProfile(prof)
		if err == nil && m.HasBundle(old.Name) {
			continue
		}
		if errors.Is(err, os.ErrNotExist) && prof != StoreProfile {
			continue
		}
		if _, err := h.DSH.PluginAdd(ctx, prof, old.Stored); err != nil {
			return err
		}
	}
	return nil
}

// record saves the install record of a package added to profiles.
func (h *Host) record(plan *Plan, stored string, profiles []string) (*Installed, error) {
	p := plan.Package
	rec := Installed{
		Name: p.Name, Version: p.Version, SHA256: plan.SHA256, Stored: stored,
		Profiles: profiles, SignedBy: plan.SignedBy, InstalledAt: time.Now().UTC(),
	}
	if p.Alcove != nil {
		rec.Capabilities = p.Alcove.Capabilities
		rec.Sidecar = p.Alcove.Sidecar[h.GOOS]
		rec.Targets = p.Alcove.Profiles
		rec.Title, rec.Avatar = p.Alcove.Title, p.Alcove.Avatar
	}
	all, err := h.load()
	if err == nil {
		all[p.Name] = rec
		err = h.save(all)
	}
	if err != nil {
		return nil, &StepError{Step: "store", Err: err}
	}
	return &rec, nil
}

// stepError wraps a failed step, with dsh's diagnostics when dsh failed.
func stepError(step, profile string, err error) *StepError {
	se := &StepError{Step: step, Profile: profile, Err: err}
	var de *dsh.Error
	if errors.As(err, &de) {
		se.Diagnostics = de.Diagnostics()
		if len(se.Diagnostics) == 0 {
			se.Diagnostics = de.StderrTail(5)
		}
	}
	return se
}

func withoutStore(profiles []string) []string {
	var out []string
	for _, prof := range profiles {
		if prof != StoreProfile && !contains(out, prof) {
			out = append(out, prof)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Uninstall removes the package from every target profile, then from the
// store profile, then deletes the stored .tgz. The caller stops the sidecar
// first.
func (h *Host) Uninstall(ctx context.Context, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return err
	}
	rec, ok := all[name]
	if !ok {
		return fmt.Errorf("%s is not installed", name)
	}
	// From here on the package stops being usable as a prerequisite: record
	// that first, so any later failure leaves an incomplete record behind.
	if !rec.Incomplete {
		rec.Incomplete = true
		all[name] = rec
		if err := h.save(all); err != nil {
			return err
		}
	}
	if err := h.undo(ctx, name, append([]string{StoreProfile}, rec.Profiles...)); err != nil {
		return err
	}
	if err := os.Remove(rec.Stored); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	delete(all, name)
	return h.save(all)
}

// AddToProfile adds an installed package to one more profile, e.g. a
// workspace created after the package was installed. The profile is
// recorded first so Uninstall also removes it there.
func (h *Host) AddToProfile(ctx context.Context, name, profile string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return err
	}
	rec, ok := all[name]
	if !ok || rec.Incomplete {
		return fmt.Errorf("%s is not installed", name)
	}
	for _, p := range rec.Profiles {
		if p == profile {
			return nil
		}
	}
	if err := h.canAdd(name, profile); err != nil {
		return err
	}
	rec.Profiles = append(rec.Profiles, profile)
	all[name] = rec
	if err := h.save(all); err != nil {
		return err
	}
	if _, err := h.DSH.PluginAdd(ctx, profile, rec.Stored); err != nil {
		h.undo(context.WithoutCancel(ctx), name, []string{profile})
		rec.Profiles = rec.Profiles[:len(rec.Profiles)-1]
		all[name] = rec
		h.save(all)
		return &StepError{Step: "add", Profile: profile, Err: err}
	}
	return nil
}

// CanAdd reports whether AddToProfile may add name to profile: not when the
// profile already has a package by that name that Alcove did not put there.
func (h *Host) CanAdd(name, profile string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.canAdd(name, profile)
}

func (h *Host) canAdd(name, profile string) error {
	all, err := h.load()
	if err != nil {
		return err
	}
	rec, ok := all[name]
	if !ok || rec.Incomplete {
		return fmt.Errorf("%s is not installed", name)
	}
	for _, p := range rec.Profiles {
		if p == profile {
			return nil
		}
	}
	m, err := h.DSH.ReadProfile(profile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cannot read profile %s: %w", profile, err)
	}
	if m.HasDependency(name) || m.HasBundle(name) {
		return fmt.Errorf("profile %s already has %s; remove it there first", profile, name)
	}
	return nil
}

// RemoveFromProfile takes an installed package out of one profile it was
// added to with AddToProfile. A profile it is not recorded in is left alone.
func (h *Host) RemoveFromProfile(ctx context.Context, name, profile string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return err
	}
	rec, ok := all[name]
	if !ok {
		return nil
	}
	keep := rec.Profiles[:0:0]
	for _, p := range rec.Profiles {
		if p != profile {
			keep = append(keep, p)
		}
	}
	if len(keep) == len(rec.Profiles) {
		return nil
	}
	if err := h.undo(ctx, name, []string{profile}); err != nil {
		return err
	}
	rec.Profiles = keep
	all[name] = rec
	return h.save(all)
}

// ForgetProfile drops profile from every record after the caller deleted
// the profile itself (a removed workspace).
func (h *Host) ForgetProfile(profile string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return err
	}
	for n, rec := range all {
		keep := rec.Profiles[:0:0]
		for _, p := range rec.Profiles {
			if p != profile {
				keep = append(keep, p)
			}
		}
		rec.Profiles = keep
		all[n] = rec
	}
	return h.save(all)
}

// List returns install records sorted by name.
func (h *Host) List() ([]Installed, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	all, err := h.load()
	if err != nil {
		return nil, err
	}
	out := make([]Installed, 0, len(all))
	for _, r := range all {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// SidecarPath is the absolute sidecar executable of an installed package, or
// "" when it has none. It lives in the store profile's node_modules.
func (h *Host) SidecarPath(rec Installed) string {
	if rec.Sidecar == "" {
		return ""
	}
	return filepath.Join(h.DSH.PackageDir(StoreProfile, rec.Name), filepath.FromSlash(rec.Sidecar))
}

// AvatarPath is where an installed plugin's avatar is, or "" without one.
func (h *Host) AvatarPath(rec Installed) string {
	if rec.Avatar == "" {
		return ""
	}
	return filepath.Join(h.DSH.PackageDir(StoreProfile, rec.Name), filepath.FromSlash(rec.Avatar))
}

// undo removes name from profiles in reverse order, skipping profiles where
// it is already gone. It keeps going after a failure and reports the first.
func (h *Host) undo(ctx context.Context, name string, profiles []string) error {
	var first error
	for i := len(profiles) - 1; i >= 0; i-- {
		prof := profiles[i]
		m, err := h.DSH.ReadProfile(prof)
		if errors.Is(err, os.ErrNotExist) {
			continue // profile gone: nothing to remove
		}
		if err != nil {
			// Unreadable is not absent: report it so the caller keeps the
			// stored archive and the record for a later retry.
			if first == nil {
				first = fmt.Errorf("read %s: %w", prof, err)
			}
			continue
		}
		if !(m.HasDependency(name) || m.HasBundle(name)) {
			continue
		}
		if _, err := h.DSH.PluginRemove(ctx, prof, name); err != nil && first == nil {
			first = fmt.Errorf("remove %s from %s: %w", name, prof, err)
		}
		// pnpm leaves an empty scope directory (@scope/) behind.
		if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
			os.Remove(filepath.Join(h.DSH.ProfileDir(prof), "node_modules", scope))
		}
	}
	return first
}

func (h *Host) targets(names []string) []string {
	seen := map[string]bool{StoreProfile: true}
	var out []string
	for _, n := range names {
		var ps []string
		if h.ResolveProfiles != nil {
			ps = h.ResolveProfiles(n)
		}
		for _, p := range ps {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

func (h *Host) recordsPath() string { return filepath.Join(h.Dir, "installed.json") }

func (h *Host) load() (map[string]Installed, error) {
	data, err := os.ReadFile(h.recordsPath())
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Installed{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := map[string]Installed{}
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", h.recordsPath(), err)
	}
	return all, nil
}

func (h *Host) save(all map[string]Installed) error {
	data, err := json.MarshalIndent(all, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(h.Dir, 0o755); err != nil {
		return err
	}
	tmp := h.recordsPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, h.recordsPath())
}

// storeName turns "@scope/name" + version into
// "scope__name-version-<sha256 prefix>.tgz". The readable part can collide
// ("@foo/bar" and "foo__bar"); the digest prefix keeps distinct archives apart.
func storeName(name, version, sha256 string) string {
	n := strings.ReplaceAll(strings.TrimPrefix(name, "@"), "/", "__")
	if len(sha256) > 16 {
		sha256 = sha256[:16]
	}
	return n + "-" + version + "-" + sha256 + ".tgz"
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}
