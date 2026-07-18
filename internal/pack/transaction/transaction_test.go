package transaction

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	"github.com/ytdlp-go/ytdlp/internal/pack/catalog"
	"github.com/ytdlp-go/ytdlp/internal/pack/upgrade"
)

var (
	packSeed    = sha256.Sum256([]byte("transaction fixture pack publisher"))
	catalogSeed = sha256.Sum256([]byte("transaction fixture catalog publisher"))
)

func TestInstallEndToEndUpgradeAndCompatibilityMatrix(t *testing.T) {
	for _, hostMinor := range []int{0, 1} {
		for _, contractMinor := range []int{0, 1} {
			t.Run(fmt.Sprintf("host-v1.%d-pack-v1.%d", hostMinor, contractMinor), func(t *testing.T) {
				request, _ := fixtureRequest(t, canonicalTempRoot(t), "1.0.0", contractMinor, nil, nil)
				request.ContractPolicy.Host.ContractVersion.Minor = hostMinor
				receipt, err := Install(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if receipt.Install.State.Current != "1.0.0" || receipt.Prepared.Contract.Manifest.ContractVersion.Minor != contractMinor {
					t.Fatalf("unexpected receipt: %+v", receipt)
				}
			})
		}
	}

	root := canonicalTempRoot(t)
	firstRequest, firstContract := fixtureRequest(t, root, "1.0.0", 0, nil, nil)
	if _, err := Install(context.Background(), firstRequest); err != nil {
		t.Fatal(err)
	}
	secondRequest, _ := fixtureRequest(t, root, "1.1.0", 1, &firstContract, []pack.Permission{pack.PermissionNetwork})
	secondRequest.PackPolicy.CurrentVersion = "1.0.0"
	secondRequest.ApprovePermissionIncrease = true
	receipt, err := Install(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Install.State.Current != "1.1.0" || receipt.Install.State.Previous != "1.0.0" || !receipt.Prepared.Contract.Review.Increase() {
		t.Fatalf("upgrade was not reviewed and activated: %+v", receipt)
	}
}

func TestPrepareBindsCatalogArtifactPublisherAndContractBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*InstallRequest)
		want   error
	}{
		{"artifact-size", func(r *InstallRequest) {
			r.LoadArtifact = func(context.Context, string, int64) ([]byte, error) { return []byte("short"), nil }
		}, ErrArtifact},
		{"artifact-digest", func(r *InstallRequest) {
			original := r.LoadArtifact
			r.LoadArtifact = func(ctx context.Context, path string, maximum int64) ([]byte, error) {
				body, err := original(ctx, path, maximum)
				body[len(body)-1] ^= 1
				return body, err
			}
		}, ErrArtifact},
		{"contract-name", func(r *InstallRequest) {
			r.Contract = signedContract(t, contractManifest("other-pack", "1.0.0", 1, nil))
		}, ErrBinding},
		{"contract-publisher", func(r *InstallRequest) {
			otherSeed := sha256.Sum256([]byte("untrusted transaction contract publisher"))
			other := ed25519.NewKeyFromSeed(otherSeed[:])
			r.Contract, _ = upgrade.Sign(context.Background(), contractManifest("fixture-pack", "1.0.0", 1, nil), other)
			otherID, _ := pack.KeyID(other.Public().(ed25519.PublicKey))
			r.ContractPolicy.TrustedKeys[otherID] = other.Public().(ed25519.PublicKey)
		}, ErrBinding},
		{"policy-current-mismatch", func(r *InstallRequest) {
			current := contractManifest("fixture-pack", "0.9.0", 1, nil)
			r.ContractPolicy.Current = &current
		}, ErrBinding},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "not-created")
			request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, nil)
			test.mutate(&request)
			_, err := Install(context.Background(), request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Install() error = %v, want %v", err, test.want)
			}
			if _, statErr := os.Stat(root); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("preflight failure mutated root: %v", statErr)
			}
		})
	}
}

func TestPermissionRevocationCancellationAndLoaderFailuresPrecedeMutation(t *testing.T) {
	t.Run("permission-review", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, []pack.Permission{pack.PermissionNetwork})
		if _, err := Install(context.Background(), request); !errors.Is(err, upgrade.ErrPermissionReview) {
			t.Fatalf("error = %v", err)
		}
		assertAbsent(t, root)
	})
	t.Run("catalog-revocation", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, nil)
		request.Catalog = signedCatalog(t, request.PreparedEntryForTest(), true)
		if _, err := Install(context.Background(), request); !errors.Is(err, catalog.ErrRevoked) {
			t.Fatalf("error = %v", err)
		}
		assertAbsent(t, root)
	})
	t.Run("pack-revocation", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, nil)
		request.PackPolicy.Revocations.Packages = []pack.PackageRevocation{{Name: "fixture-pack", Version: "1.0.0"}}
		if _, err := Install(context.Background(), request); !errors.Is(err, pack.ErrRevoked) {
			t.Fatalf("error = %v", err)
		}
		assertAbsent(t, root)
	})
	t.Run("cancelled-loader", func(t *testing.T) {
		root := filepath.Join(t.TempDir(), "root")
		request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, nil)
		ctx, cancel := context.WithCancel(context.Background())
		request.LoadArtifact = func(ctx context.Context, _ string, _ int64) ([]byte, error) {
			cancel()
			return nil, ctx.Err()
		}
		if _, err := Install(ctx, request); !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v", err)
		}
		assertAbsent(t, root)
	})
}

func TestRollbackRevalidatesCatalogContractArtifactAndRevocation(t *testing.T) {
	root := canonicalTempRoot(t)
	firstRequest, firstContract := fixtureRequest(t, root, "1.0.0", 1, nil, []pack.Permission{pack.PermissionNetwork})
	firstRequest.ApprovePermissionIncrease = true
	firstReceipt, err := Install(context.Background(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest, _ := fixtureRequest(t, root, "2.0.0", 1, &firstContract, nil)
	secondRequest.PackPolicy.CurrentVersion = "1.0.0"
	secondReceipt, err := Install(context.Background(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	rollback := RollbackRequest{
		Catalog: firstRequest.Catalog, CatalogPolicy: firstRequest.CatalogPolicy,
		Name: "fixture-pack", TargetVersion: "1.0.0", Contract: firstRequest.Contract,
		ContractPolicy: firstRequest.ContractPolicy, PackPolicy: firstRequest.PackPolicy, Root: root,
	}
	rollback.Catalog = signedCatalog(t, firstReceipt.Prepared.Entry, true)
	if _, err := Rollback(context.Background(), rollback); !errors.Is(err, catalog.ErrRevoked) {
		t.Fatalf("revoked Rollback() error = %v", err)
	}
	if current := readCurrent(t, root); current != secondReceipt.Install.State.Current {
		t.Fatalf("revoked rollback changed activation to %q", current)
	}
	rollback.Catalog = firstRequest.Catalog
	if _, err := Rollback(context.Background(), rollback); !errors.Is(err, pack.ErrPermissionReview) {
		t.Fatalf("permission Rollback() error = %v", err)
	}
	if current := readCurrent(t, root); current != "2.0.0" {
		t.Fatalf("unapproved rollback changed activation to %q", current)
	}
	rollback.ApprovePermissionIncrease = true
	receipt, err := Rollback(context.Background(), rollback)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Rollback.State.Current != "1.0.0" || receipt.Rollback.Transition.Reason != "rollback" {
		t.Fatalf("unexpected rollback: %+v", receipt)
	}
}

func TestConcurrentTransactionsHaveSingleActivation(t *testing.T) {
	root := canonicalTempRoot(t)
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "fixture-pack"), 0o700); err != nil {
		t.Fatal(err)
	}
	request, _ := fixtureRequest(t, root, "1.0.0", 1, nil, nil)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := Install(context.Background(), request)
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	var success, rejected int
	for err := range errorsSeen {
		if err == nil {
			success++
		} else if errors.Is(err, pack.ErrAlreadyInstalled) || errors.Is(err, pack.ErrLocked) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if success != 1 || rejected != 1 {
		t.Fatalf("success=%d rejected=%d", success, rejected)
	}
}

func FuzzPrepare(f *testing.F) {
	request, _ := fixtureRequest(f, "/unused", "1.0.0", 1, nil, nil)
	validCatalog, validContract := append([]byte(nil), request.Catalog...), append([]byte(nil), request.Contract...)
	artifact, _ := request.LoadArtifact(context.Background(), "", 0)
	f.Add(validCatalog, validContract, artifact)
	f.Add([]byte("{}"), []byte("{}"), []byte("not an archive"))
	f.Fuzz(func(t *testing.T, catalogBytes, contractBytes, artifactBytes []byte) {
		candidate := request
		candidate.Catalog = catalogBytes
		candidate.Contract = contractBytes
		candidate.LoadArtifact = func(context.Context, string, int64) ([]byte, error) { return artifactBytes, nil }
		_, _ = Prepare(context.Background(), candidate)
	})
}

type testingT interface {
	Helper()
	Fatal(...any)
}

func fixtureRequest(t testingT, root, version string, contractMinor int, current *upgrade.Manifest, permissions []pack.Permission) (InstallRequest, upgrade.Manifest) {
	t.Helper()
	privateKey := packKey()
	keyID, _ := pack.KeyID(privateKey.Public().(ed25519.PublicKey))
	manifest := pack.Manifest{
		SchemaVersion: pack.SchemaVersion, Name: "fixture-pack", Version: version,
		Runtime: pack.RuntimeRPC, Entrypoint: "bin/fixture", PublisherKeyID: keyID,
		CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2028-01-01T00:00:00Z",
		Permissions: append([]pack.Permission(nil), permissions...),
	}
	archive, err := pack.Build(manifest, map[string]pack.Payload{"bin/fixture": {Bytes: []byte("fixture " + version), Mode: 0o700}}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archive)
	entry := catalog.Entry{
		Name: manifest.Name, Version: version, Artifact: "packs/fixture-" + version + ".ydp",
		ArchiveSHA256: fmt.Sprintf("%x", digest), ArchiveSize: int64(len(archive)), PublisherKeyID: keyID,
	}
	contract := contractManifest(manifest.Name, version, contractMinor, permissions)
	signed := signedContract(t, contract)
	packPublic := privateKey.Public().(ed25519.PublicKey)
	catalogPrivate := catalogKey()
	catalogID, _ := pack.KeyID(catalogPrivate.Public().(ed25519.PublicKey))
	request := InstallRequest{
		Catalog:       signedCatalog(t, entry, false),
		CatalogPolicy: catalog.Policy{Trust: map[string]ed25519.PublicKey{catalogID: catalogPrivate.Public().(ed25519.PublicKey)}, Now: fixtureNow()},
		Name:          manifest.Name, Version: version,
		LoadArtifact: func(ctx context.Context, artifact string, maximum int64) ([]byte, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if artifact != entry.Artifact || maximum != entry.ArchiveSize {
				return nil, errors.New("unexpected catalog resolution")
			}
			return append([]byte(nil), archive...), nil
		},
		Contract: signed,
		ContractPolicy: upgrade.Policy{
			Host:        upgrade.Host{ContractVersion: upgrade.ContractVersion{Major: 1, Minor: 1}, Capabilities: []string{"host.network"}, RequiredPackCapabilities: []string{"extractor.resolve"}},
			TrustedKeys: map[string]ed25519.PublicKey{keyID: packPublic}, Current: current,
		},
		PackPolicy: pack.VerifyPolicy{Trust: map[string]ed25519.PublicKey{keyID: packPublic}, Now: fixtureNow(), HostVersion: "1.4.0"},
		Root:       root,
	}
	return request, contract
}

// PreparedEntryForTest obtains the authenticated entry without duplicating
// fixture state in revocation tests.
func (request InstallRequest) PreparedEntryForTest() catalog.Entry {
	verified, _ := catalog.Verify(context.Background(), request.Catalog, request.CatalogPolicy)
	entry, _ := catalog.Resolve(context.Background(), verified, request.Name, request.Version)
	return entry
}

func contractManifest(name, version string, minor int, permissions []pack.Permission) upgrade.Manifest {
	return upgrade.Manifest{
		ContractVersion: upgrade.ContractVersion{Major: 1, Minor: minor}, Name: name, Version: version,
		Runtime: upgrade.RuntimeRPC, Entrypoint: "bin/fixture", Permissions: append([]pack.Permission(nil), permissions...),
		Provides: []string{"extractor.resolve"},
	}
}

func signedContract(t testingT, manifest upgrade.Manifest) []byte {
	t.Helper()
	encoded, err := upgrade.Sign(context.Background(), manifest, packKey())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func signedCatalog(t testingT, entry catalog.Entry, revoked bool) []byte {
	t.Helper()
	input := catalog.Catalog{
		SchemaVersion: catalog.SchemaV1, GeneratedAt: "2026-07-01T00:00:00Z", ExpiresAt: "2026-09-01T00:00:00Z",
		Entries: []catalog.Entry{entry},
	}
	if revoked {
		input.Revocations = []catalog.Revocation{{Name: entry.Name, Version: entry.Version, ArchiveSHA256: entry.ArchiveSHA256}}
	}
	encoded, err := catalog.Build(input, catalogKey())
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func packKey() ed25519.PrivateKey    { return ed25519.NewKeyFromSeed(packSeed[:]) }
func catalogKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(catalogSeed[:]) }
func fixtureNow() time.Time          { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("path %q exists: %v", path, err)
	}
}

func canonicalTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "packs")
}

func readCurrent(t *testing.T, root string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "fixture-pack", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state pack.State
	if err := json.Unmarshal(body, &state); err != nil {
		t.Fatal(err)
	}
	return state.Current
}
