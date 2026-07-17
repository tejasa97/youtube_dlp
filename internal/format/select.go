// Package format implements media-format selection.
package format

import (
	"errors"
	"fmt"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var ErrNoFormats = errors.New("no downloadable formats")

type Selection struct {
	ID       string
	URL      string
	Ext      string
	Filesize int64
	Protocol string
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
		selection := Selection{URL: rawURL}
		selection.ID, _ = object.Lookup("format_id").StringValue()
		selection.Ext, _ = object.Lookup("ext").StringValue()
		selection.Filesize, _ = object.Lookup("filesize").Int()
		selection.Protocol, _ = object.Lookup("protocol").StringValue()
		return selection, nil
	}
	return Selection{}, fmt.Errorf("%w: formats contain no URL", ErrNoFormats)
}
