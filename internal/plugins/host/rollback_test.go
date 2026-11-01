//go:build !windows

package host

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/dsh"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/pkg"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

// stubDsh adds packages by writing the profile manifest, fails --dump-config
// for the profile named "broken", and fails every remove while $STUCK exists.
const stubDsh = `
home="$DSH_HOME"
case "$1" in
--version) echo 0.2.1-rc.5 ;;
plugin)
  prof="$3"; dir="$home/profiles/$prof"; mkdir -p "$dir"
  if [ "$4" = add ]; then
    printf '{"dependencies":{"@x/p":"file:%s"},"dsh":{"profile":{"bundles":["@x/p"]}}}' "$5" > "$dir/package.json"
  else
    [ -e "$STUCK" ] && { echo "remove failed" >&2; exit 1; }
    echo '{}' > "$dir/package.json"
  fi ;;
--profile) [ "$2" = broken ] && { echo "bad patch" >&2; exit 1; } ;;
esac
`

func TestIncompleteRollbackKeepsRecord(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "dsh.sh")
	os.WriteFile(script, []byte(stubDsh), 0o755)
	stuck := filepath.Join(dir, "stuck")
	os.WriteFile(stuck, nil, 0o644)
	cli := &dsh.CLI{Node: "/bin/sh", Bin: script, Home: filepath.Join(dir, "home"), Env: []string{"STUCK=" + stuck}}
	h := &Host{DSH: cli, Dir: filepath.Join(dir, "plugins"), AlcoveVersion: "0.1.0", GOOS: "linux"}

	tgz := filepath.Join(dir, "p.tgz")
	os.WriteFile(tgz, []byte("archive"), 0o644)
	plan := &Plan{Path: tgz, Package: &pkg.Package{Name: "@x/p", Version: "1.0.0"}, Profiles: []string{"ok", "broken"}, SignedBy: -1}
	d, err := sig.Digest(tgz)
	if err != nil {
		t.Fatal(err)
	}
	plan.SHA256 = hex.EncodeToString(d)

	_, err = h.Install(context.Background(), plan)
	se, ok := err.(*StepError)
	if !ok || se.RolledBack {
		t.Fatalf("want an incomplete rollback, got %v", err)
	}
	list, _ := h.List()
	if len(list) != 1 || !list[0].Incomplete {
		t.Fatalf("want one incomplete record, got %+v", list)
	}
	if _, err := os.Stat(list[0].Stored); err != nil {
		t.Fatalf("archive gone: %v", err)
	}
	all, _ := h.load()
	if probs := stateProblems(plan.Package, all); len(probs) != 1 || probs[0].Code != "already-installed" {
		t.Fatalf("Prepare would not see the leftover: %v", probs)
	}

	// A dependent package must not count the leftover as installed.
	dep := &pkg.Package{Name: "@x/q", Version: "1.0.0"}
	dep.Alcove = &pkg.Alcove{Requires: []string{"@x/p@^1.0.0"}}
	if probs := stateProblems(dep, all); len(probs) != 1 || probs[0].Code != "requires" {
		t.Fatalf("leftover accepted as a requirement: %v", probs)
	}

	// Once removal works again, Uninstall cleans up.
	os.Remove(stuck)
	if err := h.Uninstall(context.Background(), "@x/p"); err != nil {
		t.Fatal(err)
	}
	if list, _ := h.List(); len(list) != 0 {
		t.Fatalf("records left: %+v", list)
	}
}

// A package added to a target profile by hand is not taken over or removed.
func TestPreexistingProfilePackageIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "dsh.sh")
	os.WriteFile(script, []byte(stubDsh), 0o755)
	cli := &dsh.CLI{Node: "/bin/sh", Bin: script, Home: filepath.Join(dir, "home"), Env: []string{"STUCK=" + filepath.Join(dir, "none")}}
	h := &Host{DSH: cli, Dir: filepath.Join(dir, "plugins"), AlcoveVersion: "0.1.0", GOOS: "linux"}
	manual := `{"dependencies":{"@x/p":"1.0.0"},"dsh":{"profile":{"bundles":["@x/p"]}}}`
	os.MkdirAll(cli.ProfileDir("ok"), 0o755)
	os.WriteFile(filepath.Join(cli.ProfileDir("ok"), "package.json"), []byte(manual), 0o644)

	tgz := filepath.Join(dir, "p.tgz")
	os.WriteFile(tgz, []byte("archive"), 0o644)
	d, _ := sig.Digest(tgz)
	plan := &Plan{Path: tgz, Package: &pkg.Package{Name: "@x/p", Version: "1.0.0"}, Profiles: []string{"ok"}, SignedBy: -1, SHA256: hex.EncodeToString(d)}
	_, err := h.Install(context.Background(), plan)
	if se, ok := err.(*StepError); !ok || se.Step != "check" {
		t.Fatalf("want a check error, got %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(cli.ProfileDir("ok"), "package.json")); string(got) != manual {
		t.Fatalf("manual install changed: %s", got)
	}
}
