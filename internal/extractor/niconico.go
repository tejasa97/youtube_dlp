package extractor

// Niconico implements the bounded anonymous public subset of the pinned
// yt-dlp niconico family.  The service has several authenticated, live, and
// entitlement-bound surfaces; those are deliberately not represented here.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	niconicoAPIBase        = "https://nvapi.nicovideo.jp"
	niconicoPageBase       = "https://www.nicovideo.jp"
	niconicoPageLimit      = 4 << 20
	niconicoMaxString      = 4096
	niconicoMaxURL         = 16 << 10
	niconicoPageSize       = 100
	niconicoSearchPageSize = 32
	niconicoMaxPages       = 1000
	niconicoMaxEntries     = 100000
	niconicoMaxFormats     = 64
)

var (
	niconicoVideoIDRE   = regexp.MustCompile(`^(?:[a-z]{2})?[0-9]{1,32}$`)
	niconicoUserIDRE    = regexp.MustCompile(`^[0-9]{1,32}$`)
	niconicoListIDRE    = regexp.MustCompile(`^[0-9]{1,32}$`)
	niconicoTrackIDRE   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
	niconicoSearchKeyRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	niconicoVideoAttrRE = regexp.MustCompile(`(?i)data-video-id\s*=\s*["']([^"']{1,128})["']`)
)

var (
	ErrNiconicoPremium   = errors.New("niconico premium entitlement required")
	ErrNiconicoMember    = errors.New("niconico channel membership required")
	ErrNiconicoPPV       = errors.New("niconico purchase entitlement required")
	ErrNiconicoSensitive = errors.New("niconico sensitive content is unavailable anonymously")
	ErrNiconicoScheduled = errors.New("niconico content is scheduled")
	ErrNiconicoRateLimit = errors.New("niconico rate limited")
	ErrNiconicoServer    = errors.New("niconico service unavailable")
)

// NiconicoError is a typed, secret-safe failure.  The operation and status
// are intentionally coarse: access keys, track IDs, signed URLs, search
// terms, response bodies, cookies, and request URLs never enter Error().
type NiconicoError struct {
	Kind   string
	Status int
	Cause  error
}

func (err *NiconicoError) Error() string {
	if err == nil {
		return "<nil>"
	}
	switch err.Kind {
	case "authentication":
		return "niconico authentication is required"
	case "premium":
		return "niconico premium entitlement is required"
	case "member":
		return "niconico channel membership is required"
	case "ppv":
		return "niconico purchase entitlement is required"
	case "sensitive":
		return "niconico sensitive content is unavailable anonymously"
	case "scheduled":
		return "niconico content is scheduled"
	case "geo":
		return "niconico content is region restricted"
	case "rate_limit":
		return "niconico rate limited"
	case "server":
		return "niconico service unavailable"
	case "redirect":
		return "niconico redirect rejected"
	case "unavailable":
		return "niconico video unavailable"
	case "invalid_host":
		return "niconico attributable host rejected"
	case "invalid_response":
		return "niconico response is invalid"
	default:
		return "niconico request failed"
	}
}

func (err *NiconicoError) Unwrap() error { return err.Cause }

func niconicoError(kind string, status int, cause error) error {
	return &NiconicoError{Kind: kind, Status: status, Cause: cause}
}

type niconicoRole string

const (
	niconicoRolePage      niconicoRole = "page"
	niconicoRoleAPI       niconicoRole = "api"
	niconicoRoleHLS       niconicoRole = "hls"
	niconicoRoleThumbnail niconicoRole = "thumbnail"
	niconicoRoleComment   niconicoRole = "comment"
)

// NiconicoMediaURLAllowed is also used by the product HLS dispatch wrapper.
// Keeping this policy beside the extractor prevents a signed manifest from
// widening the request trust boundary through a hostile segment URL.
func NiconicoMediaURLAllowed(rawURL string) bool {
	return niconicoAttributableURL(niconicoRoleHLS, rawURL)
}

func niconicoAttributableURL(role niconicoRole, rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > niconicoMaxURL {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Host == "" {
		return false
	}
	if u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" || host != u.Host || strings.ContainsAny(host, "\x00\r\n\t /\\") {
		return false
	}
	if niconicoEscapedHazard(u.EscapedPath()) {
		return false
	}
	switch role {
	case niconicoRoleAPI:
		return host == "nvapi.nicovideo.jp"
	case niconicoRoleHLS:
		return host == "delivery.domand.nicovideo.jp"
	case niconicoRoleThumbnail:
		return host == "img.cdn.nimg.jp" || host == "nicovideo.cdn.nimg.jp" || host == "tn.smilevideo.jp"
	case niconicoRoleComment:
		return niconicoHostOrSubdomain(host, "nicovideo.jp") ||
			niconicoHostOrSubdomain(host, "niconico.jp") ||
			niconicoHostOrSubdomain(host, "dmc.nico")
	default:
		return false
	}
}

func niconicoHostOrSubdomain(host, suffix string) bool {
	return host == suffix || strings.HasSuffix(host, "."+suffix)
}

func niconicoEscapedHazard(path string) bool {
	path = strings.ToLower(path)
	for _, token := range []string{"%2f", "%5c", "%00", "%23", "%3f"} {
		if strings.Contains(path, token) {
			return true
		}
	}
	return strings.Contains(path, "\x00")
}

func niconicoPageURLAllowed(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > niconicoMaxURL {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Port() != "" || u.Host == "" || u.Host != strings.ToLower(u.Host) {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "nicovideo.jp" && host != "www.nicovideo.jp" && host != "sp.nicovideo.jp" && host != "embed.nicovideo.jp" && host != "nico.ms" {
		return false
	}
	return !niconicoEscapedHazard(u.EscapedPath()) && !strings.Contains(u.Path, "\x00")
}

func niconicoVideoURL(u *url.URL) (string, bool) {
	if u == nil || !niconicoPageURLAllowed(u.String()) || u.Fragment != "" || u.RawQuery != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "nicovideo.jp" && host != "www.nicovideo.jp" && host != "sp.nicovideo.jp" && host != "embed.nicovideo.jp" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || (parts[0] != "watch" && parts[0] != "shorts") || !niconicoVideoIDRE.MatchString(parts[1]) {
		return "", false
	}
	if strings.HasSuffix(u.Path, "/") || strings.ContainsAny(parts[1], "\\\x00") {
		return "", false
	}
	return parts[1], true
}

func niconicoCollectionURL(u *url.URL, kind string) (string, bool) {
	if u == nil || !niconicoPageURLAllowed(u.String()) || u.RawQuery != "" {
		return "", false
	}
	if kind == "playlist" {
		if u.Fragment != "" && u.Fragment != "/" {
			if !niconicoListIDRE.MatchString(strings.TrimPrefix(u.Fragment, "/")) {
				return "", false
			}
		} else if u.Fragment != "" {
			return "", false
		}
	}
	host := strings.ToLower(u.Hostname())
	if host != "nicovideo.jp" && host != "www.nicovideo.jp" && host != "sp.nicovideo.jp" && host != "nico.ms" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if strings.HasSuffix(u.Path, "/") && kind != "series" && u.Fragment == "" {
		return "", false
	}
	var id string
	switch kind {
	case "playlist":
		// user/<id>/mylist/<id>, my/mylist/<id>, and mylist/<id>.
		switch {
		case len(parts) == 2 && parts[0] == "mylist":
			id = parts[1]
		case len(parts) == 3 && parts[0] == "my" && parts[1] == "mylist":
			id = parts[2]
		case len(parts) == 4 && parts[0] == "user" && niconicoUserIDRE.MatchString(parts[1]) && parts[2] == "mylist":
			id = parts[3]
		}
		if u.Fragment != "" {
			if u.Path != "/mylist/" && u.Path != "/mylist" && u.Path != "/my/mylist/" && u.Path != "/my/mylist" {
				return "", false
			}
			id = strings.TrimPrefix(u.Fragment, "/")
		}
	case "series":
		switch {
		case len(parts) == 2 && parts[0] == "series":
			id = parts[1]
		case len(parts) == 4 && parts[0] == "user" && niconicoUserIDRE.MatchString(parts[1]) && parts[2] == "series":
			id = parts[3]
		}
	default:
		return "", false
	}
	if !niconicoListIDRE.MatchString(id) || strings.ContainsAny(id, "\\\x00") {
		return "", false
	}
	return id, true
}

func niconicoSearchURLParts(u *url.URL, searchType string) (string, bool) {
	if u == nil || !niconicoPageURLAllowed(u.String()) || u.Fragment != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "nicovideo.jp" && host != "www.nicovideo.jp" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != searchType || strings.HasSuffix(u.Path, "/") {
		return "", false
	}
	term, err := url.PathUnescape(parts[1])
	if err != nil || !niconicoSafeSearchTerm(term) || strings.ContainsAny(term, "/\\") {
		return "", false
	}
	if !niconicoSearchQueryAllowed(u.RawQuery) {
		return "", false
	}
	return term, true
}

func niconicoSafeSearchTerm(term string) bool {
	if term == "" || len(term) > 256 {
		return false
	}
	for _, r := range term {
		if r == '\x00' || r == '\\' || r == '/' || r == '?' || r == '#' || r == '&' || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func niconicoSearchQueryAllowed(rawQuery string) bool {
	if len(rawQuery) > 1024 {
		return false
	}
	if rawQuery == "" {
		return true
	}
	query, err := url.ParseQuery(rawQuery)
	if err != nil || len(query) > 6 {
		return false
	}
	for key, values := range query {
		switch key {
		case "sort", "order", "start", "end", "page":
		default:
			return false
		}
		if len(values) != 1 || len(values[0]) > 64 || strings.ContainsAny(values[0], "\x00\r\n") {
			return false
		}
		if key == "page" && (!niconicoSearchKeyRE.MatchString(values[0]) || strings.HasPrefix(values[0], "0")) {
			return false
		}
	}
	return true
}

func niconicoOpaqueSearchTerm(rawURL, scheme string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != scheme || u.Opaque == "" || u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return u.Opaque, niconicoSafeSearchTerm(u.Opaque)
}

// Niconico is the anonymous public watch/shorts extractor.
type Niconico struct{}

func NewNiconico() Niconico               { return Niconico{} }
func (Niconico) Name() string             { return "niconico" }
func (Niconico) Suitable(u *url.URL) bool { _, ok := niconicoVideoURL(u); return ok }

type niconicoNumber int64

func (number *niconicoNumber) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte("null")) {
		*number = 0
		return nil
	}
	var numeric json.Number
	if err := json.Unmarshal(data, &numeric); err == nil {
		parsed, err := strconv.ParseInt(numeric.String(), 10, 64)
		if err != nil {
			return err
		}
		*number = niconicoNumber(parsed)
		return nil
	}
	var stringValue string
	if err := json.Unmarshal(data, &stringValue); err != nil {
		return err
	}
	parsed, err := strconv.ParseInt(stringValue, 10, 64)
	if err != nil {
		return err
	}
	*number = niconicoNumber(parsed)
	return nil
}

type niconicoOwner struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Nickname string `json:"nickname"`
	User     struct {
		ID       string `json:"id"`
		Nickname string `json:"nickname"`
		Name     string `json:"name"`
	} `json:"user"`
}

type niconicoCounts struct {
	View    niconicoNumber `json:"view"`
	Comment niconicoNumber `json:"comment"`
	Like    niconicoNumber `json:"like"`
}

type niconicoVideo struct {
	ID               string            `json:"id"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	ShortDescription string            `json:"shortDescription"`
	Duration         niconicoNumber    `json:"duration"`
	RegisteredAt     string            `json:"registeredAt"`
	Owner            niconicoOwner     `json:"owner"`
	Channel          niconicoOwner     `json:"channel"`
	Count            niconicoCounts    `json:"count"`
	Thumbnail        map[string]string `json:"thumbnail"`
	Essential        struct {
		ID string `json:"id"`
	} `json:"essential"`
}

type niconicoTrack struct {
	ID           string         `json:"id"`
	IsAvailable  bool           `json:"isAvailable"`
	BitRate      niconicoNumber `json:"bitRate"`
	SamplingRate niconicoNumber `json:"samplingRate"`
	QualityLevel niconicoNumber `json:"qualityLevel"`
}

type niconicoWatchData struct {
	PublishScheduledAt string `json:"publishScheduledAt"`
	ReasonCode         string `json:"reasonCode"`
	Viewer             struct {
		AllowSensitiveContents *bool `json:"allowSensitiveContents"`
	} `json:"viewer"`
	Video   niconicoVideo  `json:"video"`
	Owner   niconicoOwner  `json:"owner"`
	Channel niconicoOwner  `json:"channel"`
	Count   niconicoCounts `json:"count"`
	Genre   struct {
		Label string `json:"label"`
	} `json:"genre"`
	Tag struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	} `json:"tag"`
	Payment struct {
		Video struct {
			IsContinuationBenefit bool `json:"isContinuationBenefit"`
			IsPpv                 bool `json:"isPpv"`
			IsAdmission           bool `json:"isAdmission"`
			IsPremium             bool `json:"isPremium"`
		} `json:"video"`
	} `json:"payment"`
	Media struct {
		Domand struct {
			AccessRightKey string          `json:"accessRightKey"`
			Videos         []niconicoTrack `json:"videos"`
			Audios         []niconicoTrack `json:"audios"`
		} `json:"domand"`
	} `json:"media"`
	Client struct {
		WatchTrackID string `json:"watchTrackId"`
	} `json:"client"`
}

type niconicoMeta struct {
	Status    int    `json:"status"`
	ErrorCode string `json:"errorCode"`
}

type niconicoEnvelope struct {
	Meta niconicoMeta    `json:"meta"`
	Data json.RawMessage `json:"data"`
}

func niconicoRead(ctx context.Context, transport Transport, role niconicoRole, method, rawURL string, body []byte, headers http.Header, limit int64) ([]byte, int, error) {
	if err := contextError(ctx); err != nil {
		return nil, 0, err
	}
	if (role == niconicoRoleAPI || role == niconicoRoleHLS) && !niconicoAttributableURL(role, rawURL) || role == niconicoRolePage && !niconicoPageURLAllowed(rawURL) {
		return nil, 0, niconicoError("invalid_host", 0, ErrTransportIsolation)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, 0, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: invalid request", ErrInvalidMetadata)
	}
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		request.Header.Del(key)
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, 0, contextErr
		}
		return nil, 0, err
	}
	if response == nil || response.Body == nil {
		return nil, 0, niconicoError("invalid_response", 0, ErrInvalidMetadata)
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status < 200 || status >= 300 {
		return nil, status, niconicoHTTPError(status)
	}
	if limit <= 0 || limit > maxExtractorJSONBytes {
		limit = maxExtractorJSONBytes
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return nil, 0, contextErr
		}
		return nil, status, niconicoError("request", status, ErrServerResponse)
	}
	if int64(len(data)) > limit {
		return nil, status, ErrJSONResponseTooLarge
	}
	return data, status, nil
}

// ErrServerResponse is kept internal to the NiconicoError wrapper so callers
// receive a stable server category without any transport text.
var ErrServerResponse = errors.New("niconico response read failed")

func niconicoHTTPError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return niconicoError("authentication", status, ErrAuthentication)
	case status == http.StatusForbidden:
		return niconicoError("authentication", status, ErrAuthentication)
	case status == http.StatusNotFound || status == http.StatusGone:
		return niconicoError("unavailable", status, ErrUnavailable)
	case status == http.StatusTooManyRequests:
		return niconicoError("rate_limit", status, ErrNiconicoRateLimit)
	case status == http.StatusUnavailableForLegalReasons:
		return niconicoError("geo", status, ErrRegionRestricted)
	case status >= 500:
		return niconicoError("server", status, ErrNiconicoServer)
	case status >= 300 && status < 400:
		return niconicoError("redirect", status, ErrUnavailable)
	default:
		return niconicoError("request", status, ErrUnavailable)
	}
}

func niconicoDecode(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return niconicoError("invalid_response", 0, ErrInvalidMetadata)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return niconicoError("invalid_response", 0, ErrInvalidMetadata)
	}
	return nil
}

func niconicoAPIURL(path string, query url.Values) (string, error) {
	u := &url.URL{Scheme: "https", Host: "nvapi.nicovideo.jp", Path: path}
	if query != nil {
		u.RawQuery = query.Encode()
	}
	if len(u.String()) > niconicoMaxURL {
		return "", fmt.Errorf("%w: API request bounds", ErrInvalidMetadata)
	}
	return u.String(), nil
}

func niconicoEnvelopeResponse(data []byte) (niconicoEnvelope, error) {
	var response niconicoEnvelope
	if err := niconicoDecode(data, &response); err != nil {
		return niconicoEnvelope{}, err
	}
	if len(response.Data) == 0 || bytes.Equal(response.Data, []byte("null")) {
		return response, niconicoError("invalid_response", response.Meta.Status, ErrInvalidMetadata)
	}
	return response, nil
}

func niconicoMapReason(meta niconicoMeta, data niconicoWatchData) error {
	reason := strings.ToUpper(strings.TrimSpace(data.ReasonCode))
	switch reason {
	case "DOMESTIC_VIDEO", "HIGH_RISK_COUNTRY_VIDEO":
		return niconicoError("geo", meta.Status, ErrRegionRestricted)
	case "CHANNEL_MEMBER_ONLY":
		return niconicoError("member", meta.Status, ErrNiconicoMember)
	case "PPV_VIDEO":
		return niconicoError("ppv", meta.Status, ErrNiconicoPPV)
	case "PREMIUM_ONLY":
		return niconicoError("premium", meta.Status, ErrNiconicoPremium)
	case "HARMFUL_VIDEO":
		return niconicoError("sensitive", meta.Status, ErrNiconicoSensitive)
	case "HIDDEN_VIDEO":
		if data.PublishScheduledAt != "" {
			return niconicoError("scheduled", meta.Status, ErrNiconicoScheduled)
		}
		return niconicoError("unavailable", meta.Status, ErrUnavailable)
	case "UNAUTHORIZED":
		return niconicoError("authentication", meta.Status, ErrAuthentication)
	case "NOT_FOUND", "INVALID_PARAMETER", "ADMINISTRATOR_DELETE_VIDEO", "RIGHT_HOLDER_DELETE_VIDEO", "DELETED_CHANNEL_VIDEO", "DELETED_COMMUNITY_VIDEO":
		return niconicoError("unavailable", meta.Status, ErrUnavailable)
	case "MAINTENANCE":
		return niconicoError("server", meta.Status, ErrNiconicoServer)
	}
	switch strings.ToUpper(strings.TrimSpace(meta.ErrorCode)) {
	case "UNAUTHORIZED":
		return niconicoError("authentication", meta.Status, ErrAuthentication)
	case "FORBIDDEN":
		return niconicoError("authentication", meta.Status, ErrAuthentication)
	case "NOT_FOUND", "INVALID_PARAMETER":
		return niconicoError("unavailable", meta.Status, ErrUnavailable)
	case "MAINTENANCE":
		return niconicoError("server", meta.Status, ErrNiconicoServer)
	}
	if meta.Status >= 500 {
		return niconicoError("server", meta.Status, ErrNiconicoServer)
	}
	return niconicoError("request", meta.Status, ErrUnavailable)
}

func niconicoGuestActionTrackID() string {
	return "AAAAAAAAAA_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
}

func (Niconico) Extract(ctx context.Context, request Request) (Extraction, error) {
	videoID, ok := niconicoVideoURLValue(request.URL)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	query := url.Values{"actionTrackId": {niconicoGuestActionTrackID()}}
	apiURL, err := niconicoAPIURL("/api/watch/v3_guest/"+videoID, query)
	if err != nil {
		return Extraction{}, err
	}
	data, status, err := niconicoRead(ctx, request.Transport, niconicoRoleAPI, http.MethodGet, apiURL, nil, niconicoAPIHeaders(), maxExtractorJSONBytes)
	if err != nil {
		return Extraction{}, err
	}
	envelope, err := niconicoEnvelopeResponse(data)
	if err != nil {
		return Extraction{}, err
	}
	var watch niconicoWatchData
	if err := niconicoDecode(envelope.Data, &watch); err != nil {
		return Extraction{}, err
	}
	if envelope.Meta.Status == 0 {
		envelope.Meta.Status = status
	}
	if envelope.Meta.Status != http.StatusOK {
		return Extraction{}, niconicoMapReason(envelope.Meta, watch)
	}
	return niconicoWatchExtraction(ctx, request.Transport, request.URL, videoID, watch)
}

func niconicoVideoURLValue(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	return niconicoVideoURL(u)
}

func niconicoAPIHeaders() http.Header {
	return http.Header{
		"Accept":             {"application/json;charset=utf-8"},
		"X-Frontend-ID":      {"6"},
		"X-Frontend-Version": {"0"},
	}
}

func niconicoWatchExtraction(ctx context.Context, transport Transport, webpageURL, requestedID string, watch niconicoWatchData) (Extraction, error) {
	videoID := watch.Video.ID
	if !niconicoVideoIDRE.MatchString(videoID) {
		videoID = requestedID
	}
	if !niconicoVideoIDRE.MatchString(videoID) || strings.TrimSpace(watch.Video.Title) == "" {
		return Extraction{}, niconicoError("invalid_response", 200, ErrInvalidMetadata)
	}
	availability := "public"
	payment := watch.Payment.Video
	switch {
	case payment.IsContinuationBenefit || payment.IsPpv:
		availability = "needs_auth"
	case payment.IsAdmission:
		availability = "subscriber_only"
	case payment.IsPremium:
		availability = "premium_only"
	}
	formats, err := niconicoAccessRightFormats(ctx, transport, videoID, watch, availability)
	if err != nil {
		return Extraction{}, err
	}
	if len(formats) == 0 {
		switch availability {
		case "needs_auth":
			return Extraction{}, niconicoError("ppv", 200, ErrNiconicoPPV)
		case "subscriber_only":
			return Extraction{}, niconicoError("member", 200, ErrNiconicoMember)
		case "premium_only":
			return Extraction{}, niconicoError("premium", 200, ErrNiconicoPremium)
		default:
			return Extraction{}, niconicoError("unavailable", 200, ErrUnavailable)
		}
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "display_id", Value: value.String(requestedID)},
		value.Field{Key: "title", Value: value.String(watch.Video.Title)},
		value.Field{Key: "description", Value: value.String(watch.Video.Description)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "availability", Value: value.String(availability)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))
	if watch.Video.Duration > 0 {
		info.Set("duration", value.Float(float64(watch.Video.Duration)))
	}
	if watch.Video.Count.View > 0 {
		info.Set("view_count", value.Int(int64(watch.Video.Count.View)))
	}
	if watch.Video.Count.Comment > 0 {
		info.Set("comment_count", value.Int(int64(watch.Video.Count.Comment)))
	}
	if watch.Video.Count.Like > 0 {
		info.Set("like_count", value.Int(int64(watch.Video.Count.Like)))
	}
	if watch.Video.RegisteredAt != "" {
		if timestamp, ok := niconicoTimestamp(watch.Video.RegisteredAt); ok {
			info.Set("timestamp", value.Int(timestamp))
		}
	}
	owner := watch.Video.Owner
	if owner.ID == "" && watch.Owner.ID != "" {
		owner = watch.Owner
	}
	if owner.Name == "" {
		owner.Name = owner.Nickname
	}
	if owner.Name == "" {
		owner.Name = owner.User.Name
	}
	if owner.Name == "" {
		owner.Name = owner.User.Nickname
	}
	if owner.ID == "" {
		owner.ID = owner.User.ID
	}
	if owner.Name != "" {
		info.Set("uploader", value.String(owner.Name))
	}
	if owner.ID != "" {
		info.Set("uploader_id", value.String(owner.ID))
	}
	if watch.Genre.Label != "" {
		info.Set("genre", value.String(watch.Genre.Label))
		info.Set("genres", value.List(value.String(watch.Genre.Label)))
	}
	tags := make([]value.Value, 0, len(watch.Tag.Items))
	for _, tag := range watch.Tag.Items {
		if niconicoSafeMetadataString(tag.Name) {
			tags = append(tags, value.String(tag.Name))
		}
	}
	if len(tags) > 0 {
		info.Set("tags", value.List(tags...))
	}
	if thumbnails := niconicoThumbnailValues(watch.Video.Thumbnail); len(thumbnails) > 0 {
		info.Set("thumbnails", value.List(thumbnails...))
		if raw, ok := thumbnails[0].Object(); ok {
			if thumb, ok := raw.Lookup("url").StringValue(); ok {
				info.Set("thumbnail", value.String(thumb))
			}
		}
	}
	return Media(info), nil
}

func niconicoAccessRightFormats(ctx context.Context, transport Transport, videoID string, watch niconicoWatchData, availability string) ([]value.Value, error) {
	if availability != "public" {
		return nil, nil
	}
	domand := watch.Media.Domand
	if !niconicoSafeToken(domand.AccessRightKey) || !niconicoTrackIDRE.MatchString(watch.Client.WatchTrackID) {
		return nil, nil
	}
	videos := make([]string, 0, len(domand.Videos))
	for _, video := range domand.Videos {
		if video.IsAvailable && niconicoTrackIDRE.MatchString(video.ID) {
			videos = append(videos, video.ID)
		}
	}
	audios := make([]string, 0, len(domand.Audios))
	for _, audio := range domand.Audios {
		if audio.IsAvailable && niconicoTrackIDRE.MatchString(audio.ID) {
			audios = append(audios, audio.ID)
		}
	}
	if len(videos) == 0 || len(audios) == 0 || len(videos)*len(audios) > niconicoMaxFormats {
		return nil, nil
	}
	outputs := make([][]string, 0, len(videos)*len(audios))
	for _, video := range videos {
		for _, audio := range audios {
			outputs = append(outputs, []string{video, audio})
		}
	}
	body, err := json.Marshal(struct {
		Outputs [][]string `json:"outputs"`
	}{Outputs: outputs})
	if err != nil {
		return nil, niconicoError("invalid_response", 200, ErrInvalidMetadata)
	}
	query := url.Values{"actionTrackId": {watch.Client.WatchTrackID}}
	endpoint, err := niconicoAPIURL("/v1/watch/"+videoID+"/access-rights/hls", query)
	if err != nil {
		return nil, err
	}
	headers := niconicoAPIHeaders()
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Access-Right-Key", domand.AccessRightKey)
	headers.Set("X-Request-With", niconicoPageBase)
	data, _, err := niconicoRead(ctx, transport, niconicoRoleAPI, http.MethodPost, endpoint, body, headers, maxExtractorJSONBytes)
	if err != nil {
		return nil, err
	}
	envelope, err := niconicoEnvelopeResponse(data)
	if err != nil {
		return nil, err
	}
	var access struct {
		ContentURL string `json:"contentUrl"`
	}
	if err := niconicoDecode(envelope.Data, &access); err != nil {
		return nil, err
	}
	if !niconicoAttributableURL(niconicoRoleHLS, access.ContentURL) {
		return nil, niconicoError("invalid_host", 200, ErrInvalidMetadata)
	}
	master, _, err := niconicoRead(ctx, transport, niconicoRoleHLS, http.MethodGet, access.ContentURL, nil, nil, niconicoPageLimit)
	if err != nil {
		return nil, err
	}
	return niconicoHLSFormats(access.ContentURL, master, domand.Videos, domand.Audios)
}

type niconicoHLSVariant struct {
	URL        string
	Bandwidth  float64
	Codecs     string
	Resolution string
}

type niconicoHLSAudio struct {
	URL     string
	GroupID string
	Name    string
}

func niconicoHLSFormats(manifestURL string, body []byte, videos, audios []niconicoTrack) ([]value.Value, error) {
	playlist, err := hls.Parse(manifestURL, body)
	if err != nil || len(playlist.Variants) == 0 || len(playlist.Variants) > niconicoMaxFormats {
		return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
	}

	variants, audiosInManifest, err := niconicoHLSMasterEntries(manifestURL, body, playlist.Variants)
	if err != nil || len(variants) != len(playlist.Variants) || len(audiosInManifest) > niconicoMaxFormats {
		return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
	}

	availableVideos := make(map[string]niconicoTrack, len(videos))
	for _, track := range videos {
		if track.IsAvailable && niconicoTrackIDRE.MatchString(track.ID) {
			availableVideos[track.ID] = track
		}
	}
	availableAudioTracks := make([]niconicoTrack, 0, len(audios))
	minABR := 0.0
	haveMinABR := false
	for _, track := range audios {
		if !track.IsAvailable || !niconicoTrackIDRE.MatchString(track.ID) {
			continue
		}
		availableAudioTracks = append(availableAudioTracks, track)
		abr := float64(track.BitRate) / 1000
		if !haveMinABR || abr < minABR {
			minABR, haveMinABR = abr, true
		}
	}

	formats := make([]value.Value, 0, len(audiosInManifest)+len(variants))
	for _, audio := range audiosInManifest {
		if !niconicoAttributableURL(niconicoRoleHLS, audio.URL) || !niconicoSafeToken(audio.GroupID) || !niconicoSafeMetadataString(audio.Name) {
			return nil, niconicoError("invalid_host", http.StatusOK, ErrInvalidMetadata)
		}
		rawID := audio.GroupID + "-" + audio.Name
		if !niconicoSafeToken(rawID) {
			return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
		}
		fields := []value.Field{
			{Key: "format_id", Value: value.String(rawID)},
			{Key: "url", Value: value.String(audio.URL)},
			{Key: "manifest_url", Value: value.String(manifestURL)},
			{Key: "ext", Value: value.String("mp4")},
			{Key: "protocol", Value: value.String("m3u8_native")},
			{Key: "vcodec", Value: value.String("none")},
			{Key: "acodec", Value: value.String("aac")},
			{Key: "_credential_isolated", Value: value.Bool(true)},
			{Key: "_niconico_scoped", Value: value.Bool(true)},
		}
		for _, track := range availableAudioTracks {
			if !strings.HasPrefix(rawID, track.ID) {
				continue
			}
			fields[0] = value.Field{Key: "format_id", Value: value.String(track.ID)}
			fields = append(fields,
				value.Field{Key: "abr", Value: value.Float(float64(track.BitRate) / 1000)},
				value.Field{Key: "asr", Value: value.Int(int64(track.SamplingRate))},
				value.Field{Key: "quality", Value: value.Int(int64(track.QualityLevel))},
			)
			break
		}
		formats = append(formats, value.ObjectValue(value.NewObject(fields...)))
	}

	videoFormats := make([]niconicoHLSVariant, 0, len(variants))
	for _, variant := range variants {
		if !niconicoAttributableURL(niconicoRoleHLS, variant.URL) {
			return nil, niconicoError("invalid_host", http.StatusOK, ErrInvalidMetadata)
		}
		id := niconicoURLStem(variant.URL)
		if !niconicoTrackIDRE.MatchString(id) {
			return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
		}
		videoFormats = append(videoFormats, variant)
	}
	sort.SliceStable(videoFormats, func(left, right int) bool {
		return videoFormats[left].Bandwidth < videoFormats[right].Bandwidth
	})
	seenVideoURLs := make(map[string]bool, len(videoFormats))
	for _, variant := range videoFormats {
		if seenVideoURLs[variant.URL] {
			continue
		}
		seenVideoURLs[variant.URL] = true
		id := niconicoURLStem(variant.URL)
		vcodec, acodec := niconicoHLSCodecs(variant.Codecs)
		if vcodec == "" {
			return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
		}
		quality := int64(-1)
		if track, ok := availableVideos[id]; ok && track.QualityLevel != 0 {
			quality = int64(track.QualityLevel)
		}
		fields := []value.Field{
			{Key: "format_id", Value: value.String(id)},
			{Key: "url", Value: value.String(variant.URL)},
			{Key: "manifest_url", Value: value.String(manifestURL)},
			{Key: "ext", Value: value.String("mp4")},
			{Key: "protocol", Value: value.String("m3u8_native")},
			{Key: "tbr", Value: value.Float(variant.Bandwidth - minABR)},
			{Key: "quality", Value: value.Int(quality)},
			{Key: "vcodec", Value: value.String(vcodec)},
			{Key: "acodec", Value: value.String(acodec)},
			{Key: "_credential_isolated", Value: value.Bool(true)},
			{Key: "_niconico_scoped", Value: value.Bool(true)},
		}
		if width, height := niconicoHLSResolution(variant.Resolution); width > 0 && height > 0 {
			fields = append(fields, value.Field{Key: "width", Value: value.Int(int64(width))}, value.Field{Key: "height", Value: value.Int(int64(height))})
		}
		formats = append(formats, value.ObjectValue(value.NewObject(fields...)))
	}
	if len(formats) == 0 || len(formats) > niconicoMaxFormats {
		return nil, niconicoError("invalid_response", http.StatusOK, ErrInvalidMetadata)
	}
	return formats, nil
}

func niconicoHLSMasterEntries(manifestURL string, body []byte, parsed []hls.Variant) ([]niconicoHLSVariant, []niconicoHLSAudio, error) {
	base, err := url.Parse(manifestURL)
	if err != nil {
		return nil, nil, err
	}
	variants := make([]niconicoHLSVariant, 0, len(parsed))
	audios := make([]niconicoHLSAudio, 0)
	var pending map[string]string
	variantIndex := 0
	for _, rawLine := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			attrs, parseErr := niconicoHLSAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
			if parseErr != nil {
				return nil, nil, parseErr
			}
			if attrs["TYPE"] == "AUDIO" && attrs["URI"] != "" {
				resolved, resolveErr := niconicoResolveHLSURL(base, attrs["URI"])
				if resolveErr != nil {
					return nil, nil, resolveErr
				}
				audios = append(audios, niconicoHLSAudio{URL: resolved, GroupID: attrs["GROUP-ID"], Name: attrs["NAME"]})
			}
			continue
		}
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			pending, err = niconicoHLSAttributes(strings.TrimPrefix(line, "#EXT-X-STREAM-INF:"))
			if err != nil {
				return nil, nil, err
			}
			continue
		}
		if strings.HasPrefix(line, "#") || pending == nil {
			continue
		}
		if variantIndex >= len(parsed) {
			return nil, nil, errors.New("too many HLS variants")
		}
		bandwidthRaw := pending["AVERAGE-BANDWIDTH"]
		if bandwidthRaw == "" {
			bandwidthRaw = pending["BANDWIDTH"]
		}
		bandwidth, parseErr := strconv.ParseFloat(bandwidthRaw, 64)
		if parseErr != nil || bandwidth < 0 {
			return nil, nil, errors.New("invalid HLS variant bandwidth")
		}
		variants = append(variants, niconicoHLSVariant{
			URL: parsed[variantIndex].URL, Bandwidth: bandwidth / 1000,
			Codecs: pending["CODECS"], Resolution: pending["RESOLUTION"],
		})
		variantIndex++
		pending = nil
	}
	if pending != nil || variantIndex != len(parsed) {
		return nil, nil, errors.New("HLS variant entries do not match parser output")
	}
	return variants, audios, nil
}

func niconicoHLSAttributes(input string) (map[string]string, error) {
	attrs := make(map[string]string)
	for index := 0; index < len(input); {
		start := index
		for index < len(input) && input[index] != '=' {
			index++
		}
		if index == len(input) {
			return nil, errors.New("HLS attribute has no value")
		}
		key := strings.TrimSpace(input[start:index])
		if key == "" {
			return nil, errors.New("HLS attribute has no name")
		}
		index++
		var valueText string
		if index < len(input) && input[index] == '"' {
			index++
			start = index
			for index < len(input) && input[index] != '"' {
				index++
			}
			if index == len(input) {
				return nil, errors.New("unterminated HLS attribute")
			}
			valueText = input[start:index]
			index++
		} else {
			start = index
			for index < len(input) && input[index] != ',' {
				index++
			}
			valueText = strings.TrimSpace(input[start:index])
		}
		if _, exists := attrs[key]; exists {
			return nil, errors.New("duplicate HLS attribute")
		}
		attrs[key] = valueText
		if index < len(input) {
			if input[index] != ',' {
				return nil, errors.New("invalid HLS attribute separator")
			}
			index++
		}
	}
	return attrs, nil
}

func niconicoResolveHLSURL(base *url.URL, reference string) (string, error) {
	parsed, err := url.Parse(reference)
	if err != nil {
		return "", err
	}
	return base.ResolveReference(parsed).String(), nil
}

func niconicoURLStem(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	base := path.Base(parsed.Path)
	return strings.TrimSuffix(base, path.Ext(base))
}

func niconicoHLSCodecs(raw string) (string, string) {
	var video, audio string
	for _, codec := range strings.Split(raw, ",") {
		codec = strings.TrimSpace(codec)
		lower := strings.ToLower(codec)
		switch {
		case video == "" && strings.HasPrefix(lower, "avc"):
			video = codec
		case video == "" && (strings.HasPrefix(lower, "hvc") || strings.HasPrefix(lower, "hev") || strings.HasPrefix(lower, "vp") || strings.HasPrefix(lower, "av01")):
			video = codec
		case audio == "" && (strings.HasPrefix(lower, "mp4a") || strings.HasPrefix(lower, "aac") || strings.HasPrefix(lower, "opus") || strings.HasPrefix(lower, "ac-3") || strings.HasPrefix(lower, "ec-3")):
			audio = codec
		}
	}
	if audio == "" {
		audio = "none"
	}
	return video, audio
}

func niconicoHLSResolution(raw string) (int, int) {
	parts := strings.SplitN(raw, "x", 2)
	if len(parts) != 2 {
		parts = strings.SplitN(raw, "X", 2)
	}
	if len(parts) != 2 {
		return 0, 0
	}
	width, widthErr := strconv.Atoi(parts[0])
	height, heightErr := strconv.Atoi(parts[1])
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0
	}
	return width, height
}

func niconicoTimestamp(raw string) (int64, bool) {
	// Avoid a general date parser in this small extractor; upstream timestamps
	// are RFC3339.  The first 19 bytes are sufficient for fixture-stable UTC
	// values and reject malformed/oversized strings without leaking them.
	if len(raw) < 19 || len(raw) > 64 || raw[4] != '-' || raw[7] != '-' || raw[10] != 'T' {
		return 0, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, false
	}
	return parsed.Unix(), true
}

func niconicoThumbnailValues(thumbnails map[string]string) []value.Value {
	keys := []string{"largeUrl", "middleUrl", "url", "player", "ogp", "short"}
	result := make([]value.Value, 0, len(keys))
	seen := make(map[string]bool)
	for index, key := range keys {
		raw := thumbnails[key]
		if !niconicoAttributableURL(niconicoRoleThumbnail, raw) || seen[raw] {
			continue
		}
		seen[raw] = true
		result = append(result, value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String(key)},
			value.Field{Key: "url", Value: value.String(raw)},
			value.Field{Key: "preference", Value: value.Int(int64(len(keys) - index))},
			value.Field{Key: "ext", Value: value.String("jpg")},
			value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
		)))
	}
	return result
}

func niconicoSafeMetadataString(raw string) bool {
	return len(raw) <= niconicoMaxString && !strings.ContainsAny(raw, "\x00\r\n")
}

func niconicoSafeToken(raw string) bool {
	if len(raw) == 0 || len(raw) > niconicoMaxString {
		return false
	}
	for _, char := range raw {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

// niconicoPagedEntries is reusable: every iterator owns its page counter and
// response identity set.  It advances by source rows, not by valid emitted
// children, so malformed rows cannot truncate a collection prematurely.
type niconicoPagedEntries struct {
	pageSize int
	fetch    func(context.Context, int) (entries []Entry, sourceRows int, responseID string, err error)
}

func (entries niconicoPagedEntries) Iterator() EntryIterator {
	return &niconicoPageIterator{source: entries, page: 1, queue: nil, seen: make(map[string]bool)}
}

type niconicoPageIterator struct {
	source niconicoPagedEntries
	page   int
	queue  []Entry
	index  int
	seen   map[string]bool
	done   bool
	count  int
}

func (iterator *niconicoPageIterator) Next(ctx context.Context) (Entry, bool, error) {
	for {
		if err := contextError(ctx); err != nil {
			return Entry{}, false, err
		}
		if iterator.index < len(iterator.queue) {
			entry := iterator.queue[iterator.index]
			iterator.index++
			return entry, true, nil
		}
		if iterator.done {
			return Entry{}, false, nil
		}
		if iterator.page > niconicoMaxPages || iterator.count >= niconicoMaxEntries {
			return Entry{}, false, ErrPlaylistLimit
		}
		page, sourceRows, responseID, err := iterator.source.fetch(ctx, iterator.page)
		if err != nil {
			return Entry{}, false, err
		}
		if responseID != "" {
			if iterator.seen[responseID] {
				return Entry{}, false, fmt.Errorf("%w: repeated niconico page", ErrInvalidPlaylist)
			}
			iterator.seen[responseID] = true
		}
		iterator.page++
		iterator.queue = page
		iterator.index = 0
		iterator.count += len(page)
		if sourceRows <= 0 || sourceRows < iterator.source.pageSize {
			iterator.done = true
		}
	}
}

type niconicoCollection struct {
	Name        string            `json:"name"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	Owner       niconicoOwner     `json:"owner"`
	Items       []json.RawMessage `json:"items"`
	Detail      struct {
		Title       string        `json:"title"`
		Description string        `json:"description"`
		Owner       niconicoOwner `json:"owner"`
	} `json:"detail"`
}

func niconicoCollectionEntries(rows []json.RawMessage) ([]Entry, int) {
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		video, ok := niconicoVideoRow(row)
		if !ok || !niconicoVideoIDRE.MatchString(video.ID) {
			continue
		}
		entry := Entry{URL: niconicoPageBase + "/watch/" + video.ID, ExtractorKey: "niconico", ID: video.ID}
		if niconicoSafeMetadataString(video.Title) {
			entry.Title = video.Title
		}
		if video.Duration > 0 {
			entry.Duration, entry.HasDuration = float64(video.Duration), true
		}
		if video.Count.View >= 0 {
			entry.ViewCount, entry.HasViewCount = int64(video.Count.View), true
		}
		for _, thumbnail := range []string{video.Thumbnail["nHdUrl"], video.Thumbnail["largeUrl"], video.Thumbnail["listingUrl"], video.Thumbnail["url"]} {
			if niconicoAttributableURL(niconicoRoleThumbnail, thumbnail) {
				entry.Thumbnail = thumbnail
				break
			}
		}
		entries = append(entries, entry)
	}
	return entries, len(rows)
}

func niconicoVideoRow(row json.RawMessage) (niconicoVideo, bool) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(row, &object); err != nil {
		return niconicoVideo{}, false
	}
	for _, key := range []string{"video", "essential"} {
		if raw, ok := object[key]; ok {
			var video niconicoVideo
			if json.Unmarshal(raw, &video) == nil {
				if key == "essential" {
					var essential struct {
						ID string `json:"id"`
					}
					if json.Unmarshal(raw, &essential) == nil {
						video.ID = essential.ID
					}
				}
				if video.ID == "" {
					video.ID = video.Essential.ID
				}
				return video, true
			}
		}
	}
	var video niconicoVideo
	if json.Unmarshal(row, &video) != nil {
		return niconicoVideo{}, false
	}
	if video.ID == "" {
		video.ID = video.Essential.ID
	}
	return video, true
}

func niconicoOwnerFields(info *value.Info, owner niconicoOwner) {
	if owner.Name == "" {
		owner.Name = owner.Nickname
	}
	if owner.Name == "" {
		owner.Name = owner.User.Name
	}
	if owner.Name == "" {
		owner.Name = owner.User.Nickname
	}
	if owner.ID == "" {
		owner.ID = owner.User.ID
	}
	if owner.Name != "" {
		info.Set("uploader", value.String(owner.Name))
	}
	if owner.ID != "" {
		info.Set("uploader_id", value.String(owner.ID))
	}
}

func niconicoListPage(ctx context.Context, transport Transport, kind, listID string, page int) ([]Entry, int, string, error) {
	path := "/v2/" + kind + "/" + listID
	if kind == "mylist" {
		path = "/v2/mylists/" + listID
	}
	query := url.Values{"page": {strconv.Itoa(page)}, "pageSize": {strconv.Itoa(niconicoPageSize)}}
	endpoint, err := niconicoAPIURL(path, query)
	if err != nil {
		return nil, 0, "", err
	}
	data, _, err := niconicoRead(ctx, transport, niconicoRoleAPI, http.MethodGet, endpoint, nil, niconicoAPIHeadersWithLanguage(), maxExtractorJSONBytes)
	if err != nil {
		return nil, 0, "", err
	}
	envelope, err := niconicoEnvelopeResponse(data)
	if err != nil {
		return nil, 0, "", err
	}
	var root struct {
		Mylist niconicoCollection `json:"mylist"`
		Items  []json.RawMessage  `json:"items"`
	}
	if kind == "mylist" {
		if err := niconicoDecode(envelope.Data, &root); err != nil {
			return nil, 0, "", err
		}
		entries, rows := niconicoCollectionEntries(root.Mylist.Items)
		return entries, rows, niconicoResponseID(data), nil
	}
	if err := niconicoDecode(envelope.Data, &root); err != nil {
		return nil, 0, "", err
	}
	entries, rows := niconicoCollectionEntries(root.Items)
	return entries, rows, niconicoResponseID(data), nil
}

func niconicoResponseID(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func niconicoAPIHeadersWithLanguage() http.Header {
	headers := niconicoAPIHeaders()
	headers.Set("X-Niconico-Language", "en-us")
	return headers
}

type NiconicoPlaylist struct{}

func NewNiconicoPlaylist() NiconicoPlaylist { return NiconicoPlaylist{} }
func (NiconicoPlaylist) Name() string       { return "niconico_playlist" }
func (NiconicoPlaylist) Suitable(u *url.URL) bool {
	_, ok := niconicoCollectionURL(u, "playlist")
	return ok
}
func (NiconicoPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	listID, ok := niconicoCollectionURLValue(request.URL, "playlist")
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint, err := niconicoAPIURL("/v2/mylists/"+listID, url.Values{"pageSize": {"1"}})
	if err != nil {
		return Extraction{}, err
	}
	data, _, err := niconicoRead(ctx, request.Transport, niconicoRoleAPI, http.MethodGet, endpoint, nil, niconicoAPIHeadersWithLanguage(), maxExtractorJSONBytes)
	if err != nil {
		return Extraction{}, err
	}
	envelope, err := niconicoEnvelopeResponse(data)
	if err != nil {
		return Extraction{}, err
	}
	var root struct {
		Mylist niconicoCollection `json:"mylist"`
	}
	if err := niconicoDecode(envelope.Data, &root); err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(listID)}))
	if root.Mylist.Name != "" {
		info.Set("title", value.String(root.Mylist.Name))
	}
	if root.Mylist.Description != "" {
		info.Set("description", value.String(root.Mylist.Description))
	}
	niconicoOwnerFields(&info, root.Mylist.Owner)
	entries, err := niconicoCollectionSequence(request.Transport, "mylist", listID)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, entries)
}

type NiconicoSeries struct{}

func NewNiconicoSeries() NiconicoSeries { return NiconicoSeries{} }
func (NiconicoSeries) Name() string     { return "niconico_series" }
func (NiconicoSeries) Suitable(u *url.URL) bool {
	_, ok := niconicoCollectionURL(u, "series")
	return ok
}
func (NiconicoSeries) Extract(ctx context.Context, request Request) (Extraction, error) {
	seriesID, ok := niconicoCollectionURLValue(request.URL, "series")
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint, err := niconicoAPIURL("/v2/series/"+seriesID, url.Values{"pageSize": {"1"}})
	if err != nil {
		return Extraction{}, err
	}
	data, _, err := niconicoRead(ctx, request.Transport, niconicoRoleAPI, http.MethodGet, endpoint, nil, niconicoAPIHeadersWithLanguage(), maxExtractorJSONBytes)
	if err != nil {
		return Extraction{}, err
	}
	envelope, err := niconicoEnvelopeResponse(data)
	if err != nil {
		return Extraction{}, err
	}
	var root niconicoCollection
	if err := niconicoDecode(envelope.Data, &root); err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(seriesID)}))
	if root.Detail.Title != "" {
		info.Set("title", value.String(root.Detail.Title))
	}
	if root.Detail.Description != "" {
		info.Set("description", value.String(root.Detail.Description))
	}
	niconicoOwnerFields(&info, root.Detail.Owner)
	entries, err := niconicoCollectionSequence(request.Transport, "series", seriesID)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, entries)
}

func niconicoCollectionURLValue(rawURL, kind string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	return niconicoCollectionURL(u, kind)
}

func niconicoCollectionSequence(transport Transport, kind, listID string) (EntrySequence, error) {
	if transport == nil || !niconicoListIDRE.MatchString(listID) {
		return nil, ErrInvalidPlaylist
	}
	return niconicoPagedEntries{
		pageSize: niconicoPageSize,
		fetch: func(ctx context.Context, page int) ([]Entry, int, string, error) {
			return niconicoListPage(ctx, transport, kind, listID, page)
		},
	}, nil
}

type NiconicoSearch struct{}

func NewNiconicoSearch() NiconicoSearch { return NiconicoSearch{} }
func (NiconicoSearch) Name() string     { return "niconico_search" }
func (NiconicoSearch) Suitable(u *url.URL) bool {
	_, ok := niconicoOpaqueSearchTerm(u.String(), "nicosearch")
	return ok
}
func (NiconicoSearch) Extract(ctx context.Context, request Request) (Extraction, error) {
	term, ok := niconicoOpaqueSearchTerm(request.URL, "nicosearch")
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return niconicoSearchExtraction(ctx, request.Transport, term, "search", term, "")
}

type NiconicoSearchURL struct{}

func NewNiconicoSearchURL() NiconicoSearchURL { return NiconicoSearchURL{} }
func (NiconicoSearchURL) Name() string        { return "niconico_search_url" }
func (NiconicoSearchURL) Suitable(u *url.URL) bool {
	_, ok := niconicoSearchURLParts(u, "search")
	return ok
}
func (NiconicoSearchURL) Extract(ctx context.Context, request Request) (Extraction, error) {
	term, ok := niconicoSearchURLValue(request.URL, "search")
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return niconicoSearchExtraction(ctx, request.Transport, term, "search", term, request.URL)
}

type NiconicoTag struct{}

func NewNiconicoTag() NiconicoTag            { return NiconicoTag{} }
func (NiconicoTag) Name() string             { return "niconico_tag" }
func (NiconicoTag) Suitable(u *url.URL) bool { _, ok := niconicoSearchURLParts(u, "tag"); return ok }
func (NiconicoTag) Extract(ctx context.Context, request Request) (Extraction, error) {
	term, ok := niconicoSearchURLValue(request.URL, "tag")
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return niconicoSearchExtraction(ctx, request.Transport, term, "tag", term, request.URL)
}

func niconicoSearchURLValue(rawURL, kind string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	return niconicoSearchURLParts(u, kind)
}

func niconicoSearchExtraction(ctx context.Context, transport Transport, term, searchType, itemID, sourceURL string) (Extraction, error) {
	if transport == nil || !niconicoSafeSearchTerm(term) {
		return Extraction{}, ErrUnsupported
	}
	var base string
	if sourceURL != "" {
		base = sourceURL
	} else {
		base = niconicoPageBase + "/" + searchType + "/" + url.PathEscape(term)
	}
	sequence := niconicoPagedEntries{
		pageSize: niconicoSearchPageSize,
		fetch: func(ctx context.Context, page int) ([]Entry, int, string, error) {
			rawURL, err := niconicoSearchPageURL(base, page)
			if err != nil {
				return nil, 0, "", err
			}
			body, _, err := niconicoRead(ctx, transport, niconicoRolePage, http.MethodGet, rawURL, nil, nil, niconicoPageLimit)
			if err != nil {
				return nil, 0, "", err
			}
			matches := niconicoVideoAttrRE.FindAllSubmatch(body, -1)
			entries := make([]Entry, 0, len(matches))
			for _, match := range matches {
				id := string(match[1])
				if !niconicoVideoIDRE.MatchString(id) {
					continue
				}
				entries = append(entries, Entry{URL: niconicoPageBase + "/watch/" + id, ExtractorKey: "niconico", ID: id})
			}
			return entries, len(matches), niconicoResponseID(body), nil
		},
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(itemID)}, value.Field{Key: "title", Value: value.String(term)}))
	return Playlist(info, sequence)
}

func niconicoSearchPageURL(base string, page int) (string, error) {
	if page < 1 || page > niconicoMaxPages {
		return "", ErrPlaylistLimit
	}
	u, err := url.Parse(base)
	if err != nil || !niconicoPageURLAllowed(base) || !niconicoSearchQueryAllowed(u.RawQuery) {
		return "", ErrUnsupported
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrUnsupported
	}
	query.Set("page", strconv.Itoa(page))
	u.RawQuery = query.Encode()
	if len(u.String()) > niconicoMaxURL {
		return "", ErrInvalidMetadata
	}
	return u.String(), nil
}

type NiconicoUser struct{}

func NewNiconicoUser() NiconicoUser           { return NiconicoUser{} }
func (NiconicoUser) Name() string             { return "niconico_user" }
func (NiconicoUser) Suitable(u *url.URL) bool { _, ok := niconicoUserURL(u); return ok }
func (NiconicoUser) Extract(ctx context.Context, request Request) (Extraction, error) {
	userID, ok := niconicoUserURLValue(request.URL)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(userID)}))
	sequence := niconicoPagedEntries{
		pageSize: niconicoPageSize,
		fetch: func(ctx context.Context, page int) ([]Entry, int, string, error) {
			query := url.Values{"sortKey": {"registeredAt"}, "sortOrder": {"desc"}, "pageSize": {strconv.Itoa(niconicoPageSize)}, "page": {strconv.Itoa(page)}}
			endpoint, err := niconicoAPIURL("/v2/users/"+userID+"/videos", query)
			if err != nil {
				return nil, 0, "", err
			}
			body, _, err := niconicoRead(ctx, request.Transport, niconicoRoleAPI, http.MethodGet, endpoint, nil, niconicoAPIHeaders(), maxExtractorJSONBytes)
			if err != nil {
				return nil, 0, "", err
			}
			envelope, err := niconicoEnvelopeResponse(body)
			if err != nil {
				return nil, 0, "", err
			}
			var data struct {
				TotalCount niconicoNumber    `json:"totalCount"`
				Items      []json.RawMessage `json:"items"`
			}
			if err := niconicoDecode(envelope.Data, &data); err != nil {
				return nil, 0, "", err
			}
			entries, rows := niconicoCollectionEntries(data.Items)
			if data.TotalCount > niconicoMaxEntries {
				return nil, 0, "", ErrPlaylistLimit
			}
			return entries, rows, niconicoResponseID(body), nil
		},
	}
	return Playlist(info, sequence)
}

func niconicoUserURL(u *url.URL) (string, bool) {
	if u == nil || !niconicoPageURLAllowed(u.String()) || u.Fragment != "" || u.RawQuery != "" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "nicovideo.jp" && host != "www.nicovideo.jp" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 && len(parts) != 3 {
		return "", false
	}
	if parts[0] != "user" || !niconicoUserIDRE.MatchString(parts[1]) || (len(parts) == 3 && parts[2] != "video") {
		return "", false
	}
	if len(parts) == 2 && strings.HasSuffix(u.Path, "/") {
		return "", false
	}
	return parts[1], true
}

func niconicoUserURLValue(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	return niconicoUserURL(u)
}

// The date-recursive upstream extractor uses the current wall clock and
// unbounded interval splitting.  It is intentionally not registered here;
// the public product makes no claim for nicosearchdate until a fixed-date,
// bounded API contract is available.
