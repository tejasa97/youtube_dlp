package differential

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	ShadowSchemaVersion  = 1
	MaxShadowBytes       = 4 << 20
	MaxShadowItems       = 10_000
	MaxShadowFields      = 10_000
	DefaultMaxMismatches = 256
	maxShadowJSONDepth   = 64
	maxShadowJSONTokens  = 100_000
)

var (
	ErrInvalidObservation = errors.New("invalid shadow observation")
	ErrObservationLimit   = errors.New("shadow observation resource limit exceeded")
)

// ObservationEnvelope is the stable, Python-free semantic capture exchanged
// by shadow producers. Its custom JSON encoding always sanitizes secrets.
type ObservationEnvelope struct {
	SchemaVersion int                        `json:"schema_version"`
	Producer      string                     `json:"producer"`
	Routing       RoutingObservation         `json:"routing"`
	Request       RequestObservation         `json:"request"`
	Metadata      map[string]json.RawMessage `json:"metadata"`
	Formats       []FormatObservation        `json:"formats"`
	Playlist      PlaylistObservation        `json:"playlist"`
	Warnings      []WarningObservation       `json:"warnings"`
	Protocols     []ProtocolObservation      `json:"protocols"`
}

type RoutingObservation struct {
	InputURL      string `json:"input_url"`
	Extractor     string `json:"extractor"`
	CanonicalURL  string `json:"canonical_url,omitempty"`
	TransparentTo string `json:"transparent_to,omitempty"`
}

type RequestObservation struct {
	Method            string              `json:"method"`
	URL               string              `json:"url"`
	Headers           map[string][]string `json:"headers,omitempty"`
	BodySHA256        string              `json:"body_sha256,omitempty"`
	CredentialHandles []string            `json:"credential_handles,omitempty"`
	CredentialCount   int                 `json:"credential_count,omitempty"`
}

type FormatObservation struct {
	ID         string   `json:"id"`
	URL        string   `json:"url,omitempty"`
	Protocol   string   `json:"protocol,omitempty"`
	Extension  string   `json:"extension,omitempty"`
	AudioCodec string   `json:"audio_codec,omitempty"`
	VideoCodec string   `json:"video_codec,omitempty"`
	Bitrate    *float64 `json:"bitrate,omitempty"`
	Usable     bool     `json:"usable"`
}

type PlaylistObservation struct {
	ID      string                     `json:"id,omitempty"`
	Entries []PlaylistEntryObservation `json:"entries"`
}

type PlaylistEntryObservation struct {
	ID          string `json:"id"`
	URL         string `json:"url,omitempty"`
	Unavailable bool   `json:"unavailable,omitempty"`
}

type WarningObservation struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type ProtocolObservation struct {
	Name      string `json:"name"`
	Manifest  string `json:"manifest_url,omitempty"`
	Usable    bool   `json:"usable"`
	Live      bool   `json:"live,omitempty"`
	Fragments int    `json:"fragments,omitempty"`
}

// MarshalJSON is the persistence boundary: raw credentials, authenticated
// URLs, and sensitive headers cannot be serialized through the public type.
func (input ObservationEnvelope) MarshalJSON() ([]byte, error) {
	sanitized, err := SanitizeObservation(input)
	if err != nil {
		return nil, err
	}
	type wire ObservationEnvelope
	return json.Marshal(wire(sanitized))
}

// ParseObservation strictly reads one bounded envelope. Metadata values retain
// JSON null, while absent map keys remain distinguishable as missing.
func ParseObservation(ctx context.Context, reader io.Reader) (ObservationEnvelope, error) {
	if err := ctx.Err(); err != nil {
		return ObservationEnvelope{}, err
	}
	limited := io.LimitReader(reader, MaxShadowBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return ObservationEnvelope{}, fmt.Errorf("%w: read: %v", ErrInvalidObservation, err)
	}
	if len(data) > MaxShadowBytes {
		return ObservationEnvelope{}, ErrObservationLimit
	}
	if err := validateJSONStructure(data); err != nil {
		return ObservationEnvelope{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result ObservationEnvelope
	if err := decoder.Decode((*observationWire)(&result)); err != nil {
		return ObservationEnvelope{}, fmt.Errorf("%w: decode: %v", ErrInvalidObservation, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ObservationEnvelope{}, fmt.Errorf("%w: trailing JSON", ErrInvalidObservation)
	}
	if err := ctx.Err(); err != nil {
		return ObservationEnvelope{}, err
	}
	if err := validateObservation(result); err != nil {
		return ObservationEnvelope{}, err
	}
	return result, nil
}

type observationWire ObservationEnvelope

func validateObservation(input ObservationEnvelope) error {
	if input.SchemaVersion != ShadowSchemaVersion || !safeToken(input.Producer) || !safeToken(input.Routing.Extractor) || !safeMethod(input.Request.Method) || !validObservationURL(input.Routing.InputURL) || !validObservationURL(input.Request.URL) {
		return ErrInvalidObservation
	}
	if input.Routing.CanonicalURL != "" && !validObservationURL(input.Routing.CanonicalURL) {
		return ErrInvalidObservation
	}
	if len(input.Metadata) > MaxShadowFields || len(input.Formats) > MaxShadowItems || len(input.Playlist.Entries) > MaxShadowItems || len(input.Warnings) > MaxShadowItems || len(input.Protocols) > MaxShadowItems {
		return ErrObservationLimit
	}
	if input.Request.CredentialCount < 0 || input.Request.CredentialCount > MaxShadowItems || len(input.Request.CredentialHandles) > MaxShadowItems {
		return ErrObservationLimit
	}
	if input.Request.CredentialCount != 0 && len(input.Request.CredentialHandles) != 0 {
		return fmt.Errorf("%w: credential count and handles are mutually exclusive", ErrInvalidObservation)
	}
	if len(input.Request.Headers) > MaxShadowFields || len(input.Request.BodySHA256) != 0 && !validHexDigest(input.Request.BodySHA256) {
		return ErrInvalidObservation
	}
	headerValues := 0
	for key, values := range input.Request.Headers {
		if !safeToken(key) || len(values) > MaxShadowItems {
			return ErrInvalidObservation
		}
		headerValues += len(values)
		if headerValues > MaxShadowItems {
			return ErrObservationLimit
		}
		for _, headerValue := range values {
			if len(headerValue) > 64<<10 || strings.ContainsAny(headerValue, "\r\n\x00") {
				return ErrInvalidObservation
			}
		}
	}
	metadataBytes := 0
	for key, raw := range input.Metadata {
		if !safeToken(key) || len(raw) == 0 || len(raw) > MaxShadowBytes {
			return ErrInvalidObservation
		}
		metadataBytes += len(key) + len(raw)
		if metadataBytes > MaxShadowBytes {
			return ErrObservationLimit
		}
		if err := validateJSONStructure(raw); err != nil {
			return err
		}
		var parsed value.Value
		if err := parsed.UnmarshalJSON(raw); err != nil {
			return fmt.Errorf("%w: metadata %q: %v", ErrInvalidObservation, key, err)
		}
	}
	formatIDs := make(map[string]struct{}, len(input.Formats))
	for _, format := range input.Formats {
		if !safeToken(format.ID) || format.Bitrate != nil && (*format.Bitrate < 0) || format.URL != "" && !validObservationURL(format.URL) {
			return ErrInvalidObservation
		}
		if _, duplicate := formatIDs[format.ID]; duplicate {
			return fmt.Errorf("%w: duplicate format id %q", ErrInvalidObservation, format.ID)
		}
		formatIDs[format.ID] = struct{}{}
	}
	for _, entry := range input.Playlist.Entries {
		if !safeToken(entry.ID) || entry.URL != "" && !validObservationURL(entry.URL) {
			return ErrInvalidObservation
		}
	}
	for _, warning := range input.Warnings {
		if !safeToken(warning.Code) || len(warning.Message) > 16<<10 {
			return ErrInvalidObservation
		}
	}
	protocolNames := make(map[string]struct{}, len(input.Protocols))
	for _, protocol := range input.Protocols {
		if !safeToken(protocol.Name) || protocol.Fragments < 0 || protocol.Manifest != "" && !validObservationURL(protocol.Manifest) {
			return ErrInvalidObservation
		}
		if _, duplicate := protocolNames[protocol.Name]; duplicate {
			return fmt.Errorf("%w: duplicate protocol name %q", ErrInvalidObservation, protocol.Name)
		}
		protocolNames[protocol.Name] = struct{}{}
	}
	return nil
}

func validateJSONStructure(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	tokens := 0
	if err := scanJSONValue(decoder, 0, &tokens); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrInvalidObservation)
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int, tokens *int) error {
	if depth > maxShadowJSONDepth || *tokens >= maxShadowJSONTokens {
		return ErrObservationLimit
	}
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("%w: JSON: %v", ErrInvalidObservation, err)
	}
	*tokens++
	delim, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delim {
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return fmt.Errorf("%w: list end", ErrInvalidObservation)
		}
		*tokens++
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("%w: object key", ErrInvalidObservation)
			}
			key, ok := keyToken.(string)
			if !ok {
				return ErrInvalidObservation
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("%w: duplicate key %q", ErrInvalidObservation, key)
			}
			seen[key] = struct{}{}
			*tokens++
			if err := scanJSONValue(decoder, depth+1, tokens); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return fmt.Errorf("%w: object end", ErrInvalidObservation)
		}
		*tokens++
	default:
		return ErrInvalidObservation
	}
	return nil
}

func validObservationURL(input string) bool {
	parsed, err := url.Parse(input)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func validHexDigest(input string) bool {
	if len(input) != sha256.Size*2 || input != strings.ToLower(input) {
		return false
	}
	_, err := hex.DecodeString(input)
	return err == nil
}

func safeToken(input string) bool {
	if input == "" || len(input) > 256 {
		return false
	}
	for _, r := range input {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func safeMethod(input string) bool {
	if input == "" || len(input) > 32 {
		return false
	}
	for _, character := range input {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}

// SanitizeObservation creates the only representation suitable for reports or
// persistence. Credential handles are reduced to a count, never retained.
func SanitizeObservation(input ObservationEnvelope) (ObservationEnvelope, error) {
	if err := validateObservation(input); err != nil {
		return ObservationEnvelope{}, err
	}
	output := input
	output.Routing.InputURL = redactObservationURL(input.Routing.InputURL)
	if input.Routing.CanonicalURL != "" {
		output.Routing.CanonicalURL = redactObservationURL(input.Routing.CanonicalURL)
	}
	output.Request.URL = redactObservationURL(input.Request.URL)
	headers := make(http.Header, len(input.Request.Headers))
	for key, values := range input.Request.Headers {
		headers[key] = append([]string(nil), values...)
	}
	redacted := network.RedactHeaders(headers)
	output.Request.Headers = make(map[string][]string, len(redacted))
	for key, values := range redacted {
		if sensitiveName(key) {
			output.Request.Headers[http.CanonicalHeaderKey(key)] = []string{"REDACTED"}
		} else {
			output.Request.Headers[http.CanonicalHeaderKey(key)] = make([]string, len(values))
			for index, headerValue := range values {
				output.Request.Headers[http.CanonicalHeaderKey(key)][index] = redactText(headerValue)
			}
		}
	}
	output.Request.CredentialCount += len(input.Request.CredentialHandles)
	output.Request.CredentialHandles = nil
	output.Metadata = make(map[string]json.RawMessage, len(input.Metadata))
	for key, raw := range input.Metadata {
		canonical, err := sanitizeMetadata(key, raw)
		if err != nil {
			return ObservationEnvelope{}, ErrInvalidObservation
		}
		output.Metadata[key] = canonical
	}
	output.Formats = append([]FormatObservation(nil), input.Formats...)
	for index := range output.Formats {
		if output.Formats[index].URL != "" {
			output.Formats[index].URL = redactObservationURL(output.Formats[index].URL)
		}
	}
	output.Playlist.Entries = append([]PlaylistEntryObservation(nil), input.Playlist.Entries...)
	for index := range output.Playlist.Entries {
		if output.Playlist.Entries[index].URL != "" {
			output.Playlist.Entries[index].URL = redactObservationURL(output.Playlist.Entries[index].URL)
		}
	}
	output.Protocols = append([]ProtocolObservation(nil), input.Protocols...)
	for index := range output.Protocols {
		if output.Protocols[index].Manifest != "" {
			output.Protocols[index].Manifest = redactObservationURL(output.Protocols[index].Manifest)
		}
	}
	output.Warnings = append([]WarningObservation(nil), input.Warnings...)
	for index := range output.Warnings {
		output.Warnings[index].Message = redactText(output.Warnings[index].Message)
	}
	return output, nil
}

func redactObservationURL(input string) string {
	parsed, err := url.Parse(input)
	if err != nil {
		return "<invalid URL>"
	}
	query := parsed.Query()
	for key := range query {
		if sensitiveName(key) {
			query.Set(key, "REDACTED")
		}
	}
	parsed.RawQuery = query.Encode()
	if parsed.Fragment != "" {
		parsed.Fragment = "REDACTED"
	}
	return network.RedactURL(parsed)
}

func sensitiveName(input string) bool {
	lower := strings.ToLower(strings.TrimSpace(input))
	for _, exact := range []string{"auth", "authorization", "cookie", "credentials", "key", "password", "proxy-authorization", "secret", "session", "set-cookie", "sig", "signature", "token", "x-api-key"} {
		if lower == exact {
			return true
		}
	}
	for _, suffix := range []string{"_auth", "-auth", "_cookie", "-cookie", "_credential", "-credential", "_password", "-password", "_secret", "-secret", "_session", "-session", "_signature", "-signature", "_token", "-token", "_api_key", "-api-key", "_access_key", "-access-key"} {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func sanitizeMetadata(key string, raw json.RawMessage) (json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	decoded = sanitizeDynamic(key, decoded)
	return json.Marshal(decoded)
}

func sanitizeDynamic(key string, input any) any {
	if sensitiveName(key) {
		return "REDACTED"
	}
	switch typed := input.(type) {
	case map[string]any:
		for childKey, child := range typed {
			typed[childKey] = sanitizeDynamic(childKey, child)
		}
		return typed
	case []any:
		for index := range typed {
			typed[index] = sanitizeDynamic(key, typed[index])
		}
		return typed
	case string:
		if strings.Contains(strings.ToLower(key), "url") && validObservationURL(typed) {
			return redactObservationURL(typed)
		}
		return redactText(typed)
	default:
		return input
	}
}

func redactText(input string) string {
	fields := strings.Fields(input)
	for index, field := range fields {
		if strings.Contains(field, "://") {
			fields[index] = network.RedactRawURL(strings.Trim(field, "()[]{}<>,.;\"'"))
			continue
		}
		lower := strings.ToLower(field)
		for _, prefix := range []string{"token=", "authorization=", "cookie=", "password=", "secret="} {
			if strings.HasPrefix(lower, prefix) {
				fields[index] = field[:len(prefix)] + "REDACTED"
				break
			}
		}
	}
	return strings.Join(fields, " ")
}

// CanonicalObservation returns stable compact JSON and its SHA-256 identity.
func CanonicalObservation(ctx context.Context, input ObservationEnvelope) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return nil, "", err
	}
	if len(encoded) > MaxShadowBytes {
		return nil, "", ErrObservationLimit
	}
	encoded = append(encoded, '\n')
	digest := sha256.Sum256(encoded)
	return encoded, hex.EncodeToString(digest[:]), nil
}

func sortedCopy[T any](items []T, key func(T) string) []T {
	result := append([]T(nil), items...)
	sort.SliceStable(result, func(i, j int) bool { return key(result[i]) < key(result[j]) })
	return result
}
