package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsMissingAndInvalidTrust(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing operation exit = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"verify", "--public-key", "bad", "--now", "2026-07-18T00:00:00Z"}, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "bad") {
		t.Fatalf("invalid trust exit/output = %d %q", code, stderr.String())
	}
}

func TestRunVerifyRejectsMalformedPackWithoutLeakingBytes(t *testing.T) {
	seed := sha256.Sum256([]byte("ytdlp-pack command deterministic test key"))
	publicKey := ed25519.NewKeyFromSeed(seed[:]).Public().(ed25519.PublicKey)
	archive := filepath.Join(t.TempDir(), "hostile.pack")
	if err := os.WriteFile(archive, []byte("secret-pack-body"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"verify", "--archive", archive, "--public-key", hex.EncodeToString(publicKey), "--now", "2026-07-18T00:00:00Z",
	}, &stdout, &stderr)
	if code != 2 || strings.Contains(stderr.String(), "secret-pack-body") || stdout.Len() != 0 {
		t.Fatalf("verify exit/output = %d %q %q", code, stdout.String(), stderr.String())
	}
}
