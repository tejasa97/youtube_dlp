package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	ardPageGatewayBase  = "https://api.ardmediathek.de/page-gateway/"
	ardPlaylistPageSize = 100
)

var ardIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,512}$`)

type ARD struct{}

func NewARD() ARD { return ARD{} }

func (ARD) Name() string { return "ard" }

func (ARD) Suitable(parsed *url.URL) bool {
	_, ok := classifyARDURL(parsed)
	return ok
}

func (ARD) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyARDURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if target.playlist {
		return extractARDPlaylist(ctx, request.Transport, target, request.URL)
	}
	return extractARDItem(ctx, request.Transport, target.id, request.URL)
}

type ardTarget struct {
	id        string
	displayID string
	kind      string
	season    string
	version   string
	playlist  bool
}

func classifyARDURL(parsed *url.URL) (ardTarget, bool) {
	if parsed == nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil {
		return ardTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "ardmediathek.de" && host != "www.ardmediathek.de" && host != "beta.ardmediathek.de" {
		return ardTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	marker := -1
	for index, part := range parts {
		switch part {
		case "video", "player", "live", "sendung", "serie", "sammlung":
			marker = index
		}
		if marker >= 0 {
			break
		}
	}
	if marker < 0 || marker == len(parts)-1 {
		return ardTarget{}, false
	}
	kind := parts[marker]
	remaining := parts[marker+1:]
	playlist := kind == "sendung" || kind == "serie" || kind == "sammlung"
	idIndex := len(remaining) - 1
	season, version := "", ""
	if playlist && len(remaining) >= 2 {
		last := remaining[len(remaining)-1]
		if last == "OV" || last == "AD" {
			version = last
			idIndex--
		}
		if idIndex > 0 {
			if _, err := strconv.Atoi(remaining[idIndex]); err == nil {
				season = remaining[idIndex]
				idIndex--
			}
		}
	}
	if idIndex < 0 || !ardIDPattern.MatchString(remaining[idIndex]) {
		return ardTarget{}, false
	}
	displayID := strings.Join(remaining[:idIndex], "/")
	if displayID == "" {
		displayID = remaining[idIndex]
	}
	return ardTarget{id: remaining[idIndex], displayID: displayID, kind: kind, season: season, version: version, playlist: playlist}, true
}

type ardPageData struct {
	Title        string `json:"title"`
	Synopsis     string `json:"synopsis"`
	FSKRating    string `json:"fskRating"`
	GeoBlocked   bool   `json:"geoBlocked"`
	Availability string `json:"availability"`
	Tracking     struct {
		ATICustomVars struct {
			ContentID json.RawMessage `json:"contentId"`
		} `json:"atiCustomVars"`
	} `json:"tracking"`
	Widgets []struct {
		Type            string `json:"type"`
		BlockedByFSK    bool   `json:"blockedByFsk"`
		MediaCollection struct {
			Embedded ardMediaCollection `json:"embedded"`
		} `json:"mediaCollection"`
	} `json:"widgets"`
}

type ardMediaCollection struct {
	Streams []struct {
		Kind  string `json:"kind"`
		Media []struct {
			URL              string `json:"url"`
			ForcedLabel      string `json:"forcedLabel"`
			MaxHResolutionPx int64  `json:"maxHResolutionPx"`
			MaxVResolutionPx int64  `json:"maxVResolutionPx"`
			VideoCodec       string `json:"videoCodec"`
			Audios           []struct {
				Kind         string `json:"kind"`
				LanguageCode string `json:"languageCode"`
			} `json:"audios"`
		} `json:"media"`
	} `json:"streams"`
	Subtitles []struct {
		LanguageCode string `json:"languageCode"`
		Sources      []struct {
			URL  string `json:"url"`
			Kind string `json:"kind"`
		} `json:"sources"`
	} `json:"subtitles"`
	Meta struct {
		Title                 string `json:"title"`
		Synopsis              string `json:"synopsis"`
		BroadcastedOnDateTime string `json:"broadcastedOnDateTime"`
		SeriesTitle           string `json:"seriesTitle"`
		DurationSeconds       int64  `json:"durationSeconds"`
		ClipSourceName        string `json:"clipSourceName"`
		Images                []struct {
			URL string `json:"url"`
		} `json:"images"`
	} `json:"meta"`
}

func extractARDItem(ctx context.Context, transport Transport, displayID, webpageURL string) (Extraction, error) {
	endpoint := ardPageGatewayBase + "pages/ard/item/" + url.PathEscape(displayID) + "?embedded=false&mcV6=true"
	var page ardPageData
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), "", &page); err != nil {
		return Extraction{}, categorizeARDError(err)
	}
	if page.GeoBlocked || strings.Contains(strings.ToLower(page.Availability), "geo") {
		return Extraction{}, ErrRegionRestricted
	}
	var player *struct {
		Type            string `json:"type"`
		BlockedByFSK    bool   `json:"blockedByFsk"`
		MediaCollection struct {
			Embedded ardMediaCollection `json:"embedded"`
		} `json:"mediaCollection"`
	}
	for index := range page.Widgets {
		if page.Widgets[index].Type == "player_ondemand" || page.Widgets[index].Type == "player_live" {
			// The anonymous wire type is intentionally copied into the matching
			// local shape to keep player selection deterministic.
			encoded, _ := json.Marshal(page.Widgets[index])
			var selected struct {
				Type            string `json:"type"`
				BlockedByFSK    bool   `json:"blockedByFsk"`
				MediaCollection struct {
					Embedded ardMediaCollection `json:"embedded"`
				} `json:"mediaCollection"`
			}
			_ = json.Unmarshal(encoded, &selected)
			player = &selected
			break
		}
	}
	if player == nil {
		return Extraction{}, ErrUnavailable
	}
	if player.BlockedByFSK {
		return Extraction{}, ErrAuthentication
	}
	formats, subtitles := normalizeARDMedia(player.MediaCollection.Embedded)
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	videoID := instagramRawString(page.Tracking.ATICustomVars.ContentID)
	if videoID == "" {
		videoID = displayID
	}
	title := player.MediaCollection.Embedded.Meta.Title
	if title == "" {
		title = page.Title
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing ARD title", ErrInvalidMetadata)
	}
	isLive := player.Type == "player_live"
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "display_id", Value: value.String(displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "is_live", Value: value.Bool(isLive)},
	)
	meta := player.MediaCollection.Embedded.Meta
	riskString(info, "description", meta.Synopsis)
	riskString(info, "series", meta.SeriesTitle)
	riskString(info, "channel", meta.ClipSourceName)
	riskPositiveInt(info, "duration", meta.DurationSeconds)
	riskPositiveInt(info, "timestamp", riskTimestamp(meta.BroadcastedOnDateTime))
	if len(meta.Images) != 0 && validHTTPURL(meta.Images[0].URL) {
		info.Set("thumbnail", value.String(meta.Images[0].URL))
	}
	if age, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(page.FSKRating), "FSK"), 10, 64); err == nil {
		info.Set("age_limit", value.Int(age))
	}
	if subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	if isLive {
		info.Set("live_status", value.String("is_live"))
	}
	return Media(value.NewInfo(info)), nil
}

func normalizeARDMedia(media ardMediaCollection) ([]value.Value, *value.Object) {
	formats := make([]value.Value, 0)
	seen := make(map[string]bool)
	for _, stream := range media.Streams {
		for _, source := range stream.Media {
			if seen[source.URL] {
				continue
			}
			format, ok := riskFormat(source.URL, "http-"+stream.Kind)
			if !ok {
				continue
			}
			seen[source.URL] = true
			riskPositiveInt(format, "width", source.MaxHResolutionPx)
			riskPositiveInt(format, "height", source.MaxVResolutionPx)
			riskString(format, "vcodec", source.VideoCodec)
			riskString(format, "format_note", source.ForcedLabel)
			if len(source.Audios) != 0 {
				language := source.Audios[0].LanguageCode
				kind := strings.TrimPrefix(source.Audios[0].Kind, "standard")
				if kind != "" {
					language += "-" + kind
				}
				riskString(format, "language", language)
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	subtitles := value.NewObject()
	for _, subtitle := range media.Subtitles {
		language := subtitle.LanguageCode
		if language == "" {
			language = "deu"
		}
		entries := make([]value.Value, 0, len(subtitle.Sources))
		for _, source := range subtitle.Sources {
			if !validHTTPURL(source.URL) {
				continue
			}
			extension := map[string]string{"webvtt": "vtt", "ebutt": "ttml"}[strings.ToLower(source.Kind)]
			entry := value.NewObject(value.Field{Key: "url", Value: value.String(source.URL)})
			riskString(entry, "ext", extension)
			entries = append(entries, value.ObjectValue(entry))
		}
		if len(entries) != 0 {
			subtitles.Set(language, value.List(entries...))
		}
	}
	return formats, subtitles
}

type ardPlaylistPage struct {
	Title    string `json:"title"`
	Synopsis string `json:"synopsis"`
	Teasers  []struct {
		ID        string `json:"id"`
		Type      string `json:"type"`
		LongTitle string `json:"longTitle"`
		Links     struct {
			Target struct {
				URLID string `json:"urlId"`
				ID    string `json:"id"`
			} `json:"target"`
		} `json:"links"`
	} `json:"teasers"`
}

func extractARDPlaylist(ctx context.Context, transport Transport, target ardTarget, webpageURL string) (Extraction, error) {
	first, err := requestARDPlaylistPage(ctx, transport, target, 0, 1)
	if err != nil {
		return Extraction{}, err
	}
	sequence, err := OnDemandEntries(ardPlaylistPageSize, func(ctx context.Context, page int) ([]Entry, error) {
		response, err := requestARDPlaylistPage(ctx, transport, target, page, ardPlaylistPageSize)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(response.Teasers))
		for _, teaser := range response.Teasers {
			itemID := teaser.Links.Target.URLID
			if itemID == "" {
				itemID = teaser.Links.Target.ID
			}
			if itemID == "" {
				itemID = teaser.ID
			}
			if !ardIDPattern.MatchString(itemID) || itemID == target.id {
				continue
			}
			mode, extractorKey := "video", "ard"
			if teaser.Type == "compilation" {
				mode = "sammlung"
			}
			entries = append(entries, Entry{URL: "https://www.ardmediathek.de/" + mode + "/" + itemID, ExtractorKey: extractorKey, ID: teaser.ID, Title: teaser.LongTitle})
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	fullID := target.id
	if target.season != "" {
		fullID += "_" + target.season
	}
	if target.version != "" {
		fullID += "_" + target.version
	}
	title := first.Title
	if title == "" {
		title = target.displayID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(fullID)},
		value.Field{Key: "display_id", Value: value.String(target.displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
	)
	riskString(info, "description", first.Synopsis)
	return Playlist(value.NewInfo(info), sequence)
}

func requestARDPlaylistPage(ctx context.Context, transport Transport, target ardTarget, page, pageSize int) (ardPlaylistPage, error) {
	apiPath := "widgets/ard/asset/"
	if target.kind == "sammlung" {
		apiPath = "compilations/ard/"
	}
	query := make(url.Values)
	query.Set("pageNumber", strconv.Itoa(page))
	query.Set("pageSize", strconv.Itoa(pageSize))
	if target.season != "" {
		query.Set("seasoned", "true")
		query.Set("seasonNumber", target.season)
		query.Set("withOriginalversion", strconv.FormatBool(target.version == "OV"))
		query.Set("withAudiodescription", strconv.FormatBool(target.version == "AD"))
	}
	endpoint := ardPageGatewayBase + apiPath + url.PathEscape(target.id) + "?" + query.Encode()
	var response ardPlaylistPage
	if err := requestRiskJSON(ctx, transport, http.MethodGet, endpoint, nil, make(http.Header), "", &response); err != nil {
		return ardPlaylistPage{}, categorizeARDError(err)
	}
	return response, nil
}

func categorizeARDError(err error) error {
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
