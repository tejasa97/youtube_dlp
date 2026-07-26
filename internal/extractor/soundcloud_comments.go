package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	soundCloudCommentPageSize      = 20
	soundCloudCommentDefaultMax    = 100
	soundCloudCommentHardMax       = 10_000
	soundCloudCommentMaxPages      = 500
	soundCloudCommentMaxTextBytes  = 1 << 20
	soundCloudCommentMaxFieldBytes = 16 << 10
)

var (
	ErrSoundCloudCommentsRateLimited = errors.New("SoundCloud comments rate limited")
	ErrSoundCloudCommentsNetwork     = errors.New("SoundCloud comments network failure")
)

type normalizedSoundCloudCommentOptions struct {
	sort string
	max  int
}

func normalizeSoundCloudCommentOptions(options SoundCloudCommentOptions) (normalizedSoundCloudCommentOptions, error) {
	sortMode := options.Sort
	if sortMode == "" {
		sortMode = "newest"
	}
	switch sortMode {
	case "newest", "oldest", "track-timestamp":
	default:
		return normalizedSoundCloudCommentOptions{}, fmt.Errorf("%w: invalid SoundCloud comment sort", ErrInvalidMetadata)
	}
	maxComments := options.MaxComments
	if maxComments == 0 {
		maxComments = soundCloudCommentDefaultMax
	}
	if maxComments < 1 || maxComments > soundCloudCommentHardMax {
		return normalizedSoundCloudCommentOptions{}, fmt.Errorf("%w: invalid SoundCloud comment limit", ErrInvalidMetadata)
	}
	return normalizedSoundCloudCommentOptions{sort: sortMode, max: maxComments}, nil
}

type soundCloudCommentPage struct {
	Collection []soundCloudComment `json:"collection"`
	NextHref   string              `json:"next_href"`
}

type soundCloudCommentID struct {
	value string
}

func (id *soundCloudCommentID) UnmarshalJSON(data []byte) error {
	id.value = ""
	raw := string(data)
	if soundCloudNumericID(raw) != "" {
		id.value = raw
	}
	return nil
}

func (id soundCloudCommentID) valid() bool {
	return id.value != ""
}

type soundCloudComment struct {
	ID        soundCloudCommentID `json:"id"`
	Body      string              `json:"body"`
	CreatedAt string              `json:"created_at"`
	Timestamp json.Number         `json:"timestamp"`
	User      struct {
		ID           soundCloudCommentID `json:"id"`
		Username     string              `json:"username"`
		AvatarURL    string              `json:"avatar_url"`
		PermalinkURL string              `json:"permalink_url"`
		Verified     *bool               `json:"verified"`
	} `json:"user"`
}

type soundCloudCommentContinuationPolicy struct {
	trackID string
	sort    string
}

func (policy soundCloudCommentContinuationPolicy) initialURL() string {
	query := url.Values{
		"limit":    {strconv.Itoa(soundCloudCommentPageSize)},
		"offset":   {"0"},
		"sort":     {policy.sort},
		"threaded": {"1"},
	}
	return soundCloudAPIBase + "tracks/" + policy.trackID + "/comments?" + query.Encode()
}

func (policy soundCloudCommentContinuationPolicy) validate(rawURL string) (string, error) {
	if len(rawURL) == 0 || len(rawURL) > soundCloudMaxURLBytes || strings.ContainsRune(rawURL, 0) {
		return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || strings.ToLower(parsed.Hostname()) != "api-v2.soundcloud.com" ||
		parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" ||
		soundCloudEncodedSeparators(parsed) || parsed.EscapedPath() != "/tracks/"+policy.trackID+"/comments" {
		return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
	}
	for key, values := range query {
		switch key {
		case "client_id", "limit", "offset", "sort", "threaded":
		default:
			return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
		}
		if len(values) != 1 || len(values[0]) > soundCloudMaxQueryValue {
			return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
		}
	}
	if query.Get("limit") != strconv.Itoa(soundCloudCommentPageSize) ||
		query.Get("threaded") != "1" || query.Get("sort") != policy.sort {
		return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
	}
	offset, err := strconv.ParseUint(query.Get("offset"), 10, 31)
	if err != nil || offset > soundCloudCommentHardMax || strconv.FormatUint(offset, 10) != query.Get("offset") {
		return "", fmt.Errorf("%w: invalid SoundCloud comment continuation", ErrInvalidPlaylist)
	}
	query.Del("client_id")
	parsed.Host = "api-v2.soundcloud.com"
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func (extractor *SoundCloud) extractTrackComments(
	ctx context.Context,
	transport Transport,
	trackID string,
	options normalizedSoundCloudCommentOptions,
) ([]value.Value, error) {
	if soundCloudNumericID(trackID) == "" {
		return nil, fmt.Errorf("%w: invalid SoundCloud comment track id", ErrInvalidMetadata)
	}
	policy := soundCloudCommentContinuationPolicy{trackID: trackID, sort: options.sort}
	next := policy.initialURL()
	seen := make(map[string]struct{})
	comments := make([]value.Value, 0, min(options.max, soundCloudCommentDefaultMax))
	for pageNumber := 0; next != ""; pageNumber++ {
		if err := contextError(ctx); err != nil {
			return nil, err
		}
		if pageNumber >= soundCloudCommentMaxPages {
			return nil, fmt.Errorf("%w: SoundCloud comment page limit exceeded", ErrPlaylistLimit)
		}
		validated, err := policy.validate(next)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[validated]; duplicate {
			return nil, fmt.Errorf("%w: repeated SoundCloud comment continuation", ErrInvalidPlaylist)
		}
		seen[validated] = struct{}{}
		var page soundCloudCommentPage
		if err := extractor.requestCommentJSON(ctx, transport, validated, &page); err != nil {
			return nil, err
		}
		if len(page.Collection) > soundCloudCommentPageSize {
			return nil, fmt.Errorf("%w: oversized SoundCloud comment page", ErrInvalidMetadata)
		}
		for index, row := range page.Collection {
			if index%16 == 0 {
				if err := contextError(ctx); err != nil {
					return nil, err
				}
			}
			comment, ok, err := normalizeSoundCloudComment(row)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			comments = append(comments, value.ObjectValue(comment))
			if len(comments) >= options.max {
				return comments, nil
			}
		}
		next = page.NextHref
	}
	return comments, nil
}

func normalizeSoundCloudComment(comment soundCloudComment) (*value.Object, bool, error) {
	if !comment.ID.valid() {
		return nil, false, nil
	}
	for _, field := range []string{comment.User.Username, comment.User.AvatarURL, comment.User.PermalinkURL, comment.CreatedAt} {
		if len(field) > soundCloudCommentMaxFieldBytes {
			return nil, false, fmt.Errorf("%w: oversized SoundCloud comment field", ErrInvalidMetadata)
		}
	}
	if len(comment.Body) > soundCloudCommentMaxTextBytes {
		return nil, false, fmt.Errorf("%w: oversized SoundCloud comment text", ErrInvalidMetadata)
	}
	object := value.NewObject(
		value.Field{Key: "id", Value: value.String(comment.ID.value)},
		value.Field{Key: "text", Value: value.String(comment.Body)},
	)
	if comment.User.ID.valid() {
		object.Set("author_id", value.String(comment.User.ID.value))
	}
	setSoundCloudString(object, "author", comment.User.Username)
	if strictValidHostedHTTPURL(comment.User.AvatarURL) {
		object.Set("author_thumbnail", value.String(comment.User.AvatarURL))
	}
	if strictValidHostedHTTPURL(comment.User.PermalinkURL) {
		object.Set("author_url", value.String(comment.User.PermalinkURL))
	}
	if comment.User.Verified != nil {
		object.Set("author_is_verified", value.Bool(*comment.User.Verified))
	}
	if timestamp, ok := parseSoundCloudTime(comment.CreatedAt); ok {
		object.Set("timestamp", value.Int(timestamp))
	}
	if milliseconds, err := strconv.ParseFloat(comment.Timestamp.String(), 64); err == nil && milliseconds >= 0 {
		seconds := milliseconds / 1000
		object.Set("start_time", value.Float(seconds))
		object.Set("end_time", value.Float(seconds))
	}
	return object, true, nil
}

func (extractor *SoundCloud) requestCommentJSON(ctx context.Context, transport Transport, endpoint string, target any) error {
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	for attempt := 0; attempt < 2; attempt++ {
		clientID, err := extractor.discoverCommentClientID(ctx, isolated, attempt > 0)
		if err != nil {
			return err
		}
		requestURL := addSoundCloudQuery(endpoint, "client_id", clientID)
		err = requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirect, http.MethodGet, requestURL, nil, make(http.Header), target)
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) && attempt == 0 {
			continue
		}
		return categorizeSoundCloudCommentError(err)
	}
	return ErrAuthentication
}

func (extractor *SoundCloud) discoverCommentClientID(
	ctx context.Context,
	transport CredentialIsolatedNoRedirectTransport,
	refresh bool,
) (string, error) {
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if !refresh && extractor.clientID != "" {
		return extractor.clientID, nil
	}
	extractor.clientID = ""
	readAsset := func(rawURL string) ([]byte, error) {
		return readSoundCloudAssetWith(ctx, transport.DoWithoutCredentialsNoRedirect, rawURL)
	}
	page, err := readAsset(soundCloudWebBase)
	if err != nil {
		return "", err
	}
	matches := soundCloudScriptPattern.FindAllSubmatch(page, 64)
	for index := len(matches) - 1; index >= 0; index-- {
		scriptURL, ok := soundCloudAssetURL(string(matches[index][1]))
		if !ok {
			continue
		}
		script, scriptErr := readAsset(scriptURL)
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

func categorizeSoundCloudCommentError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return fmt.Errorf("%w: %w", ErrAuthentication, status)
		case http.StatusNotFound, http.StatusGone:
			return fmt.Errorf("%w: %w", ErrUnavailable, status)
		case http.StatusTooManyRequests:
			return fmt.Errorf("%w: %w", ErrSoundCloudCommentsRateLimited, status)
		default:
			return fmt.Errorf("%w: HTTP status %d", ErrSoundCloudCommentsNetwork, status.Code)
		}
	}
	return fmt.Errorf("%w: request failed", ErrSoundCloudCommentsNetwork)
}
