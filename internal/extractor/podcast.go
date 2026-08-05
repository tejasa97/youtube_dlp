package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	podcastMaxEpisodes = 500
	podcastMaxTitle    = 1024
)

var (
	podcastSlug         = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,256}$`)
	podcastUUID         = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	podcastDigits       = regexp.MustCompile(`^[0-9]{1,16}$`)
	acastEpisodePath    = regexp.MustCompile(`(?i)^/(?:s/)?([A-Za-z0-9._~-]{1,128})/(?:episodes/)?([A-Za-z0-9._~-]{1,256})/?$`)
	acastChannelPath    = regexp.MustCompile(`(?i)^/(?:s/)?([A-Za-z0-9._~-]{1,128})/?$`)
	simplecastUUIDPath  = regexp.MustCompile(`(?i)^/(?:episodes/)?([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/?$`)
	simplecastEpPath    = regexp.MustCompile(`(?i)^/episodes/([A-Za-z0-9._~-]{1,256})/?$`)
	megaphonePath       = regexp.MustCompile(`(?i)^/([A-Z0-9]{1,32})/?$`)
	megaphoneEpisode    = regexp.MustCompile(`(?is)var\s+episode\s*=\s*(\{.*?\})\s*;`)
	art19EpisodePath    = regexp.MustCompile(`(?i)^/shows/([A-Za-z0-9_-]{1,128})/episodes/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/?$`)
	art19ShowPath       = regexp.MustCompile(`(?i)^/shows/([A-Za-z0-9_-]{1,128})(?:/embed)?/?$`)
	art19RSSPath        = regexp.MustCompile(`(?i)^/episodes/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})\.mp3$`)
	libsynPath          = regexp.MustCompile(`(?i)^/embed/episode/id/([0-9]{1,16})(?:/.*)?$`)
	libsynPlaylist      = regexp.MustCompile(`(?is)var\s+playlistItem\s*=\s*(\{.*?\});`)
	spreakerEpisodePath = regexp.MustCompile(`(?i)^/(?:(?:download/)?episode|v2/episodes)/([0-9]{1,16})(?:/.*)?$`)
	spreakerWebEpisode  = regexp.MustCompile(`(?i)^/episode/(?:[^/#?]*=-)?([0-9]{1,16})/?$`)
	spreakerShowAPI     = regexp.MustCompile(`(?i)^/show/([0-9]{1,16})/?$`)
	spreakerShowWeb     = regexp.MustCompile(`(?i)^/podcast/[A-Za-z0-9_-]+--([0-9]{1,16})/?$`)
	spreakerShowFeed    = regexp.MustCompile(`(?i)^/show/([0-9]{1,16})/episodes/feed/?$`)
	ogAudioTitle        = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:audio:title["'][^>]+content=["']([^"']+)["']`)
	ogAudioArtist       = regexp.MustCompile(`(?is)<meta[^>]+property=["']og:audio:artist["'][^>]+content=["']([^"']+)["']`)
	ogImage             = regexp.MustCompile(`(?is)<meta[^>]+(?:property=["']og:image["']|name=["']twitter:image["'])[^>]+content=["']([^"']+)["']`)
)

func podcastHTTPMedia(formatID, rawURL string) (*value.Object, bool) {
	cleaned, ok := cleanPodcastMediaURL(rawURL, sharedHostingMaxURLBytes)
	if !ok {
		return nil, false
	}
	parsed, _ := url.Parse(cleaned)
	ext := strings.ToLower(strings.TrimPrefix(pathExt(parsed.Path), "."))
	if ext == "" {
		ext = "mp3"
	}
	obj := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(cleaned)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "protocol", Value: value.String(strings.ToLower(parsed.Scheme))},
		value.Field{Key: "vcodec", Value: value.String("none")},
	)
	return obj, true
}

func podcastMediaInfo(id, title, webpageURL, mediaURL string, extra ...value.Field) (Extraction, error) {
	format, ok := podcastHTTPMedia("http", mediaURL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid podcast media URL", ErrInvalidMetadata)
	}
	if title == "" {
		title = id
	}
	if len(title) > podcastMaxTitle {
		title = title[:podcastMaxTitle]
	}
	ext, _ := format.Lookup("ext").StringValue()
	fields := []value.Field{
		{Key: "id", Value: value.String(id)},
		{Key: "title", Value: value.String(title)},
		{Key: "webpage_url", Value: value.String(webpageURL)},
		{Key: "ext", Value: value.String(ext)},
		{Key: "formats", Value: value.List(value.ObjectValue(format))},
	}
	fields = append(fields, extra...)
	return Media(value.NewInfo(value.NewObject(fields...))), nil
}

// ACast extracts episodes from feeder.acast.com.
type ACast struct{}

func NewACast() ACast      { return ACast{} }
func (ACast) Name() string { return "acast" }

func (ACast) Suitable(parsed *url.URL) bool {
	_, _, ok := parseACastEpisodeURL(parsed)
	return ok
}

func (ACast) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	channel, episode, ok := parseACastEpisodeURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://feeder.acast.com/api/v1/shows/" + url.PathEscape(channel) + "/episodes/" + url.PathEscape(episode) + "?showInfo=true"
	var payload struct {
		ID          string        `json:"id"`
		Title       string        `json:"title"`
		EpisodeURL  string        `json:"episodeUrl"`
		URL         string        `json:"url"`
		Description string        `json:"description"`
		Summary     string        `json:"summary"`
		Image       string        `json:"image"`
		PublishDate string        `json:"publishDate"`
		Duration    hostingNumber `json:"duration"`
		ContentSize hostingNumber `json:"contentLength"`
		Season      hostingNumber `json:"season"`
		Episode     hostingNumber `json:"episode"`
		Show        struct {
			Title  string `json:"title"`
			Author string `json:"author"`
		} `json:"show"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	id := payload.ID
	if id == "" {
		id = episode
	}
	extra := []value.Field{}
	if payload.Show.Title != "" {
		extra = append(extra, value.Field{Key: "series", Value: value.String(payload.Show.Title)})
	}
	if payload.Show.Author != "" {
		extra = append(extra, value.Field{Key: "uploader", Value: value.String(payload.Show.Author)})
		extra = append(extra, value.Field{Key: "creator", Value: value.String(payload.Show.Author)})
	}
	if description := firstNonEmpty(payload.Description, payload.Summary); description != "" {
		extra = append(extra, value.Field{Key: "description", Value: value.String(description)})
	}
	if payload.Image != "" && strictValidHostedHTTPURL(payload.Image) {
		extra = append(extra, value.Field{Key: "thumbnail", Value: value.String(payload.Image)})
	}
	if d := payload.Duration.int64(); d > 0 {
		extra = append(extra, value.Field{Key: "duration", Value: value.Int(d)})
	}
	if timestamp := hostedUnixTimestamp(payload.PublishDate); timestamp > 0 {
		extra = append(extra, value.Field{Key: "timestamp", Value: value.Int(timestamp)})
	}
	if size := payload.ContentSize.int64(); size > 0 {
		extra = append(extra, value.Field{Key: "filesize", Value: value.Int(size)})
	}
	if season := payload.Season.int64(); season > 0 {
		extra = append(extra, value.Field{Key: "season_number", Value: value.Int(season)})
	}
	if episodeNumber := payload.Episode.int64(); episodeNumber > 0 {
		extra = append(extra, value.Field{Key: "episode_number", Value: value.Int(episodeNumber)})
	}
	if payload.EpisodeURL != "" {
		extra = append(extra, value.Field{Key: "display_id", Value: value.String(payload.EpisodeURL)})
	}
	if payload.Title != "" {
		extra = append(extra, value.Field{Key: "episode", Value: value.String(payload.Title)})
	}
	return podcastMediaInfo(id, payload.Title, "https://shows.acast.com/"+channel+"/episodes/"+episode, payload.URL, extra...)
}

func parseACastEpisodeURL(parsed *url.URL) (channel, episode string, ok bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "acast.com", "www.acast.com", "shows.acast.com", "embed.acast.com", "play.acast.com":
	default:
		return "", "", false
	}
	path := parsed.EscapedPath()
	if host == "play.acast.com" && !strings.HasPrefix(strings.ToLower(path), "/s/") {
		return "", "", false
	}
	if host == "play.acast.com" {
		path = strings.TrimPrefix(path, "/s")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}
	match := acastEpisodePath.FindStringSubmatch(path)
	if len(match) != 3 || !podcastSlug.MatchString(match[1]) || !podcastSlug.MatchString(match[2]) {
		return "", "", false
	}
	return match[1], match[2], true
}

// ACastChannel enumerates show episodes from feeder.acast.com.
type ACastChannel struct{}

func NewACastChannel() ACastChannel { return ACastChannel{} }
func (ACastChannel) Name() string   { return "acast_channel" }

func (ACastChannel) Suitable(parsed *url.URL) bool {
	_, ok := parseACastChannelURL(parsed)
	return ok
}

func (ACastChannel) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseACastChannelURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://feeder.acast.com/api/v1/shows/" + url.PathEscape(slug)
	canonical := "https://shows.acast.com/" + slug
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "title", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(podcastMaxEpisodes, func(ctx context.Context) ([]Entry, error) {
		var payload struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Description string `json:"description"`
			Episodes    []struct {
				ID         string `json:"id"`
				Title      string `json:"title"`
				URL        string `json:"url"`
				EpisodeURL string `json:"episodeUrl"`
			} `json:"episodes"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(payload.Episodes) > podcastMaxEpisodes {
			return nil, fmt.Errorf("%w: Acast channel overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(payload.Episodes))
		for _, ep := range payload.Episodes {
			display := ep.EpisodeURL
			if display == "" {
				display = ep.ID
			}
			if !podcastSlug.MatchString(display) && !podcastUUID.MatchString(display) {
				continue
			}
			entries = append(entries, Entry{
				URL:          "https://shows.acast.com/" + slug + "/episodes/" + display,
				ExtractorKey: "acast",
				ID:           firstNonEmpty(ep.ID, display),
				Title:        ep.Title,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Acast channel", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseACastChannelURL(parsed *url.URL) (string, bool) {
	if _, _, episodeOK := parseACastEpisodeURL(parsed); episodeOK {
		return "", false
	}
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "acast.com", "www.acast.com", "shows.acast.com", "play.acast.com":
	default:
		return "", false
	}
	path := parsed.EscapedPath()
	if host == "play.acast.com" {
		if !strings.HasPrefix(strings.ToLower(path), "/s/") {
			return "", false
		}
		path = strings.TrimPrefix(path, "/s")
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
	}
	match := acastChannelPath.FindStringSubmatch(path)
	if len(match) != 2 || !podcastSlug.MatchString(match[1]) {
		return "", false
	}
	return match[1], true
}

// Simplecast extracts api/player.simplecast.com episode UUIDs.
type Simplecast struct{}

func NewSimplecast() Simplecast { return Simplecast{} }
func (Simplecast) Name() string { return "simplecast" }

type simplecastEpisodePayload struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Slug         string        `json:"slug"`
	Description  string        `json:"description"`
	Duration     hostingNumber `json:"duration"`
	PublishedAt  string        `json:"published_at"`
	Number       hostingNumber `json:"number"`
	EpisodeURL   string        `json:"episode_url"`
	ImageURL     string        `json:"image_url"`
	EnclosureURL string        `json:"enclosure_url"`
	AudioFileURL string        `json:"audio_file_url"`
	AudioFile    struct {
		URL  string        `json:"url"`
		Size hostingNumber `json:"size"`
	} `json:"audio_file"`
	AudioFileSize hostingNumber `json:"audio_file_size"`
	Season        struct {
		Number hostingNumber `json:"number"`
		Href   string        `json:"href"`
	} `json:"season"`
	Podcast struct {
		Title string `json:"title"`
	} `json:"podcast"`
}

func (Simplecast) Suitable(parsed *url.URL) bool {
	_, ok := parseSimplecastURL(parsed)
	return ok
}

func (Simplecast) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseSimplecastURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractSimplecastEpisode(ctx, request.Transport, id, "https://player.simplecast.com/"+id)
}

func parseSimplecastURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "api.simplecast.com" && host != "player.simplecast.com" {
		return "", false
	}
	match := simplecastUUIDPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

func extractSimplecastEpisode(ctx context.Context, transport Transport, id, webpageURL string) (Extraction, error) {
	endpoint := "https://api.simplecast.com/episodes/" + id
	var payload simplecastEpisodePayload
	if err := hostedRequestJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	return simplecastEpisodeExtraction(payload, id, webpageURL, false)
}

func simplecastEpisodeExtraction(payload simplecastEpisodePayload, expectedID, webpageURL string, bindWebpage bool) (Extraction, error) {
	id := strings.ToLower(strings.TrimSpace(payload.ID))
	if !podcastUUID.MatchString(id) || expectedID != "" && !strings.EqualFold(id, expectedID) {
		return Extraction{}, fmt.Errorf("%w: invalid Simplecast episode id", ErrInvalidMetadata)
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Simplecast episode title", ErrInvalidMetadata)
	}
	if len(title) > podcastMaxTitle {
		title = title[:podcastMaxTitle]
	}
	mediaURL := firstNonEmpty(payload.AudioFile.URL, payload.AudioFileURL, payload.EnclosureURL)
	extra := []value.Field{}
	if payload.Podcast.Title != "" {
		extra = append(extra, value.Field{Key: "series", Value: value.String(payload.Podcast.Title)})
	}
	if description := strings.TrimSpace(payload.Description); description != "" {
		extra = append(extra, value.Field{Key: "description", Value: value.String(description)})
	}
	if payload.ImageURL != "" && strictValidHostedHTTPURL(payload.ImageURL) {
		extra = append(extra, value.Field{Key: "thumbnail", Value: value.String(payload.ImageURL)})
	}
	if d := payload.Duration.int64(); d > 0 {
		extra = append(extra, value.Field{Key: "duration", Value: value.Int(d)})
	}
	if payload.Slug != "" {
		extra = append(extra, value.Field{Key: "display_id", Value: value.String(payload.Slug)})
	}
	extra = append(extra, value.Field{Key: "episode", Value: value.String(title)})
	if timestamp := hostedUnixTimestamp(payload.PublishedAt); timestamp > 0 {
		extra = append(extra, value.Field{Key: "timestamp", Value: value.Int(timestamp)})
	}
	if number := payload.Number.int64(); number > 0 {
		extra = append(extra, value.Field{Key: "episode_number", Value: value.Int(number)})
	}
	if number := payload.Season.Number.int64(); number > 0 {
		extra = append(extra, value.Field{Key: "season_number", Value: value.Int(number)})
	}
	if seasonID := simplecastSeasonID(payload.Season.Href); seasonID != "" {
		extra = append(extra, value.Field{Key: "season_id", Value: value.String(seasonID)})
	}
	size := payload.AudioFile.Size.int64()
	if size <= 0 {
		size = payload.AudioFileSize.int64()
	}
	if size > 0 {
		extra = append(extra, value.Field{Key: "filesize", Value: value.Int(size)})
	}
	if canonical, channelURL, ok := simplecastEpisodeWebpage(payload.EpisodeURL); ok && (!bindWebpage || canonical == webpageURL) {
		webpageURL = canonical
		extra = append(extra, value.Field{Key: "channel_url", Value: value.String(channelURL)})
	}
	extra = append(extra, value.Field{Key: "episode_id", Value: value.String(id)})
	return podcastMediaInfo(id, title, webpageURL, mediaURL, extra...)
}

func simplecastSeasonID(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "api.simplecast.com" ||
		parsed.User != nil || parsed.Port() != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "seasons" || !podcastUUID.MatchString(parts[1]) {
		return ""
	}
	return strings.ToLower(parts[1])
}

func simplecastEpisodeWebpage(raw string) (canonical, channelURL string, ok bool) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	host, slug, ok := parseSimplecastEpisodeURL(parsed)
	if !ok {
		return "", "", false
	}
	channelURL = "https://" + host
	return channelURL + "/episodes/" + slug, channelURL, true
}

// SimplecastEpisode resolves customer subdomain episode pages via search API.
type SimplecastEpisode struct{}

func NewSimplecastEpisode() SimplecastEpisode { return SimplecastEpisode{} }
func (SimplecastEpisode) Name() string        { return "simplecast_episode" }

func (SimplecastEpisode) Suitable(parsed *url.URL) bool {
	_, _, ok := parseSimplecastEpisodeURL(parsed)
	return ok
}

func (SimplecastEpisode) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	host, slug, ok := parseSimplecastEpisodeURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://" + host + "/episodes/" + slug
	body := []byte("url=" + url.QueryEscape(canonical))
	headers := make(http.Header)
	headers.Set("Content-Type", "application/x-www-form-urlencoded")
	var payload simplecastEpisodePayload
	if err := hostedRequestJSONWithoutCredentialsNoRedirect(ctx, request.Transport, http.MethodPost, "https://api.simplecast.com/episodes/search", body, headers, &payload); err != nil {
		return Extraction{}, err
	}
	return simplecastEpisodeExtraction(payload, "", canonical, true)
}

func parseSimplecastEpisodeURL(parsed *url.URL) (host, slug string, ok bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", "", false
	}
	host = strings.ToLower(parsed.Hostname())
	if host == "" || strings.HasSuffix(host, ".simplecast.com") == false {
		return "", "", false
	}
	switch host {
	case "api.simplecast.com", "player.simplecast.com", "cdn.simplecast.com", "embed.simplecast.com", "feeds.simplecast.com":
		return "", "", false
	}
	match := simplecastEpPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || !podcastSlug.MatchString(match[1]) {
		return "", "", false
	}
	return host, match[1], true
}

// SimplecastPodcast enumerates podcast episodes for a customer subdomain.
type SimplecastPodcast struct{}

func NewSimplecastPodcast() SimplecastPodcast { return SimplecastPodcast{} }
func (SimplecastPodcast) Name() string        { return "simplecast_podcast" }

func (SimplecastPodcast) Suitable(parsed *url.URL) bool {
	_, ok := parseSimplecastPodcastURL(parsed)
	return ok
}

func (SimplecastPodcast) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	host, ok := parseSimplecastPodcastURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://" + host + "/"
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(host)},
		value.Field{Key: "title", Value: value.String(host)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(podcastMaxEpisodes, func(ctx context.Context) ([]Entry, error) {
		body := []byte("url=" + url.QueryEscape(canonical))
		headers := make(http.Header)
		headers.Set("Content-Type", "application/x-www-form-urlencoded")
		var site struct {
			Podcast struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"podcast"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodPost, "https://api.simplecast.com/sites/search", body, headers, &site); err != nil {
			return nil, err
		}
		if !podcastUUID.MatchString(site.Podcast.ID) {
			return nil, fmt.Errorf("%w: missing Simplecast podcast id", ErrInvalidMetadata)
		}
		podcastID := strings.ToLower(site.Podcast.ID)
		var episodes struct {
			Collection []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"collection"`
		}
		listURL := "https://api.simplecast.com/podcasts/" + podcastID + "/episodes"
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, listURL, nil, make(http.Header), &episodes); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(episodes.Collection) > podcastMaxEpisodes {
			return nil, fmt.Errorf("%w: Simplecast podcast overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(episodes.Collection))
		for _, ep := range episodes.Collection {
			if !podcastUUID.MatchString(ep.ID) {
				continue
			}
			id := strings.ToLower(ep.ID)
			entries = append(entries, Entry{
				URL:          "https://player.simplecast.com/" + id,
				ExtractorKey: "simplecast",
				ID:           id,
				Title:        ep.Title,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Simplecast podcast", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseSimplecastPodcastURL(parsed *url.URL) (string, bool) {
	if _, _, epOK := parseSimplecastEpisodeURL(parsed); epOK {
		return "", false
	}
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".simplecast.com") {
		return "", false
	}
	switch host {
	case "api.simplecast.com", "player.simplecast.com", "cdn.simplecast.com", "embed.simplecast.com", "feeds.simplecast.com":
		return "", false
	}
	path := strings.Trim(parsed.EscapedPath(), "/")
	return host, path == "" || strings.EqualFold(path, "episodes")
}

// Megaphone extracts player.megaphone.fm episode JSON.
type Megaphone struct{}

func NewMegaphone() Megaphone  { return Megaphone{} }
func (Megaphone) Name() string { return "megaphone" }

func (Megaphone) Suitable(parsed *url.URL) bool {
	_, ok := parseMegaphoneURL(parsed)
	return ok
}

func (Megaphone) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseMegaphoneURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://player.megaphone.fm/" + id
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Megaphone page too large", ErrInvalidMetadata)
	}
	match := megaphoneEpisode.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Megaphone episode JSON")
	}
	var episode struct {
		MediaURL string  `json:"mediaUrl"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(match[1], &episode); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Megaphone episode JSON", ErrInvalidMetadata)
	}
	mediaURL := episode.MediaURL
	if strings.HasPrefix(mediaURL, "//") {
		mediaURL = "https:" + mediaURL
	}
	title := id
	if m := ogAudioTitle.FindSubmatch(page); len(m) == 2 {
		title = string(m[1])
	}
	extra := []value.Field{}
	if m := ogAudioArtist.FindSubmatch(page); len(m) == 2 {
		extra = append(extra, value.Field{Key: "uploader", Value: value.String(string(m[1]))})
	}
	if m := ogImage.FindSubmatch(page); len(m) == 2 && strictValidHostedHTTPURL(string(m[1])) {
		extra = append(extra, value.Field{Key: "thumbnail", Value: value.String(string(m[1]))})
	}
	if episode.Duration > 0 {
		extra = append(extra, value.Field{Key: "duration", Value: value.Int(int64(episode.Duration))})
	}
	return podcastMediaInfo(id, title, canonical, mediaURL, extra...)
}

func parseMegaphoneURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "player.megaphone.fm" {
		return "", false
	}
	match := megaphonePath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Art19 extracts show episode metadata from the Art19 API.
type Art19 struct{}

func NewArt19() Art19      { return Art19{} }
func (Art19) Name() string { return "art19" }

func (Art19) Suitable(parsed *url.URL) bool {
	_, ok := parseArt19URL(parsed)
	return ok
}

func (Art19) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseArt19URL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://art19.com/episodes/" + id
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var payload struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		Content     struct {
			URL string `json:"url"`
		} `json:"content"`
		Series struct {
			Title string `json:"title"`
		} `json:"series"`
		Duration float64 `json:"duration"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &payload); err != nil {
		return Extraction{}, err
	}
	extra := []value.Field{}
	if payload.Series.Title != "" {
		extra = append(extra, value.Field{Key: "series", Value: value.String(payload.Series.Title)})
	}
	if payload.Description != "" {
		extra = append(extra, value.Field{Key: "description", Value: value.String(payload.Description)})
	}
	if payload.Duration > 0 {
		extra = append(extra, value.Field{Key: "duration", Value: value.Int(int64(payload.Duration))})
	}
	return podcastMediaInfo(firstNonEmpty(payload.ID, id), payload.Title, "https://rss.art19.com/episodes/"+id+".mp3", payload.Content.URL, extra...)
}

func parseArt19URL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "art19.com", "www.art19.com":
		match := art19EpisodePath.FindStringSubmatch(parsed.EscapedPath())
		if len(match) != 3 {
			return "", false
		}
		return strings.ToLower(match[2]), true
	case "rss.art19.com":
		match := art19RSSPath.FindStringSubmatch(parsed.EscapedPath())
		if len(match) != 2 {
			return "", false
		}
		return strings.ToLower(match[1]), true
	default:
		return "", false
	}
}

// Art19Show enumerates episodes for an Art19 show page.
type Art19Show struct{}

func NewArt19Show() Art19Show  { return Art19Show{} }
func (Art19Show) Name() string { return "art19_show" }

func (Art19Show) Suitable(parsed *url.URL) bool {
	_, ok := parseArt19ShowURL(parsed)
	return ok
}

func (Art19Show) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseArt19ShowURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://art19.com/shows/" + slug
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "title", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(endpoint)},
	))
	sequence, err := LazyFirstPageEntries(podcastMaxEpisodes, func(ctx context.Context) ([]Entry, error) {
		headers := make(http.Header)
		headers.Set("Accept", "application/json")
		var payload struct {
			ID       string `json:"id"`
			Title    string `json:"title"`
			Episodes []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"episodes"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(payload.Episodes) > podcastMaxEpisodes {
			return nil, fmt.Errorf("%w: Art19 show overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(payload.Episodes))
		for _, ep := range payload.Episodes {
			if !podcastUUID.MatchString(ep.ID) {
				continue
			}
			id := strings.ToLower(ep.ID)
			entries = append(entries, Entry{
				URL:          "https://rss.art19.com/episodes/" + id + ".mp3",
				ExtractorKey: "art19",
				ID:           id,
				Title:        ep.Title,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Art19 show", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseArt19ShowURL(parsed *url.URL) (string, bool) {
	if _, epOK := parseArt19URL(parsed); epOK {
		return "", false
	}
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "art19.com" && host != "www.art19.com" {
		return "", false
	}
	match := art19ShowPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Libsyn extracts html5-player.libsyn.com embed playlist items.
type Libsyn struct{}

func NewLibsyn() Libsyn     { return Libsyn{} }
func (Libsyn) Name() string { return "libsyn" }

func (Libsyn) Suitable(parsed *url.URL) bool {
	_, ok := parseLibsynURL(parsed)
	return ok
}

func (Libsyn) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseLibsynURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://html5-player.libsyn.com/embed/episode/id/" + id
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Libsyn page too large", ErrInvalidMetadata)
	}
	match := libsynPlaylist.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Libsyn playlistItem")
	}
	var item struct {
		ItemTitle      string `json:"item_title"`
		MediaURLLibsyn string `json:"media_url_libsyn"`
		MediaURL       string `json:"media_url"`
		DownloadLink   string `json:"download_link"`
	}
	if err := json.Unmarshal(match[1], &item); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Libsyn playlistItem", ErrInvalidMetadata)
	}
	mediaURL := firstNonEmpty(item.MediaURLLibsyn, item.MediaURL, item.DownloadLink)
	return podcastMediaInfo(id, item.ItemTitle, canonical, mediaURL)
}

func parseLibsynURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "html5-player.libsyn.com" {
		return "", false
	}
	match := libsynPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Spreaker extracts api/www.spreaker.com episodes.
type Spreaker struct{}

func NewSpreaker() Spreaker   { return Spreaker{} }
func (Spreaker) Name() string { return "spreaker" }

func (Spreaker) Suitable(parsed *url.URL) bool {
	_, ok := parseSpreakerURL(parsed)
	return ok
}

func (Spreaker) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseSpreakerURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://api.spreaker.com/v2/episodes/" + id
	var payload struct {
		Response struct {
			Episode struct {
				EpisodeID   hostingNumber `json:"episode_id"`
				Title       string        `json:"title"`
				Description string        `json:"description"`
				DownloadURL string        `json:"download_url"`
				Duration    hostingNumber `json:"duration"`
				Show        struct {
					Title string `json:"title"`
				} `json:"show"`
			} `json:"episode"`
		} `json:"response"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	ep := payload.Response.Episode
	extra := []value.Field{}
	if ep.Show.Title != "" {
		extra = append(extra, value.Field{Key: "series", Value: value.String(ep.Show.Title)})
	}
	if ep.Description != "" {
		extra = append(extra, value.Field{Key: "description", Value: value.String(ep.Description)})
	}
	if d := ep.Duration.int64(); d > 0 {
		// Spreaker durations are milliseconds in some payloads.
		if d > 100000 {
			d = d / 1000
		}
		extra = append(extra, value.Field{Key: "duration", Value: value.Int(d)})
	}
	return podcastMediaInfo(firstNonEmpty(ep.EpisodeID.string(), id), ep.Title, "https://www.spreaker.com/episode/"+id, ep.DownloadURL, extra...)
}

func parseSpreakerURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "api.spreaker.com":
		match := spreakerEpisodePath.FindStringSubmatch(parsed.EscapedPath())
		if len(match) != 2 {
			return "", false
		}
		return match[1], true
	case "spreaker.com", "www.spreaker.com":
		match := spreakerWebEpisode.FindStringSubmatch(parsed.EscapedPath())
		if len(match) != 2 {
			return "", false
		}
		return match[1], true
	default:
		return "", false
	}
}

// SpreakerShow enumerates show episodes with a bounded first page.
type SpreakerShow struct{}

func NewSpreakerShow() SpreakerShow { return SpreakerShow{} }
func (SpreakerShow) Name() string   { return "spreaker_show" }

func (SpreakerShow) Suitable(parsed *url.URL) bool {
	_, ok := parseSpreakerShowURL(parsed)
	return ok
}

func (SpreakerShow) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseSpreakerShowURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(id)},
		value.Field{Key: "webpage_url", Value: value.String("https://api.spreaker.com/show/" + id)},
	))
	const pageSize = 100
	sequence, err := OnDemandEntries(pageSize, func(ctx context.Context, page int) ([]Entry, error) {
		if page < 0 || page >= defaultMaxPlaylistPages {
			return nil, fmt.Errorf("%w: Spreaker page out of bounds", ErrInvalidPlaylist)
		}
		endpoint := fmt.Sprintf("https://api.spreaker.com/show/%s/episodes?page=%d&max_per_page=%d", id, page+1, pageSize)
		var payload struct {
			Response struct {
				Items []struct {
					EpisodeID hostingNumber `json:"episode_id"`
					Title     string        `json:"title"`
				} `json:"items"`
				NextURL string `json:"next_url"`
			} `json:"response"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if payload.Response.NextURL != "" {
			next, err := url.Parse(payload.Response.NextURL)
			if err != nil || hostedRejectUnsafeURL(next) || strings.ToLower(next.Hostname()) != "api.spreaker.com" {
				return nil, fmt.Errorf("%w: hostile Spreaker continuation", ErrInvalidPlaylist)
			}
		}
		if len(payload.Response.Items) > pageSize {
			return nil, fmt.Errorf("%w: Spreaker show page overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(payload.Response.Items))
		seen := make(map[string]bool, len(payload.Response.Items))
		for _, item := range payload.Response.Items {
			epID := item.EpisodeID.string()
			if !podcastDigits.MatchString(epID) || seen[epID] {
				continue
			}
			seen[epID] = true
			entries = append(entries, Entry{
				URL:          "https://api.spreaker.com/v2/episodes/" + epID,
				ExtractorKey: "spreaker",
				ID:           epID,
				Title:        item.Title,
			})
		}
		if page == 0 && len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Spreaker show", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseSpreakerShowURL(parsed *url.URL) (string, bool) {
	if _, epOK := parseSpreakerURL(parsed); epOK {
		return "", false
	}
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	path := parsed.EscapedPath()
	switch host {
	case "api.spreaker.com":
		match := spreakerShowAPI.FindStringSubmatch(path)
		if len(match) != 2 {
			return "", false
		}
		return match[1], true
	case "spreaker.com", "www.spreaker.com":
		if match := spreakerShowWeb.FindStringSubmatch(path); len(match) == 2 {
			return match[1], true
		}
		if match := spreakerShowFeed.FindStringSubmatch(path); len(match) == 2 {
			return match[1], true
		}
		return "", false
	default:
		return "", false
	}
}
