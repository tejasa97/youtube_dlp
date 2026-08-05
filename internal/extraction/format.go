package extraction

import "github.com/tejasa97/youtube_dlp/internal/value"

// ManifestFormat constructs the shared minimal manifest-backed format shape.
func ManifestFormat(id, rawURL, protocolName string) *value.Object {
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String(protocolName)},
	)
}
