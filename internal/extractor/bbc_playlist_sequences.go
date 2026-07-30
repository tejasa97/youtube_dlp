package extractor

import (
	"context"
	"fmt"
	"net/url"
)

type bbcPlaylistPageResult struct {
	entries  []Entry
	lastPage bool
}

type bbcPlaylistPageFetcher func(context.Context, int) (bbcPlaylistPageResult, error)

// bbcReusablePlaylistSource is an immutable EntrySequence. Each Iterator call
// returns independent state that enumerates from the first page.
type bbcReusablePlaylistSource struct {
	pageSize   int
	maxPages   int
	maxEntries int
	singlePage bool
	seedFP     string
	fetch      bbcPlaylistPageFetcher
}

func newBBCReusablePlaylistSource(pageSize, maxPages, maxEntries int, singlePage bool, seedFP string, fetch bbcPlaylistPageFetcher) (EntrySequence, error) {
	if maxPages <= 0 || maxEntries <= 0 || fetch == nil {
		return nil, fmt.Errorf("%w: invalid BBC playlist source", ErrInvalidPlaylist)
	}
	if pageSize < 0 {
		return nil, fmt.Errorf("%w: invalid BBC playlist page size", ErrInvalidPlaylist)
	}
	return bbcReusablePlaylistSource{
		pageSize:   pageSize,
		maxPages:   maxPages,
		maxEntries: maxEntries,
		singlePage: singlePage,
		seedFP:     seedFP,
		fetch:      fetch,
	}, nil
}

func (source bbcReusablePlaylistSource) Iterator() EntryIterator {
	return &bbcReusablePlaylistIterator{
		source: source,
		seenFP: make(map[string]struct{}),
	}
}

type bbcReusablePlaylistIterator struct {
	source        bbcReusablePlaylistSource
	page          []Entry
	pageIndex     int
	pagesFetched  int
	emitted       int
	seenFP        map[string]struct{}
	noMoreFetches bool
	finished      bool
}

func (iterator *bbcReusablePlaylistIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.finished = true
		return Entry{}, false, err
	}
	if iterator.finished {
		return Entry{}, false, nil
	}
	if iterator.noMoreFetches && iterator.pageIndex >= len(iterator.page) {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.noMoreFetches {
			return Entry{}, false, nil
		}
		if iterator.pagesFetched >= iterator.source.maxPages {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		if iterator.emitted >= iterator.source.maxEntries {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		result, err := iterator.source.fetch(ctx, iterator.pagesFetched)
		if err != nil {
			iterator.finished = true
			return Entry{}, false, err
		}
		if iterator.source.pageSize > 0 && len(result.entries) > iterator.source.pageSize {
			iterator.finished = true
			return Entry{}, false, fmt.Errorf("%w: BBC playlist page overflow", ErrInvalidMetadata)
		}
		if iterator.shouldFingerprint(result.entries) {
			fingerprint := bbcPageFingerprint(result.entries)
			if fingerprint != "" {
				if _, seen := iterator.seenFP[fingerprint]; seen {
					iterator.finished = true
					return Entry{}, false, fmt.Errorf("%w: repeated BBC playlist page", ErrInvalidPlaylist)
				}
				iterator.seenFP[fingerprint] = struct{}{}
			}
		}
		iterator.pagesFetched++
		iterator.page = append(iterator.page[:0], result.entries...)
		iterator.pageIndex = 0
		if result.lastPage || iterator.source.singlePage {
			iterator.noMoreFetches = true
		}
		if len(iterator.page) == 0 {
			iterator.noMoreFetches = true
			return Entry{}, false, nil
		}
	}
	if iterator.emitted >= iterator.source.maxEntries {
		iterator.finished = true
		return Entry{}, false, ErrPlaylistLimit
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	iterator.emitted++
	return entry, true, nil
}

func (iterator *bbcReusablePlaylistIterator) shouldFingerprint(entries []Entry) bool {
	if len(entries) == 0 {
		return false
	}
	if iterator.source.pageSize == 0 {
		return true
	}
	return len(entries) == iterator.source.pageSize
}

// bbcHTMLPlaylistSource paginates programmes HTML listings with trusted
// continuation URLs. The first page bytes are part of the immutable source.
type bbcHTMLPlaylistSource struct {
	transport  Transport
	base       string
	baseURL    *url.URL
	pid        string
	singlePage bool
	firstPage  []byte
	maxPages   int
	maxEntries int
	seedFP     string
}

func newBBCHTMLPlaylistSource(ctx context.Context, transport Transport, base *url.URL, pid string, singlePage bool, firstPage []byte) (EntrySequence, error) {
	entries, err := parseBBCProgrammesPlaylistPage(ctx, firstPage, pid)
	if err != nil {
		return nil, err
	}
	seedFP := ""
	if len(entries) > 0 {
		seedFP = bbcPageFingerprint(entries)
	}
	return bbcHTMLPlaylistSource{
		transport:  transport,
		base:       base.String(),
		baseURL:    base,
		pid:        pid,
		singlePage: singlePage,
		firstPage:  append([]byte(nil), firstPage...),
		maxPages:   defaultMaxPlaylistPages,
		maxEntries: bbcMaxPlaylistEntries,
		seedFP:     seedFP,
	}, nil
}

func (source bbcHTMLPlaylistSource) Iterator() EntryIterator {
	seenURL := map[string]struct{}{source.base: struct{}{}}
	return &bbcHTMLPlaylistIterator{
		source:      source,
		currentPage: append([]byte(nil), source.firstPage...),
		currentURL:  source.base,
		seenURL:     seenURL,
		seenFP:      make(map[string]struct{}),
	}
}

type bbcHTMLPlaylistIterator struct {
	source        bbcHTMLPlaylistSource
	currentPage   []byte
	currentURL    string
	page          []Entry
	pageIndex     int
	pagesFetched  int
	emitted       int
	seenURL       map[string]struct{}
	seenFP        map[string]struct{}
	noMoreFetches bool
	finished      bool
}

func (iterator *bbcHTMLPlaylistIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := contextError(ctx); err != nil {
		iterator.finished = true
		return Entry{}, false, err
	}
	if iterator.finished {
		return Entry{}, false, nil
	}
	if iterator.noMoreFetches && iterator.pageIndex >= len(iterator.page) {
		return Entry{}, false, nil
	}
	for iterator.pageIndex >= len(iterator.page) {
		if iterator.noMoreFetches {
			return Entry{}, false, nil
		}
		if iterator.pagesFetched >= iterator.source.maxPages {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		if iterator.emitted >= iterator.source.maxEntries {
			iterator.finished = true
			return Entry{}, false, ErrPlaylistLimit
		}
		entries, err := parseBBCProgrammesPlaylistPage(ctx, iterator.currentPage, iterator.source.pid)
		if err != nil {
			iterator.finished = true
			return Entry{}, false, err
		}
		if len(entries) > 0 {
			fingerprint := bbcPageFingerprint(entries)
			if fingerprint != "" {
				if _, seen := iterator.seenFP[fingerprint]; seen {
					iterator.finished = true
					return Entry{}, false, fmt.Errorf("%w: repeated BBC programmes playlist page", ErrInvalidPlaylist)
				}
				iterator.seenFP[fingerprint] = struct{}{}
			}
		}
		iterator.page = entries
		iterator.pageIndex = 0
		iterator.pagesFetched++
		lastPage := iterator.source.singlePage
		if !lastPage {
			nextURL, ok := bbcPaginationNextURL(iterator.source.baseURL, iterator.currentPage)
			if !ok {
				lastPage = true
			} else if _, seen := iterator.seenURL[nextURL]; seen {
				iterator.finished = true
				return Entry{}, false, fmt.Errorf("%w: repeated BBC programmes playlist page URL", ErrInvalidPlaylist)
			} else {
				page, err := requestBBCIsolatedPage(ctx, iterator.source.transport, nextURL)
				if err != nil {
					iterator.finished = true
					return Entry{}, false, categorizeBBCPageError(err)
				}
				iterator.seenURL[nextURL] = struct{}{}
				iterator.currentPage = page
				iterator.currentURL = nextURL
			}
		}
		if lastPage {
			iterator.noMoreFetches = true
		}
		if len(iterator.page) == 0 {
			iterator.noMoreFetches = true
			return Entry{}, false, nil
		}
	}
	if iterator.emitted >= iterator.source.maxEntries {
		iterator.finished = true
		return Entry{}, false, ErrPlaylistLimit
	}
	entry := iterator.page[iterator.pageIndex]
	iterator.pageIndex++
	iterator.emitted++
	return entry, true, nil
}
