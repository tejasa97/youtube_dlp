package extractor

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	arcMaxStreams   = 64
	arcMaxSubtitles = 64
	arcMaxOrgs      = 64
)

var (
	arcUUIDPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	arcOrgPattern  = regexp.MustCompile(`^[a-z]{1,32}$`)
)

// ArcPublishing implements the POWA/ANS video API used by Arc-backed newsrooms.
// Only the opaque arcpublishing:org:uuid form is claimed by this backend;
// site adapters own exact HTTP hosts and hand off through URLResult.
type ArcPublishing struct{}

func NewArcPublishing() ArcPublishing { return ArcPublishing{} }
func (ArcPublishing) Name() string    { return "arcpublishing" }

func (ArcPublishing) Suitable(parsed *url.URL) bool {
	_, ok := parseArcPublishingURL(parsed)
	return ok
}

func (ArcPublishing) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseArcPublishingURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	endpoint := arcAPIEndpoint(target.org) + "?uuid=" + url.QueryEscape(target.uuid)
	var videos []arcVideo
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &videos); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if len(videos) == 0 {
		return Extraction{}, ErrUnavailable
	}
	return normalizeArcPublishing(videos[0], target)
}

type arcTarget struct {
	org, uuid, canonical string
}

func parseArcPublishingURL(parsed *url.URL) (arcTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return arcTarget{}, false
	}
	if parsed.Scheme != "arcpublishing" || parsed.Opaque == "" || parsed.Host != "" || parsed.User != nil {
		return arcTarget{}, false
	}
	parts := strings.Split(parsed.Opaque, ":")
	if len(parts) != 2 || !arcOrgPattern.MatchString(parts[0]) || !arcUUIDPattern.MatchString(parts[1]) {
		return arcTarget{}, false
	}
	org := parts[0]
	uuid := strings.ToLower(parts[1])
	return arcTarget{org: org, uuid: uuid, canonical: "arcpublishing:" + org + ":" + uuid}, true
}

func arcAPIEndpoint(org string) string {
	apiOrg := org
	if org == "wapo" {
		apiOrg = "washpost"
	}
	switch org {
	case "cmg", "prisa":
		return "https://" + apiOrg + "-config-prod.api.cdn.arcpublishing.com/video/v1/ansvideos/findByUuid"
	case "adn", "advancelocal", "answers", "bonnier", "bostonglobe", "demo", "gmg", "gruponacion", "infobae", "mco", "nzme", "pmn", "raycom", "spectator", "tbt", "tgam", "tronc", "wapo", "wweek":
		return "https://video-api-cdn." + apiOrg + ".arcpublishing.com/api/v1/ansvideos/findByUuid"
	case "gray":
		return "https://gray-prod-cdn.video-api.arcpublishing.com/api/v1/ansvideos/findByUuid"
	default:
		return "https://" + apiOrg + "-prod-cdn.video-api.arcpublishing.com/api/v1/ansvideos/findByUuid"
	}
}

type arcVideo struct {
	Headlines struct {
		Basic string `json:"basic"`
	} `json:"headlines"`
	Subheadlines struct {
		Basic string `json:"basic"`
	} `json:"subheadlines"`
	PromoImage *struct {
		URL string `json:"url"`
	} `json:"promo_image"`
	Duration    hostingNumber `json:"duration"`
	CreatedDate string        `json:"created_date"`
	Status      string        `json:"status"`
	Streams     []arcStream   `json:"streams"`
	Subtitles   *arcSubtitles `json:"subtitles"`
}

type arcStream struct {
	URL        string        `json:"url"`
	StreamType string        `json:"stream_type"`
	Bitrate    hostingNumber `json:"bitrate"`
	Width      hostingNumber `json:"width"`
	Height     hostingNumber `json:"height"`
	Filesize   hostingNumber `json:"filesize"`
}

type arcSubtitles struct {
	URLs []struct {
		URL string `json:"url"`
	} `json:"urls"`
}

func normalizeArcPublishing(video arcVideo, target arcTarget) (Extraction, error) {
	title := strings.TrimSpace(video.Headlines.Basic)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Arc Publishing title", ErrInvalidMetadata)
	}
	if len(video.Streams) > arcMaxStreams {
		return Extraction{}, fmt.Errorf("%w: Arc Publishing stream limit", ErrInvalidMetadata)
	}
	seen := make(map[string]struct{}, len(video.Streams))
	formats := make([]value.Value, 0, len(video.Streams))
	for index, stream := range video.Streams {
		rawURL := strings.TrimSpace(stream.URL)
		if rawURL == "" {
			continue
		}
		if !validHostedHTTPURL(rawURL) {
			continue
		}
		if _, exists := seen[rawURL]; exists {
			continue
		}
		seen[rawURL] = struct{}{}
		streamType := strings.ToLower(strings.TrimSpace(stream.StreamType))
		switch streamType {
		case "smil":
			// Legacy SMIL/RTMP delivery is intentionally unsupported.
			continue
		case "ts", "hls":
			format, ok := hostedURLFormat(fmt.Sprintf("hls-%d", index), rawURL)
			if !ok {
				continue
			}
			hostedSetInt(format, "width", stream.Width.int64())
			hostedSetInt(format, "height", stream.Height.int64())
			formats = append(formats, value.ObjectValue(format))
		default:
			formatID := streamType
			if vbr := stream.Bitrate.int64(); vbr > 0 {
				if formatID != "" {
					formatID += "-"
				}
				formatID += fmt.Sprintf("%d", vbr)
			}
			if formatID == "" {
				formatID = fmt.Sprintf("http-%d", index)
			}
			format, ok := hostedURLFormat(formatID, rawURL)
			if !ok {
				continue
			}
			hostedSetInt(format, "width", stream.Width.int64())
			hostedSetInt(format, "height", stream.Height.int64())
			hostedSetInt(format, "filesize", stream.Filesize.int64())
			if vbr := stream.Bitrate.int64(); vbr > 0 {
				format.Set("vbr", value.Int(vbr))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.uuid)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(info, "description", video.Subheadlines.Basic)
	if video.PromoImage != nil && validHostedHTTPURL(video.PromoImage.URL) {
		hostedSetString(info, "thumbnail", video.PromoImage.URL)
	}
	if duration := video.Duration.int64(); duration > 0 {
		// Arc durations are centiseconds in the reference.
		hostedSetInt(info, "duration", duration/100)
	}
	if timestamp := hostedUnixTimestamp(video.CreatedDate); timestamp > 0 {
		hostedSetInt(info, "timestamp", timestamp)
	}
	if strings.EqualFold(video.Status, "live") {
		info.Set("is_live", value.Bool(true))
		info.Set("live_status", value.String("is_live"))
	}
	if video.Subtitles != nil {
		subs := make([]value.Value, 0, len(video.Subtitles.URLs))
		for index, sub := range video.Subtitles.URLs {
			if index >= arcMaxSubtitles {
				return Extraction{}, fmt.Errorf("%w: Arc Publishing subtitle limit", ErrInvalidMetadata)
			}
			if !validHostedHTTPURL(sub.URL) {
				continue
			}
			subs = append(subs, value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String(sub.URL)},
			)))
		}
		if len(subs) > 0 {
			info.Set("subtitles", value.ObjectValue(value.NewObject(
				value.Field{Key: "en", Value: value.List(subs...)},
			)))
		}
	}
	return Media(value.NewInfo(info)), nil
}

// arcPowaPattern finds POWA embed divs with both data-org and data-uuid.
var arcPowaPattern = regexp.MustCompile(`(?is)<div\b[^>]*\bclass\s*=\s*["'][^"']*\bpowa\b[^"']*["'][^>]*>`)

var (
	arcPowaOrgAttr  = regexp.MustCompile(`(?i)\bdata-org\s*=\s*["']([a-z]{1,32})["']`)
	arcPowaUUIDAttr = regexp.MustCompile(`(?i)\bdata-uuid\s*=\s*["']([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})["']`)
)

func extractArcPowaEntries(page []byte, expectedOrg string) ([]Entry, error) {
	if int64(len(page)) > maxExtractorJSONBytes {
		return nil, fmt.Errorf("%w: Arc page too large", ErrInvalidMetadata)
	}
	matches := arcPowaPattern.FindAll(page, arcMaxOrgs+1)
	if len(matches) > arcMaxOrgs {
		return nil, fmt.Errorf("%w: Arc POWA embed limit", ErrInvalidMetadata)
	}
	seen := make(map[string]struct{}, len(matches))
	entries := make([]Entry, 0, len(matches))
	for _, match := range matches {
		orgMatch := arcPowaOrgAttr.FindSubmatch(match)
		uuidMatch := arcPowaUUIDAttr.FindSubmatch(match)
		if len(orgMatch) != 2 || len(uuidMatch) != 2 {
			continue
		}
		org := string(orgMatch[1])
		uuid := strings.ToLower(string(uuidMatch[1]))
		if expectedOrg != "" && org != expectedOrg {
			continue
		}
		if !arcOrgPattern.MatchString(org) || !arcUUIDPattern.MatchString(uuid) {
			continue
		}
		key := org + ":" + uuid
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, Entry{
			URL:          "arcpublishing:" + org + ":" + uuid,
			ExtractorKey: "arcpublishing",
			ID:           uuid,
		})
	}
	return entries, nil
}
