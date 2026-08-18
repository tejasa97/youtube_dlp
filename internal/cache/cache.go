// Package cache implements a bounded, namespaced filesystem cache with safe
// paths, atomic stores, and explicit expiry.
package cache

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidName = errors.New("invalid cache namespace or key")
	ErrUnsafePath  = errors.New("unsafe cache path")
	ErrTooLarge    = errors.New("cache entry limit exceeded")
	ErrCorrupt     = errors.New("corrupt cache entry")
	ErrIO          = errors.New("cache I/O failure")
)

const magic = "ytdlp-go-cache-v1\n"

// Options bounds cache operations. Clock enables deterministic expiry tests.
type Options struct {
	MaxValueBytes          int64
	MaxNamespaceBytes      int64
	MaxEntriesPerNamespace int
	MaxKeyBytes            int
	Clock                  func() time.Time
}

func (options Options) withDefaults() Options {
	if options.MaxValueBytes <= 0 {
		options.MaxValueBytes = 16 << 20
	}
	if options.MaxNamespaceBytes <= 0 {
		options.MaxNamespaceBytes = 512 << 20
	}
	if options.MaxEntriesPerNamespace <= 0 {
		options.MaxEntriesPerNamespace = 100_000
	}
	if options.MaxKeyBytes <= 0 {
		options.MaxKeyBytes = 1024
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return options
}

// Store is rooted at a private cache directory.
type Store struct {
	root        string
	options     Options
	writeGate   chan struct{}
	rootLock    *cacheRootLock
	rootLockErr error
}

var cacheRootGates sync.Map

// cacheRootLock retains the platform lock handle. Windows also keeps an open
// root-directory handle while the sentinel lock is held, preventing root
// replacement between identity validation and a cache operation.
type cacheRootLock struct {
	file  *os.File
	extra *os.File
}

// Open validates or creates root without following a root symlink.
func Open(root string, options Options) (*Store, error) {
	if root == "" || strings.IndexByte(root, 0) >= 0 || filepath.Clean(root) != root {
		return nil, ErrUnsafePath
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, ErrUnsafePath
	}
	gate := cacheRootGate(absolute)
	if err := acquireCacheRootGate(context.Background(), gate); err != nil {
		return nil, err
	}
	defer releaseCacheRootGate(gate)
	if err := secureDirectory(absolute); err != nil {
		return nil, err
	}
	return &Store{root: absolute, options: options.withDefaults(), writeGate: gate}, nil
}

// Store atomically writes a value. A non-positive TTL means no expiry.
func (store *Store) Store(ctx context.Context, namespace, key string, value []byte, ttl time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if int64(len(value)) > store.options.MaxValueBytes {
		return ErrTooLarge
	}
	if err := store.acquireWrite(ctx); err != nil {
		return err
	}
	defer store.releaseWrite()
	path, directory, err := store.path(namespace, key, true)
	if err != nil {
		return err
	}
	if err := rejectExistingNonRegular(path); err != nil {
		return err
	}
	if err := store.checkNamespaceBounds(directory, path, int64(len(value))+256); err != nil {
		return err
	}
	expires := int64(0)
	if ttl > 0 {
		now := store.options.Clock()
		expiresAt := now.Add(ttl)
		if expiresAt.UnixNano() <= now.UnixNano() {
			return ErrTooLarge
		}
		expires = expiresAt.UnixNano()
	}
	temporary, err := os.CreateTemp(directory, ".cache-*")
	if err != nil {
		return fmt.Errorf("%w: create temporary entry", ErrIO)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := secureFile(temporaryPath); err != nil {
		temporary.Close()
		return err
	}
	digest := sha256.Sum256(value)
	header := magic + strconv.FormatInt(expires, 10) + "\n" + strconv.FormatInt(int64(len(value)), 10) + "\n" + hex.EncodeToString(digest[:]) + "\n"
	if _, err := io.WriteString(temporary, header); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: write entry header", ErrIO)
	}
	for offset := 0; offset < len(value); {
		if err := ctx.Err(); err != nil {
			temporary.Close()
			return err
		}
		end := min(offset+64<<10, len(value))
		written, err := temporary.Write(value[offset:end])
		offset += written
		if err != nil || written == 0 {
			temporary.Close()
			return fmt.Errorf("%w: write entry", ErrIO)
		}
	}
	if err := ctx.Err(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("%w: sync entry", ErrIO)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("%w: close entry", ErrIO)
	}
	if err := rejectExistingNonRegular(path); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("%w: replace entry", ErrIO)
	}
	return nil
}

func (store *Store) checkNamespaceBounds(directory, replacedPath string, replacementBytes int64) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("%w: list namespace", ErrIO)
	}
	var total int64
	count := 0
	replacing := false
	for _, entry := range entries {
		if count >= store.options.MaxEntriesPerNamespace {
			return ErrTooLarge
		}
		if !validEntryFilename(entry.Name()) {
			return ErrUnsafePath
		}
		info, err := entry.Info()
		entryPath := filepath.Join(directory, entry.Name())
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || !secureExistingFile(entryPath) {
			return ErrUnsafePath
		}
		count++
		if entryPath == replacedPath {
			replacing = true
			continue
		}
		if info.Size() < 0 || total > store.options.MaxNamespaceBytes-info.Size() {
			return ErrTooLarge
		}
		total += info.Size()
	}
	if !replacing && count >= store.options.MaxEntriesPerNamespace {
		return ErrTooLarge
	}
	if replacementBytes < 0 || total > store.options.MaxNamespaceBytes-replacementBytes {
		return ErrTooLarge
	}
	return nil
}

// Lookup returns a copy of a live value. Expired entries are removed and
// reported as misses.
func (store *Store) Lookup(ctx context.Context, namespace, key string) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := store.acquireWrite(ctx); err != nil {
		return nil, false, err
	}
	defer store.releaseWrite()
	path, _, err := store.path(namespace, key, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	before, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: inspect entry", ErrIO)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || !secureExistingFile(path) {
		return nil, false, ErrUnsafePath
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: open entry", ErrIO)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) {
		return nil, false, ErrUnsafePath
	}
	maximum := store.options.MaxValueBytes + 256
	if info.Size() < 0 || info.Size() > maximum {
		return nil, false, ErrTooLarge
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, maximum+1), 4096)
	expires, length, expectedDigest, err := readHeader(reader)
	if err != nil {
		return nil, false, err
	}
	if length < 0 || length > store.options.MaxValueBytes {
		return nil, false, ErrTooLarge
	}
	if expires > 0 && !store.options.Clock().Before(time.Unix(0, expires)) {
		_ = file.Close()
		_ = removeRegular(path)
		return nil, false, nil
	}
	value := make([]byte, length)
	for offset := 0; offset < len(value); {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		end := min(offset+64<<10, len(value))
		read, err := io.ReadFull(reader, value[offset:end])
		offset += read
		if err != nil {
			return nil, false, fmt.Errorf("%w: truncated payload", ErrCorrupt)
		}
	}
	if extra, err := reader.ReadByte(); err == nil || !errors.Is(err, io.EOF) || extra != 0 {
		return nil, false, fmt.Errorf("%w: trailing payload", ErrCorrupt)
	}
	actualDigest := sha256.Sum256(value)
	if actualDigest != expectedDigest {
		return nil, false, fmt.Errorf("%w: payload checksum mismatch", ErrCorrupt)
	}
	return value, true, nil
}

// PruneOldest retains at most retain regular entries in namespace. Entries are
// ordered by modification time, then filename, so eviction is deterministic.
// Links, special files, and unknown entry names fail closed.
func (store *Store) PruneOldest(ctx context.Context, namespace string, retain int) error {
	if retain < 0 {
		return ErrInvalidName
	}
	if err := store.acquireWrite(ctx); err != nil {
		return err
	}
	defer store.releaseWrite()
	directory, err := store.namespacePath(namespace, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	type candidate struct {
		name string
		mod  time.Time
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("%w: list namespace", ErrIO)
	}
	items := make([]candidate, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validEntryFilename(entry.Name()) {
			return ErrUnsafePath
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || !secureExistingFile(filepath.Join(directory, entry.Name())) {
			return ErrUnsafePath
		}
		items = append(items, candidate{name: entry.Name(), mod: info.ModTime()})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].mod.Equal(items[j].mod) {
			return items[i].name < items[j].name
		}
		return items[i].mod.Before(items[j].mod)
	})
	for len(items) > retain {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := removeRegular(filepath.Join(directory, items[0].name)); err != nil {
			return err
		}
		items = items[1:]
	}
	return nil
}

// Remove deletes one entry without following symlinks.
func (store *Store) Remove(ctx context.Context, namespace, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := store.acquireWrite(ctx); err != nil {
		return err
	}
	defer store.releaseWrite()
	path, _, err := store.path(namespace, key, false)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return removeRegular(path)
}

// RemoveNamespace removes only regular entry files in namespace. Unknown
// directories, links, and special files make removal fail closed.
func (store *Store) RemoveNamespace(ctx context.Context, namespace string) error {
	if err := store.acquireWrite(ctx); err != nil {
		return err
	}
	defer store.releaseWrite()
	return removeNamespace(ctx, store.root, namespace)
}

// RemoveNamespaceRoot removes one namespace without creating root when it is
// absent. It is intended for explicit cache-clear controls.
func RemoveNamespaceRoot(ctx context.Context, root, namespace string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == "" || strings.ContainsRune(root, 0) || filepath.Clean(root) != root || !validNamespace(namespace) {
		return ErrUnsafePath
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return ErrUnsafePath
	}
	info, err := os.Lstat(absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect directory", ErrIO)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafePath
	}
	gate := cacheRootGate(absolute)
	if err := acquireCacheRootGate(ctx, gate); err != nil {
		return err
	}
	defer releaseCacheRootGate(gate)
	lock, err := lockCacheRoot(ctx, absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlockCacheRoot(lock)
	return removeNamespace(ctx, absolute, namespace)
}

func removeNamespace(ctx context.Context, root, namespace string) error {
	if !validNamespace(namespace) {
		return ErrInvalidName
	}
	if err := secureExistingDirectory(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	directory := filepath.Join(root, namespace)
	if err := secureExistingDirectory(directory); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: list namespace", ErrIO)
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validEntryFilename(entry.Name()) {
			return ErrUnsafePath
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || !secureExistingFile(filepath.Join(directory, entry.Name())) {
			return ErrUnsafePath
		}
		if err := os.Remove(filepath.Join(directory, entry.Name())); err != nil {
			return fmt.Errorf("%w: remove entry", ErrIO)
		}
	}
	if err := os.Remove(directory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: remove namespace", ErrIO)
	}
	return nil
}

// RemoveAll removes the complete configured cache root. It only removes the
// namespace directories and temporary cache files produced by this package;
// unexpected entries, links, and special files fail closed.
func (store *Store) RemoveAll(ctx context.Context) error {
	if err := store.acquireWrite(ctx); err != nil {
		return err
	}
	defer store.releaseWrite()
	if errors.Is(store.rootLockErr, os.ErrNotExist) {
		return nil
	}
	if store.rootLock == nil {
		return store.rootLockErr
	}
	return removeRootLocked(ctx, store.root, store.rootLock)
}

// RemoveRoot opens no files and never creates the configured path. The root
// must be an existing, non-symlink directory whose name (or parent name)
// identifies it as a cache directory, preventing accidental broad deletion.
func RemoveRoot(ctx context.Context, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if root == "" || strings.ContainsRune(root, 0) || filepath.Clean(root) != root {
		return ErrUnsafePath
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return ErrUnsafePath
	}
	if filepath.Dir(absolute) == absolute {
		return ErrUnsafePath
	}
	if home, homeErr := os.UserHomeDir(); homeErr == nil {
		if same, sameErr := filepath.Abs(home); sameErr == nil && same == absolute {
			return ErrUnsafePath
		}
	}
	base := strings.ToLower(filepath.Base(absolute))
	parent := strings.ToLower(filepath.Base(filepath.Dir(absolute)))
	if !strings.Contains(base, "cache") && !strings.Contains(parent, "cache") {
		return ErrUnsafePath
	}
	gate := cacheRootGate(absolute)
	if err := acquireCacheRootGate(ctx, gate); err != nil {
		return err
	}
	defer releaseCacheRootGate(gate)
	lock, err := lockCacheRoot(ctx, absolute)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unlockCacheRoot(lock)
	return removeRootLocked(ctx, absolute, lock)
}

func cacheRootGate(root string) chan struct{} {
	gate, _ := cacheRootGates.LoadOrStore(root, make(chan struct{}, 1))
	return gate.(chan struct{})
}

func acquireCacheRootGate(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseCacheRootGate(gate chan struct{}) {
	<-gate
}

func validEntryFilename(name string) bool {
	return strings.HasSuffix(name, ".cache") || strings.HasPrefix(name, ".cache-")
}

func (store *Store) acquireWrite(ctx context.Context) error {
	if err := acquireCacheRootGate(ctx, store.writeGate); err != nil {
		return err
	}
	lock, err := lockCacheRoot(ctx, store.root)
	if errors.Is(err, os.ErrNotExist) {
		store.rootLockErr = err
		releaseCacheRootGate(store.writeGate)
		return err
	}
	if err != nil {
		store.rootLockErr = err
		releaseCacheRootGate(store.writeGate)
		return err
	}
	store.rootLock = lock
	store.rootLockErr = nil
	return nil
}

func (store *Store) releaseWrite() {
	unlockCacheRoot(store.rootLock)
	store.rootLock = nil
	store.rootLockErr = nil
	releaseCacheRootGate(store.writeGate)
}

func readHeader(reader *bufio.Reader) (expires int64, length int64, digest [sha256.Size]byte, err error) {
	first, err := reader.ReadString('\n')
	if err != nil || first != magic {
		return 0, 0, digest, fmt.Errorf("%w: invalid magic", ErrCorrupt)
	}
	expiryLine, err := reader.ReadString('\n')
	if err != nil || len(expiryLine) > 32 {
		return 0, 0, digest, fmt.Errorf("%w: invalid expiry", ErrCorrupt)
	}
	expires, err = strconv.ParseInt(strings.TrimSuffix(expiryLine, "\n"), 10, 64)
	if err != nil || expires < 0 {
		return 0, 0, digest, fmt.Errorf("%w: invalid expiry", ErrCorrupt)
	}
	lengthLine, err := reader.ReadString('\n')
	if err != nil || len(lengthLine) > 32 {
		return 0, 0, digest, fmt.Errorf("%w: invalid length", ErrCorrupt)
	}
	length, err = strconv.ParseInt(strings.TrimSuffix(lengthLine, "\n"), 10, 64)
	if err != nil || length < 0 {
		return 0, 0, digest, fmt.Errorf("%w: invalid length", ErrCorrupt)
	}
	digestLine, err := reader.ReadString('\n')
	if err != nil || len(digestLine) != sha256.Size*2+1 {
		return 0, 0, digest, fmt.Errorf("%w: invalid checksum", ErrCorrupt)
	}
	decoded, err := hex.DecodeString(strings.TrimSuffix(digestLine, "\n"))
	if err != nil || len(decoded) != sha256.Size {
		return 0, 0, digest, fmt.Errorf("%w: invalid checksum", ErrCorrupt)
	}
	copy(digest[:], decoded)
	return expires, length, digest, nil
}

func (store *Store) path(namespace, key string, create bool) (string, string, error) {
	if key == "" || len(key) > store.options.MaxKeyBytes || !utf8.ValidString(key) || strings.IndexByte(key, 0) >= 0 {
		return "", "", ErrInvalidName
	}
	directory, err := store.namespacePath(namespace, create)
	if err != nil {
		return "", "", err
	}
	encoded := encodeKey(key)
	if len(encoded) > 4096 {
		return "", "", ErrInvalidName
	}
	return filepath.Join(directory, encoded+".cache"), directory, nil
}

func (store *Store) namespacePath(namespace string, create bool) (string, error) {
	if !validNamespace(namespace) {
		return "", ErrInvalidName
	}
	if err := secureExistingDirectory(store.root); err != nil {
		return "", err
	}
	directory := filepath.Join(store.root, namespace)
	if create {
		if err := secureDirectory(directory); err != nil {
			return "", err
		}
	} else if err := secureExistingDirectory(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func validNamespace(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.') {
			return false
		}
	}
	return true
}

// encodeKey follows yt-dlp's urllib.parse.quote(key, safe=”).replace('%', ',')
// filename convention for UTF-8 bytes.
func encodeKey(value string) string {
	const hexadecimal = "0123456789ABCDEF"
	var result strings.Builder
	for _, character := range []byte(value) {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_.-~", rune(character)) {
			result.WriteByte(character)
		} else {
			result.WriteByte(',')
			result.WriteByte(hexadecimal[character>>4])
			result.WriteByte(hexadecimal[character&15])
		}
	}
	return result.String()
}
