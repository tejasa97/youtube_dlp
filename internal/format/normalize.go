// Package format implements media-format selection.
package format

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	maxNormalizedEntries = 4096
	maxNormalizedIDBytes = 16 << 10
	maxNormalizedTotal   = 4 << 20
)

var formatNumericFields = [...]string{
	"width", "height", "asr", "audio_channels", "fps",
	"tbr", "abr", "vbr", "filesize", "filesize_approx",
	"timestamp", "release_timestamp", "available_at", "duration",
	"view_count", "like_count", "dislike_count", "repost_count", "save_count",
	"average_rating", "comment_count", "age_limit", "start_time", "end_time",
	"chapter_number", "season_number", "episode_number", "track_number",
	"disc_number", "release_year",
	// The pinned sorter consumes these numeric extractor preferences before the
	// user-visible sort fields even though they are not in YoutubeDL._NUMERIC_FIELDS.
	"preference", "language_preference", "quality", "source_preference",
}

// normalizedFormat pairs a canonical cloned format with its original extractor
// list index. Index is its position in the canonical filtered and sorted list.
type normalizedFormat struct {
	Object *value.Object
	Source int
	Index  int
	ID     string
}

// Prepared is one canonical defensive format view shared by product metadata,
// listing, printing, JSON encoding, and selector evaluation.
type Prepared struct {
	info    value.Info
	formats []normalizedFormat
	options Options
}

// Prepare clones and canonicalizes extractor format metadata without mutating
// the extractor-owned Info.
func Prepare(info value.Info, options Options) (Prepared, error) {
	return prepareFormats(info, options)
}

// Info returns the canonical defensive metadata view owned by prepared.
func (prepared Prepared) Info() value.Info { return prepared.info }

func prepareFormats(info value.Info, options Options) (Prepared, error) {
	if err := options.validate(); err != nil {
		return Prepared{}, err
	}
	canonical := value.NewInfo(info.Fields().Clone())
	formatsValue := canonical.Lookup("formats")
	implicitFormat := formatsValue.IsMissing() || formatsValue.IsNull()
	var rawFormats []value.Value
	if implicitFormat {
		rawFormats = []value.Value{value.ObjectValue(canonical.Fields().Clone())}
	} else {
		var ok bool
		rawFormats, ok = formatsValue.ListValue()
		if !ok {
			return Prepared{}, fmt.Errorf("%w: formats field is not a list", ErrInvalidFormats)
		}
		if len(rawFormats) == 0 {
			return Prepared{}, ErrNoFormats
		}
	}
	if len(rawFormats) > maxNormalizedEntries {
		return Prepared{}, fmt.Errorf("%w: %d entries exceed %d", ErrFormatLimit, len(rawFormats), maxNormalizedEntries)
	}

	// Pinned yt-dlp removes disallowed DRM and missing/empty-URL formats before
	// sorting and before generated IDs consume list indexes.
	filtered := make([]normalizedFormat, 0, len(rawFormats))
	for sourceIndex, item := range rawFormats {
		object, ok := item.Object()
		if !ok || object == nil {
			return Prepared{}, fmt.Errorf("%w: format %d is not an object", ErrInvalidFormats, sourceIndex)
		}
		if !options.AllowDRM && isDRM(object) {
			continue
		}
		if !normalizeFormatURL(object) {
			continue
		}
		if err := coerceFormatFields(object); err != nil {
			return Prepared{}, fmt.Errorf("format %d: %w", sourceIndex, err)
		}
		filtered = append(filtered, normalizedFormat{Object: object, Source: sourceIndex})
	}
	if len(filtered) == 0 {
		if implicitFormat {
			return Prepared{info: canonical, options: options}, nil
		}
		return Prepared{}, ErrNoFormats
	}

	orderedObjects := make([]*value.Object, len(filtered))
	byObject := make(map[*value.Object]normalizedFormat, len(filtered))
	for index, item := range filtered {
		orderedObjects[index] = item.Object
		byObject[item.Object] = item
	}
	orderedObjects = orderFormats(orderedObjects, options)
	ordered := make([]normalizedFormat, len(orderedObjects))
	for index, object := range orderedObjects {
		item := byObject[object]
		item.Index = index
		ordered[index] = item
	}

	groups := make(map[string][]int, len(ordered))
	for index := range ordered {
		text, present, err := rawFormatIDText(ordered[index].Object.Lookup("format_id"))
		if err != nil {
			return Prepared{}, err
		}
		if !present || text == "" {
			text = strconv.Itoa(index)
		}
		if len(text) > maxNormalizedIDBytes {
			return Prepared{}, fmt.Errorf("%w: format_id exceeds %d bytes", ErrFormatLimit, maxNormalizedIDBytes)
		}
		transformed := sanitizeFormatID(text)
		if len(transformed) > maxNormalizedIDBytes {
			return Prepared{}, fmt.Errorf("%w: format_id exceeds %d bytes", ErrFormatLimit, maxNormalizedIDBytes)
		}
		ordered[index].ID = transformed
		ordered[index].Object.Set("format_id", value.String(transformed))
		groups[transformed] = append(groups[transformed], index)
	}
	for _, members := range groups {
		if len(members) <= 1 {
			continue
		}
		for ordinal, member := range members {
			suffixed := ordered[member].ID + "-" + strconv.Itoa(ordinal)
			if len(suffixed) > maxNormalizedIDBytes {
				return Prepared{}, fmt.Errorf("%w: format_id exceeds %d bytes", ErrFormatLimit, maxNormalizedIDBytes)
			}
			ordered[member].ID = suffixed
			ordered[member].Object.Set("format_id", value.String(suffixed))
		}
	}
	for index := range ordered {
		current := ordered[index].ID
		if isExtensionSelector(current) && current != lookupExt(ordered[index].Object) {
			next := "f" + current
			if len(next) > maxNormalizedIDBytes {
				return Prepared{}, fmt.Errorf("%w: format_id exceeds %d bytes", ErrFormatLimit, maxNormalizedIDBytes)
			}
			ordered[index].ID = next
			ordered[index].Object.Set("format_id", value.String(next))
		}
	}
	totalBytes := 0
	canonicalValues := make([]value.Value, len(ordered))
	for index := range ordered {
		totalBytes += len(ordered[index].ID)
		if totalBytes > maxNormalizedTotal {
			return Prepared{}, fmt.Errorf("%w: normalized IDs exceed %d total bytes", ErrFormatLimit, maxNormalizedTotal)
		}
		canonicalValues[index] = value.ObjectValue(ordered[index].Object)
	}
	if implicitFormat {
		canonical.Fields().Merge(ordered[0].Object, true)
	} else {
		canonical.Set("formats", value.List(canonicalValues...))
	}
	return Prepared{info: canonical, formats: ordered, options: options}, nil
}

// normalizeFormatURL performs the pinned pre-sort well-formedness gate. Bytes
// are decoded as upstream's string coercion does; absent, empty, and unsupported
// URL values are excluded from the canonical list.
func normalizeFormatURL(object *value.Object) bool {
	if sabr, _ := object.Lookup("_youtube_sabr").Bool(); sabr {
		serverURL, _ := object.Lookup("_youtube_sabr_server_url").StringValue()
		if serverURL != "" {
			return true
		}
	}
	raw := object.Lookup("url")
	if text, ok := raw.StringValue(); ok {
		return text != ""
	}
	if bytes, ok := raw.BytesValue(); ok {
		if len(bytes) == 0 || !utf8.Valid(bytes) {
			return false
		}
		object.Set("url", value.String(string(bytes)))
		return true
	}
	return false
}

func coerceFormatFields(object *value.Object) error {
	rawID := object.Lookup("format_id")
	if !rawID.IsMissing() && !rawID.IsNull() && rawID.Kind() != value.KindString {
		text, err := pythonStringValue(rawID)
		if err != nil {
			return fmt.Errorf("%w: format_id: %v", ErrInvalidFormats, err)
		}
		if len(text) > maxNormalizedIDBytes {
			return fmt.Errorf("%w: format_id exceeds %d bytes", ErrFormatLimit, maxNormalizedIDBytes)
		}
		object.Set("format_id", value.String(text))
	}
	for _, field := range formatNumericFields {
		raw := object.Lookup(field)
		if raw.IsMissing() || raw.IsNull() {
			continue
		}
		switch raw.Kind() {
		case value.KindInt, value.KindFloat, value.KindBool:
			continue
		case value.KindString:
			text, _ := raw.StringValue()
			integer, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				object.Set(field, value.Null())
				continue
			}
			object.Set(field, value.Int(integer))
		default:
			object.Set(field, value.Null())
		}
	}
	return nil
}

func pythonStringValue(raw value.Value) (string, error) {
	switch raw.Kind() {
	case value.KindString:
		text, _ := raw.StringValue()
		return text, nil
	case value.KindInt:
		integer, _ := raw.Int()
		return strconv.FormatInt(integer, 10), nil
	case value.KindFloat:
		floating, _ := raw.Float()
		switch {
		case math.IsNaN(floating):
			return "nan", nil
		case math.IsInf(floating, 1):
			return "inf", nil
		case math.IsInf(floating, -1):
			return "-inf", nil
		}
		text := strconv.FormatFloat(floating, 'g', -1, 64)
		if !strings.ContainsAny(text, ".eE") {
			text += ".0"
		}
		return text, nil
	case value.KindBool:
		boolean, _ := raw.Bool()
		if boolean {
			return "True", nil
		}
		return "False", nil
	case value.KindBytes:
		bytes, _ := raw.BytesValue()
		return pythonBytesString(bytes), nil
	default:
		return "", fmt.Errorf("unsupported %s value", raw.Kind())
	}
}

func pythonBytesString(input []byte) string {
	var builder strings.Builder
	builder.WriteString("b'")
	for _, current := range input {
		switch current {
		case '\\', '\'':
			builder.WriteByte('\\')
			builder.WriteByte(current)
		case '\t':
			builder.WriteString(`\t`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		default:
			if current >= 0x20 && current < 0x7f {
				builder.WriteByte(current)
			} else {
				fmt.Fprintf(&builder, `\x%02x`, current)
			}
		}
	}
	builder.WriteByte('\'')
	return builder.String()
}

func rawFormatIDText(raw value.Value) (string, bool, error) {
	if raw.IsMissing() || raw.IsNull() {
		return "", false, nil
	}
	text, ok := raw.StringValue()
	if !ok {
		return "", false, fmt.Errorf("%w: format_id coercion did not produce a string", ErrInvalidFormats)
	}
	return text, true, nil
}

func sanitizeFormatID(id string) string {
	out := make([]rune, 0, len(id))
	for _, r := range id {
		if unicode.IsSpace(r) {
			out = append(out, '_')
			continue
		}
		switch r {
		case ',', '/', '+', '[', ']', '(', ')':
			out = append(out, '_')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}

func lookupExt(object *value.Object) string {
	text, _ := object.Lookup("ext").StringValue()
	return text
}
