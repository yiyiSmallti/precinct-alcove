//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yiyiSmallti/precinct-alcove/internal/profile"
)

// TestBridgeRefresh: a profile made with an older bridge gets the shipped one
// (RefreshBridge, used before plugin sessions so session.cancel works).
func TestBridgeRefresh(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	cli := testCLI(t, work)
	var shipped struct{ Version string }
	if raw, err := os.ReadFile(filepath.Join("..", "bridge", "package.json")); err != nil || json.Unmarshal(raw, &shipped) != nil {
		t.Fatalf("read the shipped bridge version: %v", err)
	}
	old := filepath.Join(work, "old-bridge")
	os.MkdirAll(filepath.Join(old, "lib"), 0o755)
	for _, f := range []string{"cordis.patch.yml", "lib/index.js", "package.json"} {
		raw, err := os.ReadFile(filepath.Join("..", "bridge", f))
		if err != nil {
			t.Fatal(err)
		}
		if f == "package.json" {
			raw = []byte(strings.Replace(string(raw), `"version": "`+shipped.Version+`"`, `"version": "0.1.0"`, 1))
		}
		os.WriteFile(filepath.Join(old, f), raw, 0o644)
	}
	oldDir := filepath.Join(work, "o")
	newDir := filepath.Join(work, "n")
	os.MkdirAll(oldDir, 0o755)
	os.MkdirAll(newDir, 0o755)
	oldTgz, err := packDir(old, oldDir)
	if err != nil {
		t.Fatal(err)
	}
	newTgz, err := packDir(filepath.Join("..", "bridge"), newDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := (&profile.Assembler{DSH: cli, Bridge: oldTgz}).Base(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	version := func() string {
		var pj struct{ Version string }
		raw, _ := os.ReadFile(filepath.Join(cli.PackageDir("ws-old", profile.BridgePackage), "package.json"))
		json.Unmarshal(raw, &pj)
		return pj.Version
	}
	if v := version(); v != "0.1.0" {
		t.Fatalf("old profile has bridge %q", v)
	}
	a := &profile.Assembler{DSH: cli, Bridge: newTgz}
	changed, err := a.RefreshBridge(ctx, "ws-old")
	if err != nil || !changed || version() != shipped.Version {
		t.Fatalf("refresh: changed %v err %v version %q", changed, err, version())
	}
	if changed, err := a.RefreshBridge(ctx, "ws-old"); err != nil || changed {
		t.Fatalf("second refresh: changed %v err %v", changed, err)
	}
	if res, err := cli.DumpConfig(ctx, "ws-old"); err != nil {
		t.Fatalf("dump-config after refresh: %v %v", err, res.StderrTail(5))
	}
}
