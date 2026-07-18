// Package pluginapi defines the stable, language-neutral plugin ABI model.
//
// Plugins run out of process. Importing this package does not register a
// plugin, start a process, or grant access to host resources. The JSON tags are
// the ABI contract and may be used by implementations written in any language.
package pluginapi

import "context"

const (
	ABIMajor uint16 = 1
	V1_0     uint32 = 1
	V1_1     uint32 = 1<<16 | 1
	Current         = V1_1
)

// VersionRange is inclusive. A plugin declaring 1.0 through 1.1 can run with
// either host version without pretending that a newer major is compatible.
type VersionRange struct {
	Minimum uint32 `json:"minimum"`
	Maximum uint32 `json:"maximum"`
}

type Runtime string

const (
	RuntimeNative Runtime = "native"
	RuntimeWASM   Runtime = "wasm"
)

type Capability string

const (
	CapabilityExtractor     Capability = "extractor"
	CapabilityPostprocessor Capability = "postprocessor"
	CapabilityProvider      Capability = "provider"
)

type Permission string

const (
	PermissionNetwork         Permission = "network"
	PermissionCookies         Permission = "cookies"
	PermissionSecrets         Permission = "secrets"
	PermissionFilesystemRead  Permission = "filesystem_read"
	PermissionFilesystemWrite Permission = "filesystem_write"
	PermissionProcess         Permission = "process"
)

// Manifest is stored as plugin.json next to the native executable or WASM
// module. Entrypoint is a package-relative filename, never a command line.
type Manifest struct {
	Schema       string       `json:"schema"`
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Release      string       `json:"release"`
	Runtime      Runtime      `json:"runtime"`
	Entrypoint   string       `json:"entrypoint"`
	Versions     []uint32     `json:"versions,omitempty"`
	ABIRange     VersionRange `json:"abi"`
	Capabilities []Capability `json:"capabilities"`
	Permissions  []Permission `json:"permissions,omitempty"`
}

type ExtractRequest struct {
	ID      string         `json:"id"`
	URL     string         `json:"url"`
	Options map[string]any `json:"options,omitempty"`
}

type ExtractResponse struct {
	ID       string         `json:"id"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Error    *RemoteError   `json:"error,omitempty"`
}

type PostprocessRequest struct {
	ID        string         `json:"id"`
	Operation string         `json:"operation"`
	Input     Artifact       `json:"input"`
	Options   map[string]any `json:"options,omitempty"`
}

type PostprocessResponse struct {
	ID        string       `json:"id"`
	Artifacts []Artifact   `json:"artifacts,omitempty"`
	Error     *RemoteError `json:"error,omitempty"`
}

// Artifact paths are host-issued opaque names. They are not permission to
// read or write an arbitrary host path.
type Artifact struct {
	Handle    string `json:"handle"`
	MediaType string `json:"media_type,omitempty"`
	Name      string `json:"name,omitempty"`
}

type ProviderRequest struct {
	ID        string         `json:"id"`
	Provider  string         `json:"provider"`
	Action    string         `json:"action"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Secrets   []SecretHandle `json:"secrets,omitempty"`
}

type ProviderResponse struct {
	ID     string         `json:"id"`
	Values map[string]any `json:"values,omitempty"`
	Error  *RemoteError   `json:"error,omitempty"`
}

// SecretHandle names a short-lived host-managed capability. The secret value
// is deliberately absent from the ABI's ordinary request and metadata fields.
type SecretHandle struct {
	ID      string `json:"id"`
	Purpose string `json:"purpose"`
}

type RemoteCategory string

const (
	RemoteAuthentication  RemoteCategory = "authentication"
	RemoteUnavailable     RemoteCategory = "unavailable"
	RemoteInvalidMetadata RemoteCategory = "invalid_metadata"
	RemoteNetwork         RemoteCategory = "network"
	RemotePermission      RemoteCategory = "permission"
	RemoteInvalidInput    RemoteCategory = "invalid_input"
	RemoteInternal        RemoteCategory = "internal"
)

type RemoteError struct {
	Category  RemoteCategory `json:"category"`
	Code      string         `json:"code,omitempty"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable,omitempty"`
}

// PermissionApprover is called after manifest validation and version
// negotiation but before an operation is sent to a plugin.
type PermissionApprover interface {
	Approve(context.Context, ApprovalRequest) (Approval, error)
}

type ApprovalRequest struct {
	PluginID         string       `json:"plugin_id"`
	Release          string       `json:"release"`
	Signer           string       `json:"signer"`
	ExecutableDigest string       `json:"executable_digest"`
	ABI              uint32       `json:"abi"`
	Requested        []Permission `json:"requested,omitempty"`
	Added            []Permission `json:"added,omitempty"`
}

type Approval struct {
	Granted []Permission `json:"granted,omitempty"`
}

// Extractor, Postprocessor and Provider are the author-facing SDK contracts.
// A transport adapter may expose any subset declared by the manifest.
type Extractor interface {
	Extract(context.Context, ExtractRequest) (ExtractResponse, error)
}

type Postprocessor interface {
	Postprocess(context.Context, PostprocessRequest) (PostprocessResponse, error)
}

type Provider interface {
	Provide(context.Context, ProviderRequest) (ProviderResponse, error)
}
