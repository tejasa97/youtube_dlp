package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

// BandcampWeekly extracts public Bandcamp Weekly radio episodes through the
// documented player_data_web API. Stream URLs are emitted for direct download.
type BandcampWeekly struct{}

func NewBandcampWeekly() BandcampWeekly { return BandcampWeekly{} }
func (BandcampWeekly) Name() string     { return "bandcamp_weekly" }

func (BandcampWeekly) Suitable(u *url.URL) bool {
	_, ok := classifyBandcampWeeklyURL(u)
	return ok
}

func (BandcampWeekly) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyBandcampWeeklyURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	showID, err := strconv.ParseInt(target.showID, 10, 64)
	if err != nil || showID <= 0 {
		return Extraction{}, ErrUnsupported
	}
	body, err := json.Marshal(map[string]any{
		"item_id":   showID,
		"item_type": "radio",
	})
	if err != nil {
		return Extraction{}, ErrInvalidMetadata
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var payload bandcampWeeklyPayload
	if err := hostedRequestJSONWithoutCredentialsNoRedirect(ctx, request.Transport, http.MethodPost, bandcampWeeklyAPIURL, body, headers, &payload); err != nil {
		return Extraction{}, categorizeBandcampWeeklyError(err)
	}
	return normalizeBandcampWeekly(target, payload)
}

type bandcampWeeklyPayload struct {
	Tracklist bandcampWeeklyTracklist `json:"tracklist"`
}

type bandcampWeeklyTracklist struct {
	Title         string                 `json:"title"`
	Subtitle      string                 `json:"subtitle"`
	Description   string                 `json:"description"`
	Date          any                    `json:"date"`
	ImageID       string                 `json:"imageId"`
	CompiledTrack bandcampWeeklyCompiled `json:"compiledTrack"`
}

type bandcampWeeklyCompiled struct {
	StreamURL string  `json:"streamUrl"`
	Duration  float64 `json:"duration"`
}

func normalizeBandcampWeekly(target bandcampWeeklyTarget, payload bandcampWeeklyPayload) (Extraction, error) {
	show := payload.Tracklist
	streamURL, ok := bandcampSafeStreamURL(show.CompiledTrack.StreamURL)
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid Bandcamp Weekly stream URL", ErrInvalidMetadata)
	}
	formatID := bandcampFormatIDFromStreamURL(streamURL)
	encoding, bitrateStr, _ := strings.Cut(formatID, "-")
	if encoding == "" {
		encoding = "mp3"
	}
	series, _ := bandcampBoundedText(show.Subtitle)
	episode, _ := bandcampBoundedText(show.Title)
	description, _ := bandcampBoundedText(show.Description)
	releaseTimestamp := bandcampWeeklyTimestamp(show.Date)
	titleParts := make([]string, 0, 2)
	if series != "" {
		titleParts = append(titleParts, series)
	}
	if releaseTimestamp > 0 {
		titleParts = append(titleParts, time.Unix(releaseTimestamp, 0).UTC().Format("2006-01-02"))
	}
	title := strings.Join(titleParts, ", ")
	if title == "" {
		title = "Bandcamp Weekly " + target.showID
	}
	thumbnail, _ := bandcampSafeThumbnailURL(show.ImageID)
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(streamURL)},
		value.Field{Key: "ext", Value: value.String(encoding)},
		value.Field{Key: "vcodec", Value: value.String("none")},
		value.Field{Key: "acodec", Value: value.String(encoding)},
		value.Field{Key: "protocol", Value: value.String("https")},
	)
	if bitrate, err := strconv.ParseInt(bitrateStr, 10, 64); err == nil && bitrate > 0 {
		format.Set("abr", value.Int(bitrate))
	}
	format.Set("_credential_isolated", value.Bool(true))
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.showID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "episode_id", Value: value.String(target.showID)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "ext", Value: value.String(encoding)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
	)
	if episode != "" {
		info.Set("episode", value.String(episode))
	}
	if series != "" {
		info.Set("series", value.String(series))
	}
	if description != "" {
		info.Set("description", value.String(description))
	}
	if thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	if show.CompiledTrack.Duration > 0 {
		info.Set("duration", value.Float(show.CompiledTrack.Duration))
	}
	if releaseTimestamp > 0 {
		info.Set("release_timestamp", value.Int(releaseTimestamp))
		info.Set("release_date", value.String(time.Unix(releaseTimestamp, 0).UTC().Format("20060102")))
	}
	return Media(value.NewInfo(info)), nil
}

func bandcampWeeklyTimestamp(raw any) int64 {
	switch typed := raw.(type) {
	case string:
		return bandcampParseDateString(typed)
	case float64:
		if typed > 0 && typed < 1e12 {
			return int64(typed)
		}
	case json.Number:
		if number, err := typed.Int64(); err == nil && number > 0 {
			return number
		}
		return bandcampParseDateString(typed.String())
	}
	return 0
}

func bandcampParseDateString(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > 64 || !utf8.ValidString(raw) {
		return 0
	}
	if number, err := strconv.ParseInt(raw, 10, 64); err == nil && number > 0 {
		return number
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02", "20060102"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UTC().Unix()
		}
	}
	return 0
}

func categorizeBandcampWeeklyError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRegionRestricted) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return categorizeBandcampPageStatus(status.Code)
	}
	return ErrInvalidMetadata
}
