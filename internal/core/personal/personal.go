// Package personal keeps the user's personal context: what every agent in
// their personal workspaces should know about them (AGENTS.md) and the
// skills they collected (skills/<name>/SKILL.md). Alcove points the dsh of
// personal workspaces, and only those, at this folder (see the bridge
// bundle); org workspaces and plugin runtimes never read it. Plugins write
// it through the personal_context capability; the user can edit the files.
package personal

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/yiyiSmallti/precinct-alcove/internal/core/fsutil"
)

// Limits. dsh gives all instruction files of a session 64 KiB together and
// drops the broadest one (this) first when over, so the profile stays small.
const (
	MaxProfile     = 16 << 10
	MaxDescription = 500 // dsh cuts a skill's catalog description there
	MaxSkillBody   = 64 << 10
	MaxSkills      = 200
)

// Skill is one skill of the personal context.
type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body,omitempty"`
	// Source is the plugin that wrote it; empty when the user did.
	Source string `json:"source,omitempty"`
}

// Store is the personal context at Dir.
type Store struct {
	Dir string
	mu  sync.Mutex
}

func (s *Store) profilePath() string { return filepath.Join(s.Dir, "AGENTS.md") }
func (s *Store) skillsDir() string   { return filepath.Join(s.Dir, "skills") }

// Profile returns AGENTS.md, empty when there is none.
func (s *Store) Profile() (string, error) {
	b, err := os.ReadFile(s.profilePath())
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return string(b), err
}

// SetProfile replaces AGENTS.md; empty text removes it.
func (s *Store) SetProfile(text string) error {
	text = strings.TrimSpace(text)
	if len(text) > MaxProfile {
		return fmt.Errorf("the profile is %d bytes, more than %d", len(text), MaxProfile)
	}
	if !utf8.ValidString(text) {
		return errors.New("the profile is not UTF-8")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if text == "" {
		if err := os.Remove(s.profilePath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	return fsutil.WriteFile(s.profilePath(), []byte(text+"\n"), 0o600)
}

// dsh's own rule for skill names.
var skillName = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Skills lists the skills, by name, without their bodies. A folder whose
// SKILL.md dsh would not take is left out.
func (s *Store) Skills() ([]Skill, error) {
	entries, err := os.ReadDir(s.skillsDir())
	if errors.Is(err, os.ErrNotExist) {
		return []Skill{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Skill{}
	for _, e := range entries {
		if !e.IsDir() || !skillName.MatchString(e.Name()) {
			continue
		}
		sk, err := s.Skill(e.Name())
		if err != nil {
			continue
		}
		sk.Body = ""
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Skill reads one skill, body included.
func (s *Store) Skill(name string) (Skill, error) {
	if !skillName.MatchString(name) || len(name) > 64 {
		return Skill{}, fmt.Errorf("%q is not a skill name (lowercase words joined by '-')", name)
	}
	b, err := os.ReadFile(filepath.Join(s.skillsDir(), name, "SKILL.md"))
	if err != nil {
		return Skill{}, err
	}
	sk, ok := parse(b)
	if !ok {
		return Skill{}, fmt.Errorf("skill %s has no name and description front matter", name)
	}
	sk.Name = name
	return sk, nil
}

// PutSkill writes (or replaces) a skill as skills/<name>/SKILL.md.
func (s *Store) PutSkill(sk Skill) error {
	sk.Description = strings.Join(strings.Fields(sk.Description), " ")
	sk.Body = strings.TrimSpace(sk.Body)
	switch {
	case !skillName.MatchString(sk.Name) || len(sk.Name) > 64:
		return fmt.Errorf("%q is not a skill name (lowercase words joined by '-', at most 64)", sk.Name)
	case sk.Description == "" || utf8.RuneCountInString(sk.Description) > MaxDescription:
		return fmt.Errorf("a skill's description is 1 to %d characters", MaxDescription)
	case sk.Body == "" || len(sk.Body) > MaxSkillBody:
		return fmt.Errorf("a skill's body is 1 to %d bytes", MaxSkillBody)
	case !utf8.ValidString(sk.Description + sk.Body):
		return errors.New("the skill is not UTF-8")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	path := filepath.Join(s.skillsDir(), sk.Name, "SKILL.md")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		have, err := os.ReadDir(s.skillsDir())
		if err == nil && len(have) >= MaxSkills {
			return fmt.Errorf("the personal context already has %d skills", MaxSkills)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return fsutil.WriteFile(path, render(sk), 0o600)
}

// RemoveSkill deletes a skill's folder; an unknown name is no error.
func (s *Store) RemoveSkill(name string) error {
	if !skillName.MatchString(name) || len(name) > 64 {
		return fmt.Errorf("%q is not a skill name", name)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(filepath.Join(s.skillsDir(), name))
}

// render writes the front matter dsh requires. Strings go as JSON, which is
// valid YAML, so a description with ':' or quotes stays one value.
func render(sk Skill) []byte {
	q := func(v string) string { b, _ := json.Marshal(v); return string(b) }
	var b bytes.Buffer
	b.WriteString("---\n")
	b.WriteString("name: " + q(sk.Name) + "\n")
	b.WriteString("description: " + q(sk.Description) + "\n")
	if sk.Source != "" {
		b.WriteString("metadata:\n  source: " + q(sk.Source) + "\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(sk.Body + "\n")
	return b.Bytes()
}

// parse reads name, description and metadata.source from SKILL.md's front
// matter (the subset render writes, plus plain unquoted values) and the body
// after it.
func parse(b []byte) (Skill, bool) {
	if !bytes.HasPrefix(b, []byte("---\n")) && !bytes.HasPrefix(b, []byte("---\r\n")) {
		return Skill{}, false
	}
	var sk Skill
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 0, 64<<10), MaxSkillBody+(64<<10))
	sc.Scan()
	end := false
	var body []string
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if end {
			body = append(body, line)
			continue
		}
		if line == "---" {
			end = true
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = unquote(strings.TrimSpace(val))
		switch key {
		case "name":
			sk.Name = val
		case "description":
			sk.Description = val
		case "source":
			sk.Source = val
		}
	}
	sk.Body = strings.TrimSpace(strings.Join(body, "\n"))
	return sk, end && sk.Name != "" && sk.Description != ""
}

func unquote(v string) string {
	if strings.HasPrefix(v, `"`) {
		var s string
		if json.Unmarshal([]byte(v), &s) == nil {
			return s
		}
	}
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], "''", "'")
	}
	return v
}
