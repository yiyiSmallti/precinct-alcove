//go:build e2e

// The organisation edition end to end, first two stretches (Charter
// mvp-architecture 7.8, the organisation script): one Alcove core with the
// real daily and cloister plugins, a real Cloister (access, sync, bothost;
// e2e/serve.ts in precinct-cloister) with the member's bot running in the
// cloud (the daily-tools engine where the daily runner would be), and a real
// Gatehouse in org mode with the real gatehouse-cloister plugin. Scan to
// join → the admin approves the device → the member asks their bot in WeCom
// for a reminder → a folder becomes an organisation workspace → a turn there goes
// out through the gateway → a phone number and a customer name leave as
// placeholders and come back as themselves → the organisation's audit holds
// categories and counts only → another device takes the workspace over and
// this one can no longer write to it, until it gets it back → the reminder
// comes due on the device and in WeCom → the member makes this device the
// bot's primary → a WeCom message is answered by the daily engine here,
// through the gateway → a reminder set by the engine here comes due from the
// cloud once the bot is back there → the cloud's model is told what was said
// while the bot ran on the device → the bot runs on the device again, the
// device goes offline and the member moves the bot to the cloud from another
// device → the reminder due meanwhile fires on both, and once the device is
// back what it did offline is not taken: its copy follows the cloud's, the
// settings page says so, and nothing it wrote then reaches WeCom → leaving
// takes the workspace away and kills the gateway token.
//
// Only the model providers (the gateway's and the cloud bot's) and WeCom are
// stand-ins. The gateway's provider keeps what it was sent, which is the
// evidence of what left for the outside.
//
// Ways this could pass while the product is wrong, and what rules each out:
//   - nothing reached the provider at all: "no phone number in what it got"
//     would hold of nothing, so at least one request with a placeholder for
//     each value is required;
//   - the turn used the user's own credential instead of the gateway: the
//     personal endpoint must have seen no request, and the organisation's
//     audit must hold the call under the member's name;
//   - the gateway treated the provider as local (audit, no masking): the
//     values would be in the request;
//   - the gateway did not put the values back: the answer would show
//     placeholders;
//   - the audit kept values or placeholders: its record is searched for both;
//   - a gateway token that outlives membership: it is used once before
//     leaving (accepted) and again afterwards (refused);
//   - a bot that only seems to answer: the reply in WeCom and the reminder
//     carry the words of the request (the cloud bot's model takes them from
//     what the engine shows it), and the reminder must come no sooner than
//     its time after the reply, so it is the host's timer and not an echo;
//   - a bot turn on the device that uses the member's own API: the bot's
//     workspace belongs to the organisation, so once this device is primary
//     the personal endpoint must still have seen nothing, the gateway's
//     provider must have got new requests with placeholders and no values,
//     and the reply in WeCom must carry the values themselves;
//   - a reminder that reaches only one side: it is looked for both in the
//     daily assistant's conversation on the device and in WeCom;
//   - a bot workspace that does not follow the bot: the reminder set on the
//     device must come from the cloud (no ⏰, which the device would add)
//     after the switch back, and no sooner than asked; only the device's
//     engine ever had it. The switch waits for the device's turn to close,
//     and the bot must have promised it once: a turn still open at the
//     switch goes back to the cloud, whose engine would set the reminder
//     again on its own (a mutation run that drops the device's changes
//     passed before this);
//   - a conversation that does not follow the bot: what the cloud's model is
//     told after the switch back must hold the device's engine's own answer
//     to the message with orgMarker as a reply call in the conversation; the
//     rows that came along (the trigger, the outbox message) are no reply
//     call, so they cannot pass for it (a mutation run that skips taking the
//     log fails here);
//   - a device that comes back after the bot was taken from it while offline
//     looking right only because nothing happened offline: the device must
//     have fired the reminder itself (its log) while the cloud fired it in
//     WeCom; then exactly one reminder may reach WeCom, none with the
//     device's ⏰, even after the cloud's engine ran again (it would send a
//     reply the device left in the stream), and the device's own database
//     must no longer hold the reply it wrote offline (put back to the
//     cloud's version, not just never sent);
//   - "read-only" that is only the plugin no longer uploading: while another
//     device holds the workspace, the model here is told to write a file and
//     then to ask for full access, and with an answerer that would say yes
//     to anything, no file may appear and nothing may be asked.
//
// Not here yet: cloud tasks. e2e/org.sh starts everything and
// sets the variables below. Writes e2e/report/org-chain.md.
package e2e

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/e2e/fakemodel"
	"github.com/yiyiSmallti/precinct-alcove/e2e/fakeopenai"
	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

const (
	cloisterPlugin = "@precinct/cloister"
	cloisterPage   = "cloister" // its settings page
	orgPhone       = "13800138000"
	orgCustomer    = "星辰科技"
	// orgMarker is in the message next to the two values and is nothing a
	// gateway would mask: the provider finds the user's message by it.
	orgMarker = "回个电话"
	// orgWrite in a message is followed by the name of a file the model is
	// to write in the workspace.
	orgWrite = "ORGWRITE "
)

// placeholder is what the gateway puts in place of a value: {{KIND_xxxxxxxx}}.
var placeholder = regexp.MustCompile(`\{\{[A-Z_]+_[a-z2-7]{8}\}\}`)

func TestOrgChain(t *testing.T) {
	cloud := map[string]string{}
	for _, k := range []string{"CLOISTER_E2E_ACCESS", "CLOISTER_E2E_BOT", "CLOISTER_E2E_SYNC", "CLOISTER_E2E_ADMIN", "CLOISTER_E2E_GATEWAY_SECRET",
		"CLOISTER_E2E_WECOM", "CLOISTER_E2E_USER",
		"CLOISTER_E2E_MODEL",
		"ALCOVE_E2E_GATEHOUSE", "ALCOVE_E2E_GATEHOUSE_CLOISTER", "ALCOVE_E2E_GATEWAY_LISTEN", "DOVETAIL_DAILY_TGZ", "DOVETAIL_CLOISTER_TGZ"} {
		if cloud[k] = os.Getenv(k); cloud[k] == "" {
			t.Skipf("set %s (e2e/org.sh starts the cloud and the gateway and sets all of them)", k)
		}
	}
	access, admin := cloud["CLOISTER_E2E_ACCESS"], cloud["CLOISTER_E2E_ADMIN"]
	// The edition policy holds proactive WeCom pushes (the offline notice with
	// its 切回云端 button, say) outside 08:00-21:00 Asia/Shanghai. This chain
	// is not about that window (Cloister's e2e/wecom.ts is), so it is lifted:
	// otherwise the notice is held, rightly, whenever this runs after 13:00 UTC.
	var layers []struct {
		Level  string         `json:"level"`
		Target string         `json:"target"`
		Rules  map[string]any `json:"rules"`
	}
	if _, err := adminCall(http.MethodGet, access+"/v1/admin/policy/layers", admin, nil, &layers); err != nil {
		t.Fatalf("reading the policy layers: %v", err)
	}
	for _, l := range layers {
		if l.Level != "edition" {
			continue
		}
		if bot, ok := l.Rules["daily_bot"].(map[string]any); ok {
			delete(bot, "proactive_push_hours")
		}
		if _, err := adminCall(http.MethodPut, access+"/v1/admin/policy/layers/edition/"+l.Target, admin, map[string]any{"rules": l.Rules}, nil); err != nil {
			t.Fatalf("lifting the proactive push hours: %v", err)
		}
	}
	// The device reaches the cloud through these, as through its network:
	// cutting them takes it offline with its plugins still running.
	var net3 []*cuttable
	through := func(raw string) string {
		p, err := newCuttable(raw)
		if err != nil {
			t.Fatalf("a proxy in front of %s: %v", raw, err)
		}
		net3 = append(net3, p)
		return p.URL()
	}
	devAccess, devBot, devSync := through(access), through(cloud["CLOISTER_E2E_BOT"]), through(cloud["CLOISTER_E2E_SYNC"])
	offline := func(down bool) {
		for _, p := range net3 {
			p.set(down)
		}
	}
	defer func() {
		for _, p := range net3 {
			p.close()
		}
	}()
	gateway := "http://" + cloud["ALCOVE_E2E_GATEWAY_LISTEN"]
	ctx := context.Background()
	work := shortTemp(t)
	base := testCLI(t, work)
	ver, _ := base.Version(ctx)
	r := &report{t: t, title: "组织版整链验收（前两段）：扫码加入 → 设备审批 → 企微里设提醒 → 组织工作区 → 经网关脱敏 → 审计 → 提醒到客户端与企微 → 退出组织", file: "org-chain.md"}
	defer r.write(fmt.Sprintf("dsh %s · %s/%s · %s · %s · 真实 Cloister 与 Gatehouse，模型与企微是假的 · %s", ver, runtime.GOOS, runtime.GOARCH,
		filepath.Base(cloud["DOVETAIL_DAILY_TGZ"]), filepath.Base(cloud["DOVETAIL_CLOISTER_TGZ"]), time.Now().UTC().Format(time.RFC3339)))

	// The outside: the provider the gateway forwards to, and the endpoint
	// of the member's own credential, which must stay unused.
	provider, err := fakeopenai.Start()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	// The runtime wraps what the user typed in parts of its own; the
	// provider repeats the part the user typed.
	provider.Pick = func(parts []string) string {
		for i := len(parts) - 1; i >= 0; i-- {
			if strings.Contains(parts[i], orgMarker) {
				return parts[i]
			}
		}
		return ""
	}
	// The daily engine's turns, on this device once it is the bot's primary
	// environment: the newest candidate card it shows the model is answered
	// with a reply that repeats the message, then routed as done. Asked for
	// a reminder ("N 秒后提醒我…"), it sets one first, worded as the cloud
	// bot's model words them, and says so.
	provider.Turn = func(msgs []fakeopenai.Message) *fakeopenai.ToolCall {
		at, id, text := -1, "", ""
		for i, m := range msgs {
			for _, p := range m.Texts {
				var card struct {
					Kind      string `json:"kind"`
					TriggerID string `json:"trigger_id"`
					Text      string `json:"text"`
				}
				if strings.Contains(p, `"kind":"candidate"`) && json.Unmarshal([]byte(p), &card) == nil && card.TriggerID != "" {
					at, id, text = i, card.TriggerID, card.Text
				}
			}
		}
		if at < 0 {
			return nil
		}
		results := 0
		for _, m := range msgs[at+1:] {
			if m.Role == "tool" {
				results++
			}
		}
		calls := []*fakeopenai.ToolCall{
			{Name: "orchestrator_reply", Args: map[string]any{"trigger_id": id, "purpose": "answer", "text": "收到：" + text}},
			{Name: "orchestrator_route", Args: map[string]any{"decisions": []any{map[string]any{"kind": "ignore", "trigger_id": id, "reason": "已答复"}}}},
		}
		if m := regexp.MustCompile(`(\d+) 秒后提醒我(\S+?)[。.!！]?$`).FindStringSubmatch(strings.TrimSpace(text)); m != nil {
			secs, _ := time.ParseDuration(m[1] + "s")
			calls = append([]*fakeopenai.ToolCall{{Name: "schedule_create", Args: map[string]any{
				"at": time.Now().Add(secs).Format(time.RFC3339), "timezone": "Asia/Shanghai", "kind": "notify", "text": "提醒：" + m[2], "trigger_id": id}}}, calls...)
			calls[1].Args["text"] = fmt.Sprintf("好的，%s 秒后提醒你%s。", m[1], m[2])
		}
		// "N 秒后开始每天提醒我…": the first in N seconds, then daily.
		if m := regexp.MustCompile(`(\d+) 秒后开始每天提醒我(\S+?)[。.!！]?$`).FindStringSubmatch(strings.TrimSpace(text)); m != nil {
			secs, _ := time.ParseDuration(m[1] + "s")
			calls = append([]*fakeopenai.ToolCall{{Name: "schedule_create", Args: map[string]any{
				"at": time.Now().Add(secs).Format(time.RFC3339), "timezone": "Asia/Shanghai", "recurrence": "FREQ=DAILY;COUNT=3",
				"kind": "notify", "text": "提醒：" + m[2], "trigger_id": id}}}, calls...)
			calls[1].Args["text"] = fmt.Sprintf("好的，%s 秒后开始每天提醒你%s。", m[1], m[2])
		}
		if results < len(calls) {
			return calls[results]
		}
		return nil
	}
	// A message that names a file to write (orgWrite) has the model write
	// it, then create a second one from the shell, and when that is refused,
	// ask for full access to run the very same command: the ways a runtime
	// could still change the folder. The plain attempt comes first because
	// that is how a model gets there, and because dsh takes a request for
	// more access as being about a command the sandbox has just turned down.
	provider.Tool = func(parts []string, results int) *fakeopenai.ToolCall {
		name := ""
		for _, p := range parts {
			if m := regexp.MustCompile(orgWrite + `(\S+)`).FindStringSubmatch(p); m != nil {
				name = m[1]
			}
		}
		shell, touch := "bash", "touch "+name+".shell"
		if runtime.GOOS == "windows" {
			shell, touch = "pwsh", "New-Item -ItemType File -Force -Path '"+name+".shell' | Out-Null"
		}
		switch {
		case name == "":
			return nil
		case results == 0:
			return &fakeopenai.ToolCall{Name: "write", Args: map[string]any{"file_path": name, "content": "written by the model\n"}}
		case results == 1:
			return &fakeopenai.ToolCall{Name: shell, Args: map[string]any{"command": touch, "description": "create a file"}}
		case results == 2:
			return &fakeopenai.ToolCall{Name: shell, Args: map[string]any{"command": touch, "description": "create a file",
				"sandbox_permissions": "danger-full-access", "justification": "e2e: asks for full access"}}
		}
		return nil
	}
	personal, err := fakemodel.Start(0)
	if err != nil {
		t.Fatal(err)
	}
	defer personal.Close()

	gw, err := startGatehouse(filepath.Join(work, "gatehouse"), cloud, provider.URL)
	r.step("网关起来：真实 Gatehouse（org 模式）经 gatehouse-cloister 向真实 Cloister 查令牌与策略", err, gateway)
	if err != nil {
		return
	}
	defer gw.stop()

	bridgeTgz, err := packDir(filepath.Join("..", "bridge"), work)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	view := &inboxView{items: map[string]map[string]any{}}
	rec := &recorder{ui: map[string]sidecar.Contribution{}}
	dataDir := filepath.Join(work, "data")
	// It agrees to whatever it is asked: if a write is ever put to the user
	// while the workspace is held elsewhere, the write goes through and shows.
	answers := &scriptedAnswerer{outcome: "allowed-once"}
	a, err := app.New(app.Config{
		DataDir: dataDir, AlcoveVersion: "0.1.0",
		Node: base.Node, DSHBin: base.Bin, ToolDirs: base.ToolDirs, BridgeTgz: bridgeTgz,
		TrustedKeys: []ed25519.PublicKey{pub}, Secrets: &memSecrets{m: map[string][]byte{}},
		Answerer: answers, SidecarUI: rec, Inbox: view.post,
	})
	if err == nil {
		err = a.Start(ctx)
	}
	const personalKey = "sk-e2e-org-personal"
	if err == nil {
		_, err = a.Creds.Add("我的 API", "deepseek-official", personal.URL, personalKey, "")
	}
	r.step("首次启动，配了自己的 API（组织工作区不该用到它）", err, "")
	if err != nil {
		return
	}
	defer a.Close()
	// The same lines with the time each came, for telling what happened when.
	var stampMu sync.Mutex
	var stamped []string
	a.Sidecars.Services.Log = func(plugin, level, msg string) {
		rec.log(plugin, level, msg)
		stampMu.Lock()
		stamped = append(stamped, time.Now().Format("15:04:05.000")+" "+msg)
		stampMu.Unlock()
	}
	stampedTail := func(n int) string {
		stampMu.Lock()
		defer stampMu.Unlock()
		return strings.Join(stamped[max(0, len(stamped)-n):], "\n")
	}
	a.Sidecars.Backoff = 100 * time.Millisecond
	// The cloister sidecar opens the sign-in page in the system browser;
	// here the test follows that address itself, so the opener is a no-op.
	stubs := filepath.Join(work, "stubs")
	os.MkdirAll(stubs, 0o755)
	for _, name := range []string{"open", "xdg-open"} {
		os.WriteFile(filepath.Join(stubs, name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	a.Sidecars.Env = []string{
		"ALCOVE_DATA_DIR=" + dataDir,
		"PATH=" + stubs + string(os.PathListSeparator) + os.Getenv("PATH"),
		"PRECINCT_CLOISTER_BOT_URL=" + devBot,
		"PRECINCT_CLOISTER_SYNC_URL=" + devSync,
		"PRECINCT_CLOISTER_PENDING_POLL=300ms",
		"PRECINCT_CLOISTER_LEASE_RENEW=2s",
		// An org turn on the device closes only once its engine went idle (the
		// next one waits for that): 3 minutes by default.
		"PRECINCT_DAILY_IDLE_AFTER=10s",
	}
	logs := func() string {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return strings.Join(rec.logs, "\n")
	}
	defer func() {
		if t.Failed() {
			t.Logf("sidecar log:\n%s\ninbox: %s\ngatehouse:\n%s", logs(), strings.Join(view.texts(), " | "), gw.tail())
		}
	}()

	for _, src := range []string{cloud["DOVETAIL_DAILY_TGZ"], cloud["DOVETAIL_CLOISTER_TGZ"]} {
		if err == nil {
			err = installSigned(ctx, a, priv, src, work)
		}
	}
	if err == nil {
		err = waitFor(func() bool {
			d, _ := a.Sidecars.Status(dailyPlugin)
			c, _ := a.Sidecars.Status(cloisterPlugin)
			return d.State == sidecar.Running && c.State == sidecar.Running
		}, 60*time.Second)
	}
	r.step("装上 daily 与 cloister 两个插件（签名包），边车都在运行", err, "")
	if err != nil {
		return
	}

	// Join: the settings page's button, then the sign-in the page points at
	// (the stand-in WeCom signs the member in and sends the code back to
	// the plugin's local callback).
	page := func() string { return rec.raw(cloisterPlugin + "#" + cloisterPage) }
	values := func(v map[string]any) json.RawMessage {
		b, _ := json.Marshal(map[string]any{"values": v})
		return b
	}
	// The page's status line each time it changes: when a step here fails,
	// the order things happened in is what tells why.
	var seenMu sync.Mutex
	var seen []string
	started := time.Now()
	status := func() string {
		text := pageText(page())
		line := ""
		for _, mark := range []string{"等待扫码登录", "等待管理员批准", "已批准", "未加入组织"} {
			if l := lineWith(text, mark); l != "" {
				line = fmt.Sprintf("%.60s", l)
			}
		}
		seenMu.Lock()
		defer seenMu.Unlock()
		if line != "" && (len(seen) == 0 || !strings.HasSuffix(seen[len(seen)-1], line)) {
			seen = append(seen, fmt.Sprintf("+%s %s", time.Since(started).Round(100*time.Millisecond), line))
		}
		return text
	}
	err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "join", values(map[string]any{"server_url": devAccess, "device_name": "e2e 设备"}))
	var signIn string
	if err == nil {
		if waitFor(func() bool {
			signIn = regexp.MustCompile(`https?://[^\s"\\]+/v1/access/wecom/authorize[^\s"\\]*`).FindString(status())
			return signIn != ""
		}, 30*time.Second) != nil {
			err = errors.New("the settings page never showed the sign-in address")
		}
	}
	if err == nil {
		var resp *http.Response
		if resp, err = http.Get(signIn); err == nil {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("the sign-in round trip ended in %d at %s: %.300s", resp.StatusCode, resp.Request.URL.Host, body)
			}
		}
	}
	if err == nil && waitFor(func() bool { return strings.Contains(status(), "等待管理员批准") }, 30*time.Second) != nil {
		err = errors.New("after signing in, the device never showed as waiting for approval")
	}
	r.step("扫码加入：设置页发起，企微登录后设备登记为待批准", err, "")
	if err != nil {
		st, _ := a.Sidecars.Status(cloisterPlugin)
		seenMu.Lock()
		t.Logf("status line over time: %s\ncloister sidecar: %s, %d crashes, last error %q, stderr:\n%s\nsettings page:\n%s",
			strings.Join(seen, " | "), st.State, st.Crashes, st.LastError, st.Stderr, pageText(page()))
		seenMu.Unlock()
		return
	}

	var pending []map[string]any
	_, err = adminCall(http.MethodGet, access+"/v1/admin/devices?state=pending", admin, nil, &pending)
	if err == nil && len(pending) != 1 {
		err = fmt.Errorf("%d pending devices, want the one that just joined", len(pending))
	}
	if err == nil {
		_, err = adminCall(http.MethodPost, fmt.Sprintf("%s/v1/admin/devices/%v/approve", access, pending[0]["device_id"]), admin, nil, nil)
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "已批准") }, 20*time.Second)
	}
	r.step("管理员批准设备，插件收到并显示已加入", err, lineWith(pageText(page()), "已批准"))
	if err != nil {
		return
	}

	// In WeCom the member asks their bot for a reminder. The bot answers there;
	// the reminder comes due while the rest of the chain runs and is looked
	// for further down.
	wecom, member := cloud["CLOISTER_E2E_WECOM"], cloud["CLOISTER_E2E_USER"]
	spoke := time.Now()
	var said struct {
		Status int `json:"status"`
	}
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": fmt.Sprintf("%d 秒后提醒我%s", int(remindIn/time.Second), remindWhat)}, &said)
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	var reply wecomSent
	if err == nil {
		want := fmt.Sprintf("好的，%d 秒后提醒你%s。", int(remindIn/time.Second), remindWhat)
		// The first message wakes the member's cloud runner: the engine starts
		// and attaches before it answers.
		err = waitFor(func() bool { reply = wecomFind(wecom, member, want); return reply.Text != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no %q in WeCom; sent there: %v", want, wecomTexts(wecom, member))
		}
	}
	r.step(fmt.Sprintf("在企微里对日常 bot 说“%d 秒后提醒我%s”：云端的 bot（真实 daily 引擎）在企微里答应", int(remindIn/time.Second), remindWhat), err,
		fmt.Sprintf("%.0f 秒后答：%s", time.Since(spoke).Seconds(), reply.Text))
	if err != nil {
		return
	}

	// An organisation workspace out of an ordinary folder.
	dir := filepath.Join(work, "org-proj")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# 组织项目\n"), 0o644)
	w, err := a.CreateWorkspace(ctx, "组织项目", dir)
	if err == nil {
		// As the user would: the page has to offer the new workspace first.
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "mark:"+w.ID) }, 20*time.Second)
	}
	if err == nil {
		err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "submit", values(map[string]any{"mark:" + w.ID: "new:internal"}))
	}
	if err == nil {
		err = waitFor(func() bool { return owner(a, w.ID, cloisterPlugin) == nil }, 30*time.Second)
	}
	r.step("新建组织工作区：文件夹交给组织（内部级），工作区归 cloister 插件", err, "")
	if err != nil {
		return
	}

	// A turn in it, with the two values in the message.
	const sid = "org-chain-1"
	var mu sync.Mutex
	conv := events.NewSession(sid)
	a.OnEvent = func(_ string, e alrt.Event) {
		mu.Lock()
		defer mu.Unlock()
		conv.Apply(e.Method, e.Params)
	}
	p, err := a.OpenWorkspace(ctx, w.ID)
	if err == nil {
		_, err = p.PromptText(ctx, sid, fmt.Sprintf("请给%s的王经理%s，号码 %s。", orgCustomer, orgMarker, orgPhone))
	}
	if err == nil {
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err = p.Wait(wctx, sid)
		cancel()
	}
	var answer string
	if err == nil {
		err = waitFor(func() bool {
			mu.Lock()
			defer mu.Unlock()
			for _, e := range conv.Entries {
				if e.Kind == events.Assistant && !e.Streaming && e.Text != "" {
					answer = e.Text
				}
			}
			return answer != ""
		}, 10*time.Second)
	}
	if err != nil && p != nil {
		t.Logf("runtime stderr:\n%s", p.Stderr(40))
	}
	r.step("在组织工作区里跑一个回合：运行时按插件给的路由经网关调用模型", err, "")
	if err != nil {
		return
	}

	// What left for the outside, and what came back.
	sent := strings.Join(provider.Bodies(), "\n")
	kinds := map[string]bool{}
	for _, ph := range placeholder.FindAllString(sent, -1) {
		kinds[ph[2:strings.LastIndex(ph, "_")]] = true
	}
	switch {
	case len(provider.Bodies()) == 0:
		err = errors.New("the provider got no request: nothing shows what left")
	case strings.Contains(sent, orgPhone) || strings.Contains(sent, orgCustomer):
		err = errors.New("the provider was sent the phone number or the customer name as they are")
	case !kinds["PHONE"] || !kinds["TERM"]:
		err = fmt.Errorf("the request has no placeholder for the phone number and the customer name (found %v)", kinds)
	}
	r.step("到外部模型的请求里是占位符，没有手机号和客户名", err, fmt.Sprintf("外部模型收到 %d 个请求；占位符类别 %v", len(provider.Bodies()), keys(kinds)))
	switch {
	case !strings.Contains(answer, orgPhone) || !strings.Contains(answer, orgCustomer):
		err = fmt.Errorf("the answer does not carry the original values: %.200q", answer)
	case placeholder.MatchString(answer):
		err = fmt.Errorf("the answer still has placeholders: %.200q", answer)
	default:
		err = nil
	}
	r.step("回到对话里的回复是原值，没有占位符", err, fmt.Sprintf("%.120s", answer))
	switch {
	case personal.SawKey(personalKey):
		err = errors.New("the member's own credential was used")
	case strings.Contains(strings.Join(provider.Authorizations(), " "), personalKey):
		err = errors.New("the provider was called with the member's own key")
	default:
		err = nil
	}
	r.step("没有用到成员自己的 API", err, "")

	// The organisation's audit: the call is there, with counts and nothing else.
	var call map[string]any
	err = waitFor(func() bool {
		var recs []map[string]any
		adminCall(http.MethodGet, access+"/v1/admin/audit?action=model.call", admin, nil, &recs)
		for _, x := range recs {
			g, _ := x["gateway"].(map[string]any)
			counts, _ := g["redaction_counts"].(map[string]any)
			if n, _ := counts["phone"].(float64); n >= 1 {
				call = x
			}
		}
		return call != nil
	}, 30*time.Second)
	var raw []byte
	if err == nil {
		raw, _ = json.Marshal(call)
		g, _ := call["gateway"].(map[string]any)
		counts, _ := g["redaction_counts"].(map[string]any)
		switch {
		case strings.Contains(string(raw), orgPhone) || strings.Contains(string(raw), orgCustomer):
			err = errors.New("the audit record holds the values themselves")
		case placeholder.Match(raw):
			err = errors.New("the audit record holds placeholders")
		case counts["term"] == nil:
			err = fmt.Errorf("the audit record does not count the customer name: %v", counts)
		case call["actor"] != os.Getenv("CLOISTER_E2E_USER") && os.Getenv("CLOISTER_E2E_USER") != "":
			err = fmt.Errorf("the call is recorded under %v", call["actor"])
		}
	}
	r.step("组织的审计里有这次调用，只有类别与计数", err, strings.TrimSpace(fmt.Sprintf("%.300s", raw)))

	// The reminder asked for in WeCom: due now or soon, on both sides.
	due := "提醒：" + remindWhat
	var fired wecomSent
	err = waitFor(func() bool { fired = wecomFind(wecom, member, due); return fired.Text != "" && view.has(due) }, remindIn+2*time.Minute)
	switch {
	case err != nil:
		err = fmt.Errorf("in WeCom: %v; on the device: %v", wecomTexts(wecom, member), view.texts())
	case time.Duration(fired.At-reply.At)*time.Millisecond < remindIn-5*time.Second:
		err = fmt.Errorf("the reminder came %d ms after the reply, sooner than the %s asked for", fired.At-reply.At, remindIn)
	}
	r.step("到点提醒同时到客户端（daily 助手的对话）与企微", err, fmt.Sprintf("答应后 %.0f 秒到企微", float64(fired.At-reply.At)/1000))

	// The bot's primary environment moves to this device: from now on the
	// member's WeCom messages are answered by the daily engine here, and its
	// model calls are organisation data like a workspace turn's. They go out
	// through the organisation's gateway, never on the member's own key.
	err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "submit", values(map[string]any{"bot_primary": "device"}))
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "（本机）：主力") }, 30*time.Second)
	}
	r.step("把日常 bot 的主力环境切到本机：插件显示本机为主力", err, lineWith(pageText(page()), "主力"))
	if err != nil {
		return
	}
	sentBefore := len(provider.Bodies())
	ask := fmt.Sprintf("帮我记一下：给%s的王经理%s，号码 %s", orgCustomer, orgMarker, orgPhone)
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": ask}, &said)
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	var local wecomSent
	if err == nil {
		err = waitFor(func() bool {
			var sent []wecomSent
			adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
			for _, m := range sent {
				if strings.Contains(m.Text, orgMarker) && strings.HasPrefix(m.Text, "收到：") {
					local = m
				}
			}
			return local.Text != ""
		}, 3*time.Minute)
	}
	out := strings.Join(provider.Bodies()[sentBefore:], "\n")
	switch {
	case err != nil:
		err = fmt.Errorf("no answer in WeCom: %v; the gateway's provider got %d request(s) since; the member's own endpoint was used: %v",
			wecomTexts(wecom, member), len(provider.Bodies())-sentBefore, personal.SawKey(personalKey))
	case personal.SawKey(personalKey):
		err = errors.New("the device's bot turn used the member's own credential")
	case len(provider.Bodies()) == sentBefore:
		err = errors.New("the gateway's provider got no request for the bot turn")
	case strings.Contains(out, orgPhone) || strings.Contains(out, orgCustomer):
		err = errors.New("the bot turn sent the phone number or the customer name to the provider as they are")
	case !placeholder.MatchString(out):
		err = errors.New("the bot turn's requests have no placeholders")
	case !strings.Contains(local.Text, orgPhone) || !strings.Contains(local.Text, orgCustomer) || placeholder.MatchString(local.Text):
		err = fmt.Errorf("the answer in WeCom does not carry the original values: %q", local.Text)
	}
	r.step("本机为主力时，企微里的消息由本机的 daily 引擎回答：回合经组织网关（到外部模型的是占位符，企微收到原值），不用成员自己的 API", err,
		fmt.Sprintf("外部模型收到 %d 个请求；企微收到：%.80s", len(provider.Bodies())-sentBefore, local.Text))
	if err != nil {
		return
	}

	// The bot's workspace follows the bot. A reminder set while it runs here
	// lives only in the engine on this device; once the bot is back in the
	// cloud it must come due there, which it can only if the device's
	// changes reached the cloud's engine. The cloud sends a reminder's text
	// as it is, a device puts ⏰ in front: which side fired it shows.
	spoke = time.Now()
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": fmt.Sprintf("%d 秒后提醒我%s", int(handOverIn/time.Second), handOverWhat)}, &said)
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	var promised wecomSent
	if err == nil {
		want := fmt.Sprintf("好的，%d 秒后提醒你%s。", int(handOverIn/time.Second), handOverWhat)
		err = waitFor(func() bool { promised = wecomFind(wecom, member, want); return promised.Text != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no %q in WeCom; sent there: %v", want, wecomTexts(wecom, member))
		}
	}
	r.step(fmt.Sprintf("本机为主力时在企微里说“%d 秒后提醒我%s”：本机的 daily 引擎设下提醒并答应", int(handOverIn/time.Second), handOverWhat), err,
		fmt.Sprintf("%.0f 秒后答：%s", time.Since(spoke).Seconds(), promised.Text))
	if err != nil {
		return
	}
	// The turn closes once the engine here went idle. Switching before that
	// would hand the message back to the cloud, whose engine would set the
	// reminder itself: the cloud has to get it from the device.
	idle := func() int { return strings.Count(logs(), "org bot: daily turn process going idle") }
	idleAt := idle()
	err = waitFor(func() bool { return idle() > idleAt }, time.Minute)
	if err != nil {
		r.step("本机的引擎答完后空闲退出，回合关闭", err, "")
		return
	}
	// One more, due before the switch: the device fires it without its
	// engine (a one-shot reminder does not start it), so only the host knows
	// it went out. The switch hands it to the engine first; otherwise the
	// cloud finds it still due and sends it again.
	firedText := "提醒：" + firedWhat
	spoke = time.Now()
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": fmt.Sprintf("%d 秒后提醒我%s", int(firedIn/time.Second), firedWhat)}, &said)
	if err == nil {
		want := fmt.Sprintf("好的，%d 秒后提醒你%s。", int(firedIn/time.Second), firedWhat)
		err = waitFor(func() bool { return wecomFind(wecom, member, want).Text != "" }, 3*time.Minute)
	}
	if err == nil {
		idleAt = idle()
		err = waitFor(func() bool { return idle() > idleAt }, time.Minute)
	}
	var firedHereMsg wecomSent
	if err == nil {
		err = waitFor(func() bool { firedHereMsg = wecomFind(wecom, member, "⏰ "+firedText); return firedHereMsg.Text != "" }, firedIn+time.Minute)
	}
	r.step(fmt.Sprintf("本机为主力时设“%d 秒后提醒我%s”：引擎答完退出，提醒到点由本机发到企微（不拉起引擎）", int(firedIn/time.Second), firedWhat), err,
		fmt.Sprintf("%.0f 秒后：%s", time.Since(spoke).Seconds(), firedHereMsg.Text))
	if err != nil {
		return
	}
	if early := wecomFind(wecom, member, "⏰ 提醒："+handOverWhat); early.Text != "" {
		r.step("把主力切回云端：本机的引擎先交出它的改动，插件显示云端为主力",
			fmt.Errorf("%q came due on the device before the switch (%.0f s after the reply): handOverIn is too short for this machine", handOverWhat, float64(early.At-promised.At)/1000), "")
		return
	}
	// The member switches back from their phone, not from this device's
	// settings page: the bot host asks this device to hand over first
	// (bot.handover), and its engine takes the reminder fired above before
	// the cloud runs the bot.
	_, phoneToken, err := enrollDevice(access, admin, "成员的手机")
	cloudID := ""
	if err == nil {
		cloudID, err = cloudInstance(cloud["CLOISTER_E2E_BOT"], phoneToken)
	}
	switched := time.Now()
	handedAt := strings.Count(logs(), "handed the bot over")
	if err == nil {
		var moved map[string]any
		var code int
		code, err = adminCall(http.MethodPost, cloud["CLOISTER_E2E_BOT"]+"/v1/bot/instances/"+url.PathEscape(cloudID)+"/activate", phoneToken, map[string]any{}, &moved)
		if err == nil && code != http.StatusOK {
			err = fmt.Errorf("activating the cloud instance from the phone answered %d: %v", code, moved)
		}
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "云端：主力") }, 60*time.Second)
	}
	if err == nil && strings.Count(logs(), "handed the bot over") == handedAt {
		err = errors.New("the switch went ahead without this device handing the bot over")
	}
	r.step("成员在手机上把主力切回云端：本机先交出它的改动（bot.handover），插件显示云端为主力", err,
		fmt.Sprintf("%.0f 秒；%s", time.Since(switched).Seconds(), lineWith(pageText(page()), "主力")))
	if err != nil {
		return
	}
	handed := "提醒：" + handOverWhat
	var came wecomSent
	err = waitFor(func() bool {
		came = wecomSent{}
		var sent []wecomSent
		adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
		for _, m := range sent {
			if strings.Contains(m.Text, handed) {
				came = m
			}
		}
		return came.Text != "" && view.has(handed)
	}, handOverIn+3*time.Minute)
	promises := 0
	for _, m := range wecomTexts(wecom, member) {
		if m == promised.Text {
			promises++
		}
	}
	switch {
	case err != nil:
		err = fmt.Errorf("in WeCom: %v; on the device: %v", wecomTexts(wecom, member), view.texts())
	case promises != 1:
		err = fmt.Errorf("%q was said %d times: the cloud answered the message again instead of taking the device's state", promised.Text, promises)
	case strings.Contains(came.Text, "⏰"):
		err = fmt.Errorf("the reminder was fired by the device, not the cloud: %q", came.Text)
	case time.Duration(came.At-promised.At)*time.Millisecond < handOverIn-5*time.Second:
		err = fmt.Errorf("the reminder came %d ms after the reply, sooner than the %s asked for", came.At-promised.At, handOverIn)
	}
	r.step("本机设下的提醒在切回云端后由云端按时发出：到企微与客户端，没有⏰（不是本机发的）", err,
		fmt.Sprintf("答应后 %.0f 秒到企微：%s", float64(came.At-promised.At)/1000, came.Text))
	if err != nil {
		return
	}

	// The main Session's conversation follows the bot too: the cloud's
	// engine took the log the device's engine extended, so what was said
	// while the bot ran here is in what the cloud's model is told. The
	// device's answer to the message with orgMarker is checked as the
	// engine's own reply call in the conversation (an assistant tool_use),
	// which no workspace row turns into: the rows that came along (the
	// trigger, the outbox message) are no conversation.
	modelURL := cloud["CLOISTER_E2E_MODEL"]
	var modelSaw struct {
		Count  int      `json:"count"`
		Bodies []string `json:"bodies"`
	}
	_, err = adminCall(http.MethodGet, modelURL+"/_fake/requests?from=0", "", nil, &modelSaw)
	from := modelSaw.Count
	follow := "我们刚才在本机上说到哪了？"
	var followed wecomSent
	if err == nil {
		_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": follow}, &said)
	}
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	if err == nil {
		err = waitFor(func() bool { followed = wecomFind(wecom, member, "收到："+follow); return followed.Text != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no answer from the cloud in WeCom: %v", wecomTexts(wecom, member))
		}
	}
	carried, cloudAsked := "", 0
	if err == nil {
		modelSaw.Bodies = nil
		_, err = adminCall(http.MethodGet, modelURL+fmt.Sprintf("/_fake/requests?from=%d", from), "", nil, &modelSaw)
		cloudAsked = len(modelSaw.Bodies)
		for _, b := range modelSaw.Bodies {
			var req struct {
				Messages []struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"messages"`
			}
			if json.Unmarshal([]byte(b), &req) != nil {
				continue
			}
			for _, m := range req.Messages {
				var blocks []struct {
					Type  string          `json:"type"`
					Name  string          `json:"name"`
					Input json.RawMessage `json:"input"`
				}
				if m.Role != "assistant" || json.Unmarshal(m.Content, &blocks) != nil {
					continue
				}
				for _, bl := range blocks {
					var in struct {
						Text string `json:"text"`
					}
					if bl.Type == "tool_use" && bl.Name == "orchestrator_reply" && json.Unmarshal(bl.Input, &in) == nil &&
						strings.HasPrefix(in.Text, "收到：") && strings.Contains(in.Text, orgMarker) {
						carried = in.Text
					}
				}
			}
		}
		if err == nil && carried == "" {
			err = fmt.Errorf("none of the cloud model's %d request(s) for %q has the device's answer about %q in its conversation", cloudAsked, follow, orgMarker)
		}
	}
	r.step("会话记录跟着主力走：切回云端后，云端引擎回答企微里的新消息时，给模型的对话里有在本机时 bot 的回答", err,
		fmt.Sprintf("云端模型收到 %d 个请求；其中带着本机时的回答：%.80s", cloudAsked, carried))
	if err != nil {
		return
	}

	// The bot runs on the device again, and the device goes offline: its
	// network is gone, its plugins keep running. The member moves the bot to
	// the cloud from their phone. A reminder due meanwhile fires on both: the
	// device does not know the bot left. When the device is back, what it
	// did offline is not taken, so the cloud does not send it again; the
	// device's copy goes back to the cloud's version, and its settings page
	// says what was not taken.
	caught := func() int { return strings.Count(logs(), "the org bot caught up with the cloud") }
	caughtAt := caught()
	err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "submit", values(map[string]any{"bot_primary": "device"}))
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "（本机）：主力") && caught() > caughtAt }, time.Minute)
	}
	r.step("再把主力切到本机：本机的引擎追上云端的变更", err, lineWith(pageText(page()), "主力"))
	if err != nil {
		return
	}
	spoke = time.Now()
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": fmt.Sprintf("%d 秒后开始每天提醒我%s", int(offlineIn/time.Second), offlineWhat)}, &said)
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	var kept wecomSent
	if err == nil {
		want := fmt.Sprintf("好的，%d 秒后开始每天提醒你%s。", int(offlineIn/time.Second), offlineWhat)
		err = waitFor(func() bool { kept = wecomFind(wecom, member, want); return kept.Text != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no %q in WeCom; sent there: %v", want, wecomTexts(wecom, member))
		}
	}
	if err == nil {
		// Pushed: the cloud has the reminder before the device goes. The
		// engine stays up (it goes idle only after a while), so the reminder
		// fires into a running engine that then cannot push.
		err = waitFor(func() bool {
			n, err := orgBotCount(dataDir, "SELECT count(*) FROM replica_log")
			return err == nil && n == 0
		}, 5*time.Second)
	}
	r.step(fmt.Sprintf("本机为主力时在企微里说“%d 秒后开始每天提醒我%s”：本机设下每天的提醒并推到云端", int(offlineIn/time.Second), offlineWhat), err,
		fmt.Sprintf("%.0f 秒后答：%s", time.Since(spoke).Seconds(), kept.Text))
	if err != nil {
		return
	}
	firedHere := func() int { return strings.Count(logs(), "org bot: schedule ") }
	firedAt := firedHere()
	offline(true)
	cutAt := time.Now()
	if firedHere() != firedAt {
		offline(false)
		r.step("本机断网（插件仍在运行），成员在企微里发消息，收到本机离线的提示，点「切回云端」", fmt.Errorf("the reminder fired before the device went offline (%.1f s after the promise)", time.Since(spoke).Seconds()), "")
		return
	}
	// The member writes in WeCom meanwhile. The message waits for the device,
	// and the member is told it is offline, with a 切回云端 button: one tap
	// moves the bot back to the cloud (forced), which then answers.
	time.Sleep(3 * time.Second) // the bot host sees the device gone (presence window)
	offlineAsk := "本机离线了吗"
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": offlineAsk}, &said)
	if err == nil && said.Status != http.StatusOK {
		err = fmt.Errorf("the bot host answered WeCom's callback with %d", said.Status)
	}
	var notice wecomCard
	if err == nil {
		err = waitFor(func() bool { notice = wecomCardWith(wecom, member, "切回云端"); return notice.TaskID != "" }, 30*time.Second)
		if err != nil {
			err = fmt.Errorf("no offline notice with a 切回云端 button in WeCom: %v", wecomTexts(wecom, member))
		}
	}
	if err == nil {
		var clicked struct{ Status int }
		_, err = adminCall(http.MethodPost, wecom+"/_fake/click", "", map[string]string{"userid": member, "task_id": notice.TaskID, "key": notice.Key, "response_code": notice.ResponseCode}, &clicked)
		if err == nil && clicked.Status != http.StatusOK {
			err = fmt.Errorf("the bot host answered the click with %d", clicked.Status)
		}
	}
	r.step("本机断网（插件仍在运行），成员在企微里发消息，收到本机离线的提示，点「切回云端」", err, notice.Title)
	if err != nil {
		offline(false)
		return
	}
	offDue := "提醒：" + offlineWhat
	var cloudFired wecomSent
	err = waitFor(func() bool {
		cloudFired = wecomSent{}
		var sent []wecomSent
		adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
		for _, m := range sent {
			if strings.Contains(m.Text, offDue) {
				cloudFired = m
			}
		}
		return cloudFired.Text != "" && firedHere() > firedAt
	}, offlineIn+3*time.Minute)
	if err != nil {
		err = fmt.Errorf("in WeCom: %v; fired on the device: %d", wecomTexts(wecom, member), firedHere()-firedAt)
	}
	unpushed := 0
	if err == nil {
		// The device's engine, still up, moves the reminder to the next day
		// and cannot push that: still offline.
		err = waitFor(func() bool {
			n, err := orgBotCount(dataDir, "SELECT count(*) FROM replica_log WHERE tbl = 'schedule'")
			unpushed = n
			return err == nil && n > 0
		}, 30*time.Second)
		if err != nil {
			err = fmt.Errorf("the device's engine logged no change of the schedule it fired offline: %v", err)
		}
	}
	r.step("离线期间提醒到点：云端发到企微，断网的本机也自己发了一遍（发不出去），它的引擎把日程推进到下一次", err,
		fmt.Sprintf("答应后 %.0f 秒到企微：%s；本机有 %d 条日程的改动推不出去", float64(cloudFired.At-kept.At)/1000, cloudFired.Text, unpushed))
	if err != nil {
		offline(false)
		return
	}
	offline(false)
	back := time.Now()
	err = waitFor(func() bool {
		return strings.Contains(pageText(page()), "处改动没有采用") && strings.Contains(pageText(page()), "云端：主力")
	}, 2*time.Minute)
	if err != nil {
		db, _ := orgBotDB(dataDir)
		dump, _ := exec.Command("sqlite3", "-cmd", ".timeout 5000", db, "SELECT 'effect', kind, key, attempts FROM effect; SELECT 'outbox', id, substr(value,1,160) FROM outbox; SELECT 'schedule', id, substr(value,1,240) FROM schedule; SELECT 'log', n, tbl, pk, op, substr(row,1,200) FROM replica_log; SELECT 'ctl', * FROM replica_ctl;").CombinedOutput()
		t.Logf("device org bot after coming back (%s):\n%s\ncut %s, mended %s; sidecar log with times:\n%s", db, dump, cutAt.Format("15:04:05.000"), back.Format("15:04:05.000"), stampedTail(80))
	}
	r.step("本机回线：插件显示云端为主力，并说明本机离线期间的改动没有采用", err,
		fmt.Sprintf("%.0f 秒；%s", time.Since(back).Seconds(), lineWith(pageText(page()), "没有采用")))
	if err != nil {
		return
	}
	// The cloud's engine runs again (a reminder asked for there): a reply the
	// device left in the stream would be sent now.
	nudge := fmt.Sprintf("%d 秒后提醒我%s", int(nudgeIn/time.Second), nudgeWhat)
	_, err = adminCall(http.MethodPost, wecom+"/_fake/say", "", map[string]string{"userid": member, "text": nudge}, &said)
	if err == nil {
		err = waitFor(func() bool { return wecomFind(wecom, member, "提醒："+nudgeWhat).Text != "" }, 3*time.Minute)
	}
	if err == nil {
		time.Sleep(5 * time.Second)
	}
	reminders, withClock, meetings, punches := 0, 0, 0, 0
	for _, m := range wecomTexts(wecom, member) {
		// Sent by the device while it ran the bot; the cloud must not send it
		// again after the switch.
		if strings.Contains(m, "提醒："+firedWhat) {
			punches++
			continue
		}
		if strings.Contains(m, offDue) {
			reminders++
		}
		// The cloud sent this one before the bot came back here; the device
		// must not send it again (the hand-over gave it to the engine).
		if strings.Contains(m, "提醒："+handOverWhat) {
			meetings++
		}
		if strings.Contains(m, "⏰") {
			withClock++
		}
	}
	// What the device changed offline went to the stream and came back
	// refused: nothing of it is left to push.
	unpushed, qerr := orgBotCount(dataDir, "SELECT count(*) FROM replica_log")
	switch {
	case err != nil:
		err = fmt.Errorf("the cloud never answered %q: %v", nudge, wecomTexts(wecom, member))
	case wecomFind(wecom, member, "收到："+offlineAsk).Text == "":
		err = fmt.Errorf("the message written while the device was offline was never answered from the cloud: %v", wecomTexts(wecom, member))
	case reminders != 1 || withClock != 0 || meetings != 1 || punches != 1:
		err = fmt.Errorf("%q reached WeCom %d times, %q %d, %q %d, %d other message(s) with the device's ⏰: %v", offDue, reminders, "提醒："+handOverWhat, meetings, "提醒："+firedWhat, punches, withClock, wecomTexts(wecom, member))
	case qerr != nil:
		err = qerr
	case unpushed != 0:
		err = fmt.Errorf("the device's org bot still has %d change(s) it never pushed", unpushed)
	}
	r.step("离线时在企微发的消息由云端回答；本机离线时发的提醒没有到企微，云端再跑也不会替它发；主力切来切去，每条提醒在企微里只有一条", err,
		fmt.Sprintf("企微里“%s” %d 条、“提醒：%s” %d 条、“提醒：%s” %d 条，其余带⏰ %d 条；本机还有 %d 条改动没推", offDue, reminders, handOverWhat, meetings, firedWhat, punches, withClock, unpushed))
	if err != nil {
		return
	}

	// The offline notice reached this device too, once it was back: a card in
	// the daily assistant whose 切回云端 button is the bot host's link, which
	// the cloister plugin follows. With the bot here again, a tap moves it to
	// the cloud.
	var card string
	for _, id := range view.order {
		if c, ok := view.items[id]["card"].(map[string]any); ok && strings.Contains(fmt.Sprint(c["body"]), "目前离线") {
			card = id
		}
	}
	caughtAt = caught()
	err = nil
	if card == "" {
		err = fmt.Errorf("no offline notice among what the device shows: %v", view.texts())
	}
	if err == nil {
		err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "submit", values(map[string]any{"bot_primary": "device"}))
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "（本机）：主力") && caught() > caughtAt }, time.Minute)
	}
	if err == nil {
		// The card is the daily assistant's (an org item); its button goes to cloister as org.action.
		err = a.Sidecars.UIAction(ctx, dailyPlugin, card, "switch_to_cloud", nil)
	}
	if err == nil {
		err = waitFor(func() bool { return strings.Contains(pageText(page()), "云端：主力") }, time.Minute)
	}
	r.step("本机上那条离线提示的「切回云端」按钮：链接交给 cloister 插件，主力从本机回到云端", err, lineWith(pageText(page()), "主力"))
	if err != nil {
		return
	}

	// Another of the member's devices takes the workspace over. From then on
	// this one may read it and nothing more: the model's write does not
	// land, and its request for full access is turned down without the user
	// being asked.
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(dir, name)); return err == nil }
	asked := func() int { answers.mu.Lock(); defer answers.mu.Unlock(); return len(answers.approvals) }
	write := func(sid, name string) error {
		p, err := a.OpenWorkspace(ctx, w.ID)
		if err != nil {
			return err
		}
		if _, err = p.PromptText(ctx, sid, "把结论记下来。"+orgWrite+name); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		return p.Wait(wctx, sid)
	}
	syncURL := cloud["CLOISTER_E2E_SYNC"]
	other, otherToken, err := enrollDevice(access, admin, "另一台设备")
	var spaces []map[string]any
	if err == nil {
		_, err = adminCall(http.MethodGet, syncURL+"/v1/sync/workspaces", otherToken, nil, &spaces)
	}
	if err == nil && len(spaces) != 1 {
		err = fmt.Errorf("%d organisation workspaces, want the one made above", len(spaces))
	}
	lease := ""
	var taken struct {
		LeaseToken string `json:"lease_token"`
	}
	if err == nil {
		lease = fmt.Sprintf("%s/v1/leases/%s", syncURL, url.PathEscape(fmt.Sprintf("workspace:%v", spaces[0]["workspace_id"])))
		_, err = adminCall(http.MethodPost, lease+"/reclaim", otherToken, map[string]any{"to": map[string]any{"kind": "device", "id": other}, "reason": "e2e：在另一台设备上接着做"}, &taken)
	}
	if err == nil && waitFor(func() bool { return strings.Contains(pageText(page()), "另一台设备在写，本机只读") }, 20*time.Second) != nil {
		err = errors.New("the settings page never showed the workspace as held by another device")
	}
	r.step("另一台设备把组织工作区接过去：插件显示本机只读", err, "")
	if err != nil {
		return
	}
	before0 := asked()
	err = write("org-chain-held", "held.txt")
	switch {
	case err != nil:
	case exists("held.txt"):
		err = errors.New("the model's write landed in a workspace another device holds")
	case exists("held.txt.shell"):
		err = errors.New("the model wrote from the shell, on its own or with full access")
	case asked() != before0:
		err = fmt.Errorf("the user was asked %d time(s) to let it write", asked()-before0)
	}
	r.step("别处持有期间本机只读：模型写文件写不进去，要求提权也不问用户、直接不给", err, "")

	// It gives the workspace back; this device takes it again and can write.
	_, err = adminCall(http.MethodPost, lease+"/release", otherToken, map[string]any{"lease_token": taken.LeaseToken}, nil)
	if err == nil && waitFor(func() bool { return strings.Contains(pageText(page()), "本机可写") }, 30*time.Second) != nil {
		err = errors.New("the lease was released and this device never showed the workspace as writable again")
	}
	if err == nil {
		err = write("org-chain-back", "back.txt")
	}
	if err == nil && !exists("back.txt") {
		err = errors.New("with the lease back, the model's write still did not land")
	}
	r.step("写入权回到本机后又能写", err, "")
	if err != nil {
		return
	}

	// Leaving. The gateway token of the workspace is used once before, so
	// that its refusal afterwards means something.
	tokenFile, _ := os.ReadFile(filepath.Join(dataDir, "cloister", "gateway", w.ID+".credentials.yaml"))
	token := ""
	if m := regexp.MustCompile(`PRECINCT_GATEWAY_TOKEN:\s*"([^"]+)"`).FindSubmatch(tokenFile); m != nil {
		token = string(m[1])
	}
	before, err := gw.chat(token, "ping")
	if err == nil && before != http.StatusOK {
		err = fmt.Errorf("before leaving, the gateway answered %d to the workspace's token", before)
	}
	if err == nil {
		err = a.Sidecars.UIAction(ctx, cloisterPlugin, cloisterPage, "leave", values(nil))
	}
	if err == nil {
		err = waitFor(func() bool {
			_, statErr := os.Stat(dir)
			return os.IsNotExist(statErr) && owner(a, w.ID, cloisterPlugin) != nil && strings.Contains(pageText(page()), "未加入组织")
		}, 60*time.Second)
	}
	r.step("退出组织：组织工作区的文件夹从本机删掉，工作区不再归组织", err, "")
	after := 0
	left := time.Now()
	if err == nil {
		err = waitFor(func() bool {
			after, _ = gw.chat(token, "ping")
			return after == http.StatusUnauthorized
		}, departureLimit)
		if err != nil {
			err = fmt.Errorf("after leaving, the gateway still answers %d to the old token", after)
		}
	}
	r.step(fmt.Sprintf("退出后旧的网关令牌失效（%s 之内）", departureLimit), err, fmt.Sprintf("退出前 %d；退出后 %s 起网关答 %d", before, time.Since(left).Round(time.Second), after))
}

// The reminder the member asks their bot for in WeCom: long enough to be
// due after the workspace steps rather than at once. The second is asked for
// while the bot runs on the device and comes due after it is back in the
// cloud.
const (
	remindIn   = 30 * time.Second
	remindWhat = "交周报"
	// Long enough for the next step (a whole device turn, then its 20 s
	// reminder) to finish before it is due: a device turn takes about 90 s
	// on CI.
	handOverIn   = 180 * time.Second
	handOverWhat = "开周会"
	// Set after that one and due before the switch, while the device's
	// engine is not running (it exits PRECINCT_DAILY_IDLE_AFTER after
	// answering).
	firedIn   = 20 * time.Second
	firedWhat = "打卡"
	// The third is set on the device and comes due while it is offline:
	// soon, so that it fires into the engine that set it, before that goes
	// idle (PRECINCT_DAILY_IDLE_AFTER).
	offlineIn   = 8 * time.Second
	offlineWhat = "写周报"
	nudgeIn     = 5 * time.Second
	nudgeWhat   = "喝水"
)

// wecomSent is a message the fake WeCom says the app sent; At is in ms.
type wecomSent struct {
	ToUser string `json:"touser"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
}

// wecomCard is a button card sent to the member in WeCom, as the stand-in
// WeCom's /_fake/click takes it.
type wecomCard struct{ TaskID, Key, ResponseCode, Title string }

// wecomCardWith is the latest card sent to the member with a button labelled
// label.
func wecomCardWith(wecom, member, label string) wecomCard {
	var sent []struct {
		Card *struct {
			TaskID    string `json:"task_id"`
			MainTitle struct {
				Title string `json:"title"`
			} `json:"main_title"`
			Buttons []struct {
				Text string `json:"text"`
				Key  string `json:"key"`
			} `json:"button_list"`
		} `json:"card"`
		ResponseCode string `json:"response_code"`
	}
	adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
	out := wecomCard{}
	for _, m := range sent {
		if m.Card == nil {
			continue
		}
		for _, b := range m.Card.Buttons {
			if b.Text == label {
				out = wecomCard{TaskID: m.Card.TaskID, Key: b.Key, ResponseCode: m.ResponseCode, Title: m.Card.MainTitle.Title}
			}
		}
	}
	return out
}

// cloudInstance is the member's cloud bot instance, as a device of theirs
// lists it.
func cloudInstance(bot, token string) (string, error) {
	var instances []struct {
		InstanceID string `json:"instance_id"`
		Kind       string `json:"kind"`
	}
	if _, err := adminCall(http.MethodGet, bot+"/v1/bot/instances", token, nil, &instances); err != nil {
		return "", err
	}
	for _, in := range instances {
		if in.Kind == "cloud" {
			return in.InstanceID, nil
		}
	}
	return "", fmt.Errorf("no cloud instance among %v", instances)
}

// wecomFind is the first message sent to the member in WeCom with exactly
// this text; none has an empty Text.
func wecomFind(wecom, member, text string) wecomSent {
	var sent []wecomSent
	adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
	for _, m := range sent {
		if m.Text == text {
			return m
		}
	}
	return wecomSent{}
}

func wecomTexts(wecom, member string) []string {
	var sent []wecomSent
	adminCall(http.MethodGet, wecom+"/_fake/sent?touser="+url.QueryEscape(member), "", nil, &sent)
	var out []string
	for _, m := range sent {
		out = append(out, m.Text)
	}
	return out
}

// departureLimit is how long a member's gateway token may go on working
// after they left. Gatehouse here runs with its default policy cache (how
// long it trusts an answer from Cloister before asking again), not a value
// shortened for the test: what is checked is what a deployment that changes
// nothing gets. That default is a few seconds; the limit adds room for the
// next request to notice.
const departureLimit = 10 * time.Second

// enrollDevice joins the organisation as one more device of the member the
// stand-in WeCom signs in, has the admin approve it, and returns its id and
// its organisation token: a device that exists only as HTTP calls.
func enrollDevice(access, admin, name string) (id, token string, err error) {
	const callback = "127.0.0.1:53682"
	client := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Host == callback {
			return http.ErrUseLastResponse
		}
		return nil
	}}
	resp, err := client.Get(access + "/v1/access/wecom/authorize?redirect_uri=" + url.QueryEscape("http://"+callback+"/cb") + "&state=s")
	if err != nil {
		return "", "", err
	}
	resp.Body.Close()
	loc, err := resp.Location()
	if err != nil {
		return "", "", fmt.Errorf("the sign-in did not come back with a code: %d", resp.StatusCode)
	}
	var joined struct {
		DeviceID     string `json:"device_id"`
		RefreshToken string `json:"refresh_token"`
	}
	if _, err = adminCall(http.MethodPost, access+"/v1/access/join", "", map[string]any{"code": loc.Query().Get("code"),
		"device": map[string]any{"name": name, "platform": "macos"}}, &joined); err != nil {
		return "", "", err
	}
	if _, err = adminCall(http.MethodPost, access+"/v1/admin/devices/"+joined.DeviceID+"/approve", admin, nil, nil); err != nil {
		return "", "", err
	}
	var grant struct {
		OrgToken string `json:"org_token"`
	}
	if _, err = adminCall(http.MethodPost, access+"/v1/access/refresh", "", map[string]any{"refresh_token": joined.RefreshToken}, &grant); err != nil {
		return "", "", err
	}
	return joined.DeviceID, grant.OrgToken, nil
}

// installSigned signs the package at src with priv and installs it.
func installSigned(ctx context.Context, a *app.App, priv ed25519.PrivateKey, src, work string) error {
	tgz := filepath.Join(work, filepath.Base(src))
	if err := copyFile(src, tgz); err != nil {
		return err
	}
	s, err := sig.Sign(tgz, priv)
	if err != nil {
		return err
	}
	os.WriteFile(tgz+".sig", []byte(s), 0o644)
	plan, err := a.Plugins.Prepare(ctx, tgz)
	if err != nil {
		return err
	}
	if !plan.OK() {
		return fmt.Errorf("%s: %v", filepath.Base(src), plan.Problems)
	}
	_, err = a.InstallPlugin(ctx, plan)
	return err
}

// pageText is every string of a settings page's JSON, one per line: the
// page as text, without JSON escaping in the way of a search.
func pageText(raw string) string {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case []any:
			for _, y := range x {
				walk(y)
			}
		case map[string]any:
			for _, y := range x {
				walk(y)
			}
		}
	}
	walk(v)
	return strings.Join(out, "\n")
}

// lineWith is the first line of text that contains sub.
func lineWith(text, sub string) string {
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// adminCall is one request to Cloister's admin API; out, when not nil, takes
// the JSON answer.
func adminCall(method, url, token string, body any, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, rd)
	if err != nil {
		return 0, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return resp.StatusCode, fmt.Errorf("%s %s: %d %.200s", method, url, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: %v in %.200s", method, url, err, b)
		}
	}
	return resp.StatusCode, nil
}

// gatehouse is a running Gatehouse.
type gatehouse struct {
	url string
	cmd *exec.Cmd
	out *bytes.Buffer
}

// startGatehouse runs the Gatehouse binary in org mode: tokens and policy
// from Cloister through the gatehouse-cloister plugin, one external provider
// (masked on the way out), and the customer name in its dictionary.
// Gatehouse takes its redaction rules from its own configuration; the
// organisation policy's redaction block does not reach it.
func startGatehouse(dir string, cloud map[string]string, provider string) (*gatehouse, error) {
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "words.txt"), []byte(orgCustomer+"\n"), 0o644)
	plugin := func(name, kind string) string {
		return fmt.Sprintf("  - { name: %s, kind: %s, path: %q, args: [--url, %q] }\n", name, kind, cloud["ALCOVE_E2E_GATEHOUSE_CLOISTER"], cloud["CLOISTER_E2E_ACCESS"])
	}
	cfg := fmt.Sprintf(`listen: %s
mode: org
data_dir: %q
providers:
  - { name: cloud, base_url: "%s/v1", target: external, models: [strong] }
default_group: external-strong
routing_groups:
  external-strong: { type: order, members: [cloud/strong] }
plugins:
%s%sredaction:
  mode_by_target: { local: audit, external: mask }
  categories: { phone: mask, term: mask }
  dictionary: %q
`, cloud["ALCOVE_E2E_GATEWAY_LISTEN"], dir, provider, plugin("cloister", "auth_policy"), plugin("cloister-audit", "audit_sink"), filepath.Join(dir, "words.txt"))
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte(cfg), 0o600); err != nil {
		return nil, err
	}
	g := &gatehouse{url: "http://" + cloud["ALCOVE_E2E_GATEWAY_LISTEN"], out: &bytes.Buffer{}}
	g.cmd = exec.Command(cloud["ALCOVE_E2E_GATEHOUSE"], "serve", "--config", file)
	g.cmd.Env = append(os.Environ(), "GATEHOUSE_CLOISTER_SECRET="+cloud["CLOISTER_E2E_GATEWAY_SECRET"])
	g.cmd.Stdout, g.cmd.Stderr = g.out, g.out
	if err := g.cmd.Start(); err != nil {
		return nil, err
	}
	err := waitFor(func() bool {
		resp, err := http.Get(g.url + "/healthz")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var h struct {
			Mode   string
			Policy struct{ Available bool }
		}
		json.NewDecoder(resp.Body).Decode(&h)
		return resp.StatusCode == http.StatusOK && h.Mode == "org" && h.Policy.Available
	}, 20*time.Second)
	if err != nil {
		g.stop()
		return nil, fmt.Errorf("gatehouse did not come up in org mode with its policy plugin: %s", g.tail())
	}
	return g, nil
}

func (g *gatehouse) stop() {
	if g.cmd.Process != nil {
		g.cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { g.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			g.cmd.Process.Kill()
		}
	}
}

func (g *gatehouse) tail() string {
	s := g.out.String()
	if len(s) > 3000 {
		s = s[len(s)-3000:]
	}
	return s
}

// chat sends one message through the gateway with token and returns the
// HTTP status.
func (g *gatehouse) chat(token, text string) (int, error) {
	body, _ := json.Marshal(map[string]any{"model": "auto", "messages": []any{map[string]any{"role": "user", "content": text}}})
	req, _ := http.NewRequest(http.MethodPost, g.url+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// cuttable stands between the device and a cloud service as the device's
// network: set(true) closes every connection through it and refuses new
// ones, set(false) lets them through again.
type cuttable struct {
	ln     net.Listener
	scheme string
	target string
	mu     sync.Mutex
	down   bool
	conns  map[net.Conn]struct{}
}

func newCuttable(raw string) (*cuttable, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &cuttable{ln: ln, scheme: u.Scheme, target: u.Host, conns: map[net.Conn]struct{}{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.pipe(c)
		}
	}()
	return p, nil
}

func (p *cuttable) URL() string { return p.scheme + "://" + p.ln.Addr().String() }

// keep tracks c while the network is up; false (and c closed) when it is down.
func (p *cuttable) keep(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		c.Close()
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *cuttable) drop(cs ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range cs {
		c.Close()
		delete(p.conns, c)
	}
}

func (p *cuttable) pipe(c net.Conn) {
	if !p.keep(c) {
		return
	}
	s, err := net.Dial("tcp", p.target)
	if err != nil {
		p.drop(c)
		return
	}
	if !p.keep(s) {
		p.drop(c)
		return
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(s, c); done <- struct{}{} }()
	go func() { io.Copy(c, s); done <- struct{}{} }()
	<-done
	p.drop(c, s)
}

func (p *cuttable) set(down bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.down = down
	if down {
		for c := range p.conns {
			c.Close()
			delete(p.conns, c)
		}
	}
}

func (p *cuttable) close() {
	p.ln.Close()
	p.set(true)
}

// orgBotCount runs a count over the device's org bot database (bot-org).
func orgBotCount(dataDir, query string) (int, error) {
	db, err := orgBotDB(dataDir)
	if err != nil {
		return 0, err
	}
	out, err := exec.Command("sqlite3", "-cmd", ".timeout 5000", db, query).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("sqlite3 %s: %v: %s", db, err, out)
	}
	var n int
	_, err = fmt.Sscan(strings.TrimSpace(string(out)), &n)
	return n, err
}

// orgBotDB is the device's org bot database (bot-org).
func orgBotDB(dataDir string) (string, error) {
	db := ""
	filepath.WalkDir(dataDir, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.Name() == "orchestrator.sqlite" && strings.Contains(filepath.ToSlash(path), "/bot-org/") {
			db = path
		}
		return nil
	})
	if db == "" {
		return "", fmt.Errorf("no org bot database under %s", dataDir)
	}
	return db, nil
}
