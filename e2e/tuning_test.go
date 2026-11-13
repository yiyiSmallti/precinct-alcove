//go:build e2e

// End-to-end checks for model tuning (SPEC 3.1, 模型与推理): the reasoning
// effort and compaction settings reach a real dsh runtime, an idle runtime
// restarts when they change, a model the service lists beyond pi-ai's
// catalog runs, and the efforts Alcove offers per model are exactly those
// dsh accepts.
//
// Ways this can fail, each checked below:
//   - the chosen effort never reaches the model request, or a stale one does
//     (the runtime is not restarted after a change);
//   - an effort the model does not take is passed and initialize refuses it;
//   - the compaction variable is malformed and the runtime does not start;
//   - a context cap makes retainRatio ≥ thresholdRatio (refused at load);
//   - an unlisted model of a catalog route fails with UNKNOWN_MODEL;
//   - a window widened past the catalog's is not the one requests run with;
//   - the catalog's levels disagree with dsh's (an offered level refused,
//     or an accepted one hidden).
//
// Same environment as plugininstall_test.go. Writes e2e/report/tuning.md.
package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/catalog"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
)

func TestModelTuning(t *testing.T) {
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "模型调参 e2e：思考强度、压缩、未收录模型、档位一致性", file: "tuning.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s", ver, runtime.GOOS, runtime.GOARCH, time.Now().UTC().Format(time.RFC3339)))

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{
		DataDir: filepath.Join(work, "data"), AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		Secrets: &memSecrets{m: map[string][]byte{}}, Answerer: &scriptedAnswerer{outcome: "allowed-once"},
	})
	if err == nil {
		err = a.Start(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	// The fake model keeps what each request asked for.
	var mu sync.Mutex
	var asked []string
	model, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	model.Respond = func(body []byte) (*fakemodel.ToolCall, string) {
		var req struct {
			Model    string `json:"model"`
			Thinking struct {
				Type string `json:"type"`
			} `json:"thinking"`
			Output struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		json.Unmarshal(body, &req)
		mu.Lock()
		asked = append(asked, fmt.Sprintf("%s thinking=%s effort=%s", req.Model, req.Thinking.Type, req.Output.Effort))
		mu.Unlock()
		return nil, "ok"
	}
	last := func() string {
		mu.Lock()
		defer mu.Unlock()
		if len(asked) == 0 {
			return ""
		}
		return asked[len(asked)-1]
	}

	cr, err := a.Creds.Add("假 DeepSeek", "deepseek-official", model.URL, "sk-e2e-tuning", "deepseek-v4-pro")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "调参", dir)
	if err != nil {
		t.Fatal(err)
	}
	set := func(f func(*app.Settings)) {
		st, _ := a.Settings()
		f(&st)
		if err := a.SaveSettings(st); err != nil {
			t.Fatal(err)
		}
	}
	turn := 0
	send := func() (string, string, error) {
		turn++
		p, err := a.OpenWorkspace(ctx, w.ID)
		if err != nil {
			return "", "", err
		}
		sid := fmt.Sprintf("tune-%d-%d", turn, time.Now().UnixNano())
		if _, err := p.PromptText(ctx, sid, "hi"); err != nil {
			return p.ID, "", err
		}
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		err = p.Wait(wctx, sid)
		// A plugin whose config dsh rejects only warns and the runtime
		// starts anyway: its stderr tells.
		if tail := p.Stderr(200); err == nil && strings.Contains(tail, "did not activate") {
			err = fmt.Errorf("a plugin did not activate: %s", tail)
		}
		return p.ID, last(), err
	}
	expect := func(got, want string) error {
		if !strings.Contains(got, want) {
			return fmt.Errorf("request %q, want %q", got, want)
		}
		return nil
	}

	// 1. The chosen effort and compaction point reach the request, under a
	// context cap.
	set(func(s *app.Settings) { s.ReasoningEffort, s.Compaction.Threshold = "low", 60 })
	err = a.Creds.SetContext(cr.ID, "deepseek-v4-pro", 64000)
	id1, got, err2 := send()
	if err == nil {
		err = err2
	}
	if err == nil {
		err = expect(got, "deepseek-v4-pro thinking=enabled effort=low")
	}
	r.step("思考强度「低」、压缩 60%、上下文上限 64K：运行时起来，请求带 effort=low", err, got)

	// 2. A new effort restarts the idle runtime and reaches the next request.
	set(func(s *app.Settings) { s.ReasoningEffort = "max" })
	id2, got, err := send()
	if err == nil && id2 == id1 {
		err = fmt.Errorf("runtime %s was not restarted", id1)
	}
	if err == nil {
		err = expect(got, "effort=max")
	}
	r.step("改成「极限」：空闲运行时重启，下一次请求带 effort=max", err, got)

	// 3. Off turns thinking off; automatic compaction off still starts.
	set(func(s *app.Settings) { s.ReasoningEffort, s.Compaction.Off = "off", true })
	_, got, err = send()
	if err == nil {
		err = expect(got, "thinking=disabled effort=")
	}
	r.step("关闭思考、关闭自动压缩：请求不再思考", err, got)

	// 4. An effort the model does not take is dropped, not refused.
	set(func(s *app.Settings) { s.ReasoningEffort, s.Compaction = "medium", app.Compaction{} })
	_, got, err = send()
	if err == nil {
		err = expect(got, "effort=high") // the adapter's own default
	}
	r.step("模型不支持的档位（medium）不下发，用服务商默认", err, got)

	// 5-6. pi-ai routes, initialize only (no request leaves): an unlisted
	// model is declared, and the catalog's levels are dsh's.
	cat := a.Catalog
	init := func(provider, mdl, effort string, env ...string) error {
		p, err := alrt.Launch(ctx, a.CLI, alrt.LaunchSpec{
			Profile: w.Profile, Cwd: dir, Provider: provider, Model: mdl, ReasoningEffort: effort,
			Env: append([]string{"OPENAI_API_KEY=sk-e2e", "ANTHROPIC_API_KEY=sk-e2e", "GEMINI_API_KEY=sk-e2e"}, env...),
		})
		if err == nil {
			p.Stop(0)
		}
		return err
	}
	_, listed := cat.Lookup("openai", "gpt-e2e-unlisted")
	err = init("openai", "gpt-e2e-unlisted", "")
	refused := err != nil && strings.Contains(err.Error(), "no configured model")
	err = init("openai", "gpt-e2e-unlisted", "", `ALCOVE_ROUTE_PATCH={"openai":{"models":[{"id":"gpt-e2e-unlisted"}]}}`)
	if err == nil && (listed || !refused) {
		err = fmt.Errorf("listed=%v, refused without declaring=%v", listed, refused)
	}
	r.step("目录未收录的 OpenAI 模型：不声明会被拒，声明后 initialize 通过", err, "")

	var mismatches []string
	checked := 0
	for _, c := range []struct{ provider, model string }{
		{"openai", "gpt-6.1-sol"}, {"openai", "gpt-4.1"}, {"anthropic", "claude-opus-4-6"},
		{"anthropic", "claude-sonnet-5-5"}, {"google", "gemini-3.1-pro-preview"},
	} {
		m, ok := cat.Lookup(c.provider, c.model)
		if !ok {
			mismatches = append(mismatches, c.model+": not in catalog")
			continue
		}
		for _, e := range catalog.Efforts {
			err := init(c.provider, c.model, e)
			checked++
			if accepted := err == nil; accepted != m.Supports(e) {
				mismatches = append(mismatches, fmt.Sprintf("%s %s: offered=%v accepted=%v", c.model, e, m.Supports(e), accepted))
			}
		}
	}
	err = nil
	if len(mismatches) > 0 {
		err = fmt.Errorf("%s", strings.Join(mismatches, "; "))
	}
	r.step("目录给出的思考档位与 dsh 实际接受的一致", err, fmt.Sprintf("%d 个模型×档位组合", checked))

	// 7. A window wider than pi-ai's own reaches the request: the runtime
	// records the window each request ran with (request/context). The
	// anthropic route is pointed at the fake model, which speaks its
	// protocol.
	gpt, _ := cat.Lookup("openai", "gpt-6.1-sol")
	claude, _ := cat.Lookup("anthropic", "claude-sonnet-5-5")
	wide := claude.Context + 500000
	patch, _ := json.Marshal(map[string]any{"anthropic": map[string]any{
		"baseURL":        model.URL,
		"modelOverrides": map[string]any{"claude-sonnet-5-5": map[string]any{"contextWindow": wide}},
	}})
	window, err := requestWindow(ctx, a, w.Profile, dir, "anthropic", "claude-sonnet-5-5", "ANTHROPIC_API_KEY=sk-e2e", "ALCOVE_ROUTE_PATCH="+string(patch))
	if err == nil && window != wide {
		err = fmt.Errorf("request ran with window %d, want %d (catalog %d)", window, wide, claude.Context)
	}
	if err == nil && (gpt.Context != 272000 || gpt.Max < 1000000) {
		err = fmt.Errorf("gpt-6.1-sol window %d, max %d", gpt.Context, gpt.Max)
	}
	r.step("上下文放大到目录窗口以上：请求按新窗口运行（gpt-6.1-sol 可选到 1.05M）", err,
		fmt.Sprintf("claude-sonnet-5-5 目录 %d → 请求 %d；gpt-6.1-sol 默认 %d，最大 %d", claude.Context, window, gpt.Context, gpt.Max))
}

// requestWindow runs one turn of model on route and returns the context
// window the runtime recorded for its request.
func requestWindow(ctx context.Context, a *app.App, profile, dir, route, model string, env ...string) (int, error) {
	p, err := alrt.Launch(ctx, a.CLI, alrt.LaunchSpec{Profile: profile, Cwd: dir, Provider: route, Model: model, Env: env})
	if err != nil {
		return 0, err
	}
	defer p.Stop(0)
	window := make(chan int, 1)
	go func() {
		for ev := range p.Events() {
			var e struct {
				Event struct {
					Type string `json:"type"`
					Data struct {
						ContextWindow int `json:"contextWindow"`
					} `json:"data"`
				} `json:"event"`
			}
			if json.Unmarshal(ev.Params, &e) == nil && e.Event.Type == "request/context" {
				select {
				case window <- e.Event.Data.ContextWindow:
				default:
				}
			}
		}
	}()
	sid := fmt.Sprintf("wide-%d", time.Now().UnixNano())
	if _, err := p.PromptText(ctx, sid, "hi"); err != nil {
		return 0, err
	}
	wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := p.Wait(wctx, sid); err != nil {
		return 0, err
	}
	select {
	case n := <-window:
		return n, nil
	case <-time.After(5 * time.Second):
		return 0, fmt.Errorf("no request/context event; stderr: %s", p.Stderr(20))
	}
}
