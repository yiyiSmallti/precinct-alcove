package shell

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/catalog"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/credentials"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// The 模型 settings tab: one box per connected service with the models it
// switches between, the services to connect, and when conversations are
// compacted. Which model and effort a task runs at is picked over the
// composer (runBar).

// providerGroups sorts the services to connect, by credentials.Provider ID.
var providerGroups = []struct {
	title string
	ids   []string
}{
	{"国内", []string{"deepseek-official", "moonshotai-cn", "zai-coding-cn", "qwen-token-plan-cn", "minimax-cn", "xiaomi"}},
	{"国际", []string{"openai", "anthropic", "google", "xai", "mistral", "groq"}},
	{"聚合与自定义", []string{"openrouter", "opencode-go", credentials.Custom}},
}

// effortNames are the composer's words for catalog.Efforts.
var effortNames = map[string]string{
	"off": "关闭", "minimal": "最少", "low": "低", "medium": "中", "high": "高", "xhigh": "超高", "max": "极限",
}

// contextCaps are the sizes offered besides a model's own windows, in
// tokens.
var contextCaps = []int{32000, 64000, 128000, 200000, 256000, 400000, 500000}

// thresholds are the compaction points offered, in percent.
var thresholds = []int{50, 60, 70, 80, 90}

func (s *Shell) modelSettings(c *ui.Context, pal *palette) {
	t := c.Theme()
	if len(s.creds) == 0 {
		ui.Text(c, "还没有连接模型服务。在下面选一个服务并填入 API key 后才能执行任务；key 只存在系统凭据库里。").FontSize(13).TextColor(t.TextMuted).Padding(0, 2)
	}
	for i, cr := range s.creds {
		title := ""
		if i == 0 {
			title = "已连接的服务"
		}
		ui.Column(c).Key(cr.ID).Children(func() {
			settingsGroup(c, pal, title, func() { s.serviceBox(c, pal, cr) })
		})
	}
	s.addService(c, pal)
	if len(s.creds) > 0 {
		s.toolModelSettings(c, pal)
	}
	s.compactionSettings(c, pal)
}

// toolChoice is one model offered for background work.
type toolChoice struct {
	label string
	tm    app.ToolModel
}

// toolChoices are the models background work can run on next to the
// default credential's: the enabled models of every credential that does
// not take the default one's place (app.ToolConflict), the first being
// the runtime's own model. A custom service offers the one it is set to.
func (s *Shell) toolChoices(def *credentials.Credential) []toolChoice {
	out := []toolChoice{{label: "和对话相同（默认）"}}
	for _, cr := range s.creds {
		if def != nil && app.ToolConflict(*def, cr) {
			continue
		}
		models := credentials.EnabledModels(cr)
		if cr.Provider == credentials.Custom {
			models = []string{credentials.ModelOf(cr)}
		}
		for _, m := range models {
			out = append(out, toolChoice{label: cr.Label + " · " + m, tm: app.ToolModel{Credential: cr.ID, Model: m}})
		}
	}
	// Credentials may share a name: the picker matches a choice by its
	// label, so a label taken already gets the connection's address, then a
	// number, until it is the only one.
	seen := map[string]bool{}
	for i := range out {
		label := out[i].label
		if seen[label] {
			for _, cr := range s.creds {
				if cr.ID == out[i].tm.Credential && cr.BaseURL != "" {
					label += "（" + hostOf(cr.BaseURL) + "）"
				}
			}
			for n := 2; seen[label]; n++ {
				label = fmt.Sprintf("%s（%d）", out[i].label, n)
			}
		}
		seen[label] = true
		out[i].label = label
	}
	return out
}

// toolModelSettings is the model plugins' background work runs on, such
// as the daily bot's tidying and learning.
func (s *Shell) toolModelSettings(c *ui.Context, pal *palette) {
	t := c.Theme()
	def := s.defaultCred()
	cur := s.settings.ToolModel
	settingsGroup(c, pal, "工具模型", func() {
		prefRow(c, pal, "后台活用的模型", "日常助手闲时整理记忆、从对话里学习时用它，选一个快又便宜的小模型更省。和对话用的服务相同但 key 不同的连接不能同时用，不在列表里。修改从助手下次启动开始生效。", func() {
			choices := s.toolChoices(def)
			var options []string
			pick := choices[0].label
			for _, ch := range choices {
				options = append(options, ch.label)
				if ch.tm == cur {
					pick = ch.label
				}
			}
			if ui.Select(c, &pick, options).Label("工具模型").Width(240).Changed() {
				for _, ch := range choices {
					if ch.label == pick {
						st := s.settings
						st.ToolModel = ch.tm
						s.saveSettings(st)
						break
					}
				}
			}
		})
		// A choice the default credential no longer allows (it changed to
		// another credential of the same service) is kept, unused.
		if cur.Credential != "" && def != nil {
			for _, cr := range s.creds {
				if cr.ID == cur.Credential && app.ToolConflict(*def, cr) {
					ui.Text(c, "选的工具模型和现在对话用的服务冲突，后台活暂时改用对话的模型。").FontSize(12).TextColor(t.Danger).Padding(8, 16)
				}
			}
		}
	})
}

// serviceBox is one connected service: its name, the picker of its models
// when open, and the models it switches between, each with its context cap.
func (s *Shell) serviceBox(c *ui.Context, pal *palette, cr credentials.Credential) {
	t := c.Theme()
	def := s.defaultCred()
	p, _ := credentials.ProviderOf(cr.Provider)
	pick := s.picks[cr.ID]
	ui.Row(c).Padding(12, 16).Gap(10).AlignItems(ui.Center).Background(pal.code).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
			ui.Text(c, cr.Label).FontSize(13).FontWeight(600).SingleLine()
			sub := p.Name
			if cr.BaseURL != "" {
				sub += " · " + hostOf(cr.BaseURL)
			}
			ui.Text(c, sub).FontSize(11.5).TextColor(t.TextMuted).SingleLine()
		})
		if pick != nil {
			if ui.Button(c, "收起").Clicked() {
				delete(s.picks, cr.ID)
			}
		} else if ui.Button(c, "管理模型").Clicked() {
			s.openPick(cr)
		}
		if ui.Button(c, "删除").Clicked() {
			s.removeCredential(cr.ID)
		}
	})
	if pick != nil {
		ui.Column(c).Key("pick").Children(func() { s.modelPicker(c, pal, cr, pick) })
	}
	for _, model := range credentials.EnabledModels(cr) {
		current := def != nil && def.ID == cr.ID && credentials.ModelOf(*def) == model
		ui.Row(c).Key("m:"+model).Padding(9, 16).Gap(10).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
			ui.Row(c).Width(14).Shrink(0).Justify(ui.Center).Children(func() {
				if current {
					ui.Icon(c, icCheck).Size(14, 14).TextColor(t.Accent)
				}
			})
			m, listed := s.App.Catalog.Lookup(cr.Provider, model)
			ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
				ui.Text(c, model).FontSize(13).SingleLine().Ellipsis("…").Selectable()
				meta := "上下文 " + tokens(m.Context)
				if m.Max > m.Context {
					meta += "，最大 " + tokens(m.Max)
				}
				if !listed {
					meta += "（运行时未收录，按默认值）"
				}
				if len(m.Efforts) > 0 {
					meta += " · 可调思考强度"
				}
				ui.Text(c, meta).FontSize(11.5).TextColor(pal.subtle).SingleLine()
			})
			s.contextSelect(c, cr, m)
			if current {
				ui.Text(c, "使用中").FontSize(12).TextColor(t.Accent).MinWidth(64).TextAlign(ui.Center).Shrink(0)
			} else if ui.Button(c, "使用").MinWidth(64).Shrink(0).Clicked() {
				s.switchModel(cr.ID, model)
			}
		})
	}
}

// contextSizes are the sizes offered for m's context, smallest first, with
// the one chosen on cr: the runtime's window (the default), less, or more
// up to the widest window a catalog states for the model.
func contextSizes(cr credentials.Credential, m catalog.Model) (sizes []int, cur int) {
	sizes = []int{m.Context, m.Max}
	for _, n := range contextCaps {
		if n < m.Max {
			sizes = append(sizes, n)
		}
	}
	cur = cr.Context[m.ID]
	if cur <= 0 || cur > m.Max {
		cur = m.Context
	}
	sizes = append(sizes, cur)
	sort.Ints(sizes)
	return slices.Compact(sizes), cur
}

// sizeName names a context size of m in a list of them.
func sizeName(m catalog.Model, n int) string {
	switch {
	case n == m.Context:
		return tokens(n) + "（默认）"
	case n == m.Max:
		return tokens(n) + "（最大）"
	}
	return tokens(n)
}

// setSize chooses n tokens of context for m on cr.
func (s *Shell) setSize(cr credentials.Credential, m catalog.Model, n int) {
	if n == m.Context {
		n = 0
	}
	s.setContext(cr.ID, m.ID, n)
}

// contextSelect sets how much context a conversation of m gets before it
// is compacted (contextSizes).
func (s *Shell) contextSelect(c *ui.Context, cr credentials.Credential, m catalog.Model) {
	sizes, cur := contextSizes(cr, m)
	var options []string
	for _, n := range sizes {
		options = append(options, sizeName(m, n))
	}
	pick := sizeName(m, cur)
	if ui.Select(c, &pick, options).Label("上下文上限").Width(132).Shrink(0).Changed() {
		for _, n := range sizes {
			if sizeName(m, n) == pick {
				s.setSize(cr, m, n)
			}
		}
	}
}

// tokens writes a token count the way services state windows: 272K, 1M
// (also 2^20, as DeepSeek's 1,048,576), 1.05M.
func tokens(n int) string {
	switch {
	case n >= 1000000 && (n%1000000 == 0 || n%(1<<20) == 0):
		return fmt.Sprintf("%dM", max(n/1000000, n>>20))
	case n >= 1000000:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(n)/1000000), "0"), ".") + "M"
	case n >= 1000:
		return fmt.Sprintf("%dK", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

// addService lists the services to connect; one picked opens its form.
func (s *Shell) addService(c *ui.Context, pal *palette) {
	t := c.Theme()
	f := &s.form
	settingsGroup(c, pal, "添加服务", func() {
		ui.Column(c).Padding(12, 16, 14).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
			for _, g := range providerGroups {
				ui.Row(c).Key(g.title).Gap(10).AlignItems(ui.Start).Children(func() {
					ui.Text(c, g.title).FontSize(11.5).TextColor(pal.subtle).Width(72).Shrink(0).Padding(5, 0, 0)
					ui.Row(c).Gap(6).Wrap().Grow(1).MinWidth(0).Children(func() {
						for _, id := range g.ids {
							p, ok := credentials.ProviderOf(id)
							if !ok {
								continue
							}
							open := f.open && f.provider == p.Name
							if serviceChip(c, pal, p.Name, open) {
								if open {
									s.form = credForm{}
								} else {
									s.form = credForm{provider: p.Name, open: true, protocol: credentials.Protocols[0].Name}
								}
							}
						}
					})
				})
			}
		})
		if p, ok := providerNamed(f.provider); f.open && ok {
			ui.Column(c).Key("form:" + p.ID).Children(func() {
				ui.Text(c, "连接 "+p.Name).FontSize(13).FontWeight(600).Padding(12, 16, 0).TextColor(t.Text)
				s.credentialForm(c, pal, p)
			})
		}
	})
}

// serviceChip is one service to connect.
func serviceChip(c *ui.Context, pal *palette, name string, on bool) bool {
	t := c.Theme()
	chip := ui.Row(c).Key("svc:"+name).Height(28).Padding(0, 11).AlignItems(ui.Center).Radius(14).Border(1, pal.hairline).Cursor(ui.CursorPointer).Focusable().Label(name).Transition(fade)
	switch {
	case on:
		chip.Background(t.Accent.Alpha(0.12)).Border(1, t.Accent.Alpha(0.45))
	case chip.Hovered():
		chip.Background(pal.hover)
	}
	chip.Children(func() {
		ui.Text(c, name).FontSize(12.5).TextColor(t.Text).SingleLine()
	})
	return chip.Clicked()
}

// credentialForm connects a service p: a name, its key and, for a custom
// endpoint, where it is and what it speaks. The models come after, from
// the service's own list (openPick).
func (s *Shell) credentialForm(c *ui.Context, pal *palette, p credentials.Provider) {
	t := c.Theme()
	f := &s.form
	custom := p.ID == credentials.Custom
	prefRow(c, pal, "名称", "在列表里认出它。", func() {
		name := p.Name + " 个人"
		if custom {
			name = "公司网关"
		}
		ui.TextInput(c, &f.label).Placeholder("例如：" + name).Grow(1)
	})
	if custom {
		prefRow(c, pal, "接口地址", "服务商文档里的 Base URL。", func() {
			ui.TextInput(c, &f.baseURL).Placeholder("https://api.example.com/v1").Grow(1)
		})
		prefRow(c, pal, "协议", "接口兼容哪家的 API。", func() { ui.Select(c, &f.protocol, protocolNames()).Width(260) })
	}
	prefRow(c, pal, "API key", "只存在系统凭据库里。", func() {
		ui.TextInput(c, &f.key).Password().Grow(1)
	})
	if custom {
		prefRow(c, pal, "模型", "接口支持的模型名。", func() {
			ui.TextInput(c, &f.model).Placeholder("例如：qwen3-coder").Grow(1)
		})
	} else if p.BaseEnv != "" {
		prefRow(c, pal, "接口地址", "可选，留空用服务的默认地址。", func() {
			ui.TextInput(c, &f.baseURL).Placeholder("https://…").Grow(1)
		})
	}
	ui.Row(c).Padding(12, 16).Gap(8).AlignItems(ui.Center).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		if f.err != "" {
			ui.Text(c, f.err).FontSize(12.5).TextColor(t.Danger).Grow(1).MinWidth(0)
		} else {
			hint := "保存后会向服务获取可用模型，勾选要用的。"
			if custom || p.Listing == "" {
				hint = "保存后可以在「管理模型」里添加模型名。"
			}
			ui.Text(c, hint).FontSize(12).TextColor(pal.subtle).Grow(1).MinWidth(0)
		}
		if ui.Button(c, "取消").Clicked() {
			s.form = credForm{}
		}
		ok := strings.TrimSpace(f.label) != "" && strings.TrimSpace(f.key) != ""
		if custom {
			ok = ok && strings.TrimSpace(f.baseURL) != "" && strings.TrimSpace(f.model) != ""
		}
		if primaryButton(c, "保存", ok) {
			s.addCredential()
		}
	})
}

// modelPicker lists a credential's models to tick: the service's list
// when it gives one, and any model name typed in.
func (s *Shell) modelPicker(c *ui.Context, pal *palette, cr credentials.Credential, m *modelPick) {
	t := c.Theme()
	ui.Column(c).Padding(12, 16, 14).Gap(10).BorderWidth(0, 0, 1, 0).BorderColor(pal.hairline).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			status, col := fmt.Sprintf("这个服务不提供模型列表，可以输入模型名添加。已选 %d 个", len(m.picked)), t.TextMuted
			switch {
			case m.fetching:
				status = "正在向服务获取模型列表…"
			case m.err != "":
				status, col = "获取失败："+m.err+"。可以输入模型名添加。", t.Danger
			case m.remote != nil:
				status = fmt.Sprintf("服务提供 %d 个模型，已选 %d 个。勾选的模型会出现在对话框的模型菜单里。", len(m.remote), len(m.picked))
			}
			ui.Text(c, status).FontSize(12).TextColor(col).Grow(1).MinWidth(0).Selectable()
			if m.fetching {
				ui.Spinner(c)
			}
		})
		in := ui.TextInput(c, &m.filter).Placeholder("筛选；或输入模型名后按回车添加")
		typed := strings.TrimSpace(m.filter)
		if in.Submitted() && typed != "" {
			if !m.has(typed) {
				m.toggle(typed)
			}
			m.filter = ""
		}
		// Ticked ones first, in the order ticked, then the service's.
		all := append([]string(nil), m.picked...)
		for _, id := range m.remote {
			if !m.has(id) {
				all = append(all, id)
			}
		}
		var shown []string
		for _, id := range all {
			if typed == "" || strings.Contains(strings.ToLower(id), strings.ToLower(typed)) {
				shown = append(shown, id)
			}
		}
		const most = 120
		more := 0
		if len(shown) > most {
			more, shown = len(shown)-most, shown[:most]
		}
		ui.Row(c).Gap(6).Wrap().Children(func() {
			for _, id := range shown {
				if pickChip(c, pal, id, m.has(id)) {
					m.toggle(id)
				}
			}
		})
		if more > 0 {
			ui.Text(c, fmt.Sprintf("还有 %d 个，输入关键词筛选。", more)).FontSize(11.5).TextColor(pal.subtle)
		}
		ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
			if ui.Button(c, "取消").Clicked() {
				delete(s.picks, cr.ID)
			}
			if primaryButton(c, "保存", len(m.picked) > 0 && !m.saving) {
				s.savePick(cr, m)
			}
		})
	})
}

// pickChip is one model to tick on or off.
func pickChip(c *ui.Context, pal *palette, model string, on bool) bool {
	t := c.Theme()
	chip := ui.Row(c).Key("pick:"+model).Height(26).Padding(0, 10).Gap(5).AlignItems(ui.Center).Radius(13).Border(1, pal.hairline).Background(pal.content).Cursor(ui.CursorPointer).Focusable().Label(model).Transition(fade)
	switch {
	case on:
		chip.Background(t.Accent.Alpha(0.12)).Border(1, t.Accent.Alpha(0.45))
	case chip.Hovered():
		chip.Background(pal.hover)
	}
	chip.Children(func() {
		if on {
			ui.Icon(c, icCheck).Size(12, 12).TextColor(t.Accent)
		}
		ui.Text(c, model).FontSize(12).TextColor(t.Text)
	})
	return chip.Clicked()
}

// compactionSettings is when a conversation nearing its window is
// compacted, for every model; each model's cap is on its row above.
func (s *Shell) compactionSettings(c *ui.Context, pal *palette) {
	cp := s.settings.Compaction
	settingsGroup(c, pal, "上下文与压缩", func() {
		prefRow(c, pal, "自动压缩", "对话接近上下文上限时，把较早的内容总结成摘要，腾出空间继续。关掉后上下文满了这一轮会失败。", func() {
			on := !cp.Off
			if toggle(c, &on).Label("自动压缩").Changed() {
				st := s.settings
				st.Compaction.Off = !on
				s.saveSettings(st)
			}
		})
		prefRow(c, pal, "压缩时机", "上下文用到上限的这个比例时压缩。越早压缩越省 token，但较早的细节丢得越多。修改从下一次发送开始生效。", func() {
			cur := cp.Threshold
			if cur <= 0 || cur >= 100 {
				cur = app.DefaultThreshold
			}
			var options []string
			pick := ""
			for _, n := range thresholds {
				o := fmt.Sprintf("%d%%", n)
				if n == app.DefaultThreshold {
					o += "（默认）"
				}
				if n == cur {
					pick = o
				}
				options = append(options, o)
			}
			if ui.Select(c, &pick, options).Label("压缩时机").Width(140).Disabled(cp.Off).Changed() {
				for i, o := range options {
					if o == pick {
						st := s.settings
						st.Compaction.Threshold = thresholds[i]
						if thresholds[i] == app.DefaultThreshold {
							st.Compaction.Threshold = 0
						}
						s.saveSettings(st)
					}
				}
			}
		})
	})
}

// menuChip is a small menu button showing text.
func menuChip(c *ui.Context, pal *palette, text, label string) ui.Element {
	t := c.Theme()
	chip := ui.Row(c).Key("chip:"+label).Height(22).Padding(0, 4, 0, 6).Gap(2).AlignItems(ui.Center).Radius(6).Cursor(ui.CursorPointer).Focusable().Label(label).Shrink(0)
	if chip.Hovered() {
		chip.Background(pal.hover)
	}
	chip.Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(t.Text).SingleLine()
		ui.Icon(c, icChevron).Size(11, 11).TextColor(pal.subtle)
	})
	return chip
}

// runControls lead the composer's foot: the model the task runs with at
// which thinking effort, switched in one panel (runPanel), and how full its
// context is. A workspace not on the personal credentials shows only the
// context it took. The panels open over the composer.
func (s *Shell) runControls(c *ui.Context, pal *palette, v *wsView, tk *taskView) {
	used := 0
	if tk != nil {
		used = tk.session.Context
	}
	var def *credentials.Credential
	if v.ws.OwnerName() == workspace.Personal {
		def = s.defaultCred()
	}
	// The meter opens what the context holds.
	meter := func(limit int) {
		var u events.Usage
		if tk != nil {
			u = tk.session.Usage
		}
		m := contextMeter(c, pal, used, limit, s.settings.Compaction)
		if m.Clicked() {
			s.ctxMenu = !s.ctxMenu
		}
		ui.PopoverBase(c, m, &s.ctxMenu, func(panel ui.Element) {
			t := c.Theme()
			panel.AttachTo(m, ui.AnchorBottomLeft, ui.AnchorTopLeft).Margin(6, 0, 0, 0).Width(280).Padding(12, 14).Radius(12).
				Background(t.Background).Border(1, t.Border).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.2))
			contextPanel(c, pal, used, limit, u, s.settings.Compaction)
		})
	}
	if def == nil {
		if used > 0 {
			meter(0)
		}
		return
	}
	m, limit := s.runModel(*def)
	chip := s.runChip(c, pal, m)
	if chip.Clicked() {
		s.runMenu = !s.runMenu
	}
	ui.PopoverBase(c, chip, &s.runMenu, func(panel ui.Element) {
		t := c.Theme()
		panel.AttachTo(chip, ui.AnchorBottomLeft, ui.AnchorTopLeft).Margin(6, 0, 0, 0).Width(312).Padding(6).Radius(12).
			Background(t.Background).Border(1, t.Border).Shadow(0, 10, 30, 0, ui.RGBA(0, 0, 0, 0.2))
		s.runPanel(c, pal, *def, m, used, limit)
	})
	if used > 0 {
		meter(limit)
	}
}

// runModel is what the runtime knows of cr's current model and the context
// a task gets with it: the runtime's window, or the size chosen for it.
func (s *Shell) runModel(cr credentials.Credential) (catalog.Model, int) {
	m, _ := s.App.Catalog.Lookup(cr.Provider, credentials.ModelOf(cr))
	limit := m.Context
	if limit <= 0 {
		limit = catalog.DefaultContext
	}
	if n := cr.Context[m.ID]; n > 0 && n <= m.Max {
		limit = n
	}
	return m, limit
}

// effortStops are the positions of the effort slider: the service's
// default first, then the efforts m takes, least thinking first.
func effortStops(m catalog.Model) []string {
	return append([]string{""}, m.Efforts...)
}

// effortName is how the composer says effort; "" is the service's default.
func effortName(effort string) string {
	if effort == "" {
		return "默认"
	}
	return effortNames[effort]
}

// runEffort is the effort tasks run m at: the chosen one if m takes it,
// else "" (the service's default).
func (s *Shell) runEffort(m catalog.Model) string {
	if m.Supports(s.settings.ReasoningEffort) {
		return s.settings.ReasoningEffort
	}
	return ""
}

// runChip shows the model and, for one that thinks, a meter of its effort
// with the effort's name; a click opens the run panel.
func (s *Shell) runChip(c *ui.Context, pal *palette, m catalog.Model) ui.Element {
	t := c.Theme()
	chip := ui.Row(c).Key("run").Height(26).Padding(0, 7, 0, 10).Gap(6).AlignItems(ui.Center).Radius(13).
		Cursor(ui.CursorPointer).Focusable().Label("模型与思考强度").Shrink(1).MinWidth(0).Transition(fade)
	if chip.Hovered() || s.runMenu {
		chip.Background(pal.hover)
	}
	chip.Children(func() {
		ui.Text(c, m.ID).FontSize(12.5).FontWeight(500).SingleLine().Ellipsis("…").MinWidth(0)
		if len(m.Efforts) > 0 {
			stops := effortStops(m)
			cur := indexOf(stops, s.runEffort(m))
			ui.Box(c).Width(1).Height(12).Background(pal.hairline)
			// One bar per effort, lit up to the chosen one; none lit at
			// the service's default.
			n := len(stops) - 1
			ui.Box(c).Size(float32(n*4-1), 11).Shrink(0).Draw(func(p *ui.Painter, r ui.Rect) {
				for i := 1; i <= n; i++ {
					h := 4 + 7*float32(i)/float32(n)
					col := pal.hairline
					if i <= cur {
						col = t.Accent
					}
					p.Fill(ui.Rect{X: r.X + float32(i-1)*4, Y: r.Y + r.H - h, W: 3, H: h}, col, 1)
				}
			})
			ui.Text(c, effortName(stops[cur])).FontSize(12).TextColor(t.TextMuted).SingleLine().Shrink(0)
		}
		ui.Icon(c, icChevron).Size(12, 12).TextColor(pal.subtle).Shrink(0)
	})
	return chip
}

// runPanel switches the model among the enabled ones of every service,
// slides the thinking effort among those the model takes, and tells how
// much of its context the task took.
func (s *Shell) runPanel(c *ui.Context, pal *palette, def credentials.Credential, m catalog.Model, used, limit int) {
	t := c.Theme()
	heading := func(text string) {
		ui.Text(c, text).FontSize(11).FontWeight(600).TextColor(pal.subtle).Padding(6, 8, 2)
	}
	rule := func() { ui.Box(c).Height(1).Background(pal.hairline).Margin(6, 4) }

	heading("模型")
	ui.Column(c).Gap(1).MaxHeight(240).Children(func() {
		ui.Scroll(c).Children(func() {
			for _, cr := range s.creds {
				if len(s.creds) > 1 {
					ui.Text(c, cr.Label).Key("svc:"+cr.ID).FontSize(11).TextColor(pal.subtle).Padding(6, 8, 2).SingleLine()
				}
				for _, model := range credentials.EnabledModels(cr) {
					on := cr.ID == def.ID && model == m.ID
					as := cr
					as.Model = model
					_, size := s.runModel(as)
					if listRow(c, pal, "m:"+cr.ID+"/"+model, model, false, func() {
						ui.Box(c).Size(14, 14).Shrink(0).Children(func() {
							if on {
								ui.Icon(c, icCheck).Size(14, 14).TextColor(t.Accent)
							}
						})
						ui.Text(c, model).FontSize(13).SingleLine().Ellipsis("…").Grow(1).MinWidth(0)
						ui.Text(c, tokens(size)).FontSize(11).TextColor(pal.subtle).Shrink(0)
					}) && !on {
						s.switchModel(cr.ID, model)
					}
				}
			}
		})
	})
	rule()

	stops := effortStops(m)
	cur := indexOf(stops, s.runEffort(m))
	ui.Column(c).Padding(2, 8, 6).Gap(6).Children(func() {
		if len(m.Efforts) == 0 {
			ui.Text(c, "思考强度").FontSize(12.5).FontWeight(600)
			ui.Text(c, "这个模型不能调思考强度，按服务商默认运行。").FontSize(12).TextColor(pal.subtle)
			return
		}
		s.effortSlider(c, pal, stops, cur)
	})
	rule()

	ui.Column(c).Padding(2, 8, 4).Gap(7).Children(func() {
		cp := s.settings.Compaction
		ui.Row(c).AlignItems(ui.Center).Gap(2).Children(func() {
			ui.Text(c, "上下文").FontSize(12.5).FontWeight(600).Grow(1)
			ui.Text(c, tokens(used)+" /").FontSize(12).TextColor(t.TextMuted)
			// The size itself is a menu of the sizes the model takes.
			sizes, cur := contextSizes(def, m)
			menuChip(c, pal, tokens(limit), "上下文大小").PopoverMenuButton(func(mn *ui.Menu) {
				for _, n := range sizes {
					if mn.Item(sizeName(m, n)).Radio(n == cur).Chosen() && n != cur {
						s.setSize(def, m, n)
					}
				}
			})
		})
		contextBar(c, pal, used, limit, cp)
		note := fmt.Sprintf("用到 %d%% 时自动压缩，较早的内容会被总结。", threshold(cp))
		if cp.Off {
			note = "自动压缩已关闭，上下文用满时任务会失败。"
		}
		if used == 0 {
			note = "发出消息后显示这个任务占用了多少。" + note
		}
		ui.Text(c, note).FontSize(11.5).TextColor(pal.subtle)
	})
	rule()
	if navRow(c, pal, "run:settings", icGear, "模型、上下文与压缩设置…", false) {
		s.runMenu = false
		s.page, s.prefs.tab = PageSettings, tabModel
	}
}

// threshold is how full the context gets before it is compacted, in
// percent.
func threshold(cp app.Compaction) int {
	if cp.Threshold > 0 {
		return cp.Threshold
	}
	return app.DefaultThreshold
}

// contextColor warns as a task's context nears its compaction point, and
// more as it nears the end.
func contextColor(c *ui.Context, pal *palette, frac float64, cp app.Compaction) ui.Color {
	t := c.Theme()
	switch {
	case frac >= 0.95:
		return t.Danger
	case frac >= float64(threshold(cp))/100*0.9:
		return pal.waiting
	}
	return t.Accent
}

// contextMeter is a ring filled as far as a task's context is taken, with
// the share and the tokens; limit 0 (the model not known) shows the tokens
// alone. A click opens contextPanel.
func contextMeter(c *ui.Context, pal *palette, used, limit int, cp app.Compaction) ui.Element {
	t := c.Theme()
	tip := fmt.Sprintf("这个任务的上下文已用 %s tokens", tokens(used))
	frac := 0.0
	if limit > 0 {
		frac = min(1, float64(used)/float64(limit))
		tip = fmt.Sprintf("上下文已用 %s / %s tokens（%.0f%%）", tokens(used), tokens(limit), frac*100)
		if cp.Off {
			tip += "，自动压缩已关闭"
		} else {
			tip += fmt.Sprintf("，用到 %d%% 时自动压缩", threshold(cp))
		}
	}
	row := ui.Row(c).Key("ctx").Height(26).Padding(0, 8).Gap(6).AlignItems(ui.Center).Radius(13).Tooltip(tip).Label(tip).Shrink(0).
		Cursor(ui.CursorPointer).Focusable()
	if row.Hovered() {
		row.Background(pal.hover)
	}
	row.Children(func() {
		if limit == 0 {
			ui.Text(c, "上下文 "+tokens(used)).FontSize(12).TextColor(t.TextMuted)
			return
		}
		col := contextColor(c, pal, frac, cp)
		f := float32(frac)
		ui.Box(c).Size(14, 14).Radius(7).BorderWidth(2).Shrink(0).BorderConicGradient(ui.ConicGradient{Stops: []ui.GradientStop{
			{Color: col, At: 0}, {Color: col, At: f}, {Color: pal.hairline, At: f}, {Color: pal.hairline, At: 1},
		}})
		ui.Text(c, fmt.Sprintf("%.0f%%", frac*100)).FontSize(12).TextColor(t.TextMuted)
	})
	return row
}

// contextPanel says what a task's context holds: how much of the limit is
// taken and left, when it is compacted, and how the latest request split
// between the cache and new input. limit 0 is a model not known.
func contextPanel(c *ui.Context, pal *palette, used, limit int, u events.Usage, cp app.Compaction) {
	t := c.Theme()
	line := func(label, value string) {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, label).FontSize(12.5).TextColor(t.TextMuted).Grow(1)
			ui.Text(c, value).FontSize(12.5).Shrink(0)
		})
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "上下文").FontSize(13).FontWeight(600).Grow(1)
			if limit > 0 {
				frac := min(1, float64(used)/float64(limit))
				ui.Text(c, fmt.Sprintf("%.0f%%", frac*100)).FontSize(13).FontWeight(600).TextColor(contextColor(c, pal, frac, cp))
			}
		})
		if used == 0 {
			ui.Text(c, "发出消息后显示这个任务占用了多少。").FontSize(12).TextColor(pal.subtle)
			return
		}
		if limit > 0 {
			contextBar(c, pal, used, limit, cp)
		}
		line("已用", tokens(used)+" tokens")
		if limit > 0 {
			line("上限", tokens(limit)+" tokens")
			line("剩余", tokens(max(0, limit-used))+" tokens")
			if cp.Off {
				line("自动压缩", "已关闭")
			} else {
				at := limit * threshold(cp) / 100
				line("自动压缩", fmt.Sprintf("用到 %d%%（%s）时", threshold(cp), tokens(at)))
				if used < at {
					line("距压缩", tokens(at-used)+" tokens")
				}
			}
		}
		// Tasks saved before usage was kept have none until their next request.
		if u != (events.Usage{}) {
			ui.Box(c).Height(1).Background(pal.hairline).Margin(2, 0)
			ui.Text(c, "最近一次请求").FontSize(12).FontWeight(600).TextColor(t.TextMuted)
			in := u.Input + u.CacheRead + u.CacheWrite
			line("输入", tokens(in)+" tokens")
			if in > 0 && u.CacheRead > 0 {
				line("其中缓存命中", fmt.Sprintf("%s（%.0f%%）", tokens(u.CacheRead), float64(u.CacheRead)/float64(in)*100))
			}
			line("输出", tokens(u.Output)+" tokens")
		}
		note := "上下文按最近一次请求和回复计。"
		if cp.Off {
			note += "自动压缩关闭时，用满后这一轮会失败。"
		} else {
			note += "到压缩点时较早的内容会被总结成摘要。"
		}
		ui.Text(c, note).FontSize(11.5).TextColor(pal.subtle)
	})
}

// contextBar is the run panel's measure of a task's context: filled as far
// as it is taken, with a tick where it gets compacted.
func contextBar(c *ui.Context, pal *palette, used, limit int, cp app.Compaction) {
	frac := min(1, float64(used)/float64(limit))
	col := contextColor(c, pal, frac, cp)
	ui.Box(c).Height(6).Draw(func(p *ui.Painter, r ui.Rect) {
		p.Fill(r, pal.hairline, 3)
		if frac > 0 {
			p.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(6, r.W*float32(frac)), H: r.H}, col, 3)
		}
		if !cp.Off {
			x := r.X + r.W*float32(threshold(cp))/100
			p.Fill(ui.Rect{X: x - 1, Y: r.Y - 2, W: 2, H: r.H + 4}, c.Theme().TextMuted, 1)
		}
	})
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return 0
}

// setEffort makes effort the one tasks run at; the next send restarts an
// idle runtime with it. It shows at once, before the save comes back.
func (s *Shell) setEffort(effort string) {
	s.settings.ReasoningEffort = effort
	s.saveSettings(s.settings)
}

// setContext sets the context of model on credential id; 0 goes back to
// the runtime's window.
func (s *Shell) setContext(id, model string, n int) {
	go func() {
		err := s.App.Creds.SetContext(id, model, n)
		s.reload()
		if err != nil {
			s.post(func() { s.notice = "无法保存上下文上限：" + describe(err) })
		}
	}()
}
