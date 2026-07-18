package pack

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

var deterministicZipTime = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// Build creates a byte-for-byte deterministic archive. The caller supplies a
// private key explicitly; production key discovery and selection are outside
// this package by design.
func Build(input Manifest, payload map[string]Payload, privateKey ed25519.PrivateKey) ([]byte, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid Ed25519 private key", ErrInvalidManifest)
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	keyID, err := KeyID(publicKey)
	if err != nil {
		return nil, err
	}
	if input.PublisherKeyID == "" {
		input.PublisherKeyID = keyID
	}
	if input.PublisherKeyID != keyID {
		return nil, fmt.Errorf("%w: publisher key mismatch", ErrInvalidManifest)
	}
	if len(payload) == 0 || len(payload) > maxFiles {
		return nil, ErrResourceLimit
	}
	input.Files = make([]File, 0, len(payload))
	var total int64
	for filePath, content := range payload {
		if err := validatePayloadPath(filePath); err != nil {
			return nil, err
		}
		if int64(len(content.Bytes)) > maxFileBytes || total > maxPayloadBytes-int64(len(content.Bytes)) {
			return nil, ErrResourceLimit
		}
		total += int64(len(content.Bytes))
		mode := content.Mode
		if mode == 0 {
			mode = 0o600
		}
		digest := sha256.Sum256(content.Bytes)
		input.Files = append(input.Files, File{Path: filePath, Size: int64(len(content.Bytes)), SHA256: hex.EncodeToString(digest[:]), Mode: mode})
	}
	manifest, manifestBytes, err := normalizeManifest(input)
	if err != nil {
		return nil, err
	}
	signatureValue := ed25519.Sign(privateKey, append([]byte(signatureDomain), manifestBytes...))
	signatureBytes, err := json.Marshal(Signature{Algorithm: "Ed25519", KeyID: manifest.PublisherKeyID, Value: base64.StdEncoding.EncodeToString(signatureValue)})
	if err != nil || len(signatureBytes) > maxSignatureBytes {
		return nil, fmt.Errorf("%w: encode signature record", ErrInvalidManifest)
	}
	canonicalPayload := make(map[string][]byte, len(payload))
	for filePath, content := range payload {
		canonicalPayload[filePath] = content.Bytes
	}
	return encodeArchive(manifestBytes, signatureBytes, manifest.Files, canonicalPayload)
}

func encodeArchive(manifestBytes, signatureBytes []byte, files []File, payload map[string][]byte) ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	write := func(name string, mode uint32, body []byte) error {
		header := &zip.FileHeader{Name: name, Method: zip.Store}
		header.Modified = deterministicZipTime
		header.SetMode(os.FileMode(mode))
		entry, createErr := writer.CreateHeader(header)
		if createErr != nil {
			return createErr
		}
		_, createErr = entry.Write(body)
		return createErr
	}
	if err := write("manifest.json", 0o600, manifestBytes); err != nil {
		return nil, fmt.Errorf("%w: write manifest", ErrIO)
	}
	if err := write("signature.json", 0o600, signatureBytes); err != nil {
		return nil, fmt.Errorf("%w: write signature", ErrIO)
	}
	for _, file := range files {
		if err := write("payload/"+file.Path, file.Mode, payload[file.Path]); err != nil {
			return nil, fmt.Errorf("%w: write payload", ErrIO)
		}
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("%w: finalize archive", ErrIO)
	}
	if output.Len() > maxArchiveBytes {
		return nil, ErrResourceLimit
	}
	return output.Bytes(), nil
}

// VerifyPolicy is fail-closed: Trust must contain the exact derived key ID.
// Now is mandatory so expiration checks cannot accidentally depend on a hidden
// clock in tests or update transactions.
type VerifyPolicy struct {
	Trust          map[string]ed25519.PublicKey
	Now            time.Time
	HostVersion    string
	CurrentVersion string
	Revocations    Revocations
}

type PackageRevocation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type Revocations struct {
	KeyIDs         []string            `json:"key_ids,omitempty"`
	ManifestSHA256 []string            `json:"manifest_sha256,omitempty"`
	Packages       []PackageRevocation `json:"packages,omitempty"`
}

type PermissionReview struct {
	Previous  []Permission `json:"previous,omitempty"`
	Requested []Permission `json:"requested,omitempty"`
	Added     []Permission `json:"added,omitempty"`
	Removed   []Permission `json:"removed,omitempty"`
}

func (review PermissionReview) Increase() bool { return len(review.Added) != 0 }

type Verified struct {
	Manifest       Manifest
	Signature      Signature
	Payload        map[string][]byte
	ManifestBytes  []byte
	ManifestSHA256 string
	ArchiveSHA256  string
	ArchiveSize    int64
}

func Verify(archive []byte, policy VerifyPolicy) (Verified, error) {
	var result Verified
	if len(archive) == 0 {
		return result, ErrInvalidArchive
	}
	if len(archive) > maxArchiveBytes {
		return result, ErrResourceLimit
	}
	if policy.Now.IsZero() {
		return result, fmt.Errorf("%w: verification time is required", ErrInvalidManifest)
	}
	if err := validateRevocations(policy.Revocations); err != nil {
		return result, err
	}
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return result, fmt.Errorf("%w: malformed ZIP", ErrInvalidArchive)
	}
	if len(reader.File) < 3 || len(reader.File) > maxFiles+2 || reader.Comment != "" {
		return result, ErrInvalidArchive
	}
	entries := make(map[string]*zip.File, len(reader.File))
	for _, entry := range reader.File {
		if _, duplicate := entries[entry.Name]; duplicate {
			return result, fmt.Errorf("%w: duplicate entry", ErrInvalidArchive)
		}
		if entry.Method != zip.Store || entry.FileInfo().IsDir() || !entry.Mode().IsRegular() || entry.Comment != "" || entry.Flags&^uint16(0x808) != 0 {
			return result, fmt.Errorf("%w: unsupported ZIP entry", ErrInvalidArchive)
		}
		entries[entry.Name] = entry
	}
	manifestBytes, err := readEntry(entries["manifest.json"], maxManifestBytes)
	if err != nil {
		return result, err
	}
	signatureBytes, err := readEntry(entries["signature.json"], maxSignatureBytes)
	if err != nil {
		return result, err
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return result, err
	}
	canonical, canonicalBytes, err := normalizeManifest(manifest)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(manifestBytes, canonicalBytes) {
		return result, fmt.Errorf("%w: non-canonical manifest", ErrInvalidArchive)
	}
	manifestDigest := sha256.Sum256(manifestBytes)
	manifestHash := hex.EncodeToString(manifestDigest[:])
	var signature Signature
	if err := decodeStrict(signatureBytes, &signature); err != nil || signature.Algorithm != "Ed25519" || signature.KeyID != canonical.PublisherKeyID {
		return result, fmt.Errorf("%w: malformed signature record", ErrSignature)
	}
	canonicalSignature, err := json.Marshal(signature)
	if err != nil || !bytes.Equal(canonicalSignature, signatureBytes) {
		return result, fmt.Errorf("%w: non-canonical signature record", ErrSignature)
	}
	decodedSignature, err := base64.StdEncoding.Strict().DecodeString(signature.Value)
	if err != nil || len(decodedSignature) != ed25519.SignatureSize {
		return result, fmt.Errorf("%w: malformed signature value", ErrSignature)
	}
	publicKey, trusted := policy.Trust[signature.KeyID]
	if !trusted {
		return result, ErrUntrustedPublisher
	}
	derived, err := KeyID(publicKey)
	if err != nil || derived != signature.KeyID {
		return result, ErrUntrustedPublisher
	}
	if !ed25519.Verify(publicKey, append([]byte(signatureDomain), manifestBytes...), decodedSignature) {
		return result, ErrSignature
	}
	if revoked(canonical, manifestHash, policy.Revocations) {
		return result, ErrRevoked
	}
	created, _ := parseTimestamp(canonical.CreatedAt)
	expires, _ := parseTimestamp(canonical.ExpiresAt)
	now := policy.Now.UTC()
	if now.Before(created) {
		return result, ErrNotYetValid
	}
	if !now.Before(expires) {
		return result, ErrExpired
	}
	if policy.HostVersion != "" && canonical.MinHostVersion != "" {
		comparison, compareErr := compareVersions(policy.HostVersion, canonical.MinHostVersion)
		if compareErr != nil {
			return result, compareErr
		}
		if comparison < 0 {
			return result, ErrIncompatibleHost
		}
	}
	if policy.CurrentVersion != "" {
		comparison, compareErr := compareVersions(canonical.Version, policy.CurrentVersion)
		if compareErr != nil {
			return result, compareErr
		}
		if comparison < 0 {
			return result, ErrDowngrade
		}
	}
	payload := make(map[string][]byte, len(canonical.Files))
	for _, file := range canonical.Files {
		entryName := "payload/" + file.Path
		entry, exists := entries[entryName]
		if !exists || entry.UncompressedSize64 != uint64(file.Size) || entry.Mode().Perm() != os.FileMode(file.Mode).Perm() {
			return result, fmt.Errorf("%w: payload metadata mismatch", ErrInvalidArchive)
		}
		body, readErr := readEntry(entry, int(file.Size))
		if readErr != nil || int64(len(body)) != file.Size {
			return result, fmt.Errorf("%w: payload length mismatch", ErrInvalidArchive)
		}
		digest := sha256.Sum256(body)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			return result, fmt.Errorf("%w: payload digest mismatch", ErrSignature)
		}
		payload[file.Path] = body
		delete(entries, entryName)
	}
	delete(entries, "manifest.json")
	delete(entries, "signature.json")
	if len(entries) != 0 {
		return result, fmt.Errorf("%w: undeclared entry", ErrInvalidArchive)
	}
	canonicalArchive, err := encodeArchive(manifestBytes, signatureBytes, canonical.Files, payload)
	if err != nil {
		return result, err
	}
	if !bytes.Equal(canonicalArchive, archive) {
		return result, fmt.Errorf("%w: non-canonical archive", ErrInvalidArchive)
	}
	archiveDigest := sha256.Sum256(archive)
	result = Verified{Manifest: canonical, Signature: signature, Payload: payload, ManifestBytes: append([]byte(nil), manifestBytes...), ManifestSHA256: manifestHash, ArchiveSHA256: hex.EncodeToString(archiveDigest[:]), ArchiveSize: int64(len(archive))}
	return result, nil
}

func decodeManifest(encoded []byte) (Manifest, error) {
	var manifest Manifest
	if err := decodeStrict(encoded, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("%w: malformed manifest JSON", ErrInvalidManifest)
	}
	return manifest, nil
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON data")
	}
	return nil
}

func readEntry(entry *zip.File, limit int) ([]byte, error) {
	if entry == nil {
		return nil, fmt.Errorf("%w: missing required entry", ErrInvalidArchive)
	}
	if limit < 0 || entry.UncompressedSize64 > uint64(limit) || entry.CompressedSize64 > uint64(maxArchiveBytes) {
		return nil, ErrResourceLimit
	}
	reader, err := entry.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open entry", ErrInvalidArchive)
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read entry", ErrInvalidArchive)
	}
	if len(body) > limit {
		return nil, ErrResourceLimit
	}
	return body, nil
}

func revoked(manifest Manifest, manifestHash string, revocations Revocations) bool {
	for _, keyID := range revocations.KeyIDs {
		if keyID == manifest.PublisherKeyID {
			return true
		}
	}
	for _, digest := range revocations.ManifestSHA256 {
		if digest == manifestHash {
			return true
		}
	}
	for _, item := range revocations.Packages {
		if item.Name == manifest.Name && item.Version == manifest.Version {
			return true
		}
	}
	return false
}

func validateRevocations(revocations Revocations) error {
	if len(revocations.KeyIDs) > 4096 || len(revocations.ManifestSHA256) > 4096 || len(revocations.Packages) > 4096 {
		return ErrResourceLimit
	}
	seen := make(map[string]struct{}, len(revocations.KeyIDs)+len(revocations.ManifestSHA256)+len(revocations.Packages))
	for _, keyID := range revocations.KeyIDs {
		if len(keyID) != len("ed25519:")+sha256.Size*2 || !strings.HasPrefix(keyID, "ed25519:") {
			return ErrInvalidRevocations
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(keyID, "ed25519:")); err != nil {
			return ErrInvalidRevocations
		}
		if _, duplicate := seen["key:"+keyID]; duplicate {
			return ErrInvalidRevocations
		}
		seen["key:"+keyID] = struct{}{}
	}
	for _, digest := range revocations.ManifestSHA256 {
		if len(digest) != sha256.Size*2 {
			return ErrInvalidRevocations
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return ErrInvalidRevocations
		}
		if _, duplicate := seen["digest:"+digest]; duplicate {
			return ErrInvalidRevocations
		}
		seen["digest:"+digest] = struct{}{}
	}
	for _, item := range revocations.Packages {
		if !validName(item.Name) {
			return ErrInvalidRevocations
		}
		if _, err := parseVersion(item.Version); err != nil {
			return ErrInvalidRevocations
		}
		identity := "package:" + item.Name + "@" + item.Version
		if _, duplicate := seen[identity]; duplicate {
			return ErrInvalidRevocations
		}
		seen[identity] = struct{}{}
	}
	return nil
}

func ReviewPermissions(previous, requested []Permission) PermissionReview {
	review := PermissionReview{Previous: append([]Permission(nil), previous...), Requested: append([]Permission(nil), requested...)}
	old := make(map[Permission]struct{}, len(previous))
	newPermissions := make(map[Permission]struct{}, len(requested))
	for _, permission := range previous {
		old[permission] = struct{}{}
	}
	for _, permission := range requested {
		newPermissions[permission] = struct{}{}
		if _, exists := old[permission]; !exists {
			review.Added = append(review.Added, permission)
		}
	}
	for _, permission := range previous {
		if _, exists := newPermissions[permission]; !exists {
			review.Removed = append(review.Removed, permission)
		}
	}
	sort.Slice(review.Previous, func(i, j int) bool { return review.Previous[i] < review.Previous[j] })
	sort.Slice(review.Requested, func(i, j int) bool { return review.Requested[i] < review.Requested[j] })
	sort.Slice(review.Added, func(i, j int) bool { return review.Added[i] < review.Added[j] })
	sort.Slice(review.Removed, func(i, j int) bool { return review.Removed[i] < review.Removed[j] })
	return review
}
