package extractor

import (
	"context"
	"errors"
	"fmt"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	defaultMaxPlaylistPages   = 10_000
	defaultMaxPlaylistEntries = 100_000
)

var (
	ErrInvalidPlaylist = errors.New("invalid playlist")
	ErrPlaylistLimit   = errors.New("playlist limit exceeded")
)

// Extraction is either one media item or a playlist. Playlist entries remain
// lazy until a caller asks the Entries sequence for an iterator.
type Extraction struct {
	Info     value.Info
	Entries  EntrySequence
	Redirect *Entry
	Enrich   MetadataEnricher
}

// MetadataEnricher performs explicitly requested, potentially expensive
// metadata work after product-level match filtering has accepted the item.
type MetadataEnricher func(context.Context, *value.Info) error

func Media(info value.Info) Extraction { return Extraction{Info: info} }

func URLResult(entry Entry) (Extraction, error) {
	if entry.URL == "" {
		return Extraction{}, fmt.Errorf("%w: missing URL result target", ErrInvalidPlaylist)
	}
	cloned := entry
	return Extraction{Redirect: &cloned}, nil
}

func Playlist(info value.Info, entries EntrySequence) (Extraction, error) {
	if entries == nil {
		return Extraction{}, fmt.Errorf("%w: missing entries", ErrInvalidPlaylist)
	}
	playlistInfo := value.NewInfo(info.Fields().Clone())
	playlistInfo.Set("_type", value.String("playlist"))
	return Extraction{Info: playlistInfo, Entries: entries}, nil
}

func (result Extraction) IsPlaylist() bool { return result.Entries != nil }
func (result Extraction) IsURL() bool      { return result.Redirect != nil }

// Entry mirrors yt-dlp's lazy URL result. Resolution and nested playlist
// expansion are owned by the product registry rather than the producing site.
type Entry struct {
	URL          string
	ExtractorKey string
	ID           string
	Title        string
	Transparent  bool
}

func (entry Entry) Object() *value.Object {
	typeName := "url"
	if entry.Transparent {
		typeName = "url_transparent"
	}
	object := value.NewObject(
		value.Field{Key: "_type", Value: value.String(typeName)},
		value.Field{Key: "url", Value: value.String(entry.URL)},
	)
	if entry.ExtractorKey != "" {
		object.Set("ie_key", value.String(entry.ExtractorKey))
	}
	if entry.ID != "" {
		object.Set("id", value.String(entry.ID))
	}
	if entry.Title != "" {
		object.Set("title", value.String(entry.Title))
	}
	return object
}

type EntrySequence interface {
	Iterator() EntryIterator
}

type EntryIterator interface {
	Next(context.Context) (Entry, bool, error)
}

type staticEntries struct{ entries []Entry }

func StaticEntries(entries ...Entry) EntrySequence {
	return staticEntries{entries: append([]Entry(nil), entries...)}
}

func (entries staticEntries) Iterator() EntryIterator {
	return &staticEntryIterator{entries: append([]Entry(nil), entries.entries...)}
}

type staticEntryIterator struct {
	entries []Entry
	index   int
}

func (iterator *staticEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		return Entry{}, false, err
	}
	if iterator.index >= len(iterator.entries) {
		return Entry{}, false, nil
	}
	entry := iterator.entries[iterator.index]
	iterator.index++
	return entry, true, nil
}

type PageFetcher func(context.Context, int) ([]Entry, error)

type ContinuationFetcher func(context.Context, string) ([]Entry, string, error)

type StatefulContinuationFetcher func(context.Context, string, string) ([]Entry, string, string, error)

type pagedEntries struct {
	pageSize int
	maxPages int
	fetch    PageFetcher
}

// OnDemandEntries fetches zero-based pages until a short page is returned.
// Each iterator has independent state, and fetch errors terminate that iterator.
func OnDemandEntries(pageSize int, fetch PageFetcher) (EntrySequence, error) {
	if pageSize <= 0 || fetch == nil {
		return nil, fmt.Errorf("%w: invalid page source", ErrInvalidPlaylist)
	}
	return pagedEntries{pageSize: pageSize, maxPages: defaultMaxPlaylistPages, fetch: fetch}, nil
}

func (entries pagedEntries) Iterator() EntryIterator {
	return &pagedEntryIterator{source: entries}
}

type continuationEntries struct {
	first     []Entry
	nextToken string
	fetch     ContinuationFetcher
	maxPages  int
}

// ContinuationEntries models cursor APIs whose next request depends on the
// previous response. Empty intermediate pages are followed, repeated cursors
// terminate safely, and each iterator owns independent cursor state.
func ContinuationEntries(first []Entry, nextToken string, fetch ContinuationFetcher) (EntrySequence, error) {
	if nextToken != "" && fetch == nil {
		return nil, fmt.Errorf("%w: missing continuation fetcher", ErrInvalidPlaylist)
	}
	return continuationEntries{
		first: append([]Entry(nil), first...), nextToken: nextToken,
		fetch: fetch, maxPages: defaultMaxPlaylistPages,
	}, nil
}

func (entries continuationEntries) Iterator() EntryIterator {
	seen := make(map[string]bool)
	if entries.nextToken != "" {
		seen[entries.nextToken] = true
	}
	return &continuationEntryIterator{
		page: append([]Entry(nil), entries.first...), token: entries.nextToken,
		fetch: entries.fetch, seen: seen, maxPages: entries.maxPages,
	}
}

type statefulContinuationEntries struct {
	first     []Entry
	nextToken string
	state     string
	fetch     StatefulContinuationFetcher
	maxPages  int
}

// StatefulContinuationEntries models cursor APIs that rotate request state
// independently of the continuation token. Each iterator owns both values, so
// reusable sequences and concurrent consumers cannot leak state into one
// another.
func StatefulContinuationEntries(first []Entry, nextToken, state string, fetch StatefulContinuationFetcher) (EntrySequence, error) {
	if nextToken != "" && fetch == nil {
		return nil, fmt.Errorf("%w: missing continuation fetcher", ErrInvalidPlaylist)
	}
	return statefulContinuationEntries{
		first: append([]Entry(nil), first...), nextToken: nextToken, state: state,
		fetch: fetch, maxPages: defaultMaxPlaylistPages,
	}, nil
}

func (entries statefulContinuationEntries) Iterator() EntryIterator {
	seen := make(map[string]bool)
	if entries.nextToken != "" {
		seen[entries.nextToken] = true
	}
	return &statefulContinuationEntryIterator{
		page: append([]Entry(nil), entries.first...), token: entries.nextToken,
		state: entries.state, fetch: entries.fetch, seen: seen, maxPages: entries.maxPages,
	}
}

type statefulContinuationEntryIterator struct {
	page      []Entry
	pageIndex int
	token     string
	state     string
	fetch     StatefulContinuationFetcher
	seen      map[string]bool
	pages     int
	maxPages  int
	done      bool
}

func (iterator *statefulContinuationEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	if iterator.done {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.token == "" {
			iterator.done = true
			return Entry{}, false, nil
		}
		if iterator.pages >= iterator.maxPages {
			iterator.done = true
			return Entry{}, false, ErrPlaylistLimit
		}
		page, nextToken, nextState, err := iterator.fetch(ctx, iterator.token, iterator.state)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		iterator.pages++
		iterator.page, iterator.pageIndex = append([]Entry(nil), page...), 0
		iterator.token, iterator.state = nextToken, nextState
		if nextToken != "" {
			if iterator.seen[nextToken] {
				iterator.token = ""
			} else {
				iterator.seen[nextToken] = true
			}
		}
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	return entry, true, nil
}

type continuationEntryIterator struct {
	page      []Entry
	pageIndex int
	token     string
	fetch     ContinuationFetcher
	seen      map[string]bool
	pages     int
	maxPages  int
	done      bool
}

func (iterator *continuationEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	if iterator.done {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.token == "" {
			iterator.done = true
			return Entry{}, false, nil
		}
		if iterator.pages >= iterator.maxPages {
			iterator.done = true
			return Entry{}, false, ErrPlaylistLimit
		}
		page, nextToken, err := iterator.fetch(ctx, iterator.token)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		iterator.pages++
		iterator.page, iterator.pageIndex = append([]Entry(nil), page...), 0
		iterator.token = nextToken
		if nextToken != "" {
			if iterator.seen[nextToken] {
				iterator.token = ""
			} else {
				iterator.seen[nextToken] = true
			}
		}
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	return entry, true, nil
}

type pagedEntryIterator struct {
	source    pagedEntries
	page      []Entry
	pageIndex int
	pageNum   int
	lastPage  bool
	done      bool
}

func (iterator *pagedEntryIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.done = true
		return Entry{}, false, err
	}
	if iterator.done {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.lastPage {
			iterator.done = true
			return Entry{}, false, nil
		}
		if iterator.pageNum >= iterator.source.maxPages {
			iterator.done = true
			return Entry{}, false, ErrPlaylistLimit
		}
		page, err := iterator.source.fetch(ctx, iterator.pageNum)
		if err != nil {
			iterator.done = true
			return Entry{}, false, err
		}
		iterator.pageNum++
		iterator.page, iterator.pageIndex = append([]Entry(nil), page...), 0
		if len(page) < iterator.source.pageSize {
			iterator.lastPage = true
		}
		if len(page) == 0 {
			return Entry{}, false, nil
		}
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	return entry, true, nil
}

func CollectEntries(ctx context.Context, sequence EntrySequence, limit int) ([]Entry, error) {
	if sequence == nil {
		return nil, fmt.Errorf("%w: missing entries", ErrInvalidPlaylist)
	}
	if limit <= 0 {
		limit = defaultMaxPlaylistEntries
	}
	iterator := sequence.Iterator()
	entries := make([]Entry, 0)
	for len(entries) < limit {
		entry, ok, err := iterator.Next(ctx)
		if err != nil {
			return entries, err
		}
		if !ok {
			return entries, nil
		}
		entries = append(entries, entry)
	}
	if _, ok, err := iterator.Next(ctx); err != nil {
		return entries, err
	} else if ok {
		return entries, ErrPlaylistLimit
	}
	return entries, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
