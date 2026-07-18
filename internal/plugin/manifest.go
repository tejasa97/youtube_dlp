package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const defaultMaximumManifestBytes int64 = 256 << 10
const maximumEntrypointBytes int64 = 512 << 20

var (
	pluginIDPattern       = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)
	releasePattern        = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$`)
	pythonEntrypoint      = regexp.MustCompile(`(?i)(^|[._-])(python|pypy)([0-9.]*)($|[._-])|\.py[wc]?$`)
	interpreterEntrypoint = regexp.MustCompile(`(?i)^(?:sh|bash|dash|zsh|fish|cmd|powershell|pwsh|perl|ruby|node|deno|bun)(?:\.exe)?$|\.(?:bat|cmd|ps1|sh)$`)
)

type Package struct {
	Manifest         Manifest
	Root             string
	Directory        string
	ManifestPath     string
	EntrypointPath   string
	ManifestDigest   string
	ExecutableDigest string
	// Signer is populated only by a verified signed-pack loader. Discovery
	// never derives trust from a self-asserted manifest field.
	Signer string
}

func DecodeManifest(reader io.Reader, maximum int64) (Manifest, error) {
	if maximum <= 0 {
		maximum = defaultMaximumManifestBytes
	}
	limited := io.LimitReader(reader, maximum+1)
	payload, err := io.ReadAll(limited)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: read: %v", ErrInvalidManifest, err)
	}
	if int64(len(payload)) > maximum {
		return Manifest{}, fmt.Errorf("%w: manifest exceeds %d bytes", ErrResourceLimit, maximum)
	}
	if err := rejectDuplicateJSONKeys(payload); err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: decode: %v", ErrInvalidManifest, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Manifest{}, fmt.Errorf("%w: trailing JSON", ErrInvalidManifest)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ValidateManifest(manifest Manifest) error {
	if manifest.Schema != ManifestSchema || !pluginIDPattern.MatchString(manifest.ID) ||
		strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 128 ||
		!releasePattern.MatchString(manifest.Release) {
		return fmt.Errorf("%w: missing or invalid identity", ErrInvalidManifest)
	}
	if manifest.Runtime != pluginapi.RuntimeNative && manifest.Runtime != pluginapi.RuntimeWASM {
		if strings.Contains(strings.ToLower(string(manifest.Runtime)), "python") {
			return ErrPythonRuntime
		}
		return fmt.Errorf("%w: unsupported runtime %q", ErrInvalidManifest, manifest.Runtime)
	}
	if manifest.Entrypoint == "" || filepath.IsAbs(manifest.Entrypoint) ||
		filepath.Base(manifest.Entrypoint) != manifest.Entrypoint ||
		manifest.Entrypoint == "." || manifest.Entrypoint == ".." {
		return fmt.Errorf("%w: entrypoint must be one package-relative filename", ErrInvalidManifest)
	}
	if pythonEntrypoint.MatchString(manifest.Entrypoint) {
		return ErrPythonRuntime
	}
	if interpreterEntrypoint.MatchString(manifest.Entrypoint) {
		return fmt.Errorf("%w: interpreter entrypoint", ErrInvalidManifest)
	}
	rangeValue := manifest.ABIRange
	if rangeValue.Minimum == 0 && rangeValue.Maximum == 0 && len(manifest.Versions) != 0 {
		rangeValue.Minimum, rangeValue.Maximum = manifest.Versions[0], manifest.Versions[0]
		for _, version := range manifest.Versions[1:] {
			if !pluginapi.Compatible(rangeValue.Minimum, version) {
				return fmt.Errorf("%w: versions cross ABI majors", ErrInvalidManifest)
			}
			if pluginapi.CompareVersions(version, rangeValue.Minimum) < 0 {
				rangeValue.Minimum = version
			}
			if pluginapi.CompareVersions(version, rangeValue.Maximum) > 0 {
				rangeValue.Maximum = version
			}
		}
	}
	if err := validateRange(rangeValue); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if major, _ := pluginapi.VersionParts(rangeValue.Minimum); major != pluginapi.ABIMajor {
		return fmt.Errorf("%w: unsupported ABI major %d", ErrIncompatibleVersion, major)
	}
	if len(manifest.Capabilities) == 0 || len(manifest.Capabilities) > 16 || len(manifest.Permissions) > 16 {
		return fmt.Errorf("%w: invalid capability or permission count", ErrInvalidManifest)
	}
	seenCapabilities := make(map[Capability]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		switch capability {
		case CapabilityExtractor, CapabilityPostprocessor, CapabilityProvider:
		default:
			return fmt.Errorf("%w: unknown capability %q", ErrInvalidManifest, capability)
		}
		if _, duplicate := seenCapabilities[capability]; duplicate {
			return fmt.Errorf("%w: duplicate capability %q", ErrInvalidManifest, capability)
		}
		seenCapabilities[capability] = struct{}{}
	}
	seenPermissions := make(map[Permission]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		switch permission {
		case PermissionNetwork, PermissionCookies, PermissionSecrets, PermissionFilesystemRead,
			PermissionFilesystemWrite, PermissionProcess:
		default:
			return fmt.Errorf("%w: unknown permission %q", ErrInvalidManifest, permission)
		}
		if _, duplicate := seenPermissions[permission]; duplicate {
			return fmt.Errorf("%w: duplicate permission %q", ErrInvalidManifest, permission)
		}
		seenPermissions[permission] = struct{}{}
	}
	return nil
}

func ManifestRange(manifest Manifest) VersionRange {
	if manifest.ABIRange.Minimum != 0 || manifest.ABIRange.Maximum != 0 {
		return manifest.ABIRange
	}
	result := VersionRange{Minimum: manifest.Versions[0], Maximum: manifest.Versions[0]}
	for _, version := range manifest.Versions[1:] {
		if pluginapi.CompareVersions(version, result.Minimum) < 0 {
			result.Minimum = version
		}
		if pluginapi.CompareVersions(version, result.Maximum) > 0 {
			result.Maximum = version
		}
	}
	return result
}

func HasCapability(manifest Manifest, expected Capability) bool {
	for _, capability := range manifest.Capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

type DiscoveryConfig struct {
	TrustedRoots         []string
	MaximumManifestBytes int64
}

// Discover scans only direct package children of explicitly configured roots.
// It never consults cwd, PATH, HOME, or environment variables.
func Discover(config DiscoveryConfig) ([]Package, error) {
	if len(config.TrustedRoots) == 0 {
		return nil, fmt.Errorf("%w: no trusted roots", ErrInvalidConfig)
	}
	seen := make(map[string]struct{}, len(config.TrustedRoots))
	var packages []Package
	for _, configured := range config.TrustedRoots {
		root, err := canonicalTrustedRoot(configured)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[root]; duplicate {
			continue
		}
		seen[root] = struct{}{}
		entries, err := os.ReadDir(root)
		if err != nil {
			return nil, fmt.Errorf("%w: read root: %v", ErrUntrustedPath, err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			candidate := filepath.Join(root, entry.Name())
			if _, err := os.Lstat(filepath.Join(candidate, "plugin.json")); err != nil {
				if os.IsNotExist(err) {
					continue
				}
				return nil, fmt.Errorf("%w: inspect manifest: %v", ErrUntrustedPath, err)
			}
			loaded, err := LoadPackage(root, candidate, config.MaximumManifestBytes)
			if err != nil {
				return nil, err
			}
			packages = append(packages, loaded)
		}
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].Manifest.ID < packages[j].Manifest.ID })
	for index := 1; index < len(packages); index++ {
		if packages[index-1].Manifest.ID == packages[index].Manifest.ID {
			return nil, fmt.Errorf("%w: duplicate plugin id %q", ErrInvalidManifest, packages[index].Manifest.ID)
		}
	}
	return packages, nil
}

func LoadPackage(root, directory string, maximum int64) (Package, error) {
	canonicalRoot, err := canonicalTrustedRoot(root)
	if err != nil {
		return Package{}, err
	}
	absDirectory, err := filepath.Abs(directory)
	if err != nil {
		return Package{}, fmt.Errorf("%w: package path: %v", ErrUntrustedPath, err)
	}
	canonicalDirectory, err := filepath.EvalSymlinks(absDirectory)
	if err != nil {
		return Package{}, fmt.Errorf("%w: canonicalize package: %v", ErrUntrustedPath, err)
	}
	relative, err := filepath.Rel(canonicalRoot, canonicalDirectory)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return Package{}, fmt.Errorf("%w: package escapes trusted root", ErrUntrustedPath)
	}
	info, err := os.Lstat(absDirectory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Package{}, fmt.Errorf("%w: package is not a real directory", ErrUntrustedPath)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return Package{}, fmt.Errorf("%w: package is group/world writable", ErrUntrustedPath)
	}
	if err := verifyTrustedOwner(info); err != nil {
		return Package{}, err
	}
	absDirectory = canonicalDirectory
	manifestPath := filepath.Join(absDirectory, "plugin.json")
	manifestInfo, err := os.Lstat(manifestPath)
	if err != nil || !manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0 {
		return Package{}, fmt.Errorf("%w: manifest is not a regular file", ErrUntrustedPath)
	}
	if runtime.GOOS != "windows" && manifestInfo.Mode().Perm()&0022 != 0 {
		return Package{}, fmt.Errorf("%w: manifest is group/world writable", ErrUntrustedPath)
	}
	if err := verifyTrustedOwner(manifestInfo); err != nil {
		return Package{}, err
	}
	file, err := os.Open(manifestPath)
	if err != nil {
		return Package{}, fmt.Errorf("%w: open manifest: %v", ErrUntrustedPath, err)
	}
	manifest, decodeErr := DecodeManifest(file, maximum)
	closeErr := file.Close()
	if decodeErr != nil {
		return Package{}, decodeErr
	}
	if closeErr != nil {
		return Package{}, fmt.Errorf("%w: close manifest: %v", ErrInvalidManifest, closeErr)
	}
	entrypoint := filepath.Join(absDirectory, manifest.Entrypoint)
	entryInfo, err := os.Lstat(entrypoint)
	if err != nil || !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 {
		return Package{}, fmt.Errorf("%w: entrypoint is not a regular file", ErrUntrustedPath)
	}
	if entryInfo.Size() > maximumEntrypointBytes {
		return Package{}, fmt.Errorf("%w: entrypoint exceeds %d bytes", ErrResourceLimit, maximumEntrypointBytes)
	}
	if runtime.GOOS != "windows" && entryInfo.Mode().Perm()&0022 != 0 {
		return Package{}, fmt.Errorf("%w: entrypoint is group/world writable", ErrUntrustedPath)
	}
	if err := verifyTrustedOwner(entryInfo); err != nil {
		return Package{}, err
	}
	if manifest.Runtime == pluginapi.RuntimeNative && runtime.GOOS != "windows" && entryInfo.Mode().Perm()&0111 == 0 {
		return Package{}, fmt.Errorf("%w: native entrypoint is not executable", ErrUntrustedPath)
	}
	if manifest.Runtime == pluginapi.RuntimeNative {
		if err := rejectInterpreterFile(entrypoint); err != nil {
			return Package{}, err
		}
	}
	manifestDigest, err := digestFile(manifestPath)
	if err != nil {
		return Package{}, err
	}
	executableDigest, err := digestFile(entrypoint)
	if err != nil {
		return Package{}, err
	}
	return Package{
		Manifest: manifest, Root: canonicalRoot, Directory: absDirectory,
		ManifestPath: manifestPath, EntrypointPath: entrypoint,
		ManifestDigest: manifestDigest, ExecutableDigest: executableDigest,
	}, nil
}

func canonicalTrustedRoot(configured string) (string, error) {
	if configured == "" || !filepath.IsAbs(configured) {
		return "", fmt.Errorf("%w: root must be absolute", ErrUntrustedPath)
	}
	info, err := os.Lstat(configured)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%w: root is not a real directory", ErrUntrustedPath)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0022 != 0 {
		return "", fmt.Errorf("%w: root is group/world writable", ErrUntrustedPath)
	}
	if err := verifyTrustedOwner(info); err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(configured)
	if err != nil {
		return "", fmt.Errorf("%w: canonicalize root: %v", ErrUntrustedPath, err)
	}
	return filepath.Clean(canonical), nil
}

func digestFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("%w: digest: %v", ErrUntrustedPath, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("%w: digest stat: %v", ErrUntrustedPath, err)
	}
	if info.Size() > maximumEntrypointBytes {
		return "", fmt.Errorf("%w: file exceeds %d bytes", ErrResourceLimit, maximumEntrypointBytes)
	}
	hash := sha256.New()
	written, err := io.CopyN(hash, file, maximumEntrypointBytes+1)
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("%w: digest: %v", ErrUntrustedPath, err)
	}
	if written > maximumEntrypointBytes {
		return "", fmt.Errorf("%w: file grew beyond %d bytes", ErrResourceLimit, maximumEntrypointBytes)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// RevalidatePackage minimizes the discovery-to-exec mutation window by
// repeating canonical path, ownership, mode, manifest, and digest validation
// immediately before process creation. The returned descriptor contains the
// freshly observed paths and digests.
func RevalidatePackage(expected Package) (Package, error) {
	observed, err := LoadPackage(expected.Root, expected.Directory, 0)
	if err != nil {
		return Package{}, err
	}
	if !reflect.DeepEqual(expected.Manifest, observed.Manifest) ||
		expected.ManifestDigest != observed.ManifestDigest ||
		expected.ExecutableDigest != observed.ExecutableDigest ||
		expected.EntrypointPath != observed.EntrypointPath {
		return Package{}, fmt.Errorf("%w: plugin package changed after discovery", ErrUntrustedPath)
	}
	observed.Signer = expected.Signer
	return observed, nil
}

func rejectInterpreterFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%w: inspect entrypoint: %v", ErrUntrustedPath, err)
	}
	defer file.Close()
	header := make([]byte, 256)
	read, err := file.Read(header)
	if err != nil && err != io.EOF {
		return fmt.Errorf("%w: inspect entrypoint: %v", ErrUntrustedPath, err)
	}
	header = header[:read]
	if bytes.HasPrefix(header, []byte("#!")) {
		lower := strings.ToLower(string(header))
		if strings.Contains(lower, "python") || strings.Contains(lower, "pypy") {
			return ErrPythonRuntime
		}
		return fmt.Errorf("%w: interpreter/shebang trampolines are prohibited", ErrUntrustedPath)
	}
	return nil
}

func rejectDuplicateJSONKeys(payload []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if token, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON token %v", ErrInvalidManifest, token)
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := consumeJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delimiter)
	}
	return nil
}
