package diag

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportRedacts(t *testing.T) {
	root := t.TempDir()
	home := "/home/liuliu"
	write := func(rel, s string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o700)
		os.WriteFile(p, []byte(s), 0o600)
	}
	write("settings.json", `{"defaultCredential":"c1","model":"m"}`)
	write("logs/alcove.log", "start\nlaunch env DEEPSEEK_API_KEY=my-own-key-123 in /home/liuliu/proj\nheader Authorization: Bearer abcdefghijklmnop\nok\n")
	write("logs/plugins.log", `{"plugin":"x","msg":"oops leaked q9-STORED-VALUE-77 and sk-abcdefghijkl"}`+"\n")
	write("plugins/settings/x.json", `{"apiToken":"tok-12345","name":"keep me","nested":{"password":"p\"w"}}`)
	write("dsh/sessions/s.jsonl", "conversation that must not go out")
	var buf bytes.Buffer
	m, err := Export(&buf, Source{
		Root: root, Files: []string{"settings.json", "missing.json"}, Dirs: []string{"logs", "plugins/settings"},
		Extra:   map[string][]byte{"about.json": []byte(`{"os":"linux"}`)},
		Secrets: [][]byte{[]byte("q9-STORED-VALUE-77"), []byte("x")}, Home: home,
	})
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	all := ""
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		all += f.Name + "\n" + string(b) + "\n"
	}
	for _, bad := range []string{"my-own-key-123", "abcdefghijklmnop", "q9-STORED-VALUE-77", "sk-abcdefghijkl", "tok-12345", `p\"w`, "/home/liuliu", "conversation"} {
		if strings.Contains(all, bad) {
			t.Errorf("bundle contains %q:\n%s", bad, all)
		}
	}
	for _, good := range []string{"keep me", `"model":"m"`, "~/proj", "about.json", "manifest.json", `"apiToken":"` + Redacted + `"`} {
		if !strings.Contains(all, good) {
			t.Errorf("bundle lacks %q:\n%s", good, all)
		}
	}
	if m.Redactions < 6 {
		t.Errorf("redactions %d", m.Redactions)
	}
	t.Log(names)
}

func TestTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "big.log")
	line := strings.Repeat("a", 99) + "\n"
	os.WriteFile(p, []byte(strings.Repeat(line, MaxFile/100+50)+"last\n"), 0o600)
	b, cut, err := readTail(p)
	if err != nil || !cut || len(b) > MaxFile || !strings.HasSuffix(string(b), "last\n") || !strings.HasPrefix(string(b), "aaa") {
		t.Errorf("tail: cut=%v len=%d err=%v", cut, len(b), err)
	}
}
