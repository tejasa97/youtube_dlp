package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

// Bluesky is a bounded, public-post-only extractor for the Bluesky/AT
// Protocol social graph. It is intentionally built on the unauthenticated
// public.app.bsky API surface. Login, private repositories, profile or
// feed enumeration, and arbitrary record types are out of scope.
type Bluesky struct{}

func NewBluesky() Bluesky    { return Bluesky{} }
func (Bluesky) Name() string { return "bluesky" }

const (
	blueskyMaxURLBytes       = 4096
	blueskyMaxHandleBytes    = 256
	blueskyMaxDIDBytes       = 2048
	blueskyMaxPostIDBytes    = 64
	blueskyMaxRecordText     = 32 << 10
	blueskyMaxTitleTruncate  = 72
	blueskyMaxEntries        = 8
	blueskyMaxCaptions       = 64
	blueskyMaxTags           = 64
	blueskyMaxLabelVals      = 64
	blueskyFallbackPDS       = "https://bsky.social"
	blueskyPDSDirectory      = "https://plc.directory"
	blueskyThreadEndpoint    = "https://public.api.bsky.app/xrpc/app.bsky.feed.getPostThread"
	blueskyUserAgent         = "ytdlp-go/bluesky"
	blueskyFormatHLS         = "hls"
	blueskyFormatBlob        = "blob"
	blueskyProtocolHLSNative = "m3u8_native"
	blueskyUndeterminedLang  = "und"
	blueskyProfilePathPrefix = "https://bsky.app/profile/"
)

var (
	blueskyHandlePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
	blueskyDIDPattern    = regexp.MustCompile(`^did:(plc|web):[A-Za-z0-9.\-_:]+$`)
	blueskyPostIDPattern = regexp.MustCompile(`^[a-z0-9]{11,32}$`)
	blueskyATPathPattern = regexp.MustCompile(`^/app\.bsky\.feed\.post/([A-Za-z0-9._~:-]{1,64})$`)
	blueskyBCP47Pattern  = regexp.MustCompile(`^[A-Za-z]{2,3}(?:-[A-Za-z0-9]{2,8})*$`)
)

var ErrBlueskyNetwork = errors.New("bluesky public API request failed")

type blueskyTarget struct {
	author     string
	postID     string
	atURI      string
	mediaIndex int
}

func (Bluesky) Suitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	_, _, ok := blueskyParse(parsed.String())
	return ok
}

func classifyBlueskyURL(parsed *url.URL) (blueskyTarget, bool) {
	if parsed == nil {
		return blueskyTarget{}, false
	}
	raw := parsed.String()
	if len(raw) == 0 || len(raw) > blueskyMaxURLBytes {
		return blueskyTarget{}, false
	}
	escaped := strings.ToLower(parsed.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") || strings.Contains(parsed.String(), "\x00") {
		return blueskyTarget{}, false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "at":
		return classifyBlueskyOpaque(parsed)
	case "http", "https":
		return classifyBlueskyWeb(parsed)
	default:
		return blueskyTarget{}, false
	}
}

func classifyBlueskyWeb(parsed *url.URL) (blueskyTarget, bool) {
	if parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return blueskyTarget{}, false
	}
	mediaIndex := 0
	if parsed.RawQuery != "" {
		query, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(query) != 1 || len(query["media"]) != 1 {
			return blueskyTarget{}, false
		}
		mediaIndex, err = strconv.Atoi(query.Get("media"))
		if err != nil || mediaIndex < 1 || mediaIndex > blueskyMaxEntries {
			return blueskyTarget{}, false
		}
	}
	host := strings.ToLower(parsed.Hostname())
	if !blueskyRecognizedWebHost(host) {
		return blueskyTarget{}, false
	}
	if net.ParseIP(host) != nil {
		return blueskyTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 4 || parts[0] != "profile" || parts[2] != "post" {
		return blueskyTarget{}, false
	}
	author := strings.ToLower(parts[1])
	postID := strings.ToLower(parts[3])
	if !blueskyValidAuthor(author) || !blueskyPostIDPattern.MatchString(postID) {
		return blueskyTarget{}, false
	}
	return blueskyTarget{author: author, postID: postID, atURI: "at://" + author + "/app.bsky.feed.post/" + postID, mediaIndex: mediaIndex}, true
}

func classifyBlueskyOpaque(parsed *url.URL) (blueskyTarget, bool) {
	if parsed == nil {
		return blueskyTarget{}, false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "at" {
		return blueskyTarget{}, false
	}
	original := parsed.String()
	if !strings.HasPrefix(strings.ToLower(original), "at://") {
		return blueskyTarget{}, false
	}
	body := original[len("at:"):]
	if len(body) < 2 || body[0] != '/' || body[1] != '/' {
		return blueskyTarget{}, false
	}
	body = body[2:]
	if strings.ContainsAny(body, "\x00") {
		return blueskyTarget{}, false
	}
	if strings.Contains(body, "%2f") || strings.Contains(body, "%5c") || strings.Contains(body, "%00") {
		return blueskyTarget{}, false
	}
	if decoded, err := url.PathUnescape(body); err == nil {
		body = decoded
	}
	parts := strings.SplitN(body, "/", 3)
	if len(parts) != 3 {
		return blueskyTarget{}, false
	}
	author := strings.ToLower(parts[0])
	collection := parts[1]
	postID := strings.ToLower(parts[2])
	if !blueskyValidAuthor(author) {
		return blueskyTarget{}, false
	}
	if collection != "app.bsky.feed.post" {
		return blueskyTarget{}, false
	}
	if !blueskyPostIDPattern.MatchString(postID) {
		return blueskyTarget{}, false
	}
	return blueskyTarget{author: author, postID: postID, atURI: "at://" + author + "/app.bsky.feed.post/" + postID}, true
}

// blueskyParse is the top-level entry-point parser. It returns the parsed
// URL (or nil for AT URIs which Go's url.Parse cannot represent) together
// with the routing target. The two-step dispatch lets the extractor
// recover from url.Parse's inability to represent at://did:plc:... URIs
// while keeping the Suitable() gate consistent.
func blueskyParse(rawURL string) (*url.URL, blueskyTarget, bool) {
	if len(rawURL) == 0 || len(rawURL) > blueskyMaxURLBytes {
		return nil, blueskyTarget{}, false
	}
	lower := strings.ToLower(rawURL)
	if strings.HasPrefix(lower, "at:") {
		// at:// URIs may include did:plc:... hosts which Go's url.Parse
		// rejects as a bad port. Parse the scheme through a private
		// helper that recovers the original at-uri verbatim.
		parsed, err := blueskyParseAT(rawURL)
		if err != nil {
			return nil, blueskyTarget{}, false
		}
		target, ok := classifyBlueskyOpaque(parsed)
		return parsed, target, ok
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, blueskyTarget{}, false
	}
	target, ok := classifyBlueskyURL(parsed)
	return parsed, target, ok
}

// blueskyParseAT decodes an at:// URL into a url.URL whose Host and Path
// match the original author and collection. The fake host is empty so the
// downstream dispatcher can recover the body via parsed.String().
func blueskyParseAT(rawURL string) (*url.URL, error) {
	if !strings.HasPrefix(strings.ToLower(rawURL), "at://") {
		return nil, fmt.Errorf("not an at uri")
	}
	body := rawURL[len("at:"):]
	if len(body) < 2 || body[0] != '/' || body[1] != '/' {
		return nil, fmt.Errorf("invalid at uri body")
	}
	body = body[2:]
	if strings.ContainsAny(body, "\x00\r\n") {
		return nil, fmt.Errorf("invalid at uri character")
	}
	parts := strings.SplitN(body, "/", 3)
	if len(parts) != 3 {
		return nil, fmt.Errorf("at uri must have 3 path parts")
	}
	author := parts[0]
	collection := parts[1]
	postID := parts[2]
	if strings.ContainsAny(author, "/?#") {
		return nil, fmt.Errorf("invalid at uri author")
	}
	u := &url.URL{
		Scheme: "at",
		Host:   author,
		Path:   "/" + collection + "/" + postID,
	}
	return u, nil
}

func blueskyRecognizedWebHost(host string) bool {
	switch host {
	case "bsky.app", "www.bsky.app", "main.bsky.dev":
		return true
	}
	return false
}

func blueskyValidAuthor(author string) bool {
	if len(author) == 0 || len(author) > blueskyMaxHandleBytes {
		return false
	}
	if strings.HasPrefix(author, "did:") {
		return blueskyDIDPattern.MatchString(author)
	}
	return blueskyHandlePattern.MatchString(author)
}

func (Bluesky) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, target, ok := blueskyParse(request.URL)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	_ = parsed

	thread, err := blueskyFetchThread(ctx, request.Transport, target)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}

	_, did, ok, err := blueskyResolveAuthor(ctx, request.Transport, target.author)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if !ok {
		return Extraction{}, fmt.Errorf("%w: actor handle did not resolve to a DID", ErrInvalidMetadata)
	}

	endpoint, err := blueskyResolveServiceEndpoint(ctx, request.Transport, did, target.postID)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}

	entries, err := blueskyCollectEntries(thread, target, did, endpoint)
	if err != nil {
		return Extraction{}, err
	}
	if target.mediaIndex != 0 {
		if target.mediaIndex > len(entries) {
			return Extraction{}, fmt.Errorf("%w: media index %d is unavailable", ErrUnavailable, target.mediaIndex)
		}
		return Media(entries[target.mediaIndex-1].info), nil
	}
	switch len(entries) {
	case 0:
		return Extraction{}, fmt.Errorf("%w: no media could be found in this post", ErrUnavailable)
	case 1:
		return Media(entries[0].info), nil
	default:
		playlistInfo := value.NewObject(
			value.Field{Key: "id", Value: value.String(target.postID)},
			value.Field{Key: "title", Value: value.String(blueskyTitle(entries[0].info, target.postID))},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)
		playlistItems := make([]Entry, 0, len(entries))
		for index, entry := range entries {
			item := Entry{URL: entry.entryURL, ID: entry.entryID, Title: blueskyTitle(entry.info, target.postID)}
			if !entry.external {
				item.URL = blueskyCanonicalPostURL(target) + "?media=" + strconv.Itoa(index+1)
				item.ExtractorKey = "bluesky"
				item.Transparent = true
			}
			playlistItems = append(playlistItems, item)
		}
		extraction, err := Playlist(value.NewInfo(playlistInfo), StaticEntries(playlistItems...))
		if err != nil {
			return Extraction{}, err
		}
		return extraction, nil
	}
}

type blueskyCollectedEntry struct {
	info     value.Info
	entryURL string
	entryID  string
	external bool
}

func blueskyCanonicalPostURL(target blueskyTarget) string {
	return blueskyProfilePath(target.author) + "/post/" + target.postID
}

func blueskyCollectEntries(thread blueskyThreadResponse, target blueskyTarget, did, endpoint string) ([]blueskyCollectedEntry, error) {
	post, err := blueskyExtractPost(thread)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	seenURLs := make(map[string]bool)
	var collected []blueskyCollectedEntry
	add := func(candidate blueskyCollectedEntry) bool {
		if candidate.entryID == "" || seen[candidate.entryID] {
			return false
		}
		if seenURLs[candidate.entryURL] {
			return false
		}
		seen[candidate.entryID] = true
		seenURLs[candidate.entryURL] = true
		collected = append(collected, candidate)
		return true
	}
	if entry, ok, err := blueskyBuildEntry(post, target, did, endpoint, "embed", "record", "embed"); err != nil {
		return nil, err
	} else if ok {
		add(entry)
	}
	if entry, ok, err := blueskyBuildEntry(post, target, did, endpoint, []embedPathSegment{{kind: embedPathKey, value: "embed"}, {kind: embedPathKey, value: "media"}}, "record", []embedPathSegment{{kind: embedPathKey, value: "embed"}, {kind: embedPathKey, value: "media"}}); err != nil {
		return nil, err
	} else if ok {
		add(entry)
	}
	if nested, ok := blueskyNestedPost(post); ok {
		if entry, ok, err := blueskyBuildEntry(nested, target, did, endpoint, "embed", "value", ""); err != nil {
			return nil, err
		} else if ok {
			add(entry)
		}
	}
	if len(collected) > blueskyMaxEntries {
		collected = collected[:blueskyMaxEntries]
	}
	return collected, nil
}

func blueskyNestedPost(post blueskyPost) (blueskyPost, bool) {
	record := post.Embed.Record
	if record == nil {
		return blueskyPost{}, false
	}
	// Mirrors the reference's `('record', None)` variadic by preferring
	// the unwrapped record and falling back to the wrapping view.
	if record.Record != nil {
		return *record.Record, true
	}
	if record.Value != nil {
		return *record.Value, true
	}
	// Fallback: the wrapping view itself carries the embeds array. Build
	// a synthetic post whose `embed` is the first video-shaped entry so
	// the default `embed_path='embed'` traversal succeeds.
	if len(record.Embeds) == 0 {
		return blueskyPost{}, false
	}
	return blueskyPost{Embed: record.Embeds[0]}, true
}

func blueskyFetchThread(ctx context.Context, transport Transport, target blueskyTarget) (blueskyThreadResponse, error) {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	headers.Set("User-Agent", blueskyUserAgent)
	endpoint := blueskyThreadEndpoint + "?uri=" + url.QueryEscape(target.atURI) + "&depth=0&parentHeight=0"
	var thread blueskyThreadResponse
	if err := RequestJSON(ctx, transport, http.MethodGet, endpoint, nil, headers, &thread); err != nil {
		return blueskyThreadResponse{}, blueskyCategorizeError(err)
	}
	return thread, nil
}

func blueskyExtractPost(thread blueskyThreadResponse) (blueskyPost, error) {
	if thread.Thread.Post == nil {
		return blueskyPost{}, fmt.Errorf("%w: thread has no post", ErrUnavailable)
	}
	post := *thread.Thread.Post
	if post.URI == "" {
		return blueskyPost{}, fmt.Errorf("%w: post URI is empty", ErrInvalidMetadata)
	}
	return post, nil
}

func blueskyResolveAuthor(ctx context.Context, transport Transport, author string) (string, string, bool, error) {
	if strings.HasPrefix(author, "did:") {
		return "", author, true, nil
	}
	endpoint := "https://public.api.bsky.app/xrpc/app.bsky.actor.resolveHandle?handle=" + url.QueryEscape(author)
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var resolved blueskyResolvedActor
	if err := RequestJSON(ctx, transport, http.MethodGet, endpoint, nil, headers, &resolved); err != nil {
		categorized := blueskyCategorizeError(err)
		if errors.Is(categorized, ErrAuthentication) || errors.Is(categorized, ErrUnavailable) {
			return "", "", false, nil
		}
		return "", "", false, categorized
	}
	if resolved.DID == "" {
		return "", "", false, nil
	}
	return "", resolved.DID, true, nil
}

func blueskyResolveServiceEndpoint(ctx context.Context, transport Transport, did, postID string) (string, error) {
	if did == "" {
		return blueskyFallbackPDS, nil
	}
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var didDoc string
	switch {
	case strings.HasPrefix(did, "did:plc:"):
		escaped := url.PathEscape(did)
		didDoc = blueskyPDSDirectory + "/" + escaped
	case strings.HasPrefix(did, "did:web:"):
		host := strings.TrimPrefix(did, "did:web:")
		if !validPublicPDSHost(host) {
			return "", fmt.Errorf("%w: did:web host is not publicly resolvable", ErrInvalidMetadata)
		}
		didDoc = "https://" + host + "/.well-known/did.json"
	default:
		return blueskyFallbackPDS, nil
	}
	var doc blueskyDIDDocument
	if err := RequestJSON(ctx, transport, http.MethodGet, didDoc, nil, headers, &doc); err != nil {
		if errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrInvalidMetadata) {
			return "", err
		}
		return blueskyFallbackPDS, nil
	}
	endpoint := blueskyTrustedPDSEndpoint(doc)
	if endpoint == "" {
		return blueskyFallbackPDS, nil
	}
	return endpoint, nil
}

func blueskyTrustedPDSEndpoint(doc blueskyDIDDocument) string {
	for _, service := range doc.Service {
		if service.Type != "AtprotoPersonalDataServer" {
			continue
		}
		raw := strings.TrimSpace(service.ServiceEndpoint)
		if raw == "" || len(raw) > blueskyMaxDIDBytes {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			continue
		}
		if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawQuery != "" {
			continue
		}
		host := strings.ToLower(parsed.Hostname())
		if !validPublicPDSHost(host) {
			continue
		}
		return "https://" + host
	}
	return ""
}

func validPublicPDSHost(host string) bool {
	if host == "" || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return false
	}
	labels := strings.Split(lower, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

func blueskyCategorizeError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return ErrBlueskyNetwork
		}
	}
	if errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidMetadata) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrBlueskyNetwork
}

type embedPathSegmentKind uint8

const (
	embedPathKey embedPathSegmentKind = iota
	embedPathIndex
)

type embedPathSegment struct {
	kind  embedPathSegmentKind
	value string
}

func blueskyBuildEntry(post blueskyPost, target blueskyTarget, did, endpoint string, embedPath, recordPath, recordSubpath any) (blueskyCollectedEntry, bool, error) {
	embed, ok := blueskyLookupEmbed(post, embedPath)
	if !ok {
		return blueskyCollectedEntry{}, false, nil
	}

	if external := strings.TrimSpace(embed.External.URI); external != "" {
		if safe, ok := blueskySafeMediaURL(external); ok {
			info := value.NewObject(
				value.Field{Key: "id", Value: value.String(target.postID)},
				value.Field{Key: "url", Value: value.String(safe)},
				value.Field{Key: "ext", Value: value.String("mp4")},
			)
			return blueskyCollectedEntry{info: value.NewInfo(info), entryURL: safe, entryID: target.postID + ":external:" + safe, external: true}, true, nil
		}
	}

	playlist := strings.TrimSpace(embed.Playlist)
	if playlist == "" {
		return blueskyCollectedEntry{}, false, nil
	}
	safePlaylist, ok := blueskySafeMediaURL(playlist)
	if !ok {
		return blueskyCollectedEntry{}, false, nil
	}

	record := blueskyLookupRecord(post, recordPath, nil)
	entryDID := did
	entryEndpoint := endpoint
	if blueskyEntryID(post, target.postID) != target.postID {
		nestedDID := strings.TrimSpace(post.Author.DID)
		if blueskyDIDPattern.MatchString(nestedDID) {
			entryDID = nestedDID
			if entryDID != did {
				entryEndpoint = blueskyFallbackPDS
			}
		}
	}
	videoCID := embed.CID
	if videoCID == "" {
		videoCID = blueskyStringAt(blueskyRawAt(record, blueskyPathSegments(recordSubpath)...), "video", "ref", "$link")
	}

	formats := []value.Value{value.ObjectValue(manifestFormat(blueskyFormatHLS, safePlaylist, blueskyProtocolHLSNative))}
	if entryDID != "" && videoCID != "" && entryEndpoint != "" {
		if blobURL, ok := blueskyBuildBlobURL(entryEndpoint, entryDID, videoCID); ok {
			blobFormat := value.NewObject(
				value.Field{Key: "format_id", Value: value.String(blueskyFormatBlob)},
				value.Field{Key: "url", Value: value.String(blobURL)},
				value.Field{Key: "ext", Value: value.String(blueskyExtForMIME(blueskyStringAt(record, "video", "mimeType")))},
				value.Field{Key: "protocol", Value: value.String("https")},
			)
			if width, height, ok := blueskyAspectDimensions(embed.AspectRatio); ok {
				hostedSetInt(blobFormat, "width", width)
				hostedSetInt(blobFormat, "height", height)
			}
			if size, ok := blueskyPositiveIntAt(record, "video", "size"); ok {
				hostedSetInt(blobFormat, "filesize", size)
			}
			formats = append(formats, value.ObjectValue(blobFormat))
		}
	}

	subtitles := blueskyBuildSubtitles(record, entryEndpoint, entryDID)
	info := blueskyBuildInfo(post, record, embed, target, entryDID, safePlaylist, formats, subtitles)
	return blueskyCollectedEntry{info: info, entryID: target.postID + ":video:" + safePlaylist, entryURL: safePlaylist}, true, nil
}

func blueskyLookupEmbed(post blueskyPost, embedPath any) (blueskyEmbedView, bool) {
	switch path := embedPath.(type) {
	case string:
		if path == "" {
			return blueskyEmbedView{}, false
		}
		if path == "embed" {
			return post.Embed, true
		}
		return blueskyEmbedView{}, false
	case []embedPathSegment:
		var current any = post
		for _, segment := range path {
			next, ok := blueskyEmbedStepSegment(current, segment)
			if !ok {
				return blueskyEmbedView{}, false
			}
			current = next
		}
		if view, ok := current.(blueskyEmbedView); ok {
			return view, true
		}
		return blueskyEmbedView{}, false
	}
	return blueskyEmbedView{}, false
}

func blueskyEmbedStepSegment(value any, segment embedPathSegment) (any, bool) {
	switch typed := value.(type) {
	case blueskyPost:
		if segment.kind == embedPathKey {
			switch segment.value {
			case "embed":
				return typed.Embed, true
			case "record":
				return typed.Record, true
			}
		}
	case blueskyEmbedView:
		if segment.kind == embedPathKey {
			switch segment.value {
			case "media":
				if typed.Media == nil {
					return nil, false
				}
				return *typed.Media, true
			case "embed":
				return typed, true
			}
		}
	case *blueskyRecordRef:
		if typed == nil {
			return nil, false
		}
		if segment.kind == embedPathKey && segment.value == "embeds" {
			return typed.Embeds, true
		}
		return blueskyEmbedStepSegment(*typed, segment)
	case *blueskyEmbedView:
		if typed == nil {
			return nil, false
		}
		return blueskyEmbedStepSegment(*typed, segment)
	case []blueskyEmbedView:
		if segment.kind == embedPathIndex {
			idx, err := strconv.Atoi(segment.value)
			if err != nil || idx < 0 || idx >= len(typed) {
				return nil, false
			}
			return typed[idx], true
		}
	}
	return nil, false
}

func blueskyLookupRecord(post blueskyPost, recordPath, recordSubpath any) json.RawMessage {
	raw, ok := blueskyRecordForPath(post, recordPath)
	if !ok {
		return nil
	}
	segments := blueskyPathSegments(recordSubpath)
	return blueskyRawAt(raw, segments...)
}

func blueskyRecordForPath(post blueskyPost, recordPath any) (json.RawMessage, bool) {
	switch path := recordPath.(type) {
	case string:
		switch path {
		case "":
			return nil, false
		case "record":
			return post.Record, len(post.Record) > 0
		case "value":
			if len(post.Record) > 0 {
				if wrapped := blueskyRawAt(post.Record, "value"); len(wrapped) > 0 {
					return wrapped, true
				}
				return post.Record, true
			}
			return nil, false
		}
	}
	return nil, false
}

func blueskyPathSegments(path any) []string {
	switch typed := path.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []string:
		return typed
	case []embedPathSegment:
		out := make([]string, 0, len(typed))
		for _, segment := range typed {
			if segment.kind == embedPathKey {
				out = append(out, segment.value)
			}
		}
		return out
	}
	return nil
}

func blueskyRawAt(raw json.RawMessage, segments ...string) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var current any
	if err := json.Unmarshal(raw, &current); err != nil {
		return nil
	}
	for _, segment := range segments {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		value, ok := object[segment]
		if !ok {
			return nil
		}
		current = value
	}
	data, err := json.Marshal(current)
	if err != nil {
		return nil
	}
	return data
}

func blueskyStringAt(raw json.RawMessage, segments ...string) string {
	data := blueskyRawAt(raw, segments...)
	if len(data) == 0 {
		return ""
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return ""
	}
	return text
}

func blueskyPositiveIntAt(raw json.RawMessage, segments ...string) (int64, bool) {
	data := blueskyRawAt(raw, segments...)
	if len(data) == 0 {
		return 0, false
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case float64:
		return int64(typed), true
	}
	return 0, false
}

func blueskyAspectDimensions(aspect blueskyAspectRatio) (int64, int64, bool) {
	if aspect.Width <= 0 || aspect.Height <= 0 {
		return 0, 0, false
	}
	if aspect.Width > 16384 || aspect.Height > 16384 {
		return 0, 0, false
	}
	return int64(aspect.Width), int64(aspect.Height), true
}

func blueskyExtForMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "video/quicktime":
		return "mov"
	case "application/x-mpegurl", "application/vnd.apple.mpegurl":
		return "mp4"
	}
	return "mp4"
}

func blueskyBuildSubtitles(record json.RawMessage, endpoint, did string) *value.Object {
	if len(record) == 0 || endpoint == "" || did == "" {
		return nil
	}
	data := blueskyRawAt(record, "captions")
	if len(data) == 0 {
		return nil
	}
	var entries []blueskyCaption
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil
	}
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > blueskyMaxCaptions {
		entries = entries[:blueskyMaxCaptions]
	}
	grouped := make(map[string][]value.Value)
	seen := make(map[string]bool)
	for _, caption := range entries {
		cid := strings.TrimSpace(caption.File.Ref.Link)
		if cid == "" || !blueskyPostIDPattern.MatchString(strings.ToLower(cid)) {
			continue
		}
		language := strings.TrimSpace(caption.Lang)
		if language == "" {
			language = blueskyUndeterminedLang
		}
		if !blueskyValidBCP47(language) {
			continue
		}
		blobURL, ok := blueskyBuildBlobURL(endpoint, did, cid)
		if !ok {
			continue
		}
		if seen[blobURL] {
			continue
		}
		seen[blobURL] = true
		entry := value.NewObject(
			value.Field{Key: "url", Value: value.String(blobURL)},
			value.Field{Key: "ext", Value: value.String(blueskyExtForTextMIME(caption.File.MimeType))},
		)
		grouped[language] = append(grouped[language], value.ObjectValue(entry))
	}
	if len(grouped) == 0 {
		return nil
	}
	languages := make([]string, 0, len(grouped))
	for language := range grouped {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	result := value.NewObject()
	for _, language := range languages {
		result.Set(language, value.List(grouped[language]...))
	}
	return result
}

func blueskyExtForTextMIME(mime string) string {
	mime = strings.ToLower(strings.TrimSpace(mime))
	switch mime {
	case "text/vtt":
		return "vtt"
	case "text/srt":
		return "srt"
	case "application/json", "application/ld+json":
		return "json"
	}
	return "vtt"
}

func blueskyValidBCP47(language string) bool {
	if len(language) == 0 || len(language) > 35 {
		return false
	}
	return blueskyBCP47Pattern.MatchString(language)
}

func blueskyBuildBlobURL(endpoint, did, cid string) (string, bool) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" {
		return "", false
	}
	if !validPublicPDSHost(strings.ToLower(parsed.Hostname())) {
		return "", false
	}
	if !blueskyPostIDPattern.MatchString(strings.ToLower(cid)) {
		return "", false
	}
	values := parsed.Query()
	values.Set("did", did)
	values.Set("cid", cid)
	parsed.RawQuery = values.Encode()
	parsed.Path = "/xrpc/com.atproto.sync.getBlob"
	parsed.Fragment = ""
	return parsed.String(), true
}

func blueskySafeMediaURL(raw string) (string, bool) {
	if len(raw) == 0 || len(raw) > blueskyMaxURLBytes {
		return "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.Host == "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if !validPublicPDSHost(host) {
		return "", false
	}
	return parsed.String(), true
}

func blueskyBuildInfo(post blueskyPost, record json.RawMessage, embed blueskyEmbedView, target blueskyTarget, did, safePlaylist string, formats []value.Value, subtitles *value.Object) value.Info {
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(blueskyEntryID(post, target.postID))},
		value.Field{Key: "display_id", Value: value.String(target.postID)},
		value.Field{Key: "title", Value: value.String(blueskyTitleFromRecord(record, target.postID))},
		value.Field{Key: "description", Value: value.String(blueskyRecordText(record))},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "webpage_url", Value: value.String(blueskyProfilePath(target.author) + "/post/" + target.postID)},
	)
	if safe, ok := blueskySafeMediaURL(embed.Thumbnail); ok {
		info.Set("thumbnail", value.String(safe))
	}
	if alt := strings.TrimSpace(embed.Alt); alt != "" && len(alt) <= 4096 {
		info.Set("alt_title", value.String(alt))
	}
	handle := strings.TrimSpace(post.Author.Handle)
	if !blueskyValidAuthor(strings.ToLower(handle)) {
		handle = target.author
	}
	if uploader := strings.TrimSpace(post.Author.DisplayName); uploader != "" {
		info.Set("uploader", value.String(uploader))
	} else {
		info.Set("uploader", value.String(handle))
	}
	info.Set("uploader_id", value.String(handle))
	info.Set("uploader_url", value.String(blueskyProfilePath(handle)))
	if did != "" {
		info.Set("channel_id", value.String(did))
		info.Set("channel_url", value.String(blueskyProfilePath(did)))
	}
	if like, ok := positiveInt64(post.LikeCount); ok {
		info.Set("like_count", value.Int(like))
	}
	if repost, ok := positiveInt64(post.RepostCount); ok {
		info.Set("repost_count", value.Int(repost))
	}
	if reply, ok := positiveInt64(post.ReplyCount); ok {
		info.Set("comment_count", value.Int(reply))
	}
	if timestamp, ok := blueskyTimestamp(post.IndexedAt); ok {
		info.Set("timestamp", value.Int(timestamp))
		if date := blueskyUploadDate(timestamp); date != "" {
			info.Set("upload_date", value.String(date))
		}
	}
	if tags, ok := blueskyOrderedTags(post.Labels); ok {
		info.Set("tags", value.List(tags...))
	}
	if limit := blueskyAgeLimit(post.Labels); limit > 0 {
		info.Set("age_limit", value.Int(int64(limit)))
	}
	if subtitles != nil {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	_ = safePlaylist
	return value.NewInfo(info)
}

func blueskyEntryID(post blueskyPost, fallback string) string {
	if uri := strings.TrimSpace(post.URI); uri != "" {
		if base := path.Base(uri); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return fallback
}

func blueskyRecordText(record json.RawMessage) string {
	text := blueskyStringAt(record, "text")
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if len(text) > blueskyMaxRecordText {
		text = text[:blueskyMaxRecordText]
	}
	return text
}

func blueskyTitleFromRecord(record json.RawMessage, fallbackID string) string {
	text := blueskyRecordText(record)
	if text == "" {
		return "Bluesky video #" + fallbackID
	}
	text = strings.ReplaceAll(text, "\n", " ")
	text = strings.TrimSpace(text)
	if text == "" {
		return "Bluesky video #" + fallbackID
	}
	if runes := []rune(text); len(runes) > blueskyMaxTitleTruncate {
		text = string(runes[:blueskyMaxTitleTruncate])
	}
	return text
}

func blueskyTitle(info value.Info, fallbackID string) string {
	if title, ok := info.Title(); ok && title != "" {
		return title
	}
	return "Bluesky video #" + fallbackID
}

func positiveInt64(value float64) (int64, bool) {
	if value <= 0 || value > float64(1<<53) {
		return 0, false
	}
	return int64(value), true
}

func blueskyTimestamp(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		parsed, err = time.Parse(time.RFC3339, raw)
		if err != nil {
			return 0, false
		}
	}
	return parsed.Unix(), true
}

func blueskyUploadDate(timestamp int64) string {
	if timestamp <= 0 {
		return ""
	}
	t := time.Unix(timestamp, 0).UTC()
	return t.Format("20060102")
}

func blueskyOrderedTags(labels []blueskyLabel) ([]value.Value, bool) {
	if len(labels) == 0 {
		return nil, false
	}
	if len(labels) > blueskyMaxLabelVals {
		labels = labels[:blueskyMaxLabelVals]
	}
	seen := make(map[string]bool)
	values := make([]value.Value, 0, len(labels))
	for _, label := range labels {
		val := strings.TrimSpace(label.Val)
		if val == "" || seen[val] || len(val) > 256 {
			continue
		}
		seen[val] = true
		values = append(values, value.String(val))
		if len(values) >= blueskyMaxTags {
			break
		}
	}
	if len(values) == 0 {
		return nil, false
	}
	return values, true
}

func blueskyAgeLimit(labels []blueskyLabel) int {
	for _, label := range labels {
		switch label.Val {
		case "sexual", "porn", "graphic-media":
			return 18
		}
	}
	return 0
}

func blueskyProfilePath(author string) string {
	author = strings.TrimSpace(author)
	if author == "" {
		return blueskyProfilePathPrefix
	}
	return blueskyProfilePathPrefix + author
}

type blueskyThreadResponse struct {
	Thread struct {
		Post *blueskyPost `json:"post"`
	} `json:"thread"`
}

type blueskyPost struct {
	URI         string           `json:"uri"`
	CID         string           `json:"cid"`
	Author      blueskyAuthor    `json:"author"`
	IndexedAt   string           `json:"indexedAt"`
	LikeCount   float64          `json:"likeCount"`
	RepostCount float64          `json:"repostCount"`
	ReplyCount  float64          `json:"replyCount"`
	Record      json.RawMessage  `json:"record"`
	Embed       blueskyEmbedView `json:"embed"`
	Labels      []blueskyLabel   `json:"labels"`
}

type blueskyAuthor struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

type blueskyEmbedView struct {
	Playlist    string             `json:"playlist"`
	Thumbnail   string             `json:"thumbnail"`
	Alt         string             `json:"alt"`
	CID         string             `json:"cid"`
	AspectRatio blueskyAspectRatio `json:"aspectRatio"`
	External    blueskyExternal    `json:"external"`
	Record      *blueskyRecordRef  `json:"record"`
	Media       *blueskyEmbedView  `json:"media"`
}

type blueskyAspectRatio struct {
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type blueskyExternal struct {
	URI         string `json:"uri"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Thumb       string `json:"thumbnail"`
}

type blueskyRecordRef struct {
	CID    string             `json:"cid"`
	URI    string             `json:"uri"`
	Record *blueskyPost       `json:"record"`
	Value  *blueskyPost       `json:"value"`
	Embeds []blueskyEmbedView `json:"embeds"`
}

type blueskyLabel struct {
	Val string `json:"val"`
	Src string `json:"src"`
	Cts string `json:"cts"`
}

type blueskyCaption struct {
	Lang string             `json:"lang"`
	File blueskyCaptionFile `json:"file"`
}

type blueskyCaptionFile struct {
	Ref      blueskyStrongRef `json:"ref"`
	MimeType string           `json:"mimeType"`
}

type blueskyStrongRef struct {
	Link string `json:"$link"`
}

type blueskyResolvedActor struct {
	DID         string `json:"did"`
	Handle      string `json:"handle"`
	DisplayName string `json:"displayName"`
}

type blueskyDIDDocument struct {
	ID      string              `json:"id"`
	Service []blueskyDIDService `json:"service"`
}

type blueskyDIDService struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	ServiceEndpoint string `json:"serviceEndpoint"`
}
