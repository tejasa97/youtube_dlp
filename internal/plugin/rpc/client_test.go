package rpc

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/plugin"
)

func TestRPCExtract(t *testing.T) {
	response, err := (Client{}).Extract(context.Background(), helperConfig("success"), plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"})
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != "one" || response.Metadata["title"] != "RPC fixture" {
		t.Fatalf("response = %#v", response)
	}
}

func TestRPCVersionAndPermissionRejection(t *testing.T) {
	if _, err := (Client{}).Extract(context.Background(), helperConfig("version"), request()); !errors.Is(err, plugin.ErrIncompatibleVersion) {
		t.Fatalf("version error = %v", err)
	}
	if _, err := (Client{}).Extract(context.Background(), helperConfig("permission"), request()); !errors.Is(err, plugin.ErrPermissionDenied) {
		t.Fatalf("permission error = %v", err)
	}
	if _, err := (Client{}).Extract(context.Background(), helperConfig("python-manifest"), request()); !errors.Is(err, plugin.ErrPythonRuntime) {
		t.Fatalf("Python manifest error = %v", err)
	}
}

func TestRPCV1MinorUpgradeNegotiatesV1Host(t *testing.T) {
	config := helperConfig("upgrade")
	config.SupportedABI = plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_0}
	response, err := (Client{}).Extract(context.Background(), config, request())
	if err != nil || response.Metadata["abi"] != float64(plugin.ProtocolV1_0) {
		t.Fatalf("upgrade response, error = %#v, %v", response, err)
	}
}

func TestRPCStructuredRemoteError(t *testing.T) {
	response, err := (Client{}).Extract(context.Background(), helperConfig("remote"), request())
	var remote *plugin.RemoteFailure
	if !errors.As(err, &remote) || remote.Detail.Category != plugin.RemoteUnavailable || response.ID != "one" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
}

func TestRPCPostprocessorAndProviderCapabilities(t *testing.T) {
	postprocess, err := (Client{}).Postprocess(context.Background(), helperConfig("multipurpose"), plugin.PostprocessRequest{
		ID: "post-one", Operation: "normalize", Input: plugin.Artifact{Handle: "host-artifact-1", MediaType: "video/mp4"},
	})
	if err != nil || len(postprocess.Artifacts) != 1 || postprocess.Artifacts[0].Handle != "host-artifact-2" {
		t.Fatalf("postprocess response, error = %#v, %v", postprocess, err)
	}
	provider, err := (Client{}).Provide(context.Background(), helperConfig("multipurpose"), plugin.ProviderRequest{
		ID: "provider-one", Provider: "fixture", Action: "resolve",
	})
	if err != nil || provider.Values["status"] != "ok" {
		t.Fatalf("provider response, error = %#v, %v", provider, err)
	}
}

func TestRPCMalformedCrashAndOversize(t *testing.T) {
	tests := []struct {
		mode string
		want error
	}{
		{"malformed", plugin.ErrMalformedMessage},
		{"crash", plugin.ErrCrashed},
		{"oversize", plugin.ErrResourceLimit},
	}
	for _, test := range tests {
		t.Run(test.mode, func(t *testing.T) {
			if _, err := (Client{}).Extract(context.Background(), helperConfig(test.mode), request()); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRPCCrashDoesNotExposeStderrSecrets(t *testing.T) {
	_, err := (Client{}).Extract(context.Background(), helperConfig("crash-secret"), request())
	if !errors.Is(err, plugin.ErrCrashed) || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("crash error = %v", err)
	}
}

func TestRPCCancellationAndTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	if _, err := (Client{}).Extract(ctx, helperConfig("hang"), request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	config := helperConfig("hang")
	config.Limits.Timeout = 20 * time.Millisecond
	if _, err := (Client{}).Extract(context.Background(), config, request()); !errors.Is(err, plugin.ErrTimeout) {
		t.Fatalf("timeout error = %v", err)
	}
}

func TestRPCRejectsSecretArgumentsEnvironmentAndPython(t *testing.T) {
	trampoline := filepath.Join(t.TempDir(), "innocent-plugin")
	if err := os.WriteFile(trampoline, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		config Config
		want   error
	}{
		{"argument", helperConfig("success"), plugin.ErrSecretExposure},
		{"environment", helperConfig("success"), plugin.ErrSecretExposure},
		{"python", helperConfig("success"), plugin.ErrPythonRuntime},
		{"interpreter trampoline", helperConfig("success"), plugin.ErrUntrustedPath},
	}
	tests[0].config.Args = append(tests[0].config.Args, "--token=fixture-secret")
	tests[1].config.Environment = map[string]string{"LANG": "token=fixture-secret"}
	tests[2].config.Executable = filepath.Join(filepath.Dir(os.Args[0]), "python3")
	tests[3].config.Executable = trampoline
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := (Client{}).Extract(context.Background(), test.config, request()); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

type recordingApprover struct {
	mu      sync.Mutex
	request plugin.ApprovalRequest
}

func (approver *recordingApprover) Approve(_ context.Context, request plugin.ApprovalRequest) (plugin.Approval, error) {
	approver.mu.Lock()
	defer approver.mu.Unlock()
	approver.request = request
	return plugin.Approval{Granted: append([]plugin.Permission(nil), request.Requested...)}, nil
}

func TestRPCPermissionChangeApprovalIsIdentityBound(t *testing.T) {
	approver := &recordingApprover{}
	config := helperConfig("permission-approved")
	config.Approver = approver
	config.PreviousPermissions = []plugin.Permission{plugin.PermissionNetwork}
	response, err := (Client{}).Extract(context.Background(), config, request())
	if err != nil || response.ID != "one" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	if approver.request.PluginID != "fixture.rpc" || approver.request.Release != "1.1.0" ||
		approver.request.ABI != plugin.ProtocolV1_1 || len(approver.request.Added) != 1 ||
		approver.request.Added[0] != plugin.PermissionCookies {
		t.Fatalf("approval request = %#v", approver.request)
	}
}

func TestRPCTrustedPackageLaunch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("secure discovery fails closed until Windows ACL ownership verification is available")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "fixture")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(directory, "fixture-plugin")
	copyExecutable(t, os.Args[0], executable)
	manifest := stableManifest("fixture-plugin", plugin.PermissionNetwork)
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := plugin.LoadPackage(root, directory, 0)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Signer = "fixture-signing-key"
	approver := &recordingApprover{}
	config := helperConfig("trusted")
	config.Package = &loaded
	config.Executable = ""
	config.UnsafeTestOnly = false
	config.Approver = approver
	config.PreviousPermissions = []plugin.Permission{plugin.PermissionNetwork}
	response, err := (Client{}).Extract(context.Background(), config, request())
	if err != nil || response.ID != "one" {
		t.Fatalf("response, error = %#v, %v", response, err)
	}
	approver.mu.Lock()
	defer approver.mu.Unlock()
	if approver.request.Signer != "fixture-signing-key" || approver.request.ExecutableDigest != loaded.ExecutableDigest ||
		approver.request.PluginID != loaded.Manifest.ID || approver.request.Release != loaded.Manifest.Release ||
		approver.request.ABI != plugin.ProtocolV1_1 || !reflect.DeepEqual(approver.request.Requested, loaded.Manifest.Permissions) {
		t.Fatalf("identity-bound approval = %#v", approver.request)
	}
	manifest.Release = "1.1.1"
	mutated, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "plugin.json"), mutated, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := (Client{}).Extract(context.Background(), config, request()); !errors.Is(err, plugin.ErrUntrustedPath) {
		t.Fatalf("pre-launch mutation error = %v", err)
	}
}

func TestRPCProcessTreeCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		// Windows Job Object behavior is cross-built here and exercised by CI on
		// a native Windows runner; Unix process groups are exercised locally.
		t.Skip("requires native Windows runner")
	}
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	done := filepath.Join(directory, "done")
	config := helperConfig("tree")
	config.Args = append(config.Args, ready, done)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(ready); err == nil {
				cancel()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
	}()
	if _, err := (Client{}).Extract(ctx, config, request()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	time.Sleep(650 * time.Millisecond)
	if _, err := os.Stat(done); !os.IsNotExist(err) {
		t.Fatalf("grandchild survived process-tree cancellation: %v", err)
	}
}

func FuzzReadFrame(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		var message envelope
		_ = readFrame(bytesReader(data), 1024, &message)
	})
}

func helperConfig(mode string) Config {
	return Config{
		Executable:     os.Args[0],
		UnsafeTestOnly: true,
		Args:           []string{"-test.run=TestRPCPluginHelper", "--", mode},
		Limits: plugin.Limits{
			Timeout:         time.Second,
			CancelGrace:     20 * time.Millisecond,
			MaxMessageBytes: 1024,
		},
	}
}

func stableManifest(entrypoint string, permissions ...plugin.Permission) plugin.Manifest {
	return plugin.Manifest{
		Schema: plugin.ManifestSchema, ID: "fixture.rpc", Name: "RPC fixture", Release: "1.1.0",
		Runtime: "native", Entrypoint: entrypoint,
		ABIRange:     plugin.VersionRange{Minimum: plugin.ProtocolV1_0, Maximum: plugin.ProtocolV1_1},
		Capabilities: []plugin.Capability{plugin.CapabilityExtractor}, Permissions: permissions,
	}
}

func copyExecutable(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.Link(source, destination); err == nil {
		return
	}
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func request() plugin.ExtractRequest {
	return plugin.ExtractRequest{ID: "one", URL: "https://fixture.invalid/video"}
}

func TestRPCPluginHelper(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	if mode == "" {
		return
	}
	var hello envelope
	if err := readFrame(os.Stdin, 1<<20, &hello); err != nil {
		os.Exit(10)
	}
	if mode == "crash" || mode == "crash-secret" {
		message := "fixture crash"
		if mode == "crash-secret" {
			message = "token=fixture-secret"
		}
		_, _ = fmt.Fprint(os.Stderr, message)
		os.Exit(12)
	}
	if mode == "malformed" {
		_, _ = os.Stdout.Write([]byte{0, 0, 0, 1, '{'})
		return
	}
	if mode == "oversize" {
		var header [4]byte
		binary.BigEndian.PutUint32(header[:], 1025)
		_, _ = os.Stdout.Write(header[:])
		return
	}
	manifest := stableManifest("fixture-plugin")
	if mode == "version" {
		manifest.ABIRange = plugin.VersionRange{Minimum: 99, Maximum: 99}
	}
	if mode == "python-manifest" {
		manifest.Runtime = "python"
	}
	if mode == "permission" {
		manifest.Permissions = []plugin.Permission{plugin.PermissionSecrets}
	}
	if mode == "permission-approved" {
		manifest.Permissions = []plugin.Permission{plugin.PermissionNetwork, plugin.PermissionCookies}
	}
	if mode == "trusted" {
		manifest.Permissions = []plugin.Permission{plugin.PermissionNetwork}
	}
	if mode == "multipurpose" {
		manifest.Capabilities = []plugin.Capability{
			plugin.CapabilityExtractor, plugin.CapabilityPostprocessor, plugin.CapabilityProvider,
		}
	}
	if err := writeFrame(os.Stdout, envelope{Type: "hello", Manifest: &manifest}, 1<<20); err != nil {
		os.Exit(11)
	}
	if mode == "version" || mode == "permission" || mode == "python-manifest" {
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}
	var extract envelope
	if err := readFrame(os.Stdin, 1<<20, &extract); err != nil {
		os.Exit(13)
	}
	if mode == "hang" {
		var cancel envelope
		_ = readFrame(os.Stdin, 1<<20, &cancel)
		return
	}
	if mode == "tree" {
		separator := 0
		for index, argument := range os.Args {
			if argument == "--" {
				separator = index
				break
			}
		}
		if separator == 0 || separator+3 >= len(os.Args) {
			os.Exit(17)
		}
		child := exec.Command(os.Args[0], "-test.run=TestRPCGrandchildHelper", "--", "grandchild", os.Args[separator+2], os.Args[separator+3])
		child.Env = []string{}
		if err := child.Start(); err != nil {
			os.Exit(18)
		}
		var cancel envelope
		_ = readFrame(os.Stdin, 1<<20, &cancel)
		time.Sleep(time.Second)
		return
	}
	if mode == "remote" {
		response := plugin.ExtractResponse{ID: extract.Request.ID, Error: &plugin.RemoteError{Category: plugin.RemoteUnavailable, Message: "fixture unavailable"}}
		_ = writeFrame(os.Stdout, envelope{Type: "result", Response: &response}, 1<<20)
		return
	}
	if mode == "multipurpose" && extract.Type == "postprocess" {
		response := plugin.PostprocessResponse{ID: extract.PostprocessRequest.ID, Artifacts: []plugin.Artifact{{Handle: "host-artifact-2", MediaType: "video/mp4"}}}
		_ = writeFrame(os.Stdout, envelope{Type: "postprocess_result", PostprocessResponse: &response}, 1<<20)
		return
	}
	if mode == "multipurpose" && extract.Type == "provide" {
		response := plugin.ProviderResponse{ID: extract.ProviderRequest.ID, Values: map[string]any{"status": "ok"}}
		_ = writeFrame(os.Stdout, envelope{Type: "provider_result", ProviderResponse: &response}, 1<<20)
		return
	}
	response := plugin.ExtractResponse{ID: extract.Request.ID, Metadata: map[string]any{"id": "fixture", "title": "RPC fixture", "abi": extract.Version}}
	if err := writeFrame(os.Stdout, envelope{Type: "result", Response: &response}, 1<<20); err != nil {
		os.Exit(14)
	}
}

func TestRPCGrandchildHelper(t *testing.T) {
	separator := 0
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator == 0 || separator+3 >= len(os.Args) || os.Args[separator+1] != "grandchild" {
		return
	}
	if err := os.WriteFile(os.Args[separator+2], []byte("ready"), 0600); err != nil {
		os.Exit(21)
	}
	time.Sleep(500 * time.Millisecond)
	if err := os.WriteFile(os.Args[separator+3], []byte("survived"), 0600); err != nil {
		os.Exit(22)
	}
}
