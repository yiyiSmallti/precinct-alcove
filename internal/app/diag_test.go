package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecisionsSummary(t *testing.T) {
	p := filepath.Join(t.TempDir(), "decisions.jsonl")
	log := `{"kind":"approval","runtime":"r1","request":{"toolName":"bash","reason":"rm -rf secret-project"},"outcome":"approved","time":"t1"}
{"kind":"questions","runtime":"r1","request":{"questions":[{"question":"哪个客户？"},{"question":"预算？"}]},"answers":[{"custom":"机密客户甲"}],"time":"t2"}
not json
`
	if err := os.WriteFile(p, []byte(log), 0o600); err != nil {
		t.Fatal(err)
	}
	b, err := decisionsSummary(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, leak := range []string{"secret-project", "哪个客户", "机密客户甲"} {
		if strings.Contains(got, leak) {
			t.Errorf("summary keeps %q:\n%s", leak, got)
		}
	}
	for _, keep := range []string{`"toolName":"bash"`, `"outcome":"approved"`, `"questions":2`, `"unreadable":true`} {
		if !strings.Contains(got, keep) {
			t.Errorf("summary lacks %s:\n%s", keep, got)
		}
	}
}
