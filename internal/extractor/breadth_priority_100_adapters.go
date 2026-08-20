package extractor

// Additional reference-backed exact-host / route adapters used to restore an
// honest ≥100 distinct URL-shape inventory after removing cardinality aliases
// and hostname mirrors from the breadth ledger.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	nowCanalBrightcoveAccount = "6108484330001"
	nowCanalBrightcovePlayer  = "chhIqzukMq"
	breadthAdapterMaxEntries  = 256
)

var (
	teachingChannelPath = regexp.MustCompile(`(?i)^/videos?/([A-Za-z0-9_-]{1,256})/?$`)
	teachingChannelMID  = regexp.MustCompile(`(?i)(?:data-mid=["']|id=["']jw-video-player-)([A-Za-z0-9]{8})`)
	nowCanalPath        = regexp.MustCompile(`(?i)^/(?:[\w-]+/)+detalhe/([\w-]{1,256})/?$`)
	nowCanalBrightcove  = regexp.MustCompile(`(?i)"brightcoveVideoId"\s*:\s*"([0-9]{1,32})"`)
	democracyNowPath    = regexp.MustCompile(`(?i)^/([^\?]{1,512})$`)
	democracyNowJSON    = regexp.MustCompile(`(?is)<script[^>]+type=["']text/json["'][^>]*>\s*(\{.*?\})\s*</script>`)
	buzzFeedPath        = regexp.MustCompile(`(?i)^/(?:[^/?#]+/)+([A-Za-z0-9_-]{1,256})/?$`)
	buzzFeedBucket      = regexp.MustCompile(`(?is)<div class="video-embed[^"]*"[^>]*rel:bf_bucket_data='([^']+)'`)
	mediaStreamPath     = regexp.MustCompile(`(?i)^/(embed|live-stream)/([A-Za-z0-9]{1,64})/?$`)
	mediaStreamOptions  = regexp.MustCompile(`(?is)window\.MDSTRM\.OPTIONS\s*=\s*(\{.*?\})\s*;`)
	winSportsPath       = regexp.MustCompile(`(?i)^/videos/([A-Za-z0-9_-]{1,256})/?$`)
	winSportsEmbed      = regexp.MustCompile(`(?i)https://mdstrm\.com/(?:embed|live-stream)/([A-Za-z0-9]{1,64})`)
	abcOTVSPath         = regexp.MustCompile(`(?i)^/(?:(?:[^/]+/)*)?(?:[A-Za-z0-9_-]+/)?([0-9]{1,16})/?$`)
	abcOTVSClipsPath    = regexp.MustCompile(`(?i)^/(?:[^/]+/)*video/([0-9]{1,16})/?$`)
	vidsIoPath          = regexp.MustCompile(`(?i)^/videos/([0-9a-f]{6,128})/([A-Za-z0-9_-]{1,256})/?$`)
	vidsIoSprout        = regexp.MustCompile(`(?i)https://videos\.sproutvideo\.com/embed/([0-9a-f]{6,128})/([0-9a-f]{6,128})`)
	laracastsEpisode    = regexp.MustCompile(`(?i)^/series/([A-Za-z0-9_-]{1,128}/episodes/[0-9]{1,16})/?$`)
	laracastsSeries     = regexp.MustCompile(`(?i)^/series/([A-Za-z0-9_-]{1,128})/?$`)
	laracastsVimeoID    = regexp.MustCompile(`^[0-9]{1,16}$`)
	laracastsDataPage   = regexp.MustCompile(`(?is)id=["']app["'][^>]*(?:data-page="([^"]+)"|data-page='([^']+)')`)
	abcOTVSSites        = map[string]string{
		"6abc.com":        "wpvi",
		"abc11.com":       "wtvd",
		"abc13.com":       "ktrk",
		"abc30.com":       "kfsn",
		"abc7.com":        "kabc",
		"abc7chicago.com": "wls",
		"abc7news.com":    "kgo",
		"abc7ny.com":      "wabc",
	}
)

// TeachingChannel routes teachingchannel.org videos to JW Platform.
type TeachingChannel struct{}

func NewTeachingChannel() TeachingChannel { return TeachingChannel{} }
func (TeachingChannel) Name() string      { return "teachingchannel" }

func (TeachingChannel) Suitable(parsed *url.URL) bool {
	_, ok := parseTeachingChannelURL(parsed)
	return ok
}

func (TeachingChannel) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseTeachingChannelURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.teachingchannel.org/videos/" + displayID
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Teaching Channel page too large", ErrInvalidMetadata)
	}
	match := teachingChannelMID.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Teaching Channel JW media id")
	}
	return jwPlatformURLResult(string(match[1]), displayID)
}

func parseTeachingChannelURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "teachingchannel.org" && host != "www.teachingchannel.org" {
		return "", false
	}
	match := teachingChannelPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// NowCanal routes nowcanal.pt detalhe pages to Brightcove.
type NowCanal struct{}

func NewNowCanal() NowCanal   { return NowCanal{} }
func (NowCanal) Name() string { return "nowcanal" }

func (NowCanal) Suitable(parsed *url.URL) bool {
	_, ok := parseNowCanalURL(parsed)
	return ok
}

func (NowCanal) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseNowCanalURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.nowcanal.pt" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: NowCanal page too large", ErrInvalidMetadata)
	}
	match := nowCanalBrightcove.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "NowCanal Brightcove id")
	}
	return brightcoveURLResult(nowCanalBrightcoveAccount, nowCanalBrightcovePlayer, string(match[1]))
}

func parseNowCanalURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "nowcanal.pt" && host != "www.nowcanal.pt" {
		return "", false
	}
	match := nowCanalPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// DemocracyNow extracts democracynow.org show/story pages from embedded JSON.
type DemocracyNow struct{}

func NewDemocracyNow() DemocracyNow { return DemocracyNow{} }
func (DemocracyNow) Name() string   { return "democracynow" }

func (DemocracyNow) Suitable(parsed *url.URL) bool {
	_, ok := parseDemocracyNowURL(parsed)
	return ok
}

func (DemocracyNow) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseDemocracyNowURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.democracynow.org/" + displayID
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Democracy Now page too large", ErrInvalidMetadata)
	}
	match := democracyNowJSON.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Democracy Now JSON")
	}
	var payload struct {
		Title        string `json:"title"`
		File         string `json:"file"`
		Audio        string `json:"audio"`
		Video        string `json:"video"`
		HighResVideo string `json:"high_res_video"`
	}
	if err := json.Unmarshal(match[1], &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Democracy Now JSON", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, 4)
	seen := map[string]bool{}
	for i, raw := range []string{payload.HighResVideo, payload.Video, payload.File, payload.Audio} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		resolved, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if !resolved.IsAbs() {
			base, _ := url.Parse(canonical)
			resolved = base.ResolveReference(resolved)
		}
		resolved.RawQuery = ""
		resolved.Fragment = ""
		mediaURL := resolved.String()
		if seen[mediaURL] {
			continue
		}
		format, ok := strictHostedURLFormat(fmt.Sprintf("media-%d", i), mediaURL)
		if !ok {
			continue
		}
		seen[mediaURL] = true
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing Democracy Now media", ErrInvalidMetadata)
	}
	title := strings.TrimSpace(payload.Title)
	if title == "" {
		title = displayID
	}
	return Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))), nil
}

func parseDemocracyNowURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "democracynow.org" && host != "www.democracynow.org" {
		return "", false
	}
	match := democracyNowPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || match[1] == "" || strings.Contains(match[1], "..") {
		return "", false
	}
	return strings.Trim(match[1], "/"), true
}

// BuzzFeed enumerates embedded video buckets as a lazy playlist of URL results.
type BuzzFeed struct{}

func NewBuzzFeed() BuzzFeed   { return BuzzFeed{} }
func (BuzzFeed) Name() string { return "buzzfeed" }

func (BuzzFeed) Suitable(parsed *url.URL) bool {
	_, ok := parseBuzzFeedURL(parsed)
	return ok
}

func (BuzzFeed) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseBuzzFeedURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.buzzfeed.com" + parsed.EscapedPath()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(id)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(breadthAdapterMaxEntries, func(ctx context.Context) ([]Entry, error) {
		page, _, err := request.Transport.ReadPage(ctx, canonical)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(page)) > maxExtractorJSONBytes {
			return nil, fmt.Errorf("%w: BuzzFeed page too large", ErrInvalidMetadata)
		}
		matches := buzzFeedBucket.FindAllSubmatch(page, breadthAdapterMaxEntries)
		entries := make([]Entry, 0, len(matches))
		seen := map[string]bool{}
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			var bucket struct {
				Video *struct {
					URL string `json:"url"`
					ID  string `json:"id"`
				} `json:"video"`
				Progload *struct {
					URL string `json:"url"`
					ID  string `json:"id"`
				} `json:"progload_video"`
			}
			if err := json.Unmarshal(match[1], &bucket); err != nil {
				continue
			}
			video := bucket.Video
			if video == nil {
				video = bucket.Progload
			}
			if video == nil || !strictValidHostedHTTPURL(video.URL) || seen[video.URL] {
				continue
			}
			seen[video.URL] = true
			entryID := strings.TrimSpace(video.ID)
			if entryID == "" {
				entryID = id
			}
			// Only emit an explicit ExtractorKey when a registered backend can
			// hydrate the child. Facebook embeds are kept as bare URLs because
			// no Facebook extractor is registered (bounded downstream-hydration
			// deviation). YouTube URLs keep the youtube key for verified re-entry.
			key := ""
			if u, err := url.Parse(video.URL); err == nil {
				host := strings.ToLower(u.Hostname())
				switch {
				case host == "youtu.be", host == "youtube.com", host == "www.youtube.com",
					host == "m.youtube.com", host == "youtube-nocookie.com", host == "www.youtube-nocookie.com":
					key = "youtube"
				}
			}
			entries = append(entries, Entry{URL: video.URL, ExtractorKey: key, ID: entryID})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty BuzzFeed playlist", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseBuzzFeedURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "buzzfeed.com" && host != "www.buzzfeed.com" {
		return "", false
	}
	match := buzzFeedPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// MediaStream extracts mdstrm.com embed / live-stream players.
type MediaStream struct{}

func NewMediaStream() MediaStream { return MediaStream{} }
func (MediaStream) Name() string  { return "mediastream" }

func (MediaStream) Suitable(parsed *url.URL) bool {
	_, _, ok := parseMediaStreamURL(parsed)
	return ok
}

func (MediaStream) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	kind, id, ok := parseMediaStreamURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://mdstrm.com/" + kind + "/" + id
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: MediaStream page too large", ErrInvalidMetadata)
	}
	for _, msg := range []string{
		"Debido a tu ubicación no puedes ver el contenido",
		"You are not allowed to watch this video: Geo Fencing Restriction",
		"Este contenido no está disponible en tu zona geográfica.",
	} {
		if strings.Contains(string(page), msg) {
			return Extraction{}, ErrRegionRestricted
		}
	}
	match := mediaStreamOptions.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "MediaStream player options")
	}
	var options struct {
		Title string            `json:"title"`
		Type  string            `json:"type"`
		Src   map[string]string `json:"src"`
	}
	if err := json.Unmarshal(match[1], &options); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid MediaStream options", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(options.Src))
	keys := make([]string, 0, len(options.Src))
	for key := range options.Src {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		rawURL := options.Src[key]
		format, ok := strictHostedURLFormat(key, rawURL)
		if !ok {
			continue
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing MediaStream formats", ErrInvalidMetadata)
	}
	title := strings.TrimSpace(options.Title)
	if title == "" {
		title = id
	}
	fields := []value.Field{
		{Key: "id", Value: value.String(id)},
		{Key: "title", Value: value.String(title)},
		{Key: "webpage_url", Value: value.String(canonical)},
		{Key: "formats", Value: value.List(formats...)},
	}
	if strings.EqualFold(options.Type, "live") {
		fields = append(fields, value.Field{Key: "live_status", Value: value.String("is_live")})
	}
	return Media(value.NewInfo(value.NewObject(fields...))), nil
}

func parseMediaStreamURL(parsed *url.URL) (kind, id string, ok bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", "", false
	}
	if strings.ToLower(parsed.Hostname()) != "mdstrm.com" {
		return "", "", false
	}
	match := mediaStreamPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", "", false
	}
	return strings.ToLower(match[1]), match[2], true
}

// WinSports routes winsports.co videos to MediaStream embeds.
type WinSports struct{}

func NewWinSports() WinSports  { return WinSports{} }
func (WinSports) Name() string { return "winsports" }

func (WinSports) Suitable(parsed *url.URL) bool {
	_, ok := parseWinSportsURL(parsed)
	return ok
}

func (WinSports) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseWinSportsURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.winsports.co/videos/" + displayID
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: WinSports page too large", ErrInvalidMetadata)
	}
	match := winSportsEmbed.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "WinSports MediaStream embed")
	}
	kind := "embed"
	if strings.Contains(strings.ToLower(string(match[0])), "live-stream") {
		kind = "live-stream"
	}
	return URLResult(Entry{
		URL:          "https://mdstrm.com/" + kind + "/" + string(match[1]),
		ExtractorKey: "mediastream",
		ID:           string(match[1]),
		Transparent:  true,
	})
}

func parseWinSportsURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "winsports.co" && host != "www.winsports.co" {
		return "", false
	}
	match := winSportsPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// ABCOTVS extracts ABC Owned Television Station story pages via the OTV API.
type ABCOTVS struct{}

func NewABCOTVS() ABCOTVS    { return ABCOTVS{} }
func (ABCOTVS) Name() string { return "abcotvs" }

func (ABCOTVS) Suitable(parsed *url.URL) bool {
	_, _, ok := parseABCOTVSURL(parsed)
	return ok
}

func (ABCOTVS) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	station, videoID, ok := parseABCOTVSURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://api.abcotvs.com/v2/content?id=" + url.QueryEscape(videoID) +
		"&key=" + url.QueryEscape("otv.web."+station+".story") +
		"&station=" + url.QueryEscape(station)
	var payload struct {
		Data struct {
			FeaturedMedia *struct {
				Video *abcOTVSVideo `json:"video"`
			} `json:"featuredMedia"`
			abcOTVSVideo
		} `json:"data"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	video := payload.Data.FeaturedMedia
	var item abcOTVSVideo
	if video != nil && video.Video != nil {
		item = *video.Video
	} else {
		item = payload.Data.abcOTVSVideo
	}
	return abcOTVSMedia(item, videoID, request.URL)
}

type abcOTVSVideo struct {
	ID       hostingNumber `json:"id"`
	Title    string        `json:"title"`
	LinkText string        `json:"linkText"`
	M3U8     string        `json:"m3u8"`
	MP4      string        `json:"mp4"`
}

func abcOTVSMedia(item abcOTVSVideo, fallbackID, webpageURL string) (Extraction, error) {
	formats := make([]value.Value, 0, 2)
	if raw := strings.TrimSpace(strings.Split(item.M3U8, "?")[0]); raw != "" {
		if format, ok := strictHostedURLFormat("hls", raw); ok {
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if raw := strings.TrimSpace(item.MP4); raw != "" {
		if format, ok := strictHostedURLFormat("https", raw); ok {
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing ABCOTVS formats", ErrInvalidMetadata)
	}
	id := strings.TrimSpace(item.ID.string())
	if id == "" {
		id = fallbackID
	}
	title := strings.TrimSpace(firstNonEmpty(item.Title, item.LinkText, id))
	return Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))), nil
}

func parseABCOTVSURL(parsed *url.URL) (station, id string, ok bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	station, ok = abcOTVSSites[host]
	if !ok {
		return "", "", false
	}
	match := abcOTVSPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", "", false
	}
	return station, match[1], true
}

// ABCOTVSClips extracts clips.abcotvs.com videos.
type ABCOTVSClips struct{}

func NewABCOTVSClips() ABCOTVSClips { return ABCOTVSClips{} }
func (ABCOTVSClips) Name() string   { return "abcotvs_clips" }

func (ABCOTVSClips) Suitable(parsed *url.URL) bool {
	_, ok := parseABCOTVSClipsURL(parsed)
	return ok
}

func (ABCOTVSClips) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseABCOTVSClipsURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://clips.abcotvs.com/vogo/video/getByIds?ids=" + url.QueryEscape(id)
	var payload struct {
		Results []struct {
			Title    string `json:"title"`
			VideoURL string `json:"videoURL"`
		} `json:"results"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	if len(payload.Results) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing ABCOTVS clip", ErrInvalidMetadata)
	}
	raw := strings.TrimSpace(strings.Split(payload.Results[0].VideoURL, "?")[0])
	format, ok := strictHostedURLFormat("hls", raw)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid ABCOTVS clip media", ErrInvalidMetadata)
	}
	title := strings.TrimSpace(payload.Results[0].Title)
	if title == "" {
		title = id
	}
	return Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String("https://clips.abcotvs.com/video/" + id)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	))), nil
}

func parseABCOTVSClipsURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "clips.abcotvs.com" {
		return "", false
	}
	match := abcOTVSClipsPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// VidsIo routes *.vids.io account videos to SproutVideo embeds.
type VidsIo struct{}

func NewVidsIo() VidsIo     { return VidsIo{} }
func (VidsIo) Name() string { return "vidsio" }

func (VidsIo) Suitable(parsed *url.URL) bool {
	_, ok := parseVidsIoURL(parsed)
	return ok
}

func (VidsIo) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	id, ok := parseVidsIoURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://" + strings.ToLower(parsed.Hostname()) + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Vids.io page too large", ErrInvalidMetadata)
	}
	match := vidsIoSprout.FindSubmatch(page)
	if len(match) != 3 {
		return Extraction{}, classifyMissingMediaPage(page, "Vids.io SproutVideo embed")
	}
	return URLResult(Entry{
		URL:          "https://videos.sproutvideo.com/embed/" + string(match[1]) + "/" + string(match[2]),
		ExtractorKey: "sproutvideo",
		ID:           id,
		Transparent:  true,
	})
}

func parseVidsIoURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if !strings.HasSuffix(host, ".vids.io") || host == "vids.io" || strings.Count(host, ".") < 2 {
		return "", false
	}
	match := vidsIoPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", false
	}
	return match[1], true
}

// Laracasts routes episode pages to Vimeo player URLs.
type Laracasts struct{}

func NewLaracasts() Laracasts  { return Laracasts{} }
func (Laracasts) Name() string { return "laracasts" }

func (Laracasts) Suitable(parsed *url.URL) bool {
	_, ok := parseLaracastsEpisodeURL(parsed)
	return ok
}

func (Laracasts) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseLaracastsEpisodeURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	props, err := laracastsProps(ctx, request, displayID)
	if err != nil {
		return Extraction{}, err
	}
	lesson, _ := props["lesson"].(map[string]any)
	vimeoID, _ := lesson["vimeoId"].(string)
	if !laracastsVimeoID.MatchString(vimeoID) {
		if vimeoID == "" {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, fmt.Errorf("%w: invalid Laracasts Vimeo id", ErrInvalidMetadata)
	}
	return URLResult(Entry{
		URL:          "https://player.vimeo.com/video/" + vimeoID,
		ExtractorKey: "vimeo",
		ID:           vimeoID,
		Transparent:  true,
	})
}

func parseLaracastsEpisodeURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "laracasts.com" && host != "www.laracasts.com" {
		return "", false
	}
	match := laracastsEpisode.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// LaracastsSeries enumerates series episode Vimeo handoffs.
type LaracastsSeries struct{}

func NewLaracastsSeries() LaracastsSeries { return LaracastsSeries{} }
func (LaracastsSeries) Name() string      { return "laracasts_series" }

func (LaracastsSeries) Suitable(parsed *url.URL) bool {
	_, ok := parseLaracastsSeriesURL(parsed)
	return ok
}

func (LaracastsSeries) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseLaracastsSeriesURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://laracasts.com/series/" + displayID
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(displayID)},
		value.Field{Key: "title", Value: value.String(displayID)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(breadthAdapterMaxEntries, func(ctx context.Context) ([]Entry, error) {
		props, err := laracastsProps(ctx, request, displayID)
		if err != nil {
			return nil, err
		}
		series, _ := props["series"].(map[string]any)
		chapters, _ := series["chapters"].([]any)
		entries := make([]Entry, 0)
		seen := map[string]bool{}
		for _, chapter := range chapters {
			ch, _ := chapter.(map[string]any)
			episodes, _ := ch["episodes"].([]any)
			for _, ep := range episodes {
				lesson, _ := ep.(map[string]any)
				vimeoID, _ := lesson["vimeoId"].(string)
				if !laracastsVimeoID.MatchString(vimeoID) || seen[vimeoID] {
					continue
				}
				seen[vimeoID] = true
				title, _ := lesson["title"].(string)
				entries = append(entries, Entry{
					URL:          "https://player.vimeo.com/video/" + vimeoID,
					ExtractorKey: "vimeo",
					ID:           vimeoID,
					Title:        title,
					Transparent:  true,
				})
				if len(entries) >= breadthAdapterMaxEntries {
					break
				}
			}
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Laracasts series", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseLaracastsSeriesURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "laracasts.com" && host != "www.laracasts.com" {
		return "", false
	}
	if laracastsEpisode.MatchString(parsed.EscapedPath()) {
		return "", false
	}
	match := laracastsSeries.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func laracastsProps(ctx context.Context, request Request, displayID string) (map[string]any, error) {
	canonical := "https://laracasts.com/series/" + displayID
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: Laracasts page too large", ErrInvalidMetadata)
	}
	match := laracastsDataPage.FindSubmatch(page)
	if match == nil {
		return nil, classifyMissingMediaPage(page, "Laracasts data-page")
	}
	raw := match[1]
	if len(raw) == 0 {
		raw = match[2]
	}
	if len(raw) == 0 {
		return nil, classifyMissingMediaPage(page, "Laracasts data-page")
	}
	return laracastsDecodeDataPage(raw)
}

func laracastsDecodeDataPage(raw []byte) (map[string]any, error) {
	// Match upstream attribute decoding: HTML entities only. Do not use
	// url.QueryUnescape — that would turn literal '+' into spaces.
	decoded := html.UnescapeString(string(raw))
	if int64(len(decoded)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: Laracasts props too large", ErrInvalidMetadata)
	}
	var envelope struct {
		Props map[string]any `json:"props"`
	}
	if err := json.Unmarshal([]byte(decoded), &envelope); err != nil || envelope.Props == nil {
		return nil, fmt.Errorf("%w: invalid Laracasts props", ErrInvalidMetadata)
	}
	return envelope.Props, nil
}
