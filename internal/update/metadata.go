// Package update implements the Python-free signed updater foundation.
package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"sort"
	"strings"
	"time"
)

var (
	ErrInvalidMetadata = errors.New("invalid update metadata")
	ErrSignature       = errors.New("update signature verification failed")
	ErrExpired         = errors.New("update metadata expired")
	ErrFreeze          = errors.New("update metadata is not newer")
	ErrDowngrade       = errors.New("update downgrade rejected")
	ErrWrongChannel    = errors.New("update channel unavailable")
	ErrWrongPlatform   = errors.New("update platform unavailable")
	ErrUnsafePath      = errors.New("unsafe update path")
	ErrHash            = errors.New("update artifact verification failed")
	ErrTooLarge        = errors.New("update resource limit exceeded")
	ErrLock            = errors.New("update lock failure")
	ErrIO              = errors.New("update I/O failure")
	ErrHealth          = errors.New("update health check failed")
	ErrNoRollback      = errors.New("no update rollback is available")
	ErrRecovery        = errors.New("update recovery failed")
)

const (
	MetadataSpec = "ytdlp-go-update-v1"
	ReleaseRole  = "release"
	maxMetadata  = 1 << 20
	maxTargets   = 512
	maxSigs      = 32
)

// Channel identifies an independently selected stream of releases.
type Channel string

const (
	ChannelStable  Channel = "stable"
	ChannelBeta    Channel = "beta"
	ChannelNightly Channel = "nightly"
)

func (channel Channel) valid() bool {
	switch channel {
	case ChannelStable, ChannelBeta, ChannelNightly:
		return true
	default:
		return false
	}
}

// Target describes one immutable release artifact. Artifact is a safe base
// name, not a URL; transport discovery is deliberately outside this package.
type Target struct {
	Version  string  `json:"version"`
	Channel  Channel `json:"channel"`
	GOOS     string  `json:"goos"`
	GOARCH   string  `json:"goarch"`
	Artifact string  `json:"artifact"`
	Size     int64   `json:"size"`
	SHA256   string  `json:"sha256"`
}

// Metadata is the signed portion of an update envelope. Sequence is strictly
// monotonic for a trust root and protects clients against replay/freeze.
type Metadata struct {
	Spec       string   `json:"spec"`
	Role       string   `json:"role"`
	Product    string   `json:"product"`
	Generation uint64   `json:"generation"`
	Expires    string   `json:"expires"`
	Targets    []Target `json:"targets"`
}

type Signature struct {
	KeyID string `json:"keyid"`
	Value string `json:"sig"`
}

type Envelope struct {
	Signed     json.RawMessage `json:"signed"`
	Signatures []Signature     `json:"signatures"`
}

// Root is supplied by the embedding product's trust policy. This package does
// not select, create, download, rotate, or persist production trust roots.
type Root struct {
	Keys      map[string]ed25519.PublicKey
	Threshold int
	Role      string
	Product   string
	Channels  []Channel
	Platforms []Platform
}

type Platform struct {
	GOOS   string
	GOARCH string
}

// Selection describes the local anti-rollback and target constraints.
type Selection struct {
	Product           string
	Channel           Channel
	GOOS              string
	GOARCH            string
	Installed         string
	HighestGeneration uint64
	Now               time.Time
}

func (selection Selection) withDefaults() Selection {
	if selection.GOOS == "" {
		selection.GOOS = runtime.GOOS
	}
	if selection.GOARCH == "" {
		selection.GOARCH = runtime.GOARCH
	}
	if selection.Now.IsZero() {
		selection.Now = time.Now()
	}
	return selection
}

// Sign creates a deterministic JSON envelope. It accepts explicit keys so
// tests and external release tooling can implement threshold ceremonies; it
// never creates signing authority.
func Sign(metadata Metadata, keys map[string]ed25519.PrivateKey) ([]byte, error) {
	if err := validateMetadata(metadata); err != nil {
		return nil, err
	}
	signed, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("%w: encode signed body", ErrInvalidMetadata)
	}
	keyIDs := make([]string, 0, len(keys))
	for keyID := range keys {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	if len(keyIDs) == 0 || len(keyIDs) > maxSigs {
		return nil, fmt.Errorf("%w: signature count", ErrInvalidMetadata)
	}
	signatures := make([]Signature, 0, len(keyIDs))
	for _, keyID := range keyIDs {
		key := keys[keyID]
		if len(key) != ed25519.PrivateKeySize || keyID != KeyID(key.Public().(ed25519.PublicKey)) {
			return nil, fmt.Errorf("%w: signer", ErrInvalidMetadata)
		}
		signatures = append(signatures, Signature{
			KeyID: keyID,
			Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, signed)),
		})
	}
	encoded, err := json.Marshal(Envelope{Signed: signed, Signatures: signatures})
	if err != nil {
		return nil, fmt.Errorf("%w: encode envelope", ErrInvalidMetadata)
	}
	return append(encoded, '\n'), nil
}

// Verify authenticates and strictly decodes an envelope. Diagnostics never
// include signed bodies, artifact names, URLs, keys, or signatures.
func Verify(encoded []byte, root Root) (Metadata, error) {
	if len(encoded) == 0 || len(encoded) > maxMetadata {
		return Metadata{}, ErrTooLarge
	}
	if err := validateRoot(root); err != nil {
		return Metadata{}, err
	}
	var envelope Envelope
	if err := decodeStrict(encoded, &envelope); err != nil {
		return Metadata{}, fmt.Errorf("%w: envelope", ErrInvalidMetadata)
	}
	if len(envelope.Signed) == 0 || len(envelope.Signed) > maxMetadata || len(envelope.Signatures) == 0 || len(envelope.Signatures) > maxSigs {
		return Metadata{}, ErrInvalidMetadata
	}
	valid := 0
	seen := make(map[string]struct{}, len(envelope.Signatures))
	for _, signature := range envelope.Signatures {
		if _, duplicate := seen[signature.KeyID]; duplicate {
			return Metadata{}, fmt.Errorf("%w: duplicate signer", ErrInvalidMetadata)
		}
		seen[signature.KeyID] = struct{}{}
		key, trusted := root.Keys[signature.KeyID]
		if !trusted {
			continue
		}
		decoded, err := base64.RawURLEncoding.DecodeString(signature.Value)
		if err != nil || len(decoded) != ed25519.SignatureSize {
			return Metadata{}, ErrSignature
		}
		if ed25519.Verify(key, envelope.Signed, decoded) {
			valid++
		}
	}
	if valid < root.Threshold {
		return Metadata{}, ErrSignature
	}
	var metadata Metadata
	if err := decodeStrict(envelope.Signed, &metadata); err != nil {
		return Metadata{}, fmt.Errorf("%w: signed body", ErrInvalidMetadata)
	}
	if err := validateMetadata(metadata); err != nil {
		return Metadata{}, err
	}
	if metadata.Role != root.Role || metadata.Product != root.Product {
		return Metadata{}, ErrSignature
	}
	for _, target := range metadata.Targets {
		if !rootAllows(root, target) {
			return Metadata{}, ErrSignature
		}
	}
	canonical, err := json.Marshal(metadata)
	if err != nil || !bytes.Equal(canonical, envelope.Signed) {
		return Metadata{}, fmt.Errorf("%w: non-canonical signed body", ErrInvalidMetadata)
	}
	return metadata, nil
}

// Select enforces expiration, monotonic metadata, channel, platform, and
// semantic-version downgrade protection before returning a target.
func Select(metadata Metadata, selection Selection) (Target, error) {
	selection = selection.withDefaults()
	if err := validateMetadata(metadata); err != nil {
		return Target{}, err
	}
	if !selection.Channel.valid() {
		return Target{}, fmt.Errorf("%w: selection", ErrInvalidMetadata)
	}
	expires, _ := time.Parse(time.RFC3339, metadata.Expires)
	if selection.Now.Year() < 2020 || selection.Now.Year() > 3000 || expires.Sub(selection.Now) > 31*24*time.Hour {
		return Target{}, fmt.Errorf("%w: freshness window", ErrInvalidMetadata)
	}
	if !selection.Now.Before(expires) {
		return Target{}, ErrExpired
	}
	if selection.Product == "" || selection.Product != metadata.Product {
		return Target{}, fmt.Errorf("%w: product", ErrInvalidMetadata)
	}
	if metadata.Generation <= selection.HighestGeneration {
		return Target{}, ErrFreeze
	}
	var candidates []Target
	channelFound := false
	for _, target := range metadata.Targets {
		if target.Channel != selection.Channel {
			continue
		}
		channelFound = true
		if target.GOOS == selection.GOOS && target.GOARCH == selection.GOARCH {
			candidates = append(candidates, target)
		}
	}
	if !channelFound {
		return Target{}, ErrWrongChannel
	}
	if len(candidates) == 0 {
		return Target{}, ErrWrongPlatform
	}
	sort.Slice(candidates, func(i, j int) bool { return compareVersion(candidates[i].Version, candidates[j].Version) > 0 })
	selected := candidates[0]
	if selection.Installed != "" && compareVersion(selected.Version, selection.Installed) < 0 {
		return Target{}, ErrDowngrade
	}
	return selected, nil
}

func validateRoot(root Root) error {
	if root.Threshold <= 0 || root.Threshold > len(root.Keys) || len(root.Keys) > maxSigs || root.Role != ReleaseRole || !validProduct(root.Product) {
		return fmt.Errorf("%w: trust threshold", ErrInvalidMetadata)
	}
	for keyID, key := range root.Keys {
		if len(key) != ed25519.PublicKeySize || keyID != KeyID(key) {
			return fmt.Errorf("%w: trust key", ErrInvalidMetadata)
		}
	}
	if len(root.Channels) == 0 || len(root.Channels) > 16 || len(root.Platforms) == 0 || len(root.Platforms) > 64 {
		return fmt.Errorf("%w: trust scope", ErrInvalidMetadata)
	}
	for _, channel := range root.Channels {
		if !channel.valid() {
			return fmt.Errorf("%w: trust channel", ErrInvalidMetadata)
		}
	}
	for _, platform := range root.Platforms {
		if !validPlatformPart(platform.GOOS) || !validPlatformPart(platform.GOARCH) {
			return fmt.Errorf("%w: trust platform", ErrInvalidMetadata)
		}
	}
	return nil
}

func validateMetadata(metadata Metadata) error {
	if metadata.Spec != MetadataSpec || metadata.Role != ReleaseRole || !validProduct(metadata.Product) || metadata.Generation == 0 || len(metadata.Targets) == 0 || len(metadata.Targets) > maxTargets {
		return ErrInvalidMetadata
	}
	expires, err := time.Parse(time.RFC3339, metadata.Expires)
	if err != nil || expires.Format(time.RFC3339) != metadata.Expires {
		return fmt.Errorf("%w: expiration", ErrInvalidMetadata)
	}
	seen := make(map[string]struct{}, len(metadata.Targets))
	for _, target := range metadata.Targets {
		if err := validateTarget(target); err != nil {
			return err
		}
		identity := string(target.Channel) + "/" + target.GOOS + "/" + target.GOARCH
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("%w: duplicate target", ErrInvalidMetadata)
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func validateTarget(target Target) error {
	if !target.Channel.valid() || !validPlatformPart(target.GOOS) || !validPlatformPart(target.GOARCH) || !validVersion(target.Version) || !safeArtifact(target.Artifact, target.GOOS) {
		return fmt.Errorf("%w: target", ErrInvalidMetadata)
	}
	if target.Size <= 0 || target.Size > 1<<30 {
		return fmt.Errorf("%w: target size", ErrInvalidMetadata)
	}
	digest, err := hex.DecodeString(target.SHA256)
	if err != nil || len(digest) != sha256.Size || target.SHA256 != strings.ToLower(target.SHA256) {
		return fmt.Errorf("%w: target digest", ErrInvalidMetadata)
	}
	return nil
}

func decodeStrict(encoded []byte, destination any) error {
	if err := rejectDuplicateKeys(encoded); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rejectDuplicateKeys(encoded []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, compound := token.(json.Delim)
		if !compound {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				nameToken, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := nameToken.(string)
				if !ok {
					return errors.New("non-string object key")
				}
				if _, duplicate := seen[name]; duplicate {
					return errors.New("duplicate object key")
				}
				seen[name] = struct{}{}
				if err := value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return errors.New("unterminated object")
			}
		case '[':
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return errors.New("unterminated array")
			}
		default:
			return errors.New("unexpected JSON delimiter")
		}
		return nil
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func rootAllows(root Root, target Target) bool {
	channelAllowed := false
	for _, channel := range root.Channels {
		if channel == target.Channel {
			channelAllowed = true
			break
		}
	}
	if !channelAllowed {
		return false
	}
	for _, platform := range root.Platforms {
		if platform.GOOS == target.GOOS && platform.GOARCH == target.GOARCH {
			return true
		}
	}
	return false
}

func validKeyID(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// KeyID derives the only accepted signer identifier from the raw public key.
func KeyID(key ed25519.PublicKey) string {
	digest := sha256.Sum256(key)
	return hex.EncodeToString(digest[:])
}

func validProduct(value string) bool {
	return len(value) >= 1 && len(value) <= 64 && validToken(value, "-_.")
}

func validPlatformPart(value string) bool {
	return len(value) >= 1 && len(value) <= 32 && validToken(value, "_-")
}

func safeBase(value string) bool {
	return len(value) >= 1 && len(value) <= 128 && value != "." && value != ".." && !strings.ContainsAny(value, "/\\\x00") && validToken(value, "-_.")
}

func safeArtifact(value, goos string) bool {
	if !safeBase(value) || strings.HasSuffix(value, ".") || strings.HasSuffix(value, " ") {
		return false
	}
	if goos != "windows" {
		return true
	}
	stem := strings.ToUpper(strings.SplitN(value, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	default:
		return true
	}
}

func validToken(value, extra string) bool {
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune(extra, character) {
			continue
		}
		return false
	}
	return true
}

// Versions are deliberately a bounded SemVer subset: MAJOR.MINOR.PATCH with
// optional dot/hyphen separated prerelease identifiers. Build metadata is not
// accepted because it has no ordering meaning for updater decisions.
func validVersion(value string) bool {
	if len(value) < 5 || len(value) > 64 || strings.Contains(value, "+") {
		return false
	}
	base, prerelease, found := strings.Cut(value, "-")
	parts := strings.Split(base, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !validNumeric(part) {
			return false
		}
	}
	if found {
		if prerelease == "" {
			return false
		}
		for _, part := range strings.Split(prerelease, ".") {
			if part == "" || !validToken(part, "-") || allDigits(part) && len(part) > 1 && part[0] == '0' {
				return false
			}
		}
	}
	return true
}

func validNumeric(value string) bool {
	if value == "" || len(value) > 10 || len(value) > 1 && value[0] == '0' {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func compareVersion(left, right string) int {
	leftBase, leftPre, _ := strings.Cut(left, "-")
	rightBase, rightPre, _ := strings.Cut(right, "-")
	leftParts := strings.Split(leftBase, ".")
	rightParts := strings.Split(rightBase, ".")
	for index := 0; index < 3; index++ {
		if len(leftParts[index]) != len(rightParts[index]) {
			if len(leftParts[index]) < len(rightParts[index]) {
				return -1
			}
			return 1
		}
		if leftParts[index] < rightParts[index] {
			return -1
		}
		if leftParts[index] > rightParts[index] {
			return 1
		}
	}
	if leftPre == rightPre {
		return 0
	}
	if leftPre == "" {
		return 1
	}
	if rightPre == "" {
		return -1
	}
	leftIDs := strings.Split(leftPre, ".")
	rightIDs := strings.Split(rightPre, ".")
	for index := 0; index < len(leftIDs) && index < len(rightIDs); index++ {
		leftNumeric, rightNumeric := allDigits(leftIDs[index]), allDigits(rightIDs[index])
		if leftNumeric && rightNumeric {
			if len(leftIDs[index]) != len(rightIDs[index]) {
				if len(leftIDs[index]) < len(rightIDs[index]) {
					return -1
				}
				return 1
			}
		} else if leftNumeric != rightNumeric {
			if leftNumeric {
				return -1
			}
			return 1
		}
		if leftIDs[index] < rightIDs[index] {
			return -1
		}
		if leftIDs[index] > rightIDs[index] {
			return 1
		}
	}
	if len(leftIDs) < len(rightIDs) {
		return -1
	}
	if len(leftIDs) > len(rightIDs) {
		return 1
	}
	return 0
}

func allDigits(value string) bool {
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return value != ""
}
