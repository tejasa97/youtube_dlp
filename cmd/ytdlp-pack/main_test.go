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
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	packcatalog "github.com/ytdlp-go/ytdlp/internal/pack/catalog"
)

func TestRunRejectsMissingAndInvalidTrust(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"--version"}, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "v1alpha1") {
		t.Fatalf("version exit/output = %d %q", code, stdout.String())
	}
	stdout.Reset()
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("missing operation exit = %d", code)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"verify", "--public-key", "bad", "--now", "2026-07-18T00:00:00Z"}, &stdout, &stderr); code != 2 || strings.Contains(stderr.String(), "bad") {
		t.Fatalf("invalid trust exit/output = %d %q", code, stderr.String())
	}
}

func TestRunCatalogVerifyAndExactResolve(t *testing.T) {
	seed := sha256.Sum256([]byte("ytdlp-pack catalog command deterministic key"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID, _ := pack.KeyID(publicKey)
	encoded, err := packcatalog.Build(packcatalog.Catalog{
		SchemaVersion: packcatalog.SchemaV1, GeneratedAt: "2026-07-19T00:00:00Z", ExpiresAt: "2026-08-19T00:00:00Z",
		Entries: []packcatalog.Entry{{Name: "command-fixture", Version: "1.1.0", Artifact: "packs/command.ydp", ArchiveSHA256: strings.Repeat("b", 64), ArchiveSize: 2048, PublisherKeyID: keyID}},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	trust := []string{"--catalog", path, "--public-key", hex.EncodeToString(publicKey), "--now", time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)}
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), append([]string{"catalog-verify"}, trust...), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"command-fixture"`) {
		t.Fatalf("verify code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	args := append([]string{"catalog-resolve"}, trust...)
	args = append(args, "--name", "command-fixture", "--pack-version", "1.1.0")
	if code := run(context.Background(), args, &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), `"archive_size":2048`) {
		t.Fatalf("resolve code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
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
