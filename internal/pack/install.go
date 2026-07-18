package pack

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	stateSchemaVersion = 1
	maxStateBytes      = 4 << 20
	maxInstallRecords  = 64
	manifestMetaName   = ".ytdlp-pack.manifest.json"
	signatureMetaName  = ".ytdlp-pack.signature.json"
)

type InstalledRecord struct {
	Version        string       `json:"version"`
	PublisherKeyID string       `json:"publisher_key_id"`
	ManifestSHA256 string       `json:"manifest_sha256"`
	ArchiveSHA256  string       `json:"archive_sha256"`
	ArchiveSize    int64        `json:"archive_size"`
	ExpiresAt      string       `json:"expires_at"`
	Permissions    []Permission `json:"permissions,omitempty"`
	InstalledAt    string       `json:"installed_at"`
}

type Transition struct {
	From   string `json:"from,omitempty"`
	To     string `json:"to"`
	Reason string `json:"reason"`
	At     string `json:"at"`
}

type State struct {
	SchemaVersion   int               `json:"schema_version"`
	Name            string            `json:"name"`
	Current         string            `json:"current"`
	Previous        string            `json:"previous,omitempty"`
	Records         []InstalledRecord `json:"records"`
	PendingRemovals []string          `json:"pending_removals,omitempty"`
	LastTransition  Transition        `json:"last_transition"`
}

type InstallOptions struct {
	ApprovePermissionIncrease bool
}

type Receipt struct {
	Path       string
	Verified   Verified
	Review     PermissionReview
	State      State
	Transition Transition
}

// Install verifies before touching disk, extracts into a private staging
// directory, verifies the installed bytes again, and only then atomically
// activates the new state record.
func Install(ctx context.Context, archive []byte, root string, policy VerifyPolicy, options InstallOptions) (Receipt, error) {
	var receipt Receipt
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !secureInstallPlatform() {
		return receipt, ErrPlatformSecurity
	}
	verified, err := Verify(archive, policy)
	if err != nil {
		return receipt, err
	}
	if err := prepareRoot(root); err != nil {
		return receipt, err
	}
	packRoot, err := secureChildDirectory(root, verified.Manifest.Name)
	if err != nil {
		return receipt, err
	}
	if err := syncDirectory(root); err != nil {
		return receipt, err
	}
	unlock, err := acquireLock(packRoot)
	if err != nil {
		return receipt, err
	}
	defer unlock()
	state, err := loadState(packRoot, verified.Manifest.Name)
	if err != nil {
		return receipt, err
	}
	versionsRoot, err := secureChildDirectory(packRoot, "versions")
	if err != nil {
		return receipt, err
	}
	if err := syncDirectory(packRoot); err != nil {
		return receipt, err
	}
	state, err = recoverPendingRemovals(packRoot, versionsRoot, state)
	if err != nil {
		return receipt, err
	}
	if state.Current != "" {
		comparison, compareErr := compareVersions(verified.Manifest.Version, state.Current)
		if compareErr != nil {
			return receipt, compareErr
		}
		if comparison < 0 {
			return receipt, ErrDowngrade
		}
		if comparison == 0 || recordFor(state, verified.Manifest.Version) != nil {
			return receipt, ErrAlreadyInstalled
		}
	}
	var previousPermissions []Permission
	if current := recordFor(state, state.Current); current != nil {
		previousPermissions = current.Permissions
	}
	review := ReviewPermissions(previousPermissions, verified.Manifest.Permissions)
	if review.Increase() && !options.ApprovePermissionIncrease {
		return Receipt{Verified: verified, Review: review, State: state}, ErrPermissionReview
	}
	if err := cleanupStaging(versionsRoot); err != nil {
		return receipt, err
	}
	destination := filepath.Join(versionsRoot, verified.Manifest.Version)
	if _, err := os.Lstat(destination); err == nil {
		// A crash after the version-directory rename but before state
		// publication leaves an inactive orphan. Recover only when the signed
		// bytes exactly match this requested archive.
		orphanPolicy := policy
		orphanPolicy.CurrentVersion = ""
		orphan, verifyErr := verifyInstalled(destination, orphanPolicy)
		if verifyErr != nil || orphan.ArchiveSHA256 != verified.ArchiveSHA256 {
			return receipt, ErrCorruptInstall
		}
		if err := os.RemoveAll(destination); err != nil {
			return receipt, fmt.Errorf("%w: remove inactive version", ErrIO)
		}
		if err := syncDirectory(versionsRoot); err != nil {
			return receipt, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return receipt, fmt.Errorf("%w: inspect destination", ErrIO)
	}
	staging, err := os.MkdirTemp(versionsRoot, ".stage-")
	if err != nil {
		return receipt, fmt.Errorf("%w: create staging directory", ErrIO)
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		os.RemoveAll(staging)
		return receipt, fmt.Errorf("%w: secure staging directory", ErrIO)
	}
	staged := true
	defer func() {
		if staged {
			_ = os.RemoveAll(staging)
			_ = syncDirectory(versionsRoot)
		}
	}()
	if err := extractVerified(ctx, staging, verified); err != nil {
		return receipt, err
	}
	if err := syncTreeDirectories(staging); err != nil {
		return receipt, err
	}
	if err := os.Rename(staging, destination); err != nil {
		return receipt, fmt.Errorf("%w: activate staged version", ErrIO)
	}
	if err := syncDirectory(versionsRoot); err != nil {
		_ = os.RemoveAll(destination)
		_ = syncDirectory(versionsRoot)
		return receipt, err
	}
	staged = false
	cleanupDestination := true
	defer func() {
		if cleanupDestination {
			_ = os.RemoveAll(destination)
			_ = syncDirectory(versionsRoot)
		}
	}()
	recheckPolicy := policy
	recheckPolicy.CurrentVersion = ""
	rechecked, err := verifyInstalled(destination, recheckPolicy)
	if err != nil || rechecked.ArchiveSHA256 != verified.ArchiveSHA256 {
		if err == nil {
			err = ErrCorruptInstall
		}
		removeErr := os.RemoveAll(destination)
		syncErr := syncDirectory(versionsRoot)
		cleanupDestination = false
		if removeErr != nil {
			removeErr = fmt.Errorf("%w: remove failed activation", ErrIO)
		}
		return receipt, errors.Join(err, removeErr, syncErr)
	}
	now := policy.Now.UTC().Format(time.RFC3339)
	record := InstalledRecord{
		Version: verified.Manifest.Version, PublisherKeyID: verified.Manifest.PublisherKeyID,
		ManifestSHA256: verified.ManifestSHA256, ArchiveSHA256: verified.ArchiveSHA256,
		ArchiveSize: verified.ArchiveSize, ExpiresAt: verified.Manifest.ExpiresAt,
		Permissions: append([]Permission(nil), verified.Manifest.Permissions...), InstalledAt: now,
	}
	state.SchemaVersion = stateSchemaVersion
	state.Name = verified.Manifest.Name
	state.Previous = state.Current
	from := state.Current
	state.Current = verified.Manifest.Version
	state.Records = append(state.Records, record)
	if len(state.Records) > maxInstallRecords {
		state.Records = append([]InstalledRecord(nil), state.Records[len(state.Records)-maxInstallRecords:]...)
	}
	transition := Transition{From: from, To: state.Current, Reason: "install", At: now}
	state.LastTransition = transition
	if err := writeState(packRoot, state); err != nil {
		_ = os.RemoveAll(destination)
		_ = syncDirectory(versionsRoot)
		cleanupDestination = false
		return receipt, err
	}
	cleanupDestination = false
	return Receipt{Path: destination, Verified: verified, Review: review, State: state, Transition: transition}, nil
}

type RollbackOptions struct {
	ApprovePermissionIncrease bool
	// Validate, when set by a product integration, binds the verified pack to
	// an additional runtime contract before activation.
	Validate func(Verified) error
}

// Rollback activates the previous version with fail-closed permission review.
// Use RollbackWithOptions to explicitly approve a permission increase.
func Rollback(ctx context.Context, root, name string, policy VerifyPolicy) (Receipt, error) {
	return RollbackWithOptions(ctx, root, name, policy, RollbackOptions{})
}

// RollbackWithOptions activates the previous version only after reconstructing
// and verifying its signed canonical archive and all installed payload digests.
func RollbackWithOptions(ctx context.Context, root, name string, policy VerifyPolicy, options RollbackOptions) (Receipt, error) {
	var receipt Receipt
	if err := ctx.Err(); err != nil {
		return receipt, err
	}
	if !secureInstallPlatform() {
		return receipt, ErrPlatformSecurity
	}
	if !validName(name) || policy.Now.IsZero() {
		return receipt, ErrInvalidManifest
	}
	if err := validateExistingRoot(root); err != nil {
		return receipt, err
	}
	packRoot := filepath.Join(root, name)
	if err := validateSecureDirectory(packRoot); err != nil {
		return receipt, err
	}
	unlock, err := acquireLock(packRoot)
	if err != nil {
		return receipt, err
	}
	defer unlock()
	state, err := loadState(packRoot, name)
	if err != nil {
		return receipt, err
	}
	versionsRoot := filepath.Join(packRoot, "versions")
	if err := validateSecureDirectory(versionsRoot); err != nil {
		return receipt, err
	}
	state, err = recoverPendingRemovals(packRoot, versionsRoot, state)
	if err != nil {
		return receipt, err
	}
	if state.Current == "" || state.Previous == "" {
		return receipt, fmt.Errorf("%w: no rollback target", ErrCorruptInstall)
	}
	target := filepath.Join(versionsRoot, state.Previous)
	if err := validateSecureDirectory(target); err != nil {
		return receipt, err
	}
	rollbackPolicy := policy
	rollbackPolicy.CurrentVersion = ""
	verified, err := verifyInstalled(target, rollbackPolicy)
	if err != nil {
		return receipt, err
	}
	if options.Validate != nil {
		if err := options.Validate(verified); err != nil {
			return receipt, err
		}
	}
	record := recordFor(state, state.Previous)
	if record == nil || record.ArchiveSHA256 != verified.ArchiveSHA256 || record.ManifestSHA256 != verified.ManifestSHA256 || record.ArchiveSize != verified.ArchiveSize {
		return receipt, ErrCorruptInstall
	}
	from, to := state.Current, state.Previous
	review := PermissionReview{}
	if oldRecord := recordFor(state, from); oldRecord != nil {
		review = ReviewPermissions(oldRecord.Permissions, record.Permissions)
	}
	if review.Increase() && !options.ApprovePermissionIncrease {
		return Receipt{Path: target, Verified: verified, Review: review, State: state}, ErrPermissionReview
	}
	state.Current, state.Previous = to, from
	transition := Transition{From: from, To: to, Reason: "rollback", At: policy.Now.UTC().Format(time.RFC3339)}
	state.LastTransition = transition
	if err := writeState(packRoot, state); err != nil {
		return receipt, err
	}
	return Receipt{Path: target, Verified: verified, Review: review, State: state, Transition: transition}, nil
}

func extractVerified(ctx context.Context, staging string, verified Verified) error {
	for _, file := range verified.Manifest.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		destination, err := securePayloadDestination(staging, file.Path)
		if err != nil {
			return err
		}
		if err := writeExclusive(destination, verified.Payload[file.Path], os.FileMode(file.Mode)); err != nil {
			return err
		}
	}
	if err := writeExclusive(filepath.Join(staging, manifestMetaName), verified.ManifestBytes, 0o600); err != nil {
		return err
	}
	signatureBytes, err := json.Marshal(verified.Signature)
	if err != nil {
		return fmt.Errorf("%w: encode signature metadata", ErrIO)
	}
	return writeExclusive(filepath.Join(staging, signatureMetaName), signatureBytes, 0o600)
}

func verifyInstalled(versionRoot string, policy VerifyPolicy) (Verified, error) {
	manifestBytes, err := readRegular(filepath.Join(versionRoot, manifestMetaName), maxManifestBytes)
	if err != nil {
		return Verified{}, err
	}
	signatureBytes, err := readRegular(filepath.Join(versionRoot, signatureMetaName), maxSignatureBytes)
	if err != nil {
		return Verified{}, err
	}
	manifest, err := decodeManifest(manifestBytes)
	if err != nil {
		return Verified{}, err
	}
	if err := verifyInstalledTree(versionRoot, manifest); err != nil {
		return Verified{}, err
	}
	payload := make(map[string][]byte, len(manifest.Files))
	for _, file := range manifest.Files {
		destination, pathErr := existingPayloadDestination(versionRoot, file.Path)
		if pathErr != nil {
			return Verified{}, pathErr
		}
		info, statErr := os.Lstat(destination)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != os.FileMode(file.Mode).Perm() || !secureOwnership(info) || !singleLink(info) {
			return Verified{}, ErrCorruptInstall
		}
		body, readErr := readRegular(destination, int(file.Size))
		if readErr != nil {
			return Verified{}, readErr
		}
		payload[file.Path] = body
	}
	archive, err := encodeArchive(manifestBytes, signatureBytes, manifest.Files, payload)
	if err != nil {
		return Verified{}, err
	}
	verified, err := Verify(archive, policy)
	if err != nil {
		return Verified{}, err
	}
	return verified, nil
}

func cleanupStaging(versionsRoot string) error {
	entries, err := os.ReadDir(versionsRoot)
	if err != nil {
		return fmt.Errorf("%w: inspect staging directory", ErrIO)
	}
	changed := false
	for _, entry := range entries {
		isStage := strings.HasPrefix(entry.Name(), ".stage-")
		isRemoval := strings.HasPrefix(entry.Name(), ".remove-")
		if !isStage && !isRemoval {
			continue
		}
		path := filepath.Join(versionsRoot, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !secureOwnership(info) {
			return ErrUnsafePath
		}
		if isStage && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return ErrUnsafePath
		}
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("%w: remove abandoned staging directory", ErrIO)
		}
		changed = true
	}
	if changed {
		return syncDirectory(versionsRoot)
	}
	return nil
}

func verifyInstalledTree(versionRoot string, manifest Manifest) error {
	expectedFiles := map[string]os.FileMode{manifestMetaName: 0o600, signatureMetaName: 0o600}
	expectedDirectories := map[string]struct{}{".": {}}
	for _, file := range manifest.Files {
		relative := filepath.FromSlash(file.Path)
		expectedFiles[relative] = os.FileMode(file.Mode)
		for parent := filepath.Dir(relative); parent != "."; parent = filepath.Dir(parent) {
			expectedDirectories[parent] = struct{}{}
		}
	}
	return filepath.WalkDir(versionRoot, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return ErrCorruptInstall
		}
		relative, err := filepath.Rel(versionRoot, current)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrCorruptInstall
		}
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !secureOwnership(info) {
			return ErrCorruptInstall
		}
		if entry.IsDir() {
			if _, exists := expectedDirectories[relative]; !exists || info.Mode().Perm() != 0o700 {
				return ErrCorruptInstall
			}
			return nil
		}
		mode, exists := expectedFiles[relative]
		if !exists || !info.Mode().IsRegular() || info.Mode().Perm() != mode.Perm() || !singleLink(info) {
			return ErrCorruptInstall
		}
		return nil
	})
}

func prepareRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return ErrUnsafePath
	}
	if _, err := os.Lstat(root); err == nil {
		return validateExistingRoot(root)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect install root", ErrIO)
	}
	parent := filepath.Dir(root)
	if err := validateSecureDirectory(parent); err != nil {
		return fmt.Errorf("%w: install-root parent must already be a secure directory", ErrUnsafePath)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return fmt.Errorf("%w: create install root", ErrIO)
	}
	if err := validateExistingRoot(root); err != nil {
		return err
	}
	if err := syncDirectory(root); err != nil {
		return err
	}
	return syncDirectory(parent)
}

func validateExistingRoot(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return ErrUnsafePath
	}
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("%w: resolve install root", ErrUnsafePath)
	}
	if real != root {
		return fmt.Errorf("%w: install root is not canonical", ErrUnsafePath)
	}
	return validateSecureDirectory(root)
}

func validateSecureDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("%w: inspect directory", ErrIO)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || !secureOwnership(info) {
		return ErrUnsafePath
	}
	return nil
}

func secureChildDirectory(parent, name string) (string, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return "", ErrUnsafePath
	}
	if err := validateSecureDirectory(parent); err != nil {
		return "", err
	}
	destination := filepath.Join(parent, name)
	info, err := os.Lstat(destination)
	created := false
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(destination, 0o700); err != nil {
			return "", fmt.Errorf("%w: create directory", ErrIO)
		}
		created = true
		info, err = os.Lstat(destination)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !secureOwnership(info) {
		return "", ErrUnsafePath
	}
	if created {
		if err := os.Chmod(destination, 0o700); err != nil {
			return "", fmt.Errorf("%w: secure directory", ErrIO)
		}
		info, err = os.Lstat(destination)
		if err != nil {
			return "", fmt.Errorf("%w: inspect directory", ErrIO)
		}
	}
	if info.Mode().Perm() != 0o700 {
		return "", ErrUnsafePath
	}
	return destination, nil
}

func securePayloadDestination(root, slashPath string) (string, error) {
	if err := validatePayloadPath(slashPath); err != nil {
		return "", err
	}
	parts := strings.Split(slashPath, "/")
	parent := root
	for _, part := range parts[:len(parts)-1] {
		next, err := secureChildDirectory(parent, part)
		if err != nil {
			return "", err
		}
		parent = next
	}
	return filepath.Join(parent, parts[len(parts)-1]), nil
}

func existingPayloadDestination(root, slashPath string) (string, error) {
	if err := validatePayloadPath(slashPath); err != nil {
		return "", err
	}
	current := root
	for _, part := range strings.Split(slashPath, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !secureOwnership(info) {
			return "", ErrCorruptInstall
		}
	}
	return current, nil
}

func writeExclusive(path string, body []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("%w: create file", ErrIO)
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(body); err != nil {
		return fmt.Errorf("%w: write file", ErrIO)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("%w: sync file", ErrIO)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("%w: close file", ErrIO)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != mode.Perm() || !secureOwnership(info) || !singleLink(info) {
		return ErrUnsafePath
	}
	ok = true
	return nil
}

func readRegular(path string, limit int) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o077 != 0 || !secureOwnership(before) || !singleLink(before) {
		return nil, ErrUnsafePath
	}
	if before.Size() < 0 || before.Size() > int64(limit) {
		return nil, ErrResourceLimit
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open file", ErrIO)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return nil, ErrUnsafePath
	}
	body, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: read file", ErrIO)
	}
	if len(body) > limit {
		return nil, ErrResourceLimit
	}
	return body, nil
}

func loadState(packRoot, name string) (State, error) {
	path := filepath.Join(packRoot, "state.json")
	body, err := readRegular(path, maxStateBytes)
	if errors.Is(err, ErrUnsafePath) {
		if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
			return State{SchemaVersion: stateSchemaVersion, Name: name}, nil
		}
	}
	if err != nil {
		return State{}, err
	}
	var state State
	if err := decodeStrict(body, &state); err != nil || validateState(state, name) != nil {
		return State{}, ErrCorruptInstall
	}
	return state, nil
}

func validateState(state State, name string) error {
	if state.SchemaVersion != stateSchemaVersion || state.Name != name || len(state.Records) > maxInstallRecords || len(state.PendingRemovals) > maxInstallRecords {
		return ErrCorruptInstall
	}
	seen := make(map[string]struct{}, len(state.Records))
	for _, record := range state.Records {
		if _, err := parseVersion(record.Version); err != nil || !validDigest(record.ManifestSHA256) || !validDigest(record.ArchiveSHA256) || record.ArchiveSize <= 0 || record.ArchiveSize > maxArchiveBytes {
			return ErrCorruptInstall
		}
		if len(record.PublisherKeyID) != len("ed25519:")+sha256.Size*2 || !strings.HasPrefix(record.PublisherKeyID, "ed25519:") {
			return ErrCorruptInstall
		}
		if _, err := hex.DecodeString(strings.TrimPrefix(record.PublisherKeyID, "ed25519:")); err != nil {
			return ErrCorruptInstall
		}
		if _, err := parseTimestamp(record.ExpiresAt); err != nil {
			return ErrCorruptInstall
		}
		if _, err := parseTimestamp(record.InstalledAt); err != nil {
			return ErrCorruptInstall
		}
		if len(record.Permissions) > maxPermissions {
			return ErrCorruptInstall
		}
		if err := validatePermissions(record.Permissions, true); err != nil {
			return ErrCorruptInstall
		}
		if _, duplicate := seen[record.Version]; duplicate {
			return ErrCorruptInstall
		}
		seen[record.Version] = struct{}{}
	}
	if state.Current != "" {
		if _, exists := seen[state.Current]; !exists {
			return ErrCorruptInstall
		}
	}
	if state.Previous != "" {
		if _, exists := seen[state.Previous]; !exists || state.Previous == state.Current {
			return ErrCorruptInstall
		}
	}
	if state.LastTransition.Reason != "" {
		if state.LastTransition.Reason != "install" && state.LastTransition.Reason != "rollback" && state.LastTransition.Reason != "remove" {
			return ErrCorruptInstall
		}
		if _, err := parseTimestamp(state.LastTransition.At); err != nil {
			return ErrCorruptInstall
		}
		if state.LastTransition.From != "" {
			if _, err := parseVersion(state.LastTransition.From); err != nil {
				return ErrCorruptInstall
			}
		}
		if state.LastTransition.To != "" {
			if _, err := parseVersion(state.LastTransition.To); err != nil {
				return ErrCorruptInstall
			}
		}
	}
	pending := make(map[string]struct{}, len(state.PendingRemovals))
	for _, version := range state.PendingRemovals {
		if _, err := parseVersion(version); err != nil {
			return ErrCorruptInstall
		}
		if _, duplicate := pending[version]; duplicate {
			return ErrCorruptInstall
		}
		if _, active := seen[version]; active {
			return ErrCorruptInstall
		}
		pending[version] = struct{}{}
	}
	return nil
}

func recoverPendingRemovals(packRoot, versionsRoot string, state State) (State, error) {
	if len(state.PendingRemovals) == 0 {
		return state, nil
	}
	for _, version := range state.PendingRemovals {
		if err := removeVersionDurably(versionsRoot, version); err != nil {
			return State{}, err
		}
	}
	state.PendingRemovals = nil
	if err := writeState(packRoot, state); err != nil {
		return State{}, err
	}
	return state, nil
}

func removeVersionDurably(versionsRoot, version string) error {
	if _, err := parseVersion(version); err != nil {
		return ErrCorruptInstall
	}
	target := filepath.Join(versionsRoot, version)
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		return cleanupStaging(versionsRoot)
	} else if err != nil {
		return fmt.Errorf("%w: inspect removal target", ErrIO)
	}
	placeholder, err := os.MkdirTemp(versionsRoot, ".remove-")
	if err != nil {
		return fmt.Errorf("%w: create removal quarantine", ErrIO)
	}
	if err := os.Remove(placeholder); err != nil {
		return fmt.Errorf("%w: prepare removal quarantine", ErrIO)
	}
	if err := os.Rename(target, placeholder); err != nil {
		return fmt.Errorf("%w: quarantine removed version", ErrIO)
	}
	if err := syncDirectory(versionsRoot); err != nil {
		return err
	}
	if err := os.RemoveAll(placeholder); err != nil {
		return fmt.Errorf("%w: remove quarantined version", ErrIO)
	}
	return syncDirectory(versionsRoot)
}

func validDigest(input string) bool {
	if len(input) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(input)
	return err == nil
}

func writeState(packRoot string, state State) error {
	if err := validateState(state, state.Name); err != nil {
		return err
	}
	encoded, err := json.Marshal(state)
	if err != nil || len(encoded) > maxStateBytes {
		return ErrResourceLimit
	}
	temporary, err := os.CreateTemp(packRoot, ".state-")
	if err != nil {
		return fmt.Errorf("%w: create temporary state", ErrIO)
	}
	temporaryPath := temporary.Name()
	defer func() {
		if removeErr := os.Remove(temporaryPath); removeErr == nil {
			_ = syncDirectory(packRoot)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: secure temporary state", ErrIO)
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: write temporary state", ErrIO)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: sync temporary state", ErrIO)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close temporary state", ErrIO)
	}
	destination := filepath.Join(packRoot, "state.json")
	if info, err := os.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !secureOwnership(info) {
			return ErrUnsafePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect state", ErrIO)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("%w: atomically replace state", ErrIO)
	}
	return syncDirectory(packRoot)
}

func recordFor(state State, version string) *InstalledRecord {
	for index := range state.Records {
		if state.Records[index].Version == version {
			return &state.Records[index]
		}
	}
	return nil
}
