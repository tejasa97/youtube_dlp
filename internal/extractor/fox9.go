package extractor

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const fox9AnvatoAccessKey = "anvato_epfox_app_web_prod_b3373168e12f423f41504f207000188daf88251b"

var (
	fox9VideoIDPattern = regexp.MustCompile(`^[0-9]{1,32}$`)
	fox9NewsSlug       = regexp.MustCompile(`^[A-Za-z0-9-]{1,256}$`)
	fox9AnvatoID       = regexp.MustCompile(`anvatoId\s*:\s*['"](\d{1,32})['"]`)
)

// FOX9 is a thin Anvato adapter for fox9.com/video/<id> URLs.
type FOX9 struct{}

func NewFOX9() FOX9       { return FOX9{} }
func (FOX9) Name() string { return "fox9" }

func (FOX9) Suitable(parsed *url.URL) bool {
	_, ok := parseFOX9URL(parsed)
	return ok
}

func (FOX9) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseFOX9URL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{
		URL:          "anvato:" + fox9AnvatoAccessKey + ":" + videoID,
		ExtractorKey: "anvato",
		ID:           videoID,
	})
}

func parseFOX9URL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "fox9.com" && host != "www.fox9.com" {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 2 || segments[0] != "video" || !fox9VideoIDPattern.MatchString(segments[1]) {
		return "", false
	}
	return segments[1], true
}

// FOX9News fetches fox9.com/news/<slug> pages and hands off to FOX9/Anvato.
type FOX9News struct{}

func NewFOX9News() FOX9News   { return FOX9News{} }
func (FOX9News) Name() string { return "fox9_news" }

func (FOX9News) Suitable(parsed *url.URL) bool {
	_, ok := parseFOX9NewsURL(parsed)
	return ok
}

func (FOX9News) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseFOX9NewsURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.fox9.com/news/" + slug
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: FOX9 news page too large", ErrInvalidMetadata)
	}
	match := fox9AnvatoID.FindSubmatch(page)
	if len(match) != 2 {
		lower := strings.ToLower(string(page))
		if strings.Contains(lower, "sign in") || strings.Contains(lower, "log in") {
			return Extraction{}, ErrAuthentication
		}
		if strings.Contains(lower, "not found") {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: missing FOX9 Anvato id", ErrInvalidMetadata)
	}
	videoID := string(match[1])
	return URLResult(Entry{
		URL:          "https://www.fox9.com/video/" + videoID,
		ExtractorKey: "fox9",
		ID:           videoID,
	})
}

func parseFOX9NewsURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "fox9.com" && host != "www.fox9.com" {
		return "", false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 2 || segments[0] != "news" || !fox9NewsSlug.MatchString(segments[1]) {
		return "", false
	}
	return segments[1], true
}
