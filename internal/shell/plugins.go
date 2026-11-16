package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/host"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Plugin pages (PRD 3.4, SPEC 3.2): the plugin list with install and
// uninstall, one page per plugin with its declarative settings pages, a
// settings page of its own when a sidebar item opens it ("view:<plugin>#<id>"),
// and the conversation view of each plugin that holds inbox.
const (
	PagePlugins = "plugins"
	pagePlugin  = "plugin:"
	pageInbox   = "inbox:"
	pageView    = "view:"
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
	// planFrom is the market that asked for plan (plugin_install), "" for
	// a file the user picked; planAnswer takes the user's answer to it.
	planFrom   string
	planAnswer chan bool
	background bool            // an installed plugin holds background
	confirm    string          // plugin whose uninstall waits for a second click
	busy       map[string]bool // plugin → uninstall or restart running
	forms      map[string]*settingsForm
	chats      map[string]*chat
	chatGen    int                     // the newest chat's gen
	cards      map[string]*contribCard // card contributions, by plugin#id
	avatars    map[string]*ui.Bitmap   // alcove.avatar of installed plugins, by name
	taskAsks   []*taskAsk              // workspace task requests waiting for the user
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
	// waiting is when the plugin last took an input of the user's with no
	// item from it since: the view shows it is working on a reply, for at
	// most replyWait (it may decide an input needs none).
	waiting time.Time
	saved   int // versions handed to the app to store
	gen     int // which chat this is: an uninstall drops saves of earlier ones
	// unread counts the items that came while the view was not shown.
	unread int
}

// replyWait is how long the conversation view shows a plugin working on
// a reply after taking an input.
const replyWait = 3 * time.Minute

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
		s.chatGen++
		c = &chat{byID: map[string]*chatEntry{}, gen: s.chatGen}
		s.chats[plugin] = c
		if s.App != nil {
			c.restore(s.App.LoadInbox(plugin))
		}
	}
	return c
}

// maxSavedEntries is how much of a conversation view is kept across
// restarts.
const maxSavedEntries = 300

type savedEntry struct {
	Item *inboxItem `json:"item,omitempty"`
	Out  *savedOut  `json:"out,omitempty"`
}

type savedOut struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	TS        time.Time `json:"ts"`
	Delivered bool      `json:"delivered,omitempty"`
}

// restore reads a saved view. Cards come back without their buttons: the
// plugin that still waits on one posts it again. Inputs never
// acknowledged are sent again once the plugin runs.
func (c *chat) restore(data []byte) {
	var saved []savedEntry
	if len(data) == 0 || json.Unmarshal(data, &saved) != nil {
		return
	}
	for _, e := range saved {
		switch {
		case e.Item != nil:
			if e.Item.Card != nil {
				e.Item.Card.Actions = nil
			}
			ce := &chatEntry{item: e.Item}
			c.byID[e.Item.ID] = ce
			c.entries = append(c.entries, ce)
		case e.Out != nil:
			o := &outgoing{id: e.Out.ID, text: e.Out.Text, ts: e.Out.TS, state: outFailed}
			if e.Out.Delivered {
				o.state = outDelivered
			}
			c.entries = append(c.entries, &chatEntry{out: o})
		}
	}
}

// save hands the view to the app to store (under mu).
func (s *Shell) saveChat(plugin string, c *chat) {
	if s.App == nil {
		return
	}
	entries := c.entries
	if len(entries) > maxSavedEntries {
		entries = entries[len(entries)-maxSavedEntries:]
	}
	saved := make([]savedEntry, 0, len(entries))
	for _, e := range entries {
		if e.item != nil {
			saved = append(saved, savedEntry{Item: e.item})
		} else {
			saved = append(saved, savedEntry{Out: &savedOut{ID: e.out.id, Text: e.out.text, TS: e.out.ts, Delivered: e.out.state == outDelivered}})
		}
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return
	}
	c.saved++
	seq := c.saved
	go s.App.SaveInbox(plugin, c.gen, seq, data)
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
		defer s.saveChat(plugin, c)
		if e := c.byID[it.ID]; e != nil {
			e.item = &it
			return
		}
		e := &chatEntry{item: &it}
		c.byID[it.ID] = e
		c.entries = append(c.entries, e)
		c.waiting = time.Time{}
		s.noteNew(plugin, c, &it)
	})
	return nil
}

// noteNew counts a new item as unread unless its view is shown, and
// notifies unless the user is looking at it.
func (s *Shell) noteNew(plugin string, c *chat, it *inboxItem) {
	shown := s.page == pageInbox+plugin
	if !shown {
		c.unread++
		s.badge()
	}
	if s.Notify == nil || (shown && (s.Active == nil || s.Active())) {
		return
	}
	subtitle, body := "", it.Text
	if it.Card != nil {
		subtitle, body = it.Card.Title, it.Card.Body
	}
	go s.Notify(plugin, s.pluginTitle(plugin), plainText(subtitle, 60), plainText(body, 200))
}

// markRead clears plugin's unread count; its view is shown.
func (s *Shell) markRead(plugin string) {
	if c := s.chats[plugin]; c != nil && c.unread > 0 {
		c.unread = 0
		s.badge()
	}
}

func (s *Shell) unread(plugin string) int {
	if c := s.chats[plugin]; c != nil {
		return c.unread
	}
	return 0
}

// badge shows the unread total on the app icon.
func (s *Shell) badge() {
	if s.SetBadge == nil {
		return
	}
	n := 0
	for _, c := range s.chats {
		n += c.unread
	}
	text := ""
	if n > 0 {
		text = fmt.Sprint(min(n, 99))
	}
	go s.SetBadge(text)
}

// OpenInbox shows plugin's conversation view (a notification was clicked).
func (s *Shell) OpenInbox(plugin string) {
	s.post(func() {
		if _, ok := s.installed(plugin); ok {
			s.page = pageInbox + plugin
		}
	})
}

// plainText is markdown as one line of text, at most n runes.
func plainText(md string, n int) string {
	r := strings.NewReplacer("**", "", "__", "", "`", "", "#", "")
	out := strings.Join(strings.Fields(r.Replace(md)), " ")
	if rs := []rune(out); len(rs) > n {
		out = string(rs[:n]) + "…"
	}
	return out
}

// OnSidecarState is the sidecar manager's state hook. A sidecar coming back
// gets the inputs it never acknowledged again.
func (s *Shell) OnSidecarState(name string, st sidecar.Status) {
	s.post(func() {
		s.sidecars[name] = st
		if st.State != sidecar.Running {
			if c := s.chats[name]; c != nil {
				c.waiting = time.Time{} // a stopped plugin replies to nothing
			}
			return
		}
		c := s.chats[name]
		if c == nil && s.App != nil && len(s.App.LoadInbox(name)) > 0 {
			c = s.chat(name) // inputs saved before a restart go out too
		}
		if c != nil {
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
	s.saveChat(plugin, c)
	s.deliver(plugin, o)
}

// deliver (under mu) sends o and records the outcome. The plugin treats a
// repeated id as handled, so sending again is always safe.
func (s *Shell) deliver(plugin string, o *outgoing) {
	o.state = outSending
	ch := s.chat(plugin)
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
			s.saveChat(plugin, ch)
			ch.waiting = time.Now()
			// Redraw when the wait runs out, to drop the indicator.
			time.AfterFunc(replyWait, func() { s.post(func() {}) })
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
	avatars := map[string]*ui.Bitmap{}
	for _, r := range recs {
		if p := s.App.Plugins.AvatarPath(r); p != "" && !r.Incomplete {
			if data, err := os.ReadFile(p); err == nil {
				if b, err := ui.DecodeBitmap(data); err == nil {
					avatars[r.Name] = b
				}
			}
		}
	}
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
		s.plugins, s.avatars = recs, avatars
		for k, v := range st {
			s.sidecars[k] = v
		}
		bg := false
		for _, r := range recs {
			bg = bg || !r.Incomplete && hasCap(r, "background")
		}
		if bg != s.background {
			s.background = bg
			if s.OnBackground != nil {
				s.OnBackground(bg)
			}
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
			s.dropPlan()
			s.plan = plan
			if err != nil {
				s.planErr = describeInstall(err)
			}
		})
	}()
}

func (s *Shell) cancelPlan() {
	s.dropPlan()
	s.planErr = ""
}

// dropPlan clears the install card: a market's request is declined (the
// app discards its plan), a picked file's plan is discarded.
func (s *Shell) dropPlan() {
	switch {
	case s.planAnswer != nil:
		s.planAnswer <- false
	case s.plan != nil:
		s.App.DiscardPlugin(s.plan)
	}
	s.plan, s.planFrom, s.planAnswer = nil, "", nil
}

// ConfirmInstall shows a package a plugin market asks to install or update
// in the install card and waits for the user's answer
// (app.Config.ConfirmInstall). A newer request or a picked file replaces
// it, declining it.
func (s *Shell) ConfirmInstall(ctx context.Context, plugin string, plan *host.Plan) bool {
	answer := make(chan bool, 1)
	s.post(func() {
		s.dropPlan()
		s.plan, s.planFrom, s.planAnswer, s.planErr = plan, plugin, answer, ""
	})
	select {
	case ok := <-answer:
		return ok
	case <-ctx.Done():
		s.post(func() {
			if s.planAnswer == answer {
				s.plan, s.planFrom, s.planAnswer = nil, "", nil
			}
		})
		return false
	}
}

// PluginsChanged reloads the plugin list after a market or a preinstall
// changed it (app.Config.PluginsChanged).
func (s *Shell) PluginsChanged() { s.reloadPlugins() }

// installPlan installs the checked package (Install discards the plan
// whatever the outcome).
func (s *Shell) installPlan() {
	plan := s.plan
	if plan == nil || !plan.OK() {
		return
	}
	if s.planAnswer != nil {
		// The market's request: the app installs it and the market reports.
		s.planAnswer <- true
		s.plan, s.planFrom, s.planAnswer, s.planErr = nil, "", nil, ""
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
			go s.App.DropInbox(name, s.chatGen)
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
			if s.page == pagePlugin+name || s.page == pageInbox+name || strings.HasPrefix(s.page, pageView+name+"#") {
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

// pluginTitle is the name a plugin goes by in the window: its alcove.title,
// or its package name without the scope.
func (s *Shell) pluginTitle(name string) string {
	if r, ok := s.installed(name); ok && r.Title != "" {
		return r.Title
	}
	return displayName(name)
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
	"workspace_session":   "在自己管理的工作区里运行任务",
	"workspace_task":      "请你选一个工作区来运行它的任务（每次都由你确认）",
	"deep_link":           "处理 alcove:// 链接",
	"policy_write":        "修改策略设置（例如关闭本地执行）",
	"plugin_install":      "从插件市场安装和更新插件（每次都由你确认）",
	"browser":             "在 Alcove 的浏览器里打开、阅读和操作网页（你能看到并随时接手）",
	"personal_history":    "读取你个人工作区里的对话（只读你自己发起的，不含组织工作区），在本机用来了解你的习惯",
	"personal_context":    "改写个人工作区共用的「关于我」和全局技能（只有个人工作区读取）",
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
	// In the order the plugin registered them (its ready lists them so).
	seq := func(c sidecar.Contribution) int { return s.contribSeq[plugin+"#"+c.ID] }
	sort.Slice(out, func(i, j int) bool {
		if a, b := seq(out[i]), seq(out[j]); a != b {
			return a < b
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// navItem is one plugin entry in the sidebar.
type navItem struct {
	page, title string
	icon        *ui.SVG
	avatar      *ui.Bitmap // an assistant's picture, instead of icon
}

// navIcons are the sidebar_item icon names the sidebar knows; others get
// the conversation icon.
var navIcons = map[string]*ui.SVG{
	"puzzle": icPuzzle, "chat": icChat, "inbox": icInbox, "cloud": icCloud,
	"folder": icFolder, "file": icFile, "terminal": icTerminal, "gear": icGear,
}

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
				Icon   string `json:"icon"`
				ViewID string `json:"viewId"`
			}
			json.Unmarshal(c.Raw, &it)
			if it.Title == "" {
				it.Title = displayName(r.Name)
			}
			page := pagePlugin + r.Name
			switch {
			case it.ViewID == "inbox" && hasCap(r, "inbox"):
				page, toInbox = pageInbox+r.Name, true
			case it.ViewID != "" && s.settingsPage(r.Name, it.ViewID) != nil:
				page = pageView + r.Name + "#" + it.ViewID
			}
			if !seen[page] {
				seen[page] = true
				ic := navIcons[it.Icon]
				if ic == nil {
					ic = icChat
				}
				out = append(out, navItem{page: page, title: it.Title, icon: ic})
			}
		}
		if hasCap(r, "inbox") && !toInbox && s.avatars[r.Name] == nil {
			out = append(out, navItem{page: pageInbox + r.Name, title: displayName(r.Name), icon: icChat})
		}
	}
	return out
}

// assistants lists the plugins holding inbox that have an avatar: the
// sidebar shows them apart, by picture and title, each opening its
// conversation view.
func (s *Shell) assistants() []navItem {
	var out []navItem
	for _, r := range s.plugins {
		if b := s.avatars[r.Name]; b != nil && !r.Incomplete && hasCap(r, "inbox") {
			out = append(out, navItem{page: pageInbox + r.Name, title: s.pluginTitle(r.Name), avatar: b})
		}
	}
	return out
}

// avatar is a plugin's picture cropped to a circle size across.
func avatar(c *ui.Context, b *ui.Bitmap, size float32) {
	ui.Box(c).Size(size, size).Radius(size / 2).Clip().Shrink(0).Children(func() {
		ui.Image(c, b).Fit(ui.Cover).Size(size, size)
	})
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
