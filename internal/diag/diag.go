// Package diag exports the diagnostics bundle (PRD 8, acceptance A4): a zip
// of Alcove's logs and state files with every secret taken out. Keys the
// credential store holds are removed by value; anything that looks like a
// key, token or password is removed by pattern; the home directory becomes
// "~". Conversations (the dsh home) are never included.
package diag

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Source says what goes into a bundle.
type Source struct {
	Root  string   // data directory
	Files []string // files under Root, included when present
	Dirs  []string // directories under Root, included recursively
	// Extra are generated files (name → content), e.g. about.json.
	Extra map[string][]byte
	// Secrets are values that must not appear anywhere in the bundle.
	Secrets [][]byte
	Home    string // replaced by "~"
}

// MaxFile is how much of one file goes in: the tail of longer files.
const MaxFile = 4 << 20

// Redacted replaces what was taken out.
const Redacted = "[已脱敏]"

// Manifest is manifest.json in the bundle.
type Manifest struct {
	CreatedAt  time.Time `json:"createdAt"`
	Files      []string  `json:"files"`
	Truncated  []string  `json:"truncated,omitempty"`
	Redactions int       `json:"redactions"`
}

// Export writes the bundle to w.
func Export(w io.Writer, src Source) (*Manifest, error) {
	m := &Manifest{CreatedAt: time.Now().UTC()}
	r := newRedactor(src.Secrets, src.Home)
	files := map[string][]byte{}
	add := func(rel string) error {
		path := filepath.Join(src.Root, rel)
		b, cut, err := readTail(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		name := filepath.ToSlash(rel)
		if cut {
			m.Truncated = append(m.Truncated, name)
		}
		files[name] = b
		return nil
	}
	for _, f := range src.Files {
		if err := add(f); err != nil {
			return nil, err
		}
	}
	for _, d := range src.Dirs {
		err := filepath.WalkDir(filepath.Join(src.Root, d), func(path string, e fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if !e.Type().IsRegular() {
				return nil
			}
			rel, _ := filepath.Rel(src.Root, path)
			return add(rel)
		})
		if err != nil {
			return nil, err
		}
	}
	for name, b := range src.Extra {
		files[name] = b
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)

	z := zip.NewWriter(w)
	for _, n := range names {
		out := r.redact(files[n])
		fw, err := z.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Deflate, Modified: m.CreatedAt})
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write(out); err != nil {
			return nil, err
		}
		m.Files = append(m.Files, n)
	}
	m.Redactions = r.count
	mb, _ := json.MarshalIndent(m, "", "  ")
	fw, err := z.Create("manifest.json")
	if err != nil {
		return nil, err
	}
	fw.Write(mb)
	return m, z.Close()
}

// readTail reads path, or its last MaxFile bytes.
func readTail(path string) ([]byte, bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, false, err
	}
	if st.Size() <= MaxFile {
		b, err := io.ReadAll(f)
		return b, false, err
	}
	if _, err := f.Seek(-MaxFile, io.SeekEnd); err != nil {
		return nil, false, err
	}
	b, err := io.ReadAll(f)
	// Start at a line boundary.
	if i := bytes.IndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	return b, true, err
}

// Patterns that look like secrets whatever store they came from.
var patterns = []*regexp.Regexp{
	// "apiKey": "…", "token":"…", "password": "…" and similar JSON fields.
	regexp.MustCompile(`(?i)("[a-z0-9_.-]*(?:api[_-]?key|token|secret|password|passwd|authorization|cookie)[a-z0-9_.-]*"\s*:\s*)"(?:[^"\\]|\\.)*"`),
	// Authorization headers.
	regexp.MustCompile(`(?i)\b((?:Bearer|Basic)\s+)[A-Za-z0-9._~+/=-]{8,}`),
	// KEY=value and key: value in logs and env dumps.
	regexp.MustCompile(`(?i)\b([A-Z0-9_]*(?:API_KEY|TOKEN|SECRET|PASSWORD|AUTHORIZATION)[A-Z0-9_]*\s*[=:]\s*)[^\s"',;]+`),
	// Common key shapes: sk-…, ghp_…, xox…-…, AKIA…
	regexp.MustCompile(`()\b(?:sk-[A-Za-z0-9_-]{8,}|gh[pousr]_[A-Za-z0-9]{20,}|xox[abpr]-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16})`),
}

type redactor struct {
	secrets [][]byte
	home    string
	count   int
}

func newRedactor(secrets [][]byte, home string) *redactor {
	r := &redactor{home: home}
	for _, s := range secrets {
		if len(bytes.TrimSpace(s)) >= 4 {
			r.secrets = append(r.secrets, s)
		}
	}
	// Longest first, so a secret that contains another goes whole.
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

func (r *redactor) redact(b []byte) []byte {
	for _, s := range r.secrets {
		if n := bytes.Count(b, s); n > 0 {
			r.count += n
			b = bytes.ReplaceAll(b, s, []byte(Redacted))
		}
		// JSON-escaped form too (a key with a quote or backslash).
		if esc, _ := json.Marshal(string(s)); len(esc) > 2 && !bytes.Equal(esc[1:len(esc)-1], s) {
			e := esc[1 : len(esc)-1]
			if n := bytes.Count(b, e); n > 0 {
				r.count += n
				b = bytes.ReplaceAll(b, e, []byte(Redacted))
			}
		}
	}
	for _, p := range patterns {
		b = p.ReplaceAllFunc(b, func(m []byte) []byte {
			sub := p.FindSubmatch(m)
			if bytes.HasSuffix(m, []byte(Redacted)) || bytes.HasSuffix(m, []byte(`"`+Redacted+`"`)) {
				return m
			}
			r.count++
			if bytes.HasSuffix(bytes.TrimSpace(m), []byte(`"`)) && p == patterns[0] {
				return append(append([]byte{}, sub[1]...), []byte(`"`+Redacted+`"`)...)
			}
			return append(append([]byte{}, sub[1]...), []byte(Redacted)...)
		})
	}
	if r.home != "" && len(r.home) > 1 {
		b = bytes.ReplaceAll(b, []byte(r.home), []byte("~"))
		if strings.Contains(r.home, `\`) {
			// JSON-escaped Windows paths.
			b = bytes.ReplaceAll(b, []byte(strings.ReplaceAll(r.home, `\`, `\\`)), []byte("~"))
		}
	}
	return b
}
