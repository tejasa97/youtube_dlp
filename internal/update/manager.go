package update

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	defaultMaxArtifact int64 = 512 << 20
	maxStateBytes            = 64 << 10
	maxJournalBytes          = 128 << 10
)

type Installed struct {
	Version    string `json:"version"`
	Artifact   string `json:"artifact"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Generation uint64 `json:"generation"`
}

type State struct {
	Product           string     `json:"product"`
	Channel           Channel    `json:"channel"`
	GOOS              string     `json:"goos"`
	GOARCH            string     `json:"goarch"`
	HighestGeneration uint64     `json:"highest_generation"`
	Active            *Installed `json:"active,omitempty"`
	Previous          *Installed `json:"previous,omitempty"`
}

type HealthChecker interface {
	Check(context.Context, string, Target) error
}

type HealthCheckFunc func(context.Context, string, Target) error

func (function HealthCheckFunc) Check(ctx context.Context, path string, target Target) error {
	return function(ctx, path, target)
}

type Options struct {
	Trust           Root
	Product         string
	Channel         Channel
	GOOS            string
	GOARCH          string
	Clock           func() time.Time
	Health          HealthChecker
	MaxArtifactSize int64
	LockPoll        time.Duration
	StaleLockAfter  time.Duration
}

func (options Options) withDefaults() Options {
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.GOOS == "" {
		options.GOOS = runtime.GOOS
	}
	if options.GOARCH == "" {
		options.GOARCH = runtime.GOARCH
	}
	if options.MaxArtifactSize <= 0 {
		options.MaxArtifactSize = defaultMaxArtifact
	}
	if options.LockPoll <= 0 {
		options.LockPoll = 20 * time.Millisecond
	}
	if options.StaleLockAfter <= 0 {
		options.StaleLockAfter = 10 * time.Minute
	}
	return options
}

// Manager stores immutable releases below a private root and atomically
// switches a checksummed state pointer. It does not fetch metadata or bytes.
type Manager struct {
	root    string
	options Options
}

func Open(ctx context.Context, root string, options Options) (*Manager, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options = options.withDefaults()
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	canonicalRoot, err := createSecureRoot(root)
	if err != nil {
		return nil, err
	}
	manager := &Manager{root: canonicalRoot, options: options}
	unlock, err := manager.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := manager.recover(ctx); err != nil {
		return nil, err
	}
	if _, err := manager.readState(); err != nil {
		return nil, err
	}
	return manager, nil
}

func validateOptions(options Options) error {
	if err := validateRoot(options.Trust); err != nil {
		return err
	}
	if options.Product != options.Trust.Product || !validProduct(options.Product) || !options.Channel.valid() {
		return fmt.Errorf("%w: updater scope", ErrInvalidMetadata)
	}
	if !validPlatformPart(options.GOOS) || !validPlatformPart(options.GOARCH) || !rootAllows(options.Trust, Target{Channel: options.Channel, GOOS: options.GOOS, GOARCH: options.GOARCH}) {
		return fmt.Errorf("%w: updater platform scope", ErrInvalidMetadata)
	}
	if options.Health == nil {
		return fmt.Errorf("%w: health checker required", ErrInvalidMetadata)
	}
	if options.MaxArtifactSize <= 0 || options.MaxArtifactSize > 1<<30 || options.LockPoll <= 0 || options.LockPoll > time.Minute || options.StaleLockAfter < time.Second || options.StaleLockAfter > 24*time.Hour {
		return fmt.Errorf("%w: updater limits", ErrInvalidMetadata)
	}
	return nil
}

func (manager *Manager) initialState() State {
	return State{Product: manager.options.Product, Channel: manager.options.Channel, GOOS: manager.options.GOOS, GOARCH: manager.options.GOARCH}
}

// Snapshot returns a consistent copy of the active updater state.
func (manager *Manager) Snapshot(ctx context.Context) (State, error) {
	unlock, err := manager.lock(ctx)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	if err := manager.recover(ctx); err != nil {
		return State{}, err
	}
	return manager.readState()
}

// ActivePath resolves the immutable active artifact without executing it.
func (manager *Manager) ActivePath(ctx context.Context) (string, error) {
	state, err := manager.Snapshot(ctx)
	if err != nil {
		return "", err
	}
	if state.Active == nil {
		return "", ErrNoRollback
	}
	path := manager.artifactPath(*state.Active)
	if err := verifyArtifact(path, state.Active.Size, state.Active.SHA256, manager.options.MaxArtifactSize); err != nil {
		return "", err
	}
	return path, nil
}

// Apply verifies signed metadata and artifact bytes, publishes the immutable
// release, atomically activates it, then performs a bounded caller-supplied
// health check. A failed check restores the prior pointer.
func (manager *Manager) Apply(ctx context.Context, envelope []byte, artifact io.Reader) (State, error) {
	metadata, err := Verify(envelope, manager.options.Trust)
	if err != nil {
		return State{}, err
	}
	unlock, err := manager.lock(ctx)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	if err := manager.recover(ctx); err != nil {
		return State{}, err
	}
	before, err := manager.readState()
	if err != nil {
		return State{}, err
	}
	installedVersion := ""
	if before.Active != nil {
		installedVersion = before.Active.Version
	}
	target, err := Select(metadata, Selection{
		Product:           manager.options.Product,
		Channel:           manager.options.Channel,
		GOOS:              manager.options.GOOS,
		GOARCH:            manager.options.GOARCH,
		Installed:         installedVersion,
		HighestGeneration: before.HighestGeneration,
		Now:               manager.options.Clock(),
	})
	if err != nil {
		return State{}, err
	}
	if target.Size > manager.options.MaxArtifactSize {
		return State{}, ErrTooLarge
	}
	installed := Installed{Version: target.Version, Artifact: target.Artifact, SHA256: target.SHA256, Size: target.Size, Generation: metadata.Generation}
	if before.Active != nil && sameArtifact(*before.Active, installed) {
		before.HighestGeneration = metadata.Generation
		before.Active.Generation = metadata.Generation
		if err := manager.writeState(before); err != nil {
			return State{}, err
		}
		return before, nil
	}
	if before.Previous != nil && sameArtifact(*before.Previous, installed) {
		if err := verifyArtifact(manager.artifactPath(*before.Previous), before.Previous.Size, before.Previous.SHA256, manager.options.MaxArtifactSize); err != nil {
			return State{}, err
		}
		after := before
		after.HighestGeneration = metadata.Generation
		after.Active = cloneInstalled(before.Previous)
		after.Active.Generation = metadata.Generation
		after.Previous = cloneInstalled(before.Active)
		if err := manager.writeJournal(journal{Operation: "reactivate", Before: before, After: after}); err != nil {
			return State{}, err
		}
		if err := manager.writeState(after); err != nil {
			return State{}, err
		}
		if err := manager.options.Health.Check(ctx, manager.artifactPath(*after.Active), target); err != nil {
			if restoreErr := manager.writeState(before); restoreErr != nil {
				return State{}, fmt.Errorf("%w: restore reactivation", ErrRecovery)
			}
			_ = manager.removeJournal()
			return State{}, ErrHealth
		}
		if err := manager.removeJournal(); err != nil {
			return State{}, err
		}
		return after, nil
	}
	if artifact == nil {
		return State{}, fmt.Errorf("%w: missing artifact", ErrHash)
	}
	stagingName, stagingPath, err := manager.stage(ctx, target, artifact)
	if err != nil {
		return State{}, err
	}
	defer os.RemoveAll(stagingPath)
	after := before
	after.HighestGeneration = metadata.Generation
	after.Previous = cloneInstalled(before.Active)
	after.Active = &installed
	transaction := journal{Operation: "apply", Before: before, After: after, Staging: stagingName, Release: target.Version}
	if err := manager.writeJournal(transaction); err != nil {
		return State{}, err
	}
	releasePath := filepath.Join(manager.root, "releases", target.Version)
	if _, err := os.Lstat(releasePath); err == nil {
		_ = manager.removeJournal()
		return State{}, fmt.Errorf("%w: release already exists", ErrUnsafePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return State{}, fmt.Errorf("%w: inspect release", ErrIO)
	}
	if err := os.Rename(stagingPath, releasePath); err != nil {
		return State{}, fmt.Errorf("%w: publish release", ErrIO)
	}
	if err := syncDirectory(filepath.Join(manager.root, "releases")); err != nil {
		return State{}, fmt.Errorf("%w: publish release", ErrIO)
	}
	if err := manager.writeState(after); err != nil {
		return State{}, err
	}
	activePath := manager.artifactPath(installed)
	if err := manager.options.Health.Check(ctx, activePath, target); err != nil {
		if restoreErr := manager.writeState(before); restoreErr != nil {
			return State{}, fmt.Errorf("%w: restore after health failure", ErrRecovery)
		}
		_ = os.RemoveAll(releasePath)
		_ = manager.removeJournal()
		return State{}, ErrHealth
	}
	if err := manager.removeJournal(); err != nil {
		return State{}, err
	}
	if before.Previous != nil {
		manager.removeInactive(*before.Previous, after)
	}
	return after, nil
}

// Rollback is an explicit authorization to reactivate the last verified
// release. The monotonic generation remains unchanged.
func (manager *Manager) Rollback(ctx context.Context) (State, error) {
	unlock, err := manager.lock(ctx)
	if err != nil {
		return State{}, err
	}
	defer unlock()
	if err := manager.recover(ctx); err != nil {
		return State{}, err
	}
	before, err := manager.readState()
	if err != nil {
		return State{}, err
	}
	if before.Active == nil || before.Previous == nil {
		return State{}, ErrNoRollback
	}
	if err := verifyArtifact(manager.artifactPath(*before.Previous), before.Previous.Size, before.Previous.SHA256, manager.options.MaxArtifactSize); err != nil {
		return State{}, err
	}
	after := before
	after.Active = cloneInstalled(before.Previous)
	after.Previous = cloneInstalled(before.Active)
	transaction := journal{Operation: "rollback", Before: before, After: after}
	if err := manager.writeJournal(transaction); err != nil {
		return State{}, err
	}
	if err := manager.writeState(after); err != nil {
		return State{}, err
	}
	target := Target{Version: after.Active.Version, Channel: after.Channel, GOOS: after.GOOS, GOARCH: after.GOARCH, Artifact: after.Active.Artifact, Size: after.Active.Size, SHA256: after.Active.SHA256}
	if err := manager.options.Health.Check(ctx, manager.artifactPath(*after.Active), target); err != nil {
		if restoreErr := manager.writeState(before); restoreErr != nil {
			return State{}, fmt.Errorf("%w: restore rollback", ErrRecovery)
		}
		_ = manager.removeJournal()
		return State{}, ErrHealth
	}
	if err := manager.removeJournal(); err != nil {
		return State{}, err
	}
	return after, nil
}

func (manager *Manager) stage(ctx context.Context, target Target, source io.Reader) (string, string, error) {
	stagingRoot := filepath.Join(manager.root, "staging")
	directory, err := os.MkdirTemp(stagingRoot, ".update-")
	if err != nil {
		return "", "", fmt.Errorf("%w: create staging", ErrIO)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		os.RemoveAll(directory)
		return "", "", fmt.Errorf("%w: secure staging", ErrIO)
	}
	path := filepath.Join(directory, target.Artifact)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		os.RemoveAll(directory)
		return "", "", fmt.Errorf("%w: create staged artifact", ErrIO)
	}
	hash := sha256.New()
	written, copyErr := copyContext(ctx, io.MultiWriter(file, hash), io.LimitReader(source, manager.options.MaxArtifactSize+1))
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		os.RemoveAll(directory)
		return "", "", copyErr
	}
	if syncErr != nil || closeErr != nil {
		os.RemoveAll(directory)
		return "", "", fmt.Errorf("%w: finalize staged artifact", ErrIO)
	}
	if written != target.Size || written > manager.options.MaxArtifactSize || hex.EncodeToString(hash.Sum(nil)) != target.SHA256 {
		os.RemoveAll(directory)
		return "", "", ErrHash
	}
	return filepath.Base(directory), directory, nil
}

func copyContext(ctx context.Context, destination io.Writer, source io.Reader) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			written, writeErr := destination.Write(buffer[:count])
			total += int64(written)
			if writeErr != nil {
				return total, fmt.Errorf("%w: write staged artifact", ErrIO)
			}
			if written != count {
				return total, fmt.Errorf("%w: short staged write", ErrIO)
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, fmt.Errorf("%w: read artifact", ErrIO)
		}
	}
}

func verifyArtifact(path string, expectedSize int64, expectedHash string, maximum int64) error {
	info, err := os.Lstat(path)
	if err != nil || !secureRegular(info) || info.Size() != expectedSize || info.Size() > maximum {
		return ErrHash
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrHash
	}
	defer file.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedHash {
		return ErrHash
	}
	return nil
}

func (manager *Manager) artifactPath(installed Installed) string {
	return filepath.Join(manager.root, "releases", installed.Version, installed.Artifact)
}

func sameArtifact(left, right Installed) bool {
	return left.Version == right.Version && left.Artifact == right.Artifact && left.SHA256 == right.SHA256 && left.Size == right.Size
}

func cloneInstalled(source *Installed) *Installed {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func (manager *Manager) removeInactive(candidate Installed, active State) {
	if active.Active != nil && candidate.Version == active.Active.Version || active.Previous != nil && candidate.Version == active.Previous.Version {
		return
	}
	if validVersion(candidate.Version) {
		_ = os.RemoveAll(filepath.Join(manager.root, "releases", candidate.Version))
	}
}

func randomToken() (string, error) {
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(token[:]), nil
}

func safeRelativeName(value string) bool {
	return value == filepath.Base(value) && value != "." && value != ".." && !strings.ContainsRune(value, 0)
}
