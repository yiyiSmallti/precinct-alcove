package shell

import (
	"context"
	"fmt"

	"github.com/egoist/mygo/ui"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/tasks"
	"github.com/yiyiSmallti/precinct-alcove/internal/core/workspace"
	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Workspace task requests (task.start): a plugin asks to run a task in one
// of the user's own workspaces. The window asks, in the plugin's
// conversation view and in 等你处理, with a button per workspace; the
// plugin learns only the answer.

// taskAsk is one request waiting for the user.
type taskAsk struct {
	plugin string
	req    sidecar.TaskRequest
	answer chan string // the workspace picked, "" declined
}

// maxAskWorkspaces is how many workspaces a request offers, the current
// project first.
const maxAskWorkspaces = 4

// ConfirmTask is app.Config.ConfirmTask: it shows the request until the
// user answers or ctx ends.
func (s *Shell) ConfirmTask(ctx context.Context, plugin string, req sidecar.TaskRequest) (string, bool) {
	ask := &taskAsk{plugin: plugin, req: req, answer: make(chan string, 1)}
	s.post(func() { s.taskAsks = append(s.taskAsks, ask) })
	select {
	case ws := <-ask.answer:
		return ws, ws != ""
	case <-ctx.Done():
		s.post(func() { s.dropAsk(ask) })
		return "", false
	}
}

func (s *Shell) dropAsk(ask *taskAsk) {
	for i, a := range s.taskAsks {
		if a == ask {
			s.taskAsks = append(s.taskAsks[:i], s.taskAsks[i+1:]...)
			return
		}
	}
}

// answerAsk records the user's answer. An agreed task is listed in its
// workspace right away, under the plugin's name and the request's title.
func (s *Shell) answerAsk(ask *taskAsk, ws string) {
	s.dropAsk(ask)
	if ws != "" {
		if v := s.view(ws); v != nil {
			sid := "task-" + ask.req.ID
			if v.tasks[sid] == nil {
				s.addTask(v, tasks.Task{ID: sid, Title: ask.req.Title, Source: ask.plugin})
			}
		}
	}
	ask.answer <- ws
}

// askWorkspaces are the user's own workspaces a request offers.
func (s *Shell) askWorkspaces() []workspace.Workspace {
	var out []workspace.Workspace
	add := func(w workspace.Workspace) {
		if len(out) >= maxAskWorkspaces || w.OwnerName() != workspace.Personal || w.ID == s.scratchID || w.Missing() {
			return
		}
		for _, o := range out {
			if o.ID == w.ID {
				return
			}
		}
		out = append(out, w)
	}
	if w := s.find(s.curProj); w != nil {
		add(*w)
	}
	for _, w := range s.workspaces {
		add(w)
	}
	return out
}

// askCard is one request, drawn as a task's approval: what the plugin wants
// done, and a button per workspace to run it in.
func (s *Shell) askCard(c *ui.Context, pal *palette, ask *taskAsk, key any) {
	t := c.Theme()
	name := s.pluginTitle(ask.plugin)
	ui.Column(c).Key(key).Gap(8).Padding(14, 16).Radius(12).Border(1, t.Border).Background(pal.content).Shadow(0, 4, 18, 0, pal.shadow).Children(func() {
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			dot(c, pal.waiting)
			ui.Text(c, "需要你确认").FontSize(13).Bold()
			ui.Text(c, name+" 想在工作区里运行任务").FontSize(13).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		})
		ui.Text(c, ask.req.Title).FontSize(14).FontWeight(600).Selectable()
		if ask.req.Detail != "" {
			ui.Text(c, ask.req.Detail).FontSize(13).TextColor(t.TextMuted).Selectable().MaxLines(6).Ellipsis("…")
		}
		list := s.askWorkspaces()
		note := "会在你选的工作区里新建一个任务，用那个工作区的模型和权限运行；越界的操作照常问你，结果交回" + name + "。"
		if len(list) == 0 {
			note = "你还没有自己的项目工作区。先在侧栏添加一个项目文件夹，再回来选择；也可以拒绝。"
		}
		ui.Text(c, note).FontSize(12).TextColor(pal.subtle)
		ui.Row(c).Gap(8).Wrap().Padding(4, 0, 0).AlignItems(ui.Center).Children(func() {
			for i, w := range list {
				label := fmt.Sprintf("在「%s」里运行", w.Name)
				var b ui.Element
				if i == 0 {
					b = ui.PrimaryButton(c, label)
				} else {
					b = ui.Button(c, label)
				}
				if b.Key(w.ID).Clicked() {
					s.answerAsk(ask, w.ID)
				}
			}
			if ui.Button(c, "拒绝").Key("decline").Clicked() {
				s.answerAsk(ask, "")
			}
		})
	})
}

// asksOf lists the requests of one plugin.
func (s *Shell) asksOf(plugin string) []*taskAsk {
	var out []*taskAsk
	for _, a := range s.taskAsks {
		if a.plugin == plugin {
			out = append(out, a)
		}
	}
	return out
}
