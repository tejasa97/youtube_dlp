package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const weatherComMaxVariants = 64

var weatherComSlug = regexp.MustCompile(`^[A-Za-z0-9-]{1,256}$`)

// WeatherCom is a thin ThePlatform-aware adapter for weather.com video pages.
type WeatherCom struct{}

func NewWeatherCom() WeatherCom { return WeatherCom{} }
func (WeatherCom) Name() string { return "weathercom" }

func (WeatherCom) Suitable(parsed *url.URL) bool {
	_, ok := parseWeatherComURL(parsed)
	return ok
}

func (WeatherCom) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseWeatherComURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	body, err := json.Marshal([]map[string]any{{
		"name": "getCMSAssetsUrlConfig",
		"params": map[string]any{
			"language": strings.ReplaceAll(target.locale, "-", "_"),
			"query": map[string]any{
				"assetName": map[string]any{"$in": target.assetName},
			},
		},
	}})
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: encode Weather.com request", ErrInvalidMetadata)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var response map[string]any
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodPost, "https://weather.com/api/v1/p/redux-dal", body, headers, &response); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return normalizeWeatherCom(ctx, request.Transport, response, target)
}

type weatherComTarget struct {
	assetName, locale, displayID, canonical string
}

func parseWeatherComURL(parsed *url.URL) (weatherComTarget, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return weatherComTarget{}, false
	}
	if strings.ToLower(parsed.Hostname()) != "weather.com" && strings.ToLower(parsed.Hostname()) != "www.weather.com" {
		return weatherComTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) < 2 {
		return weatherComTarget{}, false
	}
	locale := "en-US"
	start := 0
	if len(segments[0]) == 5 && segments[0][2] == '-' {
		locale = segments[0]
		start = 1
	}
	videoIndex := -1
	for i := start; i < len(segments); i++ {
		if segments[i] == "video" {
			videoIndex = i
			break
		}
	}
	if videoIndex < 0 || videoIndex+1 >= len(segments) || !weatherComSlug.MatchString(segments[videoIndex+1]) {
		return weatherComTarget{}, false
	}
	displayID := segments[videoIndex+1]
	assetName := "/" + strings.Join(segments, "/")
	return weatherComTarget{
		assetName: assetName, locale: locale, displayID: displayID,
		canonical: "https://weather.com" + assetName,
	}, true
}

func normalizeWeatherCom(ctx context.Context, transport Transport, response map[string]any, target weatherComTarget) (Extraction, error) {
	dal, _ := response["dal"].(map[string]any)
	config, _ := dal["getCMSAssetsUrlConfig"].(map[string]any)
	var asset map[string]any
	for _, blockValue := range config {
		block, ok := blockValue.(map[string]any)
		if !ok {
			continue
		}
		data, _ := block["data"].([]any)
		if len(data) == 0 {
			continue
		}
		asset, _ = data[0].(map[string]any)
		if asset != nil {
			break
		}
	}
	if asset == nil {
		return Extraction{}, ErrUnavailable
	}
	videoID, _ := asset["id"].(string)
	videoID = strings.TrimSpace(videoID)
	if videoID == "" || len(videoID) > 128 {
		return Extraction{}, fmt.Errorf("%w: missing Weather.com video id", ErrInvalidMetadata)
	}
	title, _ := asset["title"].(string)
	if strings.TrimSpace(title) == "" {
		if seo, ok := asset["seometa"].(map[string]any); ok {
			title, _ = seo["title"].(string)
		}
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = target.displayID
	}
	variants, _ := asset["variants"].(map[string]any)
	if len(variants) > weatherComMaxVariants {
		return Extraction{}, fmt.Errorf("%w: Weather.com variant limit", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(variants))
	seen := make(map[string]struct{}, len(variants))
	appendFormats := func(extra ...value.Value) error {
		if len(formats)+len(extra) > thePlatformMaxFormats {
			return fmt.Errorf("%w: Weather.com format limit", ErrInvalidMetadata)
		}
		formats = append(formats, extra...)
		return nil
	}
	for variantID, raw := range variants {
		rawURL, _ := raw.(string)
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" || !strictValidHostedHTTPURL(rawURL) {
			continue
		}
		if _, exists := seen[rawURL]; exists {
			continue
		}
		seen[rawURL] = struct{}{}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
		if ext == "jpg" || ext == "jpeg" || ext == "png" {
			continue
		}
		if NewThePlatform().Suitable(parsed) {
			targetTP, ok := parseThePlatformURL(parsed)
			if !ok {
				return Extraction{}, fmt.Errorf("%w: invalid Weather.com ThePlatform variant", ErrInvalidMetadata)
			}
			tpFormats, _, err := extractThePlatformSMIL(ctx, transport, targetTP)
			if err != nil {
				return Extraction{}, err
			}
			if err := appendFormats(tpFormats...); err != nil {
				return Extraction{}, err
			}
			continue
		}
		format, ok := strictHostedURLFormat(variantID, rawURL)
		if ok {
			if err := appendFormats(value.ObjectValue(format)); err != nil {
				return Extraction{}, err
			}
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "display_id", Value: value.String(target.displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	if desc, ok := asset["description"].(string); ok {
		hostedSetString(info, "description", desc)
	}
	if provider, ok := asset["providername"].(string); ok {
		hostedSetString(info, "uploader", provider)
	}
	if cc, ok := asset["cc_url"].(string); ok && strictValidHostedHTTPURL(cc) {
		info.Set("subtitles", value.ObjectValue(value.NewObject(
			value.Field{Key: target.locale[:2], Value: value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String(cc)},
			)))},
		)))
	}
	return Media(value.NewInfo(info)), nil
}

// NBCOlympics is a thin adapter that rewrites vplayer.nbcolympics.com player
// URLs onto player.theplatform.com and preserves URLResult semantics.
type NBCOlympics struct{}

func NewNBCOlympics() NBCOlympics { return NBCOlympics{} }
func (NBCOlympics) Name() string  { return "nbcolympics" }

func (NBCOlympics) Suitable(parsed *url.URL) bool {
	_, ok := parseNBCOlympicsURL(parsed)
	return ok
}

func (NBCOlympics) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseNBCOlympicsURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{
		URL:          target.thePlatformURL,
		ExtractorKey: "theplatform",
		ID:           target.videoID,
	})
}

type nbcOlympicsTarget struct {
	videoID, thePlatformURL string
}

func parseNBCOlympicsURL(parsed *url.URL) (nbcOlympicsTarget, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return nbcOlympicsTarget{}, false
	}
	if strings.ToLower(parsed.Hostname()) != "vplayer.nbcolympics.com" {
		return nbcOlympicsTarget{}, false
	}
	// Same path shape as player.theplatform.com/p/...
	rewritten := *parsed
	rewritten.Scheme = "https"
	rewritten.Host = "player.theplatform.com"
	rewritten.User = nil
	tp, ok := parseThePlatformURL(&rewritten)
	if !ok {
		return nbcOlympicsTarget{}, false
	}
	return nbcOlympicsTarget{videoID: tp.videoID, thePlatformURL: rewritten.String()}, true
}
