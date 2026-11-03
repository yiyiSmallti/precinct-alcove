package main

import (
	"crypto/ed25519"
	_ "embed"
	"fmt"
	"strings"

	"github.com/yiyiSmallti/precinct-alcove/internal/plugins/sig"
)

// trustedKeysFile lists the plugin signing keys every Alcove build trusts
// (SPEC: the first-party key ships with Alcove; org keys come later from
// policy). One base64 Ed25519 public key per line; # starts a comment.
//
//go:embed trusted-keys.txt
var trustedKeysFile string

// trust is what plugin installs verify against: the built-in keys, plus,
// in development builds only (Version ends in -dev, i.e. go run or go
// build without mygo), keys from ALCOVE_DEV_TRUSTED_KEYS (comma-separated)
// and unsigned packages when ALCOVE_DEV_ALLOW_UNSIGNED=1.
func trust(version string, getenv func(string) string) (keys []ed25519.PublicKey, allowUnsigned bool, err error) {
	keys, err = parseKeys(strings.Split(trustedKeysFile, "\n"))
	if err != nil {
		return nil, false, fmt.Errorf("built-in trusted-keys.txt: %w", err)
	}
	if !strings.HasSuffix(version, "-dev") {
		return keys, false, nil
	}
	extra, err := parseKeys(strings.Split(getenv("ALCOVE_DEV_TRUSTED_KEYS"), ","))
	if err != nil {
		return nil, false, fmt.Errorf("ALCOVE_DEV_TRUSTED_KEYS: %w", err)
	}
	return append(keys, extra...), getenv("ALCOVE_DEV_ALLOW_UNSIGNED") == "1", nil
}

func parseKeys(lines []string) ([]ed25519.PublicKey, error) {
	var keys []ed25519.PublicKey
	for _, l := range lines {
		if i := strings.IndexByte(l, '#'); i >= 0 {
			l = l[:i]
		}
		if l = strings.TrimSpace(l); l == "" {
			continue
		}
		k, err := sig.ParsePublicKey(l)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", l, err)
		}
		keys = append(keys, k)
	}
	return keys, nil
}
