//go:build windows

package cache

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWindowsCacheUsesProtectedOwnerACL(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	store, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(context.Background(), "test", "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{root, filepath.Join(root, "test"), filepath.Join(root, "test", "key.cache")} {
		if info, err := os.Lstat(path); err != nil || (info.IsDir() && !secureWindowsACL(path, true)) || (!info.IsDir() && !secureWindowsACL(path, false)) {
			t.Fatalf("%s is not protected by an owner-only ACL: %v", path, err)
		}
	}
}

func TestWindowsCacheRootLockIsCancellationAware(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	if _, err := Open(root, Options{}); err != nil {
		t.Fatal(err)
	}
	first, err := lockCacheRoot(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlockCacheRoot(first)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second, err := lockCacheRoot(ctx, root)
	if second != nil {
		unlockCacheRoot(second)
		t.Fatal("contended root lock unexpectedly acquired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock = %v, want deadline", err)
	}
}
