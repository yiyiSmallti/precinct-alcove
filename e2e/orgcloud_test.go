//go:build e2e

package e2e

// The cloud-task stretch of the organisation chain (Charter mvp-architecture
// 7.8: the bot sends a task to the cloud sandbox → this device is read-only
// → WeCom hears how it goes → pause with a snapshot → resume → the task ends
// → its result is pulled back here). It runs inside TestOrgChain while the
// bot runs in the cloud and this device holds the organisation workspace.
//
// The member asks the bot in WeCom for a piece of work. The bot's engine
// makes it a Ticket and, with this device online, asks the device to run it
// (a local task card, Cloister API 1.3.0); on the card the member sends it to
// the cloud instead (1.7.0): the plugin pushes the folder and hands it to a
// cloud task in a real sandbox (Cloister's orchestrator on containerd, with
// its stand-in runner in place of the worker's dsh). The task's events reach
// the bot through Cloister, so what it asks and what it ends with reach the
// member in WeCom through the bot.
//
// Ways this could pass while the product is wrong, and what rules each out:
//   - a dispatch that only moved the lease: the runner appends a line to
//     notes.md in the sandbox, and that line must be in the folder here
//     after the pause, which only a pulled snapshot brings;
//   - a resume that starts over: the resumed run writes resume-check.txt
//     with the number of session lines it found from before the pause, which
//     must be more than none;
//   - "read-only" that is only a label: the model here is told to write a
//     file while the task runs, and it must not appear, nor the user be asked;
//   - progress that reaches WeCom on its own and not through the bot: the
//     approval the task asks for and its result arrive in WeCom as the bot's
//     replies, whose words the bot's model takes from the reports the engine
//     relayed to it (the cloud model's requests are checked for the pause
//     too, which the bot hears without telling anyone).
//
// It needs the cloud started with real sandboxes (CLOISTER_E2E_TASKS=1, set
// by Cloister's e2e/serve.ts under CLOISTER_E2E_SANDBOX=1; e2e/org.sh does
// that with ALCOVE_E2E_SANDBOX=1). Without them the stretch is reported as
// not checked.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
)

// cloudStretch is what the stretch uses of TestOrgChain's state.
type cloudStretch struct {
	a        *app.App
	view     *inboxView
	answers  *scriptedAnswerer
	page     func() string
	wecom    string
	member   string
	modelURL string // the cloud bot's fake model
	wsID     string // the organisation workspace (Alcove id)
	dir      string // its folder
}

const cloudTaskTitle = "整理季度材料，发出前请 approve"

// stretch names, also for the report when it cannot run.
var cloudSteps = []string{
	"在企微里让 bot 派一件事：bot 在企微里答应，本机弹出确认卡，可以选本机运行，也可以派到云端",
	"在确认卡上选「派到云端」：先同步，工作区交给云端任务（真实沙盒），本机只读，模型写不进去也不问人",
	"云端任务要人批准：经 bot 转达到企微，提示到客户端的任务卡片上处理",
	"在任务卡片上暂停：云端留下快照，写入权回到本机，任务写的东西拉回文件夹；bot 知道任务暂停了",
	"继续在云端运行：同一个会话接着跑，结束后结果拉回本机，bot 在企微里报告结果",
}

func runCloudStretch(ctx context.Context, r *report, c cloudStretch) bool {
	if os.Getenv("CLOISTER_E2E_TASKS") != "1" {
		for _, s := range cloudSteps {
			r.skip(s, "没有真实沙盒：云不是带 CLOISTER_E2E_SANDBOX=1 起的（e2e/org.sh 要 ALCOVE_E2E_SANDBOX=1、root 与 containerd）")
		}
		return true
	}
	click := func(card, action string) error {
		// Cards of the cloister plugin are the daily assistant's org items; their buttons go to cloister.
		return c.a.Sidecars.UIAction(ctx, dailyPlugin, card, action, nil)
	}
	wecomWith := func(sub string) string {
		for _, t := range wecomTexts(c.wecom, c.member) {
			if strings.Contains(t, sub) {
				return t
			}
		}
		return ""
	}

	// ---------- the bot takes the work and asks the device ----------
	_, err := adminCall(http.MethodPost, c.wecom+"/_fake/say", "", map[string]string{"userid": c.member, "text": "派到云端：" + cloudTaskTitle}, nil)
	var ack, offer string
	if err == nil {
		err = waitFor(func() bool { ack = wecomWith("交给工作任务去做"); return ack != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no acknowledgement in WeCom: %v", wecomTexts(c.wecom, c.member))
		}
	}
	if err == nil {
		err = waitFor(func() bool {
			offer = c.view.cardTitled("云端 bot 想在本机运行：" + cloudTaskTitle)
			return offer != "" && strings.Contains(c.view.actions(offer), "cloud:"+c.wsID)
		}, 2*time.Minute)
		if err != nil {
			err = fmt.Errorf("no card offering the cloud: card %q, actions %q; inbox %v", offer, c.view.actions(offer), c.view.texts())
		}
	}
	r.step(cloudSteps[0], err, fmt.Sprintf("企微：%s；卡片按钮：%s", ack, c.view.actions(offer)))
	if err != nil {
		return false
	}

	// ---------- sent to the cloud from the card ----------
	os.WriteFile(filepath.Join(c.dir, "材料.md"), []byte("# 季度材料\n收入、成本\n"), 0o644)
	exists := func(name string) bool { _, err := os.Stat(filepath.Join(c.dir, name)); return err == nil }
	err = click(offer, "cloud:"+c.wsID)
	if err == nil && waitFor(func() bool { return strings.Contains(pageText(c.page()), "云端任务运行中，本机只读") }, time.Minute) != nil {
		err = fmt.Errorf("the settings page never showed the workspace held by a cloud task: %s", lineWith(pageText(c.page()), "本机"))
	}
	// The workspace is the task's before Cloister has taken the answer; the card says so after.
	if err == nil && waitFor(func() bool { return c.view.cardHas(offer, "已派到云端") }, time.Minute) != nil {
		err = fmt.Errorf("the card does not say it went to the cloud: %v", c.view.texts())
	}
	if err == nil {
		approvals := func() int { c.answers.mu.Lock(); defer c.answers.mu.Unlock(); return len(c.answers.approvals) }
		before := approvals()
		p, perr := c.a.OpenWorkspace(ctx, c.wsID)
		if err = perr; err == nil {
			if _, err = p.PromptText(ctx, "org-chain-cloud", "把结论记下来。"+orgWrite+"cloud.txt"); err == nil {
				wctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				err = p.Wait(wctx, "org-chain-cloud")
				cancel()
			}
		}
		switch {
		case err != nil:
		case exists("cloud.txt") || exists("cloud.txt.shell"):
			err = errors.New("the model wrote into the workspace while a cloud task held it")
		case approvals() != before:
			err = fmt.Errorf("the user was asked %d time(s) to let it write", approvals()-before)
		}
	}
	r.step(cloudSteps[1], err, lineWith(pageText(c.page()), "云端任务"))
	if err != nil {
		return false
	}

	// ---------- what the task asks reaches WeCom through the bot ----------
	var asked string
	err = waitFor(func() bool { asked = wecomWith("云端任务要你批准"); return asked != "" }, 3*time.Minute)
	if err == nil && !strings.Contains(asked, "任务卡片") {
		err = fmt.Errorf("the approval in WeCom does not say where to act on it: %q", asked)
	}
	if err != nil {
		err = fmt.Errorf("%v; WeCom: %v", err, wecomTexts(c.wecom, c.member))
	}
	r.step(cloudSteps[2], err, asked)
	if err != nil {
		return false
	}

	// ---------- pause: the snapshot comes back here ----------
	task := ""
	err = waitFor(func() bool {
		task = c.view.cardTitled("云端任务：" + cloudTaskTitle)
		return task != "" && strings.Contains(c.view.actions(task), "pause")
	}, time.Minute)
	modelSeen := 0
	if err == nil {
		modelSeen = modelRequests(c.modelURL, 0).count
		err = click(task, "pause")
	}
	if err == nil && waitFor(func() bool { return strings.Contains(pageText(c.page()), "本机可写") }, 2*time.Minute) != nil {
		err = fmt.Errorf("the workspace never came back after the pause: %s", lineWith(pageText(c.page()), "本机"))
	}
	notes := ""
	if err == nil {
		err = waitFor(func() bool {
			b, _ := os.ReadFile(filepath.Join(c.dir, "notes.md"))
			notes = string(b)
			return strings.Contains(notes, "- run at")
		}, time.Minute)
		if err != nil {
			err = fmt.Errorf("what the task wrote never reached the folder: notes.md %q", notes)
		}
	}
	if err == nil {
		// The bot is told (a worker report it keeps to itself).
		err = waitFor(func() bool { return strings.Contains(modelRequests(c.modelURL, modelSeen).joined, "已暂停") }, 2*time.Minute)
		if err != nil {
			err = errors.New("the cloud bot's model was never told the task paused")
		}
	}
	r.step(cloudSteps[3], err, fmt.Sprintf("notes.md：%q", strings.TrimSpace(notes)))
	if err != nil {
		return false
	}

	// ---------- resume: the same session, to the end ----------
	err = click(task, "resume")
	if err == nil && waitFor(func() bool { return exists("resume-check.txt") && strings.Contains(pageText(c.page()), "本机可写") }, 3*time.Minute) != nil {
		err = fmt.Errorf("the resumed task's result never reached the folder: %s", lineWith(pageText(c.page()), "本机"))
	}
	check := ""
	if err == nil {
		b, _ := os.ReadFile(filepath.Join(c.dir, "resume-check.txt"))
		check = strings.TrimSpace(string(b))
		n, _ := strconv.Atoi(strings.TrimPrefix(strings.SplitN(check, "\n", 2)[0], "prior_session_lines="))
		if n < 1 {
			err = fmt.Errorf("the resumed run found no session from before the pause: %q", check)
		}
	}
	result := ""
	if err == nil {
		err = waitFor(func() bool { result = wecomWith("做完了"); return result != "" }, 3*time.Minute)
		if err != nil {
			err = fmt.Errorf("no result in WeCom: %v", wecomTexts(c.wecom, c.member))
		}
	}
	r.step(cloudSteps[4], err, fmt.Sprintf("%s；企微：%s", strings.ReplaceAll(check, "\n", " "), result))
	return err == nil
}

// cardTitled is the id of the newest card with this title. The daily
// assistant's conversation shows the organisation's cards with "组织 · " in
// front.
func (v *inboxView) cardTitled(title string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	found := ""
	for _, id := range v.order {
		if c, ok := v.items[id]["card"].(map[string]any); ok && (c["title"] == title || c["title"] == "组织 · "+title) {
			found = id
		}
	}
	return found
}

type modelSeen struct {
	count  int
	joined string
}

// modelRequests reads what the cloud bot's fake model was sent, from the
// from-th request on (Cloister's e2e/real-engine.ts, /_fake/requests).
func modelRequests(modelURL string, from int) modelSeen {
	var got struct {
		Count  int      `json:"count"`
		Bodies []string `json:"bodies"`
	}
	adminCall(http.MethodGet, modelURL+"/_fake/requests?from="+url.QueryEscape(strconv.Itoa(from)), "", nil, &got)
	return modelSeen{count: got.Count, joined: strings.Join(got.Bodies, "\n")}
}
