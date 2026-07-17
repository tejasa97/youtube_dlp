package extractor

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type Generic struct{}

func NewGeneric() Generic { return Generic{} }

func (Generic) Name() string           { return "generic" }
func (Generic) Suitable(*url.URL) bool { return true }

func (Generic) Extract(ctx context.Context, request Request) (value.Info, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}
	httpRequest, _ := http.NewRequest(http.MethodHead, request.URL, nil)
	response, err := request.Transport.Do(ctx, httpRequest)
	if err != nil {
		return value.Info{}, err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return value.Info{}, fmt.Errorf("%w: HTTP status %d", ErrUnsupported, response.StatusCode)
	}
	mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if !isDirectMediaType(mediaType) {
		return value.Info{}, fmt.Errorf("%w: content type %q is not direct media", ErrUnsupported, mediaType)
	}

	base := path.Base(parsed.Path)
	if base == "." || base == "/" || base == "" {
		base = "download"
	}
	extension := strings.TrimPrefix(path.Ext(base), ".")
	protocol := protocolForMediaType(mediaType)
	if protocol != "" {
		extension = "mp4"
	} else if extension == "" {
		extension = extensionForMediaType(mediaType)
	}
	title := strings.TrimSuffix(base, path.Ext(base))
	if title == "" {
		title = "download"
	}

	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("direct-http")},
		value.Field{Key: "url", Value: value.String(request.URL)},
		value.Field{Key: "ext", Value: value.String(extension)},
	)
	if protocol != "" {
		format.Set("protocol", value.String(protocol))
	}
	if response.ContentLength >= 0 {
		format.Set("filesize", value.Int(response.ContentLength))
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(title)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "http_headers", Value: value.ObjectValue(headersValue(response.Request.Header))},
	)
	return value.NewInfo(info), nil
}

func isDirectMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") || mediaType == "application/octet-stream" || protocolForMediaType(mediaType) != ""
}

func protocolForMediaType(mediaType string) string {
	switch mediaType {
	case "application/vnd.apple.mpegurl", "application/x-mpegurl":
		return "m3u8_native"
	case "application/dash+xml":
		return "http_dash_segments"
	default:
		return ""
	}
}

func extensionForMediaType(mediaType string) string {
	extensions, _ := mime.ExtensionsByType(mediaType)
	if len(extensions) > 0 {
		return strings.TrimPrefix(extensions[0], ".")
	}
	return "bin"
}

func headersValue(headers http.Header) *value.Object {
	object := value.NewObject()
	for key, entries := range headers {
		if len(entries) == 1 {
			object.Set(key, value.String(entries[0]))
		} else {
			values := make([]value.Value, len(entries))
			for index, entry := range entries {
				values[index] = value.String(entry)
			}
			object.Set(key, value.List(values...))
		}
	}
	return object
}
