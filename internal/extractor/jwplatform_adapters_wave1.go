package extractor

// Thin adapters onto the existing jwplatform backend. Each adapter
// discovers a validated 8-character JW Platform media id for one public site
// from the pinned upstream reference and hands off as `jwplatform:<id>`.
// No JW playback logic is duplicated here.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	jwWave1MaxEntries   = 128
	jwWave1MaxSlugBytes = 128
	jwWave1MaxTextBytes = 512
)

var (
	bundesligaPath            = regexp.MustCompile(`^/[a-z]{2}/bundesliga/videos(?:/[^?#]*)?$`)
	businessInsiderLabel      = regexp.MustCompile(`^[a-z0-9-]{1,63}$`)
	businessInsiderPath       = regexp.MustCompile(`^/(?:[^/?#]+/)*([^/?#&]{1,128})/?$`)
	businessInsiderIDPatterns = []*regexp.Regexp{
		regexp.MustCompile(`data-media-id=["']([a-zA-Z0-9]{8})`),
		regexp.MustCompile(`id=["']jwplayer_([a-zA-Z0-9]{8})`),
		regexp.MustCompile(`id["']?\s*:\s*["']?([a-zA-Z0-9]{8})`),
		regexp.MustCompile(`(?:jwplatform\.com/players/|jwplayer_)([a-zA-Z0-9]{8})`),
	}
	dbtvPath              = regexp.MustCompile(`^/video/(?:([^/?#]+)/)?([0-9A-Za-z_-]{11}|[A-Za-z0-9]{8})/?$`)
	hollywoodReporterPath = regexp.MustCompile(`^/video/([A-Za-z0-9_-]{1,128})/?$`)
	hollywoodReporterCard = regexp.MustCompile(`(?is)<a\b[^>]*class=["'][^"']*vlanding-video-card__link[^"']*["'][^>]*>`)
	iltalehtiPath         = regexp.MustCompile(`^/[^/?#]+/a/([A-Za-z0-9][A-Za-z0-9-]{0,63})/?$`)
	iltalehtiAppMarker    = regexp.MustCompile(`(?i)<script>\s*window\.App\s*=`)
	leFigaroEmbedPath     = regexp.MustCompile(`^/embed/(?:[^/?#]+/)+([A-Za-z0-9_-]{1,128})/?$`)
	jwWave1NextData       = regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>`)
	mirrorCoUKPath        = regexp.MustCompile(`^/(?:[^/?#]+/)*[A-Za-z0-9_-]+-(\d{1,16})/?$`)
	mirrorCoUKPlaceholder = regexp.MustCompile(`(?i)div\s+class="json-placeholder"\s+data-json="`)
	outsideTVPath         = regexp.MustCompile(`^/(?:[^/?#]+/)*play/[A-Za-z0-9]{8}/\d{1,10}/\d{1,10}/([A-Za-z0-9]{8})(?:/\d{1,10})?/?$`)
	theInterceptPath      = regexp.MustCompile(`^/fieldofvision/([^/?#]{1,128})/?$`)
	theInterceptStore     = regexp.MustCompile(`initialStoreTree\s*=`)
	jwWave1YouTubeID      = regexp.MustCompile(`^[0-9A-Za-z_-]{11}$`)
)

// jwWave1YouTubeHandoff produces a transparent YouTube URL handoff using the
// shared validated helper semantics. Transparent metadata from entry flows
// through the next extractor.
func jwWave1YouTubeHandoff(videoID string, entry Entry) (Extraction, error) {
	if !jwWave1YouTubeID.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: invalid YouTube handoff", ErrInvalidMetadata)
	}
	entry.URL = "https://www.youtube.com/watch?v=" + videoID
	entry.ExtractorKey = "youtube"
	if entry.ID == "" {
		entry.ID = videoID
	}
	entry.Transparent = true
	return URLResult(entry)
}

func jwWave1ReadBoundedPage(ctx context.Context, request Request, canonical, what string) ([]byte, error) {
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: %s page too large", ErrInvalidMetadata, what)
	}
	return page, nil
}

func jwWave1ExactHost(parsed *url.URL, bare string) bool {
	host := strings.ToLower(parsed.Hostname())
	return host == bare || host == "www."+bare
}

// Bundesliga routes bundesliga.com video pages, whose `vid` query parameter is
// the JW Platform media id, without any page fetch.
type Bundesliga struct{}

func NewBundesliga() Bundesliga { return Bundesliga{} }
func (Bundesliga) Name() string { return "bundesliga" }

func (Bundesliga) Suitable(parsed *url.URL) bool {
	_, ok := parseBundesligaURL(parsed)
	return ok
}

func (Bundesliga) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseBundesligaURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return jwPlatformURLEntry(videoID, Entry{ID: videoID})
}

func parseBundesligaURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "bundesliga.com") {
		return "", false
	}
	if !bundesligaPath.MatchString(parsed.EscapedPath()) {
		return "", false
	}
	videoID := parsed.Query().Get("vid")
	if !jwPlatformID.MatchString(videoID) {
		return "", false
	}
	return videoID, true
}

// BusinessInsider routes businessinsider.com/.nl articles via the pinned
// ordered JW Platform id patterns.
type BusinessInsider struct{}

func NewBusinessInsider() BusinessInsider { return BusinessInsider{} }
func (BusinessInsider) Name() string      { return "businessinsider" }

func (BusinessInsider) Suitable(parsed *url.URL) bool {
	_, ok := parseBusinessInsiderURL(parsed)
	return ok
}

func (BusinessInsider) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseBusinessInsiderURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://" + strings.ToLower(parsed.Hostname()) + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "Business Insider")
	if err != nil {
		return Extraction{}, err
	}
	for _, pattern := range businessInsiderIDPatterns {
		if match := pattern.FindSubmatch(page); len(match) == 2 {
			return jwPlatformURLEntry(string(match[1]), Entry{ID: displayID})
		}
	}
	return Extraction{}, classifyMissingMediaPage(page, "Business Insider JW Platform id")
}

func jwWave1BoundedSlug(slug string) bool {
	return slug != "" && len(slug) <= jwWave1MaxSlugBytes && !strings.ContainsAny(slug, "/?#\x00")
}

func parseBusinessInsiderURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !businessInsiderHost(strings.ToLower(parsed.Hostname())) {
		return "", false
	}
	match := businessInsiderPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !jwWave1BoundedSlug(match[1]) {
		return "", false
	}
	return match[1], true
}

func businessInsiderHost(host string) bool {
	for _, domain := range []string{"businessinsider.com", "businessinsider.nl"} {
		if host == domain {
			return true
		}
		if label, ok := strings.CutSuffix(host, "."+domain); ok && businessInsiderLabel.MatchString(label) {
			return true
		}
	}
	return false
}

// DBTV routes dagbladet.no video pages: 11-character ids are YouTube videos,
// 8-character ids are JW Platform media, both as transparent URL results.
type DBTV struct{}

func NewDBTV() DBTV       { return DBTV{} }
func (DBTV) Name() string { return "dbtv" }

func (DBTV) Suitable(parsed *url.URL) bool {
	_, ok := parseDBTVURL(parsed)
	return ok
}

func (DBTV) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseDBTVURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if len(videoID) == 11 {
		return jwWave1YouTubeHandoff(videoID, Entry{ID: videoID, Transparent: true})
	}
	return jwPlatformURLEntry(videoID, Entry{ID: videoID, Transparent: true})
}

func parseDBTVURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "dagbladet.no") {
		return "", false
	}
	match := dbtvPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", false
	}
	return match[2], true
}

// HollywoodReporter routes hollywoodreporter.com video pages through the
// showcase card attributes to JW Platform or YouTube.
type HollywoodReporter struct{}

func NewHollywoodReporter() HollywoodReporter { return HollywoodReporter{} }
func (HollywoodReporter) Name() string        { return "hollywoodreporter" }

func (HollywoodReporter) Suitable(parsed *url.URL) bool {
	_, ok := parseHollywoodReporterURL(parsed)
	return ok
}

func (HollywoodReporter) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseHollywoodReporterURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.hollywoodreporter.com" + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "Hollywood Reporter")
	if err != nil {
		return Extraction{}, err
	}
	card := hollywoodReporterCard.Find(page)
	if card == nil {
		return Extraction{}, classifyMissingMediaPage(page, "Hollywood Reporter showcase card")
	}
	attributes := wave2HTMLAttributes(string(card))
	videoID := attributes["data-video-showcase-trigger"]
	switch attributes["data-video-showcase-type"] {
	case "jwplayer":
		return jwPlatformURLEntry(videoID, Entry{ID: videoID})
	case "youtube":
		return jwWave1YouTubeHandoff(videoID, Entry{ID: videoID})
	default:
		// The page-controlled type value is never echoed into the error.
		return Extraction{}, fmt.Errorf("%w: unsupported Hollywood Reporter showcase type", ErrInvalidMetadata)
	}
}

func parseHollywoodReporterURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "hollywoodreporter.com") {
		return "", false
	}
	match := hollywoodReporterPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !jwWave1BoundedSlug(match[1]) {
		return "", false
	}
	return match[1], true
}

// Iltalehti routes iltalehti.fi articles: the balanced `window.App` state JSON
// yields an ordered, reusable playlist of jwplayer media ids.
type Iltalehti struct{}

func NewIltalehti() Iltalehti  { return Iltalehti{} }
func (Iltalehti) Name() string { return "iltalehti" }

func (Iltalehti) Suitable(parsed *url.URL) bool {
	_, ok := parseIltalehtiURL(parsed)
	return ok
}

func (Iltalehti) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	articleID, ok := parseIltalehtiURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.iltalehti.fi" + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "Iltalehti")
	if err != nil {
		return Extraction{}, err
	}
	title, mediaIDs, err := iltalehtiArticle(page)
	if err != nil {
		return Extraction{}, err
	}
	if len(mediaIDs) == 0 {
		return Extraction{}, classifyMissingMediaPage(page, "Iltalehti JW Platform embeds")
	}
	entries := make([]Entry, 0, len(mediaIDs))
	for _, mediaID := range mediaIDs {
		built, err := jwPlatformEntry(mediaID, Entry{})
		if err != nil {
			return Extraction{}, err
		}
		entries = append(entries, built)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(articleID)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	)
	if title != "" {
		info.Set("title", value.String(title))
	}
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

// iltalehtiArticle walks the balanced window.App JS object using the bounded
// jwWave1JSToJSON subset needed for Iltalehti app state. Media ids stay in
// upstream order: per article, main_media first and body items afterwards.
func iltalehtiArticle(page []byte) (string, []string, error) {
	raw, err := extractJSObjectAfter(page, iltalehtiAppMarker)
	if err != nil {
		return "", nil, classifyMissingMediaPage(page, "Iltalehti app state")
	}
	jsonBytes, err := jwWave1JSToJSON(raw)
	if err != nil {
		return "", nil, fmt.Errorf("%w: invalid Iltalehti app state", ErrInvalidMetadata)
	}
	var app struct {
		State struct {
			Articles []struct {
				Items struct {
					CanonicalTitle string            `json:"canonical_title"`
					MainMedia      json.RawMessage   `json:"main_media"`
					Body           []json.RawMessage `json:"body"`
				} `json:"items"`
			} `json:"articles"`
		} `json:"state"`
	}
	if err := json.Unmarshal(jsonBytes, &app); err != nil {
		return "", nil, fmt.Errorf("%w: invalid Iltalehti app state", ErrInvalidMetadata)
	}
	title := ""
	mediaIDs := make([]string, 0, 4)
	for _, article := range app.State.Articles {
		if title == "" {
			title = wave2BoundString(article.Items.CanonicalTitle, jwWave1MaxTextBytes)
		}
		candidates := make([]json.RawMessage, 0, 1+len(article.Items.Body))
		if article.Items.MainMedia != nil {
			candidates = append(candidates, article.Items.MainMedia)
		}
		candidates = append(candidates, article.Items.Body...)
		for _, candidate := range candidates {
			var item struct {
				Properties struct {
					Provider string `json:"provider"`
					ID       string `json:"id"`
				} `json:"properties"`
			}
			if json.Unmarshal(candidate, &item) != nil || item.Properties.Provider != "jwplayer" {
				continue
			}
			if !jwPlatformID.MatchString(item.Properties.ID) {
				return "", nil, fmt.Errorf("%w: invalid Iltalehti jwplayer id", ErrInvalidMetadata)
			}
			if len(mediaIDs) >= jwWave1MaxEntries {
				return "", nil, fmt.Errorf("%w: too many Iltalehti entries", ErrInvalidMetadata)
			}
			mediaIDs = append(mediaIDs, item.Properties.ID)
		}
	}
	return title, mediaIDs, nil
}

func parseIltalehtiURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "iltalehti.fi") {
		return "", false
	}
	match := iltalehtiPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// LeFigaroVideoEmbed routes video.lefigaro.fr embed pages via the balanced
// __NEXT_DATA__ payload's playerData.
type LeFigaroVideoEmbed struct{}

func NewLeFigaroVideoEmbed() LeFigaroVideoEmbed { return LeFigaroVideoEmbed{} }
func (LeFigaroVideoEmbed) Name() string         { return "lefigarovideoembed" }

func (LeFigaroVideoEmbed) Suitable(parsed *url.URL) bool {
	_, ok := parseLeFigaroVideoEmbedURL(parsed)
	return ok
}

func (LeFigaroVideoEmbed) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseLeFigaroVideoEmbedURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://video.lefigaro.fr" + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "Le Figaro")
	if err != nil {
		return Extraction{}, err
	}
	raw, err := extractJSONObjectAfter(page, jwWave1NextData)
	if err != nil {
		return Extraction{}, classifyMissingMediaPage(page, "Le Figaro next data")
	}
	var payload struct {
		Props struct {
			PageProps struct {
				InitialProps struct {
					PageData struct {
						PlayerData struct {
							VideoID string `json:"videoId"`
							Title   string `json:"title"`
							Poster  string `json:"poster"`
						} `json:"playerData"`
					} `json:"pageData"`
				} `json:"initialProps"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Le Figaro next data", ErrInvalidMetadata)
	}
	player := payload.Props.PageProps.InitialProps.PageData.PlayerData
	entry := Entry{ID: player.VideoID, Title: wave2BoundString(player.Title, jwWave1MaxTextBytes)}
	if player.VideoID == "" {
		return Extraction{}, fmt.Errorf("%w: missing Le Figaro video id", ErrInvalidMetadata)
	}
	if jwWave1HTTPPoster(player.Poster) {
		entry.Thumbnail = player.Poster
	}
	return jwPlatformURLEntry(player.VideoID, entry)
}

// jwWave1HTTPPoster accepts only strict-validated HTTPS poster URLs so HTTP or
// non-conforming posters are omitted without failing the extraction.
func jwWave1HTTPPoster(rawURL string) bool {
	if !strictValidHostedHTTPURL(rawURL) {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme == "https"
}

func parseLeFigaroVideoEmbedURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "video.lefigaro.fr" {
		return "", false
	}
	match := leFigaroEmbedPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !jwWave1BoundedSlug(match[1]) {
		return "", false
	}
	return match[1], true
}

// MirrorCoUK routes mirror.co.uk articles via the HTML-escaped, balanced
// json-placeholder payload. display_id is omitted until Entry supports it;
// the JW Platform media id is preserved as the handoff id.
type MirrorCoUK struct{}

func NewMirrorCoUK() MirrorCoUK { return MirrorCoUK{} }
func (MirrorCoUK) Name() string { return "mirrorcouk" }

func (MirrorCoUK) Suitable(parsed *url.URL) bool {
	_, ok := parseMirrorCoUKURL(parsed)
	return ok
}

func (MirrorCoUK) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseMirrorCoUKURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	_ = displayID
	canonical := "https://www.mirror.co.uk" + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "Mirror")
	if err != nil {
		return Extraction{}, err
	}
	mediaID, err := mirrorCoUKVideoID(page)
	if err != nil {
		return Extraction{}, err
	}
	return jwPlatformURLEntry(mediaID, Entry{ID: mediaID})
}

// mirrorCoUKVideoID unescapes the json-placeholder attribute and parses the
// balanced videoData object instead of matching nested JSON with regexes.
func mirrorCoUKVideoID(page []byte) (string, error) {
	location := mirrorCoUKPlaceholder.FindIndex(page)
	if location == nil {
		return "", classifyMissingMediaPage(page, "Mirror json-placeholder")
	}
	unescaped := htmlUnescapeAttr(string(page[location[1]:]))
	raw, _, err := extractJSONObjectFrom([]byte(unescaped), 0, 1)
	if err != nil {
		return "", fmt.Errorf("%w: invalid Mirror json-placeholder", ErrInvalidMetadata)
	}
	var payload struct {
		VideoData struct {
			VideoID string `json:"videoId"`
		} `json:"videoData"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("%w: invalid Mirror video data", ErrInvalidMetadata)
	}
	if !jwPlatformID.MatchString(payload.VideoData.VideoID) {
		return "", fmt.Errorf("%w: invalid Mirror JW Platform id", ErrInvalidMetadata)
	}
	return payload.VideoData.VideoID, nil
}

func parseMirrorCoUKURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "mirror.co.uk") {
		return "", false
	}
	match := mirrorCoUKPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// OutsideTV routes outsidetv.com play URLs whose final 8-character segment is
// the JW Platform media id, without any page fetch.
type OutsideTV struct{}

func NewOutsideTV() OutsideTV  { return OutsideTV{} }
func (OutsideTV) Name() string { return "outsidetv" }

func (OutsideTV) Suitable(parsed *url.URL) bool {
	_, ok := parseOutsideTVURL(parsed)
	return ok
}

func (OutsideTV) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	mediaID, ok := parseOutsideTVURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return jwPlatformURLEntry(mediaID, Entry{ID: mediaID})
}

func parseOutsideTVURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !jwWave1ExactHost(parsed, "outsidetv.com") {
		return "", false
	}
	match := outsideTVPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// TheIntercept routes theintercept.com Field of Vision posts via the balanced
// initialStoreTree JSON.
type TheIntercept struct{}

func NewTheIntercept() TheIntercept { return TheIntercept{} }
func (TheIntercept) Name() string   { return "theintercept" }

func (TheIntercept) Suitable(parsed *url.URL) bool {
	_, ok := parseTheInterceptURL(parsed)
	return ok
}

func (TheIntercept) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseTheInterceptURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://theintercept.com" + parsed.EscapedPath()
	page, err := jwWave1ReadBoundedPage(ctx, request, canonical, "The Intercept")
	if err != nil {
		return Extraction{}, err
	}
	raw, err := extractJSONObjectAfter(page, theInterceptStore)
	if err != nil {
		return Extraction{}, classifyMissingMediaPage(page, "The Intercept store tree")
	}
	var store struct {
		Resources struct {
			Posts map[string]struct {
				ID         json.Number `json:"ID"`
				Slug       string      `json:"slug"`
				Title      string      `json:"title"`
				Date       string      `json:"date"`
				FOVVideoID string      `json:"fov_videoid"`
			} `json:"posts"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &store); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid The Intercept store tree", ErrInvalidMetadata)
	}
	post, err := theInterceptPost(store.Resources.Posts, displayID)
	if err != nil {
		return Extraction{}, err
	}
	entry := Entry{
		ID:          post.id.String(),
		Title:       wave2BoundString(post.title, jwWave1MaxTextBytes),
		Transparent: true,
	}
	if timestamp := hostedUnixTimestamp(post.date); timestamp > 0 {
		entry.Timestamp, entry.HasTimestamp = timestamp, true
	}
	return jwPlatformURLEntry(post.videoID, entry)
}

type theInterceptPostData struct {
	id      json.Number
	slug    string
	title   string
	date    string
	videoID string
}

func theInterceptPost(posts map[string]struct {
	ID         json.Number `json:"ID"`
	Slug       string      `json:"slug"`
	Title      string      `json:"title"`
	Date       string      `json:"date"`
	FOVVideoID string      `json:"fov_videoid"`
}, slug string) (theInterceptPostData, error) {
	// Page and balanced-JSON byte caps already bound posts; we only enforce the
	// known slug once.
	var match theInterceptPostData
	found := false
	for key, candidate := range posts {
		if candidate.Slug != slug {
			continue
		}
		_ = key
		if found {
			return theInterceptPostData{}, fmt.Errorf("%w: duplicate The Intercept post slug", ErrInvalidMetadata)
		}
		match = theInterceptPostData{
			id:      candidate.ID,
			slug:    candidate.Slug,
			title:   candidate.Title,
			date:    candidate.Date,
			videoID: candidate.FOVVideoID,
		}
		found = true
	}
	if !found {
		return theInterceptPostData{}, fmt.Errorf("%w: missing The Intercept post", ErrInvalidMetadata)
	}
	return match, nil
}

func parseTheInterceptURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	// The pinned reference matches the bare host only.
	if strings.ToLower(parsed.Hostname()) != "theintercept.com" {
		return "", false
	}
	match := theInterceptPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !jwWave1BoundedSlug(match[1]) {
		return "", false
	}
	return match[1], true
}

var (
	_ Extractor = Bundesliga{}
	_ Extractor = BusinessInsider{}
	_ Extractor = DBTV{}
	_ Extractor = HollywoodReporter{}
	_ Extractor = Iltalehti{}
	_ Extractor = LeFigaroVideoEmbed{}
	_ Extractor = MirrorCoUK{}
	_ Extractor = OutsideTV{}
	_ Extractor = TheIntercept{}
)
