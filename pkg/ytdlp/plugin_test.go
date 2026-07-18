//go:build darwin || linux

package ytdlp

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

func TestSignedWASMPluginPackHostAndProductIntegration(t *testing.T) {
	privateKey, trust := pluginPackFixtureTrust(t)
	archive := buildWASMPluginPack(t, privateKey, "1.0.0", "1.0.0")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "packs")
	installed, review, err := InstallPluginPack(context.Background(), archive, root, trust, PluginPackInstallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if review.Increase() || installed.Descriptor().SignerKeyID == "" || installed.Descriptor().ID != "example.wasm" {
		t.Fatalf("unexpected installed plugin: %#v %#v", installed.Descriptor(), review)
	}
	approver := PluginPermissionApproveFunc(func(_ context.Context, request PluginApprovalRequest) (PluginApproval, error) {
		if request.PluginID != "example.wasm" || request.Signer == "" || request.ExecutableDigest == "" || request.ABI != pluginapi.V1_0 {
			t.Fatalf("approval was not identity-bound: %#v", request)
		}
		return PluginApproval{Granted: append([]PluginPermission(nil), request.Requested...)}, nil
	})
	host, err := NewPluginHost(installed, approver, PluginLimits{Timeout: time.Second, MemoryLimitPages: 2})
	if err != nil {
		t.Fatal(err)
	}
	response, err := host.Extract(context.Background(), PluginExtractRequest{ID: "one", URL: "https://fixture.invalid/video"})
	if err != nil || response.Metadata["title"] != "WASM example" {
		t.Fatalf("plugin response = %#v, %v", response, err)
	}
	client := NewClient(WithInstalledPlugins(installed), WithPluginPermissionApprover(approver))
	result, err := client.Run(context.Background(), Request{
		URL: "https://fixture.invalid/video", PluginID: "example.wasm",
		SkipDownload: true, OutputDir: t.TempDir(), OutputTemplate: "%(title)s.%(ext)s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "example.wasm" || !strings.Contains(string(result.InfoJSON), `"title":"WASM example"`) {
		t.Fatalf("unexpected product result: %#v %s", result, result.InfoJSON)
	}
}

func TestPluginPackBindingFailsBeforeFilesystemMutation(t *testing.T) {
	privateKey, trust := pluginPackFixtureTrust(t)
	archive := buildWASMPluginPack(t, privateKey, "1.0.0", "2.0.0")
	parent, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(parent, "packs")
	if _, _, err := InstallPluginPack(context.Background(), archive, root, trust, PluginPackInstallOptions{}); !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("binding error = %v", err)
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("invalid binding mutated install root: %v", err)
	}
}

func TestSandboxedPluginHostBuildsFailClosedNativePolicy(t *testing.T) {
	installed := &InstalledPlugin{packageValue: plugin.Package{
		Manifest:  plugin.Manifest{Runtime: pluginapi.RuntimeNative},
		Directory: "/trusted/plugin", EntrypointPath: "/trusted/plugin/helper",
	}}
	approver := PluginPermissionApproveFunc(func(context.Context, PluginApprovalRequest) (PluginApproval, error) {
		return PluginApproval{}, nil
	})
	policy := PluginSandbox{ReadOnlyPaths: []string{"/input"}, WritablePaths: []string{"/output"}, CPUSeconds: 5, OpenFiles: 32}
	host, err := NewSandboxedPluginHost(installed, approver, PluginLimits{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	policy.ReadOnlyPaths[0] = "/changed"
	config := host.rpcConfig()
	if config.Sandbox == nil || config.Sandbox.ReadOnlyPaths[0] != "/input" || config.Sandbox.Limits.CPUSeconds != 5 || config.Sandbox.Limits.OpenFiles != 32 {
		t.Fatalf("sandbox config = %#v", config.Sandbox)
	}
}

func pluginPackFixtureTrust(t *testing.T) (ed25519.PrivateKey, PluginPackTrust) {
	t.Helper()
	seed := sha256.Sum256([]byte("ytdlp-go public signed plugin pack deterministic test key"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID, err := pack.KeyID(publicKey)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 18, 0, 0, 0, 0, time.UTC)
	return privateKey, PluginPackTrust{Keys: map[string]ed25519.PublicKey{keyID: publicKey}, Now: now, HostVersion: "1.0.0"}
}

func buildWASMPluginPack(t *testing.T, privateKey ed25519.PrivateKey, packVersion, pluginRelease string) []byte {
	t.Helper()
	fixtureHex, err := os.ReadFile(filepath.Join("..", "..", "conformance", "plugins", "wasm", "example.hex"))
	if err != nil {
		t.Fatal(err)
	}
	module, err := hex.DecodeString(strings.TrimSpace(string(fixtureHex)))
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(PluginManifest{
		Schema: "ytdlp-go.plugin/v1", ID: "example.wasm", Name: "WASM example", Release: pluginRelease,
		Runtime: pluginapi.RuntimeWASM, Entrypoint: "example.wasm",
		ABIRange:     pluginapi.VersionRange{Minimum: pluginapi.V1_0, Maximum: pluginapi.V1_0},
		Capabilities: []PluginCapability{pluginapi.CapabilityExtractor},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive, err := pack.Build(pack.Manifest{
		SchemaVersion: pack.SchemaVersion, Name: "example-wasm", Version: packVersion,
		Runtime: pack.RuntimeWASM, Entrypoint: "example.wasm",
		CreatedAt: "2026-01-01T00:00:00Z", ExpiresAt: "2027-01-01T00:00:00Z",
	}, map[string]pack.Payload{
		"plugin.json":  {Bytes: manifestJSON, Mode: 0o600},
		"example.wasm": {Bytes: module, Mode: 0o600},
	}, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	return archive
}
