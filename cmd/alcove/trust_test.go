package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"testing"
)

func TestTrust(t *testing.T) {
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	k := base64.StdEncoding.EncodeToString(pub)
	builtin := trustedKeysFile
	defer func() { trustedKeysFile = builtin }()
	trustedKeysFile = "# first-party\n" + k + "  # comment\n\n"
	env := map[string]string{"ALCOVE_DEV_TRUSTED_KEYS": k + "," + k, "ALCOVE_DEV_ALLOW_UNSIGNED": "1"}
	get := func(n string) string { return env[n] }

	keys, unsigned, err := trust("0.1.0", get)
	if err != nil || len(keys) != 1 || unsigned {
		t.Fatalf("release build: %d keys, unsigned %v, %v", len(keys), unsigned, err)
	}
	keys, unsigned, err = trust("0.1.0-dev", get)
	if err != nil || len(keys) != 3 || !unsigned {
		t.Fatalf("dev build: %d keys, unsigned %v, %v", len(keys), unsigned, err)
	}
	trustedKeysFile = "not-a-key\n"
	if _, _, err := trust("0.1.0", get); err == nil {
		t.Fatal("malformed built-in key accepted")
	}
}

func TestBuiltinKeysParse(t *testing.T) {
	if _, err := parseKeys([]string{trustedKeysFile}); err != nil {
		t.Fatal(err)
	}
}
