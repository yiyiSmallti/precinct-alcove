// Package sig verifies the detached Ed25519 signature that travels with every
// plugin package (`<name>-<version>.tgz.sig`, Charter ADR 0001 point 5).
//
// Format of the .sig file (Alcove SPEC 3.2): one line of standard base64
// encoding the 64-byte Ed25519 signature over the 32-byte SHA-256 digest of the
// .tgz. Surrounding whitespace is ignored. dsh and pnpm never check publisher
// signatures; this is the only check.
package sig

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// ErrUnsigned means the .sig file is missing.
var ErrUnsigned = errors.New("package is not signed (no .sig file next to it)")

// ErrBadSignature means no trusted key verifies the signature.
var ErrBadSignature = errors.New("signature does not match any trusted key")

// Digest returns the SHA-256 of the file.
func Digest(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, err
	}
	return h.Sum(nil), nil
}

// Verify checks tgz against sigPath with any of keys and returns the index of
// the key that verified it.
func Verify(tgz, sigPath string, keys []ed25519.PublicKey) (int, error) {
	raw, err := os.ReadFile(sigPath)
	if errors.Is(err, os.ErrNotExist) {
		return -1, ErrUnsigned
	}
	if err != nil {
		return -1, err
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(s) != ed25519.SignatureSize {
		return -1, fmt.Errorf("%s: not a base64 Ed25519 signature", sigPath)
	}
	digest, err := Digest(tgz)
	if err != nil {
		return -1, err
	}
	for i, k := range keys {
		if len(k) == ed25519.PublicKeySize && ed25519.Verify(k, digest, s) {
			return i, nil
		}
	}
	return -1, ErrBadSignature
}

// Sign produces the .sig contents for tgz (used by tests and dev tooling;
// Dovetail signs releases).
func Sign(tgz string, key ed25519.PrivateKey) (string, error) {
	digest, err := Digest(tgz)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, digest)) + "\n", nil
}

// ParsePublicKey reads a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("not a base64 Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}
