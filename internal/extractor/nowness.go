package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	nownessMaxEntries = 256
	nownessMaxPosts   = 256
)

var (
	nownessStoryPath    = regexp.MustCompile(`(?i)^/(?:story|(?:series|category)/[^/]+)/([A-Za-z0-9_-]{1,256})/?$`)
	nownessPlaylistPath = regexp.MustCompile(`(?i)^/playlist/([0-9]{1,16})(?:/[A-Za-z0-9_-]{1,256})?/?$`)
	nownessSeriesPath   = regexp.MustCompile(`(?i)^/series/([A-Za-z0-9_-]{1,256})/?$`)
	nownessBrightcove   = regexp.MustCompile(`(?i)https?://players\.brightcove\.net/[0-9]+/[A-Za-z0-9_-]+/index\.html\?[^"'\s>]*videoId=[0-9]+`)
	nownessLegacyBC     = regexp.MustCompile(`(?i)https?://c\.brightcove\.com/services/viewer/federated_f9\?[^"'\s>]+`)
	nownessSlug         = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	nownessDigits       = regexp.MustCompile(`^[0-9]{1,16}$`)
	nownessVimeoID      = regexp.MustCompile(`^[0-9]{1,16}$`)
)

func nownessHostOK(host string) bool {
	switch strings.ToLower(host) {
	case "nowness.com", "www.nowness.com", "cn.nowness.com":
		return true
	default:
		return false
	}
}

func nownessLanguage(host string) string {
	if strings.EqualFold(host, "cn.nowness.com") {
		return "zh-cn"
	}
	return "en-us"
}

func nownessAPIHeaders(host string) http.Header {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("X-Nowness-Language", nownessLanguage(host))
	return headers
}

// Nowness extracts a single NOWNESS story into a Brightcove/Vimeo URL result.
type Nowness struct{}

func NewNowness() Nowness    { return Nowness{} }
func (Nowness) Name() string { return "nowness" }

func (Nowness) Suitable(parsed *url.URL) bool {
	_, ok := parseNownessStoryURL(parsed)
	return ok
}

func (Nowness) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseNownessStoryURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	host := strings.ToLower(parsed.Hostname())
	endpoint := "https://api.nowness.com/api/post/getBySlug/" + url.PathEscape(slug)
	var post nownessPost
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nownessAPIHeaders(host), &post); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return nownessURLResult(ctx, request.Transport, post)
}

func parseNownessStoryURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) || !nownessHostOK(parsed.Hostname()) {
		return "", false
	}
	if nownessPlaylistPath.MatchString(parsed.EscapedPath()) || nownessSeriesPath.MatchString(parsed.EscapedPath()) {
		return "", false
	}
	match := nownessStoryPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !nownessSlug.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

type nownessPost struct {
	Type  string `json:"type"`
	Slug  string `json:"slug"`
	Media []struct {
		Type    string `json:"type"`
		Source  string `json:"source"`
		Content string `json:"content"`
	} `json:"media"`
}

func nownessURLResult(ctx context.Context, transport Transport, post nownessPost) (Extraction, error) {
	if !strings.EqualFold(post.Type, "video") {
		return Extraction{}, fmt.Errorf("%w: unsupported NOWNESS post type", ErrInvalidMetadata)
	}
	for _, media := range post.Media {
		if !strings.EqualFold(media.Type, "video") {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(media.Source)) {
		case "brightcove":
			videoID := strings.TrimSpace(media.Content)
			if !brightcoveDigitsID.MatchString(videoID) {
				continue
			}
			iframeURL := "https://www.nowness.com/iframe?id=" + url.QueryEscape(videoID)
			page, _, err := transport.ReadPage(ctx, iframeURL)
			if err != nil {
				return Extraction{}, err
			}
			if err := ctx.Err(); err != nil {
				return Extraction{}, err
			}
			if int64(len(page)) > maxExtractorJSONBytes {
				return Extraction{}, fmt.Errorf("%w: NOWNESS iframe too large", ErrInvalidMetadata)
			}
			if match := nownessBrightcove.Find(page); len(match) > 0 {
				return URLResult(Entry{
					URL:          string(match),
					ExtractorKey: "brightcove",
					ID:           videoID,
					Transparent:  true,
				})
			}
			if match := nownessLegacyBC.Find(page); len(match) > 0 {
				return URLResult(Entry{
					URL:          string(match),
					ExtractorKey: "brightcove",
					ID:           videoID,
					Transparent:  true,
				})
			}
			return Extraction{}, fmt.Errorf("%w: missing NOWNESS Brightcove player", ErrInvalidMetadata)
		case "vimeo":
			id := strings.TrimSpace(media.Content)
			if !nownessVimeoID.MatchString(id) {
				continue
			}
			return URLResult(Entry{
				URL:          "https://vimeo.com/" + id,
				ExtractorKey: "vimeo",
				ID:           id,
				Transparent:  true,
			})
		case "youtube":
			// Deliberate: YouTube handoff is out of scope for this breadth program.
			continue
		}
	}
	return Extraction{}, fmt.Errorf("%w: missing NOWNESS playable media", ErrInvalidMetadata)
}

// NownessPlaylist enumerates posts for a numeric NOWNESS playlist id.
type NownessPlaylist struct{}

func NewNownessPlaylist() NownessPlaylist { return NownessPlaylist{} }
func (NownessPlaylist) Name() string      { return "nowness_playlist" }

func (NownessPlaylist) Suitable(parsed *url.URL) bool {
	_, ok := parseNownessPlaylistURL(parsed)
	return ok
}

func (NownessPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseNownessPlaylistURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	host := strings.ToLower(parsed.Hostname())
	endpoint := "https://api.nowness.com/api/post?PlaylistId=" + url.QueryEscape(id)
	canonical := "https://www.nowness.com/playlist/" + id
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(id)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(nownessMaxEntries, func(ctx context.Context) ([]Entry, error) {
		var payload struct {
			Items []nownessPost `json:"items"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nownessAPIHeaders(host), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(payload.Items) > nownessMaxEntries {
			return nil, fmt.Errorf("%w: NOWNESS playlist overflow", ErrInvalidMetadata)
		}
		entries, err := nownessPostEntries(payload.Items)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty NOWNESS playlist", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseNownessPlaylistURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) || !nownessHostOK(parsed.Hostname()) {
		return "", false
	}
	match := nownessPlaylistPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !nownessDigits.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

// NownessSeries enumerates posts for a NOWNESS series slug.
type NownessSeries struct{}

func NewNownessSeries() NownessSeries { return NownessSeries{} }
func (NownessSeries) Name() string    { return "nowness_series" }

func (NownessSeries) Suitable(parsed *url.URL) bool {
	_, ok := parseNownessSeriesURL(parsed)
	return ok
}

func (NownessSeries) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseNownessSeriesURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	host := strings.ToLower(parsed.Hostname())
	endpoint := "https://api.nowness.com/api/series/getBySlug/" + url.PathEscape(slug)
	canonical := "https://www.nowness.com/series/" + slug
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "title", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(nownessMaxPosts, func(ctx context.Context) ([]Entry, error) {
		var payload struct {
			ID    any           `json:"id"`
			Posts []nownessPost `json:"posts"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, nownessAPIHeaders(host), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(payload.Posts) > nownessMaxPosts {
			return nil, fmt.Errorf("%w: NOWNESS series overflow", ErrInvalidMetadata)
		}
		entries, err := nownessPostEntries(payload.Posts)
		if err != nil {
			return nil, err
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty NOWNESS series", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseNownessSeriesURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) || !nownessHostOK(parsed.Hostname()) {
		return "", false
	}
	// Prefer playlist matcher when path is /playlist/...
	if nownessPlaylistPath.MatchString(parsed.EscapedPath()) {
		return "", false
	}
	match := nownessSeriesPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !nownessSlug.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

func nownessPostEntries(posts []nownessPost) ([]Entry, error) {
	entries := make([]Entry, 0, len(posts))
	seen := make(map[string]bool, len(posts))
	for _, post := range posts {
		slug := strings.TrimSpace(post.Slug)
		if !nownessSlug.MatchString(slug) || seen[slug] {
			continue
		}
		seen[slug] = true
		entries = append(entries, Entry{
			URL:          "https://www.nowness.com/story/" + slug,
			ExtractorKey: "nowness",
			ID:           slug,
		})
	}
	return entries, nil
}
