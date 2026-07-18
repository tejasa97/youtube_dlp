// Package plugin implements validation and host policy for pluginapi.
package plugin

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

var sensitiveDiagnostic = regexp.MustCompile(`(?i)\b(authorization|cookie|password|secret|signature|token|sig|api[_-]?key|key)([=:][^&[:space:]]+)`)
var remoteCodePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9_.-]{0,127})$`)

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
	Timeout         time.Duration
	CancelGrace     time.Duration
	MaxMessageBytes uint32
	MaxStderrBytes  int
	// MemoryLimitPages is enforced by the WASM host only. The native RPC
	// transport does not claim a portable address-space limit.
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
	if limits.Timeout < 0 || limits.CancelGrace < 0 || limits.MaxStderrBytes < 0 {
		return fmt.Errorf("%w: limits cannot be negative", ErrInvalidConfig)
	}
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
	minimumMajor, minimumMinor := pluginapi.VersionParts(value.Minimum)
	maximumMajor, maximumMinor := pluginapi.VersionParts(value.Maximum)
	if minimumMajor == 0 || minimumMajor != maximumMajor || pluginapi.CompareVersions(value.Minimum, value.Maximum) > 0 {
		return fmt.Errorf("%w: invalid ABI range", ErrIncompatibleVersion)
	}
	if pluginapi.Version(minimumMajor, minimumMinor) != value.Minimum || pluginapi.Version(maximumMajor, maximumMinor) != value.Maximum {
		return fmt.Errorf("%w: non-canonical ABI version", ErrIncompatibleVersion)
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
	if err := validatePermissionSet(request.Requested); err != nil {
		return err
	}
	granted := static
	if approver != nil {
		approval, err := approver.Approve(ctx, request)
		if err != nil {
			return fmt.Errorf("%w: %s", ErrPermissionDenied, RedactDiagnostic(err.Error()))
		}
		granted = approval.Granted
	} else if len(request.Added) != 0 && !samePermissions(request.Requested, static) {
		return fmt.Errorf("%w: %w: %v", ErrPermissionDenied, ErrPermissionReview, request.Added)
	}
	if err := validatePermissionSet(granted); err != nil {
		return err
	}
	if !samePermissions(request.Requested, granted) {
		return fmt.Errorf("%w: approval must grant the exact requested set", ErrPermissionDenied)
	}
	return nil
}

func validatePermissionSet(permissions []Permission) error {
	seen := make(map[Permission]struct{}, len(permissions))
	for _, permission := range permissions {
		switch permission {
		case PermissionNetwork, PermissionCookies, PermissionSecrets, PermissionFilesystemRead,
			PermissionFilesystemWrite, PermissionProcess:
		default:
			return fmt.Errorf("%w: unknown permission %q", ErrPermissionDenied, permission)
		}
		if _, duplicate := seen[permission]; duplicate {
			return fmt.Errorf("%w: duplicate permission %q", ErrPermissionDenied, permission)
		}
		seen[permission] = struct{}{}
	}
	return nil
}

func samePermissions(left, right []Permission) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[Permission]int, len(left))
	for _, permission := range left {
		counts[permission]++
	}
	for _, permission := range right {
		counts[permission]--
	}
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
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
	if strings.TrimSpace(remote.Message) == "" || len(remote.Message) > 4096 ||
		remote.Code != "" && !remoteCodePattern.MatchString(remote.Code) {
		return fmt.Errorf("%w: invalid remote error", ErrMalformedMessage)
	}
	detail := *remote
	detail.Message = RedactDiagnostic(detail.Message)
	return &RemoteFailure{Detail: detail}
}

func ResponseErrorLegacy(response ExtractResponse) error { return ResponseError(response.Error) }

// CheckPayload rejects conventional secret-bearing keys and values from
// ordinary plugin maps. Secrets cross a separate host capability channel as
// opaque handles; they are not metadata or provider values.
func CheckPayload(value any) error {
	nodes := 0
	var visit func(reflect.Value, int) error
	visit = func(current reflect.Value, depth int) error {
		if depth > 64 {
			return fmt.Errorf("%w: payload nesting", ErrResourceLimit)
		}
		nodes++
		if nodes > 10000 {
			return fmt.Errorf("%w: payload nodes", ErrResourceLimit)
		}
		if !current.IsValid() {
			return nil
		}
		for current.Kind() == reflect.Interface || current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return nil
			}
			current = current.Elem()
			if depth++; depth > 64 {
				return fmt.Errorf("%w: payload nesting", ErrResourceLimit)
			}
		}
		switch current.Kind() {
		case reflect.Map:
			if current.Type().Key().Kind() != reflect.String {
				return fmt.Errorf("%w: plugin payload maps require string keys", ErrInvalidConfig)
			}
			iterator := current.MapRange()
			for iterator.Next() {
				if sensitivePayloadKey(iterator.Key().String()) {
					return ErrSecretExposure
				}
				if err := visit(iterator.Value(), depth+1); err != nil {
					return err
				}
			}
		case reflect.Slice, reflect.Array:
			for index := 0; index < current.Len(); index++ {
				if err := visit(current.Index(index), depth+1); err != nil {
					return err
				}
			}
		case reflect.Struct:
			for index := 0; index < current.NumField(); index++ {
				field := current.Type().Field(index)
				if field.PkgPath != "" {
					continue
				}
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name == "-" {
					continue
				}
				if name == "" {
					name = field.Name
				}
				if sensitivePayloadKey(name) {
					return ErrSecretExposure
				}
				if err := visit(current.Field(index), depth+1); err != nil {
					return err
				}
			}
		case reflect.String:
			if typed := current.String(); RedactDiagnostic(typed) != typed {
				return ErrSecretExposure
			}
		case reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return fmt.Errorf("%w: unsupported plugin payload value", ErrInvalidConfig)
		}
		return nil
	}
	return visit(reflect.ValueOf(value), 0)
}

func sensitivePayloadKey(key string) bool {
	lower := strings.ToLower(key)
	for _, sensitive := range []string{"authorization", "cookie", "password", "secret", "signature", "token", "api_key"} {
		if lower == sensitive || strings.HasSuffix(lower, "_"+sensitive) {
			return true
		}
	}
	return false
}
