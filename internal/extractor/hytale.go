package extractor

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	hytaleMaxPageBytes = 2 << 20
	hytaleMaxStreamIDs = 64
	hytaleMaxSlugBytes = 256
)

var (
	hytaleSlugPattern = regexp.MustCompile(`^[a-z0-9-]{1,256}$`)
	hytaleStreamSrc   = regexp.MustCompile(`(?i)<stream\b[^>]*\bsrc\s*=\s*["']([0-9a-f]{32})["']`)
	hytaleOGTitle     = regexp.MustCompile(`(?i)<meta\b[^>]*\bproperty\s*=\s*["']og:title["'][^>]*\bcontent\s*=\s*["']([^"']{1,512})["']`)
	hytaleOGTitleAlt  = regexp.MustCompile(`(?i)<meta\b[^>]*\bcontent\s*=\s*["']([^"']{1,512})["'][^>]*\bproperty\s*=\s*["']og:title["']`)
	hytaleTitleTag    = regexp.MustCompile(`(?is)<title[^>]*>\s*([^<]{1,512})\s*</title>`)
)

// Hytale is a thin Cloudflare Stream adapter for documented Hytale news URLs.
// It emits ordered lazy URL results to the Cloudflare Stream backend and never
// claims arbitrary cloudflarestream.com hosts itself.
type Hytale struct{}

func NewHytale() Hytale     { return Hytale{} }
func (Hytale) Name() string { return "hytale" }

type hytaleTarget struct {
	id, canonical string
}

func (Hytale) Suitable(parsed *url.URL) bool {
	_, ok := parseHytaleURL(parsed)
	return ok
}

func (Hytale) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseHytaleURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(target.id)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	sequence, err := LazyFirstPageEntries(hytaleMaxStreamIDs, func(ctx context.Context) ([]Entry, error) {
		page, _, err := request.Transport.ReadPage(ctx, target.canonical)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, _, err := parseHytaleEntries(page)
		return entries, err
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(info), sequence)
}

func parseHytaleEntries(page []byte) ([]Entry, string, error) {
	if int64(len(page)) > hytaleMaxPageBytes {
		return nil, "", fmt.Errorf("%w: Hytale page too large", ErrInvalidMetadata)
	}
	lower := bytes.ToLower(page)
	if bytes.Contains(lower, []byte("sign in")) || bytes.Contains(lower, []byte("log in")) {
		return nil, "", ErrAuthentication
	}
	if bytes.Contains(lower, []byte("not found")) || bytes.Contains(lower, []byte("page not found")) {
		return nil, "", ErrUnavailable
	}

	matches := hytaleStreamSrc.FindAllSubmatch(page, hytaleMaxStreamIDs+1)
	if len(matches) == 0 {
		return nil, "", fmt.Errorf("%w: missing Hytale Cloudflare Stream embeds", ErrInvalidMetadata)
	}
	if len(matches) > hytaleMaxStreamIDs {
		return nil, "", fmt.Errorf("%w: Hytale stream embed limit", ErrInvalidMetadata)
	}

	seen := make(map[string]struct{}, len(matches))
	entries := make([]Entry, 0, len(matches))
	for _, match := range matches {
		if len(match) != 2 {
			continue
		}
		videoID := string(match[1])
		if !cloudflareStreamHexID.MatchString(videoID) {
			continue
		}
		if _, exists := seen[videoID]; exists {
			continue // first-wins duplicate policy
		}
		seen[videoID] = struct{}{}
		entries = append(entries, Entry{
			URL:          "https://cloudflarestream.com/" + videoID + "/manifest/video.mpd",
			ExtractorKey: "cloudflarestream",
			ID:           videoID,
			Transparent:  true,
		})
	}
	if len(entries) == 0 {
		return nil, "", fmt.Errorf("%w: no usable Hytale stream embeds", ErrInvalidMetadata)
	}
	return entries, hytalePageTitle(page), nil
}

func parseHytaleURL(parsed *url.URL) (hytaleTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes || hostedRejectUnsafeURL(parsed) {
		return hytaleTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "hytale.com" && host != "www.hytale.com" {
		return hytaleTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 4 || segments[0] != "news" || len(segments[1]) != 4 || len(segments[2]) != 2 {
		return hytaleTarget{}, false
	}
	for _, r := range segments[1] + segments[2] {
		if r < '0' || r > '9' {
			return hytaleTarget{}, false
		}
	}
	if !hytaleSlugPattern.MatchString(segments[3]) || len(segments[3]) > hytaleMaxSlugBytes {
		return hytaleTarget{}, false
	}
	id := segments[3]
	return hytaleTarget{id: id, canonical: "https://hytale.com/news/" + segments[1] + "/" + segments[2] + "/" + id}, true
}

func hytalePageTitle(page []byte) string {
	if match := hytaleOGTitle.FindSubmatch(page); len(match) == 2 {
		return strings.TrimSpace(string(match[1]))
	}
	if match := hytaleOGTitleAlt.FindSubmatch(page); len(match) == 2 {
		return strings.TrimSpace(string(match[1]))
	}
	if match := hytaleTitleTag.FindSubmatch(page); len(match) == 2 {
		return strings.TrimSpace(string(match[1]))
	}
	return ""
}
