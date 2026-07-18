package archive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (store *Store) lock(ctx context.Context) (func(), error) {
	lockPath := store.path + ".lock"
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		token, err := randomToken()
		if err != nil {
			return nil, fmt.Errorf("%w: generate owner", ErrLock)
		}
		err = os.Mkdir(lockPath, 0o700)
		if err == nil {
			owner := token + "\n" + strconv.FormatInt(store.options.Clock().UnixNano(), 10) + "\n"
			if err := os.WriteFile(filepath.Join(lockPath, "owner"), []byte(owner), 0o600); err != nil {
				_ = os.RemoveAll(lockPath)
				return nil, fmt.Errorf("%w: write owner", ErrLock)
			}
			return func() { releaseLock(lockPath, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%w: acquire", ErrLock)
		}
		info, statErr := os.Lstat(lockPath)
		if statErr != nil {
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnsafePath
		}
		if store.lockIsStale(lockPath, info.ModTime()) {
			tombstone := lockPath + ".stale-" + token
			if os.Rename(lockPath, tombstone) == nil {
				_ = os.RemoveAll(tombstone)
				continue
			}
		}
		timer := time.NewTimer(store.options.LockPoll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (store *Store) lockIsStale(lockPath string, fallback time.Time) bool {
	stamp := fallback
	if data, err := os.ReadFile(filepath.Join(lockPath, "owner")); err == nil && len(data) <= 256 {
		parts := strings.Split(string(data), "\n")
		if len(parts) >= 2 {
			if nanos, err := strconv.ParseInt(parts[1], 10, 64); err == nil {
				stamp = time.Unix(0, nanos)
			}
		}
	}
	return store.options.Clock().Sub(stamp) > store.options.StaleLockAfter
}

func releaseLock(lockPath, token string) {
	data, err := os.ReadFile(filepath.Join(lockPath, "owner"))
	if err != nil || len(data) > 256 || !strings.HasPrefix(string(data), token+"\n") {
		return
	}
	_ = os.RemoveAll(lockPath)
}

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
