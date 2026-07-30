package extractor

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// BBCCoUkPlaylist extracts programmes episodes/broadcasts/clips listings and
// hands programme URLs to bbciplayer via transparent child reentry.
type BBCCoUkPlaylist struct{}

func NewBBCCoUkPlaylist() BBCCoUkPlaylist { return BBCCoUkPlaylist{} }

func (BBCCoUkPlaylist) Name() string { return "bbc_co_uk_playlist" }

func (BBCCoUkPlaylist) Suitable(parsed *url.URL) bool {
	_, ok := classifyBBCCoUkPlaylistURL(parsed)
	return ok
}

func (BBCCoUkPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyBBCCoUkPlaylistURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := requestBBCIsolatedPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, categorizeBBCPageError(err)
	}
	title := bbcHTMLField(page, bbcOGTitle)
	if title == "" {
		title = target.pid
	}
	description := strings.TrimSpace(bbcHTMLField(page, bbcMetaDesc))
	sequence, err := newBBCHTMLPlaylistSource(ctx, request.Transport, parsed, target.pid, target.singlePage, page)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(bbcPlaylistInfo(target.pid, title, request.URL, description), sequence)
}

type bbcProgrammesPlaylistTarget struct {
	pid        string
	singlePage bool
}

func classifyBBCCoUkPlaylistURL(parsed *url.URL) (bbcProgrammesPlaylistTarget, bool) {
	if !bbcValidHost(parsed) {
		return bbcProgrammesPlaylistTarget{}, false
	}
	match := bbcProgrammesListPath.FindStringSubmatch(parsed.Path)
	if len(match) != 3 || !bbcPIDPattern.MatchString(match[1]) {
		return bbcProgrammesPlaylistTarget{}, false
	}
	_, singlePage := parsed.Query()["page"]
	return bbcProgrammesPlaylistTarget{pid: match[1], singlePage: singlePage}, true
}

func parseBBCProgrammesPlaylistPage(ctx context.Context, page []byte, playlistPID string) ([]Entry, error) {
	matches := bbcProgrammePIDPattern.FindAllStringSubmatch(string(page), -1)
	seen := make(map[string]struct{})
	entries := make([]Entry, 0, len(matches))
	for index, match := range matches {
		if index%64 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if len(match) < 2 {
			return nil, fmt.Errorf("%w: malformed BBC programmes playlist node", ErrInvalidMetadata)
		}
		pid := match[1]
		if !bbcPIDPattern.MatchString(pid) {
			return nil, fmt.Errorf("%w: malformed BBC programmes playlist pid", ErrInvalidMetadata)
		}
		if pid == playlistPID {
			continue
		}
		if _, exists := seen[pid]; exists {
			continue
		}
		seen[pid] = struct{}{}
		entries = append(entries, bbcProgrammeChildEntry(pid))
	}
	return bbcDedupeEntries(entries), nil
}

func bbcPaginationNextURL(base *url.URL, page []byte) (string, bool) {
	match := bbcPaginationNext.FindStringSubmatch(string(page))
	if len(match) < 2 {
		return "", false
	}
	return bbcTrustedContinuationURL(base, match[1])
}

func bbcExplicitPageNumber(parsed *url.URL) (int, bool, error) {
	values := parsed.Query()["page"]
	if len(values) == 0 {
		return 0, false, nil
	}
	first := strings.TrimSpace(values[0])
	for _, value := range values[1:] {
		if strings.TrimSpace(value) != first {
			return 0, false, fmt.Errorf("%w: conflicting BBC page query", ErrInvalidMetadata)
		}
	}
	page, err := strconv.Atoi(first)
	if err != nil || page <= 0 || page > defaultMaxPlaylistPages {
		return 0, false, fmt.Errorf("%w: invalid BBC page query", ErrInvalidMetadata)
	}
	return page, true, nil
}
