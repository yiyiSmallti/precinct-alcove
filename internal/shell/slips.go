package shell

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	alrt "github.com/yiyiSmallti/precinct-alcove/internal/runtime"
	"github.com/yiyiSmallti/precinct-alcove/internal/runtime/events"
)

// Slips are what a task waits on the user for, pinned over the composer:
// an approval of a step past the workspace's bounds, and the agent's
// questions.

// approvalCard asks the user to allow a step once or refuse it: which step
// and why, and, when the request names a call of the conversation, exactly
// what it runs. Only the call the request names is shown, never a guess at
// which it might be.
func (s *Shell) approvalCard(c *ui.Context, pal *palette, v *wsView, a *approval) {
	t := c.Theme()
	reason := a.req.DisplayReason.Text(s.Lang)
	if reason == "" {
		reason = a.req.Reason
	}
	var call *events.Entry
	if tk := v.tasks[a.req.SessionID]; tk != nil {
		call = tk.session.Call(a.req.CallID)
	}
	ink := pal.waiting
	slip(c, pal, "ask:"+a.req.RequestID, ink).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			slipMark(c, ink.Alpha(0.16), func() { ui.Icon(c, stepIcon(a.req.ToolName)).Size(13, 13).TextColor(ink.Mix(t.Text, 0.25)) })
			ui.Text(c, "需要你确认").FontSize(13.5).Bold()
			ui.Text(c, toolVerb(a.req.ToolName)).FontSize(13).TextColor(t.TextMuted).SingleLine().Shrink(1).MinWidth(0)
		})
		if reason != "" {
			ui.Text(c, reason).FontSize(13.5).LineHeight(1.5).Selectable()
		}
		if call != nil {
			if cmd := firstString(call.Args, "command", "cmd"); cmd != "" {
				terminal(c, pal, clip(cmd, 2000), "", false)
			} else if what := toolSummary(call.Args); what != "" {
				codeBox(c, pal, what, true)
			}
		}
		ui.Row(c).Gap(8).Padding(2, 0, 0).Children(func() {
			if ui.PrimaryButton(c, "允许这一次").Clicked() {
				answer(a, alrt.AllowedOnce)
			}
			if ui.Button(c, "拒绝").Clicked() {
				answer(a, alrt.Rejected)
			}
		})
	})
}

// firstString is the first of keys that is a non-empty string field of
// the JSON object raw, or "".
func firstString(raw string, keys ...string) string {
	for _, k := range keys {
		if v := jsonString(raw, k); strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// answer gives an approval its outcome, once.
func answer(a *approval, outcome string) {
	select {
	case a.answer <- outcome:
	default: // already answered
	}
}

// questionCard is the agent's questions on a slip: each with its options
// (one to pick, or several to tick) and a line for an answer of the user's
// own, which stands in for a single pick; 提交 sends them all.
func questionCard(c *ui.Context, pal *palette, q *question) {
	t := c.Theme()
	ink := t.Accent
	// In the ink the mark is solid, the one dark spot on the slip.
	mark, sign := ink.Alpha(0.16), ink
	if pal.inkAccent {
		mark, sign = ink, t.AccentText
	}
	slip(c, pal, "ask:"+q.req.RequestID, ink).Children(func() {
		ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
			slipMark(c, mark, func() { ui.Text(c, "?").FontSize(14).Bold().Font(pal.display).TextColor(sign) })
			ui.Text(c, "想先问你").FontSize(13.5).Bold()
		})
		for i, qq := range q.req.Questions {
			ui.Column(c).Key(i).Gap(8).Children(func() {
				if qq.Header != "" {
					ui.Text(c, qq.Header).FontSize(11.5).FontWeight(600).TextColor(pal.subtle)
				}
				ui.Text(c, qq.Question).FontSize(14).FontWeight(600).LineHeight(1.45).Selectable()
				if qq.Detail != "" {
					ui.Text(c, qq.Detail).FontSize(13).LineHeight(1.5).TextColor(t.TextMuted).Selectable()
				}
				// A single pick counts only while no answer is typed.
				own := strings.TrimSpace(q.custom[i]) != ""
				for j, o := range qq.Options {
					on := q.choice[i] == j && !own
					if qq.MultiSelect {
						on = q.multi[i][j]
					}
					if optionRow(c, pal, j, o.Label, o.Description, on, qq.MultiSelect) {
						if qq.MultiSelect {
							q.multi[i][j] = !q.multi[i][j]
						} else {
							q.choice[i], q.custom[i] = j, ""
						}
					}
				}
				ui.TextInput(c, &q.custom[i]).Placeholder("其他（可选）")
			})
		}
		ui.Row(c).Padding(2, 0, 0).Children(func() {
			if !ui.PrimaryButton(c, "提交").Clicked() {
				return
			}
			var out []alrt.Answer
			for i, qq := range q.req.Questions {
				a := alrt.Answer{ID: qq.ID, Custom: strings.TrimSpace(q.custom[i])}
				switch {
				case qq.MultiSelect:
					for j, on := range q.multi[i] {
						if on {
							a.Selected = append(a.Selected, qq.Options[j].Label)
						}
					}
				case len(qq.Options) > 0 && a.Custom == "":
					a.Selected = []string{qq.Options[q.choice[i]].Label}
				}
				out = append(out, a)
			}
			select {
			case q.answer <- out:
			default:
			}
		})
	})
}

// slip is the paper of a slip: tinted in ink, edged and lifted in it.
// In the window's own ink (an accent that is the ink) a tint is only gray,
// which reads as disabled, and its shadow as smudge: the slip stays paper,
// edged in the ink and lifted by the plain shadow.
func slip(c *ui.Context, pal *palette, key any, ink ui.Color) ui.Element {
	e := ui.Column(c).Key(key).Gap(10).Padding(14, 16).Radius(14).
		Transition(ui.ElementTransition{Enter: &ui.Motion{Opacity: 0, Y: 8}})
	if pal.inkAccent && ink == c.Theme().Accent {
		return e.Background(pal.content).Border(1, ink.Alpha(0.24)).
			Shadow(0, 12, 32, -12, pal.shadow.Alpha(3)).Shadow(0, 1, 2, 0, pal.shadow)
	}
	return e.Background(ink.Alpha(0.07).Over(pal.content)).Border(1, ink.Alpha(0.38)).Shadow(0, 10, 28, -10, ink.Alpha(0.4))
}

// slipMark is the round mark that leads a slip's title, on bg.
func slipMark(c *ui.Context, bg ui.Color, mark func()) {
	ui.Column(c).Size(26, 26).Radius(13).Background(bg).AlignItems(ui.Center).Justify(ui.Center).Shrink(0).Children(mark)
}

// optionRow is one option of a question: a radio (or, multi, a box to
// tick), its label, and what it means; it reports a click.
func optionRow(c *ui.Context, pal *palette, key int, label, about string, on, multi bool) bool {
	t := c.Theme()
	r := ui.Row(c).Key(fmt.Sprint("opt", key)).Padding(9, 12).Gap(10).AlignItems(ui.Start).Radius(10).Cursor(ui.CursorPointer).Focusable().Label(label).Transition(fade)
	switch {
	case on && pal.inkAccent:
		// Chosen in the ink: outlined in it, not grayed.
		r.Background(pal.content).Border(1, t.Accent.Alpha(0.85))
	case on:
		r.Background(t.Accent.Alpha(0.08)).Border(1, t.Accent.Alpha(0.55))
	case r.Hovered():
		r.Background(pal.hover).Border(1, pal.hairline.Alpha(1.6))
	default:
		r.Background(pal.content).Border(1, pal.hairline.Alpha(1.6))
	}
	r.Children(func() {
		mark := ui.Column(c).Size(16, 16).Margin(1, 0, 0).Shrink(0).AlignItems(ui.Center).Justify(ui.Center)
		if multi {
			mark.Radius(4)
		} else {
			mark.Radius(8)
		}
		if on {
			mark.Background(t.Accent)
		} else {
			mark.Border(1.5, pal.subtle)
		}
		mark.Children(func() {
			switch {
			case on && multi:
				ui.Icon(c, icCheck).Size(11, 11).TextColor(t.AccentText)
			case on:
				ui.Box(c).Size(6, 6).Radius(3).Background(t.AccentText)
			}
		})
		ui.Column(c).Gap(2).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, label).FontSize(13.5).FontWeight(500)
			if about != "" {
				ui.Text(c, about).FontSize(12).LineHeight(1.45).TextColor(t.TextMuted)
			}
		})
	})
	return r.Clicked()
}
