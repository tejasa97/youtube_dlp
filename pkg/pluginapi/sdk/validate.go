package sdk

import (
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode"

	"github.com/ytdlp-go/ytdlp/pkg/pluginapi"
)

const manifestSchema = "ytdlp-go.plugin/v1"

var (
	identifierPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,126}[a-z0-9])?$`)
	releasePattern    = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,127}$`)
	pythonEntrypoint  = regexp.MustCompile(`(?i)(^|[._-])(python|pypy)([0-9.]*)($|[._-])|\.py[wc]?$`)
	interpreter       = regexp.MustCompile(`(?i)^(?:sh|bash|dash|zsh|fish|cmd|powershell|pwsh|perl|ruby|node|deno|bun)(?:\.exe)?$|\.(?:bat|cmd|ps1|sh)$`)
)

func validateServer(server Server) error {
	if err := validateManifest(server.Manifest); err != nil {
		return err
	}
	declared := make(map[pluginapi.Capability]bool, len(server.Manifest.Capabilities))
	for _, capability := range server.Manifest.Capabilities {
		declared[capability] = true
	}
	present := map[pluginapi.Capability]bool{
		pluginapi.CapabilityExtractor: server.Extractor != nil, pluginapi.CapabilityPostprocessor: server.Postprocessor != nil, pluginapi.CapabilityProvider: server.Provider != nil,
	}
	for capability, hasHandler := range present {
		if declared[capability] != hasHandler {
			return fmt.Errorf("%w: %s handler declaration mismatch", ErrCapability, capability)
		}
	}
	return nil
}

func validateManifest(manifest pluginapi.Manifest) error {
	if manifest.Schema != manifestSchema || !identifierPattern.MatchString(manifest.ID) ||
		strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 128 || strings.IndexFunc(manifest.Name, unicode.IsControl) >= 0 ||
		!releasePattern.MatchString(manifest.Release) {
		return ErrInvalidManifest
	}
	if manifest.Runtime != pluginapi.RuntimeNative && manifest.Runtime != pluginapi.RuntimeWASM {
		if strings.Contains(strings.ToLower(string(manifest.Runtime)), "python") {
			return ErrPythonRuntime
		}
		return ErrInvalidManifest
	}
	if manifest.Entrypoint == "" || filepath.IsAbs(manifest.Entrypoint) || filepath.Base(manifest.Entrypoint) != manifest.Entrypoint ||
		manifest.Entrypoint == "." || manifest.Entrypoint == ".." || strings.IndexFunc(manifest.Entrypoint, unicode.IsControl) >= 0 {
		return ErrInvalidManifest
	}
	if pythonEntrypoint.MatchString(manifest.Entrypoint) || interpreter.MatchString(manifest.Entrypoint) {
		return ErrPythonRuntime
	}
	rangeValue := manifestRange(manifest)
	if err := validateRange(rangeValue); err != nil {
		return err
	}
	if pluginapi.CompareVersions(rangeValue.Minimum, pluginapi.V1_0) < 0 || pluginapi.CompareVersions(rangeValue.Maximum, pluginapi.V1_1) > 0 {
		return ErrIncompatibleVersion
	}
	if len(manifest.Versions) > 16 || len(manifest.Capabilities) == 0 || len(manifest.Capabilities) > 16 || len(manifest.Permissions) > 16 {
		return ErrInvalidManifest
	}
	seenVersions := make(map[uint32]struct{}, len(manifest.Versions))
	for _, version := range manifest.Versions {
		if !canonicalVersion(version) || !within(version, rangeValue) {
			return ErrIncompatibleVersion
		}
		if _, exists := seenVersions[version]; exists {
			return ErrInvalidManifest
		}
		seenVersions[version] = struct{}{}
	}
	seenCapabilities := make(map[pluginapi.Capability]struct{}, len(manifest.Capabilities))
	for _, capability := range manifest.Capabilities {
		switch capability {
		case pluginapi.CapabilityExtractor, pluginapi.CapabilityPostprocessor, pluginapi.CapabilityProvider:
		default:
			return ErrInvalidManifest
		}
		if _, exists := seenCapabilities[capability]; exists {
			return ErrInvalidManifest
		}
		seenCapabilities[capability] = struct{}{}
	}
	seenPermissions := make(map[pluginapi.Permission]struct{}, len(manifest.Permissions))
	for _, permission := range manifest.Permissions {
		switch permission {
		case pluginapi.PermissionNetwork, pluginapi.PermissionCookies, pluginapi.PermissionSecrets, pluginapi.PermissionFilesystemRead, pluginapi.PermissionFilesystemWrite, pluginapi.PermissionProcess:
		default:
			return ErrInvalidManifest
		}
		if _, exists := seenPermissions[permission]; exists {
			return ErrInvalidManifest
		}
		seenPermissions[permission] = struct{}{}
	}
	return nil
}

func negotiateHello(hello pluginapi.Envelope, supported pluginapi.VersionRange) (uint32, error) {
	if hello.Type != "hello" || hello.Version != 0 || hello.Manifest != nil || hello.ExtractRequest != nil || hello.ExtractResponse != nil ||
		hello.PostprocessRequest != nil || hello.PostprocessResponse != nil || hello.ProviderRequest != nil || hello.ProviderResponse != nil || hello.RequestID != "" ||
		(len(hello.Versions) == 0 && hello.ABI == nil) || len(hello.Versions) > 16 {
		return 0, ErrProtocol
	}
	if hello.ABI != nil {
		if err := validateRange(*hello.ABI); err != nil {
			return 0, err
		}
	}
	var selected uint32
	seen := make(map[uint32]struct{}, len(hello.Versions))
	for _, version := range hello.Versions {
		if !canonicalVersion(version) {
			return 0, ErrIncompatibleVersion
		}
		if _, duplicate := seen[version]; duplicate {
			return 0, ErrProtocol
		}
		seen[version] = struct{}{}
		if within(version, supported) && (hello.ABI == nil || within(version, *hello.ABI)) && pluginapi.CompareVersions(version, pluginapi.V1_1) <= 0 &&
			(selected == 0 || pluginapi.CompareVersions(version, selected) > 0) {
			selected = version
		}
	}
	if len(hello.Versions) == 0 && hello.ABI != nil {
		minimum := supported.Minimum
		if pluginapi.CompareVersions(hello.ABI.Minimum, minimum) > 0 {
			minimum = hello.ABI.Minimum
		}
		maximum := supported.Maximum
		if pluginapi.CompareVersions(hello.ABI.Maximum, maximum) < 0 {
			maximum = hello.ABI.Maximum
		}
		if pluginapi.CompareVersions(maximum, pluginapi.V1_1) > 0 {
			maximum = pluginapi.V1_1
		}
		if pluginapi.Compatible(minimum, maximum) && pluginapi.CompareVersions(minimum, maximum) <= 0 {
			selected = maximum
		}
	}
	if selected == 0 {
		return 0, ErrIncompatibleVersion
	}
	return selected, nil
}

func validateOperation(envelope pluginapi.Envelope, version uint32, manifest pluginapi.Manifest) (string, error) {
	if envelope.Version != version || envelope.Manifest != nil || len(envelope.Versions) != 0 || envelope.ABI != nil || envelope.RequestID != "" ||
		envelope.ExtractResponse != nil || envelope.PostprocessResponse != nil || envelope.ProviderResponse != nil {
		return "", ErrProtocol
	}
	var id string
	switch envelope.Type {
	case "extract":
		if envelope.ExtractRequest == nil || envelope.PostprocessRequest != nil || envelope.ProviderRequest != nil || !hasCapability(manifest, pluginapi.CapabilityExtractor) {
			return "", ErrCapability
		}
		id = envelope.ExtractRequest.ID
		if envelope.ExtractRequest.URL == "" || len(envelope.ExtractRequest.URL) > 16<<10 {
			return "", ErrProtocol
		}
		if err := validatePayload(envelope.ExtractRequest.Options); err != nil {
			return "", err
		}
	case "postprocess":
		if envelope.PostprocessRequest == nil || envelope.ExtractRequest != nil || envelope.ProviderRequest != nil || !hasCapability(manifest, pluginapi.CapabilityPostprocessor) {
			return "", ErrCapability
		}
		id = envelope.PostprocessRequest.ID
		if envelope.PostprocessRequest.Operation == "" || envelope.PostprocessRequest.Input.Handle == "" {
			return "", ErrProtocol
		}
		if err := validatePayload(envelope.PostprocessRequest.Input); err != nil {
			return "", err
		}
		if err := validatePayload(envelope.PostprocessRequest.Options); err != nil {
			return "", err
		}
	case "provide":
		if envelope.ProviderRequest == nil || envelope.ExtractRequest != nil || envelope.PostprocessRequest != nil || !hasCapability(manifest, pluginapi.CapabilityProvider) {
			return "", ErrCapability
		}
		id = envelope.ProviderRequest.ID
		if envelope.ProviderRequest.Provider == "" || envelope.ProviderRequest.Action == "" || len(envelope.ProviderRequest.Secrets) > 32 {
			return "", ErrProtocol
		}
		for _, secret := range envelope.ProviderRequest.Secrets {
			if !validRequestID(secret.ID) || secret.Purpose == "" || len(secret.Purpose) > 128 || strings.IndexFunc(secret.Purpose, unicode.IsControl) >= 0 {
				return "", ErrProtocol
			}
		}
		if err := validatePayload(envelope.ProviderRequest.Arguments); err != nil {
			return "", err
		}
	default:
		return "", ErrProtocol
	}
	if !validRequestID(id) {
		return "", ErrProtocol
	}
	return id, nil
}

func validateCancel(envelope pluginapi.Envelope, requestID string) error {
	if envelope.Type != "cancel" || envelope.RequestID != requestID || envelope.Version != 0 || envelope.Manifest != nil || envelope.ABI != nil || len(envelope.Versions) != 0 ||
		envelope.ExtractRequest != nil || envelope.ExtractResponse != nil || envelope.PostprocessRequest != nil || envelope.PostprocessResponse != nil || envelope.ProviderRequest != nil || envelope.ProviderResponse != nil {
		return ErrProtocol
	}
	return nil
}

func validateExtractResponse(response pluginapi.ExtractResponse, id string) error {
	if response.ID != id || validateRemoteError(response.Error) != nil || validatePayload(response.Metadata) != nil {
		return ErrProtocol
	}
	return nil
}

func validatePostprocessResponse(response pluginapi.PostprocessResponse, id string) error {
	if response.ID != id || validateRemoteError(response.Error) != nil || len(response.Artifacts) > 128 {
		return ErrProtocol
	}
	for _, artifact := range response.Artifacts {
		if !validRequestID(artifact.Handle) || len(artifact.MediaType) > 256 || len(artifact.Name) > 512 || validatePayload(artifact) != nil {
			return ErrProtocol
		}
	}
	return nil
}

func validateProviderResponse(response pluginapi.ProviderResponse, id string) error {
	if response.ID != id || validateRemoteError(response.Error) != nil || validatePayload(response.Values) != nil {
		return ErrProtocol
	}
	return nil
}

func manifestRange(manifest pluginapi.Manifest) pluginapi.VersionRange {
	if manifest.ABIRange.Minimum != 0 || manifest.ABIRange.Maximum != 0 {
		return manifest.ABIRange
	}
	if len(manifest.Versions) == 0 {
		return pluginapi.VersionRange{}
	}
	result := pluginapi.VersionRange{Minimum: manifest.Versions[0], Maximum: manifest.Versions[0]}
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

func validateRange(value pluginapi.VersionRange) error {
	if !canonicalVersion(value.Minimum) || !canonicalVersion(value.Maximum) || !pluginapi.Compatible(value.Minimum, value.Maximum) || pluginapi.CompareVersions(value.Minimum, value.Maximum) > 0 {
		return ErrIncompatibleVersion
	}
	return nil
}

func canonicalVersion(version uint32) bool {
	major, minor := pluginapi.VersionParts(version)
	return major != 0 && pluginapi.Version(major, minor) == version
}

func within(version uint32, rangeValue pluginapi.VersionRange) bool {
	return pluginapi.Compatible(version, rangeValue.Minimum) && pluginapi.CompareVersions(version, rangeValue.Minimum) >= 0 && pluginapi.CompareVersions(version, rangeValue.Maximum) <= 0
}

func hasCapability(manifest pluginapi.Manifest, expected pluginapi.Capability) bool {
	for _, capability := range manifest.Capabilities {
		if capability == expected {
			return true
		}
	}
	return false
}

func validRequestID(value string) bool {
	return value != "" && len(value) <= 128 && strings.IndexFunc(value, unicode.IsControl) < 0
}

func cloneManifest(input pluginapi.Manifest) pluginapi.Manifest {
	result := input
	result.Versions = append([]uint32(nil), input.Versions...)
	result.Capabilities = append([]pluginapi.Capability(nil), input.Capabilities...)
	result.Permissions = append([]pluginapi.Permission(nil), input.Permissions...)
	return result
}

func validatePayload(value any) error {
	nodes := 0
	var visit func(reflect.Value, int) error
	visit = func(current reflect.Value, depth int) error {
		if depth > 64 {
			return ErrResourceLimit
		}
		nodes++
		if nodes > 10000 {
			return ErrResourceLimit
		}
		if !current.IsValid() {
			return nil
		}
		for current.Kind() == reflect.Interface || current.Kind() == reflect.Pointer {
			if current.IsNil() {
				return nil
			}
			current = current.Elem()
			depth++
			if depth > 64 {
				return ErrResourceLimit
			}
		}
		switch current.Kind() {
		case reflect.Map:
			if current.Type().Key().Kind() != reflect.String {
				return ErrProtocol
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
			if sensitivePattern.MatchString(current.String()) {
				return ErrSecretExposure
			}
		case reflect.Func, reflect.Chan, reflect.UnsafePointer:
			return ErrProtocol
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
