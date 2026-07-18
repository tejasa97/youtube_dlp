//go:build windows

package archive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Windows uses an exclusive regular-file lease. Removing a non-empty lock
// directory while contenders inspect it is not atomic on Windows and can
// transiently return access denied, leaving every contender spinning on the
// abandoned directory. CREATE_NEW gives us the required cross-process atomic
// acquisition without that directory-removal race.
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
		owner := token + "\n" + strconv.FormatInt(store.options.Clock().UnixNano(), 10) + "\n"
		file, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err = file.WriteString(owner); err == nil {
				err = file.Sync()
			}
			closeErr := file.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				_ = os.Remove(lockPath)
				return nil, fmt.Errorf("%w: write owner", ErrLock)
			}
			return func() { releaseLock(lockPath, token) }, nil
		}
		if !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("%w: acquire", ErrLock)
		}
		info, statErr := os.Lstat(lockPath)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			if errors.Is(statErr, os.ErrPermission) {
				if err := waitLockPoll(ctx, store.options.LockPoll); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("%w: inspect", ErrLock)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrUnsafePath
		}
		if store.lockIsStale(lockPath, info.ModTime()) {
			tombstone := lockPath + ".stale-" + token
			if os.Rename(lockPath, tombstone) == nil {
				_ = os.Remove(tombstone)
				continue
			}
		}
		if err := waitLockPoll(ctx, store.options.LockPoll); err != nil {
			return nil, err
		}
	}
}

func waitLockPoll(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (store *Store) lockIsStale(lockPath string, fallback time.Time) bool {
	stamp := fallback
	if data, err := os.ReadFile(lockPath); err == nil && len(data) <= 256 {
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
	data, err := os.ReadFile(lockPath)
	if err != nil || len(data) > 256 || !strings.HasPrefix(string(data), token+"\n") {
		return
	}
	_ = os.Remove(lockPath)
}

func randomToken() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
