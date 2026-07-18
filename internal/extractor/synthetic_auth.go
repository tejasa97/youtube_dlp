package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const syntheticAuthAPIBase = "https://auth-fixture.invalid/api/media/"

var syntheticAuthPath = regexp.MustCompile(`^/watch/([A-Za-z0-9_-]{1,128})/?$`)

// SyntheticAuth is a deterministic authenticated-extraction target. It is
// intentionally network-inert outside conformance environments: the reserved
// .invalid origin can only be served by an injected operation transport.
// Authentication remains transport-owned; the extractor never reads a jar or
// handles a cookie value.
type SyntheticAuth struct{}

func NewSyntheticAuth() SyntheticAuth { return SyntheticAuth{} }

func (SyntheticAuth) Name() string { return "synthetic_auth" }

func (SyntheticAuth) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "https" && parsed.Host == "auth-fixture.invalid" && syntheticAuthPath.MatchString(parsed.Path)
}

func (SyntheticAuth) Extract(ctx context.Context, request Request) (Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil || !NewSyntheticAuth().Suitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	match := syntheticAuthPath.FindStringSubmatch(parsed.Path)
	requestedID := match[1]
	headers := make(http.Header)
	if request.Credentials != nil {
		credential, ok, lookupErr := request.Credentials.Lookup(ctx, "auth-fixture.invalid")
		if lookupErr != nil {
			return Extraction{}, lookupErr
		}
		if ok {
			authRequest, requestErr := http.NewRequest(http.MethodGet, syntheticAuthAPIBase, nil)
			if requestErr != nil {
				return Extraction{}, fmt.Errorf("%w: synthetic authenticated request", ErrInvalidMetadata)
			}
			authRequest.SetBasicAuth(credential.Username, credential.Password)
			headers.Set("Authorization", authRequest.Header.Get("Authorization"))
		}
	}
	var response syntheticAuthResponse
	if err := RequestJSON(ctx, request.Transport, http.MethodGet, syntheticAuthAPIBase+url.PathEscape(requestedID), nil, headers, &response); err != nil {
		var status *HTTPStatusError
		if errors.As(err, &status) {
			switch status.Code {
			case http.StatusUnauthorized, http.StatusForbidden:
				return Extraction{}, ErrAuthentication
			case http.StatusNotFound, http.StatusGone:
				return Extraction{}, ErrUnavailable
			}
		}
		return Extraction{}, err
	}
	return normalizeSyntheticAuth(response, requestedID)
}

type syntheticAuthResponse struct {
	Session struct {
		Authenticated bool `json:"authenticated"`
	} `json:"session"`
	Unavailable bool `json:"unavailable"`
	Media       struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		URL         string `json:"url"`
		Duration    int64  `json:"duration"`
	} `json:"media"`
}

func normalizeSyntheticAuth(response syntheticAuthResponse, requestedID string) (Extraction, error) {
	if !response.Session.Authenticated {
		return Extraction{}, ErrAuthentication
	}
	if response.Unavailable {
		return Extraction{}, ErrUnavailable
	}
	media := response.Media
	if media.ID == "" || media.ID != requestedID || media.Title == "" || !validHTTPURL(media.URL) {
		return Extraction{}, fmt.Errorf("%w: synthetic authenticated media", ErrInvalidMetadata)
	}
	extension := path.Ext(mustURLPath(media.URL))
	if extension == "" {
		extension = ".mp4"
	}
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("http-authenticated")},
		value.Field{Key: "url", Value: value.String(media.URL)},
		value.Field{Key: "ext", Value: value.String(extension[1:])},
		value.Field{Key: "protocol", Value: value.String("https")},
	)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(media.ID)},
		value.Field{Key: "title", Value: value.String(media.Title)},
		value.Field{Key: "description", Value: value.String(media.Description)},
		value.Field{Key: "webpage_url", Value: value.String("https://auth-fixture.invalid/watch/" + url.PathEscape(requestedID))},
		value.Field{Key: "ext", Value: value.String(extension[1:])},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)
	if media.Duration > 0 {
		info.Set("duration", value.Int(media.Duration))
	}
	return Media(value.NewInfo(info)), nil
}
