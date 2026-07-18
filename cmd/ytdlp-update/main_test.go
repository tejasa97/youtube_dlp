package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunValidatesExplicitTrustAndRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing operation exit = %d", code)
	}
	seed := sha256.Sum256([]byte("ytdlp-update command deterministic test key"))
	publicKey := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	args := []string{
		"snapshot", "--root", filepath.Join(t.TempDir(), "updates"),
		"--public-key", hex.EncodeToString(publicKey), "--now", "2026-07-18T00:00:00Z",
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"product":"ytdlp-go"`) {
		t.Fatalf("snapshot exit/output = %d %q %q", code, stdout.String(), stderr.String())
	}
}

func TestParseKeysRejectsDuplicateAndDoesNotEchoInput(t *testing.T) {
	seed := sha256.Sum256([]byte("duplicate update command key"))
	key := hex.EncodeToString(ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey))
	if _, err := parseKeys([]string{key, key}); err == nil || strings.Contains(err.Error(), key) {
		t.Fatalf("duplicate error = %v", err)
	}
}
