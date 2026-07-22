package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	soundCloudAPIBase         = "https://api-v2.soundcloud.com/"
	soundCloudWebBase         = "https://soundcloud.com/"
	soundCloudMaxAssetBytes   = int64(4 << 20)
	soundCloudMaxTranscodings = 64
	soundCloudMaxPageEntries  = 200
	soundCloudMaxSetEntries   = 10_000
	soundCloudMaxURLBytes     = 8 << 10
	soundCloudMaxQueryParams  = 16
	soundCloudMaxQueryValue   = 1024
)

var (
	soundCloudSlugPattern      = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
	soundCloudSetSlugPattern   = regexp.MustCompile(`^[A-Za-z0-9_:-]+$`)
	soundCloudTokenPattern     = regexp.MustCompile(`^s-[A-Za-z0-9_-]+$`)
	soundCloudClientIDPattern  = regexp.MustCompile(`(?i)client_id\s*:\s*["']([0-9a-zA-Z]{32})["']`)
	soundCloudScriptPattern    = regexp.MustCompile(`(?i)<script[^>]+src=["']([^"']+)["']`)
	soundCloudCodecPattern     = regexp.MustCompile(`(?i)codecs=["']([^"']+)`)
	soundCloudABRPattern       = regexp.MustCompile(`(?i)(\d+)k(?:_|$)`)
	soundCloudStationIDPattern = regexp.MustCompile(`^soundcloud:track-stations:(\d+)$`)
	soundCloudTrackReserved    = map[string]bool{
		"tracks": true, "albums": true, "sets": true, "reposts": true,
		"likes": true, "spotlight": true, "comments": true,
		"stations": true, "recommended": true,
	}
	soundCloudRelations = map[string]bool{
		"recommended": true, "albums": true, "sets": true,
	}
)

type SoundCloud struct {
	mu       sync.Mutex
	clientID string
}

func NewSoundCloud() *SoundCloud { return &SoundCloud{} }

func (*SoundCloud) Name() string { return "soundcloud" }

func (*SoundCloud) Suitable(parsed *url.URL) bool {
	_, ok := classifySoundCloudURL(parsed)
	return ok
}

func (extractor *SoundCloud) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifySoundCloudURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	switch target.kind {
	case soundCloudTrackTarget:
		return extractor.extractTrack(ctx, request.Transport, target)
	case soundCloudSetTarget, soundCloudAPIPlaylistTarget:
		return extractor.extractSet(ctx, request.Transport, target)
	case soundCloudUserTracksTarget:
		return extractor.extractUserTracks(ctx, request.Transport, target)
	case soundCloudStationTarget:
		return extractor.extractStation(ctx, request.Transport, target)
	case soundCloudRelatedTarget:
		return extractor.extractRelated(ctx, request.Transport, target)
	default:
		return Extraction{}, ErrUnsupported
	}
}

type soundCloudTargetKind uint8

const (
	soundCloudTrackTarget soundCloudTargetKind = iota + 1
	soundCloudSetTarget
	soundCloudAPIPlaylistTarget
	soundCloudUserTracksTarget
	soundCloudStationTarget
	soundCloudRelatedTarget
)

type soundCloudTarget struct {
	kind         soundCloudTargetKind
	id           string
	canonical    string
	secretToken  string
	relation     string
	baseTrackURL string
	trackSlug    string // artist/track slug for related fallback title
}

func classifySoundCloudURL(parsed *url.URL) (soundCloudTarget, bool) {
	if parsed == nil || len(parsed.String()) > soundCloudMaxURLBytes || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Port() != "" || parsed.User != nil || soundCloudEncodedSeparators(parsed) {
		return soundCloudTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	trimmedPath := strings.Trim(parsed.Path, "/")
	segments := strings.Split(trimmedPath, "/")
	if trimmedPath == "" || strings.Contains(trimmedPath, "//") {
		return soundCloudTarget{}, false
	}
	secret := parsed.Query().Get("secret_token")
	if secret != "" && !soundCloudTokenPattern.MatchString(secret) {
		return soundCloudTarget{}, false
	}
	if len(secret) > 256 {
		return soundCloudTarget{}, false
	}
	for _, segment := range segments {
		if len(segment) > 256 {
			return soundCloudTarget{}, false
		}
	}
	switch host {
	case "soundcloud.com", "www.soundcloud.com", "m.soundcloud.com":
		if len(segments) == 4 && segments[0] == "stations" && segments[1] == "track" && soundCloudSlugPattern.MatchString(segments[2]) && soundCloudSlugPattern.MatchString(segments[3]) {
			return soundCloudTarget{kind: soundCloudStationTarget, canonical: soundCloudWebBase + strings.Join(segments, "/")}, true
		}
		if len(segments) == 2 && soundCloudSlugPattern.MatchString(segments[0]) && segments[1] == "tracks" {
			return soundCloudTarget{kind: soundCloudUserTracksTarget, canonical: soundCloudWebBase + segments[0] + "/tracks"}, true
		}
		if (len(segments) == 3 || len(segments) == 4) && soundCloudSlugPattern.MatchString(segments[0]) && segments[1] == "sets" && soundCloudSetSlugPattern.MatchString(segments[2]) {
			if len(segments) == 4 {
				if !soundCloudTokenPattern.MatchString(segments[3]) {
					return soundCloudTarget{}, false
				}
				secret = segments[3]
			}
			return soundCloudTarget{kind: soundCloudSetTarget, canonical: soundCloudWebBase + strings.Join(segments, "/"), secretToken: secret}, true
		}
		if len(segments) == 3 && soundCloudSlugPattern.MatchString(segments[0]) && soundCloudSlugPattern.MatchString(segments[1]) && !soundCloudTrackReserved[segments[1]] && soundCloudRelations[segments[2]] {
			return soundCloudTarget{
				kind:         soundCloudRelatedTarget,
				relation:     segments[2],
				canonical:    soundCloudWebBase + strings.Join(segments, "/"),
				baseTrackURL: soundCloudWebBase + segments[0] + "/" + segments[1],
				trackSlug:    segments[0] + "/" + segments[1],
			}, true
		}
		if (len(segments) == 2 || len(segments) == 3) && soundCloudSlugPattern.MatchString(segments[0]) && soundCloudSlugPattern.MatchString(segments[1]) && !soundCloudTrackReserved[segments[1]] {
			if len(segments) == 3 {
				if !soundCloudTokenPattern.MatchString(segments[2]) {
					return soundCloudTarget{}, false
				}
				secret = segments[2]
			}
			return soundCloudTarget{kind: soundCloudTrackTarget, canonical: soundCloudWebBase + strings.Join(segments, "/"), secretToken: secret}, true
		}
	case "api.soundcloud.com", "api-v2.soundcloud.com":
		if len(segments) != 2 {
			return soundCloudTarget{}, false
		}
		identifier := soundCloudNumericID(segments[1])
		if identifier == "" {
			return soundCloudTarget{}, false
		}
		switch segments[0] {
		case "tracks":
			return soundCloudTarget{kind: soundCloudTrackTarget, id: identifier, secretToken: secret}, true
		case "playlists":
			return soundCloudTarget{kind: soundCloudAPIPlaylistTarget, id: identifier, secretToken: secret}, true
		}
	}
	return soundCloudTarget{}, false
}

func soundCloudEncodedSeparators(parsed *url.URL) bool {
	escaped := strings.ToLower(parsed.EscapedPath())
	return strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") || strings.Contains(escaped, "%00") || strings.Contains(parsed.String(), "\x00")
}

func soundCloudNumericID(input string) string {
	if index := strings.LastIndex(input, ":"); index >= 0 {
		input = input[index+1:]
	}
	if input == "" || len(input) > 20 {
		return ""
	}
	for _, character := range input {
		if character < '0' || character > '9' {
			return ""
		}
	}
	parsed, err := strconv.ParseUint(input, 10, 64)
	if err != nil || parsed == 0 {
		return ""
	}
	return input
}

func (extractor *SoundCloud) extractTrack(ctx context.Context, transport Transport, target soundCloudTarget) (Extraction, error) {
	endpoint := soundCloudAPIBase + "resolve?url=" + url.QueryEscape(target.canonical)
	if target.id != "" {
		endpoint = soundCloudAPIBase + "tracks/" + target.id
	}
	endpoint = addSoundCloudQuery(endpoint, "secret_token", target.secretToken)
	var track soundCloudTrack
	if err := extractor.requestJSON(ctx, transport, endpoint, &track); err != nil {
		return Extraction{}, err
	}
	return extractor.normalizeTrack(ctx, transport, track, target.secretToken)
}

func (extractor *SoundCloud) extractSet(ctx context.Context, transport Transport, target soundCloudTarget) (Extraction, error) {
	endpoint := soundCloudAPIBase + "resolve?url=" + url.QueryEscape(target.canonical)
	if target.kind == soundCloudAPIPlaylistTarget {
		endpoint = soundCloudAPIBase + "playlists/" + target.id
	}
	endpoint = addSoundCloudQuery(endpoint, "secret_token", target.secretToken)
	var playlist soundCloudPlaylist
	if err := extractor.requestJSON(ctx, transport, endpoint, &playlist); err != nil {
		return Extraction{}, err
	}
	if !validSoundCloudJSONID(playlist.ID) || strings.TrimSpace(playlist.Title) == "" || playlist.Tracks == nil || len(playlist.Tracks) > soundCloudMaxSetEntries {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud playlist", ErrInvalidMetadata)
	}
	entries := make([]Entry, 0, len(playlist.Tracks))
	for _, track := range playlist.Tracks {
		entry, ok := soundCloudTrackEntry(track, target.secretToken)
		if ok {
			entries = append(entries, entry)
		}
	}
	info := soundCloudPlaylistInfo(playlist)
	return Playlist(info, StaticEntries(entries...))
}

func (extractor *SoundCloud) extractUserTracks(ctx context.Context, transport Transport, target soundCloudTarget) (Extraction, error) {
	var user soundCloudUser
	endpoint := soundCloudAPIBase + "resolve?url=" + url.QueryEscape(target.canonical)
	if err := extractor.requestJSON(ctx, transport, endpoint, &user); err != nil {
		return Extraction{}, err
	}
	if !validSoundCloudJSONID(user.ID) || strings.TrimSpace(user.Username) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud user", ErrInvalidMetadata)
	}
	apiPath := "users/" + user.ID.String() + "/tracks"
	firstURL := soundCloudAPIBase + apiPath + "?linked_partitioning=1&limit=200"
	policy := soundCloudContinuationPolicy{allowedPath: "/" + apiPath}
	sequence, err := ContinuationEntries(nil, firstURL, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return extractor.fetchCollectionPage(ctx, transport, cursor, policy)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(user.ID.String())},
		value.Field{Key: "title", Value: value.String(user.Username + " (Tracks)")},
		value.Field{Key: "uploader", Value: value.String(user.Username)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func (extractor *SoundCloud) fetchCollectionPage(ctx context.Context, transport Transport, cursor string, policy soundCloudContinuationPolicy) ([]Entry, string, error) {
	validated, err := policy.validate(cursor)
	if err != nil {
		return nil, "", err
	}
	var page soundCloudPage
	if err := extractor.requestJSON(ctx, transport, validated, &page); err != nil {
		return nil, "", err
	}
	if page.Collection == nil || len(page.Collection) > soundCloudMaxPageEntries {
		return nil, "", fmt.Errorf("%w: malformed SoundCloud page", ErrInvalidPlaylist)
	}
	entries := make([]Entry, 0, len(page.Collection))
	for _, item := range page.Collection {
		// Reference ordering: resolve_entry(e, e.get('track'), e.get('playlist'))
		// 1. Direct collection item (classify by permalink/kind)
		if entry, ok := soundCloudDirectCollectionEntry(item.soundCloudTrack); ok {
			entries = append(entries, entry)
			continue
		}
		// 2. Nested track
		if entry, ok := soundCloudTrackEntry(item.Track, ""); ok {
			entries = append(entries, entry)
			continue
		}
		// 3. Nested playlist
		if entry, ok := soundCloudPlaylistCollectionEntry(item.Playlist); ok {
			entries = append(entries, entry)
		}
	}
	next := ""
	if page.NextHref != "" {
		next, err = policy.validate(page.NextHref)
		if err != nil {
			return nil, "", err
		}
	}
	return entries, next, nil
}

// soundCloudDirectCollectionEntry classifies a direct collection item using
// its explicit kind field and/or permalink URL, matching the reference
// resolve_entry behavior with typed, fail-closed dispatch.
//
// Dispatch rules:
//   - kind "track": use valid track permalink, or /tracks/<id> fallback.
//   - kind "playlist": use valid set/playlist permalink, or /playlists/<id> fallback.
//   - kind unknown/empty: use permalink only if it independently provides an
//     unambiguous supported type (track or set). Otherwise skip (fail closed).
//   - Contradictory kind/permalink: skip (fail closed).
//
// A malformed direct candidate returns false so that valid nested candidates
// can still be considered.
func soundCloudDirectCollectionEntry(item soundCloudTrack) (Entry, bool) {
	id := ""
	if validSoundCloudJSONID(item.ID) {
		id = item.ID.String()
	}
	kind := strings.ToLower(strings.TrimSpace(item.Kind))

	// Classify the permalink independently.
	var permalinkKind soundCloudTargetKind // 0 = unclassified
	rawURL := item.PermalinkURL
	if rawURL != "" {
		if parsed, parseErr := url.Parse(rawURL); parseErr == nil {
			if target, suitable := classifySoundCloudURL(parsed); suitable {
				permalinkKind = target.kind
			}
		}
	}

	switch kind {
	case "track":
		// Direct track: use valid track permalink or /tracks/<id> fallback.
		if permalinkKind == soundCloudTrackTarget {
			return Entry{URL: rawURL, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true
		}
		// Contradictory: permalink classifies as something else (e.g., set).
		if permalinkKind != 0 {
			return Entry{}, false
		}
		// Missing or unclassifiable permalink: fall back to track API URL.
		if id == "" {
			return Entry{}, false
		}
		return Entry{URL: soundCloudAPIBase + "tracks/" + id, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true

	case "playlist":
		// Direct playlist: use valid set/playlist permalink or /playlists/<id> fallback.
		if permalinkKind == soundCloudSetTarget || permalinkKind == soundCloudAPIPlaylistTarget {
			return Entry{URL: rawURL, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true
		}
		// Contradictory: permalink classifies as something else (e.g., track).
		if permalinkKind != 0 {
			return Entry{}, false
		}
		// Missing or unclassifiable permalink: fall back to playlist API URL.
		if id == "" {
			return Entry{}, false
		}
		return Entry{URL: soundCloudAPIBase + "playlists/" + id, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true

	default:
		// Unknown or empty kind: use permalink only if unambiguous.
		switch permalinkKind {
		case soundCloudTrackTarget:
			return Entry{URL: rawURL, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true
		case soundCloudSetTarget, soundCloudAPIPlaylistTarget:
			return Entry{URL: rawURL, ExtractorKey: "soundcloud", ID: id, Title: item.Title, Transparent: true}, true
		default:
			// No usable permalink and unknown kind: fail closed (skip).
			return Entry{}, false
		}
	}
}

func (extractor *SoundCloud) extractStation(ctx context.Context, transport Transport, target soundCloudTarget) (Extraction, error) {
	endpoint := soundCloudAPIBase + "resolve?url=" + url.QueryEscape(target.canonical)
	var station soundCloudStation
	if err := extractor.requestJSON(ctx, transport, endpoint, &station); err != nil {
		return Extraction{}, err
	}
	match := soundCloudStationIDPattern.FindStringSubmatch(station.ID)
	if len(match) != 2 || soundCloudNumericID(match[1]) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud station identifier", ErrInvalidMetadata)
	}
	trackID := match[1]
	if strings.TrimSpace(station.Title) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud station", ErrInvalidMetadata)
	}
	apiPath := "stations/" + station.ID + "/tracks"
	firstURL := soundCloudAPIBase + apiPath + "?linked_partitioning=1&limit=200"
	policy := soundCloudContinuationPolicy{allowedPath: "/" + apiPath}
	sequence, err := ContinuationEntries(nil, firstURL, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return extractor.fetchCollectionPage(ctx, transport, cursor, policy)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(trackID)},
		value.Field{Key: "title", Value: value.String("Track station: " + station.Title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	return Playlist(value.NewInfo(info), sequence)
}

func (extractor *SoundCloud) extractRelated(ctx context.Context, transport Transport, target soundCloudTarget) (Extraction, error) {
	endpoint := soundCloudAPIBase + "resolve?url=" + url.QueryEscape(target.baseTrackURL)
	var track soundCloudTrack
	if err := extractor.requestJSON(ctx, transport, endpoint, &track); err != nil {
		return Extraction{}, err
	}
	if len(track.Errors) > 0 {
		// Do not expose raw error_message from the remote response: it may
		// contain client IDs, signed URLs, tokens, or other sensitive content.
		return Extraction{}, fmt.Errorf("%w: SoundCloud related resource unavailable", ErrUnavailable)
	}
	if !validSoundCloudJSONID(track.ID) {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud track", ErrInvalidMetadata)
	}
	trackID := track.ID.String()
	// Reference: track.get('title') or slug — fall back to URL slug when title is blank.
	title := strings.TrimSpace(track.Title)
	if title == "" {
		title = target.trackSlug
	}
	var apiPath, suffix string
	switch target.relation {
	case "recommended":
		apiPath = "tracks/" + trackID + "/related"
		suffix = "Recommended"
	case "albums":
		apiPath = "tracks/" + trackID + "/albums"
		suffix = "Albums"
	case "sets":
		apiPath = "tracks/" + trackID + "/playlists_without_albums"
		suffix = "Sets"
	default:
		return Extraction{}, ErrUnsupported
	}
	firstURL := soundCloudAPIBase + apiPath + "?linked_partitioning=1&limit=200"
	policy := soundCloudContinuationPolicy{allowedPath: "/" + apiPath}
	sequence, err := ContinuationEntries(nil, firstURL, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		return extractor.fetchCollectionPage(ctx, transport, cursor, policy)
	})
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(trackID)},
		value.Field{Key: "title", Value: value.String(title + " (" + suffix + ")")},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	return Playlist(value.NewInfo(info), sequence)
}

type soundCloudTrack struct {
	Kind             string      `json:"kind"`
	ID               json.Number `json:"id"`
	Title            string      `json:"title"`
	Description      string      `json:"description"`
	Duration         int64       `json:"duration"`
	CreatedAt        string      `json:"created_at"`
	PermalinkURL     string      `json:"permalink_url"`
	ArtworkURL       string      `json:"artwork_url"`
	License          string      `json:"license"`
	Genre            string      `json:"genre"`
	PlaybackCount    *int64      `json:"playback_count"`
	LikesCount       *int64      `json:"likes_count"`
	FavoritingsCount *int64      `json:"favoritings_count"`
	CommentCount     *int64      `json:"comment_count"`
	RepostsCount     *int64      `json:"reposts_count"`
	Policy           string      `json:"policy"`
	Errors           []struct {
		ErrorMessage string `json:"error_message"`
	} `json:"errors"`
	User  soundCloudUser `json:"user"`
	Media struct {
		Transcodings []soundCloudTranscoding `json:"transcodings"`
	} `json:"media"`
}

type soundCloudStation struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type soundCloudUser struct {
	ID           json.Number `json:"id"`
	Username     string      `json:"username"`
	PermalinkURL string      `json:"permalink_url"`
	AvatarURL    string      `json:"avatar_url"`
}

type soundCloudTranscoding struct {
	URL     string `json:"url"`
	Preset  string `json:"preset"`
	Quality string `json:"quality"`
	Snipped bool   `json:"snipped"`
	Format  struct {
		Protocol string `json:"protocol"`
		MimeType string `json:"mime_type"`
	} `json:"format"`
}

type soundCloudPlaylist struct {
	ID           json.Number       `json:"id"`
	Title        string            `json:"title"`
	Description  string            `json:"description"`
	Duration     int64             `json:"duration"`
	CreatedAt    string            `json:"created_at"`
	PermalinkURL string            `json:"permalink_url"`
	ArtworkURL   string            `json:"artwork_url"`
	SetType      string            `json:"set_type"`
	User         soundCloudUser    `json:"user"`
	Tracks       []soundCloudTrack `json:"tracks"`
}

type soundCloudCollectionItem struct {
	soundCloudTrack
	Track    soundCloudTrack    `json:"track"`
	Playlist soundCloudPlaylist `json:"playlist"`
}

type soundCloudPage struct {
	Collection []soundCloudCollectionItem `json:"collection"`
	NextHref   string                     `json:"next_href"`
}

func (extractor *SoundCloud) normalizeTrack(ctx context.Context, transport Transport, track soundCloudTrack, secretToken string) (Extraction, error) {
	trackID := track.ID.String()
	if !validSoundCloudJSONID(track.ID) || strings.TrimSpace(track.Title) == "" || track.Media.Transcodings == nil || len(track.Media.Transcodings) > soundCloudMaxTranscodings {
		return Extraction{}, fmt.Errorf("%w: malformed SoundCloud track", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(track.Media.Transcodings))
	seen := make(map[string]bool)
	hasDRM := false
	for _, transcoding := range track.Media.Transcodings {
		format, drm, err := extractor.resolveTranscoding(ctx, transport, transcoding, secretToken)
		hasDRM = hasDRM || drm
		if err != nil {
			var status *HTTPStatusError
			if errors.As(err, &status) && status.Code == http.StatusNotFound {
				continue
			}
			return Extraction{}, err
		}
		if format == nil {
			continue
		}
		formatURL, _ := format.Lookup("url").StringValue()
		if seen[formatURL] {
			continue
		}
		seen[formatURL] = true
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		if strings.EqualFold(track.Policy, "BLOCK") {
			return Extraction{}, ErrUnavailable
		}
		if hasDRM {
			return Extraction{}, fmt.Errorf("%w: SoundCloud track is DRM protected", ErrUnavailable)
		}
		return Extraction{}, fmt.Errorf("%w: no SoundCloud formats", ErrInvalidMetadata)
	}
	firstFormat, _ := formats[0].Object()
	extension, _ := firstFormat.Lookup("ext").StringValue()
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(trackID)},
		value.Field{Key: "title", Value: value.String(track.Title)},
		value.Field{Key: "track", Value: value.String(track.Title)},
		value.Field{Key: "description", Value: value.String(track.Description)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	setSoundCloudString(info, "webpage_url", track.PermalinkURL)
	setSoundCloudString(info, "uploader", track.User.Username)
	setSoundCloudString(info, "uploader_id", track.User.ID.String())
	setSoundCloudString(info, "uploader_url", track.User.PermalinkURL)
	setSoundCloudString(info, "license", track.License)
	setSoundCloudString(info, "genre", track.Genre)
	if track.Duration > 0 {
		info.Set("duration", value.Float(float64(track.Duration)/1000))
	}
	if timestamp, ok := parseSoundCloudTime(track.CreatedAt); ok {
		info.Set("timestamp", value.Int(timestamp))
	}
	setSoundCloudCount(info, "view_count", track.PlaybackCount)
	likes := track.LikesCount
	if likes == nil {
		likes = track.FavoritingsCount
	}
	setSoundCloudCount(info, "like_count", likes)
	setSoundCloudCount(info, "comment_count", track.CommentCount)
	setSoundCloudCount(info, "repost_count", track.RepostsCount)
	thumbnail := track.ArtworkURL
	if thumbnail == "" {
		thumbnail = track.User.AvatarURL
	}
	if validHTTPURL(thumbnail) {
		info.Set("thumbnail", value.String(thumbnail))
	}
	return Media(value.NewInfo(info)), nil
}

func (extractor *SoundCloud) resolveTranscoding(ctx context.Context, transport Transport, transcoding soundCloudTranscoding, secretToken string) (*value.Object, bool, error) {
	if !validHTTPURL(transcoding.URL) || transcoding.Preset == "" {
		return nil, false, nil
	}
	protocol := strings.ToLower(transcoding.Format.Protocol)
	if strings.HasPrefix(protocol, "ctr-") || strings.HasPrefix(protocol, "cbc-") {
		return nil, true, nil
	}
	presetBase := strings.SplitN(transcoding.Preset, "_", 2)[0]
	if presetBase == "abr" {
		return nil, false, nil
	}
	endpoint := addSoundCloudQuery(transcoding.URL, "secret_token", secretToken)
	var response struct {
		URL string `json:"url"`
	}
	if err := extractor.requestJSON(ctx, transport, endpoint, &response); err != nil {
		return nil, false, err
	}
	if !validHTTPURL(response.URL) {
		return nil, false, nil
	}
	if protocol == "progressive" || protocol == "" {
		protocol = "http"
	}
	if strings.Contains(transcoding.URL, "/encrypted-hls") || protocol == "encrypted-hls" {
		protocol = "hls-aes"
	} else if strings.Contains(transcoding.URL, "/hls") {
		protocol = "hls"
	}
	codec := ""
	if match := soundCloudCodecPattern.FindStringSubmatch(transcoding.Format.MimeType); len(match) == 2 {
		codec = match[1]
	}
	extension := soundCloudExtension(transcoding.Format.MimeType, codec, presetBase)
	formatID := protocol + "_" + transcoding.Preset
	if transcoding.Snipped || strings.Contains(transcoding.URL, "/preview/") || strings.Contains(response.URL, "/preview/") {
		formatID += "_preview"
	}
	object := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(response.URL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "vcodec", Value: value.String("none")},
	)
	if codec != "" {
		object.Set("acodec", value.String(codec))
	}
	if protocol == "hls" || protocol == "hls-aes" {
		object.Set("protocol", value.String("m3u8_native"))
	} else {
		object.Set("protocol", value.String("http"))
	}
	if match := soundCloudABRPattern.FindStringSubmatch(transcoding.Preset); len(match) == 2 {
		if abr, err := strconv.ParseInt(match[1], 10, 64); err == nil {
			object.Set("abr", value.Int(abr))
		}
	}
	return object, false, nil
}

func soundCloudExtension(mimeType, codec, fallback string) string {
	switch {
	case strings.HasPrefix(strings.ToLower(codec), "mp4a"):
		return "m4a"
	case strings.HasPrefix(strings.ToLower(codec), "opus"):
		return "opus"
	case strings.Contains(strings.ToLower(mimeType), "mpeg"):
		return "mp3"
	case strings.Contains(strings.ToLower(mimeType), "ogg"):
		return "ogg"
	case fallback != "":
		return fallback
	default:
		return "mp3"
	}
}

func soundCloudTrackEntry(track soundCloudTrack, secretToken string) (Entry, bool) {
	id := ""
	if validSoundCloudJSONID(track.ID) {
		id = track.ID.String()
	}
	rawURL := track.PermalinkURL
	parsed, parseErr := url.Parse(rawURL)
	target, suitable := classifySoundCloudURL(parsed)
	if parseErr != nil || !suitable || target.kind != soundCloudTrackTarget {
		if id == "" {
			return Entry{}, false
		}
		rawURL = soundCloudAPIBase + "tracks/" + id
		if secretToken != "" {
			rawURL = addSoundCloudQuery(rawURL, "secret_token", secretToken)
		}
	}
	return Entry{URL: rawURL, ExtractorKey: "soundcloud", ID: id, Title: track.Title, Transparent: true}, true
}

func soundCloudPlaylistEntry(playlist soundCloudPlaylist) (Entry, bool) {
	if !validSoundCloudJSONID(playlist.ID) {
		return Entry{}, false
	}
	parsed, err := url.Parse(playlist.PermalinkURL)
	target, suitable := classifySoundCloudURL(parsed)
	if err != nil || !suitable || target.kind != soundCloudSetTarget {
		return Entry{}, false
	}
	return Entry{URL: playlist.PermalinkURL, ExtractorKey: "soundcloud", ID: playlist.ID.String(), Title: playlist.Title, Transparent: true}, true
}

func soundCloudPlaylistCollectionEntry(playlist soundCloudPlaylist) (Entry, bool) {
	if !validSoundCloudJSONID(playlist.ID) {
		return Entry{}, false
	}
	id := playlist.ID.String()
	if validHTTPURL(playlist.PermalinkURL) {
		parsed, err := url.Parse(playlist.PermalinkURL)
		if err == nil {
			if _, ok := classifySoundCloudURL(parsed); ok {
				return Entry{URL: playlist.PermalinkURL, ExtractorKey: "soundcloud", ID: id, Title: playlist.Title, Transparent: true}, true
			}
		}
	}
	return Entry{URL: soundCloudAPIBase + "playlists/" + id, ExtractorKey: "soundcloud", ID: id, Title: playlist.Title, Transparent: true}, true
}

func soundCloudPlaylistInfo(playlist soundCloudPlaylist) value.Info {
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(playlist.ID.String())},
		value.Field{Key: "title", Value: value.String(playlist.Title)},
		value.Field{Key: "description", Value: value.String(playlist.Description)},
	)
	setSoundCloudString(info, "webpage_url", playlist.PermalinkURL)
	setSoundCloudString(info, "uploader", playlist.User.Username)
	setSoundCloudString(info, "uploader_id", playlist.User.ID.String())
	setSoundCloudString(info, "uploader_url", playlist.User.PermalinkURL)
	setSoundCloudString(info, "album", playlist.Title)
	albumType := playlist.SetType
	if albumType == "" {
		albumType = "playlist"
	}
	info.Set("album_type", value.String(albumType))
	if playlist.Duration > 0 {
		info.Set("duration", value.Float(float64(playlist.Duration)/1000))
	}
	if timestamp, ok := parseSoundCloudTime(playlist.CreatedAt); ok {
		info.Set("timestamp", value.Int(timestamp))
	}
	if validHTTPURL(playlist.ArtworkURL) {
		info.Set("thumbnail", value.String(playlist.ArtworkURL))
	}
	return value.NewInfo(info)
}

func (extractor *SoundCloud) requestJSON(ctx context.Context, transport Transport, endpoint string, target any) error {
	for attempt := 0; attempt < 2; attempt++ {
		clientID, err := extractor.discoverClientID(ctx, transport, attempt > 0)
		if err != nil {
			return err
		}
		requestURL := addSoundCloudQuery(endpoint, "client_id", clientID)
		err = RequestJSON(ctx, transport, http.MethodGet, requestURL, nil, nil, target)
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) && attempt == 0 {
			continue
		}
		return categorizeSoundCloudError(err)
	}
	return ErrAuthentication
}

func (extractor *SoundCloud) discoverClientID(ctx context.Context, transport Transport, refresh bool) (string, error) {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if !refresh && extractor.clientID != "" {
		return extractor.clientID, nil
	}
	extractor.clientID = ""
	page, err := readSoundCloudAsset(ctx, transport, soundCloudWebBase)
	if err != nil {
		return "", err
	}
	matches := soundCloudScriptPattern.FindAllSubmatch(page, 64)
	for index := len(matches) - 1; index >= 0; index-- {
		scriptURL, ok := soundCloudAssetURL(string(matches[index][1]))
		if !ok {
			continue
		}
		script, scriptErr := readSoundCloudAsset(ctx, transport, scriptURL)
		if scriptErr != nil {
			continue
		}
		match := soundCloudClientIDPattern.FindSubmatch(script)
		if len(match) == 2 {
			extractor.clientID = string(match[1])
			return extractor.clientID, nil
		}
	}
	return "", fmt.Errorf("%w: SoundCloud client identifier unavailable", ErrUnavailable)
}

func readSoundCloudAsset(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid SoundCloud asset request", ErrInvalidMetadata)
	}
	response, err := transport.Do(ctx, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, categorizeSoundCloudError(&HTTPStatusError{Code: response.StatusCode})
	}
	limited := &io.LimitedReader{R: response.Body, N: soundCloudMaxAssetBytes + 1}
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("%w: read SoundCloud asset", ErrInvalidMetadata)
	}
	if int64(len(data)) > soundCloudMaxAssetBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return data, nil
}

func soundCloudAssetURL(rawURL string) (string, bool) {
	base, _ := url.Parse(soundCloudWebBase)
	reference, err := url.Parse(html.UnescapeString(rawURL))
	if err != nil {
		return "", false
	}
	resolved := base.ResolveReference(reference)
	host := strings.ToLower(resolved.Hostname())
	if resolved.Scheme != "https" || resolved.Port() != "" || resolved.User != nil || !(host == "soundcloud.com" || strings.HasSuffix(host, ".soundcloud.com") || host == "sndcdn.com" || strings.HasSuffix(host, ".sndcdn.com")) {
		return "", false
	}
	return resolved.String(), true
}

type soundCloudContinuationPolicy struct {
	allowedPath string
}

func (policy soundCloudContinuationPolicy) validate(rawURL string) (string, error) {
	if len(rawURL) > soundCloudMaxURLBytes || strings.Contains(rawURL, "\x00") {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	// Authority and scheme checks: HTTPS only, exact hostname, no userinfo,
	// no explicit port, no fragment.
	if parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "api-v2.soundcloud.com" || parsed.Port() != "" || parsed.User != nil || parsed.Fragment != "" || parsed.RawFragment != "" {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	// Reject encoded slash, backslash, NUL, and malformed escaping.
	if soundCloudEncodedSeparators(parsed) {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	// Reject literal "." or ".." path segments and trailing slash.
	// The decoded path must equal allowedPath exactly (no path.Clean).
	decodedPath := parsed.Path
	if decodedPath != policy.allowedPath {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	// Additional guard: reject any dot segments in the escaped path.
	escapedPath := parsed.EscapedPath()
	for _, segment := range strings.Split(escapedPath, "/") {
		if segment == "." || segment == ".." {
			return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
		}
	}
	// Query cardinality and value limits.
	// Use url.ParseQuery explicitly to reject malformed percent-escaping and
	// invalid semicolon syntax that parsed.Query() would silently discard.
	query, queryErr := url.ParseQuery(parsed.RawQuery)
	if queryErr != nil {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	if len(query) > soundCloudMaxQueryParams {
		return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
	}
	for key, values := range query {
		if len(key) > 256 {
			return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
		}
		for _, v := range values {
			if len(v) > soundCloudMaxQueryValue {
				return "", fmt.Errorf("%w: invalid SoundCloud continuation", ErrInvalidPlaylist)
			}
		}
	}
	// Strip stale client_id before reusing a cursor.
	query.Del("client_id")
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func validSoundCloudJSONID(input json.Number) bool {
	return soundCloudNumericID(input.String()) != ""
}

func addSoundCloudQuery(rawURL, key, input string) string {
	if input == "" {
		return rawURL
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	query := parsed.Query()
	query.Set(key, input)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func categorizeSoundCloudError(err error) error {
	if err == nil {
		return nil
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrAuthentication, status)
		case http.StatusNotFound, http.StatusGone:
			return fmt.Errorf("%w: %w", ErrUnavailable, status)
		}
	}
	return err
}

func parseSoundCloudTime(input string) (int64, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006/01/02 15:04:05 -0700"} {
		parsed, err := time.Parse(layout, input)
		if err == nil {
			return parsed.Unix(), true
		}
	}
	return 0, false
}

func setSoundCloudString(object *value.Object, key, input string) {
	if input != "" {
		object.Set(key, value.String(input))
	}
}

func setSoundCloudCount(object *value.Object, key string, input *int64) {
	if input != nil && *input >= 0 {
		object.Set(key, value.Int(*input))
	}
}
