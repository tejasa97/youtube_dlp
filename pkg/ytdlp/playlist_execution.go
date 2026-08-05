// Package ytdlp playlist execution-policy types.
//
// This file defines the typed per-playlist execution primitives the playlist
// loop honors after entry selection. The semantics mirror the pinned
// yt-dlp reference checkout (commit CompatibilityReferenceCommit in
// api_contract.go) for --ignore-errors / --abort-on-error,
// --skip-playlist-after-errors, --playlist-random, and --lazy-playlist.
//
// The Go API intentionally exposes only the behavior it can distinguish:
// continue after an ordinary entry error or abort. The pinned distinction
// between ignoreerrors="only_download" and true depends on yt-dlp's global
// return-code machinery, which this API does not expose; both continuing CLI
// spellings therefore select Continue and failures remain observable on Result.
//
// Categorized errors that must never be swallowed (context cancellation,
// deadlines, resource limits, unsafe paths, configuration errors, and
// global playlist failures) propagate regardless of the configured mode.
// This invariant is enforced by IsNonOverridableError.
package ytdlp

import (
	"context"
	"errors"
	"math/rand"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
)

// PlaylistErrorPolicy controls ordinary per-entry failures. Continue is the
// zero value and mirrors yt-dlp's default queue behavior.
type PlaylistErrorPolicy uint8

const (
	PlaylistErrorContinue PlaylistErrorPolicy = iota
	PlaylistErrorAbort
)

// String returns a stable, lowercase identifier used in events and tests.
func (mode PlaylistErrorPolicy) String() string {
	switch mode {
	case PlaylistErrorAbort:
		return "abort"
	default:
		return "continue"
	}
}

// Valid reports whether the value is one of the defined constants.
func (mode PlaylistErrorPolicy) Valid() bool {
	switch mode {
	case PlaylistErrorContinue, PlaylistErrorAbort:
		return true
	}
	return false
}

// PlaylistRandomSource is the deterministic injection seam used by the
// playlist random order stage. Each invocation must return a fresh,
// independent *math/rand.Rand; the loop uses one to shuffle the materialized
// selected entries. A nil value selects a process-wide time-seeded source.
//
// Tests pass a fixed-seed source so the shuffled order is reproducible.
type PlaylistRandomSource func() *rand.Rand

// DefaultPlaylistRandomSource returns a fresh time-seeded *math/rand.Rand on
// every invocation. It is the value used when PlaylistOptions.RandomSource is
// nil.
func DefaultPlaylistRandomSource() *rand.Rand {
	return rand.New(rand.NewSource(time.Now().UnixNano()))
}

// IsNonOverridableError reports whether the supplied error belongs to
// a category that must propagate regardless of the configured error
// mode. The set is closed and mirrors the categorization in categorized(); it
// is reviewed whenever the project's error categorization gains a new root
// sentinel. Plain entry errors that fall outside these categories are governed
// by the playlist error policy.
func IsNonOverridableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, extractor.ErrPlaylistLimit) {
		return true
	}
	if IsCategory(err, ErrorCancelled) {
		return true
	}
	if IsCategory(err, ErrorSecurity) {
		return true
	}
	if IsCategory(err, ErrorInternal) {
		return true
	}
	if errors.Is(err, errInvalidRequestOptions) {
		return true
	}
	return false
}

func isPlaylistErrorNonOverridable(err error) bool {
	return IsNonOverridableError(err)
}

// playlistEntryErrorEventKind is the emitted event kind used when an entry
// failure is recorded but continued past by the configured error policy.
const playlistEntryErrorEventKind = "playlist_entry_error"

// playlistMaxFailuresEventKind is emitted once when MaxFailures has been
// reached, mirroring pinned yt-dlp's
// "Skipping the remaining entries in playlist ... since N items failed
// extraction" report_error.
const playlistMaxFailuresEventKind = "playlist_max_failures_reached"

// emitPlaylistEntryError records an attributable failure event so callers can
// observe which entries were skipped without leaking the raw error text into
// the event stream. The returned error is the handler error from emit; the
// caller is expected to honor it.
func emitPlaylistEntryError(ctx context.Context, client *Client, extractorName string, sourceIndex int, err error) error {
	if client == nil {
		return nil
	}
	return client.emit(ctx, Event{
		Kind:      playlistEntryErrorEventKind,
		Extractor: extractorName,
		Message:   playlistEntryErrorMessage(sourceIndex, err),
	})
}

// emitPlaylistMaxFailuresReached records the aggregate signal that mirrors
// pinned yt-dlp's "Skipping the remaining entries in playlist" report_error.
// The event handler error is surfaced so the caller can propagate it.
func emitPlaylistMaxFailuresReached(ctx context.Context, client *Client, extractorName, title string, failures int) error {
	if client == nil {
		return nil
	}
	displayTitle := strings.ToValidUTF8(title, "")
	displayTitle = strings.TrimSpace(strings.NewReplacer("\x00", "", "\r", " ", "\n", " ").Replace(displayTitle))
	if displayTitle == "" {
		displayTitle = "<untitled>"
	}
	if len(displayTitle) > 256 {
		displayTitle = displayTitle[:256]
		for !utf8.ValidString(displayTitle) {
			displayTitle = displayTitle[:len(displayTitle)-1]
		}
	}
	return client.emit(ctx, Event{
		Kind:      playlistMaxFailuresEventKind,
		Extractor: extractorName,
		Message:   "Skipping the remaining entries in playlist \"" + displayTitle + "\" since " + strconv.Itoa(failures) + " items failed extraction",
	})
}

// playlistEntryErrorMessage composes a redacted, category-aware summary of an
// entry error. The message contains only the source index, the operation
// stage, and the categorical label. Raw err.Error() text is never included.
func playlistEntryErrorMessage(sourceIndex int, err error) string {
	category := "error"
	op := "process entry"
	var typed *Error
	if errors.As(err, &typed) {
		if typed.Category != "" {
			category = string(typed.Category)
		}
		if typed.Op != "" {
			op = typed.Op
		}
	}
	return "playlist entry " + strconv.Itoa(sourceIndex) + ": " + category + ": " + op
}

// shufflePlaylistEntries reorders the materialized entries in place using the
// supplied source. The shuffle is a bounded in-place permutation; each
// element's SourceIndex field is preserved and only its position in the slice
// changes. A nil source falls back to DefaultPlaylistRandomSource so callers
// that only want to opt in to non-deterministic behavior do not need to
// plumb a source through every test.
func shufflePlaylistEntries(entries []indexedPlaylistEntry, source PlaylistRandomSource) {
	if len(entries) < 2 {
		return
	}
	if source == nil {
		source = DefaultPlaylistRandomSource
	}
	random := source()
	if random == nil {
		random = DefaultPlaylistRandomSource()
	}
	random.Shuffle(len(entries), func(i, j int) {
		entries[i], entries[j] = entries[j], entries[i]
	})
}

// reversePlaylistEntries reorders the materialized entries in place by
// swapping first and last iteratively. It is equivalent to the existing
// reverse block in the playlist loop but is exposed so it can be reused by
// the new shuffling helpers and tested independently of processPlaylist.
func reversePlaylistEntries(entries []indexedPlaylistEntry) {
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
}

func normalizedPlaylistExecutionOptions(options PlaylistOptions) (effective PlaylistOptions, warnings []string) {
	effective = options
	if effective.Reverse && effective.Random {
		warnings = append(warnings, "--playlist-reverse is ignored since --playlist-random was given")
		effective.Reverse = false
	}
	if effective.Reverse && effective.Lazy {
		warnings = append(warnings, "--playlist-reverse is ignored since --lazy-playlist was given")
		effective.Reverse = false
	}
	if effective.Random && effective.Lazy {
		warnings = append(warnings, "--playlist-random is ignored since --lazy-playlist was given")
		effective.Random = false
	}
	return effective, warnings
}

// materializableExecutionOrder decides whether the requested execution
// ordering requires pre-materialization. Lazy forces streaming, so neither
// Reverse nor Random materializes. Otherwise Reverse or Random both
// materialize. A caller without Reverse or Random returns false because the
// iterator already yields the selected entries lazily.
func materializableExecutionOrder(options PlaylistOptions) bool {
	if options.Lazy {
		return false
	}
	return options.Reverse || options.Random
}
