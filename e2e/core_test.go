//go:build e2e

// End-to-end checks for Alcove's local core (PRD 3.2, 3.3, F2): identity,
// credentials whose key only lives in the credential store and the runtime's
// environment, and workspaces. Writes e2e/report/core.md.
package e2e

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/identity"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/paths"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

// memSecrets stands in for the system credential store, which CI lacks.
// ALCOVE_E2E_KEYCHAIN=1 uses the real one (Credential Manager / Keychain).
type memSecrets struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memSecrets) Set(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = append([]byte(nil), v...)
	return nil
}

func (s *memSecrets) Get(k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	if !ok {
		return nil, credentials.ErrNotFound
	}
	return v, nil
}

func (s *memSecrets) Delete(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.m[k]; !ok {
		return credentials.ErrNotFound
	}
	delete(s.m, k)
	return nil
}

func TestCore(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	cli := testCLI(t, work)
	ver, _ := cli.Version(ctx)
	r := &report{t: t, title: "本地核心 e2e：身份、凭据、工作区（PRD 3.2、3.3、F2）", file: "core.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	data := filepath.Join(work, "alcove-data")
	os.Setenv("ALCOVE_DATA_DIR", data)
	defer os.Unsetenv("ALCOVE_DATA_DIR")
	root, err := paths.DataDir()
	if err == nil && root != data {
		err = fmt.Errorf("data dir %s", root)
	}
	r.step("数据目录可由 ALCOVE_DATA_DIR 指定", err, "")
	l := paths.Layout{Root: data}

	// 1. Identity: created once, stable afterwards.
	a, err := identity.LoadOrCreate(l.Identity())
	if err == nil {
		var b *identity.Identity
		if b, err = identity.LoadOrCreate(l.Identity()); err == nil && b.ID != a.ID {
			err = fmt.Errorf("identity changed: %s → %s", a.ID, b.ID)
		}
	}
	r.step("首次启动生成本地身份，再次启动不变", err, "")

	// 2. Credentials: the key stays out of every file.
	var secrets credentials.SecretStore = &memSecrets{m: map[string][]byte{}}
	store := "内存替身"
	if os.Getenv("ALCOVE_E2E_KEYCHAIN") == "1" {
		secrets, store = credentials.MygoSecrets{}, "系统凭据库"
	}
	model, err := fakemodel.Start(1)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	const key = "sk-e2e-core-9f3c1d7a"
	creds := &credentials.Store{Path: l.Credentials(), Secrets: secrets}
	_, err = creds.Add("bad", "deepseek-official", "ftp://x", key, "")
	if err == nil {
		err = errors.New("accepted a non-http base URL")
	} else {
		err = nil
	}
	var c *credentials.Credential
	if err == nil {
		c, err = creds.Add("DeepSeek（假服务）", "deepseek-official", model.URL, key, "")
	}
	if err == nil {
		err = noFileContains(data, key)
	}
	r.step("录入凭据：key 进凭据库，凭据文件里没有 key", err, store)
	if c == nil {
		return
	}

	// 3. Workspaces.
	ws := &workspace.Store{Path: l.Workspaces(), DataDir: data}
	dirA := filepath.Join(work, "proj-a")
	os.MkdirAll(dirA, 0o755)
	w, err := ws.Create("", dirA)
	if err == nil && (w.Name != "proj-a" || w.Profile != "ws-"+w.ID) {
		err = fmt.Errorf("unexpected record %+v", w)
	}
	r.step("新建工作区：名称取目录名，profile 为 ws-<id>", err, "")
	if err != nil {
		return
	}
	dup := dirA
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		dup = filepath.Join(work, "PROJ-A")
	}
	_, e1 := ws.Create("again", dup)
	os.MkdirAll(filepath.Join(data, "inside"), 0o755)
	_, e2 := ws.Create("inside", filepath.Join(data, "inside"))
	_, e3 := ws.Create("file", l.Identity())
	err = nil
	switch {
	case !errors.Is(e1, workspace.ErrTaken):
		err = fmt.Errorf("duplicate: %v", e1)
	case !errors.Is(e2, workspace.ErrInData):
		err = fmt.Errorf("inside data dir: %v", e2)
	case !errors.Is(e3, workspace.ErrNotDir):
		err = fmt.Errorf("file: %v", e3)
	}
	r.step("拒绝重复目录、数据目录内的目录和文件", err, "")

	// 4. A runtime started with the credential reaches the model and leaves
	// no key on disk after it exits.
	provider, _, env, err := creds.LaunchEnv(c.ID)
	var p *alrt.Process
	if err == nil {
		p, err = alrt.Launch(ctx, cli, alrt.LaunchSpec{Profile: "sdk", Cwd: w.Dir, Provider: provider, Model: "deepseek-v4-flash", Env: env, IdleTimeout: -1})
	}
	if err == nil {
		go func() {
			for range p.Events() {
			}
		}()
		err = turn(ctx, p, "core-"+fmt.Sprint(time.Now().UnixNano()))
		p.Stop(5 * time.Second)
	}
	if err == nil && !model.SawKey(key) {
		err = errors.New("model endpoint did not receive the key")
	}
	if err == nil {
		err = noFileContains(cli.Home, key)
	}
	if err == nil {
		err = noFileContains(data, key)
	}
	r.step("用凭据拉起运行时完成一个回合；退出后数据目录与 dsh home 都没有 key", err, "")

	// 5. Rename and remove; the folder stays.
	err = ws.Rename(w.ID, "项目 A")
	if err == nil {
		var got *workspace.Workspace
		if got, err = ws.Find(w.ID); err == nil && got.Name != "项目 A" {
			err = fmt.Errorf("name %q", got.Name)
		}
	}
	if err == nil {
		err = ws.Remove(w.ID)
	}
	if err == nil {
		if _, serr := os.Stat(filepath.Join(dirA, "hello.txt")); serr != nil {
			err = fmt.Errorf("workspace files gone: %v", serr)
		}
	}
	if err == nil {
		if _, ferr := ws.Find(w.ID); !errors.Is(ferr, workspace.ErrNotFound) {
			err = fmt.Errorf("still listed: %v", ferr)
		}
	}
	r.step("重命名与移除；移除不删目录里的文件", err, "")

	// 6. Removing the credential deletes its key.
	err = creds.Remove(c.ID)
	if err == nil {
		if _, gerr := secrets.Get("credential/" + c.ID); !errors.Is(gerr, credentials.ErrNotFound) {
			err = fmt.Errorf("key still stored: %v", gerr)
		}
	}
	r.step("删除凭据时凭据库里的 key 一并删除", err, "")
}

// noFileContains fails if any file under root contains s.
func noFileContains(root, s string) error {
	var hit string
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || hit != "" {
			return nil
		}
		if info, err := d.Info(); err != nil || info.Size() > 32<<20 || !info.Mode().IsRegular() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err == nil && strings.Contains(string(b), s) {
			hit = path
		}
		return nil
	})
	if hit != "" {
		return fmt.Errorf("key found in %s", hit)
	}
	return nil
}
