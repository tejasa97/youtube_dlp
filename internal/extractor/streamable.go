package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Streamable extracts public videos through Streamable's anonymous metadata
// endpoint. Uploading, processing, and failed videos are reported as
// unavailable instead of returning an unusable media URL.
type Streamable struct{}

func NewStreamable() Streamable { return Streamable{} }
func (Streamable) Name() string { return "streamable" }

var streamableID = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)

func (Streamable) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" || !strings.EqualFold(u.Hostname(), "streamable.com") {
		return false
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	if len(parts) == 1 {
		return streamableID.MatchString(parts[0])
	}
	if len(parts) == 2 && (parts[0] == "e" || parts[0] == "s") {
		return streamableID.MatchString(parts[1])
	}
	if len(parts) == 3 && parts[0] == "s" {
		return streamableID.MatchString(parts[1]) && streamableID.MatchString(parts[2])
	}
	return false
}

func (Streamable) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewStreamable().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parts := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	id := parts[0]
	if parts[0] == "e" || parts[0] == "s" {
		id = parts[1]
	}
	body, _, err := request.Transport.ReadPage(ctx, "https://ajax.streamable.com/videos/"+url.PathEscape(id))
	if err != nil {
		return Extraction{}, err
	}
	if int64(len(body)) > maxExtractorJSONBytes {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	response, err := decodeStreamableResponse(body)
	if err != nil {
		return Extraction{}, err
	}
	return normalizeStreamable(id, request.URL, response)
}

func decodeStreamableResponse(body []byte) (streamableResponse, error) {
	var response streamableResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&response); err != nil {
		return streamableResponse{}, fmt.Errorf("%w: invalid Streamable response", ErrInvalidMetadata)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return streamableResponse{}, fmt.Errorf("%w: trailing Streamable response data", ErrInvalidMetadata)
	}
	return response, nil
}

type streamableResponse struct {
	Status       int         `json:"status"`
	Title        string      `json:"title"`
	RedditTitle  string      `json:"reddit_title"`
	Description  string      `json:"description"`
	ThumbnailURL string      `json:"thumbnail_url"`
	DateAdded    json.Number `json:"date_added"`
	Duration     json.Number `json:"duration"`
	Plays        json.Number `json:"plays"`
	Owner        struct {
		UserName string `json:"user_name"`
	} `json:"owner"`
	Files map[string]streamableFile `json:"files"`
}

type streamableFile struct {
	URL           string      `json:"url"`
	Width         json.Number `json:"width"`
	Height        json.Number `json:"height"`
	Size          json.Number `json:"size"`
	Framerate     json.Number `json:"framerate"`
	Bitrate       json.Number `json:"bitrate"`
	InputMetadata struct {
		VideoCodec string `json:"video_codec_name"`
		AudioCodec string `json:"audio_codec_name"`
	} `json:"input_metadata"`
}

func normalizeStreamable(id, webpage string, response streamableResponse) (Extraction, error) {
	if response.Status != 2 {
		return Extraction{}, fmt.Errorf("%w: Streamable video is uploading, processing, or failed", ErrUnavailable)
	}
	title := strings.TrimSpace(response.RedditTitle)
	if title == "" {
		title = strings.TrimSpace(response.Title)
	}
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing Streamable title", ErrInvalidMetadata)
	}
	keys := make([]string, 0, len(response.Files))
	for key := range response.Files {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	formats := make([]value.Value, 0, len(keys))
	for _, key := range keys {
		file := response.Files[key]
		rawURL := streamableAbsoluteURL(file.URL)
		if !validHTTPURL(rawURL) {
			continue
		}
		parsedURL, _ := url.Parse(rawURL)
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(key)},
			value.Field{Key: "url", Value: value.String(rawURL)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String(strings.ToLower(parsedURL.Scheme))},
		)
		setJSONPositiveInt(format, "width", file.Width)
		setJSONPositiveInt(format, "height", file.Height)
		setJSONPositiveInt(format, "filesize", file.Size)
		setJSONPositiveInt(format, "fps", file.Framerate)
		if bitrate, err := file.Bitrate.Float64(); err == nil && bitrate > 0 {
			format.Set("vbr", value.Float(bitrate/1000))
		}
		if codec := strings.TrimSpace(file.InputMetadata.VideoCodec); codec != "" {
			format.Set("vcodec", value.String(codec))
		}
		if codec := strings.TrimSpace(file.InputMetadata.AudioCodec); codec != "" {
			format.Set("acodec", value.String(codec))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: no public Streamable formats", ErrUnavailable)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(webpage)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	setString := func(key, raw string) {
		if raw = strings.TrimSpace(raw); raw != "" {
			info.Set(key, value.String(raw))
		}
	}
	setString("description", response.Description)
	if thumbnail := streamableAbsoluteURL(response.ThumbnailURL); validHTTPURL(thumbnail) {
		setString("thumbnail", thumbnail)
	}
	setString("uploader", response.Owner.UserName)
	setJSONPositiveFloat(info, "timestamp", response.DateAdded)
	setJSONPositiveFloat(info, "duration", response.Duration)
	setJSONPositiveInt(info, "view_count", response.Plays)
	return Media(value.NewInfo(info)), nil
}

func streamableAbsoluteURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func setJSONPositiveInt(object *value.Object, key string, number json.Number) {
	if parsed, err := number.Int64(); err == nil && parsed > 0 {
		object.Set(key, value.Int(parsed))
	}
}

func setJSONPositiveFloat(object *value.Object, key string, number json.Number) {
	if parsed, err := number.Float64(); err == nil && parsed > 0 {
		object.Set(key, value.Float(parsed))
	}
}
