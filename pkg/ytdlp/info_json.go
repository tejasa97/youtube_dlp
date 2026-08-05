package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	cookiesnapshot "github.com/tejasa97/youtube_dlp/internal/cookies/snapshot"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

var ErrInvalidInfoJSON = errors.New("invalid info JSON")

const (
	maxLoadedInfoJSONBytes = 32 << 20
	maxLoadedInfoJSONDepth = 64
	maxLoadedInfoJSONNodes = 100_000
	maxLoadedInfoString    = 1 << 20
)

var loadedInfoPathFields = map[string]struct{}{
	"filepath": {}, "filename": {}, "_filename": {}, "infojson_filename": {},
}

var loadedInfoCredentialFields = map[string]struct{}{
	"cookie": {}, "cookies": {}, "authorization": {}, "proxy-authorization": {},
}

var loadedInfoURLFields = map[string]struct{}{
	"url": {}, "webpage_url": {}, "manifest_url": {}, "fragment_base_url": {}, "thumbnail": {},
}

func loadInfoJSON(ctx context.Context, filename string) (value.Info, error) {
	return loadInfoJSONWithOpen(ctx, filename, cookiesnapshot.OpenReadOnlyNoFollow)
}

func loadInfoJSONWithOpen(ctx context.Context, filename string, open func(string) (*os.File, error)) (value.Info, error) {
	if err := ctx.Err(); err != nil {
		return value.Info{}, err
	}
	if filename == "" || filepath.Clean(filename) != filename || strings.ContainsRune(filename, 0) {
		return value.Info{}, fmt.Errorf("%w: unsafe path", ErrInvalidInfoJSON)
	}
	fileInfo, err := os.Lstat(filename)
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: inspect file", ErrInvalidInfoJSON)
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() {
		return value.Info{}, fmt.Errorf("%w: file must be a regular non-symlink", ErrInvalidInfoJSON)
	}
	if fileInfo.Size() < 0 || fileInfo.Size() > maxLoadedInfoJSONBytes {
		return value.Info{}, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalidInfoJSON, maxLoadedInfoJSONBytes)
	}
	file, err := open(filename)
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: open file", ErrInvalidInfoJSON)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: inspect opened file", ErrInvalidInfoJSON)
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(fileInfo, openedInfo) {
		return value.Info{}, fmt.Errorf("%w: file changed while opening", ErrInvalidInfoJSON)
	}
	data, err := readLoadedInfoBytes(ctx, file)
	if err != nil {
		return value.Info{}, err
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: inspect opened file after reading", ErrInvalidInfoJSON)
	}
	pathAfter, err := os.Lstat(filename)
	if err != nil {
		return value.Info{}, fmt.Errorf("%w: inspect file after reading", ErrInvalidInfoJSON)
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(openedInfo, openedAfter) || !os.SameFile(openedAfter, pathAfter) ||
		openedInfo.Size() != openedAfter.Size() || !openedInfo.ModTime().Equal(openedAfter.ModTime()) {
		return value.Info{}, fmt.Errorf("%w: file changed while reading", ErrInvalidInfoJSON)
	}
	var decoded value.Value
	if err := json.Unmarshal(data, &decoded); err != nil {
		return value.Info{}, fmt.Errorf("%w: decode JSON", ErrInvalidInfoJSON)
	}
	object, ok := decoded.Object()
	if !ok || object == nil {
		return value.Info{}, fmt.Errorf("%w: root must be an object", ErrInvalidInfoJSON)
	}
	if err := validateLoadedInfoValue(ctx, decoded, 0, new(int)); err != nil {
		return value.Info{}, err
	}
	cleaned, err := sanitizeLoadedInfoObject(ctx, object, 0)
	if err != nil {
		return value.Info{}, err
	}
	info := value.NewInfo(cleaned)
	typeName, _ := info.Lookup("_type").StringValue()
	if typeName != "" && typeName != "video" {
		return value.Info{}, fmt.Errorf("%w: only single-video metadata is supported", ErrInvalidInfoJSON)
	}
	if id, ok := info.ID(); !ok || id == "" {
		return value.Info{}, fmt.Errorf("%w: missing string id", ErrInvalidInfoJSON)
	}
	if title, ok := info.Title(); !ok || title == "" {
		return value.Info{}, fmt.Errorf("%w: missing string title", ErrInvalidInfoJSON)
	}
	if formats, ok := info.Formats(); !ok || len(formats) == 0 {
		if rawURL, ok := info.Lookup("url").StringValue(); !ok || rawURL == "" {
			return value.Info{}, fmt.Errorf("%w: missing media URL or formats", ErrInvalidInfoJSON)
		}
	}
	return info, nil
}

func readLoadedInfoBytes(ctx context.Context, reader io.Reader) ([]byte, error) {
	data := make([]byte, 0, 64<<10)
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			if len(data) > maxLoadedInfoJSONBytes-read {
				return nil, fmt.Errorf("%w: file exceeds %d bytes", ErrInvalidInfoJSON, maxLoadedInfoJSONBytes)
			}
			data = append(data, buffer[:read]...)
		}
		if errors.Is(err, io.EOF) {
			return data, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%w: read file", ErrInvalidInfoJSON)
		}
	}
}

func validateLoadedInfoValue(ctx context.Context, item value.Value, depth int, nodes *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	*nodes++
	if *nodes > maxLoadedInfoJSONNodes {
		return fmt.Errorf("%w: metadata node limit exceeded", ErrInvalidInfoJSON)
	}
	if depth > maxLoadedInfoJSONDepth {
		return fmt.Errorf("%w: metadata depth limit exceeded", ErrInvalidInfoJSON)
	}
	switch item.Kind() {
	case value.KindString:
		text, _ := item.StringValue()
		if len(text) > maxLoadedInfoString || !utf8.ValidString(text) {
			return fmt.Errorf("%w: metadata string limit exceeded", ErrInvalidInfoJSON)
		}
	case value.KindList:
		items, _ := item.ListValue()
		for _, child := range items {
			if err := validateLoadedInfoValue(ctx, child, depth+1, nodes); err != nil {
				return err
			}
		}
	case value.KindObject:
		object, _ := item.Object()
		for _, field := range object.Fields() {
			if len(field.Key) > 256 || !utf8.ValidString(field.Key) {
				return fmt.Errorf("%w: metadata field name limit exceeded", ErrInvalidInfoJSON)
			}
			if err := validateLoadedInfoValue(ctx, field.Value, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeLoadedInfoObject(ctx context.Context, input *value.Object, depth int) (*value.Object, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth > maxLoadedInfoJSONDepth {
		return nil, fmt.Errorf("%w: metadata depth limit exceeded", ErrInvalidInfoJSON)
	}
	output := value.NewObject()
	for _, field := range input.Fields() {
		keyLower := strings.ToLower(field.Key)
		if strings.HasPrefix(field.Key, "__") {
			continue
		}
		if _, forbidden := loadedInfoPathFields[field.Key]; forbidden {
			return nil, fmt.Errorf("%w: path field %q is not accepted", ErrInvalidInfoJSON, field.Key)
		}
		if _, forbidden := loadedInfoCredentialFields[keyLower]; forbidden {
			return nil, fmt.Errorf("%w: credential field %q is not accepted", ErrInvalidInfoJSON, field.Key)
		}
		if keyLower == "http_headers" {
			headers, ok := field.Value.Object()
			if !ok || headers == nil {
				return nil, fmt.Errorf("%w: http_headers must be an object", ErrInvalidInfoJSON)
			}
			cleanHeaders := value.NewObject()
			for _, header := range headers.Fields() {
				name := strings.ToLower(header.Key)
				if _, forbidden := loadedInfoCredentialFields[name]; forbidden {
					continue
				}
				text, ok := header.Value.StringValue()
				if !ok || strings.ContainsAny(text, "\x00\r\n") {
					return nil, fmt.Errorf("%w: invalid HTTP header", ErrInvalidInfoJSON)
				}
				cleanHeaders.Set(header.Key, value.String(text))
			}
			output.Set(field.Key, value.ObjectValue(cleanHeaders))
			continue
		}
		if _, isURL := loadedInfoURLFields[keyLower]; isURL {
			text, ok := field.Value.StringValue()
			if !ok || text == "" || !safeLoadedInfoURL(text) {
				return nil, fmt.Errorf("%w: invalid %s", ErrInvalidInfoJSON, field.Key)
			}
		}
		cleaned, err := sanitizeLoadedInfoValue(ctx, field.Value, depth+1)
		if err != nil {
			return nil, err
		}
		output.Set(field.Key, cleaned)
	}
	return output, nil
}

func sanitizeLoadedInfoValue(ctx context.Context, item value.Value, depth int) (value.Value, error) {
	if err := ctx.Err(); err != nil {
		return value.Missing(), err
	}
	switch item.Kind() {
	case value.KindObject:
		object, _ := item.Object()
		cleaned, err := sanitizeLoadedInfoObject(ctx, object, depth)
		if err != nil {
			return value.Missing(), err
		}
		return value.ObjectValue(cleaned), nil
	case value.KindList:
		items, _ := item.ListValue()
		cleaned := make([]value.Value, len(items))
		for index, child := range items {
			var err error
			cleaned[index], err = sanitizeLoadedInfoValue(ctx, child, depth+1)
			if err != nil {
				return value.Missing(), err
			}
		}
		return value.List(cleaned...), nil
	default:
		return item.Clone(), nil
	}
}

func safeLoadedInfoURL(raw string) bool {
	if len(raw) == 0 || len(raw) > 16<<10 || strings.ContainsAny(raw, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(raw)
	return err == nil && parsed.User == nil && parsed.Hostname() != "" && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
