package runtime

import (
	"encoding/json"
	"testing"
)

func TestLocalText(t *testing.T) {
	var r ApprovalRequest
	if err := json.Unmarshal([]byte(`{"displayReason":{"en":"Allow","zh":"允许"}}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := r.DisplayReason.Text("zh"); got != "允许" {
		t.Errorf("zh = %q", got)
	}
	if got := r.DisplayReason.Text("fr"); got != "Allow" {
		t.Errorf("fr fallback = %q", got)
	}
	if err := json.Unmarshal([]byte(`{"displayReason":"plain"}`), &r); err != nil {
		t.Fatal(err)
	}
	if got := r.DisplayReason.Text("zh"); got != "plain" {
		t.Errorf("plain = %q", got)
	}
}
