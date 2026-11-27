package shell

import (
	"fmt"
	"sort"

	"github.com/egoist/mygo/ui"
)

// PageWaiting gathers everything waiting on the user: approvals and
// questions from every workspace's tasks, and plugin cards that still offer
// actions (the cloud bot asking to run a task on this computer).
const PageWaiting = "waiting"

type waitingCard struct {
	plugin string
	item   *inboxItem
}

// waitingCards lists the plugin inbox cards that still have buttons.
func (s *Shell) waitingCards() []waitingCard {
	var out []waitingCard
	names := make([]string, 0, len(s.chats))
	for name := range s.chats {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, e := range s.chats[name].entries {
			if e.item != nil && e.item.Kind == "card" && e.item.Card != nil && len(e.item.Card.Actions) > 0 {
				out = append(out, waitingCard{name, e.item})
			}
		}
	}
	return out
}

// waitingTotal counts what waits on the user, for the sidebar.
func (s *Shell) waitingTotal() int {
	n := len(s.waitingCards()) + len(s.taskAsks)
	for _, v := range s.views {
		n += len(v.approvals) + len(v.questions)
	}
	return n
}

func (s *Shell) waitingView(c *ui.Context, pal *palette) {
	t := c.Theme()
	topBar(c, pal, func() {
		ui.Icon(c, icInbox).Size(15, 15).TextColor(t.TextMuted)
		ui.Text(c, "等你处理").FontSize(14).Bold().Grow(1)
	})
	n := s.waitingTotal()
	if n == 0 {
		ui.Column(c).Grow(1).AlignItems(ui.Center).Justify(ui.Center).Children(func() {
			emptyState(c, icCheck, pal.done, "没有要你处理的事", "任务需要确认、提问，或助手和云端 bot 想在工作区里运行任务时，会出现在这里。", nil)
		})
		return
	}
	pageColumn(c, pal, fmt.Sprintf("有 %d 件事等你处理", n), "任务要你确认或回答的事，以及助手和云端 bot 想在工作区里运行的任务。", func() {
		for _, w := range s.workspaces {
			v := s.views[w.ID]
			if v == nil || len(v.approvals)+len(v.questions) == 0 {
				continue
			}
			for _, a := range v.approvals {
				s.waitingContext(c, pal, v, a.req.SessionID)
				s.approvalCard(c, pal, v, a)
			}
			for _, q := range v.questions {
				s.waitingContext(c, pal, v, q.req.SessionID)
				questionCard(c, pal, q)
			}
		}
		for i, ask := range s.taskAsks {
			s.askCard(c, pal, ask, fmt.Sprint("ask:", i, ask.plugin, ask.req.ID))
		}
		for _, wc := range s.waitingCards() {
			ui.Row(c).Key("ctx:"+wc.plugin+wc.item.ID).Gap(6).AlignItems(ui.Center).Padding(6, 0, 0).Children(func() {
				ui.Icon(c, icCloud).Size(13, 13).TextColor(t.TextMuted)
				ui.Text(c, "来自 "+displayName(wc.plugin)).FontSize(12).TextColor(t.TextMuted)
			})
			s.cardView(c, pal, wc.plugin, wc.item, "w:"+wc.plugin+wc.item.ID)
		}
	})
}

// waitingContext names the workspace and task a request comes from, with a
// way to open the task.
func (s *Shell) waitingContext(c *ui.Context, pal *palette, v *wsView, sid string) {
	t := c.Theme()
	title := "当前任务"
	tk := v.tasks[sid]
	if tk != nil {
		title = taskTitle(tk)
	}
	ui.Row(c).Key(fmt.Sprint("ctx:", v.ws.ID, sid, len(title))).Gap(6).AlignItems(ui.Center).Padding(6, 0, 0).Children(func() {
		ui.Icon(c, icFolder).Size(13, 13).TextColor(t.TextMuted)
		ui.Text(c, v.ws.Name+" · "+title).FontSize(12).TextColor(t.TextMuted).SingleLine().Grow(1).MinWidth(0)
		if tk != nil && ui.Button(c, "打开任务").Clicked() {
			s.openTask(v, sid)
		}
	})
}
