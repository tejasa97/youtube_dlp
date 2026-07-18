package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const rumbleEmbedEndpoint = "https://rumble.com/embedJS/u3/"

type Rumble struct{}

func NewRumble() Rumble     { return Rumble{} }
func (Rumble) Name() string { return "rumble" }

var rumbleID = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,128}$`)
var rumbleEmbedID = regexp.MustCompile(`(?i)(?:["']video["']\s*:\s*["']|/embed/)([a-z0-9][a-z0-9.-]{1,128})`)
var rumbleVideoLink = regexp.MustCompile(`(?i)href=["'](/v[a-z0-9.-]+(?:[^"'?#]*)?\.html)["']`)

func (Rumble) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "rumble.com" && h != "www.rumble.com" {
		return false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) == 2 && p[0] == "embed" {
		return rumbleID.MatchString(strings.TrimSuffix(p[1], ".html"))
	}
	if len(p) == 1 && p[0] != "videos" && strings.HasPrefix(p[0], "v") {
		return rumbleID.MatchString(strings.Split(p[0], "-")[0]) || rumbleID.MatchString(strings.TrimSuffix(p[0], ".html"))
	}
	return len(p) == 2 && (p[0] == "c" || p[0] == "user") && rumbleID.MatchString(p[1])
}
func (Rumble) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewRumble().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) == 2 && (p[0] == "c" || p[0] == "user") {
		return extractRumbleChannel(request, p[1], u)
	}
	id := ""
	if len(p) == 2 && p[0] == "embed" {
		id = strings.TrimSuffix(p[1], ".html")
	} else {
		page, _, err := request.Transport.ReadPage(ctx, request.URL)
		if err != nil {
			return Extraction{}, err
		}
		if int64(len(page)) > maxExtractorJSONBytes {
			return Extraction{}, ErrJSONResponseTooLarge
		}
		match := rumbleEmbedID.FindSubmatch(page)
		if len(match) == 2 {
			id = string(match[1])
		}
		if id == "" {
			id = strings.Split(strings.TrimSuffix(p[0], ".html"), "-")[0]
		}
	}
	if !rumbleID.MatchString(id) {
		return Extraction{}, fmt.Errorf("%w: invalid Rumble video ID", ErrInvalidMetadata)
	}
	endpoint, _ := url.Parse(rumbleEmbedEndpoint)
	q := endpoint.Query()
	q.Set("request", "video")
	q.Set("ver", "2")
	q.Set("v", id)
	endpoint.RawQuery = q.Encode()
	var video rumbleVideo
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, endpoint.String(), nil, make(http.Header), &video); err != nil {
		return Extraction{}, categorizeRumbleError(err)
	}
	return normalizeRumble(video, id, "https://rumble.com/embed/"+id)
}

type rumbleVideo struct {
	Title            string  `json:"title"`
	Duration         float64 `json:"duration"`
	PubDate          string  `json:"pubDate"`
	Live             int     `json:"live"`
	LivestreamHasDVR *bool   `json:"livestream_has_dvr"`
	FPS              float64 `json:"fps"`
	I                string  `json:"i"`
	Author           struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"author"`
	UA map[string]json.RawMessage `json:"ua"`
	CC map[string]struct {
		Path     string `json:"path"`
		Language string `json:"language"`
	} `json:"cc"`
	Sys struct {
		Msg string `json:"msg"`
	} `json:"sys"`
}
type rumbleVariant struct {
	URL  string `json:"url"`
	Meta struct {
		H       int64 `json:"h"`
		W       int64 `json:"w"`
		Bitrate int64 `json:"bitrate"`
		Size    int64 `json:"size"`
		Live    bool  `json:"live"`
	} `json:"meta"`
}

func normalizeRumble(v rumbleVideo, id, webpage string) (Extraction, error) {
	if strings.TrimSpace(v.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: missing Rumble title", ErrInvalidMetadata)
	}
	live := rumbleLiveStatus(v)
	formats := make([]value.Value, 0)
	keys := make([]string, 0, len(v.UA))
	for k := range v.UA {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, kind := range keys {
		for _, variant := range rumbleVariants(v.UA[kind]) {
			if !validHTTPURL(variant.URL) || kind == "tar" {
				continue
			}
			if kind == "hls" {
				formats = append(formats, value.ObjectValue(manifestFormat("hls", variant.URL, "m3u8_native")))
				continue
			}
			formatID := kind
			if variant.Meta.H > 0 {
				formatID += "-" + strconv.FormatInt(variant.Meta.H, 10) + "p"
			}
			f := value.NewObject(value.Field{Key: "format_id", Value: value.String(formatID)}, value.Field{Key: "url", Value: value.String(variant.URL)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "protocol", Value: value.String("https")})
			if kind == "audio" {
				f.Set("vcodec", value.String("none"))
			}
			if kind == "timeline" {
				f.Set("acodec", value.String("none"))
			}
			setPositiveInt(f, "width", variant.Meta.W)
			setPositiveInt(f, "height", variant.Meta.H)
			if variant.Meta.Bitrate > 0 {
				f.Set("tbr", value.Float(float64(variant.Meta.Bitrate)/1000))
			}
			formats = append(formats, value.ObjectValue(f))
		}
	}
	if len(formats) == 0 {
		if live == "is_upcoming" {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: no Rumble formats", ErrInvalidMetadata)
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(v.Title)}, value.Field{Key: "uploader", Value: value.String(v.Author.Name)}, value.Field{Key: "channel", Value: value.String(v.Author.Name)}, value.Field{Key: "channel_url", Value: value.String(v.Author.URL)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "formats", Value: value.List(formats...)}, value.Field{Key: "live_status", Value: value.String(live)})
	if live != "is_live" && live != "post_live" && v.Duration > 0 {
		info.Set("duration", value.Float(v.Duration))
	}
	if validHTTPURL(v.I) {
		info.Set("thumbnail", value.String(v.I))
	}
	if ts, err := time.Parse(time.RFC3339, v.PubDate); err == nil {
		info.Set("timestamp", value.Int(ts.Unix()))
	}
	subs := value.NewObject()
	for lang, s := range v.CC {
		if validHTTPURL(s.Path) {
			subs.Set(lang, value.List(value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(s.Path)}, value.Field{Key: "name", Value: value.String(s.Language)}))))
		}
	}
	if len(subs.Fields()) > 0 {
		info.Set("subtitles", value.ObjectValue(subs))
	}
	return Media(value.NewInfo(info)), nil
}
func rumbleVariants(raw json.RawMessage) []rumbleVariant {
	var list []rumbleVariant
	if json.Unmarshal(raw, &list) == nil {
		return list
	}
	var object map[string]rumbleVariant
	if json.Unmarshal(raw, &object) == nil {
		keys := make([]string, 0, len(object))
		for k := range object {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			item := object[k]
			if item.Meta.H == 0 {
				item.Meta.H, _ = strconv.ParseInt(k, 10, 64)
			}
			list = append(list, item)
		}
		return list
	}
	var nested map[string][]rumbleVariant
	if json.Unmarshal(raw, &nested) == nil {
		keys := make([]string, 0, len(nested))
		for k := range nested {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			list = append(list, nested[k]...)
		}
	}
	return list
}
func rumbleLiveStatus(v rumbleVideo) string {
	switch v.Live {
	case 0:
		if v.LivestreamHasDVR != nil {
			return "was_live"
		}
		return "not_live"
	case 1:
		if v.LivestreamHasDVR != nil && *v.LivestreamHasDVR {
			return "is_upcoming"
		}
		return "was_live"
	case 2:
		return "is_live"
	}
	return "not_live"
}
func extractRumbleChannel(request Request, id string, u *url.URL) (Extraction, error) {
	base := &url.URL{Scheme: "https", Host: "rumble.com", Path: u.Path}
	sequence, err := OnDemandEntries(24, func(ctx context.Context, page int) ([]Entry, error) {
		next := *base
		q := next.Query()
		q.Set("page", strconv.Itoa(page+1))
		next.RawQuery = q.Encode()
		body, _, err := request.Transport.ReadPage(ctx, next.String())
		if err != nil {
			return nil, err
		}
		if int64(len(body)) > maxExtractorJSONBytes {
			return nil, ErrJSONResponseTooLarge
		}
		matches := rumbleVideoLink.FindAllSubmatch(body, -1)
		entries := make([]Entry, 0, len(matches))
		seen := make(map[string]bool)
		for _, m := range matches {
			raw := "https://rumble.com" + string(m[1])
			if seen[raw] {
				continue
			}
			seen[raw] = true
			entries = append(entries, Entry{URL: raw, ExtractorKey: "rumble", Transparent: true})
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(id)}, value.Field{Key: "webpage_url", Value: value.String(base.String())})), sequence)
}
func categorizeRumbleError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		}
	}
	return err
}
