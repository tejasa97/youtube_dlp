package pack

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	SchemaVersion           = 1
	maxFiles                = 256
	maxPermissions          = 32
	maxPathBytes            = 240
	maxManifestBytes        = 256 << 10
	maxSignatureBytes       = 16 << 10
	maxFileBytes      int64 = 32 << 20
	maxPayloadBytes         = 64 << 20
	maxArchiveBytes         = 72 << 20
)

const signatureDomain = "ytdlp-go/plugin-pack/v1\x00"

type Permission string

const (
	PermissionNetwork         Permission = "network"
	PermissionCookies         Permission = "cookies"
	PermissionSecrets         Permission = "secrets"
	PermissionFilesystemRead  Permission = "filesystem_read"
	PermissionFilesystemWrite Permission = "filesystem_write"
)

type Runtime string

const (
	RuntimeRPC  Runtime = "rpc"
	RuntimeWASM Runtime = "wasm"
)

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Mode   uint32 `json:"mode"`
}

type Manifest struct {
	SchemaVersion  int          `json:"schema_version"`
	Name           string       `json:"name"`
	Version        string       `json:"version"`
	Runtime        Runtime      `json:"runtime"`
	Entrypoint     string       `json:"entrypoint"`
	PublisherKeyID string       `json:"publisher_key_id"`
	CreatedAt      string       `json:"created_at"`
	ExpiresAt      string       `json:"expires_at"`
	MinHostVersion string       `json:"min_host_version,omitempty"`
	Permissions    []Permission `json:"permissions,omitempty"`
	Files          []File       `json:"files"`
}

type Payload struct {
	Bytes []byte
	Mode  uint32
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Value     string `json:"value"`
}

// KeyID derives a collision-resistant identifier from an Ed25519 public key.
// Callers cannot select an unrelated key ID and thereby create a TOFU path.
func KeyID(publicKey ed25519.PublicKey) (string, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return "", fmt.Errorf("%w: invalid Ed25519 public key", ErrInvalidManifest)
	}
	digest := sha256.Sum256(publicKey)
	return "ed25519:" + hex.EncodeToString(digest[:]), nil
}

func normalizeManifest(manifest Manifest) (Manifest, []byte, error) {
	manifest.Permissions = append([]Permission(nil), manifest.Permissions...)
	manifest.Files = append([]File(nil), manifest.Files...)
	sort.Slice(manifest.Permissions, func(i, j int) bool { return manifest.Permissions[i] < manifest.Permissions[j] })
	sort.Slice(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path })
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, nil, err
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: encode canonical manifest", ErrInvalidManifest)
	}
	if len(encoded) > maxManifestBytes {
		return Manifest{}, nil, ErrResourceLimit
	}
	return manifest, encoded, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: unsupported schema", ErrInvalidManifest)
	}
	if !validName(manifest.Name) {
		return fmt.Errorf("%w: invalid package name", ErrInvalidManifest)
	}
	if _, err := parseVersion(manifest.Version); err != nil {
		return err
	}
	if manifest.MinHostVersion != "" {
		if _, err := parseVersion(manifest.MinHostVersion); err != nil {
			return err
		}
	}
	if manifest.Runtime != RuntimeRPC && manifest.Runtime != RuntimeWASM {
		return fmt.Errorf("%w: unsupported runtime", ErrInvalidManifest)
	}
	if manifest.PublisherKeyID == "" || len(manifest.PublisherKeyID) != len("ed25519:")+sha256.Size*2 || !strings.HasPrefix(manifest.PublisherKeyID, "ed25519:") {
		return fmt.Errorf("%w: invalid publisher key ID", ErrInvalidManifest)
	}
	if _, err := hex.DecodeString(strings.TrimPrefix(manifest.PublisherKeyID, "ed25519:")); err != nil {
		return fmt.Errorf("%w: invalid publisher key ID", ErrInvalidManifest)
	}
	created, err := parseTimestamp(manifest.CreatedAt)
	if err != nil {
		return err
	}
	expires, err := parseTimestamp(manifest.ExpiresAt)
	if err != nil || !expires.After(created) {
		return fmt.Errorf("%w: invalid expiration", ErrInvalidManifest)
	}
	if len(manifest.Permissions) > maxPermissions || len(manifest.Files) == 0 || len(manifest.Files) > maxFiles {
		return ErrResourceLimit
	}
	seenPermissions := make(map[Permission]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		switch permission {
		case PermissionNetwork, PermissionCookies, PermissionSecrets, PermissionFilesystemRead, PermissionFilesystemWrite:
		default:
			return fmt.Errorf("%w: unknown permission", ErrInvalidManifest)
		}
		if _, exists := seenPermissions[permission]; exists {
			return fmt.Errorf("%w: duplicate permission", ErrInvalidManifest)
		}
		seenPermissions[permission] = struct{}{}
	}
	var total int64
	seenFiles := make(map[string]struct{}, len(manifest.Files))
	seenPortableFiles := make(map[string]struct{}, len(manifest.Files))
	for _, file := range manifest.Files {
		if err := validatePayloadPath(file.Path); err != nil {
			return err
		}
		if _, exists := seenFiles[file.Path]; exists {
			return fmt.Errorf("%w: duplicate payload path", ErrInvalidManifest)
		}
		seenFiles[file.Path] = struct{}{}
		portable := strings.ToLower(file.Path)
		if _, exists := seenPortableFiles[portable]; exists {
			return fmt.Errorf("%w: case-folded payload path collision", ErrInvalidManifest)
		}
		seenPortableFiles[portable] = struct{}{}
		if file.Size < 0 || file.Size > maxFileBytes || total > maxPayloadBytes-file.Size {
			return ErrResourceLimit
		}
		total += file.Size
		if len(file.SHA256) != sha256.Size*2 {
			return fmt.Errorf("%w: invalid payload digest", ErrInvalidManifest)
		}
		if _, err := hex.DecodeString(file.SHA256); err != nil {
			return fmt.Errorf("%w: invalid payload digest", ErrInvalidManifest)
		}
		if file.Mode != 0o600 && file.Mode != 0o700 {
			return fmt.Errorf("%w: unsafe payload mode", ErrInvalidManifest)
		}
	}
	if err := validatePayloadPath(manifest.Entrypoint); err != nil {
		return fmt.Errorf("%w: invalid entrypoint", ErrInvalidManifest)
	}
	_, exists := seenFiles[manifest.Entrypoint]
	if !exists {
		return fmt.Errorf("%w: entrypoint is not in payload", ErrInvalidManifest)
	}
	if manifest.Runtime == RuntimeRPC {
		for _, file := range manifest.Files {
			if file.Path == manifest.Entrypoint && file.Mode != 0o700 {
				return fmt.Errorf("%w: RPC entrypoint is not executable", ErrInvalidManifest)
			}
		}
	}
	return nil
}

func parseTimestamp(input string) (time.Time, error) {
	if input == "" || len(input) > len(time.RFC3339Nano)+6 || strings.TrimSpace(input) != input {
		return time.Time{}, fmt.Errorf("%w: invalid timestamp", ErrInvalidManifest)
	}
	parsed, err := time.Parse(time.RFC3339, input)
	if err != nil || parsed.UTC().Format(time.RFC3339) != input {
		return time.Time{}, fmt.Errorf("%w: timestamps must be canonical UTC RFC3339", ErrInvalidManifest)
	}
	return parsed, nil
}

func validName(input string) bool {
	if input == "" || len(input) > 80 || !utf8.ValidString(input) || input[0] < 'a' || input[0] > 'z' {
		return false
	}
	for _, character := range input {
		if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' || character == '_') {
			return false
		}
	}
	return true
}

func validatePayloadPath(input string) error {
	if input == "" || len(input) > maxPathBytes || !utf8.ValidString(input) || strings.IndexByte(input, 0) >= 0 || strings.Contains(input, "\\") || strings.Contains(input, ":") {
		return ErrUnsafePath
	}
	if strings.HasPrefix(input, "/") || path.Clean(input) != input || input == "." || strings.HasPrefix(input, "../") || strings.Contains(input, "/../") || strings.HasPrefix(input, ".ytdlp-pack.") || strings.Contains(input, "/.ytdlp-pack.") {
		return ErrUnsafePath
	}
	for _, part := range strings.Split(input, "/") {
		if part == "" || part == "." || part == ".." || strings.HasSuffix(part, ".") {
			return ErrUnsafePath
		}
		for _, character := range part {
			if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-') {
				return ErrUnsafePath
			}
		}
		base := strings.ToUpper(strings.SplitN(part, ".", 2)[0])
		switch base {
		case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
			return ErrUnsafePath
		}
	}
	return nil
}
