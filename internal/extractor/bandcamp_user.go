package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

var (
	bandcampItemLinkPattern   = regexp.MustCompile(`(?is)<li\s+data-item-id=["'][^>]+>\s*<a\s+href=["']([^"']+)`)
	bandcampTrackTitlePattern = regexp.MustCompile(`(?is)<div[^>]+trackTitle["'][^"']+["']([^"']+)`)
)

// BandcampUser extracts an artist discography playlist from the public artist
// page. Child track and album URLs are emitted as transparent Bandcamp entries.
type BandcampUser struct{}

func NewBandcampUser() BandcampUser { return BandcampUser{} }
func (BandcampUser) Name() string   { return "bandcamp_user" }
func (BandcampUser) Suitable(u *url.URL) bool {
	_, ok := classifyBandcampArtistURL(u)
	return ok
}

func (BandcampUser) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyBandcampArtistURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	page, err := requestBandcampIsolatedPage(ctx, request.Transport, request.URL)
	if err != nil {
		return Extraction{}, err
	}
	entries, err := parseBandcampDiscography(ctx, page, parsed, target.artist)
	if err != nil {
		return Extraction{}, err
	}
	title := "Discography of " + target.artist
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.artist)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
	)
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func parseBandcampDiscography(ctx context.Context, page []byte, base *url.URL, artist string) ([]Entry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(page) > bandcampMaxHTMLBytes {
		return nil, ErrJSONResponseTooLarge
	}
	webpage := string(page)
	links := bandcampItemLinkPattern.FindAllStringSubmatch(webpage, -1)
	if len(links) == 0 {
		links = bandcampTrackTitlePattern.FindAllStringSubmatch(webpage, -1)
	}
	rawHrefs := make([]string, 0, len(links)+16)
	for _, match := range links {
		if len(match) < 2 {
			continue
		}
		if strings.Contains(match[1], "/merch") {
			continue
		}
		rawHrefs = append(rawHrefs, match[1])
	}
	if gridLinks, err := parseBandcampMusicGridLinks(webpage); err != nil {
		return nil, err
	} else {
		rawHrefs = append(rawHrefs, gridLinks...)
	}
	seen := make(map[string]struct{}, len(rawHrefs))
	entries := make([]Entry, 0, len(rawHrefs))
	for _, raw := range rawHrefs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		joined, ok := bandcampJoinURL(base, raw)
		if !ok || !bandcampSameArtistChild(artist, joined) {
			continue
		}
		canonical, ok := bandcampCanonicalChildURL(artist, joined)
		if !ok {
			continue
		}
		if _, exists := seen[canonical]; exists {
			continue
		}
		if len(entries) >= bandcampDiscographyLimit {
			return nil, ErrPlaylistLimit
		}
		seen[canonical] = struct{}{}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(canonical, base.Scheme+"://"+artist+".bandcamp.com/"), "/"), "/")
		id := parts[1]
		entries = append(entries, Entry{
			URL:          canonical,
			ExtractorKey: "bandcamp",
			ID:           id,
			Transparent:  true,
		})
	}
	return entries, nil
}

func parseBandcampMusicGridLinks(webpage string) ([]string, error) {
	tag, ok := bandcampOpeningTag(webpage, "music-grid")
	if !ok {
		return nil, nil
	}
	raw, ok := bandcampHTMLAttributeInTag(tag, "data-client-items")
	if !ok {
		return nil, nil
	}
	decoded := html.UnescapeString(raw)
	if len(decoded) > int(maxExtractorJSONBytes) {
		return nil, ErrJSONResponseTooLarge
	}
	var items []struct {
		PageURL string `json:"page_url"`
	}
	if err := json.Unmarshal([]byte(decoded), &items); err != nil {
		return nil, fmt.Errorf("%w: invalid Bandcamp music-grid data", ErrInvalidMetadata)
	}
	links := make([]string, 0, len(items))
	for _, item := range items {
		if item.PageURL != "" {
			links = append(links, item.PageURL)
		}
	}
	return links, nil
}

func bandcampHTMLAttribute(webpage, elementID, attribute string) (string, bool) {
	tag, ok := bandcampOpeningTag(webpage, elementID)
	if !ok {
		return "", false
	}
	return bandcampHTMLAttributeInTag(tag, attribute)
}

func bandcampHTMLAttributeInTag(tag, attribute string) (string, bool) {
	attrIndex, ok := bandcampHTMLAttributeIndex(tag, attribute)
	if !ok {
		return "", false
	}
	attrLower := strings.ToLower(attribute) + "="
	value := strings.TrimSpace(tag[attrIndex+len(attrLower):])
	if value == "" {
		return "", false
	}
	quote := value[0]
	if quote != '"' && quote != '\'' {
		return "", false
	}
	value = value[1:]
	end := strings.IndexByte(value, quote)
	if end < 0 || !bandcampAttributeValueTailValid(value[end+1:]) {
		return "", false
	}
	return value[:end], true
}

func bandcampAttributeValueTailValid(tail string) bool {
	tail = strings.TrimSpace(tail)
	if tail == "" || strings.HasPrefix(tail, ">") {
		return true
	}
	for i := 0; i < len(tail); i++ {
		switch {
		case tail[i] == '>' || tail[i] == '=' || tail[i] == '"' || tail[i] == '\'':
		case tail[i] == ' ' || tail[i] == '\t' || tail[i] == '\n':
		case tail[i] == '-' || tail[i] == '_' || (tail[i] >= 'a' && tail[i] <= 'z') || (tail[i] >= 'A' && tail[i] <= 'Z') || (tail[i] >= '0' && tail[i] <= '9'):
		default:
			return false
		}
	}
	return true
}

func bandcampOpeningTag(webpage, elementID string) (string, bool) {
	lower := strings.ToLower(webpage)
	markers := []string{
		`id="` + strings.ToLower(elementID) + `"`,
		`id='` + strings.ToLower(elementID) + `'`,
	}
	for _, marker := range markers {
		index := 0
		for {
			found := strings.Index(lower[index:], marker)
			if found < 0 {
				break
			}
			found += index
			if !bandcampHTMLAttrNameBoundary(webpage, found) {
				index = found + 1
				continue
			}
			start := strings.LastIndexByte(webpage[:found], '<')
			if start < 0 {
				index = found + 1
				continue
			}
			end := strings.IndexByte(webpage[found:], '>')
			if end < 0 {
				return "", false
			}
			return webpage[start : found+end+1], true
		}
	}
	return "", false
}

func bandcampHTMLAttributeIndex(tag, attribute string) (int, bool) {
	tagLower := strings.ToLower(tag)
	attrLower := strings.ToLower(attribute) + "="
	index := 0
	for {
		found := strings.Index(tagLower[index:], attrLower)
		if found < 0 {
			return 0, false
		}
		found += index
		if bandcampHTMLAttrNameBoundary(tag, found) {
			return found, true
		}
		index = found + 1
	}
}

func bandcampHTMLAttrNameBoundary(tag string, attrStart int) bool {
	if attrStart == 0 {
		return false
	}
	switch tag[attrStart-1] {
	case ' ', '\t', '\n', '\r', '<':
		return true
	default:
		return false
	}
}
