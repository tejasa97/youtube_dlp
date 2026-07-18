// Package plugin implements validation and host policy for pluginapi.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

var sensitiveDiagnostic = regexp.MustCompile(`(?i)(authorization|cookie|password|secret|signature|token|sig|key)([=:][^&[:space:]]+)`)

const (
	ProtocolVersion = pluginapi.Current
	ProtocolV1_0    = pluginapi.V1_0
	ProtocolV1_1    = pluginapi.V1_1
	ManifestSchema  = "ytdlp-go.plugin/v1"
)

var (
	ErrInvalidConfig        = errors.New("invalid plugin configuration")
	ErrInvalidManifest      = errors.New("invalid plugin manifest")
	ErrIncompatibleVersion  = errors.New("incompatible plugin protocol version")
	ErrPermissionDenied     = errors.New("plugin permission denied")
	ErrPermissionReview     = errors.New("plugin permission review required")
	ErrResourceLimit        = errors.New("plugin resource limit exceeded")
	ErrMalformedMessage     = errors.New("malformed plugin message")
	ErrCrashed              = errors.New("plugin crashed")
	ErrTimeout              = errors.New("plugin timed out")
	ErrUntrustedPath        = errors.New("untrusted plugin path")
	ErrPythonRuntime        = errors.New("Python plugin runtime is prohibited")
	ErrSecretExposure       = errors.New("plugin secret exposure prohibited")
	ErrIsolationUnavailable = errors.New("required plugin isolation is unavailable")
)

type Permission = pluginapi.Permission

const (
	PermissionNetwork         = pluginapi.PermissionNetwork
	PermissionCookies         = pluginapi.PermissionCookies
	PermissionSecrets         = pluginapi.PermissionSecrets
	PermissionFilesystemRead  = pluginapi.PermissionFilesystemRead
	PermissionFilesystemWrite = pluginapi.PermissionFilesystemWrite
	PermissionProcess         = pluginapi.PermissionProcess
)

type Capability = pluginapi.Capability

const (
	CapabilityExtractor     = pluginapi.CapabilityExtractor
	CapabilityPostprocessor = pluginapi.CapabilityPostprocessor
	CapabilityProvider      = pluginapi.CapabilityProvider
)

type Manifest = pluginapi.Manifest
type VersionRange = pluginapi.VersionRange
type ExtractRequest = pluginapi.ExtractRequest
type ExtractResponse = pluginapi.ExtractResponse
type PostprocessRequest = pluginapi.PostprocessRequest
type PostprocessResponse = pluginapi.PostprocessResponse
type ProviderRequest = pluginapi.ProviderRequest
type ProviderResponse = pluginapi.ProviderResponse
type Artifact = pluginapi.Artifact
type ApprovalRequest = pluginapi.ApprovalRequest
type Approval = pluginapi.Approval
type PermissionApprover = pluginapi.PermissionApprover

type RemoteCategory = pluginapi.RemoteCategory

const (
	RemoteAuthentication  = pluginapi.RemoteAuthentication
	RemoteUnavailable     = pluginapi.RemoteUnavailable
	RemoteInvalidMetadata = pluginapi.RemoteInvalidMetadata
	RemoteNetwork         = pluginapi.RemoteNetwork
	RemotePermission      = pluginapi.RemotePermission
	RemoteInvalidInput    = pluginapi.RemoteInvalidInput
	RemoteInternal        = pluginapi.RemoteInternal
)

type RemoteError = pluginapi.RemoteError

type Limits struct {
	Timeout          time.Duration
	CancelGrace      time.Duration
	MaxMessageBytes  uint32
	MaxStderrBytes   int
	MemoryLimitPages uint32
}

func (limits Limits) WithDefaults() Limits {
	if limits.Timeout <= 0 {
		limits.Timeout = 10 * time.Second
	}
	if limits.CancelGrace <= 0 {
		limits.CancelGrace = 250 * time.Millisecond
	}
	if limits.MaxMessageBytes == 0 {
		limits.MaxMessageBytes = 1 << 20
	}
	if limits.MaxStderrBytes <= 0 {
		limits.MaxStderrBytes = 64 << 10
	}
	if limits.MemoryLimitPages == 0 {
		limits.MemoryLimitPages = 256
	}
	return limits
}

func (limits Limits) Validate() error {
	limits = limits.WithDefaults()
	if limits.Timeout > 10*time.Minute || limits.CancelGrace > 30*time.Second ||
		limits.MaxMessageBytes > 16<<20 || limits.MaxStderrBytes > 1<<20 ||
		limits.MemoryLimitPages > 65536 {
		return fmt.Errorf("%w: limits exceed ABI hard caps", ErrInvalidConfig)
	}
	return nil
}

// Negotiate retains the Phase 1 enumerated-version API.
func Negotiate(supported, offered []uint32) (uint32, error) {
	var selected uint32
	for _, host := range supported {
		for _, candidate := range offered {
			if host == candidate && (selected == 0 || pluginapi.CompareVersions(host, selected) > 0) {
				selected = host
			}
		}
	}
	if selected == 0 {
		return 0, fmt.Errorf("%w: host=%v plugin=%v", ErrIncompatibleVersion, supported, offered)
	}
	return selected, nil
}

// NegotiateRange selects the newest mutually supported minor in one major.
func NegotiateRange(host, candidate VersionRange) (uint32, error) {
	if err := validateRange(host); err != nil {
		return 0, err
	}
	if err := validateRange(candidate); err != nil {
		return 0, err
	}
	minimum := host.Minimum
	if pluginapi.CompareVersions(candidate.Minimum, minimum) > 0 {
		minimum = candidate.Minimum
	}
	maximum := host.Maximum
	if pluginapi.CompareVersions(candidate.Maximum, maximum) < 0 {
		maximum = candidate.Maximum
	}
	if !pluginapi.Compatible(minimum, maximum) || pluginapi.CompareVersions(minimum, maximum) > 0 {
		return 0, fmt.Errorf("%w: host=%v plugin=%v", ErrIncompatibleVersion, host, candidate)
	}
	return maximum, nil
}

func validateRange(value VersionRange) error {
	minimumMajor, _ := pluginapi.VersionParts(value.Minimum)
	maximumMajor, _ := pluginapi.VersionParts(value.Maximum)
	if minimumMajor == 0 || minimumMajor != maximumMajor || pluginapi.CompareVersions(value.Minimum, value.Maximum) > 0 {
		return fmt.Errorf("%w: invalid ABI range", ErrIncompatibleVersion)
	}
	return nil
}

func CheckPermissions(required, granted []Permission) error {
	allowed := make(map[Permission]struct{}, len(granted))
	for _, permission := range granted {
		allowed[permission] = struct{}{}
	}
	for _, permission := range required {
		if _, ok := allowed[permission]; !ok {
			return fmt.Errorf("%w: %s", ErrPermissionDenied, permission)
		}
	}
	return nil
}

func AddedPermissions(previous, requested []Permission) []Permission {
	old := make(map[Permission]struct{}, len(previous))
	for _, permission := range previous {
		old[permission] = struct{}{}
	}
	var added []Permission
	for _, permission := range requested {
		if _, ok := old[permission]; !ok {
			added = append(added, permission)
		}
	}
	sort.Slice(added, func(i, j int) bool { return added[i] < added[j] })
	return added
}

// Approve asks the host policy for an exact permission set. The approval is
// operation-scoped and bound to the immutable descriptor fields.
func Approve(ctx context.Context, approver PermissionApprover, request ApprovalRequest, static []Permission) error {
	granted := static
	if approver != nil {
		approval, err := approver.Approve(ctx, request)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPermissionDenied, err)
		}
		granted = approval.Granted
	} else if len(request.Added) != 0 {
		if err := CheckPermissions(request.Requested, static); err != nil {
			return fmt.Errorf("%w: %w: %v", ErrPermissionDenied, ErrPermissionReview, request.Added)
		}
	}
	return CheckPermissions(request.Requested, granted)
}

type RemoteFailure struct{ Detail RemoteError }

func (failure *RemoteFailure) Error() string {
	return fmt.Sprintf("plugin %s error: %s", failure.Detail.Category, RedactDiagnostic(failure.Detail.Message))
}

// RedactDiagnostic removes conventional credential values from untrusted
// plugin text before it is rendered in errors or logs.
func RedactDiagnostic(input string) string {
	return sensitiveDiagnostic.ReplaceAllString(input, "$1=REDACTED")
}

func ResponseError(remote *RemoteError) error {
	if remote == nil {
		return nil
	}
	switch remote.Category {
	case RemoteAuthentication, RemoteUnavailable, RemoteInvalidMetadata, RemoteNetwork,
		RemotePermission, RemoteInvalidInput, RemoteInternal:
	default:
		return fmt.Errorf("%w: unknown remote error category", ErrMalformedMessage)
	}
	if strings.TrimSpace(remote.Message) == "" || len(remote.Message) > 4096 || len(remote.Code) > 128 {
		return fmt.Errorf("%w: invalid remote error", ErrMalformedMessage)
	}
	return &RemoteFailure{Detail: *remote}
}

func ResponseErrorLegacy(response ExtractResponse) error { return ResponseError(response.Error) }
