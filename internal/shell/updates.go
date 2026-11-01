package shell

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"

	"github.com/yiyiSmallti/precinct-alcove/internal/app"
	"github.com/yiyiSmallti/precinct-alcove/internal/update"
)

// CheckEvery is how often Alcove looks for a new version while it runs.
const CheckEvery = 24 * time.Hour

// updatesState is the 更新 section of the settings page (A5).
type updatesState struct {
	checking   bool
	release    *update.Release // a newer version, nil when up to date or not checked
	checked    time.Time
	err        string
	installing bool
	progress   float64 // 0..1 while installing
	installed  string  // the version installed, waiting for the restart
	source     string  // the 更新源 field being edited
	sourceErr  string
}

// StartUpdates applies the saved update source and checks for a new version
// now and every CheckEvery; call it after Load. It does nothing in builds
// that cannot update themselves.
func (s *Shell) StartUpdates(ctx context.Context) {
	if s.Updater == nil || !s.Updater.Enabled() {
		return
	}
	st, _ := s.App.Settings()
	if err := s.Updater.SetSource(st.UpdateSource); err != nil {
		log.Printf("update source %q: %v", st.UpdateSource, err)
	}
	s.post(func() { s.updates.source = st.UpdateSource })
	go func() {
		t := time.NewTicker(CheckEvery)
		defer t.Stop()
		for {
			s.checkUpdates(ctx)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
		}
	}()
}

// checkUpdates asks for a newer version (blocking; the UI calls it in a
// goroutine).
func (s *Shell) checkUpdates(ctx context.Context) {
	s.post(func() { s.updates.checking, s.updates.err = true, "" })
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	rel, err := s.Updater.Check(ctx)
	cancel()
	if err != nil {
		log.Printf("check for updates: %v", err)
	}
	s.post(func() {
		u := &s.updates
		u.checking = false
		if err != nil {
			u.err = "检查更新失败：" + describe(err)
			return
		}
		u.release, u.checked = rel, time.Now()
	})
}

func (s *Shell) installUpdate() {
	s.updates.installing, s.updates.progress, s.updates.err = true, 0, ""
	version := s.updates.release.Version
	go func() {
		err := s.Updater.Install(context.Background(), func(done, total int64) {
			if total > 0 {
				s.post(func() { s.updates.progress = float64(done) / float64(total) })
			}
		})
		if err != nil {
			log.Printf("install update: %v", err)
		}
		s.post(func() {
			u := &s.updates
			u.installing = false
			if err != nil {
				u.err = "安装更新失败：" + describe(err)
				return
			}
			u.installed = version
		})
	}()
}

func (s *Shell) saveUpdateSource() {
	src := strings.TrimSpace(s.updates.source)
	go func() {
		err := s.Updater.SetSource(src)
		if err == nil {
			var st app.Settings
			if st, err = s.App.Settings(); err == nil {
				st.UpdateSource = src
				err = s.App.SaveSettings(st)
			}
		}
		s.reload()
		s.post(func() {
			s.updates.sourceErr = ""
			if err != nil {
				s.updates.sourceErr = describe(err)
				return
			}
			s.updates.source = src
			s.updates.release = nil
		})
		if err == nil {
			s.checkUpdates(context.Background())
		}
	}()
}

// updatesView is the 更新 section of the settings page.
func (s *Shell) updatesView(c *ui.Context) {
	t := c.Theme()
	u := &s.updates
	ui.Text(c, "更新").FontSize(18).Bold()
	version := s.Version
	if version == "" {
		version = "开发版"
	}
	if s.Updater == nil || !s.Updater.Enabled() {
		ui.Text(c, "当前版本 "+version+"。这个版本不能自动更新：开发版，或装在当前用户不能写的位置。").TextColor(t.TextMuted)
		return
	}
	ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
		line := "当前版本 " + version
		switch {
		case u.release != nil:
			line += "，有新版本 " + u.release.Version
		case !u.checked.IsZero():
			line += "，已是最新（" + u.checked.Format("01-02 15:04") + " 检查）"
		}
		ui.Text(c, line).Grow(1)
		if u.checking {
			ui.Spinner(c)
		}
		busy := u.checking || u.installing
		if ui.Button(c, "检查更新").Disabled(busy).Clicked() && !busy {
			go s.checkUpdates(context.Background())
		}
	})
	if r := u.release; r != nil {
		ui.Column(c).Gap(8).Padding(12).Radius(8).Border(1, t.Border).Children(func() {
			title := "Alcove " + r.Version
			if !r.Date.IsZero() {
				title += " · " + r.Date.Format("2006-01-02")
			}
			ui.Text(c, title).Bold()
			if strings.TrimSpace(r.Notes) != "" {
				markdown.View(c, r.Notes, nil)
			}
			switch {
			case u.installed == r.Version:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "已下载并装好，重启后生效。").Grow(1)
					if ui.PrimaryButton(c, "立即重启").Clicked() {
						s.Updater.Relaunch()
					}
				})
			case u.installing:
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					ui.Spinner(c)
					ui.Text(c, fmt.Sprintf("正在下载 %d%%", int(u.progress*100)))
				})
			default:
				if ui.PrimaryButton(c, "下载并安装").Clicked() {
					s.installUpdate()
				}
			}
		})
	}
	if u.err != "" {
		ui.Text(c, u.err).TextColor(t.Danger).Selectable()
	}
	ui.Field(c, "更新源（留空用官方源；企业镜像填放更新文件的 https 目录）", func() {
		ui.Row(c).Gap(8).Children(func() {
			in := ui.TextInput(c, &u.source).Placeholder("https://…").Grow(1)
			changed := strings.TrimSpace(u.source) != s.settings.UpdateSource
			if (ui.Button(c, "保存").Disabled(!changed).Clicked() || in.Submitted()) && changed {
				s.saveUpdateSource()
			}
		})
	})
	if u.sourceErr != "" {
		ui.Text(c, u.sourceErr).TextColor(t.Danger)
	}
}
