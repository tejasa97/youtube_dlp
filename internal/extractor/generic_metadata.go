package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ytdlp-go/ytdlp/internal/value"
	xhtml "golang.org/x/net/html"
)

const (
	maxGenericMetadataCandidates = 256
	maxGenericJSONLDScripts      = 32
	maxGenericJSONLDBytes        = 512 << 10
	maxGenericJSONLDNodes        = 2048
	maxGenericJSONLDDepth        = 64
	maxGenericMetadataTitle      = 1024
	maxGenericMetadataText       = 8 << 10
	maxGenericMetadataTags       = 128
	maxGenericMetadataTagBytes   = 256
)

var genericISODuration = regexp.MustCompile(`^PT(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?$`)

type genericMetadataCandidate struct {
	rawURL      string
	mediaType   string
	kind        string
	title       string
	description string
	thumbnail   string
	duration    *float64
	uploader    string
	artist      string
	timestamp   *int64
	filesize    *int64
	bitrate     *float64
	width       *int64
	height      *int64
	viewCount   *int64
	tags        []string
}

type genericMetadataDocument struct {
	jsonLD      []genericMetadataCandidate
	twitter     []genericMetadataCandidate
	openGraph   []genericMetadataCandidate
	title       string
	description string
	thumbnail   string
	htmlTitle   strings.Builder
}

func discoverGenericMetadataMedia(ctx context.Context, pageURL *url.URL, page []byte) (Extraction, bool, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, false, err
	}
	if pageURL == nil || !NewGeneric().Suitable(pageURL) {
		return Extraction{}, false, fmt.Errorf("%w: invalid generic metadata base URL", ErrInvalidMetadata)
	}
	if len(page) > maxGenericHTMLBytes {
		return Extraction{}, false, fmt.Errorf("%w: generic HTML exceeds %d bytes", ErrInvalidMetadata, maxGenericHTMLBytes)
	}
	document, err := parseGenericMetadataDocument(ctx, page)
	if err != nil {
		return Extraction{}, false, err
	}
	for _, source := range []struct {
		name       string
		candidates []genericMetadataCandidate
	}{
		{name: "json-ld", candidates: document.jsonLD},
		{name: "twitter", candidates: document.twitter},
		{name: "open-graph", candidates: document.openGraph},
	} {
		extraction, found := genericMetadataExtraction(pageURL, document, source.name, source.candidates)
		if found {
			return extraction, true, nil
		}
	}
	return Extraction{}, false, nil
}

func parseGenericMetadataDocument(ctx context.Context, page []byte) (genericMetadataDocument, error) {
	var document genericMetadataDocument
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(page))
	var jsonLD strings.Builder
	inJSONLD, inTitle := false, false
	tokens, candidates, scripts := 0, 0, 0
	finishJSONLD := func() error {
		if !inJSONLD {
			return nil
		}
		inJSONLD = false
		if jsonLD.Len() > maxGenericJSONLDBytes {
			return fmt.Errorf("%w: generic JSON-LD script exceeds %d bytes", ErrInvalidMetadata, maxGenericJSONLDBytes)
		}
		candidates, err := genericJSONLDCandidates([]byte(jsonLD.String()))
		if err != nil {
			return err
		}
		document.jsonLD = append(document.jsonLD, candidates...)
		if len(document.jsonLD) > maxGenericMetadataCandidates {
			return fmt.Errorf("%w: generic JSON-LD media candidate limit exceeded", ErrPlaylistLimit)
		}
		jsonLD.Reset()
		return nil
	}
	var twitterType, openGraphVideoType, openGraphAudioType string
	for {
		tokenType := tokenizer.Next()
		if tokenType == xhtml.ErrorToken {
			if errors.Is(tokenizer.Err(), io.EOF) {
				if err := finishJSONLD(); err != nil {
					return document, err
				}
				return document, nil
			}
			return document, fmt.Errorf("%w: tokenize generic metadata HTML", ErrInvalidMetadata)
		}
		tokens++
		if tokens > maxGenericHTMLTokens {
			return document, fmt.Errorf("%w: generic HTML token limit exceeded", ErrInvalidMetadata)
		}
		if tokens%256 == 0 {
			if err := contextError(ctx); err != nil {
				return document, err
			}
		}
		token := tokenizer.Token()
		switch tokenType {
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			switch strings.ToLower(token.Data) {
			case "meta":
				kind, content := genericMetadataMeta(token)
				if kind == "" || content == "" {
					continue
				}
				switch kind {
				case "twitter:player:stream":
					candidates++
					document.twitter = append(document.twitter, genericMetadataCandidate{rawURL: content, mediaType: twitterType})
				case "twitter:player:stream:content_type":
					twitterType = content
					if index := len(document.twitter) - 1; index >= 0 {
						document.twitter[index].mediaType = content
					}
				case "og:video":
					candidates++
					document.openGraph = append(document.openGraph, genericMetadataCandidate{
						rawURL: content, mediaType: openGraphVideoType, kind: "video",
					})
				case "og:audio":
					candidates++
					document.openGraph = append(document.openGraph, genericMetadataCandidate{
						rawURL: content, mediaType: openGraphAudioType, kind: "audio",
					})
				case "og:video:type":
					openGraphVideoType = content
					for index := len(document.openGraph) - 1; index >= 0; index-- {
						if document.openGraph[index].kind == "video" {
							document.openGraph[index].mediaType = content
							break
						}
					}
				case "og:audio:type":
					openGraphAudioType = content
					for index := len(document.openGraph) - 1; index >= 0; index-- {
						if document.openGraph[index].kind == "audio" {
							document.openGraph[index].mediaType = content
							break
						}
					}
				case "og:title", "twitter:title":
					if document.title == "" {
						document.title = genericMetadataText(content, maxGenericMetadataTitle)
					}
				case "og:description", "twitter:description", "description":
					if document.description == "" {
						document.description = genericMetadataText(content, maxGenericMetadataText)
					}
				case "og:image", "twitter:image":
					if document.thumbnail == "" {
						document.thumbnail = strings.TrimSpace(content)
					}
				}
				if candidates > maxGenericMetadataCandidates {
					return document, fmt.Errorf("%w: generic metadata candidate limit exceeded", ErrPlaylistLimit)
				}
			case "script":
				if tokenType == xhtml.StartTagToken && genericJSONLDScript(token) {
					scripts++
					if scripts > maxGenericJSONLDScripts {
						return document, fmt.Errorf("%w: generic JSON-LD script limit exceeded", ErrPlaylistLimit)
					}
					inJSONLD = true
					jsonLD.Reset()
				}
			case "title":
				inTitle = tokenType == xhtml.StartTagToken
			}
		case xhtml.TextToken:
			if inJSONLD {
				if jsonLD.Len()+len(token.Data) > maxGenericJSONLDBytes {
					return document, fmt.Errorf("%w: generic JSON-LD script exceeds %d bytes", ErrInvalidMetadata, maxGenericJSONLDBytes)
				}
				jsonLD.WriteString(token.Data)
			} else if inTitle && document.htmlTitle.Len() <= maxGenericMetadataTitle {
				document.htmlTitle.WriteString(token.Data)
			}
		case xhtml.EndTagToken:
			switch strings.ToLower(token.Data) {
			case "script":
				if err := finishJSONLD(); err != nil {
					return document, err
				}
			case "title":
				inTitle = false
			}
		}
	}
}

func genericMetadataMeta(token xhtml.Token) (string, string) {
	var kind, content string
	for _, attribute := range token.Attr {
		switch strings.ToLower(attribute.Key) {
		case "name", "property":
			if kind == "" {
				kind = strings.ToLower(strings.TrimSpace(attribute.Val))
			}
		case "content", "value":
			if content == "" {
				content = strings.TrimSpace(attribute.Val)
			}
		}
	}
	return kind, content
}

func genericJSONLDScript(token xhtml.Token) bool {
	for _, attribute := range token.Attr {
		if strings.EqualFold(attribute.Key, "type") {
			mediaType, _, _ := mime.ParseMediaType(strings.TrimSpace(attribute.Val))
			return strings.EqualFold(mediaType, "application/ld+json")
		}
	}
	return false
}

func genericJSONLDCandidates(data []byte) ([]genericMetadataCandidate, error) {
	var root any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&root); err != nil {
		return nil, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, nil
	}
	type node struct {
		value    any
		depth    int
		topLevel bool
	}
	queue := []node{{value: root, topLevel: true}}
	result := make([]genericMetadataCandidate, 0)
	for visited := 0; len(queue) != 0; visited++ {
		if visited >= maxGenericJSONLDNodes {
			return nil, fmt.Errorf("%w: generic JSON-LD node limit exceeded", ErrInvalidMetadata)
		}
		current := queue[0]
		queue = queue[1:]
		if current.depth > maxGenericJSONLDDepth {
			return nil, fmt.Errorf("%w: generic JSON-LD depth limit exceeded", ErrInvalidMetadata)
		}
		switch item := current.value.(type) {
		case []any:
			for _, child := range item {
				queue = append(queue, node{value: child, depth: current.depth + 1, topLevel: current.topLevel})
			}
		case map[string]any:
			if current.topLevel {
				if _, hasContext := item["@context"]; !hasContext {
					continue
				}
			}
			if genericJSONLDType(item["@type"], "VideoObject", "AudioObject") {
				if candidate, ok := genericJSONLDCandidate(item); ok {
					if len(result) >= maxGenericMetadataCandidates {
						return nil, fmt.Errorf("%w: generic JSON-LD media candidate limit exceeded", ErrPlaylistLimit)
					}
					result = append(result, candidate)
				}
			}
			for _, child := range item {
				switch child.(type) {
				case []any, map[string]any:
					queue = append(queue, node{value: child, depth: current.depth + 1})
				}
			}
		}
	}
	return result, nil
}

func genericJSONLDType(raw any, expected ...string) bool {
	matches := func(value string) bool {
		for _, candidate := range expected {
			if value == candidate {
				return true
			}
		}
		return false
	}
	switch typed := raw.(type) {
	case string:
		return matches(typed)
	case []any:
		for _, item := range typed {
			if value, ok := item.(string); ok && matches(value) {
				return true
			}
		}
	}
	return false
}

func genericJSONLDCandidate(item map[string]any) (genericMetadataCandidate, bool) {
	rawURL, ok := item["contentUrl"].(string)
	if !ok || strings.TrimSpace(rawURL) == "" {
		return genericMetadataCandidate{}, false
	}
	candidate := genericMetadataCandidate{
		rawURL:      rawURL,
		mediaType:   genericJSONString(item["encodingFormat"]),
		title:       genericMetadataText(genericJSONString(item["name"]), maxGenericMetadataTitle),
		description: genericMetadataText(genericJSONString(item["description"]), maxGenericMetadataText),
		thumbnail:   genericJSONLDThumbnail(item),
		duration:    genericJSONLDDuration(item["duration"]),
		uploader:    genericJSONLDPersonName(item["author"]),
		artist:      genericJSONLDPersonName(item["byArtist"]),
		timestamp:   genericJSONLDTimestamp(item["uploadDate"]),
		filesize:    genericJSONLDInt(item["contentSize"], false),
		bitrate:     genericJSONLDFloat(item["bitrate"], false),
		width:       genericJSONLDInt(item["width"], false),
		height:      genericJSONLDInt(item["height"], false),
		viewCount:   genericJSONLDInt(item["interactionCount"], true),
		tags:        genericJSONLDTags(item["keywords"]),
	}
	if genericJSONLDType(item["@type"], "AudioObject") && !genericJSONLDType(item["@type"], "VideoObject") {
		candidate.kind = "audio"
	}
	return candidate, true
}

func genericJSONLDPersonName(raw any) string {
	switch item := raw.(type) {
	case string:
		return genericMetadataText(item, maxGenericMetadataTitle)
	case map[string]any:
		return genericMetadataText(genericJSONString(item["name"]), maxGenericMetadataTitle)
	default:
		return ""
	}
}

func genericJSONLDTimestamp(raw any) *int64 {
	text := genericJSONString(raw)
	if text == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		parsed, err := time.Parse(layout, text)
		if err == nil {
			timestamp := parsed.Unix()
			if timestamp >= 0 {
				return &timestamp
			}
			return nil
		}
	}
	return nil
}

func genericJSONLDInt(raw any, allowZero bool) *int64 {
	number := genericJSONLDNumber(raw)
	if number == "" {
		return nil
	}
	value, err := strconv.ParseInt(number, 10, 64)
	if err != nil || value < 0 || value == 0 && !allowZero {
		return nil
	}
	return &value
}

func genericJSONLDFloat(raw any, allowZero bool) *float64 {
	number := genericJSONLDNumber(raw)
	if number == "" {
		return nil
	}
	value, err := strconv.ParseFloat(number, 64)
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) || value < 0 || value == 0 && !allowZero {
		return nil
	}
	return &value
}

func genericJSONLDNumber(raw any) string {
	switch item := raw.(type) {
	case json.Number:
		return item.String()
	case string:
		return strings.TrimSpace(item)
	default:
		return ""
	}
}

func genericJSONLDTags(raw any) []string {
	var source []string
	switch item := raw.(type) {
	case string:
		source = strings.Split(item, ",")
	case []any:
		for _, value := range item {
			if text, ok := value.(string); ok {
				source = append(source, text)
			}
		}
	}
	tags := make([]string, 0, min(len(source), maxGenericMetadataTags))
	seen := make(map[string]struct{})
	for _, rawTag := range source {
		tag := genericMetadataText(rawTag, maxGenericMetadataTagBytes)
		if tag == "" {
			continue
		}
		if _, duplicate := seen[tag]; duplicate {
			continue
		}
		if len(tags) >= maxGenericMetadataTags {
			break
		}
		seen[tag] = struct{}{}
		tags = append(tags, tag)
	}
	return tags
}

func genericJSONString(raw any) string {
	value, _ := raw.(string)
	return strings.TrimSpace(value)
}

func genericJSONLDThumbnail(item map[string]any) string {
	for _, key := range []string{"thumbnailUrl", "thumbnailURL", "thumbnail_url"} {
		switch raw := item[key].(type) {
		case string:
			if strings.TrimSpace(raw) != "" {
				return strings.TrimSpace(raw)
			}
		case []any:
			for _, value := range raw {
				if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
					return strings.TrimSpace(text)
				}
			}
		}
	}
	return ""
}

func genericJSONLDDuration(raw any) *float64 {
	if number, ok := raw.(json.Number); ok {
		value, err := number.Float64()
		if err == nil && value >= 0 {
			return &value
		}
	}
	text, ok := raw.(string)
	if !ok {
		return nil
	}
	match := genericISODuration.FindStringSubmatch(strings.TrimSpace(text))
	if len(match) != 4 {
		return nil
	}
	var seconds float64
	for index, multiplier := range []float64{3600, 60, 1} {
		if match[index+1] == "" {
			continue
		}
		value, err := strconv.ParseFloat(match[index+1], 64)
		if err != nil {
			return nil
		}
		seconds += value * multiplier
	}
	return &seconds
}

func genericMetadataExtraction(
	pageURL *url.URL,
	document genericMetadataDocument,
	source string,
	candidates []genericMetadataCandidate,
) (Extraction, bool) {
	formats := make([]value.Value, 0, len(candidates))
	seen := make(map[string]struct{})
	var selected genericMetadataCandidate
	for _, candidate := range candidates {
		resolved, ok := genericMetadataURL(pageURL, candidate.rawURL)
		if !ok || resolved.String() == pageURL.String() {
			continue
		}
		extension, protocol, ok := genericMetadataMediaKind(resolved, candidate.mediaType)
		if !ok {
			continue
		}
		if _, duplicate := seen[resolved.String()]; duplicate {
			continue
		}
		seen[resolved.String()] = struct{}{}
		if len(formats) == 0 {
			selected = candidate
		}
		formatID := source
		if len(candidates) > 1 {
			formatID += "-" + strconv.Itoa(len(formats)+1)
		}
		headers := value.NewObject(value.Field{Key: "Referer", Value: value.String(pageURL.String())})
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String(formatID)},
			value.Field{Key: "url", Value: value.String(resolved.String())},
			value.Field{Key: "ext", Value: value.String(extension)},
			value.Field{Key: "http_headers", Value: value.ObjectValue(headers)},
		)
		if protocol != "" {
			format.Set("protocol", value.String(protocol))
		}
		mediaType, _, _ := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(candidate.mediaType)))
		if candidate.kind == "audio" || strings.HasPrefix(mediaType, "audio/") {
			format.Set("vcodec", value.String("none"))
		}
		genericSetJSONLDFormatFields(format, candidate)
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, false
	}
	id, fallbackTitle := genericPageIdentity(pageURL)
	title := firstNonEmpty(selected.title, document.title, genericMetadataText(document.htmlTitle.String(), maxGenericMetadataTitle), fallbackTitle)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(pageURL.String())},
		value.Field{Key: "direct", Value: value.Bool(true)},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Referer", Value: value.String(pageURL.String())},
		))},
	)
	if firstFormat, ok := formats[0].Object(); ok {
		if ext, exists := firstFormat.Lookup("ext").StringValue(); exists {
			info.Set("ext", value.String(ext))
		}
	}
	if description := firstNonEmpty(selected.description, document.description); description != "" {
		info.Set("description", value.String(description))
	}
	if thumbnail := firstNonEmpty(selected.thumbnail, document.thumbnail); thumbnail != "" {
		if resolved, ok := genericMetadataURL(pageURL, thumbnail); ok {
			info.Set("thumbnail", value.String(resolved.String()))
		}
	}
	if selected.duration != nil {
		info.Set("duration", value.Float(*selected.duration))
	}
	genericSetJSONLDInfoFields(info, selected)
	return Media(value.NewInfo(info)), true
}

func genericSetJSONLDFormatFields(format *value.Object, candidate genericMetadataCandidate) {
	if candidate.filesize != nil {
		format.Set("filesize", value.Int(*candidate.filesize))
	}
	if candidate.bitrate != nil {
		format.Set("tbr", value.Float(*candidate.bitrate))
	}
	if candidate.width != nil {
		format.Set("width", value.Int(*candidate.width))
	}
	if candidate.height != nil {
		format.Set("height", value.Int(*candidate.height))
	}
}

func genericSetJSONLDInfoFields(info *value.Object, candidate genericMetadataCandidate) {
	if candidate.uploader != "" {
		info.Set("uploader", value.String(candidate.uploader))
	}
	if candidate.artist != "" {
		info.Set("artist", value.String(candidate.artist))
	}
	if candidate.timestamp != nil {
		info.Set("timestamp", value.Int(*candidate.timestamp))
	}
	if candidate.filesize != nil {
		info.Set("filesize", value.Int(*candidate.filesize))
	}
	if candidate.bitrate != nil {
		info.Set("tbr", value.Float(*candidate.bitrate))
	}
	if candidate.width != nil {
		info.Set("width", value.Int(*candidate.width))
	}
	if candidate.height != nil {
		info.Set("height", value.Int(*candidate.height))
	}
	if candidate.viewCount != nil {
		info.Set("view_count", value.Int(*candidate.viewCount))
	}
	if len(candidate.tags) != 0 {
		tags := make([]value.Value, len(candidate.tags))
		for index, tag := range candidate.tags {
			tags[index] = value.String(tag)
		}
		info.Set("tags", value.List(tags...))
	}
}

func genericMetadataURL(pageURL *url.URL, raw string) (*url.URL, bool) {
	if len(raw) == 0 || len(raw) > maxGenericEmbedURLBytes || strings.IndexByte(raw, 0) >= 0 {
		return nil, false
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || reference.Scheme != "" && reference.Scheme != "http" && reference.Scheme != "https" {
		return nil, false
	}
	resolved := pageURL.ResolveReference(reference)
	if resolved == nil || (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" ||
		resolved.User != nil || resolved.Fragment != "" || len(resolved.String()) > maxGenericEmbedURLBytes {
		return nil, false
	}
	if strings.HasSuffix(strings.ToLower(resolved.Hostname()), ".") {
		return nil, false
	}
	if genericHasExplicitPort(resolved) {
		port, err := strconv.Atoi(resolved.Port())
		if err != nil || port < 1 || port > 65535 {
			return nil, false
		}
	}
	escaped := strings.ToLower(resolved.EscapedPath())
	if strings.Contains(escaped, "%00") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return nil, false
	}
	return resolved, true
}

func genericMetadataMediaKind(parsed *url.URL, rawMediaType string) (string, string, bool) {
	mediaType, _, _ := mime.ParseMediaType(strings.ToLower(strings.TrimSpace(rawMediaType)))
	protocol := protocolForMediaType(mediaType)
	extension := strings.ToLower(strings.TrimPrefix(path.Ext(parsed.Path), "."))
	if protocol == "" {
		switch extension {
		case "m3u8":
			protocol = "m3u8_native"
		case "mpd":
			protocol = "http_dash_segments"
		default:
			if strings.HasSuffix(strings.ToLower(parsed.Path), ".ism/manifest") {
				protocol = "ism"
			}
		}
	}
	if protocol != "" {
		return "mp4", protocol, true
	}
	if strings.HasPrefix(mediaType, "video/") || strings.HasPrefix(mediaType, "audio/") {
		if extension == "" || !genericDirectMediaExtension(extension) {
			extension = genericMetadataExtension(mediaType)
		}
		return extension, "", extension != ""
	}
	if genericDirectMediaExtension(extension) {
		return extension, "", true
	}
	return "", "", false
}

func genericDirectMediaExtension(extension string) bool {
	switch strings.ToLower(extension) {
	case "mp4", "m4v", "webm", "mov", "mkv", "avi", "flv", "ts",
		"mp3", "m4a", "aac", "ogg", "oga", "opus", "wav", "flac":
		return true
	default:
		return false
	}
}

func genericMetadataExtension(mediaType string) string {
	switch mediaType {
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "video/quicktime":
		return "mov"
	case "audio/mpeg":
		return "mp3"
	case "audio/mp4":
		return "m4a"
	case "audio/aac":
		return "aac"
	case "audio/ogg":
		return "ogg"
	case "audio/opus":
		return "opus"
	case "audio/wav", "audio/x-wav":
		return "wav"
	case "audio/flac":
		return "flac"
	default:
		return ""
	}
}

func genericMetadataText(input string, limit int) string {
	input = strings.TrimSpace(input)
	if input == "" || limit <= 0 {
		return ""
	}
	var builder strings.Builder
	for _, character := range input {
		size := utf8.RuneLen(character)
		if size < 0 || builder.Len()+size > limit {
			break
		}
		if unicode.IsControl(character) {
			if character == '\n' || character == '\r' || character == '\t' {
				builder.WriteByte(' ')
			}
			continue
		}
		builder.WriteRune(character)
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}
