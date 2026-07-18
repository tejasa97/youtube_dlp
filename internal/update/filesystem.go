package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type diskRecord struct {
	Payload  json.RawMessage `json:"payload"`
	Checksum string          `json:"sha256"`
}

type journal struct {
	Operation string `json:"operation"`
	Before    State  `json:"before"`
	After     State  `json:"after"`
	Staging   string `json:"staging,omitempty"`
	Release   string `json:"release,omitempty"`
}

func createSecureRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root || strings.IndexByte(root, 0) >= 0 {
		return "", ErrUnsafePath
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", ErrUnsafePath
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", ErrUnsafePath
	}
	missing := []string{}
	ancestor := root
	for {
		if _, err := os.Lstat(ancestor); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", ErrUnsafePath
		}
		missing = append(missing, filepath.Base(ancestor))
		next := filepath.Dir(ancestor)
		if next == ancestor {
			return "", ErrUnsafePath
		}
		ancestor = next
	}
	resolved, err := filepath.EvalSymlinks(ancestor)
	if err != nil || !filepath.IsAbs(resolved) {
		return "", ErrUnsafePath
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
		if err := os.Mkdir(resolved, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: create updater root", ErrIO)
		}
		info, err := os.Lstat(resolved)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
	}
	if err := validateDirectorySecurity(resolved); err != nil {
		return "", err
	}
	for _, name := range []string{"releases", "staging"} {
		path := filepath.Join(resolved, name)
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", fmt.Errorf("%w: create updater directory", ErrIO)
		}
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", ErrUnsafePath
		}
		if err := validateDirectorySecurity(path); err != nil {
			return "", err
		}
	}
	return resolved, nil
}

func (manager *Manager) readState() (State, error) {
	state := manager.initialState()
	err := readRecord(filepath.Join(manager.root, "state.json"), maxStateBytes, &state)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return State{}, err
	}
	if err := manager.validateState(state); err != nil {
		return State{}, err
	}
	return state, nil
}

func (manager *Manager) validateState(state State) error {
	if state.Product != manager.options.Product || state.Channel != manager.options.Channel || state.GOOS != manager.options.GOOS || state.GOARCH != manager.options.GOARCH {
		return ErrRecovery
	}
	for _, installed := range []*Installed{state.Active, state.Previous} {
		if installed == nil {
			continue
		}
		if !validVersion(installed.Version) || !safeArtifact(installed.Artifact, state.GOOS) || installed.Size <= 0 || installed.Size > manager.options.MaxArtifactSize || installed.Generation > state.HighestGeneration {
			return ErrRecovery
		}
		digest, err := hex.DecodeString(installed.SHA256)
		if err != nil || len(digest) != sha256.Size {
			return ErrRecovery
		}
	}
	return nil
}

func (manager *Manager) writeState(state State) error {
	if err := manager.validateState(state); err != nil {
		return err
	}
	return writeRecord(filepath.Join(manager.root, "state.json"), state)
}

func (manager *Manager) writeJournal(value journal) error {
	if value.Operation != "apply" && value.Operation != "rollback" && value.Operation != "reactivate" {
		return ErrRecovery
	}
	if value.Staging != "" && !safeRelativeName(value.Staging) || value.Release != "" && !validVersion(value.Release) {
		return ErrRecovery
	}
	return writeRecord(filepath.Join(manager.root, "journal.json"), value)
}

func (manager *Manager) removeJournal() error {
	err := os.Remove(filepath.Join(manager.root, "journal.json"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove journal", ErrIO)
	}
	return syncDirectory(manager.root)
}

func (manager *Manager) recover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var value journal
	err := readRecord(filepath.Join(manager.root, "journal.json"), maxJournalBytes, &value)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: journal", ErrRecovery)
	}
	if value.Operation != "apply" && value.Operation != "rollback" && value.Operation != "reactivate" || value.Staging != "" && !safeRelativeName(value.Staging) || value.Release != "" && !validVersion(value.Release) {
		return ErrRecovery
	}
	if err := manager.validateState(value.Before); err != nil {
		return ErrRecovery
	}
	if err := manager.validateState(value.After); err != nil {
		return ErrRecovery
	}
	current, err := manager.readState()
	if err != nil {
		return err
	}
	if !equalState(current, value.Before) && !equalState(current, value.After) {
		return ErrRecovery
	}
	// A remaining journal means health was not durably acknowledged. Restore
	// the known verified pre-transaction state and discard an unverified apply.
	if !equalState(current, value.Before) {
		if err := manager.writeState(value.Before); err != nil {
			return fmt.Errorf("%w: restore state", ErrRecovery)
		}
	}
	if value.Staging != "" {
		if err := os.RemoveAll(filepath.Join(manager.root, "staging", value.Staging)); err != nil {
			return ErrRecovery
		}
	}
	if value.Operation == "apply" && value.Release != "" {
		release := filepath.Join(manager.root, "releases", value.Release)
		if value.After.Active == nil {
			return ErrRecovery
		}
		artifact := filepath.Join(release, value.After.Active.Artifact)
		if _, err := os.Lstat(release); err == nil {
			if err := verifyArtifact(artifact, value.After.Active.Size, value.After.Active.SHA256, manager.options.MaxArtifactSize); err != nil {
				return ErrRecovery
			}
			if err := os.RemoveAll(release); err != nil {
				return ErrRecovery
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return ErrRecovery
		}
	}
	if err := manager.removeJournal(); err != nil {
		return fmt.Errorf("%w: finish recovery", ErrRecovery)
	}
	return nil
}

func equalState(left, right State) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}

func writeRecord(path string, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: encode record", ErrIO)
	}
	digest := sha256.Sum256(payload)
	record, err := json.Marshal(diskRecord{Payload: payload, Checksum: hex.EncodeToString(digest[:])})
	if err != nil {
		return fmt.Errorf("%w: encode record envelope", ErrIO)
	}
	record = append(record, '\n')
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".update-record-")
	if err != nil {
		return fmt.Errorf("%w: create record", ErrIO)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: secure record", ErrIO)
	}
	if _, err := temporary.Write(record); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: write record", ErrIO)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: sync record", ErrIO)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close record", ErrIO)
	}
	if info, err := os.Lstat(path); err == nil && !secureRegular(info) {
		return fmt.Errorf("%w: record destination", ErrUnsafePath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: inspect record", ErrIO)
	}
	if err := replaceFile(temporaryPath, path); err != nil {
		return fmt.Errorf("%w: publish record", ErrIO)
	}
	if err := syncDirectory(parent); err != nil {
		return fmt.Errorf("%w: sync record directory", ErrIO)
	}
	return nil
}

func readRecord(path string, maximum int64, destination any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !secureRegular(info) || info.Size() <= 0 || info.Size() > maximum {
		return ErrRecovery
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read record", ErrIO)
	}
	var record diskRecord
	if err := decodeStrict(encoded, &record); err != nil {
		return ErrRecovery
	}
	digest := sha256.Sum256(record.Payload)
	if record.Checksum != hex.EncodeToString(digest[:]) {
		return ErrRecovery
	}
	if err := decodeStrict(record.Payload, destination); err != nil {
		return ErrRecovery
	}
	return nil
}

func (manager *Manager) lock(ctx context.Context) (func(), error) {
	path := filepath.Join(manager.root, ".update.lock")
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := randomToken()
		if err != nil {
			return nil, ErrLock
		}
		owner := token + "\n" + strconv.FormatInt(manager.options.Clock().UnixNano(), 10) + "\n" + strconv.Itoa(os.Getpid()) + "\n"
		if acquired, err := createLockObject(path, []byte(owner)); err == nil && acquired {
			return func() { releaseLock(path, token) }, nil
		} else if err != nil && !lockContention(err) {
			return nil, ErrLock
		}
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			if lockContention(err) {
				if err := waitLock(ctx, manager.options.LockPoll); err != nil {
					return nil, err
				}
				continue
			}
			return nil, ErrLock
		}
		if !validLockObject(info) {
			return nil, fmt.Errorf("%w: lock object", ErrUnsafePath)
		}
		if err := validateLockSecurity(path); err != nil {
			if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("%w: lock ownership", err)
		}
		if manager.lockStale(path, info.ModTime()) {
			tombstone := path + ".stale-" + token
			if os.Rename(path, tombstone) == nil {
				removeLockObject(tombstone)
				continue
			}
		}
		if err := waitLock(ctx, manager.options.LockPoll); err != nil {
			return nil, err
		}
	}
}

func waitLock(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (manager *Manager) lockStale(path string, fallback time.Time) bool {
	stamp := fallback
	if encoded, err := readLockOwner(path); err == nil && len(encoded) <= 256 {
		parts := strings.Split(string(encoded), "\n")
		if len(parts) >= 4 {
			if nanos, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				stamp = time.Unix(0, nanos)
			} else {
				return false
			}
			pid, err := strconv.Atoi(parts[2])
			if err != nil || pid <= 0 {
				return false
			}
			if processAlive(pid) {
				return false
			}
			return manager.options.Clock().Sub(stamp) > manager.options.StaleLockAfter
		}
		return false
	}
	// A contender can observe the directory before its owner file is written.
	// Do not apply an injected metadata clock to that filesystem race.
	return time.Since(fallback) > manager.options.StaleLockAfter
}

func releaseLock(path, token string) {
	encoded, err := readLockOwner(path)
	if err == nil && len(encoded) <= 256 && strings.HasPrefix(string(encoded), token+"\n") {
		removeLockObject(path)
	}
}
