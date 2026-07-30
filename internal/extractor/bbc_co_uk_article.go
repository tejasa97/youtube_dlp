package extractor

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// BBCCoUkArticle extracts BBC programme article pages and hands clip programme
// URLs to bbciplayer via transparent child reentry.
type BBCCoUkArticle struct{}

func NewBBCCoUkArticle() BBCCoUkArticle { return BBCCoUkArticle{} }

func (BBCCoUkArticle) Name() string { return "bbc_co_uk_article" }

func (BBCCoUkArticle) Suitable(parsed *url.URL) bool {
	_, ok := classifyBBCCoUkArticleURL(parsed)
	return ok
}

func (BBCCoUkArticle) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	articleID, ok := classifyBBCCoUkArticleURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, err := requestBBCIsolatedPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, categorizeBBCPageError(err)
	}
	entries, err := parseBBCArticleClips(ctx, page)
	if err != nil {
		return Extraction{}, err
	}
	title := bbcHTMLField(page, bbcOGTitle)
	if title == "" {
		title = articleID
	}
	info := bbcPlaylistInfo(articleID, title, request.URL, strings.TrimSpace(bbcHTMLField(page, bbcMetaDesc)))
	return Playlist(info, StaticEntries(entries...))
}

func classifyBBCCoUkArticleURL(parsed *url.URL) (string, bool) {
	if !bbcValidHost(parsed) {
		return "", false
	}
	match := bbcArticlePath.FindStringSubmatch(parsed.Path)
	if len(match) != 2 || !bbcArticleIDPattern.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

func parseBBCArticleClips(ctx context.Context, page []byte) ([]Entry, error) {
	matches := bbcArticleClipPattern.FindAllStringSubmatch(string(page), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: no BBC article clips", ErrUnavailable)
	}
	seen := make(map[string]struct{})
	entries := make([]Entry, 0, len(matches))
	for index, match := range matches {
		if index%32 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if len(match) < 2 {
			continue
		}
		_, programmeID, ok := bbcTrustedProgrammeURL(match[1])
		if !ok {
			continue
		}
		if _, exists := seen[programmeID]; exists {
			continue
		}
		seen[programmeID] = struct{}{}
		entries = append(entries, bbcProgrammeChildEntry(programmeID))
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: no trusted BBC article clips", ErrInvalidMetadata)
	}
	if len(entries) > bbcMaxPlaylistEntries {
		return nil, ErrPlaylistLimit
	}
	return entries, nil
}
