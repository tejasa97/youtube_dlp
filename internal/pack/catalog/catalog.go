// Package catalog implements a signed, deterministic, offline pack catalog.
package catalog

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/pack"
)

const (
	SchemaV1        = 1
	maximumEntries  = 4096
	maximumBytes    = 1 << 20
	signatureDomain = "ytdlp-go/pack-catalog/v1\x00"
)

var (
	ErrInvalid     = errors.New("invalid pack catalog")
	ErrLimit       = errors.New("pack catalog resource limit exceeded")
	ErrSignature   = errors.New("invalid pack catalog signature")
	ErrUntrusted   = errors.New("untrusted pack catalog publisher")
	ErrExpired     = errors.New("pack catalog expired")
	ErrRevoked     = errors.New("pack catalog entry revoked")
	ErrNotFound    = errors.New("pack catalog entry not found")
	namePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})\.(0|[1-9][0-9]{0,8})(?:-[0-9A-Za-z.-]{1,64})?$`)
)

type Entry struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Artifact       string `json:"artifact"`
	ArchiveSHA256  string `json:"archive_sha256"`
	ArchiveSize    int64  `json:"archive_size"`
	PublisherKeyID string `json:"publisher_key_id"`
}

type Revocation struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	ArchiveSHA256 string `json:"archive_sha256,omitempty"`
}

type Catalog struct {
	SchemaVersion int          `json:"schema_version"`
	GeneratedAt   string       `json:"generated_at"`
	ExpiresAt     string       `json:"expires_at"`
	Entries       []Entry      `json:"entries"`
	Revocations   []Revocation `json:"revocations,omitempty"`
}

type Envelope struct {
	Catalog   json.RawMessage `json:"catalog"`
	Signature pack.Signature  `json:"signature"`
}

type Policy struct {
	Trust       map[string]ed25519.PublicKey
	RevokedKeys map[string]bool
	Now         time.Time
}

func Build(input Catalog, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, ErrInvalid
	}
	normalized, payload, err := normalize(input)
	if err != nil {
		return nil, err
	}
	_ = normalized
	keyID, err := pack.KeyID(privateKey.Public().(ed25519.PublicKey))
	if err != nil {
		return nil, ErrInvalid
	}
	signature := ed25519.Sign(privateKey, append([]byte(signatureDomain), payload...))
	envelope := Envelope{Catalog: payload, Signature: pack.Signature{
		Algorithm: "ed25519", KeyID: keyID, Value: base64.RawStdEncoding.EncodeToString(signature),
	}}
	encoded, err := json.Marshal(envelope)
	if err != nil || len(encoded) > maximumBytes {
		return nil, ErrLimit
	}
	return encoded, nil
}

func Verify(ctx context.Context, encoded []byte, policy Policy) (Catalog, error) {
	if err := contextError(ctx); err != nil {
		return Catalog{}, err
	}
	if len(encoded) == 0 || len(encoded) > maximumBytes || policy.Now.IsZero() {
		return Catalog{}, ErrInvalid
	}
	var envelope Envelope
	if err := decodeStrict(encoded, &envelope); err != nil || envelope.Signature.Algorithm != "ed25519" {
		return Catalog{}, ErrInvalid
	}
	if policy.RevokedKeys[envelope.Signature.KeyID] {
		return Catalog{}, ErrRevoked
	}
	publicKey, ok := policy.Trust[envelope.Signature.KeyID]
	if !ok || len(publicKey) != ed25519.PublicKeySize {
		return Catalog{}, ErrUntrusted
	}
	signature, err := base64.RawStdEncoding.DecodeString(envelope.Signature.Value)
	if err != nil || !ed25519.Verify(publicKey, append([]byte(signatureDomain), envelope.Catalog...), signature) {
		return Catalog{}, ErrSignature
	}
	var parsed Catalog
	if err := decodeStrict(envelope.Catalog, &parsed); err != nil {
		return Catalog{}, ErrInvalid
	}
	normalized, canonical, err := normalize(parsed)
	if err != nil || !bytes.Equal(canonical, envelope.Catalog) {
		return Catalog{}, ErrInvalid
	}
	expires, _ := time.Parse(time.RFC3339, normalized.ExpiresAt)
	generated, _ := time.Parse(time.RFC3339, normalized.GeneratedAt)
	if policy.Now.Before(generated) {
		return Catalog{}, ErrInvalid
	}
	if !policy.Now.Before(expires) {
		return Catalog{}, ErrExpired
	}
	return normalized, nil
}

func Resolve(ctx context.Context, catalog Catalog, name, version string) (Entry, error) {
	if err := contextError(ctx); err != nil {
		return Entry{}, err
	}
	if !namePattern.MatchString(name) || !versionPattern.MatchString(version) {
		return Entry{}, ErrInvalid
	}
	normalized, _, err := normalize(catalog)
	if err != nil {
		return Entry{}, err
	}
	index := sort.Search(len(normalized.Entries), func(index int) bool {
		entry := normalized.Entries[index]
		return entry.Name > name || entry.Name == name && entry.Version >= version
	})
	if index >= len(normalized.Entries) || normalized.Entries[index].Name != name || normalized.Entries[index].Version != version {
		return Entry{}, ErrNotFound
	}
	entry := normalized.Entries[index]
	for _, revoked := range normalized.Revocations {
		if revoked.Name == name && revoked.Version == version && (revoked.ArchiveSHA256 == "" || revoked.ArchiveSHA256 == entry.ArchiveSHA256) {
			return Entry{}, ErrRevoked
		}
	}
	return entry, nil
}

func normalize(input Catalog) (Catalog, []byte, error) {
	if input.SchemaVersion != SchemaV1 {
		return Catalog{}, nil, ErrInvalid
	}
	if len(input.Entries) > maximumEntries || len(input.Revocations) > maximumEntries {
		return Catalog{}, nil, ErrLimit
	}
	generated, err := parseTime(input.GeneratedAt)
	if err != nil {
		return Catalog{}, nil, err
	}
	expires, err := parseTime(input.ExpiresAt)
	if err != nil || !generated.Before(expires) {
		return Catalog{}, nil, ErrInvalid
	}
	input.GeneratedAt = generated.Format(time.RFC3339)
	input.ExpiresAt = expires.Format(time.RFC3339)
	input.Entries = append([]Entry(nil), input.Entries...)
	input.Revocations = append([]Revocation(nil), input.Revocations...)
	sort.Slice(input.Entries, func(i, j int) bool {
		if input.Entries[i].Name != input.Entries[j].Name {
			return input.Entries[i].Name < input.Entries[j].Name
		}
		return input.Entries[i].Version < input.Entries[j].Version
	})
	sort.Slice(input.Revocations, func(i, j int) bool {
		if input.Revocations[i].Name != input.Revocations[j].Name {
			return input.Revocations[i].Name < input.Revocations[j].Name
		}
		if input.Revocations[i].Version != input.Revocations[j].Version {
			return input.Revocations[i].Version < input.Revocations[j].Version
		}
		return input.Revocations[i].ArchiveSHA256 < input.Revocations[j].ArchiveSHA256
	})
	for index, entry := range input.Entries {
		if err := validateEntry(entry); err != nil {
			return Catalog{}, nil, err
		}
		if index > 0 && input.Entries[index-1].Name == entry.Name && input.Entries[index-1].Version == entry.Version {
			return Catalog{}, nil, ErrInvalid
		}
	}
	for index, revoked := range input.Revocations {
		if !namePattern.MatchString(revoked.Name) || !versionPattern.MatchString(revoked.Version) || revoked.ArchiveSHA256 != "" && !validDigest(revoked.ArchiveSHA256) {
			return Catalog{}, nil, ErrInvalid
		}
		if index > 0 && input.Revocations[index-1] == revoked {
			return Catalog{}, nil, ErrInvalid
		}
	}
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > maximumBytes {
		return Catalog{}, nil, ErrLimit
	}
	return input, encoded, nil
}

func validateEntry(entry Entry) error {
	if !namePattern.MatchString(entry.Name) || !versionPattern.MatchString(entry.Version) || !validDigest(entry.ArchiveSHA256) || entry.ArchiveSize <= 0 || entry.ArchiveSize > 72<<20 || !strings.HasPrefix(entry.PublisherKeyID, "ed25519:") || !validDigest(strings.TrimPrefix(entry.PublisherKeyID, "ed25519:")) {
		return ErrInvalid
	}
	if entry.Artifact == "" || entry.Artifact == "." || len(entry.Artifact) > 240 || path.IsAbs(entry.Artifact) || path.Clean(entry.Artifact) != entry.Artifact || strings.HasPrefix(entry.Artifact, "../") || strings.ContainsAny(entry.Artifact, "\\\x00") {
		return ErrInvalid
	}
	return nil
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32 && value == strings.ToLower(value)
}

func parseTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Format(time.RFC3339) != value {
		return time.Time{}, ErrInvalid
	}
	return parsed, nil
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("trailing JSON")
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalid
	}
	return ctx.Err()
}
