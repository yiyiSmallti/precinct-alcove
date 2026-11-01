package shell

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sidecar"
)

// Declarative UI schema v1 (SPEC 3.2): settings_page. The host renders the
// page; the plugin only sees what the user submits, through onUiAction:
//
//	action "submit"      the page's 保存 button
//	action <button id>   a button field
//
// both with payload { values: { <field id>: value } }: text → string,
// toggle → bool, select → the option's value, secretRef → { ref } (only when
// the user entered a new secret; the value itself goes to the system
// credential store and never to the plugin).

type settingsPage struct {
	Title    string            `json:"title"`
	Sections []settingsSection `json:"sections"`
}

type settingsSection struct {
	Title       string          `json:"title"`
	Description string          `json:"description"`
	Fields      []settingsField `json:"fields"`
}

type settingsField struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"` // text | secretRef | toggle | select | button
	Label       string          `json:"label"`
	Help        string          `json:"help"`
	Placeholder string          `json:"placeholder"`
	Value       json.RawMessage `json:"value"`
	Options     []fieldOption   `json:"options"`
	Style       string          `json:"style"` // button: primary | danger
}

// fieldOption is "value" or { value, label }.
type fieldOption struct{ Value, Label string }

func (o *fieldOption) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		o.Value, o.Label = s, s
		return nil
	}
	var v struct{ Value, Label string }
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	if v.Label == "" {
		v.Label = v.Value
	}
	o.Value, o.Label = v.Value, v.Label
	return nil
}

// settingsForm is the user's edits of one settings_page contribution.
type settingsForm struct {
	raw    string // the contribution the form was built from
	page   settingsPage
	values map[string]*fieldValue
	dirty  bool
	busy   bool
	status string
	err    string
}

type fieldValue struct {
	text   string // text, secretRef (new secret), select (option label)
	on     bool   // toggle
	hasRef bool   // secretRef: the plugin already has one stored
}

// pluginForm returns the form for c, rebuilt when the plugin changed the
// contribution and the user has no unsaved edits.
func (s *Shell) pluginForm(plugin string, c sidecar.Contribution) *settingsForm {
	key := plugin + "#" + c.ID
	f := s.forms[key]
	if f != nil && (f.raw == string(c.Raw) || f.dirty || f.busy) {
		return f
	}
	f = &settingsForm{raw: string(c.Raw), values: map[string]*fieldValue{}}
	json.Unmarshal(c.Raw, &f.page)
	for _, sec := range f.page.Sections {
		for _, fd := range sec.Fields {
			v := &fieldValue{}
			switch fd.Type {
			case "text":
				json.Unmarshal(fd.Value, &v.text)
			case "toggle":
				json.Unmarshal(fd.Value, &v.on)
			case "select":
				var val string
				json.Unmarshal(fd.Value, &val)
				labels := optionLabels(fd.Options)
				for i, o := range fd.Options {
					if o.Value == val {
						v.text = labels[i]
						break
					}
				}
				if v.text == "" && len(labels) > 0 {
					v.text = labels[0]
				}
			case "secretRef":
				v.hasRef = len(fd.Value) > 0 && string(fd.Value) != "null" && string(fd.Value) != `""`
			}
			f.values[fd.ID] = v
		}
	}
	s.forms[key] = f
	return f
}

// submit sends the form's values with action; new secrets are stored first
// and replaced by their references.
func (s *Shell) submit(plugin, id string, f *settingsForm, action string) {
	type secret struct{ field, value string }
	values := map[string]any{}
	var secrets []secret
	for _, sec := range f.page.Sections {
		for _, fd := range sec.Fields {
			v := f.values[fd.ID]
			if v == nil || fd.ID == "" {
				continue
			}
			switch fd.Type {
			case "text":
				values[fd.ID] = v.text
			case "toggle":
				values[fd.ID] = v.on
			case "select":
				for i, l := range optionLabels(fd.Options) {
					if l == v.text {
						values[fd.ID] = fd.Options[i].Value
						break
					}
				}
			case "secretRef":
				if strings.TrimSpace(v.text) != "" {
					secrets = append(secrets, secret{fd.ID, v.text})
				}
			}
		}
	}
	f.busy, f.status, f.err = true, "", ""
	calls := s.calls()
	go func() {
		var err error
		for _, sc := range secrets {
			var ref string
			if ref, err = s.App.SetPluginSecret(plugin, sc.field, []byte(sc.value)); err != nil {
				break
			}
			values[sc.field] = map[string]string{"ref": ref}
		}
		if err == nil {
			payload, _ := json.Marshal(map[string]any{"values": values})
			ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
			err = calls.UIAction(ctx, plugin, id, action, payload)
			cancel()
		}
		s.post(func() {
			f.busy = false
			if err != nil {
				f.err = describe(err)
				return
			}
			f.dirty = false
			for _, sc := range secrets {
				if v := f.values[sc.field]; v != nil {
					v.text, v.hasRef = "", true
				}
			}
			if action == "submit" {
				f.status = "已保存"
			}
		})
	}()
}

// optionLabels are the labels the select shows, made unique (a repeated
// label gets its value appended) so the chosen label maps back to exactly
// one option.
func optionLabels(opts []fieldOption) []string {
	count := map[string]int{}
	for _, o := range opts {
		count[o.Label]++
	}
	out := make([]string, len(opts))
	for i, o := range opts {
		out[i] = o.Label
		if count[o.Label] > 1 {
			out[i] = o.Label + "（" + o.Value + "）"
		}
	}
	return out
}
