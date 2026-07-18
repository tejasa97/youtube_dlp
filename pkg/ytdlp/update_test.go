package ytdlp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	internalupdate "github.com/ytdlp-go/ytdlp/internal/update"
)

func TestPublicUpdaterApplyAndRollback(t *testing.T) {
	seed := sha256.Sum256([]byte("ytdlp-go public updater API deterministic test key"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := UpdateKeyID(publicKey)
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	trust := UpdateTrust{
		Keys: map[string]ed25519.PublicKey{keyID: publicKey}, Threshold: 1,
		Role: internalupdate.ReleaseRole, Product: "ytdlp-go",
		Channels:  []UpdateChannel{UpdateChannelStable},
		Platforms: []UpdatePlatform{{GOOS: "linux", GOARCH: "amd64"}},
	}
	health := UpdateHealthCheckFunc(func(_ context.Context, path string, target UpdateTarget) error {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != target.Version {
			return errors.New("unexpected staged artifact")
		}
		return nil
	})
	updater, err := OpenUpdater(context.Background(), t.TempDir(), UpdateOptions{
		Trust: trust, Product: "ytdlp-go", Channel: UpdateChannelStable,
		GOOS: "linux", GOARCH: "amd64", Clock: func() time.Time { return now }, Health: health,
	})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(generation uint64, version string) {
		t.Helper()
		body := []byte(version)
		digest := sha256.Sum256(body)
		envelope, err := internalupdate.Sign(internalupdate.Metadata{
			Spec: internalupdate.MetadataSpec, Role: internalupdate.ReleaseRole, Product: "ytdlp-go",
			Generation: generation, Expires: now.Add(24 * time.Hour).Format(time.RFC3339),
			Targets: []internalupdate.Target{{Version: version, Channel: internalupdate.ChannelStable, GOOS: "linux", GOARCH: "amd64", Artifact: "ytdlp-go", Size: int64(len(body)), SHA256: hex.EncodeToString(digest[:])}},
		}, map[string]ed25519.PrivateKey{keyID: privateKey})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := updater.Apply(context.Background(), envelope, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	apply(1, "1.0.0")
	apply(2, "1.1.0")
	state, err := updater.Rollback(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if state.Active == nil || state.Active.Version != "1.0.0" || state.HighestGeneration != 2 {
		t.Fatalf("unexpected rollback state: %#v", state)
	}
}

func TestPublicUpdaterCategorizesTrustAndCancellation(t *testing.T) {
	if _, err := VerifyUpdateMetadata([]byte(`{"signed":{},"signatures":[]}`), UpdateTrust{}); !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("invalid trust category = %v", err)
	}
	var updater *Updater
	if _, err := updater.Apply(context.Background(), nil, nil); !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("nil updater category = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := OpenUpdater(cancelled, t.TempDir(), UpdateOptions{}); !IsCategory(err, ErrorCancelled) {
		t.Fatalf("cancellation category = %v", err)
	}
}
