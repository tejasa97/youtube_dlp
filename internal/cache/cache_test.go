package cache

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

type cancelAfterContext struct {
	context.Context
	remaining int
}

func (ctx *cancelAfterContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func TestCacheReferenceEncodingStoreLookupExpiryAndRemove(t *testing.T) {
	fixtureData, err := os.ReadFile(filepath.Join("..", "..", "conformance", "cache", "reference.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Namespace string `json:"namespace"`
		Key       string `json:"key"`
		Encoded   string `json:"encoded_key"`
	}
	if err := json.Unmarshal(fixtureData, &fixture); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	value := []byte(`{"client_id":"public-fixture"}`)
	if err := store.Store(context.Background(), fixture.Namespace, fixture.Key, value, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.root, fixture.Namespace, fixture.Encoded)); err != nil {
		t.Fatalf("encoded cache path: %v", err)
	}
	got, hit, err := store.Lookup(context.Background(), fixture.Namespace, fixture.Key)
	if err != nil || !hit || string(got) != string(value) {
		t.Fatalf("Lookup = %q, %v, %v", got, hit, err)
	}
	now = now.Add(time.Minute)
	if _, hit, err := store.Lookup(context.Background(), fixture.Namespace, fixture.Key); err != nil || hit {
		t.Fatalf("expired Lookup = %v, %v", hit, err)
	}
	if err := store.Remove(context.Background(), fixture.Namespace, fixture.Key); err != nil {
		t.Fatal(err)
	}
}

func TestCacheAtomicOverwriteAndConcurrentWriters(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	values := [][]byte{[]byte(strings.Repeat("a", 100_000)), []byte(strings.Repeat("b", 100_000)), []byte(strings.Repeat("c", 100_000))}
	var wait sync.WaitGroup
	for _, value := range values {
		value := value
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := store.Store(context.Background(), "test", "same", value, 0); err != nil {
				t.Error(err)
			}
		}()
	}
	wait.Wait()
	got, hit, err := store.Lookup(context.Background(), "test", "same")
	if err != nil || !hit {
		t.Fatalf("Lookup = %v, %v", hit, err)
	}
	valid := false
	for _, value := range values {
		if string(got) == string(value) {
			valid = true
		}
	}
	if !valid {
		t.Fatal("concurrent store produced partial payload")
	}
	before := append([]byte(nil), got...)
	limited, err := Open(store.root, Options{MaxValueBytes: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := limited.Store(context.Background(), "test", "same", []byte(strings.Repeat("x", 11)), 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize Store = %v", err)
	}
	after, hit, err := store.Lookup(context.Background(), "test", "same")
	if err != nil || !hit || string(after) != string(before) {
		t.Fatal("failed overwrite changed existing entry")
	}
}

func TestCacheRejectsTraversalAndSymlinks(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache")
	store, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"", ".", "..", "../escape", "a/b", "a\\b"} {
		if err := store.Store(context.Background(), namespace, "key", []byte("x"), 0); !errors.Is(err, ErrInvalidName) {
			t.Fatalf("namespace %q error = %v", namespace, err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err == nil {
		if err := store.Store(context.Background(), "linked", "key", []byte("secret"), 0); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("namespace symlink error = %v", err)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through namespace symlink: %v", entries)
	}
}

func TestCacheRejectsCorruptionTruncationAndEntrySymlink(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{MaxValueBytes: 64})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := store.namespacePath("test", true)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "key.cache")
	for _, data := range [][]byte{
		[]byte("bad\n"),
		[]byte(magic + "0\n10\nshort"),
		[]byte(magic + "0\n1000\n"),
		[]byte(magic + "0\n1\n" + strings.Repeat("0", 64) + "\nxy"),
		[]byte(magic + "0\n3\n" + strings.Repeat("0", 64) + "\nabc"),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := store.Lookup(context.Background(), "test", "key")
		if !errors.Is(err, ErrCorrupt) && !errors.Is(err, ErrTooLarge) {
			t.Fatalf("corruption error = %v for %q", err, data)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err == nil {
		if err := store.Store(context.Background(), "test", "key", []byte("changed"), 0); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("entry symlink error = %v", err)
		}
		if _, _, err := store.Lookup(context.Background(), "test", "key"); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("entry symlink lookup error = %v", err)
		}
		data, _ := os.ReadFile(target)
		if string(data) != "untouched" {
			t.Fatal("cache followed entry symlink")
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
}

func TestCacheAtomicReplacementDoesNotMutateHardlink(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(context.Background(), "test", "key", []byte("old"), 0); err != nil {
		t.Fatal(err)
	}
	path, _, _ := store.path("test", "key", false)
	linked := filepath.Join(t.TempDir(), "linked")
	if err := os.Link(path, linked); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("hard links unavailable: %v", err)
		}
		t.Fatal(err)
	}
	before, _ := os.ReadFile(linked)
	if err := store.Store(context.Background(), "test", "key", []byte("new"), 0); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(linked)
	if string(after) != string(before) {
		t.Fatal("atomic cache replacement mutated a hard-linked inode")
	}
}

func TestCacheCancellationRemovalAndSecretSafeErrors(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Store(cancelled, "test", "key", []byte("value"), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("Store cancellation = %v", err)
	}
	secret := "token=top-secret"
	if err := store.Store(context.Background(), "test", secret, []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	path, _, _ := store.path("test", secret, false)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Lookup(context.Background(), "test", secret)
	if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "top-secret") {
		t.Fatalf("unsafe diagnostic = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := store.Store(context.Background(), "test", "one", []byte("1"), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RemoveNamespace(context.Background(), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.root, "test")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("namespace remains: %v", err)
	}
}

func TestCacheMidWriteCancellationLeavesPriorValue(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{MaxValueBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(context.Background(), "test", "key", []byte("prior"), 0); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterContext{Context: context.Background(), remaining: 4}
	if err := store.Store(ctx, "test", "key", []byte(strings.Repeat("x", 512<<10)), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-write cancellation = %v", err)
	}
	value, hit, err := store.Lookup(context.Background(), "test", "key")
	if err != nil || !hit || string(value) != "prior" {
		t.Fatalf("prior value = %q, %v, %v", value, hit, err)
	}
}

func TestCacheRemoveNamespaceFailsClosedOnSymlink(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "cache"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Store(context.Background(), "test", "key", []byte("value"), 0); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(target, []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(store.root, "test", "malicious")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlinks unavailable: %v", err)
		}
		t.Fatal(err)
	}
	if err := store.RemoveNamespace(context.Background(), "test"); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("RemoveNamespace = %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "safe" {
		t.Fatal("RemoveNamespace followed symlink")
	}
}

func FuzzCacheKeyEncoding(f *testing.F) {
	for _, seed := range []string{"client/id:β", "simple", "../escape", "a b", ""} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, key string) {
		if len(key) > 1024 || !utf8.ValidString(key) {
			return
		}
		encoded := encodeKey(key)
		if strings.ContainsAny(encoded, "/\\\x00") {
			t.Fatalf("unsafe encoding %q for %q", encoded, key)
		}
	})
}

func FuzzCacheHeader(f *testing.F) {
	f.Add([]byte(magic + "0\n3\n" + strings.Repeat("0", 64) + "\nabc"))
	f.Add([]byte("bad\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4096 {
			return
		}
		reader := bufioNewReader(data)
		expires, length, _, err := readHeader(reader)
		if err == nil && (expires < 0 || length < 0) {
			t.Fatalf("invalid header accepted: %d %d", expires, length)
		}
	})
}

func bufioNewReader(data []byte) *bufio.Reader {
	return bufio.NewReader(strings.NewReader(string(data)))
}
