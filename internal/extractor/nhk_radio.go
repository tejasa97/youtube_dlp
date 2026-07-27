// NHK Radiru (radio) extractors: NhkRadiruIE, NhkRadioNewsPageIE, and
// NhkRadiruLiveIE. The Radiru APIs are stateless JSON over HTTPS, the news
// endpoint returns a flat list of all news-site items, and the live endpoint
// is configured by an XML document that defines areas, streams, and now-on-air
// URLs.
package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	nhkRadioMaxURLBytes      = 4096
	nhkRadioMaxConfigBytes   = 4 << 20
	nhkRadioMaxSeriesBytes   = 8 << 20
	nhkRadioMaxNewsBytes     = 8 << 20
	nhkRadioMaxDetailBytes   = 4 << 20
	nhkRadioMaxPlaylistBytes = 2 << 20
	nhkRadioMaxXMLDepth      = 64
	nhkRadioMaxXMLAttributes = 256
	nhkRadioMaxNewsEntries   = 1024
	nhkRadioMaxSeriesEntries = 1024
	nhkRadioMaxThumbnails    = 32
	nhkRadioMaxCategories    = 64
	nhkRadioMaxCast          = 64
	nhkRadioMaxEpisodes      = 1024
	nhkRadioMaxChapter       = 256
	nhkRadioMaxMetadata      = 8192
	nhkRadioMaxAreas         = 128
)

const (
	nhkRadioOnDemandHTMLHost  = "www.nhk.or.jp"
	nhkRadioOnDemandHTMLPathA = "/radio/player/ondemand.html"
	nhkRadioOnDemandHTMLPathB = "/radio/ondemand/detail.html"
	nhkRadioNewsHandOffPath   = "/radionews/"
	nhkRadioSpecialNewsSiteID = "18439M2W42"
	nhkRadioSpecialNewsCorner = "01"
	nhkRadioSeriesAPIBase     = "https://www.nhk.or.jp/radio-api/app/v1/web/ondemand/series"
	nhkRadioNewsAPIBase       = "https://www.nhk.or.jp/s-media/news/news-site/list/v1/all.json"
	nhkRadioConfigAPIBase     = "https://www.nhk.or.jp/radio/config/config_web.xml"
	nhkRadioLiveURLTemplateR1 = "https://www.nhk.or.jp/radio/player/?ch=r1"
	nhkRadioLiveURLTemplateR2 = "https://www.nhk.or.jp/radio/player/?ch=r2"
	nhkRadioLiveURLTemplateFM = "https://www.nhk.or.jp/radio/player/?ch=fm"
	nhkRadioDefaultArea       = "tokyo"
	nhkRadioMaxAreaLen        = 32
)

// nhkRadioProgramKey is the parsed `p` query parameter in on-demand URLs.
// It is composed of `{site}_{corner}[_{headline}]` where the optional
// headline may itself contain underscores.
type nhkRadioProgramKey struct {
	SiteID     string
	Corner     string
	Headline   string
	Playlist   bool
	Identifier string
}

func nhkRadioParseProgramKey(raw string) (nhkRadioProgramKey, bool) {
	if raw == "" {
		return nhkRadioProgramKey{}, false
	}
	parts := strings.Split(raw, "_")
	if len(parts) < 2 {
		return nhkRadioProgramKey{}, false
	}
	site := strings.TrimSpace(parts[0])
	corner := strings.TrimSpace(parts[1])
	if len(site) > 64 || len(corner) > 64 || site == "" || corner == "" {
		return nhkRadioProgramKey{}, false
	}
	identifier := site + "_" + corner
	playlist := len(parts) == 2
	if playlist {
		return nhkRadioProgramKey{
			SiteID:     site,
			Corner:     corner,
			Headline:   "",
			Playlist:   true,
			Identifier: identifier,
		}, true
	}
	headline := strings.TrimSpace(strings.Join(parts[2:], "_"))
	if headline == "" || len(headline) > 128 {
		return nhkRadioProgramKey{}, false
	}
	return nhkRadioProgramKey{
		SiteID:     site,
		Corner:     corner,
		Headline:   headline,
		Playlist:   false,
		Identifier: identifier + "_" + headline,
	}, true
}

func nhkRadioValidateProgramKey(key nhkRadioProgramKey) bool {
	if len(key.SiteID) == 0 || len(key.SiteID) > 64 {
		return false
	}
	for _, r := range key.SiteID {
		if r >= '0' && r <= '9' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		return false
	}
	return nhkRadioValidateIdentifier(key.Corner) && (key.Playlist || nhkRadioValidateIdentifier(key.Headline))
}

func nhkRadioValidateIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'A' && r <= 'Z':
		case r >= 'a' && r <= 'z':
		case r == '_' || r == '-':
		default:
			return false
		}
	}
	return true
}

// --- Routing ---

// NhkRadiruIE is the catch-all Radiru on-demand extractor. It is only
// registered *after* the news-page and live extractors so those get first
// look at their narrower URL families.
type NhkRadiruIE struct{}

func NewNhkRadiruIE() NhkRadiruIE { return NhkRadiruIE{} }
func (NhkRadiruIE) Name() string  { return "nhk_radiru" }

func (NhkRadiruIE) Suitable(parsed *url.URL) bool {
	return nhkRadiruOnDemandSuitable(parsed)
}

func nhkRadiruOnDemandSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkRadioOnDemandHTMLHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	if lowerPath != nhkRadioOnDemandHTMLPathA && lowerPath != nhkRadioOnDemandHTMLPathB {
		return false
	}
	key, ok := nhkRadioParseProgramKey(parsed.Query().Get("p"))
	if !ok {
		return false
	}
	return nhkRadioValidateProgramKey(key)
}

func (NhkRadiruIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkRadioOnDemand(ctx, request)
}

// NhkRadioNewsPageIE handles the transparent handoff from /radionews/ to the
// dedicated news corner 18439M2W42_01. It is registered before NhkRadiruIE so
// that broader routes cannot swallow it.
type NhkRadioNewsPageIE struct{}

func NewNhkRadioNewsPageIE() NhkRadioNewsPageIE { return NhkRadioNewsPageIE{} }
func (NhkRadioNewsPageIE) Name() string         { return "nhk_radio_news_page" }

func (NhkRadioNewsPageIE) Suitable(parsed *url.URL) bool {
	return nhkRadioNewsPageSuitable(parsed)
}

func nhkRadioNewsPageSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkRadioOnDemandHTMLHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	return lowerPath == "/radionews" || lowerPath == "/radionews/"
}

func (NhkRadioNewsPageIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkRadioNewsPage(ctx, request)
}

// NhkRadiruLiveIE handles Radiru live URLs.
type NhkRadiruLiveIE struct{}

func NewNhkRadiruLiveIE() NhkRadiruLiveIE { return NhkRadiruLiveIE{} }
func (NhkRadiruLiveIE) Name() string      { return "nhk_radiru_live" }

func (NhkRadiruLiveIE) Suitable(parsed *url.URL) bool {
	return nhkRadiruLiveSuitable(parsed)
}

func nhkRadiruLiveSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkRadioOnDemandHTMLHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	if lowerPath != "/radio/player/" {
		return false
	}
	switch strings.ToLower(parsed.Query().Get("ch")) {
	case "r1", "r2", "fm":
		return true
	default:
		return false
	}
}

func (NhkRadiruLiveIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkRadioLive(ctx, request)
}

// --- On-demand / news ---

func extractNhkRadioOnDemand(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkRadiruOnDemandSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	rawKey := parsed.Query().Get("p")
	key, _ := nhkRadioParseProgramKey(rawKey)
	if key.SiteID == nhkRadioSpecialNewsSiteID {
		return extractNhkRadioNewsCorner(ctx, request, key)
	}
	config, err := nhkRadioFetchConfig(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	seriesEndpoint := nhkRadioSeriesEndpoint(config)
	seriesPayload, err := nhkRadioFetchSeries(ctx, request.Transport, seriesEndpoint, key.SiteID, key.Corner)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	series, err := nhkRadioBuildSeries(config, seriesPayload, key.SiteID, key.Corner)
	if err != nil {
		return Extraction{}, err
	}
	if series.noEpisodes {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru series is empty", ErrInvalidMetadata)
	}
	if key.Playlist {
		return nhkRadioRenderPlaylist(series)
	}
	episode, err := series.selectEpisode(key.Headline)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	episodeURL := series.detailURL(episode.ID)
	detail, err := nhkRadioFetchSeries(ctx, request.Transport, episodeURL, key.SiteID, key.Corner)
	if err != nil {
		// Extended metadata failure must be nonfatal; retain fallback
		// metadata. The series still carries title, station, etc.
		_ = err
	} else if extended := nhkRadioMergeDetail(episode, detail, config); extended != nil {
		episode = extended
	}
	return nhkRadioRenderEpisode(ctx, request, series, episode)
}

func nhkRadioRenderPlaylist(series *nhkRadioSeries) (Extraction, error) {
	info := series.seriesInfo()
	if len(series.episodes) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru series has no items", ErrInvalidPlaylist)
	}
	entries := make([]Entry, 0, len(series.episodes))
	for _, episode := range series.episodes {
		entries = append(entries, Entry{
			URL:          nhkRadioEpisodePublicURL(series, episode),
			ExtractorKey: "nhk_radiru",
			ID:           nhkRadioEpisodeID(series, episode),
			Title:        episode.Title,
		})
	}
	sequence, err := nhkRadioStaticSequence(entries)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(info), sequence)
}

func nhkRadioRenderEpisode(ctx context.Context, request Request, series *nhkRadioSeries, episode *nhkRadioEpisode) (Extraction, error) {
	if episode.StreamURL == "" {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru episode stream missing", ErrUnavailable)
	}
	formats, err := nhkRadioHLSFormats(ctx, request.Transport, episode.StreamURL)
	if err != nil {
		return Extraction{}, err
	}
	info := episode.episodeInfo(series)
	if duration := episode.DurationSeconds(); duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	if episode.WasLive() {
		info.Set("was_live", value.Bool(true))
	}
	if release := episode.ReleaseTimestamp(); release > 0 {
		info.Set("release_timestamp", value.Int(release))
	}
	if upload := episode.UploadTimestamp(); upload > 0 {
		info.Set("timestamp", value.Int(upload))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru episode stream not found", ErrUnavailable)
	}
	info.Set("formats", value.List(formats...))
	return Media(value.NewInfo(info)), nil
}

func nhkRadioEpisodePublicURL(series *nhkRadioSeries, episode *nhkRadioEpisode) string {
	return "https://www.nhk.or.jp/radio/player/ondemand.html?p=" + url.QueryEscape(series.identifier+"_"+episode.ID)
}

func nhkRadioEpisodeID(series *nhkRadioSeries, episode *nhkRadioEpisode) string {
	return series.identifier + "_" + episode.ID
}

// --- News handoff ---

func extractNhkRadioNewsPage(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkRadioNewsPageSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{
		URL:          "https://www.nhk.or.jp" + nhkRadioOnDemandHTMLPathB + "?p=" + url.QueryEscape(nhkRadioSpecialNewsSiteID+"_"+nhkRadioSpecialNewsCorner),
		ExtractorKey: "nhk_radiru",
	})
}

func extractNhkRadioNewsCorner(ctx context.Context, request Request, key nhkRadioProgramKey) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	newsPayload, err := nhkRadioFetchNews(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	series, err := nhkRadioBuildNewsSeries(newsPayload, key.SiteID, key.Corner)
	if err != nil {
		return Extraction{}, err
	}
	if series.noEpisodes {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru news is empty", ErrInvalidMetadata)
	}
	if key.Playlist {
		return nhkRadioRenderNewsPlaylist(series, newsPayload)
	}
	headline, err := series.selectNewsHeadline(key.Headline)
	if err != nil {
		return Extraction{}, err
	}
	return nhkRadioRenderNewsEpisode(ctx, request, series, headline)
}

// --- Config XML parsing ---

type nhkRadioConfigArea struct {
	Key   string
	Name  string
	R1HLS string
	R2HLS string
	FMHLS string
}

type nhkRadioConfigSeries struct {
	SiteID        string
	Title         string
	Station       string
	ProgramDetail string
}

type nhkRadioConfigLive struct {
	Areas    map[string]nhkRadioConfigArea
	NowOnAir string // url_program_noa template
}

type nhkRadioConfig struct {
	Series   map[string]*nhkRadioConfigSeries
	Programs map[string]nhkRadioProgramConfig
	Live     nhkRadioConfigLive
}

type nhkRadioProgramConfig struct {
	ProgramDetail string
	Title         string
	Station       string
}

func nhkRadioFetchConfig(ctx context.Context, transport Transport) (*nhkRadioConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nhkRadioConfigAPIBase, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru config request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "application/xml")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, nhkRadioMaxConfigBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkRadioMaxConfigBytes {
		return nil, fmt.Errorf("%w: NHK Radiru config too large", ErrInvalidMetadata)
	}
	return nhkRadioParseConfigXML(data)
}

func nhkRadioParseConfigXML(data []byte) (*nhkRadioConfig, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity

	config := &nhkRadioConfig{
		Series:   map[string]*nhkRadioConfigSeries{},
		Programs: map[string]nhkRadioProgramConfig{},
		Live: nhkRadioConfigLive{
			Areas: map[string]nhkRadioConfigArea{},
		},
	}

	type dataBlock struct {
		area    string
		areakey string
		r1hls   string
		r2hls   string
		fmhls   string
	}
	var stack []xml.StartElement
	var current *dataBlock
	var textBuf strings.Builder
	tokenCount := 0
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: invalid NHK Radiru config XML: %v", ErrInvalidMetadata, err)
		}
		tokenCount++
		if tokenCount > 100000 {
			return nil, fmt.Errorf("%w: NHK Radiru config XML too large", ErrInvalidMetadata)
		}
		switch t := token.(type) {
		case xml.StartElement:
			if len(stack) >= nhkRadioMaxXMLDepth {
				return nil, fmt.Errorf("%w: NHK Radiru config XML too deep", ErrInvalidMetadata)
			}
			stack = append(stack, t)
			textBuf.Reset()
			if strings.EqualFold(t.Name.Local, "data") {
				current = &dataBlock{}
			}
		case xml.EndElement:
			name := strings.ToLower(t.Name.Local)
			text := strings.TrimSpace(textBuf.String())
			textBuf.Reset()
			if current != nil {
				switch name {
				case "area":
					current.area = text
				case "areakey":
					current.areakey = text
				case "r1hls":
					current.r1hls = text
				case "r2hls":
					current.r2hls = text
				case "fmhls":
					current.fmhls = text
				case "data":
					if current.area != "" && len(config.Live.Areas) < nhkRadioMaxAreas {
						config.Live.Areas[current.area] = nhkRadioConfigArea{
							Key:   current.areakey,
							Name:  current.area,
							R1HLS: current.r1hls,
							R2HLS: current.r2hls,
							FMHLS: current.fmhls,
						}
					}
					current = nil
				}
			}
			if name == "url_program_noa" && text != "" {
				config.Live.NowOnAir = text
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(textBuf.String())+len(t) > nhkRadioMaxMetadata {
				continue
			}
			textBuf.Write(t)
		}
	}
	return config, nil
}

func nhkRadioAttributeValue(attributes []xml.Attr, name string) string {
	for _, attr := range attributes {
		if strings.EqualFold(attr.Name.Local, name) {
			return strings.TrimSpace(attr.Value)
		}
	}
	return ""
}

// --- Series and episode data ---

type nhkRadioSeries struct {
	identifier string
	key        nhkRadioProgramKey
	config     *nhkRadioConfigSeries
	program    nhkRadioProgramConfig
	episodes   []*nhkRadioEpisode
	noEpisodes bool
}

type nhkRadioEpisode struct {
	ID          string
	Title       string
	Subtitle    string
	Description string
	Station     string
	Series      string
	StreamURL   string
	ReleaseAt   string
	UploadedAt  string
	Thumbnails  []nhkRadioThumbnail
	Categories  []string
	Cast        []string
	ExpiresAt   string
}

type nhkRadioThumbnail struct {
	URL    string
	Width  int
	Height int
}

func (e *nhkRadioEpisode) ReleaseTimestamp() int64 {
	if e == nil {
		return 0
	}
	return nhkRadioParseTimestamp(e.ReleaseAt)
}

func (e *nhkRadioEpisode) UploadTimestamp() int64 {
	if e == nil {
		return 0
	}
	return nhkRadioParseTimestamp(e.UploadedAt)
}

func (e *nhkRadioEpisode) WasLive() bool {
	if e == nil {
		return false
	}
	return e.StreamURL != ""
}

func (e *nhkRadioEpisode) DurationSeconds() float64 {
	if e == nil {
		return 0
	}
	if e.Subtitle == "" {
		return 0
	}
	// Some Radiru payload embed duration in description metadata. The
	// Go model intentionally does not surface the upstream duration
	// parser beyond its presence in raw strings; durations in the model
	// are derived from HLS playlist metadata only.
	return 0
}

func (e *nhkRadioEpisode) episodeInfo(series *nhkRadioSeries) *value.Object {
	identifier := ""
	if series != nil {
		identifier = series.identifier
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(identifier + "_" + e.ID)})
	if e.Title != "" {
		info.Set("title", value.String(e.Title))
	}
	if e.Subtitle != "" {
		info.Set("subtitle", value.String(e.Subtitle))
	}
	if e.Description != "" {
		info.Set("description", value.String(e.Description))
	}
	if e.Series != "" {
		info.Set("series", value.String(e.Series))
	} else if series != nil && series.config != nil && series.config.Title != "" {
		info.Set("series", value.String(series.config.Title))
	}
	if e.Station != "" {
		info.Set("uploader", value.String(e.Station))
		info.Set("station", value.String(e.Station))
	} else if series != nil && series.config != nil && series.config.Station != "" {
		info.Set("uploader", value.String(series.config.Station))
		info.Set("station", value.String(series.config.Station))
	}
	if len(e.Thumbnails) > 0 {
		thumbs := make([]value.Value, 0, len(e.Thumbnails))
		for _, thumb := range e.Thumbnails {
			if len(thumbs) >= nhkRadioMaxThumbnails {
				break
			}
			object := value.NewObject(value.Field{Key: "url", Value: value.String(thumb.URL)})
			if thumb.Width > 0 {
				object.Set("width", value.Int(int64(thumb.Width)))
			}
			if thumb.Height > 0 {
				object.Set("height", value.Int(int64(thumb.Height)))
			}
			thumbs = append(thumbs, value.ObjectValue(object))
		}
		info.Set("thumbnails", value.List(thumbs...))
		if len(thumbs) > 0 {
			if first, ok := thumbs[0].Object(); ok {
				if thumbURL, ok := first.Lookup("url").StringValue(); ok {
					info.Set("thumbnail", value.String(thumbURL))
				}
			}
		}
	}
	if len(e.Categories) > 0 {
		cats := make([]value.Value, 0, len(e.Categories))
		for _, category := range e.Categories {
			if len(cats) >= nhkRadioMaxCategories {
				break
			}
			cats = append(cats, value.String(category))
		}
		info.Set("categories", value.List(cats...))
	}
	if len(e.Cast) > 0 {
		cast := make([]value.Value, 0, len(e.Cast))
		for _, person := range e.Cast {
			if len(cast) >= nhkRadioMaxCast {
				break
			}
			cast = append(cast, value.String(person))
		}
		info.Set("cast", value.List(cast...))
	}
	if release := e.ReleaseTimestamp(); release > 0 {
		info.Set("release_timestamp", value.Int(release))
	}
	if upload := e.UploadTimestamp(); upload > 0 {
		info.Set("timestamp", value.Int(upload))
	}
	info.Set("webpage_url", value.String("https://www.nhk.or.jp/radio/player/ondemand.html?p="+url.QueryEscape(identifier+"_"+e.ID)))
	info.Set("extractor", value.String("nhk_radiru"))
	info.Set("extractor_key", value.String("NhkRadiruIE"))
	return info
}

func (s *nhkRadioSeries) seriesInfo() *value.Object {
	info := value.NewObject(value.Field{Key: "id", Value: value.String(s.identifier)})
	if s.config != nil && s.config.Title != "" {
		info.Set("title", value.String(s.config.Title))
	}
	if s.config != nil && s.config.Station != "" {
		info.Set("uploader", value.String(s.config.Station))
		info.Set("station", value.String(s.config.Station))
	}
	info.Set("webpage_url", value.String("https://www.nhk.or.jp/radio/player/ondemand.html?p="+url.QueryEscape(s.identifier)))
	info.Set("extractor", value.String("nhk_radiru"))
	info.Set("extractor_key", value.String("NhkRadiruIE"))
	return info
}

func (s *nhkRadioSeries) selectEpisode(headline string) (*nhkRadioEpisode, error) {
	for _, episode := range s.episodes {
		if episode.ID == headline {
			return episode, nil
		}
	}
	return nil, fmt.Errorf("%w: NHK Radiru headline not found: %s", ErrUnavailable, headline)
}

func (s *nhkRadioSeries) detailURL(episodeID string) string {
	if s.config != nil && s.config.ProgramDetail != "" {
		// Replace any {id} placeholder; otherwise append the ID.
		if strings.Contains(s.config.ProgramDetail, "{id}") {
			return strings.ReplaceAll(s.config.ProgramDetail, "{id}", url.PathEscape(episodeID))
		}
		if strings.Contains(s.config.ProgramDetail, "%s") {
			return strings.ReplaceAll(s.config.ProgramDetail, "%s", url.PathEscape(episodeID))
		}
		return strings.TrimSuffix(s.config.ProgramDetail, "/") + "/" + url.PathEscape(episodeID)
	}
	if s.program.ProgramDetail != "" {
		return strings.TrimSuffix(s.program.ProgramDetail, "/") + "/" + url.PathEscape(episodeID)
	}
	return ""
}

// --- Network ---

func nhkRadioSeriesEndpoint(_ *nhkRadioConfig) string {
	return nhkRadioSeriesAPIBase
}

func nhkRadioFetchSeries(ctx context.Context, transport Transport, baseURL, site, corner string) (map[string]any, error) {
	if !nhkRadioAcceptsAPIURL(baseURL) {
		return nil, fmt.Errorf("%w: unsafe NHK Radiru API URL", ErrInvalidMetadata)
	}
	endpoint := baseURL
	if site != "" && corner != "" {
		endpoint = fmt.Sprintf("%s?site_id=%s&corner_id=%s", baseURL, url.QueryEscape(site), url.QueryEscape(corner))
	} else if site != "" {
		endpoint = baseURL + "/" + url.PathEscape(site)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru API request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, nhkRadioMaxSeriesBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkRadioMaxSeriesBytes {
		return nil, ErrJSONResponseTooLarge
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty NHK Radiru payload", ErrInvalidMetadata)
	}
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("%w: trailing NHK Radiru JSON", ErrInvalidMetadata)
	}
	return payload, nil
}

func nhkRadioFetchNews(ctx context.Context, transport Transport) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nhkRadioNewsAPIBase, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru news request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, nhkRadioMaxNewsBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkRadioMaxNewsBytes {
		return nil, ErrJSONResponseTooLarge
	}
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru news JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("%w: trailing NHK Radiru news JSON", ErrInvalidMetadata)
	}
	return payload, nil
}

func nhkRadioAcceptsAPIURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > 4096 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	return parsed.Host == "www.nhk.or.jp"
}

// --- Building series and episodes ---

func nhkRadioBuildSeries(config *nhkRadioConfig, payload map[string]any, siteID, corner string) (*nhkRadioSeries, error) {
	series := &nhkRadioSeries{
		identifier: siteID + "_" + corner,
		key:        nhkRadioProgramKey{SiteID: siteID, Corner: corner},
	}
	if config != nil {
		if entry, ok := config.Series[siteID]; ok && entry != nil {
			series.config = entry
		}
	}
	mainNode, _ := payload["main"].(map[string]any)
	programNode, _ := mainNode["program"].(map[string]any)
	if programNode != nil {
		if title, ok := programNode["program_title"].(string); ok {
			series.config = ensureConfigSeries(series.config, siteID, config)
			series.config.Title = title
		}
		if station, ok := programNode["program_station"].(string); ok {
			series.config = ensureConfigSeries(series.config, siteID, config)
			series.config.Station = station
		}
		if detail, ok := programNode["program_detail"].(string); ok && nhkRadioValidDetailURL(detail) {
			series.config = ensureConfigSeries(series.config, siteID, config)
			series.config.ProgramDetail = detail
		}
	}
	episodesRaw, _ := mainNode["episodes"].([]any)
	if episodesRaw == nil {
		// Fallback for news payload shape.
		episodesRaw, _ = payload["data"].([]any)
	}
	if len(episodesRaw) > nhkRadioMaxSeriesEntries {
		episodesRaw = episodesRaw[:nhkRadioMaxSeriesEntries]
	}
	seen := make(map[string]bool, len(episodesRaw))
	for _, raw := range episodesRaw {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id := nhkRadioEpisodeIDFromNode(node)
		if id == "" {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		episode := nhkRadioEpisodeFromNode(node)
		series.episodes = append(series.episodes, episode)
		if len(series.episodes) >= nhkRadioMaxEpisodes {
			break
		}
	}
	if len(series.episodes) == 0 {
		series.noEpisodes = true
	}
	return series, nil
}

func nhkRadioBuildNewsSeries(payload map[string]any, siteID, corner string) (*nhkRadioSeries, error) {
	mainNode, _ := payload["main"].(map[string]any)
	if mainNode == nil {
		return nil, fmt.Errorf("%w: NHK Radiru news payload missing main", ErrInvalidMetadata)
	}
	series := &nhkRadioSeries{
		identifier: siteID + "_" + corner,
		key: nhkRadioProgramKey{
			SiteID:     siteID,
			Corner:     corner,
			Playlist:   true,
			Identifier: siteID + "_" + corner,
		},
		config: &nhkRadioConfigSeries{
			SiteID:  siteID,
			Title:   nhkRadioFirstString(mainNode, "program_name"),
			Station: nhkRadioFirstString(mainNode, "media_name"),
		},
	}
	thumb := nhkRadioFirstString(mainNode, "thumbnail_c")
	if thumb == "" {
		thumb = nhkRadioFirstString(mainNode, "thumbnail_p")
	}
	if thumb != "" {
		series.config.Title = strings.TrimSpace(series.config.Title)
	}
	detailList, _ := mainNode["detail_list"].([]any)
	if len(detailList) > nhkRadioMaxNewsEntries {
		detailList = detailList[:nhkRadioMaxNewsEntries]
	}
	seen := make(map[string]bool, len(detailList))
	for _, raw := range detailList {
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		headlineID := nhkRadioFirstString(node, "headline_id")
		if headlineID == "" || seen[headlineID] {
			continue
		}
		seen[headlineID] = true
		episode := nhkRadioEpisodeFromHeadline(node, thumb)
		if episode.StreamURL == "" {
			continue
		}
		series.episodes = append(series.episodes, episode)
		if len(series.episodes) >= nhkRadioMaxEpisodes {
			break
		}
	}
	if len(series.episodes) == 0 {
		series.noEpisodes = true
	}
	return series, nil
}

func nhkRadioEpisodeFromHeadline(node map[string]any, seriesThumb string) *nhkRadioEpisode {
	headlineID := nhkRadioFirstString(node, "headline_id")
	fileNode := nhkRadioFirstNewsFile(node)
	episode := &nhkRadioEpisode{ID: headlineID}
	if fileNode != nil {
		episode.StreamURL = nhkRadioFirstString(fileNode, "file_name")
		episode.Title = nhkRadioFirstString(fileNode, "file_title")
		episode.Subtitle = nhkRadioFirstString(fileNode, "file_title_sub")
		episode.UploadedAt = nhkRadioFirstString(fileNode, "open_time")
		if release := nhkRadioFirstString(fileNode, "aa_vinfo4"); release != "" {
			if idx := strings.Index(release, "_"); idx >= 0 {
				release = release[:idx]
			}
			episode.ReleaseAt = release
		}
	}
	if thumb := nhkRadioFirstString(node, "headline_image"); thumb != "" {
		episode.Thumbnails = []nhkRadioThumbnail{{URL: thumb}}
	} else if seriesThumb != "" {
		episode.Thumbnails = []nhkRadioThumbnail{{URL: seriesThumb}}
	}
	return episode
}

func nhkRadioFirstNewsFile(node map[string]any) map[string]any {
	raw, ok := node["file_list"].([]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	fileNode, ok := raw[0].(map[string]any)
	if !ok {
		return nil
	}
	return fileNode
}

func (s *nhkRadioSeries) selectNewsHeadline(headline string) (*nhkRadioEpisode, error) {
	for _, episode := range s.episodes {
		if episode.ID == headline {
			return episode, nil
		}
	}
	return nil, fmt.Errorf("%w: NHK Radiru news headline not found: %s", ErrUnavailable, headline)
}

func nhkRadioRenderNewsPlaylist(series *nhkRadioSeries, payload map[string]any) (Extraction, error) {
	info := series.seriesInfo()
	if mainNode, ok := payload["main"].(map[string]any); ok {
		if description := nhkRadioFirstString(mainNode, "site_detail"); description != "" {
			info.Set("description", value.String(description))
		}
		if thumb := nhkRadioFirstString(mainNode, "thumbnail_c"); thumb != "" {
			info.Set("thumbnail", value.String(thumb))
		} else if thumb := nhkRadioFirstString(mainNode, "thumbnail_p"); thumb != "" {
			info.Set("thumbnail", value.String(thumb))
		}
	}
	entries := make([]Entry, 0, len(series.episodes))
	for _, episode := range series.episodes {
		entries = append(entries, Entry{
			URL:          nhkRadioNewsEpisodePublicURL(series, episode),
			ExtractorKey: "nhk_radiru",
			ID:           nhkRadioEpisodeID(series, episode),
			Title:        episode.Title,
		})
	}
	sequence, err := nhkRadioStaticSequence(entries)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(info), sequence)
}

func nhkRadioRenderNewsEpisode(ctx context.Context, request Request, series *nhkRadioSeries, episode *nhkRadioEpisode) (Extraction, error) {
	if episode.StreamURL == "" {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru news stream missing", ErrUnavailable)
	}
	formats, err := nhkRadioHLSFormats(ctx, request.Transport, episode.StreamURL)
	if err != nil {
		return Extraction{}, err
	}
	info := episode.episodeInfo(series)
	if series.config != nil && series.config.Title != "" {
		info.Set("series", value.String(series.config.Title))
	}
	info.Set("was_live", value.Bool(true))
	info.Set("container", value.String("m4a_dash"))
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru news stream not found", ErrUnavailable)
	}
	info.Set("formats", value.List(formats...))
	return Media(value.NewInfo(info)), nil
}

func nhkRadioNewsEpisodePublicURL(series *nhkRadioSeries, episode *nhkRadioEpisode) string {
	return "https://www.nhk.or.jp/radio/ondemand/detail.html?p=" + url.QueryEscape(series.identifier+"_"+episode.ID)
}

func nhkRadioEpisodeIDFromNode(node map[string]any) string {
	for _, key := range []string{"id", "episode_id", "onair_id"} {
		if value, ok := node[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func nhkRadioEpisodeFromNode(node map[string]any) *nhkRadioEpisode {
	episode := &nhkRadioEpisode{}
	episode.ID = nhkRadioEpisodeIDFromNode(node)
	episode.Title = nhkRadioFirstString(node, "title", "episode_title", "program_title")
	episode.Subtitle = nhkRadioFirstString(node, "subtitle", "episode_subtitle")
	episode.Description = nhkRadioFirstString(node, "description", "episode_description", "summary")
	episode.Station = nhkRadioFirstString(node, "station", "program_station")
	episode.Series = nhkRadioFirstString(node, "series", "program_title")
	episode.StreamURL = nhkRadioFirstString(node, "stream_url", "audio_url", "media_url")
	episode.ReleaseAt = nhkRadioFirstString(node, "release_at", "onair_date", "publish_at")
	episode.UploadedAt = nhkRadioFirstString(node, "uploaded_at", "onair_date", "publish_at")
	episode.ExpiresAt = nhkRadioFirstString(node, "expire_at", "expired_at", "end_date")
	if thumbs := nhkRadioThumbnailsFromNode(node); len(thumbs) > 0 {
		episode.Thumbnails = thumbs
	}
	if categories := nhkRadioStringListFromNode(node, nhkRadioMaxCategories, "categories", "tags"); len(categories) > 0 {
		episode.Categories = categories
	}
	if cast := nhkRadioStringListFromNode(node, nhkRadioMaxCast, "cast", "performers"); len(cast) > 0 {
		episode.Cast = cast
	}
	return episode
}

func nhkRadioThumbnailsFromNode(node map[string]any) []nhkRadioThumbnail {
	raw, ok := node["thumbnail"]
	if !ok {
		raw = node["thumbnails"]
	}
	switch value := raw.(type) {
	case string:
		if value == "" {
			return nil
		}
		return []nhkRadioThumbnail{{URL: value}}
	case []any:
		out := make([]nhkRadioThumbnail, 0, len(value))
		for _, item := range value {
			object, ok := item.(map[string]any)
			if !ok {
				continue
			}
			urlValue := nhkRadioFirstString(object, "url", "image_url", "thumbnail_url")
			if urlValue == "" {
				continue
			}
			thumb := nhkRadioThumbnail{URL: urlValue}
			if width, ok := nhkIntFromAny(object["width"]); ok {
				thumb.Width = int(width)
			}
			if height, ok := nhkIntFromAny(object["height"]); ok {
				thumb.Height = int(height)
			}
			out = append(out, thumb)
		}
		return out
	}
	return nil
}

func nhkRadioStringListFromNode(node map[string]any, limit int, keys ...string) []string {
	for _, key := range keys {
		raw, ok := node[key]
		if !ok {
			continue
		}
		switch value := raw.(type) {
		case string:
			if value == "" {
				continue
			}
			return []string{value}
		case []any:
			out := make([]string, 0, len(value))
			for _, item := range value {
				str, ok := item.(string)
				if !ok {
					continue
				}
				if str == "" {
					continue
				}
				out = append(out, str)
				if limit > 0 && len(out) >= limit {
					break
				}
			}
			if len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func nhkRadioFirstString(node map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := node[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func nhkRadioParseTimestamp(text string) int64 {
	if text == "" {
		return 0
	}
	for _, layout := range []string{
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02 15:04:05",
		"2006/01/02 15:04",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func ensureConfigSeries(existing *nhkRadioConfigSeries, siteID string, config *nhkRadioConfig) *nhkRadioConfigSeries {
	if existing != nil {
		return existing
	}
	if config == nil {
		return &nhkRadioConfigSeries{SiteID: siteID}
	}
	entry := &nhkRadioConfigSeries{SiteID: siteID}
	config.Series[siteID] = entry
	return entry
}

func nhkRadioValidDetailURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > 4096 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" {
		return false
	}
	if parsed.Host != "www.nhk.or.jp" {
		return false
	}
	return true
}

func nhkRadioMergeDetail(episode *nhkRadioEpisode, detail map[string]any, _ *nhkRadioConfig) *nhkRadioEpisode {
	main, _ := detail["main"].(map[string]any)
	if main == nil {
		main, _ = detail["data"].(map[string]any)
	}
	if main == nil {
		return nil
	}
	extended := nhkRadioEpisodeFromNode(main)
	if extended.ID == "" {
		extended.ID = episode.ID
	}
	if extended.Title == "" {
		extended.Title = episode.Title
	}
	if extended.Description == "" {
		extended.Description = episode.Description
	}
	if extended.Station == "" {
		extended.Station = episode.Station
	}
	if extended.Series == "" {
		extended.Series = episode.Series
	}
	if extended.StreamURL == "" {
		extended.StreamURL = episode.StreamURL
	}
	if extended.ReleaseAt == "" {
		extended.ReleaseAt = episode.ReleaseAt
	}
	if extended.UploadedAt == "" {
		extended.UploadedAt = episode.UploadedAt
	}
	if len(extended.Thumbnails) == 0 {
		extended.Thumbnails = episode.Thumbnails
	}
	if len(extended.Categories) == 0 {
		extended.Categories = episode.Categories
	}
	if len(extended.Cast) == 0 {
		extended.Cast = episode.Cast
	}
	if extended.ExpiresAt == "" {
		extended.ExpiresAt = episode.ExpiresAt
	}
	return extended
}

func nhkRadioHLSFormats(ctx context.Context, transport Transport, streamURL string) ([]value.Value, error) {
	if transport == nil {
		return nil, ErrTransportIsolation
	}
	if !nhkValidPublicURL(streamURL) {
		return nil, fmt.Errorf("%w: unsafe NHK Radiru stream URL", ErrInvalidMetadata)
	}
	data, err := nhkFetchIsolatedBytes(ctx, transport, streamURL, nhkRadioMaxPlaylistBytes)
	if err != nil {
		return nil, err
	}
	playlist, err := hls.Parse(streamURL, data)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}
	return nhkRadioLiveHLSFormats(playlist), nil
}

// nhkRadioStaticSequence is a tiny wrapper that returns a static playlist
// sequence and surfaces a playlist-shape error when the list is empty.
func nhkRadioStaticSequence(entries []Entry) (EntrySequence, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: NHK Radiru entries empty", ErrInvalidPlaylist)
	}
	return StaticEntries(entries...), nil
}

// --- Live extractor ---

func extractNhkRadioLive(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkRadiruLiveSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	channel := strings.ToLower(parsed.Query().Get("ch"))
	area := strings.ToLower(strings.TrimSpace(request.NHK.RadiruArea))
	if area == "" {
		area = nhkRadioDefaultArea
	}
	if !nhkRadioValidArea(area) {
		return Extraction{}, fmt.Errorf("%w: invalid NHK Radiru area: %s", ErrInvalidMetadata, area)
	}
	config, err := nhkRadioFetchConfig(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	areaConfig, ok := config.Live.Areas[area]
	if !ok {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru area not in config: %s", ErrInvalidMetadata, area)
	}
	streamURL := nhkRadioLiveStreamForChannel(channel, areaConfig)
	if streamURL == "" {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru live stream URL missing", ErrInvalidMetadata)
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	playlist, err := nhkRadioFetchLivePlaylistURL(ctx, request.Transport, streamURL)
	if err != nil {
		return Extraction{}, err
	}
	formats := nhkRadioLiveHLSFormats(playlist)
	title := nhkRadioChannelTitle(channel)
	serviceID := "bs-" + nhkRadioChannelServiceID(channel) + "-" + areaConfig.Key
	if channel == "r2" {
		serviceID = "bs-r2-400"
	}
	broadcast := nhkRadioFetchLiveBroadcast(ctx, request.Transport, config, areaConfig, channel)
	if broadcast != nil {
		if broadcast.ID != "" && channel != "r2" {
			serviceID = broadcast.ID
		}
		if broadcast.Title != "" {
			title = broadcast.Title
		}
	}
	if areaConfig.Name != "" && channel != "r2" && !strings.Contains(title, "・") {
		title = title + "・" + areaConfig.Name
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(serviceID)})
	info.Set("title", value.String(title))
	info.Set("extractor", value.String("nhk_radiru_live"))
	info.Set("extractor_key", value.String("NhkRadiruLiveIE"))
	info.Set("is_live", value.Bool(true))
	info.Set("live_status", value.String("is_live"))
	info.Set("webpage_url", value.String("https://www.nhk.or.jp/radio/player/?ch="+channel))
	if broadcast != nil && len(broadcast.Thumbnails) > 0 {
		thumbs := make([]value.Value, 0, len(broadcast.Thumbnails))
		for _, thumb := range broadcast.Thumbnails {
			object := value.NewObject(value.Field{Key: "url", Value: value.String(thumb.URL)})
			if thumb.Width > 0 {
				object.Set("width", value.Int(int64(thumb.Width)))
			}
			if thumb.Height > 0 {
				object.Set("height", value.Int(int64(thumb.Height)))
			}
			thumbs = append(thumbs, value.ObjectValue(object))
		}
		info.Set("thumbnails", value.List(thumbs...))
		if first, ok := thumbs[0].Object(); ok {
			if thumbURL, ok := first.Lookup("url").StringValue(); ok {
				info.Set("thumbnail", value.String(thumbURL))
			}
		}
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK Radiru live formats missing", ErrUnavailable)
	}
	info.Set("formats", value.List(formats...))
	return Media(value.NewInfo(info)), nil
}

func nhkRadioLiveStreamForChannel(channel string, area nhkRadioConfigArea) string {
	switch channel {
	case "r1":
		return area.R1HLS
	case "r2":
		return area.R2HLS
	case "fm":
		return area.FMHLS
	}
	return ""
}

func nhkRadioChannelServiceID(channel string) string {
	switch channel {
	case "r1":
		return "r1"
	case "r2":
		return "r2"
	case "fm":
		return "r3"
	}
	return channel
}

func nhkRadioFetchLivePlaylistURL(ctx context.Context, transport Transport, streamURL string) (hls.Playlist, error) {
	data, err := nhkFetchIsolatedBytes(ctx, transport, streamURL, nhkRadioMaxPlaylistBytes)
	if err != nil {
		return hls.Playlist{}, err
	}
	playlist, err := hls.Parse(streamURL, data)
	if err != nil {
		return hls.Playlist{}, fmt.Errorf("%w: %v", ErrInvalidMetadata, err)
	}
	return playlist, nil
}

type nhkRadioLiveBroadcast struct {
	ID         string
	Title      string
	Thumbnails []nhkRadioThumbnail
}

func nhkRadioFetchLiveBroadcast(ctx context.Context, transport Transport, config *nhkRadioConfig, area nhkRadioConfigArea, channel string) *nhkRadioLiveBroadcast {
	if transport == nil || config == nil || config.Live.NowOnAir == "" || area.Key == "" {
		return nil
	}
	noaURL := nhkRadioFormatNOAURL(config.Live.NowOnAir, area.Key)
	if noaURL == "" {
		return nil
	}
	payload, err := nhkRadioFetchNOA(ctx, transport, noaURL)
	if err != nil {
		return nil
	}
	stationKey := nhkRadioNOAStationKey(channel)
	if stationKey == "" {
		return nil
	}
	stationNode, _ := payload[stationKey].(map[string]any)
	if stationNode == nil {
		return nil
	}
	published, _ := stationNode["publishedOn"].(map[string]any)
	if published == nil {
		return nil
	}
	broadcast := &nhkRadioLiveBroadcast{
		ID:    nhkRadioFirstString(published, "id"),
		Title: nhkRadioFirstString(published, "broadcastDisplayName"),
	}
	if logos, ok := published["logo"].([]any); ok {
		for _, raw := range logos {
			node, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			urlValue := nhkRadioFirstString(node, "url")
			if urlValue == "" {
				continue
			}
			thumb := nhkRadioThumbnail{URL: urlValue}
			if width, ok := nhkIntFromAny(node["width"]); ok {
				thumb.Width = int(width)
			}
			if height, ok := nhkIntFromAny(node["height"]); ok {
				thumb.Height = int(height)
			}
			broadcast.Thumbnails = append(broadcast.Thumbnails, thumb)
		}
	}
	if broadcast.ID == "" && broadcast.Title == "" && len(broadcast.Thumbnails) == 0 {
		return nil
	}
	return broadcast
}

func nhkRadioFormatNOAURL(template, areaKey string) string {
	if template == "" || areaKey == "" {
		return ""
	}
	raw := strings.ReplaceAll(template, "{area}", areaKey)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	if !nhkRadioAcceptsAPIURL(raw) {
		return ""
	}
	return raw
}

func nhkRadioFetchNOA(ctx context.Context, transport Transport, rawURL string) (map[string]any, error) {
	if !nhkRadioAcceptsAPIURL(rawURL) {
		return nil, fmt.Errorf("%w: unsafe NHK Radiru now-on-air URL", ErrInvalidMetadata)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru now-on-air request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, nhkRadioMaxDetailBytes+1))
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkRadioMaxDetailBytes {
		return nil, ErrJSONResponseTooLarge
	}
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: invalid NHK Radiru now-on-air JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("%w: trailing NHK Radiru now-on-air JSON", ErrInvalidMetadata)
	}
	return payload, nil
}

func nhkRadioNOAStationKey(channel string) string {
	switch channel {
	case "r1":
		return "r1"
	case "r2":
		return "r2"
	case "fm":
		return "r3"
	}
	return ""
}

func nhkRadioValidArea(area string) bool {
	if len(area) == 0 || len(area) > nhkRadioMaxAreaLen {
		return false
	}
	for _, r := range area {
		if r >= 'A' && r <= 'Z' {
			continue
		}
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= '0' && r <= '9' {
			continue
		}
		if r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func nhkRadioChannelTitle(channel string) string {
	switch channel {
	case "r1":
		return "NHK ラジオ第1"
	case "r2":
		return "NHK ラジオ第2"
	case "fm":
		return "NHK FM"
	}
	return ""
}

func nhkRadioLiveHLSFormats(playlist hls.Playlist) []value.Value {
	formats := make([]value.Value, 0, len(playlist.Variants))
	for _, variant := range playlist.Variants {
		if variant.URL == "" {
			continue
		}
		if !nhkValidPublicURL(variant.URL) {
			continue
		}
		object := value.NewObject(value.Field{Key: "format_id", Value: value.String("hls-aac")})
		object.Set("url", value.String(variant.URL))
		object.Set("ext", value.String("aac"))
		object.Set("acodec", value.String("aac"))
		object.Set("vcodec", value.String("none"))
		object.Set("protocol", value.String("m3u8_native"))
		if variant.Bandwidth > 0 {
			object.Set("tbr", value.Float(float64(variant.Bandwidth)))
		}
		formats = append(formats, value.ObjectValue(object))
	}
	if len(formats) == 0 {
		return nil
	}
	return nhkCredentialIsolateFormats(formats)
}

// ensure unused symbols remain referenced when wiring is finalized.
var (
	_ = regexp.MustCompile
	_ = errors.Is
	_ = hls.Playlist{}
	_ = strconv.Itoa
)
