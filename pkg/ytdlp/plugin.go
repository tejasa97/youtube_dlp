package ytdlp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/pack"
	"github.com/ytdlp-go/ytdlp/internal/plugin"
	"github.com/ytdlp-go/ytdlp/internal/plugin/rpc"
	pluginwasm "github.com/ytdlp-go/ytdlp/internal/plugin/wasm"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

type PluginPermission = pluginapi.Permission
type PluginCapability = pluginapi.Capability
type PluginManifest = pluginapi.Manifest
type PluginApprovalRequest = pluginapi.ApprovalRequest
type PluginApproval = pluginapi.Approval
type PluginExtractRequest = pluginapi.ExtractRequest
type PluginExtractResponse = pluginapi.ExtractResponse
type PluginPostprocessRequest = pluginapi.PostprocessRequest
type PluginPostprocessResponse = pluginapi.PostprocessResponse
type PluginProviderRequest = pluginapi.ProviderRequest
type PluginProviderResponse = pluginapi.ProviderResponse

type PluginPermissionApprover = pluginapi.PermissionApprover

type PluginPermissionApproveFunc func(context.Context, PluginApprovalRequest) (PluginApproval, error)

func (function PluginPermissionApproveFunc) Approve(ctx context.Context, request PluginApprovalRequest) (PluginApproval, error) {
	return function(ctx, request)
}

const (
	PluginPermissionNetwork         = pluginapi.PermissionNetwork
	PluginPermissionCookies         = pluginapi.PermissionCookies
	PluginPermissionSecrets         = pluginapi.PermissionSecrets
	PluginPermissionFilesystemRead  = pluginapi.PermissionFilesystemRead
	PluginPermissionFilesystemWrite = pluginapi.PermissionFilesystemWrite
	PluginPermissionProcess         = pluginapi.PermissionProcess
)

type PluginLimits struct {
	Timeout          time.Duration
	CancelGrace      time.Duration
	MaxMessageBytes  uint32
	MaxStderrBytes   int
	MemoryLimitPages uint32
}

type PackRevocation = pack.PackageRevocation
type PackRevocations = pack.Revocations
type PackPermissionReview = pack.PermissionReview
type PackState = pack.State

type PluginPackTrust struct {
	Keys           map[string]ed25519.PublicKey
	Now            time.Time
	HostVersion    string
	CurrentVersion string
	Revocations    PackRevocations
}

type PluginPackInstallOptions struct {
	ApprovePermissionIncrease bool
}

type PluginDescriptor struct {
	ID               string
	Name             string
	Release          string
	Runtime          string
	SignerKeyID      string
	ExecutableDigest string
	Capabilities     []PluginCapability
	Permissions      []PluginPermission
}

// InstalledPlugin is an opaque, revalidated binding between a signed pack and
// its ABI manifest. Callers cannot construct one from an unsigned directory.
type InstalledPlugin struct {
	packageValue plugin.Package
	descriptor   PluginDescriptor
}

func (installed *InstalledPlugin) Descriptor() PluginDescriptor {
	if installed == nil {
		return PluginDescriptor{}
	}
	result := installed.descriptor
	result.Capabilities = append([]PluginCapability(nil), result.Capabilities...)
	result.Permissions = append([]PluginPermission(nil), result.Permissions...)
	return result
}

func WithInstalledPlugins(installed ...*InstalledPlugin) Option {
	return func(client *Client) {
		client.plugins = append([]*InstalledPlugin(nil), installed...)
	}
}

func WithPluginPermissionApprover(approver PluginPermissionApprover) Option {
	return func(client *Client) { client.pluginApprover = approver }
}

func InstallPluginPack(ctx context.Context, archive []byte, root string, trust PluginPackTrust, options PluginPackInstallOptions) (*InstalledPlugin, PackPermissionReview, error) {
	policy := packPolicy(trust)
	verified, err := pack.Verify(archive, policy)
	if err != nil {
		return nil, PackPermissionReview{}, categorizePack("verify plugin pack", err)
	}
	manifest, err := validatePluginPackBinding(verified)
	if err != nil {
		return nil, PackPermissionReview{}, categorizePlugin("validate plugin pack", err)
	}
	receipt, err := pack.Install(ctx, archive, root, policy, pack.InstallOptions{ApprovePermissionIncrease: options.ApprovePermissionIncrease})
	if err != nil {
		return nil, receipt.Review, categorizePack("install plugin pack", err)
	}
	installed, err := bindInstalledPlugin(receipt.Path, receipt.Verified.Manifest.PublisherKeyID, manifest)
	if err != nil {
		return nil, receipt.Review, categorizePlugin("bind installed plugin", err)
	}
	return installed, receipt.Review, nil
}

type PluginPackRollbackOptions struct {
	ApprovePermissionIncrease bool
}

func RollbackPluginPack(ctx context.Context, root, name string, trust PluginPackTrust, options PluginPackRollbackOptions) (*InstalledPlugin, PackPermissionReview, error) {
	var validatedManifest plugin.Manifest
	receipt, err := pack.RollbackWithOptions(ctx, root, name, packPolicy(trust), pack.RollbackOptions{
		ApprovePermissionIncrease: options.ApprovePermissionIncrease,
		Validate: func(verified pack.Verified) error {
			manifest, err := validatePluginPackBinding(verified)
			validatedManifest = manifest
			return err
		},
	})
	if err != nil {
		return nil, receipt.Review, categorizePluginPackOperation("roll back plugin pack", err)
	}
	installed, err := bindInstalledPlugin(receipt.Path, receipt.Verified.Manifest.PublisherKeyID, validatedManifest)
	if err != nil {
		return nil, receipt.Review, categorizePlugin("bind rolled-back plugin", err)
	}
	return installed, receipt.Review, nil
}

type PluginPackRemoveOptions struct {
	ActivatePrevious          bool
	ApprovePermissionIncrease bool
}

func RemovePluginPack(ctx context.Context, root, name, version string, trust PluginPackTrust, options PluginPackRemoveOptions) (PackState, PackPermissionReview, error) {
	receipt, err := pack.Remove(ctx, root, name, version, packPolicy(trust), pack.RemoveOptions{
		ActivatePrevious: options.ActivatePrevious, ApprovePermissionIncrease: options.ApprovePermissionIncrease,
		ValidateReplacement: func(verified pack.Verified) error {
			_, err := validatePluginPackBinding(verified)
			return err
		},
	})
	return receipt.State, receipt.Review, categorizePluginPackOperation("remove plugin pack", err)
}

func packPolicy(trust PluginPackTrust) pack.VerifyPolicy {
	keys := make(map[string]ed25519.PublicKey, len(trust.Keys))
	for keyID, key := range trust.Keys {
		keys[keyID] = append(ed25519.PublicKey(nil), key...)
	}
	return pack.VerifyPolicy{
		Trust: keys, Now: trust.Now, HostVersion: trust.HostVersion,
		CurrentVersion: trust.CurrentVersion,
		Revocations: pack.Revocations{
			KeyIDs:         append([]string(nil), trust.Revocations.KeyIDs...),
			ManifestSHA256: append([]string(nil), trust.Revocations.ManifestSHA256...),
			Packages:       append([]pack.PackageRevocation(nil), trust.Revocations.Packages...),
		},
	}
}

func validatePluginPackBinding(verified pack.Verified) (plugin.Manifest, error) {
	manifestBody, exists := verified.Payload["plugin.json"]
	if !exists {
		return plugin.Manifest{}, fmt.Errorf("%w: signed pack does not contain plugin.json", plugin.ErrInvalidManifest)
	}
	manifest, err := plugin.DecodeManifest(bytes.NewReader(manifestBody), int64(len(manifestBody)))
	if err != nil {
		return plugin.Manifest{}, err
	}
	expectedRuntime := pluginapi.RuntimeNative
	if verified.Manifest.Runtime == pack.RuntimeWASM {
		expectedRuntime = pluginapi.RuntimeWASM
	}
	if manifest.Runtime != expectedRuntime || manifest.Release != verified.Manifest.Version || manifest.Entrypoint != verified.Manifest.Entrypoint {
		return plugin.Manifest{}, fmt.Errorf("%w: signed pack and ABI identity differ", plugin.ErrInvalidManifest)
	}
	permissions := make([]pluginapi.Permission, len(verified.Manifest.Permissions))
	for index, permission := range verified.Manifest.Permissions {
		permissions[index] = pluginapi.Permission(permission)
	}
	if !samePluginPermissions(manifest.Permissions, permissions) {
		return plugin.Manifest{}, fmt.Errorf("%w: signed pack and ABI permissions differ", plugin.ErrInvalidManifest)
	}
	return manifest, nil
}

func samePluginPermissions(left, right []pluginapi.Permission) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]pluginapi.Permission(nil), left...), append([]pluginapi.Permission(nil), right...)
	sortPermissions(leftCopy)
	sortPermissions(rightCopy)
	return reflect.DeepEqual(leftCopy, rightCopy)
}

func sortPermissions(values []pluginapi.Permission) {
	for index := 1; index < len(values); index++ {
		for cursor := index; cursor > 0 && values[cursor] < values[cursor-1]; cursor-- {
			values[cursor], values[cursor-1] = values[cursor-1], values[cursor]
		}
	}
}

func bindInstalledPlugin(path, signer string, expected plugin.Manifest) (*InstalledPlugin, error) {
	loaded, err := plugin.LoadPackage(filepath.Dir(path), path, 0)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(loaded.Manifest, expected) {
		return nil, fmt.Errorf("%w: installed manifest changed", plugin.ErrInvalidManifest)
	}
	loaded.Signer = signer
	return &InstalledPlugin{
		packageValue: loaded,
		descriptor: PluginDescriptor{
			ID: expected.ID, Name: expected.Name, Release: expected.Release, Runtime: string(expected.Runtime),
			SignerKeyID: signer, ExecutableDigest: loaded.ExecutableDigest,
			Capabilities: append([]PluginCapability(nil), expected.Capabilities...),
			Permissions:  append([]PluginPermission(nil), expected.Permissions...),
		},
	}, nil
}

type PluginHost struct {
	installed *InstalledPlugin
	approver  PluginPermissionApprover
	limits    PluginLimits
}

func NewPluginHost(installed *InstalledPlugin, approver PluginPermissionApprover, limits PluginLimits) (*PluginHost, error) {
	if installed == nil || approver == nil {
		return nil, &Error{Category: ErrorInvalidInput, Op: "configure plugin host", Err: plugin.ErrInvalidConfig}
	}
	return &PluginHost{installed: installed, approver: approver, limits: limits}, nil
}

func (host *PluginHost) Extract(ctx context.Context, request PluginExtractRequest) (PluginExtractResponse, error) {
	if host == nil || host.installed == nil {
		return PluginExtractResponse{}, &Error{Category: ErrorInvalidInput, Op: "extract with plugin", Err: plugin.ErrInvalidConfig}
	}
	response, err := runPluginExtract(ctx, host.installed, host.approver, host.limits, request)
	return response, categorizePlugin("extract with plugin", err)
}

func (host *PluginHost) Postprocess(ctx context.Context, request PluginPostprocessRequest) (PluginPostprocessResponse, error) {
	if host == nil || host.installed == nil || host.installed.packageValue.Manifest.Runtime != pluginapi.RuntimeNative {
		return PluginPostprocessResponse{}, &Error{Category: ErrorUnsupported, Op: "postprocess with plugin", Err: plugin.ErrInvalidConfig}
	}
	response, err := (rpc.Client{}).Postprocess(ctx, rpc.Config{Package: &host.installed.packageValue, Approver: host.approver, Limits: internalPluginLimits(host.limits)}, request)
	return response, categorizePlugin("postprocess with plugin", err)
}

func (host *PluginHost) Provide(ctx context.Context, request PluginProviderRequest) (PluginProviderResponse, error) {
	if host == nil || host.installed == nil || host.installed.packageValue.Manifest.Runtime != pluginapi.RuntimeNative {
		return PluginProviderResponse{}, &Error{Category: ErrorUnsupported, Op: "invoke plugin provider", Err: plugin.ErrInvalidConfig}
	}
	response, err := (rpc.Client{}).Provide(ctx, rpc.Config{Package: &host.installed.packageValue, Approver: host.approver, Limits: internalPluginLimits(host.limits)}, request)
	return response, categorizePlugin("invoke plugin provider", err)
}

func internalPluginLimits(limits PluginLimits) plugin.Limits {
	return plugin.Limits{
		Timeout: limits.Timeout, CancelGrace: limits.CancelGrace,
		MaxMessageBytes: limits.MaxMessageBytes, MaxStderrBytes: limits.MaxStderrBytes,
		MemoryLimitPages: limits.MemoryLimitPages,
	}
}

func runPluginExtract(ctx context.Context, installed *InstalledPlugin, approver PluginPermissionApprover, limits PluginLimits, request PluginExtractRequest) (PluginExtractResponse, error) {
	if installed == nil || approver == nil {
		return PluginExtractResponse{}, plugin.ErrInvalidConfig
	}
	if installed.packageValue.Manifest.Runtime == pluginapi.RuntimeWASM {
		module, err := os.ReadFile(installed.packageValue.EntrypointPath)
		if err != nil {
			return PluginExtractResponse{}, plugin.ErrUntrustedPath
		}
		return (pluginwasm.Host{}).Extract(ctx, module, pluginwasm.Config{
			Package: &installed.packageValue, Manifest: installed.packageValue.Manifest,
			Approver: approver, Limits: internalPluginLimits(limits),
		}, request)
	}
	return (rpc.Client{}).Extract(ctx, rpc.Config{Package: &installed.packageValue, Approver: approver, Limits: internalPluginLimits(limits)}, request)
}

type installedPluginExtractor struct {
	installed *InstalledPlugin
	approver  PluginPermissionApprover
}

func (candidate *installedPluginExtractor) Name() string {
	return candidate.installed.packageValue.Manifest.ID
}
func (*installedPluginExtractor) Suitable(*url.URL) bool { return false }

func (candidate *installedPluginExtractor) Extract(ctx context.Context, request extractor.Request) (extractor.Extraction, error) {
	response, err := runPluginExtract(ctx, candidate.installed, candidate.approver, PluginLimits{}, pluginapi.ExtractRequest{ID: "one", URL: request.URL})
	if err != nil {
		return extractor.Extraction{}, mapPluginExtractorError(err)
	}
	encoded, err := json.Marshal(response.Metadata)
	if err != nil {
		return extractor.Extraction{}, extractor.ErrInvalidMetadata
	}
	var metadata value.Value
	if err := json.Unmarshal(encoded, &metadata); err != nil {
		return extractor.Extraction{}, extractor.ErrInvalidMetadata
	}
	object, ok := metadata.Object()
	if !ok {
		return extractor.Extraction{}, extractor.ErrInvalidMetadata
	}
	return extractor.Media(value.NewInfo(object)), nil
}

func mapPluginExtractorError(err error) error {
	var remote *plugin.RemoteFailure
	if errors.As(err, &remote) {
		switch remote.Detail.Category {
		case plugin.RemoteAuthentication:
			return fmt.Errorf("%w: plugin authentication", extractor.ErrAuthentication)
		case plugin.RemoteUnavailable:
			return fmt.Errorf("%w: plugin unavailable", extractor.ErrUnavailable)
		case plugin.RemoteInvalidMetadata:
			return fmt.Errorf("%w: plugin metadata", extractor.ErrInvalidMetadata)
		}
	}
	return err
}

func categorizePlugin(op string, err error) error {
	if err == nil {
		return nil
	}
	category := ErrorInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, plugin.ErrIncompatibleVersion), errors.Is(err, plugin.ErrPythonRuntime), errors.Is(err, plugin.ErrIsolationUnavailable):
		category = ErrorUnsupported
	case errors.Is(err, plugin.ErrPermissionDenied), errors.Is(err, plugin.ErrPermissionReview),
		errors.Is(err, plugin.ErrUntrustedPath), errors.Is(err, plugin.ErrSecretExposure):
		category = ErrorSecurity
	case errors.Is(err, plugin.ErrInvalidConfig), errors.Is(err, plugin.ErrInvalidManifest),
		errors.Is(err, plugin.ErrResourceLimit), errors.Is(err, plugin.ErrMalformedMessage):
		category = ErrorInvalidInput
	}
	return &Error{Category: category, Op: op, Err: err}
}

func categorizePack(op string, err error) error {
	if err == nil {
		return nil
	}
	category := ErrorInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, pack.ErrPlatformSecurity), errors.Is(err, pack.ErrIncompatibleHost):
		category = ErrorUnsupported
	case errors.Is(err, pack.ErrUntrustedPublisher), errors.Is(err, pack.ErrSignature),
		errors.Is(err, pack.ErrRevoked), errors.Is(err, pack.ErrExpired), errors.Is(err, pack.ErrNotYetValid),
		errors.Is(err, pack.ErrDowngrade), errors.Is(err, pack.ErrPermissionReview), errors.Is(err, pack.ErrUnsafePath),
		errors.Is(err, pack.ErrCorruptInstall):
		category = ErrorSecurity
	case errors.Is(err, pack.ErrInvalidManifest), errors.Is(err, pack.ErrInvalidArchive),
		errors.Is(err, pack.ErrInvalidRevocations), errors.Is(err, pack.ErrResourceLimit),
		errors.Is(err, pack.ErrAlreadyInstalled), errors.Is(err, pack.ErrNotInstalled):
		category = ErrorInvalidInput
	}
	return &Error{Category: category, Op: op, Err: err}
}

func categorizePluginPackOperation(op string, err error) error {
	if errors.Is(err, plugin.ErrInvalidConfig) || errors.Is(err, plugin.ErrInvalidManifest) ||
		errors.Is(err, plugin.ErrPythonRuntime) || errors.Is(err, plugin.ErrUntrustedPath) {
		return categorizePlugin(op, err)
	}
	return categorizePack(op, err)
}
