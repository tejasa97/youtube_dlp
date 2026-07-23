package extractor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
	xhtml "golang.org/x/net/html"
)

const (
	maxGenericHTMLBytes       = 2 << 20
	maxGenericHTMLTokens      = 100_000
	maxGenericHTMLDepth       = 256
	maxGenericEmbedCandidates = 256
	maxGenericEmbeds          = 64
	maxGenericEmbedURLBytes   = 8 << 10
)

type Generic struct{}

func NewGeneric() Generic { return Generic{} }

func (Generic) Name() string { return "generic" }
func (Generic) Suitable(parsed *url.URL) bool {
	return parsed != nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}

func (Generic) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewGeneric().Suitable(parsed) {
		return Extraction{}, fmt.Errorf("%w: invalid generic URL", ErrUnsupported)
	}
	if request.Transport == nil {
		return Extraction{}, fmt.Errorf("%w: missing transport", ErrInvalidMetadata)
	}
	httpRequest, err := http.NewRequest(http.MethodHead, request.URL, nil)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid generic URL", ErrUnsupported)
	}
	response, err := request.Transport.Do(ctx, httpRequest)
	if err != nil {
		return Extraction{}, err
	}
	if response == nil {
		return Extraction{}, fmt.Errorf("%w: missing generic response", ErrInvalidMetadata)
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	if redirect := genericRedirectEntry(parsed, response); redirect != nil {
		return URLResult(*redirect)
	}
	if response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotImplemented {
		response = nil
	} else if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Extraction{}, fmt.Errorf("%w: HTTP status %d", ErrUnsupported, response.StatusCode)
	}
	mediaType := ""
	if response != nil {
		mediaType, _, _ = mime.ParseMediaType(response.Header.Get("Content-Type"))
	}
	if response != nil && isDirectMediaType(mediaType) {
		return genericDirectMedia(parsed, request.URL, mediaType, response), nil
	}

	page, pageURL, getResponse, err := readGenericPage(ctx, request, parsed)
	if err != nil {
		return Extraction{}, err
	}
	if redirect := genericRedirectEntry(parsed, getResponse); redirect != nil {
		return URLResult(*redirect)
	}
	getMediaType, _, _ := mime.ParseMediaType(getResponse.Header.Get("Content-Type"))
	detectedType, _, _ := mime.ParseMediaType(http.DetectContentType(page))
	if !isDirectMediaType(getMediaType) && !isGenericHTMLType(getMediaType) &&
		(isDirectMediaType(detectedType) || isGenericHTMLType(detectedType)) {
		getMediaType = detectedType
	}
	if getMediaType == "" {
		getMediaType = detectedType
	}
	if isDirectMediaType(getMediaType) {
		return genericDirectMedia(pageURL, pageURL.String(), getMediaType, getResponse), nil
	}
	if !isGenericHTMLType(getMediaType) {
		return Extraction{}, fmt.Errorf("%w: GET content type %q is neither direct media nor HTML", ErrUnsupported, getMediaType)
	}
	entries, err := discoverGenericEmbedEntries(ctx, pageURL, page)
	if err != nil {
		return Extraction{}, err
	}
	if len(entries) == 0 {
		metadata, found, err := discoverGenericMetadataMedia(ctx, pageURL, page)
		if err != nil {
			return Extraction{}, err
		}
		if found {
			return metadata, nil
		}
		return Extraction{}, fmt.Errorf("%w: no supported embeds or metadata media", ErrUnsupported)
	}
	id, title := genericPageIdentity(pageURL)
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(pageURL.String())},
	))
	if len(entries) == 1 {
		return URLResult(entries[0])
	}
	return Playlist(info, StaticEntries(entries...))
}

func genericDirectMedia(parsed *url.URL, rawURL, mediaType string, response *http.Response) Extraction {
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
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
	)
	if protocol != "" {
		format.Set("protocol", value.String(protocol))
	}
	if response.ContentLength >= 0 {
		format.Set("filesize", value.Int(response.ContentLength))
	}
	requestHeaders := make(http.Header)
	if response.Request != nil {
		requestHeaders = response.Request.Header
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(title)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "http_headers", Value: value.ObjectValue(headersValue(requestHeaders))},
	)
	return Media(value.NewInfo(info))
}

func isGenericHTMLType(mediaType string) bool {
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func readGenericPage(ctx context.Context, request Request, requestedURL *url.URL) ([]byte, *url.URL, *http.Response, error) {
	httpRequest, err := http.NewRequest(http.MethodGet, request.URL, nil)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: invalid generic URL", ErrUnsupported)
	}
	response, err := request.Transport.Do(ctx, httpRequest)
	if err != nil {
		return nil, nil, nil, err
	}
	if response == nil {
		return nil, nil, nil, fmt.Errorf("%w: missing generic response", ErrInvalidMetadata)
	}
	if response.Body == nil {
		return nil, nil, response, fmt.Errorf("%w: missing generic response body", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nil, response, fmt.Errorf("%w: HTTP status %d", ErrUnsupported, response.StatusCode)
	}
	prefix := make([]byte, 512)
	readBytes, readErr := io.ReadFull(response.Body, prefix)
	prefix = prefix[:readBytes]
	if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, nil, response, contextErr
		}
		return nil, nil, response, fmt.Errorf("read generic response: %w", readErr)
	}
	headerType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	detectedType, _, _ := mime.ParseMediaType(http.DetectContentType(prefix))
	if isDirectMediaType(headerType) || (!isGenericHTMLType(headerType) && isDirectMediaType(detectedType)) {
		return prefix, genericResponseURL(requestedURL, response), response, nil
	}
	if !isGenericHTMLType(headerType) && !isGenericHTMLType(detectedType) {
		return prefix, genericResponseURL(requestedURL, response), response, nil
	}
	reader := &io.LimitedReader{R: response.Body, N: maxGenericHTMLBytes - int64(len(prefix)) + 1}
	remainder, err := io.ReadAll(reader)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, nil, response, contextErr
		}
		return nil, nil, response, fmt.Errorf("read generic response: %w", err)
	}
	page := append(prefix, remainder...)
	if len(page) > maxGenericHTMLBytes {
		return nil, nil, response, fmt.Errorf("%w: generic response exceeds %d bytes", ErrInvalidMetadata, maxGenericHTMLBytes)
	}
	return page, genericResponseURL(requestedURL, response), response, nil
}

func genericResponseURL(requestedURL *url.URL, response *http.Response) *url.URL {
	pageURL := requestedURL
	if response.Request != nil && response.Request.URL != nil {
		candidate := response.Request.URL
		if NewGeneric().Suitable(candidate) {
			pageURL = candidate
		}
	}
	cloned := *pageURL
	cloned.Fragment = ""
	return &cloned
}

func genericRedirectEntry(requested *url.URL, response *http.Response) *Entry {
	if requested == nil || response == nil || response.Request == nil || response.Request.URL == nil {
		return nil
	}
	final := *response.Request.URL
	final.Fragment = ""
	initial := *requested
	initial.Fragment = ""
	if final.String() == initial.String() || !NewGeneric().Suitable(&final) {
		return nil
	}
	return &Entry{URL: final.String()}
}

// discoverGenericEmbedEntries examines only embed-bearing HTML attributes. It
// never follows an iframe, executes script, or treats an arbitrary link as
// media. Every returned entry has first passed a supported extractor's strict
// URL policy and has been reduced to that extractor's canonical target.
func discoverGenericEmbedEntries(ctx context.Context, pageURL *url.URL, page []byte) ([]Entry, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if pageURL == nil || !NewGeneric().Suitable(pageURL) {
		return nil, fmt.Errorf("%w: invalid generic HTML base URL", ErrInvalidMetadata)
	}
	if len(page) > maxGenericHTMLBytes {
		return nil, fmt.Errorf("%w: generic HTML exceeds %d bytes", ErrInvalidMetadata, maxGenericHTMLBytes)
	}

	tokenizer := xhtml.NewTokenizer(bytes.NewReader(page))
	entries := make([]Entry, 0)
	seen := make(map[string]struct{})
	stack := make([]string, 0, 32)
	tokens, candidates := 0, 0
	for {
		tokenType := tokenizer.Next()
		if tokenType == xhtml.ErrorToken {
			err := tokenizer.Err()
			if errors.Is(err, io.EOF) {
				return entries, nil
			}
			return nil, fmt.Errorf("%w: tokenize generic HTML", ErrInvalidMetadata)
		}
		tokens++
		if tokens > maxGenericHTMLTokens {
			return nil, fmt.Errorf("%w: generic HTML token limit exceeded", ErrInvalidMetadata)
		}
		if tokens%256 == 0 {
			if err := contextError(ctx); err != nil {
				return nil, err
			}
		}

		token := tokenizer.Token()
		switch {
		case tokenType == xhtml.StartTagToken && !genericVoidElement(token.Data):
			stack = append(stack, strings.ToLower(token.Data))
			if len(stack) > maxGenericHTMLDepth {
				return nil, fmt.Errorf("%w: generic HTML depth limit exceeded", ErrInvalidMetadata)
			}
		case tokenType == xhtml.EndTagToken && !genericVoidElement(token.Data):
			name := strings.ToLower(token.Data)
			found := -1
			for index := len(stack) - 1; index >= 0; index-- {
				if stack[index] == name {
					found = index
					break
				}
			}
			if found < 0 {
				return nil, fmt.Errorf("%w: mismatched generic HTML end tag", ErrInvalidMetadata)
			}
			stack = stack[:found]
		}
		if tokenType != xhtml.StartTagToken && tokenType != xhtml.SelfClosingTagToken {
			continue
		}
		rawURL, ok := genericEmbedAttribute(token)
		if !ok {
			continue
		}
		candidates++
		if candidates > maxGenericEmbedCandidates {
			return nil, fmt.Errorf("%w: generic embed candidate limit exceeded", ErrPlaylistLimit)
		}
		if len(rawURL) == 0 || len(rawURL) > maxGenericEmbedURLBytes || strings.IndexByte(rawURL, 0) >= 0 {
			return nil, fmt.Errorf("%w: invalid generic embed URL", ErrInvalidMetadata)
		}
		entry, ok := canonicalGenericEmbed(pageURL, rawURL)
		if !ok {
			continue
		}
		key := entry.ExtractorKey + "\x00" + entry.URL
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		if len(entries) >= maxGenericEmbeds {
			return nil, fmt.Errorf("%w: generic embed limit exceeded", ErrPlaylistLimit)
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
}

func genericVoidElement(name string) bool {
	switch strings.ToLower(name) {
	case "area", "base", "br", "col", "embed", "hr", "img", "input", "link", "meta", "param", "source", "track", "wbr":
		return true
	default:
		return false
	}
}

func genericEmbedAttribute(token xhtml.Token) (string, bool) {
	var attributeName string
	switch strings.ToLower(token.Data) {
	case "iframe", "embed":
		attributeName = "src"
	case "object":
		attributeName = "data"
	case "meta":
		var kind string
		for _, attribute := range token.Attr {
			switch strings.ToLower(attribute.Key) {
			case "name", "property":
				kind = strings.ToLower(strings.TrimSpace(attribute.Val))
			}
		}
		if kind != "twitter:player" {
			return "", false
		}
		attributeName = "content"
	default:
		return "", false
	}
	for _, attribute := range token.Attr {
		if strings.EqualFold(attribute.Key, attributeName) {
			return strings.TrimSpace(attribute.Val), true
		}
	}
	return "", false
}

func canonicalGenericEmbed(pageURL *url.URL, rawURL string) (Entry, bool) {
	reference, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || reference.Scheme != "" && reference.Scheme != "http" && reference.Scheme != "https" {
		return Entry{}, false
	}
	resolved := pageURL.ResolveReference(reference)
	if resolved == nil || (resolved.Scheme != "http" && resolved.Scheme != "https") || resolved.Hostname() == "" ||
		resolved.User != nil || genericHasExplicitPort(resolved) || resolved.Fragment != "" || len(resolved.String()) > maxGenericEmbedURLBytes {
		return Entry{}, false
	}
	host := strings.ToLower(resolved.Hostname())
	if strings.HasSuffix(host, ".") {
		return Entry{}, false
	}
	if escaped := strings.ToLower(resolved.EscapedPath()); strings.Contains(escaped, "%00") || strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return Entry{}, false
	}

	if target, ok := canonicalGenericYouTubeEmbed(resolved); ok {
		return target, true
	}
	if host == "player.vimeo.com" {
		if match := vimeoURLPattern.FindStringSubmatch(resolved.Path); len(match) == 2 && strings.HasPrefix(resolved.Path, "/video/") {
			return genericTransparentEntry("https://vimeo.com/"+match[1], "vimeo", match[1]), true
		}
	}
	if target, ok := parseBrightcoveURL(resolved); ok {
		return genericTransparentEntry(target.canonical, "brightcove", target.contentID), true
	}
	if target, ok := parseKalturaURL(resolved); ok {
		return genericTransparentEntry(target.canonical, "kaltura", firstNonEmpty(target.entryID, target.playlistID)), true
	}
	if id, canonical, ok := parseJWPlatformURL(resolved); ok {
		return genericTransparentEntry(canonical, "jwplatform", id), true
	}
	if target, ok := parseWistiaURL(resolved); ok {
		return genericTransparentEntry(target.canonical, "wistia", target.id), true
	}
	if id, canonical, ok := parseSproutVideoURL(resolved); ok {
		return genericTransparentEntry(canonical, "sproutvideo", id), true
	}
	if genericDailymotionEmbedURL(resolved) {
		id := dailymotionVideoID(resolved)
		return genericTransparentEntry("https://www.dailymotion.com/video/"+id, "dailymotion", id), true
	}
	if genericRumbleEmbedURL(resolved) {
		parts := strings.Split(strings.Trim(resolved.Path, "/"), "/")
		id := strings.TrimSuffix(parts[1], ".html")
		return genericTransparentEntry("https://rumble.com/embed/"+id, "rumble", id), true
	}
	if genericStreamableEmbedURL(resolved) {
		parts := strings.Split(strings.Trim(resolved.EscapedPath(), "/"), "/")
		return genericTransparentEntry("https://streamable.com/"+parts[1], "streamable", parts[1]), true
	}
	if target, ok := parsePeerTubeURL(resolved); ok && strings.Contains(resolved.EscapedPath(), "/videos/embed/") {
		return genericTransparentEntry(target.webpageURL(), "peertube", target.id), true
	}
	return Entry{}, false
}

func genericHasExplicitPort(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	host := parsed.Host
	if strings.HasPrefix(host, "[") {
		closeBracket := strings.LastIndex(host, "]")
		return closeBracket >= 0 && len(host) > closeBracket+1 && host[closeBracket+1] == ':'
	}
	return strings.Contains(host, ":")
}

func canonicalGenericYouTubeEmbed(parsed *url.URL) (Entry, bool) {
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com", "youtube-nocookie.com", "www.youtube-nocookie.com":
	default:
		return Entry{}, false
	}
	if !strings.HasPrefix(parsed.Path, "/embed/") || strings.Count(strings.Trim(parsed.Path, "/"), "/") != 1 {
		return Entry{}, false
	}
	target, err := parseYouTubeTarget(parsed.String())
	if err != nil {
		return Entry{}, false
	}
	query := url.Values{"v": []string{target.videoID}}
	if target.startSet && target.startTime != nil {
		query.Set("start", strconv.FormatFloat(*target.startTime, 'f', -1, 64))
	}
	if target.endSet && target.endTime != nil {
		query.Set("end", strconv.FormatFloat(*target.endTime, 'f', -1, 64))
	}
	return genericTransparentEntry("https://www.youtube.com/watch?"+query.Encode(), "youtube", target.videoID), true
}

func genericDailymotionEmbedURL(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	if host != "dailymotion.com" && !strings.HasSuffix(host, ".dailymotion.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return (len(parts) == 3 && parts[0] == "embed" && parts[1] == "video" && dailymotionVideoID(parsed) != "") ||
		(strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "player/") && dailymotionID.MatchString(parsed.Query().Get("video")))
}

func genericRumbleEmbedURL(parsed *url.URL) bool {
	if host := strings.ToLower(parsed.Hostname()); host != "rumble.com" && host != "www.rumble.com" {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	return len(parts) == 2 && parts[0] == "embed" && rumbleID.MatchString(strings.TrimSuffix(parts[1], ".html"))
}

func genericStreamableEmbedURL(parsed *url.URL) bool {
	if !strings.EqualFold(parsed.Hostname(), "streamable.com") {
		return false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	return len(parts) == 2 && parts[0] == "e" && streamableID.MatchString(parts[1])
}

func genericTransparentEntry(rawURL, extractorKey, id string) Entry {
	return Entry{URL: rawURL, ExtractorKey: extractorKey, ID: id, Transparent: true}
}

func genericPageIdentity(pageURL *url.URL) (string, string) {
	base := strings.TrimSuffix(path.Base(pageURL.Path), path.Ext(pageURL.Path))
	if base == "" || base == "." || base == "/" {
		base = pageURL.Hostname()
	}
	if base == "" {
		base = "embedded-media"
	}
	return base, base
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
	case "application/vnd.ms-sstr+xml", "text/xml+smoothstreaming":
		return "ism"
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
