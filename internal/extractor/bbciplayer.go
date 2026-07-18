package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	bbcMediaSelectorBase = "https://open.live.bbc.co.uk/mediaselector/6/select/version/2.0/mediaset/"
	bbcPlaylistGraphQL   = "https://graph.ibl.api.bbc.co.uk/"
	bbcPlaylistPageSize  = 100
)

var (
	bbcPIDPattern    = regexp.MustCompile(`^[pbmlw][0-9a-z]{7,14}$`)
	bbcVpidPattern   = regexp.MustCompile(`(?i)["']vpid["']\s*:\s*["']([pbmlw][0-9a-z]{7,14})["']`)
	bbcOGTitle       = regexp.MustCompile(`(?is)<meta\b[^>]*(?:property|name)=["']og:title["'][^>]*content=["']([^"']+)["']`)
	bbcMetaDesc      = regexp.MustCompile(`(?is)<meta\b[^>]*name=["']description["'][^>]*content=["']([^"']*)["']`)
	bbcIPlayerTarget = regexp.MustCompile(`^/iplayer/(episode|episodes|group|playlist)/([pbmlw][0-9a-z]{7,14})(?:/[^/?#]*)?/?$`)
	bbcProgrammePath = regexp.MustCompile(`^/programmes/([pbmlw][0-9a-z]{7,14})(?:/player)?/?$`)
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
	if target.playlist {
		return extractBBCPlaylist(ctx, request.Transport, target.id, parsed.Query().Get("seriesId"), request.URL)
	}
	return extractBBCEpisode(ctx, request.Transport, target.id, request.URL)
}

type bbcTarget struct {
	id       string
	playlist bool
}

func classifyBBCIPlayerURL(parsed *url.URL) (bbcTarget, bool) {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil {
		return bbcTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "bbc.co.uk" && host != "www.bbc.co.uk" {
		return bbcTarget{}, false
	}
	if match := bbcIPlayerTarget.FindStringSubmatch(parsed.Path); len(match) == 3 {
		return bbcTarget{id: match[2], playlist: match[1] == "episodes" || match[1] == "group"}, true
	}
	if match := bbcProgrammePath.FindStringSubmatch(parsed.Path); len(match) == 2 {
		return bbcTarget{id: match[1]}, true
	}
	return bbcTarget{}, false
}

func extractBBCEpisode(ctx context.Context, transport Transport, pageID, webpageURL string) (Extraction, error) {
	page, _, err := transport.ReadPage(ctx, webpageURL)
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
	programmeID := pageID
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
		err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), "", &selection)
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
			formats = append(formats, value.ObjectValue(format))
		}
	}
	return formats, subtitles
}

func extractBBCPlaylist(ctx context.Context, transport Transport, pid, seriesID, webpageURL string) (Extraction, error) {
	metadata, err := requestBBCPlaylistPage(ctx, transport, pid, seriesID, 1, 1)
	if err != nil {
		return Extraction{}, err
	}
	title := metadata.Title.Default
	if title == "" {
		title = pid
	}
	sequence, err := OnDemandEntries(bbcPlaylistPageSize, func(ctx context.Context, page int) ([]Entry, error) {
		response, err := requestBBCPlaylistPage(ctx, transport, pid, seriesID, page+1, bbcPlaylistPageSize)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(response.Entities.Results))
		for _, result := range response.Entities.Results {
			episode := result.Episode
			if !bbcPIDPattern.MatchString(episode.ID) {
				continue
			}
			entries = append(entries, Entry{
				URL:          "https://www.bbc.co.uk/iplayer/episode/" + episode.ID,
				ExtractorKey: "bbciplayer", ID: episode.ID, Title: episode.Subtitle.Default,
			})
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(pid)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
	)
	riskString(info, "description", firstBBCSynopsis(metadata.Synopsis))
	return Playlist(value.NewInfo(info), sequence)
}

type bbcPlaylistResponse struct {
	Title struct {
		Default string `json:"default"`
	} `json:"title"`
	Synopsis map[string]string `json:"synopsis"`
	Entities struct {
		Results []struct {
			Episode struct {
				ID       string `json:"id"`
				Subtitle struct {
					Default string `json:"default"`
				} `json:"subtitle"`
			} `json:"episode"`
		} `json:"results"`
	} `json:"entities"`
}

func requestBBCPlaylistPage(ctx context.Context, transport Transport, pid, seriesID string, page, perPage int) (bbcPlaylistResponse, error) {
	variables := map[string]any{"id": pid, "page": page, "perPage": perPage}
	if seriesID != "" {
		variables["sliceId"] = seriesID
	}
	body, _ := json.Marshal(map[string]any{"id": "5692d93d5aac8d796a0305e895e61551", "variables": variables})
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var envelope struct {
		Data struct {
			Programme bbcPlaylistResponse `json:"programme"`
		} `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	err := requestRiskJSON(ctx, transport, http.MethodPost, bbcPlaylistGraphQL, body, headers, "", &envelope)
	if err != nil {
		switch riskHTTPStatus(err) {
		case http.StatusUnauthorized:
			return bbcPlaylistResponse{}, ErrAuthentication
		case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
			return bbcPlaylistResponse{}, ErrRegionRestricted
		case http.StatusNotFound, http.StatusGone:
			return bbcPlaylistResponse{}, ErrUnavailable
		}
		return bbcPlaylistResponse{}, err
	}
	if len(envelope.Errors) != 0 {
		return bbcPlaylistResponse{}, fmt.Errorf("%w: BBC playlist GraphQL error", ErrInvalidMetadata)
	}
	return envelope.Data.Programme, nil
}

func categorizeBBCPageError(err error) error {
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return err
	}
}

func bbcHTMLField(page []byte, pattern *regexp.Regexp) string {
	match := pattern.FindSubmatch(page)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(match[1])))
}

func mergeRiskSubtitles(target, source *value.Object) {
	for _, field := range source.Fields() {
		existing, _ := target.Lookup(field.Key).ListValue()
		incoming, _ := field.Value.ListValue()
		target.Set(field.Key, value.List(append(existing, incoming...)...))
	}
}

func firstBBCSynopsis(synopsis map[string]string) string {
	for _, key := range []string{"large", "medium", "small"} {
		if synopsis[key] != "" {
			return synopsis[key]
		}
	}
	return ""
}
