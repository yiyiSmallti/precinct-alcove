//go:build e2e

// The personal edition end to end, in one Alcove core and one data
// directory (Charter mvp-architecture 7.8, the personal script): configure
// your own API → talk to the daily bot and set a reminder (the local daily
// turn process) → open a personal workspace and finish a task there (the
// worker runtime) → both runtimes exit when idle → the reminder shows up on
// time with no runtime running → nothing but the model endpoint was called.
//
// Needs DOVETAIL_DAILY_TGZ (a packed @precinct/daily). The traffic step
// needs a capture: e2e/personal.sh sets one up on Linux and passes
// ALCOVE_E2E_PCAP, ALCOVE_E2E_PCAP_MAC, ALCOVE_E2E_MODEL_LISTEN and
// ALCOVE_E2E_MODEL_URL; without them the step is reported as not checked.
// Not covered here: installing from the installer (A5 does that), and a
// real model. Writes e2e/report/personal.md.
package e2e

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

func TestPersonal(t *testing.T) {
	src := os.Getenv("DOVETAIL_DAILY_TGZ")
	if src == "" {
		t.Skip("set DOVETAIL_DAILY_TGZ to a packed @precinct/daily")
	}
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	pcap := os.Getenv("ALCOVE_E2E_PCAP")
	captured := "没有抓包"
	if pcap != "" {
		captured = "抓包 " + filepath.Base(pcap)
	}
	r := &report{t: t, title: "个人版整链验收：配置 API → 日常 bot 设提醒 → 个人工作区任务 → 空闲退出 → 提醒弹出 → 只有模型端点的流量", file: "personal.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s · %s · %s", ver, runtime.GOOS, runtime.GOARCH, filepath.Base(src), captured, time.Now().UTC().Format(time.RFC3339)))

	listen := os.Getenv("ALCOVE_E2E_MODEL_LISTEN")
	if listen == "" {
		listen = "127.0.0.1:0"
	}
	model, err := fakemodel.StartOn(listen, os.Getenv("ALCOVE_E2E_MODEL_URL"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer model.Close()
	bot := &personalModel{daily: &dailyModel{t: t}}
	model.Respond = bot.respond

	// Whatever happens below, say what was sent: a chain that broke half-way
	// is exactly when an unexpected call is worth seeing.
	defer trafficStep(r, pcap, model.URL)

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	view := &inboxView{items: map[string]map[string]any{}}
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	answers := &scriptedAnswerer{outcome: "allowed-once"}
	dataDir := filepath.Join(work, "data")
	const idle = 10 * time.Second
	a, err := app.New(app.Config{
		DataDir: dataDir, AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
		Answerer: answers, SidecarUI: rec, Inbox: view.post, RuntimeIdle: idle,
	})
	if err == nil {
		err = a.Start(ctx)
	}
	r.step("首次启动：全新的数据目录", err, "")
	if err != nil {
		return
	}
	defer a.Close()
	a.Sidecars.Services.Log = rec.log
	a.Sidecars.Backoff = 100 * time.Millisecond
	a.Sidecars.Env = []string{"ALCOVE_DATA_DIR=" + dataDir, "PRECINCT_DAILY_IDLE_AFTER=" + idle.String()}
	sidecarLog := func() string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return strings.Join(rec.logs, "\n")
	}
	defer func() {
		if t.Failed() {
			t.Logf("sidecar log:\n%s\ninbox: %s", sidecarLog(), strings.Join(view.texts(), " | "))
		}
	}()

	const key = "sk-e2e-personal"
	_, err = a.Creds.Add("我的 API", "deepseek-official", model.URL, key, "")
	r.step("配置自己的 API", err, "模型端点 "+model.URL)
	if err != nil {
		return
	}

	tgz := filepath.Join(work, filepath.Base(src))
	err = copyFile(src, tgz)
	if err == nil {
		s, serr := sig.Sign(tgz, priv)
		err = serr
		os.WriteFile(tgz+".sig", []byte(s), 0o644)
	}
	if err == nil {
		plan, perr := a.Plugins.Prepare(ctx, tgz)
		err = perr
		if err == nil && !plan.OK() {
			err = fmt.Errorf("problems: %v", plan.Problems)
		}
		if err == nil {
			_, err = a.InstallPlugin(ctx, plan)
		}
	}
	if err == nil {
		err = waitFor(func() bool { st, ok := a.Sidecars.Status(dailyPlugin); return ok && st.State == sidecar.Running }, 30*time.Second)
	}
	r.step("装上日常 bot（签名的 daily 包）", err, "")
	if err != nil {
		return
	}

	say := func(id, text, want string) error {
		err := a.Sidecars.InboxInput(ctx, dailyPlugin, id, text, time.Now())
		if err == nil {
			err = waitFor(func() bool { return view.has(want) }, 90*time.Second)
		}
		return err
	}
	err = say("in-1", "你好", "你好，我在")
	if err == nil && !model.SawKey(key) {
		err = errors.New("the model endpoint did not get the configured key")
	}
	r.step("与日常 bot 对话：本机 daily 回合进程用配置的 API 回答", err, "")
	if err != nil {
		return
	}
	err = say("in-2", "两分钟后提醒我喝水", "两分钟后提醒你")
	asked := time.Now()
	r.step("让日常 bot 设一条两分钟后的提醒", err, "")
	if err != nil {
		return
	}

	// The worker: a personal workspace and a task that writes a file there,
	// then one outside it, which the sandbox refuses until the user agrees.
	// The temp dir is writable under workspace-write, so "outside" is a
	// directory under the home directory.
	home, _ := os.UserHomeDir()
	outside, err := os.MkdirTemp(home, ".alcove-e2e-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(outside)
	escalated := filepath.Join(outside, "approved.txt")
	bot.task(escalated)
	dir := filepath.Join(work, "proj")
	os.MkdirAll(dir, 0o755)
	w, err := a.CreateWorkspace(ctx, "我的项目", dir)
	if err == nil {
		p, perr := a.OpenWorkspace(ctx, w.ID)
		err = perr
		if err == nil {
			err = turn(ctx, p, "personal-"+fmt.Sprint(time.Now().UnixNano()))
		}
	}
	var wrote []byte
	if err == nil {
		wrote, err = os.ReadFile(filepath.Join(dir, personalFile))
	}
	if err == nil && string(wrote) != personalText {
		err = fmt.Errorf("the task wrote %q", wrote)
	}
	answers.mu.Lock()
	approvals := len(answers.approvals)
	answers.mu.Unlock()
	if err == nil && approvals == 0 {
		err = errors.New("the write outside the workspace was not put to the user for approval")
	}
	if err == nil {
		if _, serr := os.Stat(escalated); serr != nil {
			err = fmt.Errorf("the approved command did not run: %v", serr)
		}
	}
	r.step("打开个人工作区完成一个任务：worker 写文件；越出工作区的写入先问用户，批准后执行", err, fmt.Sprintf("%s；审批 %d 次。%s", personalFile, approvals, approvalDetail(answers)))
	if err != nil {
		return
	}

	// Both runtimes go away by themselves, before the reminder is due: gone
	// from the runtime manager's list, and gone as processes.
	var alive []string
	err = waitFor(func() bool {
		alive, _ = runtimeProcesses(base.Node)
		return strings.Contains(sidecarLog(), "exited after idle") && len(a.Runtimes.List()) == 0 && len(alive) == 0
	}, 60*time.Second)
	if err != nil && len(alive) > 0 {
		err = fmt.Errorf("still running: %s", strings.Join(alive, "; "))
	}
	if err == nil && view.has("该喝水了") {
		err = fmt.Errorf("the reminder arrived %s after it was asked for, before the runtimes were gone", time.Since(asked).Round(time.Second))
	}
	r.step(fmt.Sprintf("空闲后运行时退出：daily 回合进程与工作区运行时都不在了，进程也没有了（验收里空闲时长设为 %s，默认 10 分钟）", idle), err,
		fmt.Sprintf("设提醒后 %s", time.Since(asked).Round(time.Second)))
	if err != nil {
		return
	}

	launches := strings.Count(sidecarLog(), "turn process attached")
	err = waitFor(func() bool { return view.has("该喝水了") }, 3*time.Minute)
	took := time.Since(asked)
	alive, _ = runtimeProcesses(base.Node)
	switch {
	case err != nil:
	case took < 110*time.Second || took > 150*time.Second:
		err = fmt.Errorf("the reminder arrived %s after it was asked for, want about 2m", took.Round(time.Second))
	case strings.Count(sidecarLog(), "turn process attached") != launches:
		err = errors.New("the turn process was started again for the reminder")
	case len(a.Runtimes.List()) != 0 || len(alive) != 0:
		err = fmt.Errorf("a runtime was running when the reminder showed up: %s", strings.Join(alive, "; "))
	}
	r.step("提醒到点弹出：由边车自己显示，没有拉起任何运行时", err, fmt.Sprintf("设提醒后 %s", took.Round(time.Second)))
}

// runtimeProcesses lists the Node processes under this one (a dsh runtime is
// a Node process started from node): the operating system's view, next to
// what the runtime manager believes. Windows has no ps; nothing is listed
// there.
func runtimeProcesses(node string) ([]string, error) {
	if runtime.GOOS == "windows" {
		return nil, nil
	}
	out, err := exec.Command("ps", "-eo", "pid=,ppid=,command=").Output()
	if err != nil {
		return nil, err
	}
	kids, cmds := map[int][]int{}, map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		pid, _ := strconv.Atoi(f[0])
		ppid, _ := strconv.Atoi(f[1])
		kids[ppid] = append(kids[ppid], pid)
		cmds[pid] = strings.Join(f[2:], " ")
	}
	var found []string
	for todo := kids[os.Getpid()]; len(todo) > 0; todo = todo[1:] {
		pid := todo[0]
		todo = append(todo, kids[pid]...)
		if strings.HasPrefix(cmds[pid], node) {
			found = append(found, fmt.Sprintf("%d %.200s", pid, cmds[pid]))
		}
	}
	return found, nil
}

const (
	personalFile = "note.txt"
	personalText = "written by the workspace task\n"
)

// personalModel is the one model endpoint the user configured: the daily
// bot's router and the workspace's worker both call it. A request that
// offers the write tool comes from the workspace (the daily profile has no
// file tools) and gets the next call of the task; everything else is the
// bot's.
type personalModel struct {
	daily *dailyModel
	mu    sync.Mutex
	plan  []fakemodel.ToolCall
}

// task sets what the workspace's worker does: write a file in the
// workspace, try to create outside (a path beyond it) under the sandbox,
// then ask for the escalation that needs the user's approval.
func (m *personalModel) task(outside string) {
	shell, cmd := "bash", "touch "+outside
	if runtime.GOOS == "windows" {
		shell, cmd = "pwsh", "New-Item -ItemType File -Force -Path '"+outside+"' | Out-Null"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.plan = []fakemodel.ToolCall{
		{Name: "write", Input: map[string]any{"file_path": personalFile, "content": personalText}},
		{Name: shell, Input: map[string]any{"command": cmd, "description": "write outside the workspace"}},
		{Name: shell, Input: map[string]any{
			"command": cmd, "description": "write outside the workspace",
			"sandbox_permissions": "danger-full-access", "justification": "e2e: needs a write outside the workspace",
		}},
	}
}

func (m *personalModel) respond(body []byte) (*fakemodel.ToolCall, string) {
	var req struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	json.Unmarshal(body, &req)
	workspace := false
	for _, t := range req.Tools {
		workspace = workspace || t.Name == "write"
	}
	if !workspace {
		return m.daily.respond(body)
	}
	done := 0
	for _, msg := range req.Messages {
		var blocks []msgBlock
		if json.Unmarshal(msg.Content, &blocks) != nil {
			continue
		}
		for _, b := range blocks {
			if b.Type == "tool_result" {
				done++
			}
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if done < len(m.plan) {
		c := m.plan[done]
		return &c, ""
	}
	return nil, "做完了。"
}

// trafficStep reports what the capture at pcap shows was sent during the
// test: every flow, and whether allowedFlow lets it through. modelURL is the
// endpoint the credential points at.
func trafficStep(r *report, pcap, modelURL string) {
	const name = "对外流量：全程只有模型端点"
	if pcap == "" {
		r.skip(name, "没有抓包，这一步没验（e2e/personal.sh 在 Linux 上会抓）")
		return
	}
	mac, err := net.ParseMAC(os.Getenv("ALCOVE_E2E_PCAP_MAC"))
	var model netip.AddrPort
	if err == nil {
		u, uerr := url.Parse(modelURL)
		err = uerr
		if err == nil {
			model, err = netip.ParseAddrPort(u.Host)
		}
	}
	var flows []flow
	if err == nil {
		flows, err = captureMark(pcap, mac, model, modelURL)
	}
	var lines, bad []string
	for _, f := range flows {
		if allowedFlow(f, model) {
			lines = append(lines, f.String())
		} else {
			lines = append(lines, "✗ "+f.String())
			bad = append(bad, f.String())
		}
	}
	if err == nil && len(bad) > 0 {
		err = fmt.Errorf("%d kind(s) of traffic that is not allowed: %s", len(bad), strings.Join(bad, "; "))
	}
	r.step(name, err, "从被测机器发出的全部流量（TCP 按连接数，其余按包数；最后一次模型连接是收尾探针）："+strings.Join(lines, "；"))
}

// captureMark opens one more connection to the model endpoint and waits for
// the capture to show it, so that the capture is known to have run to the
// end and to be flushed; it returns what the capture holds then. A capture
// with no model connection before the mark saw nothing of the test.
func captureMark(pcap string, mac net.HardwareAddr, model netip.AddrPort, modelURL string) ([]flow, error) {
	before, err := readCapture(pcap, mac)
	if err != nil {
		return nil, err
	}
	seen := modelConnections(before, model)
	if seen == 0 {
		return before, fmt.Errorf("the capture shows no connection to the model endpoint %s: it did not see this test's traffic", model)
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := client.Get(modelURL + "/v1/models")
	if err != nil {
		return before, fmt.Errorf("capture mark: %w", err)
	}
	resp.Body.Close()
	var flows []flow
	err = waitFor(func() bool {
		flows, _ = readCapture(pcap, mac)
		return modelConnections(flows, model) > seen
	}, 20*time.Second)
	if err != nil {
		return before, errors.New("the capture stopped before the end of the test: the closing connection to the model endpoint is not in it")
	}
	return flows, nil
}
