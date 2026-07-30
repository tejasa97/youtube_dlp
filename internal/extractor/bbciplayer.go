package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type BBCIPlayer struct{}

func NewBBCIPlayer() BBCIPlayer { return BBCIPlayer{} }

func (BBCIPlayer) Name() string { return "bbciplayer" }

func (BBCIPlayer) Suitable(parsed *url.URL) bool {
	_, ok := classifyBBCIPlayerURL(parsed)
	return ok
}

func (BBCIPlayer) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyBBCIPlayerURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return extractBBCEpisode(ctx, request.Transport, target, request.URL)
}

type bbcEpisodeTarget struct {
	id string
}

func classifyBBCIPlayerURL(parsed *url.URL) (bbcEpisodeTarget, bool) {
	if !bbcValidHost(parsed) {
		return bbcEpisodeTarget{}, false
	}
	if match := bbcEpisodePath.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return bbcEpisodeTarget{id: match[1]}, true
	}
	if match := bbcProgrammePath.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return bbcEpisodeTarget{id: match[1]}, true
	}
	return bbcEpisodeTarget{}, false
}

func extractBBCEpisode(ctx context.Context, transport Transport, target bbcEpisodeTarget, webpageURL string) (Extraction, error) {
	page, err := requestBBCIsolatedPage(ctx, transport, webpageURL)
	if err != nil {
		return Extraction{}, categorizeBBCPageError(err)
	}
	if int64(len(page)) > riskExtractorMaxJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	lower := strings.ToLower(string(page))
	switch {
	case strings.Contains(lower, "sign in to watch"), strings.Contains(lower, "you need to sign in"), strings.Contains(lower, "/signin"):
		return Extraction{}, ErrAuthentication
	case strings.Contains(lower, "not available in your location"), strings.Contains(lower, "only available in the uk"):
		return Extraction{}, ErrRegionRestricted
	case strings.Contains(lower, "no longer available"), strings.Contains(lower, "not currently available"):
		return Extraction{}, ErrUnavailable
	}
	programmeID := target.id
	if match := bbcVpidPattern.FindSubmatch(page); len(match) == 2 {
		programmeID = string(match[1])
	}
	formats, subtitles, err := fetchBBCMediaSelector(ctx, transport, programmeID)
	if err != nil {
		return Extraction{}, err
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	title := bbcHTMLField(page, bbcOGTitle)
	if title == "" {
		title = "BBC programme " + programmeID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(programmeID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	riskString(info, "description", bbcHTMLField(page, bbcMetaDesc))
	if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func fetchBBCMediaSelector(ctx context.Context, transport Transport, programmeID string) ([]value.Value, *value.Object, error) {
	if !bbcPIDPattern.MatchString(programmeID) {
		return nil, value.NewObject(), fmt.Errorf("%w: invalid BBC programme id", ErrInvalidMetadata)
	}
	var last error
	formats := make([]value.Value, 0)
	subtitles := value.NewObject()
	seen := make(map[string]bool)
	for _, mediaSet := range []string{"iptv-all", "pc"} {
		endpoint := bbcMediaSelectorBase + mediaSet + "/vpid/" + programmeID
		var selection bbcMediaSelection
		err := requestBBCIsolatedJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), &selection)
		if err != nil {
			switch riskHTTPStatus(err) {
			case http.StatusUnauthorized:
				return nil, subtitles, ErrAuthentication
			case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
				last = ErrRegionRestricted
				continue
			case http.StatusNotFound, http.StatusGone:
				last = ErrUnavailable
				continue
			default:
				return nil, subtitles, err
			}
		}
		if selection.Result != "" {
			switch strings.ToLower(selection.Result) {
			case "notukerror", "geolocation":
				last = ErrRegionRestricted
			case "selectionunavailable", "notavailable", "noitems":
				last = ErrUnavailable
			case "authenticationrequired", "loginrequired":
				return nil, subtitles, ErrAuthentication
			default:
				return nil, subtitles, fmt.Errorf("%w: BBC media selector result", ErrUnavailable)
			}
			continue
		}
		parsedFormats, parsedSubtitles := normalizeBBCMediaSelection(selection, seen)
		formats = append(formats, parsedFormats...)
		mergeRiskSubtitles(subtitles, parsedSubtitles)
	}
	if len(formats) == 0 && last != nil {
		return nil, subtitles, last
	}
	return formats, subtitles, nil
}

type bbcMediaSelection struct {
	Result string `json:"result"`
	Media  []struct {
		Kind          string          `json:"kind"`
		Bitrate       json.RawMessage `json:"bitrate"`
		Encoding      string          `json:"encoding"`
		Width         int64           `json:"width"`
		Height        int64           `json:"height"`
		MediaFileSize int64           `json:"media_file_size"`
		Connection    []struct {
			Href           string `json:"href"`
			Kind           string `json:"kind"`
			Protocol       string `json:"protocol"`
			Supplier       string `json:"supplier"`
			TransferFormat string `json:"transferFormat"`
		} `json:"connection"`
	} `json:"media"`
}

func normalizeBBCMediaSelection(selection bbcMediaSelection, seen map[string]bool) ([]value.Value, *value.Object) {
	formats := make([]value.Value, 0)
	subtitles := value.NewObject()
	for _, media := range selection.Media {
		if media.Kind == "captions" {
			for _, connection := range media.Connection {
				if !validHTTPURL(connection.Href) {
					continue
				}
				entry := value.NewObject(
					value.Field{Key: "url", Value: value.String(connection.Href)},
					value.Field{Key: "ext", Value: value.String("ttml")},
				)
				bbcMarkCredentialIsolated(entry)
				subtitles.Set("en", value.List(value.ObjectValue(entry)))
				break
			}
			continue
		}
		if media.Kind != "video" && media.Kind != "audio" {
			continue
		}
		for _, connection := range media.Connection {
			if seen[connection.Href] || !validHTTPURL(connection.Href) {
				continue
			}
			formatID := connection.Supplier
			if formatID == "" {
				formatID = connection.Kind
			}
			if formatID == "" {
				formatID = connection.Protocol
			}
			format, ok := riskFormat(connection.Href, formatID)
			if !ok {
				continue
			}
			seen[connection.Href] = true
			switch strings.ToLower(connection.TransferFormat) {
			case "hls":
				format.Set("protocol", value.String("m3u8_native"))
			case "dash":
				format.Set("protocol", value.String("http_dash_segments"))
			}
			if media.Kind == "audio" {
				format.Set("vcodec", value.String("none"))
				riskPositiveInt(format, "abr", riskFlexibleInt(media.Bitrate))
				riskString(format, "acodec", media.Encoding)
			} else {
				riskPositiveInt(format, "tbr", riskFlexibleInt(media.Bitrate))
				riskPositiveInt(format, "width", media.Width)
				riskPositiveInt(format, "height", media.Height)
				riskString(format, "vcodec", media.Encoding)
			}
			riskPositiveInt(format, "filesize", media.MediaFileSize)
			bbcMarkCredentialIsolated(format)
			formats = append(formats, value.ObjectValue(format))
		}
	}
	return formats, subtitles
}

func mergeRiskSubtitles(target, source *value.Object) {
	for _, field := range source.Fields() {
		existing, _ := target.Lookup(field.Key).ListValue()
		incoming, _ := field.Value.ListValue()
		target.Set(field.Key, value.List(append(existing, incoming...)...))
	}
}
