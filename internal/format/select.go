// Package format implements media-format selection.
package format

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var (
	ErrNoFormats      = errors.New("no downloadable formats")
	ErrInvalidHeaders = errors.New("invalid format HTTP headers")
)

type Selection struct {
	ID       string
	URL      string
	Ext      string
	Filesize int64
	Protocol string
	VCodec   string
	ACodec   string
	Height   int64
	TBR      float64
	Headers  http.Header
}

// Default applies yt-dlp-style best-quality selection: prefer a video-only and
// audio-only pair, then a single combined format. Explicit user selectors
// remain authoritative.
func Default(info value.Info, options Options) ([]Selection, error) {
	selector := Selector{Alternatives: []Choice{
		{Terms: []Term{{Name: "bestvideo"}, {Name: "bestaudio"}}},
		{Terms: []Term{{Name: "best"}}},
	}}
	return SelectWithOptions(info, selector, options)
}

// Best selects the first normalized format. Phase 0 extractors order their
// formats best-first; richer selector syntax is intentionally deferred.
func Best(info value.Info) (Selection, error) {
	formats, ok := info.Formats()
	if !ok || len(formats) == 0 {
		return Selection{}, ErrNoFormats
	}
	for _, candidate := range formats {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		rawURL, ok := object.Lookup("url").StringValue()
		if !ok || rawURL == "" {
			continue
		}
		headers, err := mergeHeaders(info.Lookup("http_headers"), object.Lookup("http_headers"))
		if err != nil {
			return Selection{}, err
		}
		selection := Selection{URL: rawURL, Headers: headers}
		selection.ID, _ = object.Lookup("format_id").StringValue()
		selection.Ext, _ = object.Lookup("ext").StringValue()
		selection.Filesize, _ = object.Lookup("filesize").Int()
		selection.Protocol, _ = object.Lookup("protocol").StringValue()
		selection.VCodec, _ = object.Lookup("vcodec").StringValue()
		selection.ACodec, _ = object.Lookup("acodec").StringValue()
		selection.Height, _ = object.Lookup("height").Int()
		selection.TBR, _ = numeric(object.Lookup("tbr"))
		return selection, nil
	}
	return Selection{}, fmt.Errorf("%w: formats contain no URL", ErrNoFormats)
}

func mergeHeaders(values ...value.Value) (http.Header, error) {
	headers := make(http.Header)
	for _, candidate := range values {
		if candidate.IsMissing() || candidate.IsNull() {
			continue
		}
		object, ok := candidate.Object()
		if !ok {
			return nil, fmt.Errorf("%w: header collection is not an object", ErrInvalidHeaders)
		}
		for _, field := range object.Fields() {
			text, ok := field.Value.StringValue()
			name := http.CanonicalHeaderKey(field.Key)
			if !ok || name == "" || strings.ContainsAny(field.Key+text, "\r\n") {
				return nil, fmt.Errorf("%w: malformed field", ErrInvalidHeaders)
			}
			headers.Set(name, text)
		}
	}
	return headers, nil
}

func numeric(input value.Value) (float64, bool) {
	if integer, ok := input.Int(); ok {
		return float64(integer), true
	}
	return input.Float()
}
