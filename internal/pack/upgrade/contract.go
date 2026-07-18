// Package upgrade defines the signed compatibility contract used when an
// extractor pack moves from contract v1.0 to the additive v1.1 schema.
package upgrade

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/pack"
)

const (
	ContractMajor   = 1
	ContractMinor   = 1
	MaxContractSize = 256 << 10
	maxCapabilities = 64
	maxPermissions  = 32
	signatureDomain = "ytdlp-go/extractor-pack-contract/v1\x00"
)

var (
	ErrInvalidContract   = errors.New("invalid extractor pack contract")
	ErrUnsupportedMajor  = errors.New("unsupported extractor pack contract major")
	ErrIncompatibleHost  = errors.New("extractor pack contract is incompatible with host")
	ErrMissingCapability = errors.New("required extractor pack capability is missing")
	ErrDowngrade         = errors.New("extractor pack downgrade rejected")
	ErrPythonRuntime     = errors.New("Python extractor pack runtime rejected")
	ErrSignature         = errors.New("invalid extractor pack contract signature")
	ErrUntrustedKey      = errors.New("untrusted extractor pack contract key")
	ErrPermissionReview  = errors.New("extractor pack permission increase requires review")
	ErrResourceLimit     = errors.New("extractor pack contract resource limit exceeded")
)

type ContractVersion struct {
	Major int `json:"major"`
	Minor int `json:"minor"`
}

type Runtime string

const (
	RuntimeRPC  Runtime = "rpc"
	RuntimeWASM Runtime = "wasm"
)

// Manifest v1.1 adds RequiresHost, RequiredProvides, and Annotations. Their
// omission is the canonical v1.0 representation.
type Manifest struct {
	ContractVersion  ContractVersion   `json:"contract_version"`
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	Runtime          Runtime           `json:"runtime"`
	Entrypoint       string            `json:"entrypoint"`
	Permissions      []pack.Permission `json:"permissions,omitempty"`
	Provides         []string          `json:"provides"`
	RequiresHost     []string          `json:"requires_host,omitempty"`
	RequiredProvides []string          `json:"required_provides,omitempty"`
	Annotations      map[string]string `json:"annotations,omitempty"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

type signedRecord struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature Signature       `json:"signature"`
}

type Host struct {
	ContractVersion          ContractVersion
	Capabilities             []string
	RequiredPackCapabilities []string
}

type Policy struct {
	Host                      Host
	TrustedKeys               map[string]ed25519.PublicKey
	Current                   *Manifest
	ApprovePermissionIncrease bool
}

type Result struct {
	Manifest       Manifest
	ManifestSHA256 string
	KeyID          string
	Review         pack.PermissionReview
}

// Sign creates canonical deterministic bytes. Production code supplies its
// key explicitly; the package contains no embedded private key or key lookup.
func Sign(ctx context.Context, manifest Manifest, privateKey ed25519.PrivateKey) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, ErrSignature
	}
	canonical, manifestBytes, err := normalizeManifest(manifest)
	if err != nil {
		return nil, err
	}
	_ = canonical
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID := keyID(publicKey)
	signature := ed25519.Sign(privateKey, append([]byte(signatureDomain), manifestBytes...))
	record := signedRecord{Manifest: manifestBytes, Signature: Signature{Algorithm: "Ed25519", KeyID: keyID, Value: base64.StdEncoding.EncodeToString(signature)}}
	encoded, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("%w: encode", ErrInvalidContract)
	}
	if len(encoded) > MaxContractSize {
		return nil, ErrResourceLimit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// VerifyAndNegotiate authenticates a canonical record before applying the
// host/pack matrix, downgrade, capability, and permission-review policies.
func VerifyAndNegotiate(ctx context.Context, encoded []byte, policy Policy) (Result, error) {
	var result Result
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if len(encoded) == 0 || len(encoded) > MaxContractSize {
		return result, ErrResourceLimit
	}
	encoded = bytes.TrimSuffix(encoded, []byte("\n"))
	var record signedRecord
	if err := decodeStrict(encoded, &record); err != nil {
		return result, fmt.Errorf("%w: signed record", ErrInvalidContract)
	}
	canonicalRecord, err := json.Marshal(record)
	if err != nil || !bytes.Equal(encoded, canonicalRecord) {
		return result, fmt.Errorf("%w: non-canonical signed record", ErrInvalidContract)
	}
	var manifest Manifest
	if err := decodeStrict(record.Manifest, &manifest); err != nil {
		return result, fmt.Errorf("%w: manifest", ErrInvalidContract)
	}
	canonical, manifestBytes, err := normalizeManifest(manifest)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(record.Manifest, manifestBytes) {
		return result, fmt.Errorf("%w: non-canonical manifest", ErrInvalidContract)
	}
	if record.Signature.Algorithm != "Ed25519" {
		return result, ErrSignature
	}
	publicKey, trusted := policy.TrustedKeys[record.Signature.KeyID]
	if !trusted || len(publicKey) != ed25519.PublicKeySize || keyID(publicKey) != record.Signature.KeyID {
		return result, ErrUntrustedKey
	}
	signature, err := base64.StdEncoding.Strict().DecodeString(record.Signature.Value)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, append([]byte(signatureDomain), manifestBytes...), signature) {
		return result, ErrSignature
	}
	digest := sha256.Sum256(manifestBytes)
	result = Result{Manifest: canonical, ManifestSHA256: hex.EncodeToString(digest[:]), KeyID: record.Signature.KeyID}
	if err := negotiate(ctx, &result, policy); err != nil {
		return result, err
	}
	return result, nil
}

func negotiate(ctx context.Context, result *Result, policy Policy) error {
	manifest := result.Manifest
	if err := validateHost(policy.Host); err != nil {
		return err
	}
	if policy.Current != nil {
		if err := validateManifest(*policy.Current); err != nil {
			return fmt.Errorf("%w: current manifest", err)
		}
	}
	if policy.Host.ContractVersion.Major != ContractMajor || manifest.ContractVersion.Major != ContractMajor {
		return ErrUnsupportedMajor
	}
	if policy.Host.ContractVersion.Minor < 0 || policy.Host.ContractVersion.Minor > ContractMinor || manifest.ContractVersion.Minor < 0 || manifest.ContractVersion.Minor > ContractMinor {
		return ErrIncompatibleHost
	}
	hostCapabilities := stringSet(policy.Host.Capabilities)
	for _, capability := range manifest.RequiresHost {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, available := hostCapabilities[capability]; !available {
			return ErrIncompatibleHost
		}
	}
	provided := stringSet(manifest.Provides)
	required := append([]string(nil), policy.Host.RequiredPackCapabilities...)
	if policy.Current != nil {
		comparison, err := compareSemver(manifest.Version, policy.Current.Version)
		if err != nil {
			return err
		}
		if comparison <= 0 {
			return ErrDowngrade
		}
		required = append(required, policy.Current.RequiredProvides...)
		result.Review = pack.ReviewPermissions(policy.Current.Permissions, manifest.Permissions)
	} else {
		result.Review = pack.ReviewPermissions(nil, manifest.Permissions)
	}
	for _, capability := range required {
		if _, available := provided[capability]; !available {
			return ErrMissingCapability
		}
	}
	if result.Review.Increase() && !policy.ApprovePermissionIncrease {
		return ErrPermissionReview
	}
	return ctx.Err()
}

func validateHost(host Host) error {
	if host.ContractVersion.Major != ContractMajor {
		return ErrUnsupportedMajor
	}
	if host.ContractVersion.Minor < 0 || host.ContractVersion.Minor > ContractMinor {
		return ErrIncompatibleHost
	}
	if len(host.Capabilities) > maxCapabilities || len(host.RequiredPackCapabilities) > maxCapabilities {
		return ErrResourceLimit
	}
	for _, capabilities := range [][]string{host.Capabilities, host.RequiredPackCapabilities} {
		seen := make(map[string]struct{}, len(capabilities))
		for _, capability := range capabilities {
			if !validCapability(capability) {
				return ErrInvalidContract
			}
			if _, duplicate := seen[capability]; duplicate {
				return ErrInvalidContract
			}
			seen[capability] = struct{}{}
		}
	}
	return nil
}

func normalizeManifest(input Manifest) (Manifest, []byte, error) {
	manifest := input
	manifest.Permissions = append([]pack.Permission(nil), input.Permissions...)
	manifest.Provides = append([]string(nil), input.Provides...)
	manifest.RequiresHost = append([]string(nil), input.RequiresHost...)
	manifest.RequiredProvides = append([]string(nil), input.RequiredProvides...)
	if input.Annotations != nil {
		manifest.Annotations = make(map[string]string, len(input.Annotations))
		for key, value := range input.Annotations {
			manifest.Annotations[key] = value
		}
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, nil, err
	}
	sort.Slice(manifest.Permissions, func(i, j int) bool { return manifest.Permissions[i] < manifest.Permissions[j] })
	sort.Strings(manifest.Provides)
	sort.Strings(manifest.RequiresHost)
	sort.Strings(manifest.RequiredProvides)
	encoded, err := json.Marshal(manifest)
	if err != nil || len(encoded) > MaxContractSize {
		return Manifest{}, nil, ErrResourceLimit
	}
	return manifest, encoded, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.ContractVersion.Major != ContractMajor {
		return ErrUnsupportedMajor
	}
	if manifest.ContractVersion.Minor < 0 || manifest.ContractVersion.Minor > ContractMinor {
		return ErrIncompatibleHost
	}
	if !validIdentifier(manifest.Name, 80) || !validPath(manifest.Entrypoint) {
		return ErrInvalidContract
	}
	if _, err := parseSemver(manifest.Version); err != nil {
		return err
	}
	if manifest.Runtime == "python" || strings.HasSuffix(strings.ToLower(manifest.Entrypoint), ".py") {
		return ErrPythonRuntime
	}
	if manifest.Runtime != RuntimeRPC && manifest.Runtime != RuntimeWASM {
		return ErrInvalidContract
	}
	if len(manifest.Permissions) > maxPermissions || len(manifest.Provides) == 0 || len(manifest.Provides) > maxCapabilities || len(manifest.RequiresHost) > maxCapabilities || len(manifest.RequiredProvides) > maxCapabilities || len(manifest.Annotations) > maxCapabilities {
		return ErrResourceLimit
	}
	if manifest.ContractVersion.Minor == 0 && (len(manifest.RequiresHost) != 0 || len(manifest.RequiredProvides) != 0 || len(manifest.Annotations) != 0) {
		return fmt.Errorf("%w: v1.1 field in v1.0 contract", ErrInvalidContract)
	}
	seenPermissions := make(map[pack.Permission]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		switch permission {
		case pack.PermissionNetwork, pack.PermissionCookies, pack.PermissionSecrets, pack.PermissionFilesystemRead, pack.PermissionFilesystemWrite:
		default:
			return ErrInvalidContract
		}
		if _, duplicate := seenPermissions[permission]; duplicate {
			return ErrInvalidContract
		}
		seenPermissions[permission] = struct{}{}
	}
	for _, capabilities := range [][]string{manifest.Provides, manifest.RequiresHost, manifest.RequiredProvides} {
		seen := make(map[string]struct{}, len(capabilities))
		for _, capability := range capabilities {
			if !validCapability(capability) {
				return ErrInvalidContract
			}
			if _, duplicate := seen[capability]; duplicate {
				return ErrInvalidContract
			}
			seen[capability] = struct{}{}
		}
	}
	provided := stringSet(manifest.Provides)
	for _, capability := range manifest.RequiredProvides {
		if _, exists := provided[capability]; !exists {
			return ErrMissingCapability
		}
	}
	for key, value := range manifest.Annotations {
		if !validIdentifier(key, 64) || len(value) > 256 || strings.IndexByte(value, 0) >= 0 {
			return ErrInvalidContract
		}
	}
	return nil
}

func validIdentifier(input string, maximum int) bool {
	if input == "" || len(input) > maximum || input[0] < 'a' || input[0] > 'z' {
		return false
	}
	for _, character := range input {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validCapability(input string) bool {
	return validIdentifier(input, 64) && strings.Contains(input, ".")
}

func validPath(input string) bool {
	return input != "" && len(input) <= 240 && !strings.ContainsAny(input, "\\\x00:") && !strings.HasPrefix(input, "/") && path.Clean(input) == input && input != "." && !strings.HasPrefix(input, "../")
}

func keyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return "ed25519:" + hex.EncodeToString(digest[:])
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func stringSet(input []string) map[string]struct{} {
	result := make(map[string]struct{}, len(input))
	for _, value := range input {
		result[value] = struct{}{}
	}
	return result
}
