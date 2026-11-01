package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Plugin pages (PRD 3.4, SPEC 3.2): the plugin list with install and
// uninstall, one page per plugin with its declarative settings pages, and the
// conversation view of each plugin that holds inbox.
const (
	PagePlugins = "plugins"
	pagePlugin  = "plugin:"
	pageInbox   = "inbox:"
)

// sidecarCalls is what the shell asks of running sidecars: App.Sidecars, or
// a fake in tests.
type sidecarCalls interface {
	UIAction(ctx context.Context, plugin, contributionID, action string, payload json.RawMessage) error
	InboxInput(ctx context.Context, plugin, id, text string, ts time.Time) error
}

// callTimeout bounds one call into a sidecar from the UI.
const callTimeout = 15 * time.Second

func (s *Shell) calls() sidecarCalls {
	if s.sidecarCalls != nil {
		return s.sidecarCalls
	}
	return s.App.Sidecars
}

// pluginsState is the plugin part of the shell's state.
type pluginsState struct {
	plugins  []host.Installed
	sidecars map[string]sidecar.Status
	plan     *host.Plan // checked package waiting for the user's yes
	planBusy bool       // checking or installing
	planErr  string
	confirm  string          // plugin whose uninstall waits for a second click
	busy     map[string]bool // plugin → uninstall or restart running
	forms    map[string]*settingsForm
	chats    map[string]*chat
	cards    map[string]*contribCard // card contributions, by plugin#id
}

// contribCard is a card contribution (ui.register type card) with its
// click state.
type contribCard struct {
	raw  string
	item *inboxItem
}

// chat is one plugin's conversation view: what the plugin posted and what
// the user typed, in arrival order.
type chat struct {
	input   string
	entries []*chatEntry
	byID    map[string]*chatEntry // plugin items by id
}

type chatEntry struct {
	item *inboxItem // from the plugin
	out  *outgoing  // typed by the user
}

// inboxItem is one inbox.post item (SPEC 3.2).
type inboxItem struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"` // message | card
	Text    string `json:"text"`
	TS      string `json:"ts"`
	ReplyTo string `json:"replyTo"`
	Card    *card  `json:"card"`
	busy    string // action id being sent
	err     string
}

type card struct {
	Title   string       `json:"title"`
	Body    string       `json:"body"`
	Actions []cardAction `json:"actions"`
}

type cardAction struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Style string `json:"style"` // primary | danger
}

// outgoing is one input of the user's: sent until the plugin acknowledges
// it, resent under the same id when its sidecar comes back.
type outgoing struct {
	id    string
	text  string
	ts    time.Time
	state outState
}

type outState int

const (
	outSending outState = iota
	outDelivered
	outFailed // shown as 未送达
)

func (s *Shell) chat(plugin string) *chat {
	c := s.chats[plugin]
	if c == nil {
		c = &chat{byID: map[string]*chatEntry{}}
		s.chats[plugin] = c
	}
	return c
}

// Inbox is the sidecars' inbox.post: it shows item in the plugin's
// conversation view; a repeated id replaces the earlier item in place.
func (s *Shell) Inbox(plugin string, raw json.RawMessage) error {
	var it inboxItem
	if err := json.Unmarshal(raw, &it); err != nil {
		return err
	}
	switch it.Kind {
	case "message":
	case "card":
		if it.Card == nil {
			return errors.New("inbox card item needs card")
		}
	default:
		return fmt.Errorf("unknown inbox item kind %q", it.Kind)
	}
	s.post(func() {
		c := s.chat(plugin)
		if e := c.byID[it.ID]; e != nil {
			e.item = &it
			return
		}
		e := &chatEntry{item: &it}
		c.byID[it.ID] = e
		c.entries = append(c.entries, e)
	})
	return nil
}

// OnSidecarState is the sidecar manager's state hook. A sidecar coming back
// gets the inputs it never acknowledged again.
func (s *Shell) OnSidecarState(name string, st sidecar.Status) {
	s.post(func() {
		s.sidecars[name] = st
		if st.State != sidecar.Running {
			return
		}
		if c := s.chats[name]; c != nil {
			for _, e := range c.entries {
				if e.out != nil && e.out.state == outFailed {
					s.deliver(name, e.out)
				}
			}
		}
	})
}

func newID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// sendInbox sends what the user typed in a plugin's conversation view.
func (s *Shell) sendInbox(plugin string, c *chat, text string) {
	o := &outgoing{id: newID(), text: text, ts: time.Now()}
	c.entries = append(c.entries, &chatEntry{out: o})
	s.deliver(plugin, o)
}

// deliver (under mu) sends o and records the outcome. The plugin treats a
// repeated id as handled, so sending again is always safe.
func (s *Shell) deliver(plugin string, o *outgoing) {
	o.state = outSending
	calls := s.calls()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		err := calls.InboxInput(ctx, plugin, o.id, o.text, o.ts)
		cancel()
		s.post(func() {
			if err != nil {
				o.state = outFailed
				return
			}
			o.state = outDelivered
		})
	}()
}

// cardAction forwards a click on an inbox card's button.
func (s *Shell) cardAction(plugin string, it *inboxItem, action string) {
	it.busy, it.err = action, ""
	calls := s.calls()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		err := calls.UIAction(ctx, plugin, it.ID, action, json.RawMessage(`{}`))
		cancel()
		s.post(func() {
			it.busy = ""
			if err != nil {
				it.err = describe(err)
			}
		})
	}()
}

// reloadPlugins reads the install records and sidecar states.
func (s *Shell) reloadPlugins() {
	recs, err := s.App.Plugins.List()
	sort.Slice(recs, func(i, j int) bool { return recs[i].Name < recs[j].Name })
	st := map[string]sidecar.Status{}
	for _, r := range recs {
		if v, ok := s.App.Sidecars.Status(r.Name); ok {
			st[r.Name] = v
		}
	}
	s.post(func() {
		if err != nil {
			s.notice = "无法读取插件列表：" + describe(err)
			return
		}
		s.plugins = recs
		for k, v := range st {
			s.sidecars[k] = v
		}
	})
}

// pickPlugin asks for a package file and checks it.
func (s *Shell) pickPlugin() {
	if s.PickFile == nil {
		return
	}
	go func() {
		path, err := s.PickFile()
		if err != nil || path == "" {
			return
		}
		s.post(func() { s.planBusy, s.planErr = true, "" })
		plan, err := s.App.PreparePlugin(context.Background(), path)
		s.post(func() {
			s.planBusy = false
			if s.plan != nil {
				s.App.DiscardPlugin(s.plan)
			}
			s.plan = plan
			if err != nil {
				s.planErr = describeInstall(err)
			}
		})
	}()
}

func (s *Shell) cancelPlan() {
	if s.plan != nil {
		s.App.DiscardPlugin(s.plan)
	}
	s.plan, s.planErr = nil, ""
}

// installPlan installs the checked package (Install discards the plan
// whatever the outcome).
func (s *Shell) installPlan() {
	plan := s.plan
	if plan == nil || !plan.OK() {
		return
	}
	s.planBusy, s.planErr = true, ""
	// Install updates the plan's targets: give it a copy, the card keeps
	// showing the original under mu.
	work := *plan
	work.Profiles = append([]string(nil), plan.Profiles...)
	go func() {
		rec, err := s.App.InstallPlugin(context.Background(), &work)
		s.reloadPlugins()
		s.post(func() {
			s.planBusy = false
			if s.plan == plan {
				s.plan = nil
			}
			if err != nil {
				s.planErr = describeInstall(err)
				return
			}
			s.page = pagePlugin + rec.Name
		})
	}()
}

func (s *Shell) uninstall(name string) {
	s.confirm = ""
	s.busy[name] = true
	go func() {
		err := s.App.UninstallPlugin(context.Background(), name)
		s.reloadPlugins()
		s.post(func() {
			delete(s.busy, name)
			if err != nil {
				s.notice = "卸载失败：" + describeInstall(err)
				return
			}
			delete(s.sidecars, name)
			delete(s.chats, name)
			for k := range s.forms {
				if strings.HasPrefix(k, name+"#") {
					delete(s.forms, k)
				}
			}
			for k := range s.cards {
				if strings.HasPrefix(k, name+"#") {
					delete(s.cards, k)
				}
			}
			if s.page == pagePlugin+name || s.page == pageInbox+name {
				s.page = PagePlugins
			}
		})
	}()
}

func (s *Shell) restart(name string) {
	s.busy[name] = true
	go func() {
		err := s.App.RestartPlugin(context.Background(), name)
		s.post(func() {
			delete(s.busy, name)
			if err != nil {
				s.notice = "无法启动 " + displayName(name) + "：" + describe(err)
			}
		})
	}()
}

// describeInstall spells out an install step failure with dsh's diagnostics.
func describeInstall(err error) string {
	var se *host.StepError
	if !errors.As(err, &se) {
		return describe(err)
	}
	step := map[string]string{
		"signature": "签名校验", "inspect": "读取插件包", "check": "兼容性检查", "store": "保存插件包",
		"add": "装入 profile", "verify": "校验 profile", "dump-config": "校验 profile 配置",
	}[se.Step]
	if step == "" {
		step = se.Step
	}
	msg := step + "失败"
	if se.Profile != "" {
		msg += "（" + se.Profile + "）"
	}
	msg += "：" + fmt.Sprint(se.Err)
	if se.RolledBack {
		msg += "。已回滚，没有留下改动"
	}
	if len(se.Diagnostics) > 0 {
		msg += "\n" + strings.Join(se.Diagnostics, "\n")
	}
	return msg
}

// displayName is a package name without its scope.
func displayName(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

// capNames says in words what each capability lets a plugin do.
var capNames = map[string]string{
	"settings_page":       "添加设置页",
	"sidebar_item":        "在侧栏添加入口",
	"inbox":               "在对话视图里给你发消息",
	"background":          "在后台运行，并按需启动自己的运行时",
	"runtime_launch_hook": "在运行时启动前设置环境变量",
	"workspace_hook":      "知道工作区的创建、打开与移除",
	"provider_endpoint":   "提供模型服务地址",
	"deep_link":           "处理 alcove:// 链接",
	"policy_write":        "修改策略设置（例如关闭本地执行）",
}

func capName(c string) string {
	if n := capNames[c]; n != "" {
		return n
	}
	return c
}

func hasCap(r host.Installed, c string) bool {
	for _, x := range r.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

func (s *Shell) installed(name string) (host.Installed, bool) {
	for _, r := range s.plugins {
		if r.Name == name {
			return r, true
		}
	}
	return host.Installed{}, false
}

// contributions lists a plugin's registered contributions of one type, by id.
func (s *Shell) contributions(plugin, typ string) []sidecar.Contribution {
	var out []sidecar.Contribution
	for k, c := range s.contribs {
		if strings.HasPrefix(k, plugin+"#") && c.Type == typ {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// navItem is one plugin entry in the sidebar.
type navItem struct{ page, title string }

// pluginNav lists the sidebar entries plugins add: their sidebar_item
// contributions, and a conversation view for each plugin holding inbox that
// has no sidebar_item leading there.
func (s *Shell) pluginNav() []navItem {
	var out []navItem
	seen := map[string]bool{}
	for _, r := range s.plugins {
		toInbox := false
		for _, c := range s.contributions(r.Name, "sidebar_item") {
			var it struct {
				Title  string `json:"title"`
				ViewID string `json:"viewId"`
			}
			json.Unmarshal(c.Raw, &it)
			if it.Title == "" {
				it.Title = displayName(r.Name)
			}
			page := pagePlugin + r.Name
			if it.ViewID == "inbox" && hasCap(r, "inbox") {
				page, toInbox = pageInbox+r.Name, true
			}
			if !seen[page] {
				seen[page] = true
				out = append(out, navItem{page, it.Title})
			}
		}
		if hasCap(r, "inbox") && !toInbox {
			out = append(out, navItem{pageInbox + r.Name, displayName(r.Name)})
		}
	}
	return out
}

// contribCard returns the click state of a card contribution, rebuilt when
// the plugin changed it.
func (s *Shell) contribCard(plugin string, ct sidecar.Contribution) *inboxItem {
	key := plugin + "#" + ct.ID
	cc := s.cards[key]
	if cc == nil || cc.raw != string(ct.Raw) && cc.item.busy == "" {
		var cd card
		json.Unmarshal(ct.Raw, &cd)
		cc = &contribCard{raw: string(ct.Raw), item: &inboxItem{ID: ct.ID, Kind: "card", Card: &cd}}
		s.cards[key] = cc
	}
	return cc.item
}
