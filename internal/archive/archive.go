// Package archive implements yt-dlp-compatible download archive identities
// and a bounded, concurrency-safe persistent store.
package archive

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalidIdentity = errors.New("invalid archive identity")
	ErrCorrupt         = errors.New("corrupt download archive")
	ErrTooLarge        = errors.New("download archive limit exceeded")
	ErrUnsafePath      = errors.New("unsafe download archive path")
	ErrIO              = errors.New("download archive I/O failure")
	ErrLock            = errors.New("download archive lock failure")
)

const (
	defaultMaxFileBytes   int64 = 64 << 20
	defaultMaxRecordBytes       = 16 << 10
)

// Identity is the persisted yt-dlp archive identity. Its text form is
// "lowercase-extractor video-id".
type Identity struct {
	Extractor string
	VideoID   string
}

// NewIdentity builds the same identity as yt-dlp's make_archive_id.
func NewIdentity(extractor, videoID string) (Identity, error) {
	identity := Identity{Extractor: strings.ToLower(extractor), VideoID: videoID}
	if err := identity.validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// ParseIdentity parses a canonical archive line.
func ParseIdentity(line string) (Identity, error) {
	separator := strings.IndexByte(line, ' ')
	if separator <= 0 || separator == len(line)-1 {
		return Identity{}, ErrInvalidIdentity
	}
	identity := Identity{Extractor: line[:separator], VideoID: line[separator+1:]}
	if identity.Extractor != strings.ToLower(identity.Extractor) {
		return Identity{}, ErrInvalidIdentity
	}
	if err := identity.validate(); err != nil {
		return Identity{}, err
	}
	return identity, nil
}

func (identity Identity) validate() error {
	if identity.Extractor == "" || identity.VideoID == "" || !utf8.ValidString(identity.Extractor) || !utf8.ValidString(identity.VideoID) {
		return ErrInvalidIdentity
	}
	if strings.TrimSpace(identity.Extractor) != identity.Extractor || strings.TrimSpace(identity.VideoID) != identity.VideoID {
		return ErrInvalidIdentity
	}
	if strings.ContainsAny(identity.Extractor, " \t\r\n\x00") || strings.ContainsAny(identity.VideoID, "\r\n\x00") {
		return ErrInvalidIdentity
	}
	return nil
}

func (identity Identity) String() string {
	return identity.Extractor + " " + identity.VideoID
}

// Options bounds archive operations and controls portable lock recovery.
type Options struct {
	MaxFileBytes   int64
	MaxRecordBytes int
	LockPoll       time.Duration
	StaleLockAfter time.Duration
	Clock          func() time.Time
}

func (options Options) withDefaults() Options {
	if options.MaxFileBytes <= 0 {
		options.MaxFileBytes = defaultMaxFileBytes
	}
	if options.MaxRecordBytes <= 0 {
		options.MaxRecordBytes = defaultMaxRecordBytes
	}
	if options.LockPoll <= 0 {
		options.LockPoll = 10 * time.Millisecond
	}
	if options.StaleLockAfter <= 0 {
		options.StaleLockAfter = 5 * time.Minute
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return options
}

// Store is a download archive at Path. Methods re-read under the operation's
// lock, so separate Store values and separate processes observe one another.
type Store struct {
	path    string
	options Options
}

// Open validates path and the existing archive without retaining file data.
func Open(ctx context.Context, path string, options Options) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store := &Store{path: path, options: options.withDefaults()}
	if err := validatePath(path); err != nil {
		return nil, err
	}
	if _, err := store.read(ctx); err != nil {
		return nil, err
	}
	return store, nil
}

// Match checks the current identity and then exact legacy archive IDs.
func (store *Store) Match(ctx context.Context, current Identity, legacyIDs []string) (matched string, found bool, err error) {
	if err := current.validate(); err != nil {
		return "", false, err
	}
	if len(legacyIDs) > 1024 {
		return "", false, ErrTooLarge
	}
	entries, err := store.read(ctx)
	if err != nil {
		return "", false, err
	}
	candidates := append([]string{current.String()}, legacyIDs...)
	for _, candidate := range candidates {
		if err := validateRecord(candidate, store.options.MaxRecordBytes); err != nil {
			return "", false, err
		}
		if _, ok := entries.set[candidate]; ok {
			return candidate, true, nil
		}
	}
	return "", false, nil
}

// Contains checks only the canonical identity.
func (store *Store) Contains(ctx context.Context, identity Identity) (bool, error) {
	_, found, err := store.Match(ctx, identity, nil)
	return found, err
}

// Record atomically adds identity. It returns false if another operation or
// process has already recorded it.
func (store *Store) Record(ctx context.Context, identity Identity) (bool, error) {
	if err := identity.validate(); err != nil {
		return false, err
	}
	unlock, err := store.lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	entries, err := store.read(ctx)
	if err != nil {
		return false, err
	}
	record := identity.String()
	if _, exists := entries.set[record]; exists {
		return false, nil
	}
	entries.order = append(entries.order, record)
	entries.set[record] = struct{}{}
	if err := store.write(ctx, entries.order); err != nil {
		return false, err
	}
	return true, nil
}

// Migrate replaces exact legacy IDs with canonical identities and removes
// duplicates while preserving the first-seen order of all records.
func (store *Store) Migrate(ctx context.Context, replacements map[string]Identity) (changed bool, err error) {
	if len(replacements) > 100_000 {
		return false, ErrTooLarge
	}
	canonical := make(map[string]string, len(replacements))
	for old, replacement := range replacements {
		if err := validateRecord(old, store.options.MaxRecordBytes); err != nil {
			return false, err
		}
		if err := replacement.validate(); err != nil {
			return false, err
		}
		canonical[old] = replacement.String()
	}
	unlock, err := store.lock(ctx)
	if err != nil {
		return false, err
	}
	defer unlock()
	entries, err := store.read(ctx)
	if err != nil {
		return false, err
	}
	seen := make(map[string]struct{}, len(entries.order))
	result := make([]string, 0, len(entries.order))
	for _, record := range entries.order {
		if replacement, ok := canonical[record]; ok {
			record = replacement
			changed = true
		}
		if _, duplicate := seen[record]; duplicate {
			changed = true
			continue
		}
		seen[record] = struct{}{}
		result = append(result, record)
	}
	if !changed {
		return false, nil
	}
	if err := store.write(ctx, result); err != nil {
		return false, err
	}
	return true, nil
}

// Entries returns a snapshot in file order. Opaque legacy lines are retained.
func (store *Store) Entries(ctx context.Context) ([]string, error) {
	entries, err := store.read(ctx)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), entries.order...), nil
}

func validateRecord(record string, limit int) error {
	if record == "" || len(record) > limit {
		return ErrTooLarge
	}
	if !utf8.ValidString(record) || strings.ContainsAny(record, "\r\n\x00") {
		return fmt.Errorf("%w: invalid record encoding", ErrCorrupt)
	}
	return nil
}
