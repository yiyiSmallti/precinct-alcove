// Package pkg reads an Alcove plugin package before anything is installed.
//
// An Alcove plugin package is a standard dsh plugin npm package (.tgz) with an
// `alcove` field in package.json (Charter ADR 0001, Alcove SPEC 3.2). Inspect
// reads package/package.json straight out of the tarball; Check applies the
// host-side rules. dsh itself re-checks engines.dsh on install and is the
// authority for it; the pre-check here only makes the refusal early and cheap.
package pkg

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Protocol is the host-extension protocol major version this Alcove speaks.
const Protocol = 1

// Package is the subset of package.json Alcove reads.
type Package struct {
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description"`
	Engines     struct {
		Dsh string `json:"dsh"`
	} `json:"engines"`
	Dsh struct {
		Bundle *struct {
			Patch string `json:"patch"`
		} `json:"bundle"`
	} `json:"dsh"`
	Scripts map[string]string `json:"scripts"`
	Alcove  *Alcove           `json:"alcove"`
}

// Alcove is the host-side declaration (SPEC 3.2).
type Alcove struct {
	Protocol     int               `json:"protocol"`
	Host         string            `json:"host"`
	Requires     []string          `json:"requires"`
	Capabilities []string          `json:"capabilities"`
	Profiles     []string          `json:"profiles"`
	Sidecar      map[string]string `json:"sidecar"`
	UI           []json.RawMessage `json:"ui"`
}

// Capabilities a package may declare (internal/plugins/caps).
var KnownCapabilities = map[string]bool{
	"settings_page":       true,
	"sidebar_item":        true,
	"workspace_hook":      true,
	"runtime_launch_hook": true,
	"provider_endpoint":   true,
	"workspace_session":   true,
	"background":          true,
	"inbox":               true,
	"deep_link":           true,
	"policy_write":        true,
}

// installScripts are lifecycle scripts pnpm would run; first-party packages
// carry none (ADR 0001 point 6) and Alcove never approves builds.
var installScripts = []string{"preinstall", "install", "postinstall", "prepare"}

// Inspect reads package/package.json from a .tgz without extracting anything else.
func Inspect(tgz string) (*Package, error) {
	f, err := os.Open(tgz)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("%s: not a gzip tarball: %w", tgz, err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%s: no package/package.json", tgz)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", tgz, err)
		}
		if h.Name != "package/package.json" {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 1<<20))
		if err != nil {
			return nil, err
		}
		var p Package
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("%s: package.json: %w", tgz, err)
		}
		if p.Name == "" || p.Version == "" {
			return nil, fmt.Errorf("%s: package.json has no name or version", tgz)
		}
		return &p, nil
	}
}

// Problem is one reason a package cannot be installed.
type Problem struct {
	Code    string // stable id for the UI: not-alcove, protocol, host-range, engines, not-bundle, scripts, capability, sidecar, profiles
	Message string
}

func (p Problem) String() string { return p.Code + ": " + p.Message }

// Env is what the package is checked against.
type Env struct {
	DshVersion    string // `dsh --version`
	AlcoveVersion string
	GOOS          string // sidecar key: windows, darwin, linux
}

// Check returns every problem that blocks installation; none means installable.
func (p *Package) Check(env Env) []Problem {
	var out []Problem
	add := func(code, format string, a ...any) { out = append(out, Problem{code, fmt.Sprintf(format, a...)}) }

	if p.Dsh.Bundle == nil || p.Dsh.Bundle.Patch == "" {
		// dsh's CLI would install it as a plain dependency without rolling back.
		add("not-bundle", "package.json has no dsh.bundle.patch; declare one (an empty patch `[]` is fine)")
	}
	if p.Engines.Dsh == "" {
		add("engines", "package.json has no engines.dsh range")
	} else if ok, err := satisfies(env.DshVersion, p.Engines.Dsh); err != nil {
		add("engines", "engines.dsh %q: %v", p.Engines.Dsh, err)
	} else if !ok {
		add("engines", "requires dsh %s, this Alcove ships dsh %s", p.Engines.Dsh, env.DshVersion)
	}
	for _, s := range installScripts {
		if _, ok := p.Scripts[s]; ok {
			add("scripts", "package declares a %q script; Alcove does not run install scripts", s)
		}
	}
	a := p.Alcove
	if a == nil {
		add("not-alcove", "package.json has no alcove field; install it as a dsh bundle instead")
		return out
	}
	if a.Protocol != Protocol {
		add("protocol", "alcove.protocol %d, this Alcove speaks %d", a.Protocol, Protocol)
	}
	if a.Host == "" {
		add("host-range", "alcove.host is empty")
	} else if ok, err := satisfies(env.AlcoveVersion, a.Host); err != nil {
		add("host-range", "alcove.host %q: %v", a.Host, err)
	} else if !ok {
		add("host-range", "requires Alcove %s, this is %s", a.Host, env.AlcoveVersion)
	}
	for _, c := range a.Capabilities {
		if !KnownCapabilities[c] {
			add("capability", "unknown capability %q", c)
		}
	}
	if len(a.Sidecar) > 0 {
		rel, ok := a.Sidecar[env.GOOS]
		if !ok {
			add("sidecar", "no sidecar for %s", env.GOOS)
		} else if !safeRel(rel) {
			add("sidecar", "sidecar path %q must be relative and stay inside the package", rel)
		}
	}
	for _, prof := range a.Profiles {
		if prof == "" || strings.ContainsAny(prof, `/\ `) {
			add("profiles", "invalid profile name %q", prof)
		}
	}
	return out
}

// Requirement is one parsed entry of alcove.requires ("<name>@<range>").
type Requirement struct{ Name, Range string }

// Requirements parses alcove.requires; scoped names keep their leading @.
func (p *Package) Requirements() ([]Requirement, error) {
	if p.Alcove == nil {
		return nil, nil
	}
	var out []Requirement
	for _, r := range p.Alcove.Requires {
		i := strings.LastIndex(r, "@")
		if i <= 0 {
			out = append(out, Requirement{Name: r, Range: "*"})
			continue
		}
		out = append(out, Requirement{Name: r[:i], Range: r[i+1:]})
	}
	return out, nil
}

// Satisfies reports whether version is in the npm-style range.
func Satisfies(version, rng string) (bool, error) { return satisfies(version, rng) }

func satisfies(version, rng string) (bool, error) {
	v, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Errorf("version %q: %w", version, err)
	}
	c, err := semver.NewConstraint(rng)
	if err != nil {
		return false, err
	}
	if v.Prerelease() == "" {
		return c.Check(v), nil
	}
	// npm admits a prerelease only when a comparator in the same "||" set
	// names a prerelease of the same major.minor.patch (">=0.2.1-rc.5 <0.3.0"
	// admits 0.2.1-rc.7 but not 0.2.2-rc.1; "1.0.0 - 2.0.0" admits none).
	// Masterminds is looser, so check only those sets, prereleases enabled.
	for _, set := range strings.Split(rng, "||") {
		eligible := false
		for _, m := range prereleaseRe.FindAllStringSubmatch(set, -1) {
			if pv, err := semver.NewVersion(m[1]); err == nil &&
				pv.Major() == v.Major() && pv.Minor() == v.Minor() && pv.Patch() == v.Patch() {
				eligible = true
			}
		}
		if !eligible {
			continue
		}
		sc, err := semver.NewConstraint(set)
		if err != nil {
			return false, err
		}
		sc.IncludePrerelease = true
		if sc.Check(v) {
			return true, nil
		}
	}
	return false, nil
}

// prereleaseRe finds versions carrying a prerelease tag ("0.2.1-rc.5"); the
// spaced " - " of a hyphen range does not match.
var prereleaseRe = regexp.MustCompile(`v?(\d+\.\d+\.\d+-[0-9A-Za-z.-]+)`)

func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.Contains(p, ":") {
		return false
	}
	for _, part := range strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }) {
		if part == ".." {
			return false
		}
	}
	return true
}
