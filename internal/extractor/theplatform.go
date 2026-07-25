package extractor

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	thePlatformMaxSMILBytes   = 2 << 20
	thePlatformMaxFormats     = 128
	thePlatformMaxCaptions    = 64
	thePlatformMaxChapters    = 256
	thePlatformMaxFeedContent = 32
)

var (
	thePlatformProvider = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	thePlatformMediaID  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	thePlatformFeedID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
)

// ThePlatform extracts public link/player.theplatform.com media. Adobe Pass and
// signed-SMIL DRM success paths are intentionally unsupported.
type ThePlatform struct{}

func NewThePlatform() ThePlatform { return ThePlatform{} }
func (ThePlatform) Name() string  { return "theplatform" }

func (ThePlatform) Suitable(parsed *url.URL) bool {
	_, ok := parseThePlatformURL(parsed)
	return ok
}

func (ThePlatform) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseThePlatformURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if target.needsConfig {
		releasePath, err := thePlatformReleasePath(ctx, request.Transport, target)
		if err != nil {
			return Extraction{}, err
		}
		target.path = releasePath
	}
	formats, smilSubs, err := extractThePlatformSMIL(ctx, request.Transport, target)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	meta, err := downloadThePlatformMetadata(ctx, request.Transport, target.path, target.videoID)
	if err != nil {
		return Extraction{}, err
	}
	return normalizeThePlatform(target, formats, smilSubs, meta)
}

type thePlatformTarget struct {
	provider    string
	videoID     string
	path        string
	canonical   string
	needsConfig bool
}

func parseThePlatformURL(parsed *url.URL) (thePlatformTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return thePlatformTarget{}, false
	}
	if parsed.Scheme == "theplatform" {
		id := parsed.Opaque
		if !thePlatformMediaID.MatchString(id) {
			return thePlatformTarget{}, false
		}
		return thePlatformTarget{
			provider:  "dJ5BDC",
			videoID:   id,
			path:      "dJ5BDC/" + id,
			canonical: "theplatform:" + id,
		}, true
	}
	if hostedRejectUnsafeURL(parsed) {
		return thePlatformTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	switch host {
	case "link.theplatform.com":
		if len(segments) < 3 || segments[0] != "s" || !thePlatformProvider.MatchString(segments[1]) {
			return thePlatformTarget{}, false
		}
		provider := segments[1]
		rest := segments[2:]
		videoID := rest[len(rest)-1]
		videoID = strings.TrimSuffix(videoID, ".smil")
		videoID = strings.TrimSuffix(videoID, ".json")
		if !thePlatformMediaID.MatchString(videoID) {
			return thePlatformTarget{}, false
		}
		path := provider + "/" + strings.Join(rest, "/")
		path = strings.TrimSuffix(strings.TrimSuffix(path, ".smil"), ".json")
		return thePlatformTarget{
			provider: provider, videoID: videoID, path: path,
			canonical: "https://link.theplatform.com/s/" + path,
		}, true
	case "player.theplatform.com":
		if len(segments) < 2 || segments[0] != "p" || !thePlatformProvider.MatchString(segments[1]) {
			return thePlatformTarget{}, false
		}
		provider := segments[1]
		videoID := segments[len(segments)-1]
		if guid := parsed.Query().Get("guid"); guid != "" {
			if !thePlatformMediaID.MatchString(guid) {
				return thePlatformTarget{}, false
			}
			// Guid-based players require feed lookup; unsupported without feed id.
			return thePlatformTarget{}, false
		}
		if !thePlatformMediaID.MatchString(videoID) {
			return thePlatformTarget{}, false
		}
		needsConfig := false
		path := provider + "/" + videoID
		for i, segment := range segments {
			if segment == "media" && i+1 < len(segments) {
				path = provider + "/media/" + segments[i+1]
				videoID = segments[i+1]
				needsConfig = false
				return thePlatformTarget{
					provider: provider, videoID: videoID, path: path, needsConfig: false,
					canonical: "https://player.theplatform.com/p/" + provider + "/media/" + videoID,
				}, true
			}
		}
		for _, segment := range segments {
			if segment == "config" || segment == "swf" || segment == "onsite" || segment == "select" {
				needsConfig = true
				break
			}
		}
		return thePlatformTarget{
			provider: provider, videoID: videoID, path: path, needsConfig: needsConfig,
			canonical: "https://player.theplatform.com/p/" + provider + "/media/" + videoID,
		}, true
	default:
		return thePlatformTarget{}, false
	}
}

func thePlatformReleasePath(ctx context.Context, transport Transport, target thePlatformTarget) (string, error) {
	configURL := strings.Replace(target.canonical, "/swf/", "/config/", 1)
	configURL = strings.Replace(configURL, "/onsite/", "/onsite/config/", 1)
	if !strings.Contains(configURL, "form=json") {
		if strings.Contains(configURL, "?") {
			configURL += "&form=json"
		} else {
			configURL += "?form=json"
		}
	}
	var config struct {
		ReleaseURL string `json:"releaseUrl"`
	}
	if err := hostedRequestJSON(ctx, transport, http.MethodGet, configURL, nil, make(http.Header), &config); err != nil {
		return "", err
	}
	release := strings.TrimSpace(config.ReleaseURL)
	if release == "" {
		return target.path, nil
	}
	parsed, err := url.Parse(release)
	if err != nil || hostedRejectUnsafeURL(parsed) {
		return "", fmt.Errorf("%w: invalid ThePlatform release URL", ErrInvalidMetadata)
	}
	if strings.ToLower(parsed.Hostname()) != "link.theplatform.com" {
		return "", fmt.Errorf("%w: ThePlatform release host", ErrInvalidMetadata)
	}
	releaseTarget, ok := parseThePlatformURL(parsed)
	if !ok {
		return "", fmt.Errorf("%w: ThePlatform release path", ErrInvalidMetadata)
	}
	return releaseTarget.path, nil
}

func extractThePlatformSMIL(ctx context.Context, transport Transport, target thePlatformTarget) ([]value.Value, *value.Object, error) {
	smilURL := "https://link.theplatform.com/s/" + target.path + "?mbr=true&format=SMIL"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, smilURL, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: invalid ThePlatform SMIL request", ErrInvalidMetadata)
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, nil, err
	}
	defer response.Body.Close()
	reader := &io.LimitedReader{R: response.Body, N: thePlatformMaxSMILBytes + 1}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: read ThePlatform SMIL failed", ErrInvalidMetadata)
	}
	if int64(len(data)) > thePlatformMaxSMILBytes {
		return nil, nil, fmt.Errorf("%w: ThePlatform SMIL too large", ErrInvalidMetadata)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, hostedStatusError(response.StatusCode, data)
	}
	return parseThePlatformSMIL(data)
}

type thePlatformSMIL struct {
	XMLName xml.Name `xml:"smil"`
	Body    struct {
		Switch []struct {
			Video []struct {
				Src    string `xml:"src,attr"`
				Type   string `xml:"type,attr"`
				Width  string `xml:"width,attr"`
				Height string `xml:"height,attr"`
			} `xml:"video"`
			Audio []struct {
				Src string `xml:"src,attr"`
			} `xml:"audio"`
			TextStream []struct {
				Src  string `xml:"src,attr"`
				Lang string `xml:"lang,attr"`
				Type string `xml:"type,attr"`
			} `xml:"textstream"`
		} `xml:"switch"`
		Video []struct {
			Src    string `xml:"src,attr"`
			Width  string `xml:"width,attr"`
			Height string `xml:"height,attr"`
		} `xml:"video"`
		Ref []struct {
			Src      string `xml:"src,attr"`
			Abstract string `xml:"abstract,attr"`
			Params   []struct {
				Name  string `xml:"name,attr"`
				Value string `xml:"value,attr"`
			} `xml:"param"`
		} `xml:"ref"`
	} `xml:"body"`
}

func parseThePlatformSMIL(data []byte) ([]value.Value, *value.Object, error) {
	lower := bytes.ToLower(data)
	if bytes.Contains(lower, []byte("geolocationblocked")) {
		return nil, nil, ErrRegionRestricted
	}
	var doc thePlatformSMIL
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	if err := decoder.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("%w: invalid ThePlatform SMIL", ErrInvalidMetadata)
	}
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid ThePlatform SMIL", ErrInvalidMetadata)
		}
		if cd, ok := tok.(xml.CharData); ok && len(bytes.TrimSpace(cd)) == 0 {
			continue
		}
		return nil, nil, fmt.Errorf("%w: trailing ThePlatform SMIL", ErrInvalidMetadata)
	}
	for _, ref := range doc.Body.Ref {
		if strings.Contains(strings.ToLower(ref.Src), "errorfiles/unavailable") {
			return nil, nil, ErrUnavailable
		}
		for _, param := range ref.Params {
			if strings.EqualFold(param.Name, "exception") && strings.EqualFold(param.Value, "GeoLocationBlocked") {
				return nil, nil, ErrRegionRestricted
			}
		}
	}
	formats := make([]value.Value, 0, 8)
	index := 0
	appendFormat := func(formatID, rawURL string) error {
		if !strictValidHostedHTTPURL(rawURL) {
			return nil
		}
		if len(formats) >= thePlatformMaxFormats {
			return fmt.Errorf("%w: ThePlatform format limit", ErrInvalidMetadata)
		}
		format, ok := strictHostedURLFormat(formatID, rawURL)
		if ok {
			formats = append(formats, value.ObjectValue(format))
		}
		return nil
	}
	for _, sw := range doc.Body.Switch {
		for _, video := range sw.Video {
			formatID := fmt.Sprintf("http-%d", index)
			lowerURL := strings.ToLower(video.Src)
			if strings.Contains(lowerURL, ".m3u8") {
				formatID = fmt.Sprintf("hls-%d", index)
			}
			if err := appendFormat(formatID, video.Src); err != nil {
				return nil, nil, err
			}
			index++
		}
		for _, audio := range sw.Audio {
			if err := appendFormat(fmt.Sprintf("audio-%d", index), audio.Src); err != nil {
				return nil, nil, err
			}
			index++
		}
	}
	for _, video := range doc.Body.Video {
		if err := appendFormat(fmt.Sprintf("http-%d", index), video.Src); err != nil {
			return nil, nil, err
		}
		index++
	}
	if len(formats) == 0 {
		return nil, nil, ErrUnavailable
	}
	subs := value.NewObject()
	hasSubs := false
	captionCount := 0
	for _, sw := range doc.Body.Switch {
		for _, text := range sw.TextStream {
			if !strictValidHostedHTTPURL(text.Src) {
				continue
			}
			if captionCount >= thePlatformMaxCaptions {
				return nil, nil, fmt.Errorf("%w: ThePlatform caption limit", ErrInvalidMetadata)
			}
			lang := strings.TrimSpace(text.Lang)
			if lang == "" {
				lang = "en"
			}
			entry := value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(text.Src)}))
			existing, ok := subs.Lookup(lang).ListValue()
			if !ok {
				existing = nil
			}
			subs.Set(lang, value.List(append(existing, entry)...))
			hasSubs = true
			captionCount++
		}
	}
	if !hasSubs {
		return formats, nil, nil
	}
	return formats, subs, nil
}

type thePlatformMetadata struct {
	Title               string  `json:"title"`
	Description         string  `json:"description"`
	DefaultThumbnailURL string  `json:"defaultThumbnailUrl"`
	Duration            float64 `json:"duration"`
	PubDate             float64 `json:"pubDate"`
	BillingCode         string  `json:"billingCode"`
	Captions            []struct {
		Src  string `json:"src"`
		Lang string `json:"lang"`
		Type string `json:"type"`
	} `json:"captions"`
	Chapters []struct {
		StartTime float64 `json:"startTime"`
		EndTime   float64 `json:"endTime"`
	} `json:"chapters"`
}

func downloadThePlatformMetadata(ctx context.Context, transport Transport, path, videoID string) (thePlatformMetadata, error) {
	endpoint := "https://link.theplatform.com/s/" + path + "?format=preview"
	var meta thePlatformMetadata
	if err := hostedRequestJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), &meta); err != nil {
		return thePlatformMetadata{}, err
	}
	return meta, nil
}

func normalizeThePlatform(target thePlatformTarget, formats []value.Value, smilSubs *value.Object, meta thePlatformMetadata) (Extraction, error) {
	title := strings.TrimSpace(meta.Title)
	if title == "" {
		title = target.videoID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.videoID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(info, "description", meta.Description)
	hostedSetString(info, "uploader", meta.BillingCode)
	if strictValidHostedHTTPURL(meta.DefaultThumbnailURL) {
		hostedSetString(info, "thumbnail", meta.DefaultThumbnailURL)
	}
	if meta.Duration > 0 {
		hostedSetFloat(info, "duration", meta.Duration/1000)
	}
	if meta.PubDate > 0 {
		hostedSetInt(info, "timestamp", int64(meta.PubDate/1000))
	}
	subs := value.NewObject()
	hasSubs := false
	if smilSubs != nil {
		subs = smilSubs
		hasSubs = true
	}
	if len(meta.Captions) > thePlatformMaxCaptions {
		return Extraction{}, fmt.Errorf("%w: ThePlatform caption limit", ErrInvalidMetadata)
	}
	for _, caption := range meta.Captions {
		if !strictValidHostedHTTPURL(caption.Src) {
			continue
		}
		lang := strings.TrimSpace(caption.Lang)
		if lang == "" {
			lang = "en"
		}
		entry := value.NewObject(value.Field{Key: "url", Value: value.String(caption.Src)})
		existing, ok := subs.Lookup(lang).ListValue()
		if !ok {
			existing = nil
		}
		subs.Set(lang, value.List(append(existing, value.ObjectValue(entry))...))
		hasSubs = true
	}
	if hasSubs {
		info.Set("subtitles", value.ObjectValue(subs))
	}
	if len(meta.Chapters) > 1 && len(meta.Chapters) <= thePlatformMaxChapters {
		chapters := make([]value.Value, 0, len(meta.Chapters))
		for _, chapter := range meta.Chapters {
			obj := value.NewObject()
			if chapter.StartTime >= 0 {
				obj.Set("start_time", value.Float(chapter.StartTime/1000))
			}
			if chapter.EndTime > 0 {
				obj.Set("end_time", value.Float(chapter.EndTime/1000))
			}
			chapters = append(chapters, value.ObjectValue(obj))
		}
		info.Set("chapters", value.List(chapters...))
	}
	return Media(value.NewInfo(info)), nil
}

// ThePlatformFeed extracts byGuid/byId feed entries from feed.theplatform.com.
type ThePlatformFeed struct{}

func NewThePlatformFeed() ThePlatformFeed { return ThePlatformFeed{} }
func (ThePlatformFeed) Name() string      { return "theplatform_feed" }

func (ThePlatformFeed) Suitable(parsed *url.URL) bool {
	_, ok := parseThePlatformFeedURL(parsed)
	return ok
}

func (ThePlatformFeed) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseThePlatformFeedURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	endpoint := fmt.Sprintf("https://feed.theplatform.com/f/%s/%s?form=json&%s",
		url.PathEscape(target.provider), url.PathEscape(target.feedID), target.filter)
	var feed struct {
		Entries []struct {
			Title       string  `json:"title"`
			Description string  `json:"description"`
			GUID        string  `json:"guid"`
			PublicURL   string  `json:"plmedia$publicUrl"`
			Duration    float64 `json:"plfile$duration"`
			Available   float64 `json:"media$availableDate"`
			Content     []struct {
				URL    string   `json:"plfile$url"`
				Format string   `json:"plfile$format"`
				Types  []string `json:"plfile$assetTypes"`
			} `json:"media$content"`
			Thumbnails []struct {
				URL string `json:"plfile$url"`
			} `json:"media$thumbnails"`
		} `json:"entries"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &feed); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if len(feed.Entries) == 0 {
		return Extraction{}, ErrUnavailable
	}
	entry := feed.Entries[0]
	videoID := target.videoID
	if entry.GUID != "" {
		videoID = entry.GUID
	}
	formats := make([]value.Value, 0, 4)
	appendFormats := func(extra ...value.Value) error {
		if len(formats)+len(extra) > thePlatformMaxFormats {
			return fmt.Errorf("%w: ThePlatform feed format limit", ErrInvalidMetadata)
		}
		formats = append(formats, extra...)
		return nil
	}
	if len(entry.Content) > thePlatformMaxFeedContent {
		return Extraction{}, fmt.Errorf("%w: ThePlatform feed content limit", ErrInvalidMetadata)
	}
	for index, content := range entry.Content {
		rawURL := strings.TrimSpace(content.URL)
		if !strictValidHostedHTTPURL(rawURL) {
			continue
		}
		if thePlatformLooksLikeSMILURL(rawURL) {
			parsedSMIL, err := url.Parse(rawURL)
			if err != nil {
				return Extraction{}, fmt.Errorf("%w: invalid ThePlatform feed SMIL URL", ErrInvalidMetadata)
			}
			smilTarget, ok := parseThePlatformURL(parsedSMIL)
			if !ok {
				return Extraction{}, fmt.Errorf("%w: unsupported ThePlatform feed SMIL URL", ErrInvalidMetadata)
			}
			smilFormats, _, err := extractThePlatformSMIL(ctx, request.Transport, smilTarget)
			if err != nil {
				return Extraction{}, err
			}
			if err := appendFormats(smilFormats...); err != nil {
				return Extraction{}, err
			}
			continue
		}
		formatID := fmt.Sprintf("content-%d", index)
		if strings.Contains(strings.ToLower(rawURL), ".m3u8") {
			formatID = fmt.Sprintf("hls-%d", index)
		}
		if format, ok := strictHostedURLFormat(formatID, rawURL); ok {
			if err := appendFormats(value.ObjectValue(format)); err != nil {
				return Extraction{}, err
			}
		}
	}
	if len(formats) == 0 && strictValidHostedHTTPURL(entry.PublicURL) && !thePlatformLooksLikeSMILURL(entry.PublicURL) {
		if format, ok := strictHostedURLFormat("public", entry.PublicURL); ok {
			if err := appendFormats(value.ObjectValue(format)); err != nil {
				return Extraction{}, err
			}
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	title := strings.TrimSpace(entry.Title)
	if title == "" {
		title = videoID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(info, "description", entry.Description)
	if entry.Duration > 0 {
		hostedSetFloat(info, "duration", entry.Duration)
	}
	if entry.Available > 0 {
		hostedSetInt(info, "timestamp", int64(entry.Available/1000))
	}
	if len(entry.Thumbnails) > 0 && strictValidHostedHTTPURL(entry.Thumbnails[0].URL) {
		hostedSetString(info, "thumbnail", entry.Thumbnails[0].URL)
	}
	return Media(value.NewInfo(info)), nil
}

type thePlatformFeedTarget struct {
	provider, feedID, videoID, filter, canonical string
}

func parseThePlatformFeedURL(parsed *url.URL) (thePlatformFeedTarget, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return thePlatformFeedTarget{}, false
	}
	if strings.ToLower(parsed.Hostname()) != "feed.theplatform.com" {
		return thePlatformFeedTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) != 3 || segments[0] != "f" || !thePlatformProvider.MatchString(segments[1]) || !thePlatformFeedID.MatchString(segments[2]) {
		return thePlatformFeedTarget{}, false
	}
	query := parsed.Query()
	videoID := query.Get("byGuid")
	filter := "byGuid=" + url.QueryEscape(videoID)
	if videoID == "" {
		videoID = query.Get("byId")
		filter = "byId=" + url.QueryEscape(videoID)
	}
	if !thePlatformMediaID.MatchString(videoID) {
		return thePlatformFeedTarget{}, false
	}
	return thePlatformFeedTarget{
		provider: segments[1], feedID: segments[2], videoID: videoID, filter: filter,
		canonical: "https://feed.theplatform.com/f/" + segments[1] + "/" + segments[2] + "?" + filter,
	}, true
}

func thePlatformLooksLikeSMILURL(rawURL string) bool {
	lower := strings.ToLower(rawURL)
	if strings.Contains(lower, ".smil") || strings.Contains(lower, "format=smil") {
		return true
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || hostedRejectUnsafeURL(parsed) {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != "link.theplatform.com" {
		return false
	}
	ext := strings.ToLower(path.Ext(parsed.Path))
	return ext == "" || ext == ".smil"
}
