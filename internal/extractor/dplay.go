package extractor

// Discovery/DPlay is a deliberately configuration-driven implementation of
// the public Discovery "disco-api" contract.  The individual sites differ in
// routing, realm, product client name, and API origin, but not in the content
// and playback protocol.  Keeping those facts in immutable configuration is
// important: a response must never be allowed to select an authentication
// destination or a different realm.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/protocol/dash"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	discoveryMaxIDBytes    = 512
	discoveryMaxStreaming  = 64
	discoveryMaxIncluded   = 256
	discoveryMaxTokenBytes = 4096
	discoveryMaxTokenJSON  = 32 << 10
	discoveryClientVersion = "27.43.0"
)

var (
	ErrDiscoveryRateLimited = errors.New("Discovery API rate limited")
	ErrDiscoveryNetwork     = errors.New("Discovery API network failure")
)

type discoveryRoute uint8

const (
	discoveryVideoRoute discoveryRoute = iota
	discoveryDPlayRoute
	discoveryPlusRoute
	discoveryGermanRoute
	discoveryShowRoute
)

type discoveryConfig struct {
	key, host, apiHost, realm, country, product string
	route                                       discoveryRoute
	// legacy uses the original /videoPlaybackInfo endpoint and has no product
	// client header.  hyoga is the German/Tele5 client convention.
	legacy, hyoga, india bool
	showBase, showPath   string
	showIndex            int
}

// DiscoveryDPlay is a thin, immutable site adapter.  It contains no token
// cache: bearer values are scoped to one Extract invocation and never escape
// into metadata, entries, or errors.
type DiscoveryDPlay struct {
	config   discoveryConfig
	deviceID func() (string, error) // test seam; nil selects crypto/rand.
}

func newDiscoveryDPlay(config discoveryConfig) DiscoveryDPlay { return DiscoveryDPlay{config: config} }
func (extractor DiscoveryDPlay) Name() string                 { return extractor.config.key }
func (extractor DiscoveryDPlay) Suitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	extractor.config = extractor.configFor(parsed)
	_, ok := extractor.target(parsed)
	return ok
}

func (extractor DiscoveryDPlay) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	extractor.config = extractor.configFor(parsed)
	target, ok := extractor.target(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if request.Transport == nil {
		return Extraction{}, fmt.Errorf("%w: missing Discovery transport", ErrInvalidMetadata)
	}
	if extractor.config.route == discoveryShowRoute {
		return extractor.extractShow(ctx, request, target)
	}
	if extractor.config.key == "discoverynetworksde" {
		return extractor.extractGerman(ctx, request, target)
	}
	if extractor.config.key == "tele5" {
		if target.canonical == "" {
			referer, err := url.Parse(request.Referer)
			if err != nil {
				return Extraction{}, ErrUnsupported
			}
			publicTarget, ok := extractor.target(referer)
			if !ok || publicTarget.canonical == "" {
				return Extraction{}, ErrUnsupported
			}
			target.canonical = publicTarget.canonical
			return extractor.extractVideo(ctx, request, target)
		}
		return extractor.extractTele5(ctx, request, target)
	}
	return extractor.extractVideo(ctx, request, target)
}

type discoveryTarget struct{ displayID, canonical string }

func (extractor DiscoveryDPlay) target(parsed *url.URL) (discoveryTarget, bool) {
	if extractor.config.key == "tele5" && parsed != nil && parsed.Scheme == "discovery" && strings.HasPrefix(parsed.Opaque, "tele5:") {
		id := strings.TrimPrefix(parsed.Opaque, "tele5:")
		if discoveryTele5ID(id) {
			return discoveryTarget{displayID: id}, true
		}
		return discoveryTarget{}, false
	}
	if hostedRejectUnsafeURL(parsed) || len(parsed.RawQuery) > 4096 {
		return discoveryTarget{}, false
	}
	config := extractor.config
	if !discoveryHostMatches(strings.ToLower(parsed.Hostname()), config) {
		return discoveryTarget{}, false
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return discoveryTarget{}, false
	}
	for _, segment := range segments {
		if !discoverySegment(segment) {
			return discoveryTarget{}, false
		}
	}
	var id string
	switch config.route {
	case discoveryDPlayRoute:
		if config.key == "tele5" {
			if len(segments) != 2 && len(segments) != 3 {
				return discoveryTarget{}, false
			}
			id = strings.Join(segments, "/")
			break
		}
		if len(segments) != 3 {
			return discoveryTarget{}, false
		}
		if config.key == "hgtvde" && segments[0] != "sendungen" {
			return discoveryTarget{}, false
		}
		id = segments[1] + "/" + segments[2]
	case discoveryVideoRoute:
		if len(segments) != 3 || (segments[0] != "video" && !(config.key == "discoveryplusindia" && segments[0] == "videos")) {
			return discoveryTarget{}, false
		}
		id = segments[1] + "/" + segments[2]
	case discoveryPlusRoute:
		if config.key == "discoveryplus" && segments[0] == "it" {
			return discoveryTarget{}, false
		}
		if config.key == "discoveryplusitaly" && (len(segments) == 0 || segments[0] != "it") {
			return discoveryTarget{}, false
		}
		index := 0
		if segments[index] == "it" || discoveryCountry(segments[index]) {
			index++
		}
		if len(segments) > index && segments[index] == "video" {
			index++
		} else {
			return discoveryTarget{}, false
		}
		if len(segments) > index && (segments[index] == "sport" || segments[index] == "olympics") {
			index++
		}
		if len(segments) != index+2 {
			return discoveryTarget{}, false
		}
		id = segments[index] + "/" + segments[index+1]
	case discoveryGermanRoute:
		if len(segments) < 3 || (segments[0] != "programme" && segments[0] != "show" && segments[0] != "sendungen") {
			return discoveryTarget{}, false
		}
		index := 2
		if len(segments) > index && segments[index] == "video" {
			index++
		}
		if len(segments) != index+1 {
			return discoveryTarget{}, false
		}
		id = segments[1] + "/" + segments[index]
	case discoveryShowRoute:
		if len(segments) != 2 || segments[0] != config.showPath {
			return discoveryTarget{}, false
		}
		id = segments[1]
	default:
		return discoveryTarget{}, false
	}
	if len(id) == 0 || len(id) > discoveryMaxIDBytes {
		return discoveryTarget{}, false
	}
	return discoveryTarget{displayID: id, canonical: "https://" + strings.ToLower(parsed.Hostname()) + parsed.EscapedPath()}, true
}

func (extractor DiscoveryDPlay) configFor(parsed *url.URL) discoveryConfig {
	config, host := extractor.config, strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	if config.key == "dplay" {
		country := ""
		switch {
		case strings.HasPrefix(host, "dplay."):
			country = strings.TrimPrefix(host, "dplay.")
		case strings.HasPrefix(host, "discoveryplus."):
			country = strings.TrimPrefix(host, "discoveryplus.")
		case strings.HasSuffix(host, ".dplay.com"):
			country = strings.TrimSuffix(host, ".dplay.com")
		}
		valid := (strings.HasPrefix(host, "dplay.") && discoveryDPlayCountry(country)) || (strings.HasPrefix(host, "discoveryplus.") && discoveryLegacyPlusCountry(country)) || (strings.HasSuffix(host, ".dplay.com") && discoveryLegacySubdomainCountry(country))
		if valid {
			config.host = host
			config.country, config.realm = country, "dplay"+country
			if strings.HasPrefix(host, "dplay.") {
				config.apiHost = "disco-api." + host
			} else {
				config.apiHost = "eu2-prod.disco-api.com"
			}
		}
	}
	if config.key == "discoveryplus" {
		segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
		country := "us"
		if len(segments) > 0 && discoveryCountry(segments[0]) {
			country = segments[0]
		}
		config.country, config.product = country, "dplus_"+country
		if country == "br" || country == "ca" || country == "us" {
			config.apiHost, config.realm = "us1-prod-direct.discoveryplus.com", "go"
		} else {
			config.apiHost, config.realm = "eu1-prod-direct.discoveryplus.com", "dplay"
		}
	}
	if config.key == "discoverynetworksde" {
		if host == "dmax.de" || host == "tlc.de" {
			config.host = host
			config.realm = strings.ReplaceAll(host, ".", "")
		}
	}
	return config
}

func discoveryHostMatches(host string, config discoveryConfig) bool {
	configured := config.host
	if host == configured {
		return true
	}
	if strings.HasPrefix(configured, "www.") && host == strings.TrimPrefix(configured, "www.") || host == "www."+configured {
		return true
	}
	bare := strings.TrimPrefix(host, "www.")
	switch config.key {
	case "dplay":
		return (strings.HasPrefix(bare, "dplay.") && discoveryDPlayCountry(strings.TrimPrefix(bare, "dplay."))) || (strings.HasPrefix(bare, "discoveryplus.") && discoveryLegacyPlusCountry(strings.TrimPrefix(bare, "discoveryplus."))) || (strings.HasSuffix(bare, ".dplay.com") && discoveryLegacySubdomainCountry(strings.TrimSuffix(bare, ".dplay.com")))
	case "discoverynetworksde":
		return bare == "dmax.de" || bare == "tlc.de"
	case "godiscovery":
		return bare == "discovery.com" || bare == "go.discovery.com"
	case "travelchannel", "cookingchannel", "hgtvusa", "foodnetwork":
		return bare == configured || bare == "watch."+configured
	case "tlc":
		return bare == "tlc.com" || bare == "go.tlc.com"
	}
	return false
}
func discoveryCountry(value string) bool {
	return len(value) == 2 && value[0] >= 'a' && value[0] <= 'z' && value[1] >= 'a' && value[1] <= 'z'
}

func discoveryDPlayCountry(host string) bool {
	allowed := map[string]bool{"dk": true, "fi": true, "jp": true, "se": true, "no": true}
	return allowed[host]
}

func discoveryLegacyPlusCountry(host string) bool {
	allowed := map[string]bool{"dk": true, "es": true, "fi": true, "it": true, "se": true, "no": true}
	return allowed[host]
}

func discoveryLegacySubdomainCountry(host string) bool { return host == "es" || host == "it" }
func discoverySegment(value string) bool {
	if value == "" || len(value) > 256 || strings.Contains(strings.ToLower(value), "%") {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func (extractor DiscoveryDPlay) apiBase() string { return "https://" + extractor.config.apiHost + "/" }

func (extractor DiscoveryDPlay) extractVideo(ctx context.Context, request Request, target discoveryTarget) (Extraction, error) {
	authorization, err := extractor.authorization(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	headers := extractor.headers(target.canonical, authorization)
	var content discoveryContentResponse
	endpoint, err := discoveryContentURL(extractor.apiBase(), target.displayID)
	if err != nil {
		return Extraction{}, err
	}
	if err := discoveryRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &content); err != nil {
		return Extraction{}, discoveryError(err)
	}
	if !discoverySegment(content.Data.ID) || len(content.Data.ID) > discoveryMaxIDBytes || strings.TrimSpace(content.Data.Attributes.Name) == "" {
		return Extraction{}, fmt.Errorf("%w: malformed Discovery content", ErrInvalidMetadata)
	}
	var playback discoveryPlaybackResponse
	playbackURL := extractor.apiBase() + "playback/videoPlaybackInfo/" + url.PathEscape(content.Data.ID)
	method, body := http.MethodGet, []byte(nil)
	if !extractor.config.legacy {
		playbackURL = extractor.apiBase() + "playback/v3/videoPlaybackInfo"
		method = http.MethodPost
		body, err = json.Marshal(struct {
			DeviceInfo struct {
				AdBlocker    bool `json:"adBlocker"`
				DRMSupported bool `json:"drmSupported"`
			} `json:"deviceInfo"`
			VideoID            string         `json:"videoId"`
			WisteriaProperties map[string]any `json:"wisteriaProperties"`
		}{DeviceInfo: struct {
			AdBlocker    bool `json:"adBlocker"`
			DRMSupported bool `json:"drmSupported"`
		}{false, false}, VideoID: content.Data.ID, WisteriaProperties: map[string]any{}})
		if err != nil {
			return Extraction{}, fmt.Errorf("%w: encode Discovery playback request", ErrInvalidMetadata)
		}
	}
	if err := discoveryRequestJSON(ctx, request.Transport, method, playbackURL, body, headers, &playback); err != nil {
		return Extraction{}, discoveryError(err)
	}
	return extractor.media(ctx, request.Transport, content, playback, target)
}

func discoveryContentURL(base, displayID string) (string, error) {
	parts := strings.Split(displayID, "/")
	if (len(parts) != 1 && len(parts) != 2) || !discoverySegment(parts[0]) || (len(parts) == 2 && !discoverySegment(parts[1])) {
		return "", fmt.Errorf("%w: invalid Discovery content identity", ErrInvalidMetadata)
	}
	query := url.Values{
		"fields[channel]": {"name"}, "fields[image]": {"height,src,width"}, "fields[show]": {"name"}, "fields[tag]": {"name"},
		"fields[video]": {"description,episodeNumber,name,publishStart,seasonNumber,videoDuration"}, "include": {"images,primaryChannel,show,tags"},
	}
	path := url.PathEscape(parts[0])
	if len(parts) == 2 {
		path += "/" + url.PathEscape(parts[1])
	}
	return base + "content/videos/" + path + "?" + query.Encode(), nil
}

type discoveryCookieTransport interface {
	Cookies(string) ([]*http.Cookie, error)
}

// discoveryRequestJSON preserves the scoped/no-redirect invariant while
// retaining the small structured Discovery error code needed to distinguish
// a geo block from an ordinary authorization failure. Response text is never
// returned or interpolated into an error.
func discoveryRequestJSON(ctx context.Context, transport Transport, method, rawURL string, body []byte, headers http.Header, target any) error {
	scoped, ok := transport.(ScopedAuthorizationNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return ErrInvalidMetadata
	}
	request.Header = headers.Clone()
	response, err := scoped.DoWithScopedAuthorizationNoRedirect(ctx, request)
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("%w: nil Discovery response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDiscoveryNetwork
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}
		if response.StatusCode == http.StatusBadRequest {
			decoder := json.NewDecoder(bytes.NewReader(data))
			if err := decoder.Decode(&failure); err != nil {
				return fmt.Errorf("%w: invalid Discovery error JSON", ErrInvalidMetadata)
			}
			if err := ensureJSONEOF(decoder); err != nil {
				return fmt.Errorf("%w: trailing Discovery error JSON", ErrInvalidMetadata)
			}
		} else {
			_ = json.Unmarshal(data, &failure)
		}
		if len(failure.Errors) > 0 {
			switch failure.Errors[0].Code {
			case "access.denied.geoblocked":
				return ErrRegionRestricted
			case "access.denied.missingpackage", "invalid.token":
				return ErrAuthentication
			}
		}
		return &HTTPStatusError{Code: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Discovery JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Discovery JSON", ErrInvalidMetadata)
	}
	return nil
}

func discoveryShowRequestJSON(ctx context.Context, transport Transport, method, rawURL string, headers http.Header, target any) error {
	scoped, ok := transport.(ScopedAuthenticationNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return ErrInvalidMetadata
	}
	request.Header = headers.Clone()
	response, err := scoped.DoWithScopedAuthenticationNoRedirect(ctx, request)
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("%w: nil Discovery show response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDiscoveryNetwork
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var failure struct {
			Errors []struct {
				Code string `json:"code"`
			} `json:"errors"`
		}
		if response.StatusCode == http.StatusBadRequest {
			decoder := json.NewDecoder(bytes.NewReader(data))
			if err := decoder.Decode(&failure); err != nil {
				return fmt.Errorf("%w: invalid Discovery show error JSON", ErrInvalidMetadata)
			}
			if err := ensureJSONEOF(decoder); err != nil {
				return fmt.Errorf("%w: trailing Discovery show error JSON", ErrInvalidMetadata)
			}
		} else {
			_ = json.Unmarshal(data, &failure)
		}
		if len(failure.Errors) > 0 {
			switch failure.Errors[0].Code {
			case "access.denied.geoblocked":
				return ErrRegionRestricted
			case "access.denied.missingpackage", "invalid.token":
				return ErrAuthentication
			}
		}
		return &HTTPStatusError{Code: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Discovery show JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Discovery show JSON", ErrInvalidMetadata)
	}
	return nil
}

func (extractor DiscoveryDPlay) authorization(ctx context.Context, transport Transport) (string, error) {
	base := extractor.apiBase()
	if cookies, ok := transport.(discoveryCookieTransport); ok {
		found, err := cookies.Cookies(base)
		if err != nil {
			return "", ErrTransportIsolation
		}
		for _, cookie := range found {
			if cookie != nil && cookie.Name == "st" && discoveryToken(cookie.Value) {
				return "Bearer " + cookie.Value, nil
			}
		}
	}
	query := url.Values{"realm": {extractor.config.realm}}
	if !extractor.config.legacy {
		newDeviceID := extractor.deviceID
		if newDeviceID == nil {
			newDeviceID = discoveryDeviceID
		}
		deviceID, err := newDeviceID()
		if err != nil || !discoveryDeviceIDValid(deviceID) {
			return "", fmt.Errorf("%w: generate Discovery device identity", ErrInvalidMetadata)
		}
		query.Set("deviceId", deviceID)
	}
	endpoint := base + "token?" + query.Encode()
	var response discoveryTokenResponse
	if err := discoveryTokenJSON(ctx, transport, endpoint, &response); err != nil {
		return "", discoveryError(err)
	}
	if !discoveryToken(response.Data.Attributes.Token) {
		return "", fmt.Errorf("%w: malformed Discovery token", ErrAuthentication)
	}
	return "Bearer " + response.Data.Attributes.Token, nil
}

func discoveryTokenJSON(ctx context.Context, transport Transport, rawURL string, target any) error {
	isolate, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ErrInvalidMetadata
	}
	response, err := isolate.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("%w: nil Discovery token response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, discoveryMaxTokenJSON+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDiscoveryNetwork
	}
	if len(data) > discoveryMaxTokenJSON {
		return ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Code: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Discovery token JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Discovery token JSON", ErrInvalidMetadata)
	}
	return nil
}

func discoveryPublicJSON(ctx context.Context, transport Transport, rawURL string, target any) error {
	isolate, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return ErrInvalidMetadata
	}
	response, err := isolate.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return err
	}
	if response == nil || response.Body == nil {
		return fmt.Errorf("%w: nil Discovery public response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxExtractorJSONBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrDiscoveryNetwork
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &HTTPStatusError{Code: response.StatusCode}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: invalid Discovery public JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return fmt.Errorf("%w: trailing Discovery public JSON", ErrInvalidMetadata)
	}
	return nil
}

func discoveryDeviceID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("%w: generate Discovery device identity", ErrInvalidMetadata)
	}
	return hex.EncodeToString(bytes), nil
}
func discoveryDeviceIDValid(deviceID string) bool {
	if len(deviceID) != 32 {
		return false
	}
	for _, r := range deviceID {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}
func discoveryToken(token string) bool {
	if len(token) == 0 || len(token) > discoveryMaxTokenBytes || strings.ContainsAny(token, " \t\r\n") {
		return false
	}
	for _, r := range token {
		if r < 0x21 || r > 0x7e {
			return false
		}
	}
	return true
}

func (extractor DiscoveryDPlay) headers(referer, authorization string) http.Header {
	headers := make(http.Header)
	headers.Set("Referer", referer)
	headers.Set("Authorization", authorization)
	if extractor.config.hyoga {
		headers.Set("x-disco-params", "realm="+extractor.config.realm)
		headers.Set("x-disco-client", "Alps:HyogaPlayer:0.0.0")
		return headers
	}
	if extractor.config.legacy {
		return headers
	}
	switch extractor.config.key {
	case "discoveryplus":
		headers.Set("x-disco-params", "realm="+extractor.config.realm+",siteLookupKey="+extractor.config.product)
		headers.Set("x-disco-client", "WEB:UNKNOWN:dplus_us:"+discoveryClientVersion)
	case "discoveryplusitaly":
		headers.Set("x-disco-params", "realm="+extractor.config.realm+",siteLookupKey=dplus_it")
		headers.Set("x-disco-client", "WEB:UNKNOWN:dplus_us:"+discoveryClientVersion)
	case "discoveryplusindia":
		headers.Set("x-disco-params", "realm="+extractor.config.realm)
		headers.Set("x-disco-client", "WEB:UNKNOWN:"+extractor.config.product+":17.0.0")
	default:
		headers.Set("x-disco-params", "realm="+extractor.config.realm+",siteLookupKey="+extractor.config.product)
		headers.Set("x-disco-client", "WEB:UNKNOWN:"+extractor.config.product+":"+discoveryClientVersion)
	}
	return headers
}

type discoveryTokenResponse struct {
	Data struct {
		Attributes struct {
			Token string `json:"token"`
		} `json:"attributes"`
	} `json:"data"`
}
type discoveryContentResponse struct {
	Data     discoveryContent    `json:"data"`
	Included []discoveryIncluded `json:"included"`
}
type discoveryContent struct {
	ID         string              `json:"id"`
	Attributes discoveryAttributes `json:"attributes"`
}
type discoveryAttributes struct {
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	PublishStart  string        `json:"publishStart"`
	VideoDuration hostingNumber `json:"videoDuration"`
	SeasonNumber  hostingNumber `json:"seasonNumber"`
	EpisodeNumber hostingNumber `json:"episodeNumber"`
}
type discoveryIncluded struct {
	Type       string `json:"type"`
	Attributes struct {
		Name   string        `json:"name"`
		Src    string        `json:"src"`
		Width  hostingNumber `json:"width"`
		Height hostingNumber `json:"height"`
	} `json:"attributes"`
}
type discoveryPlaybackResponse struct{ Streaming []discoveryStream }
type discoveryStream struct{ Type, URL string }

func (response *discoveryPlaybackResponse) UnmarshalJSON(data []byte) error {
	response.Streaming = nil
	var envelope struct {
		Data struct {
			Attributes struct {
				Streaming json.RawMessage `json:"streaming"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	raw := bytes.TrimSpace(envelope.Data.Attributes.Streaming)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil
	}
	if raw[0] == '{' {
		var legacy map[string]struct {
			URL string `json:"url"`
		}
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return err
		}
		if len(legacy) > discoveryMaxStreaming {
			return fmt.Errorf("streaming overflow")
		}
		kinds := make([]string, 0, len(legacy))
		for kind := range legacy {
			kinds = append(kinds, kind)
		}
		sort.Strings(kinds)
		for _, kind := range kinds {
			item := legacy[kind]
			if len(kind) > 256 || len(item.URL) > sharedHostingMaxURLBytes {
				return fmt.Errorf("streaming field overflow")
			}
			response.Streaming = append(response.Streaming, discoveryStream{Type: kind, URL: item.URL})
		}
		return nil
	}
	if raw[0] != '[' {
		return fmt.Errorf("invalid streaming shape")
	}
	var v3 []struct {
		Type string `json:"type"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(raw, &v3); err != nil {
		return err
	}
	if len(v3) > discoveryMaxStreaming {
		return fmt.Errorf("streaming overflow")
	}
	for _, item := range v3 {
		if len(item.Type) > 256 || len(item.URL) > sharedHostingMaxURLBytes {
			return fmt.Errorf("streaming field overflow")
		}
		response.Streaming = append(response.Streaming, discoveryStream{Type: item.Type, URL: item.URL})
	}
	return nil
}

func (extractor DiscoveryDPlay) media(ctx context.Context, transport Transport, content discoveryContentResponse, playback discoveryPlaybackResponse, target discoveryTarget) (Extraction, error) {
	if len(content.Included) > discoveryMaxIncluded || len(playback.Streaming) == 0 || len(playback.Streaming) > discoveryMaxStreaming {
		return Extraction{}, fmt.Errorf("%w: malformed Discovery media", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(playback.Streaming))
	seenFormats := make(map[string]bool, len(playback.Streaming))
	seenFormatURLs := make(map[string]bool, discoveryMaxFormats)
	subtitles := make(map[string][]value.Value)
	var manifestErr error
	for _, stream := range playback.Streaming {
		if err := ctx.Err(); err != nil {
			return Extraction{}, err
		}
		key := strings.ToLower(stream.Type) + "\x00" + stream.URL
		if seenFormats[key] {
			continue
		}
		seenFormats[key] = true
		kind := strings.ToLower(stream.Type)
		if kind == "hls" || strings.HasSuffix(strings.ToLower(strings.Split(stream.URL, "?")[0]), ".m3u8") {
			expanded, tracks, err := discoveryHLSManifest(ctx, transport, stream.URL)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return Extraction{}, err
				}
				manifestErr = err
				continue
			}
			discoveryAppendFormats(&formats, seenFormatURLs, expanded)
			discoveryMergeSubtitles(subtitles, tracks)
			continue
		}
		if kind == "dash" || strings.HasSuffix(strings.ToLower(strings.Split(stream.URL, "?")[0]), ".mpd") {
			expanded, tracks, err := discoveryDASHManifest(ctx, transport, stream.URL)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return Extraction{}, err
				}
				manifestErr = err
				continue
			}
			discoveryAppendFormats(&formats, seenFormatURLs, expanded)
			discoveryMergeSubtitles(subtitles, tracks)
			continue
		}
		if format, ok := strictHostedURLFormat(strings.ToLower(stream.Type), stream.URL); ok {
			discoveryAppendFormats(&formats, seenFormatURLs, []value.Value{value.ObjectValue(format)})
		}
	}
	formats = discoveryStableFormats(formats)
	if len(formats) == 0 {
		if manifestErr != nil {
			return Extraction{}, manifestErr
		}
		return Extraction{}, ErrUnavailable
	}
	var creator, series string
	tags := make([]value.Value, 0)
	thumbnails := make([]value.Value, 0)
	for _, item := range content.Included {
		switch item.Type {
		case "channel":
			creator = strings.TrimSpace(item.Attributes.Name)
		case "show":
			series = strings.TrimSpace(item.Attributes.Name)
		case "tag":
			if tag := strings.TrimSpace(item.Attributes.Name); tag != "" && len(tags) < discoveryMaxIncluded {
				tags = append(tags, value.String(tag))
			}
		case "image":
			if thumbnail, ok := strictHostedURLFormat("thumbnail", item.Attributes.Src); ok {
				thumbnail.Delete("format_id")
				thumbnail.Delete("protocol")
				if width := item.Attributes.Width.int64(); width > 0 {
					thumbnail.Set("width", value.Int(width))
				}
				if height := item.Attributes.Height.int64(); height > 0 {
					thumbnail.Set("height", value.Int(height))
				}
				thumbnails = append(thumbnails, value.ObjectValue(thumbnail))
			}
		}
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(content.Data.ID)}, value.Field{Key: "display_id", Value: value.String(target.displayID)}, value.Field{Key: "title", Value: value.String(strings.TrimSpace(content.Data.Attributes.Name))}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)}, value.Field{Key: "formats", Value: value.List(formats...)}))
	hostedSetString(info.Fields(), "description", strings.TrimSpace(content.Data.Attributes.Description))
	hostedSetString(info.Fields(), "creator", creator)
	hostedSetString(info.Fields(), "series", series)
	if duration := content.Data.Attributes.VideoDuration.float64() / 1000; duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	if season := content.Data.Attributes.SeasonNumber.int64(); season > 0 {
		info.Set("season_number", value.Int(season))
	}
	if episode := content.Data.Attributes.EpisodeNumber.int64(); episode > 0 {
		info.Set("episode_number", value.Int(episode))
	}
	if timestamp, ok := discoveryTimestamp(content.Data.Attributes.PublishStart); ok {
		info.Set("timestamp", value.Int(timestamp))
	}
	if len(tags) > 0 {
		info.Set("tags", value.List(tags...))
	}
	if len(thumbnails) > 0 {
		info.Set("thumbnails", value.List(thumbnails...))
	}
	if len(subtitles) > 0 {
		info.Set("subtitles", discoverySubtitleValue(subtitles))
	}
	if referer := extractor.downloadReferer(); referer != "" {
		info.Set("http_headers", hostedHeadersValue(http.Header{"Referer": {referer}}))
	}
	return Media(info), nil
}

func (extractor DiscoveryDPlay) downloadReferer() string {
	switch extractor.config.key {
	case "dplay":
		return strings.TrimPrefix(extractor.config.host, "www.")
	case "discoveryplusindia":
		return "https://www.discoveryplus.in/"
	default:
		return ""
	}
}

const discoveryMaxFormats = 256

func discoveryAppendFormats(dst *[]value.Value, seen map[string]bool, additions []value.Value) {
	for _, item := range additions {
		object, ok := item.Object()
		if !ok {
			continue
		}
		rawURL, ok := object.Lookup("url").StringValue()
		if !ok || seen[rawURL] {
			continue
		}
		seen[rawURL] = true
		if len(*dst) >= discoveryMaxFormats {
			continue
		}
		*dst = append(*dst, item)
	}
}

func discoveryStableFormats(formats []value.Value) []value.Value {
	seenURLs, ids := make(map[string]bool), make(map[string]int)
	out := make([]value.Value, 0, min(len(formats), discoveryMaxFormats))
	for _, item := range formats {
		object, ok := item.Object()
		if !ok {
			continue
		}
		rawURL, ok := object.Lookup("url").StringValue()
		if !ok || seenURLs[rawURL] {
			continue
		}
		seenURLs[rawURL] = true
		if len(out) >= discoveryMaxFormats {
			continue
		}
		id, _ := object.Lookup("format_id").StringValue()
		if id == "" {
			id = "http"
		}
		ids[id]++
		if ids[id] > 1 {
			object = object.Clone()
			object.Set("format_id", value.String(id+"-"+strconv.Itoa(ids[id])))
		}
		out = append(out, value.ObjectValue(object))
	}
	return out
}

const discoveryMaxManifestBytes = 4 << 20

func discoveryManifest(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if !strictValidHostedHTTPURL(rawURL) {
		return nil, fmt.Errorf("%w: unsafe Discovery manifest", ErrInvalidMetadata)
	}
	isolate, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrInvalidMetadata
	}
	response, err := isolate.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, ErrDiscoveryNetwork
	}
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("%w: nil Discovery manifest response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, discoveryError(&HTTPStatusError{Code: response.StatusCode})
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, discoveryMaxManifestBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrDiscoveryNetwork
	}
	if len(payload) > discoveryMaxManifestBytes {
		return nil, fmt.Errorf("%w: Discovery manifest too large", ErrInvalidMetadata)
	}
	return payload, nil
}
func discoveryHLSManifest(ctx context.Context, transport Transport, rawURL string) ([]value.Value, map[string][]value.Value, error) {
	payload, err := discoveryManifest(ctx, transport, rawURL)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil, fmt.Errorf("%w: empty HLS manifest", ErrInvalidMetadata)
	}
	// Preflight before hls.Parse allocates its Variant slice. The extractor's
	// per-manifest budget is deliberately far lower than the shared parser's
	// generic safety ceiling.
	if bytes.Count(payload, []byte("#EXT-X-STREAM-INF")) > 64 {
		return nil, nil, fmt.Errorf("%w: HLS variant overflow", ErrInvalidMetadata)
	}
	playlist, err := hls.Parse(rawURL, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: malformed HLS manifest", ErrInvalidMetadata)
	}
	tracks, _ := hls.ParseMasterSubtitles(rawURL, payload)
	formats := make([]value.Value, 0, min(len(playlist.Variants), 64))
	seen := make(map[string]bool, min(len(playlist.Variants), 64))
	for index, variant := range playlist.Variants {
		if seen[variant.URL] {
			continue
		}
		seen[variant.URL] = true
		if len(formats) >= discoveryMaxStreaming {
			continue
		}
		if format, ok := strictHostedURLFormat("hls-"+strconv.Itoa(index+1), variant.URL); ok {
			if variant.Bandwidth > 0 {
				format.Set("tbr", value.Float(float64(variant.Bandwidth)/1000))
			}
			formats = append(formats, value.ObjectValue(format))
		}
	}
	// A media playlist has no master variants but is itself a valid HLS
	// rendition. Preserve it instead of turning valid playback into no formats.
	if len(formats) == 0 && playlist.Media != nil && len(playlist.Media.Segments) > 0 {
		if format, ok := strictHostedURLFormat("hls", rawURL); ok {
			formats = append(formats, value.ObjectValue(format))
		}
	}
	subs := make(map[string][]value.Value)
	for _, track := range tracks {
		if len(subs) >= 32 {
			break
		}
		if strictValidHostedHTTPURL(track.URL) && discoveryLanguage(track.Language) {
			subs[track.Language] = append(subs[track.Language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(track.URL)}, value.Field{Key: "ext", Value: value.String("vtt")}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})))
		}
	}
	if len(formats) == 0 {
		return nil, nil, fmt.Errorf("%w: HLS manifest has no playable renditions", ErrInvalidMetadata)
	}
	return formats, subs, nil
}
func discoveryDASHManifest(ctx context.Context, transport Transport, rawURL string) ([]value.Value, map[string][]value.Value, error) {
	payload, err := discoveryManifest(ctx, transport, rawURL)
	if err != nil {
		return nil, nil, err
	}
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, nil, fmt.Errorf("%w: empty DASH manifest", ErrInvalidMetadata)
	}
	if bytes.Count(payload, []byte("<Representation")) > discoveryMaxStreaming {
		return nil, nil, fmt.Errorf("%w: DASH representation overflow", ErrInvalidMetadata)
	}
	manifest, err := dash.Parse(rawURL, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: malformed DASH manifest", ErrInvalidMetadata)
	}
	if len(manifest.Representations) > discoveryMaxStreaming {
		return nil, nil, fmt.Errorf("%w: DASH representation overflow", ErrInvalidMetadata)
	}
	// The downstream DASH pipeline selects representations from an MPD. Until
	// extraction carries representation IDs end-to-end, expose one honest MPD
	// rather than several fake quality choices all pointing at the same URL.
	formats := make([]value.Value, 0, 1)
	playable := false
	for _, rep := range manifest.Representations {
		if rep.ContentType != "text" {
			playable = true
			break
		}
	}
	if playable {
		if format, ok := strictHostedURLFormat("dash", rawURL); ok {
			formats = append(formats, value.ObjectValue(format))
		}
	}
	text, _ := dash.ParseTextRepresentations(rawURL, payload)
	subs := make(map[string][]value.Value)
	for _, track := range text {
		if len(subs) >= 32 {
			break
		}
		if strictValidHostedHTTPURL(track.URL) && discoveryLanguage(track.Language) {
			subs[track.Language] = append(subs[track.Language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(track.URL)}, value.Field{Key: "_credential_isolated", Value: value.Bool(true)})))
		}
	}
	if len(formats) == 0 {
		return nil, nil, fmt.Errorf("%w: DASH manifest has no playable representations", ErrInvalidMetadata)
	}
	return formats, subs, nil
}
func discoveryLanguage(input string) bool {
	return len(input) > 0 && len(input) <= 32 && !strings.ContainsAny(input, "/\\\r\n")
}
func discoveryMergeSubtitles(dst, src map[string][]value.Value) {
	for language, tracks := range src {
		if !discoveryLanguage(language) {
			continue
		}
		seen := make(map[string]bool)
		for _, current := range dst[language] {
			if object, ok := current.Object(); ok {
				if raw, ok := object.Lookup("url").StringValue(); ok {
					seen[raw] = true
				}
			}
		}
		for _, track := range tracks {
			if len(dst[language]) >= 16 {
				break
			}
			object, ok := track.Object()
			if !ok {
				continue
			}
			raw, ok := object.Lookup("url").StringValue()
			if !ok || seen[raw] {
				continue
			}
			seen[raw] = true
			dst[language] = append(dst[language], track)
		}
	}
}
func discoverySubtitleValue(subs map[string][]value.Value) value.Value {
	languages := make([]string, 0, len(subs))
	for language := range subs {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	object := value.NewObject()
	for _, language := range languages {
		object.Set(language, value.List(subs[language]...))
	}
	return value.ObjectValue(object)
}

func discoveryTimestamp(raw string) (int64, bool) {
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0, false
	}
	return parsed.Unix(), true
}
func discoveryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrAuthentication) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrRegionRestricted) || errors.Is(err, ErrDiscoveryRateLimited) || errors.Is(err, ErrDiscoveryNetwork) || errors.Is(err, ErrTransportIsolation) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		case http.StatusTooManyRequests:
			return ErrDiscoveryRateLimited
		default:
			if status.Code >= 500 {
				return ErrDiscoveryNetwork
			}
			return fmt.Errorf("Discovery service HTTP status %d", status.Code)
		}
	}
	return ErrDiscoveryNetwork
}

func (extractor DiscoveryDPlay) extractShow(ctx context.Context, request Request, target discoveryTarget) (Extraction, error) {
	authentication, err := extractor.authorization(ctx, request.Transport)
	if err != nil {
		return Extraction{}, err
	}
	headers := extractor.showHeaders(authentication)
	var cms discoveryShowCMS
	endpoint := extractor.apiBase() + "cms/routes/" + url.PathEscape(extractor.config.showPath) + "/" + url.PathEscape(target.displayID) + "?include=default"
	if err := discoveryShowRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, headers, &cms); err != nil {
		return Extraction{}, discoveryError(err)
	}
	plan, err := extractor.showPlan(cms, target.displayID)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(target.displayID)}, value.Field{Key: "title", Value: value.String(target.displayID)}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)}))
	sequence := discoveryShowEntries{extractor: extractor, transport: request.Transport, authentication: authentication, plan: plan}
	return Playlist(info, sequence)
}

func (extractor DiscoveryDPlay) showHeaders(authentication string) http.Header {
	headers := make(http.Header)
	headers.Set("x-disco-params", "realm="+extractor.config.realm)
	headers.Set("Referer", extractor.config.showBase)
	headers.Set("Authentication", authentication)
	if extractor.config.key == "discoveryplusindia" || extractor.config.key == "discoveryplusindiashow" {
		headers.Set("x-disco-client", "WEB:UNKNOWN:dplus-india:prod")
	} else {
		headers.Set("x-disco-client", "WEB:UNKNOWN:dplay-client:2.6.0")
	}
	return headers
}

type discoveryShowCMS struct {
	Included []struct {
		Attributes struct {
			Component struct {
				MandatoryParams string `json:"mandatoryParams"`
				Filters         []struct {
					Options []struct {
						ID hostingNumber `json:"id"`
					} `json:"options"`
				} `json:"filters"`
			} `json:"component"`
		} `json:"attributes"`
	} `json:"included"`
}
type discoveryShowPlan struct {
	showID  string
	seasons []string
}

func (extractor DiscoveryDPlay) showPlan(cms discoveryShowCMS, _ string) (discoveryShowPlan, error) {
	if len(cms.Included) <= extractor.config.showIndex || len(cms.Included) > discoveryMaxIncluded {
		return discoveryShowPlan{}, fmt.Errorf("%w: malformed Discovery show CMS", ErrInvalidMetadata)
	}
	component := cms.Included[extractor.config.showIndex].Attributes.Component
	parts := strings.Split(component.MandatoryParams, "=")
	showID := parts[len(parts)-1]
	if !discoverySegment(showID) || len(component.Filters) == 0 || len(component.Filters) > discoveryMaxIncluded || len(component.Filters[0].Options) == 0 || len(component.Filters[0].Options) > discoveryMaxIncluded {
		return discoveryShowPlan{}, fmt.Errorf("%w: malformed Discovery show identity", ErrInvalidMetadata)
	}
	plan := discoveryShowPlan{showID: showID, seasons: make([]string, 0, len(component.Filters[0].Options))}
	for _, option := range component.Filters[0].Options {
		season := option.ID.string()
		if season == "" || !discoverySegment(season) {
			return discoveryShowPlan{}, fmt.Errorf("%w: malformed Discovery season", ErrInvalidMetadata)
		}
		plan.seasons = append(plan.seasons, season)
	}
	return plan, nil
}

type discoveryShowEntries struct {
	extractor      DiscoveryDPlay
	transport      Transport
	authentication string
	plan           discoveryShowPlan
}

func (entries discoveryShowEntries) Iterator() EntryIterator {
	return &discoveryShowIterator{entries: entries, seenPages: make(map[string]bool)}
}

type discoveryShowIterator struct {
	entries                                  discoveryShowEntries
	season, page, total, pages, entriesTotal int
	queued                                   []Entry
	seenPages                                map[string]bool
}

func (iterator *discoveryShowIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	for len(iterator.queued) == 0 {
		if iterator.season >= len(iterator.entries.plan.seasons) {
			return Entry{}, false, nil
		}
		if iterator.pages >= 10000 {
			return Entry{}, false, ErrPlaylistLimit
		}
		page, err := iterator.fetch(ctx)
		if err != nil {
			return Entry{}, false, err
		}
		fingerprint, err := discoveryShowPageFingerprint(page)
		if err != nil {
			return Entry{}, false, err
		}
		if len(page.Data) > 0 {
			fingerprint = iterator.entries.plan.seasons[iterator.season] + ":" + fingerprint
			if iterator.seenPages[fingerprint] {
				return Entry{}, false, fmt.Errorf("%w: repeated Discovery show response", ErrInvalidPlaylist)
			}
			iterator.seenPages[fingerprint] = true
		}
		iterator.page++
		iterator.pages++
		if iterator.total == 0 {
			iterator.total = page.Meta.TotalPages
			if iterator.total == 0 {
				iterator.total = 1
			}
			if iterator.total < 0 || iterator.total > 10000 {
				return Entry{}, false, fmt.Errorf("%w: invalid Discovery show page count", ErrInvalidPlaylist)
			}
		} else if page.Meta.TotalPages != 0 && page.Meta.TotalPages != iterator.total {
			return Entry{}, false, fmt.Errorf("%w: inconsistent Discovery show page count", ErrInvalidPlaylist)
		}
		for _, video := range page.Data {
			path := strings.Trim(video.Attributes.Path, "/")
			if !discoveryShowVideoPath(path) {
				return Entry{}, false, fmt.Errorf("%w: malformed Discovery episode path", ErrInvalidPlaylist)
			}
			if iterator.entriesTotal >= defaultMaxPlaylistEntries {
				return Entry{}, false, ErrPlaylistLimit
			}
			iterator.entriesTotal++
			episodeID, present, err := discoveryShowEpisodeID(video.ID)
			if err != nil {
				return Entry{}, false, err
			}
			if !present {
				episodeID = path
			}
			domain, key := iterator.entries.extractor.config.showBase+"videos/", "discoveryplusindia"
			if iterator.entries.extractor.config.key == "discoveryplusitalyshow" {
				key = "dplay"
			}
			iterator.queued = append(iterator.queued, Entry{URL: domain + path, ExtractorKey: key, ID: episodeID})
		}
		if iterator.page >= iterator.total {
			iterator.season++
			iterator.page, iterator.total = 0, 0
		}
	}
	entry := iterator.queued[0]
	iterator.queued = iterator.queued[1:]
	return entry, true, nil
}

func discoveryShowPageFingerprint(page discoveryShowPage) (string, error) {
	if len(page.Data) > 100 {
		return "", fmt.Errorf("%w: Discovery show page overflow", ErrInvalidPlaylist)
	}
	var builder strings.Builder
	builder.WriteString(strconv.Itoa(page.Meta.TotalPages))
	builder.WriteByte('|')
	for _, video := range page.Data {
		path := strings.Trim(video.Attributes.Path, "/")
		id, _, err := discoveryShowEpisodeID(video.ID)
		if err != nil || !discoveryShowVideoPath(path) {
			return "", fmt.Errorf("%w: malformed Discovery episode identity", ErrInvalidPlaylist)
		}
		if builder.Len()+len(id)+len(path)+2 > 8192 {
			return "", fmt.Errorf("%w: Discovery show page identity overflow", ErrInvalidPlaylist)
		}
		builder.WriteString(id)
		builder.WriteByte(':')
		builder.WriteString(path)
		builder.WriteByte('|')
	}
	return builder.String(), nil
}

type discoveryShowPage struct {
	Data []struct {
		ID         json.RawMessage `json:"id"`
		Attributes struct {
			Path string `json:"path"`
		} `json:"attributes"`
	} `json:"data"`
	Meta struct {
		TotalPages int `json:"totalPages"`
	} `json:"meta"`
}

func discoveryShowEpisodeID(raw json.RawMessage) (string, bool, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return "", false, nil
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || !discoverySegment(id) {
		return "", true, fmt.Errorf("%w: malformed Discovery episode ID", ErrInvalidPlaylist)
	}
	return id, true, nil
}

func (iterator *discoveryShowIterator) fetch(ctx context.Context) (discoveryShowPage, error) {
	var page discoveryShowPage
	query := url.Values{"sort": {"episodeNumber"}, "filter[seasonNumber]": {iterator.entries.plan.seasons[iterator.season]}, "filter[show.id]": {iterator.entries.plan.showID}, "page[size]": {"100"}, "page[number]": {strconv.Itoa(iterator.page + 1)}}
	headers := iterator.entries.extractor.showHeaders(iterator.entries.authentication)
	err := discoveryShowRequestJSON(ctx, iterator.entries.transport, http.MethodGet, iterator.entries.extractor.apiBase()+"content/videos?"+query.Encode(), headers, &page)
	if err != nil {
		return discoveryShowPage{}, discoveryError(err)
	}
	if len(page.Data) > 100 {
		return discoveryShowPage{}, fmt.Errorf("%w: Discovery show page overflow", ErrInvalidPlaylist)
	}
	return page, nil
}
func discoveryShowVideoPath(path string) bool {
	parts := strings.Split(path, "/")
	return len(parts) == 2 && discoverySegment(parts[0]) && discoverySegment(parts[1])
}

func (extractor DiscoveryDPlay) extractTele5(ctx context.Context, request Request, target discoveryTarget) (Extraction, error) {
	parts := strings.Split(target.displayID, "/")
	if len(parts) < 2 || len(parts) > 3 {
		return Extraction{}, ErrUnsupported
	}
	query := url.Values{"include": {"default"}, "filter[environment]": {"tele5"}, "v": {"2"}}
	endpoint := "https://public.aurora.enhanced.live/site/"
	if len(parts) == 2 {
		endpoint += "page/" + url.PathEscape(parts[1]) + "/"
		query.Set("parent_slug", parts[0])
	} else {
		endpoint += "shows/" + url.PathEscape(parts[1]) + "/"
		query.Set("filter[video.slug]", parts[2])
	}
	var cms discoveryTele5CMS
	if err := discoveryPublicJSON(ctx, request.Transport, endpoint+"?"+query.Encode(), &cms); err != nil {
		return Extraction{}, discoveryError(err)
	}
	if len(cms.Blocks) == 0 || len(cms.Blocks) > discoveryMaxIncluded {
		return Extraction{}, ErrUnavailable
	}
	entries, seen := make([]Entry, 0, len(cms.Blocks)), make(map[string]bool)
	for _, block := range cms.Blocks {
		if !discoveryTele5ID(block.VideoID) {
			continue
		}
		if seen[block.VideoID] {
			continue
		}
		seen[block.VideoID] = true
		entries = append(entries, Entry{URL: "discovery:tele5:" + block.VideoID, ExtractorKey: "tele5", ID: block.VideoID, Referer: target.canonical})
	}
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: malformed Tele5 CMS", ErrInvalidMetadata)
	}
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(strings.ReplaceAll(target.displayID, "/", "-"))}, value.Field{Key: "title", Value: value.String(target.displayID)}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)}))
	return Playlist(info, StaticEntries(entries...))
}

type discoveryTele5CMS struct {
	Blocks []struct {
		VideoID string `json:"videoId"`
	} `json:"blocks"`
}

func discoveryTele5ID(value string) bool {
	if len(value) == 0 || len(value) > discoveryMaxIDBytes {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func (extractor DiscoveryDPlay) extractGerman(ctx context.Context, request Request, target discoveryTarget) (Extraction, error) {
	parts := strings.Split(target.displayID, "/")
	if len(parts) != 2 {
		return Extraction{}, ErrUnsupported
	}
	environment := strings.Split(extractor.config.realm, "de")[0]
	query := url.Values{"environment": {environment}, "v": {"2"}, "filter[show.slug]": {parts[0]}}
	var cms discoveryGermanCMS
	err := discoveryPublicJSON(ctx, request.Transport, "https://de-api.loma-cms.com/feloma/videos/"+url.PathEscape(parts[1])+"/?"+query.Encode(), &cms)
	if err != nil {
		if mapped := discoveryError(err); !errors.Is(mapped, ErrUnavailable) {
			return Extraction{}, mapped
		}
	}
	if len(cms.Taxonomies) > discoveryMaxIncluded {
		return Extraction{}, fmt.Errorf("%w: German CMS taxonomy overflow", ErrInvalidMetadata)
	}
	videoID := target.displayID
	if cms.UID != "" {
		if len(cms.UID) < 7 {
			return Extraction{}, fmt.Errorf("%w: malformed German CMS UID", ErrInvalidMetadata)
		}
		candidate := cms.UID[len(cms.UID)-7:]
		if !discoverySegment(candidate) {
			return Extraction{}, fmt.Errorf("%w: malformed German CMS UID", ErrInvalidMetadata)
		}
		videoID = candidate
	}
	result, err := extractor.extractVideo(ctx, request, discoveryTarget{displayID: videoID, canonical: target.canonical})
	if err != nil {
		return Extraction{}, err
	}
	result.Info.Set("display_id", value.String(target.displayID))
	categories := make([]value.Value, 0, len(cms.Taxonomies))
	for _, taxonomy := range cms.Taxonomies {
		if taxonomy.Category == "genre" && strings.TrimSpace(taxonomy.Title) != "" && len(categories) < discoveryMaxIncluded {
			categories = append(categories, value.String(strings.TrimSpace(taxonomy.Title)))
		}
	}
	if len(categories) > 0 {
		result.Info.Set("categories", value.List(categories...))
	}
	return result, nil
}

type discoveryGermanCMS struct {
	UID        string `json:"uid"`
	Taxonomies []struct {
		Category string `json:"category"`
		Title    string `json:"title"`
	} `json:"taxonomies"`
}

// Constructors retain exact upstream extractor keys while sharing one backend.
func NewAmHistoryChannel() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"amhistorychannel", "ahctv.com", "us1-prod-direct.ahctv.com", "go", "us", "ahc", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewAnimalPlanet() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"animalplanet", "animalplanet.com", "us1-prod-direct.animalplanet.com", "go", "us", "apl", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewCookingChannel() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"cookingchannel", "cookingchanneltv.com", "us1-prod-direct.watch.cookingchanneltv.com", "go", "us", "cook", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewDPlay() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"dplay", "dplay.se", "disco-api.dplay.se", "dplayse", "se", "", discoveryDPlayRoute, true, false, false, "", "", 0})
}
func NewDestinationAmerica() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"destinationamerica", "destinationamerica.com", "us1-prod-direct.destinationamerica.com", "go", "us", "dam", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewDiscoveryLife() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoverylife", "discoverylife.com", "us1-prod-direct.discoverylife.com", "go", "us", "dlf", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewDiscoveryNetworksDe() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoverynetworksde", "dmax.de", "eu1-prod.disco-api.com", "dmaxde", "de", "", discoveryGermanRoute, false, true, false, "", "", 0})
}
func NewDiscoveryPlus() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoveryplus", "discoveryplus.com", "us1-prod-direct.discoveryplus.com", "go", "us", "dplus_us", discoveryPlusRoute, false, false, false, "", "", 0})
}
func NewDiscoveryPlusIndia() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoveryplusindia", "discoveryplus.in", "ap2-prod-direct.discoveryplus.in", "dplusindia", "in", "dplus-india", discoveryVideoRoute, false, false, true, "", "", 0})
}
func NewDiscoveryPlusIndiaShow() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoveryplusindiashow", "discoveryplus.in", "ap2-prod-direct.discoveryplus.in", "dplusindia", "in", "dplus-india", discoveryShowRoute, false, false, true, "https://www.discoveryplus.in/", "show", 4})
}
func NewDiscoveryPlusItaly() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoveryplusitaly", "discoveryplus.com", "eu1-prod-direct.discoveryplus.com", "dplay", "it", "dplus_it", discoveryPlusRoute, false, false, false, "", "", 0})
}
func NewDiscoveryPlusItalyShow() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"discoveryplusitalyshow", "discoveryplus.it", "disco-api.discoveryplus.it", "dplayit", "it", "dplay-client", discoveryShowRoute, false, false, false, "https://www.discoveryplus.it/", "programmi", 1})
}
func NewFoodNetwork() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"foodnetwork", "foodnetwork.com", "us1-prod-direct.watch.foodnetwork.com", "go", "us", "food", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewGoDiscovery() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"godiscovery", "discovery.com", "us1-prod-direct.go.discovery.com", "go", "us", "dsc", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewHGTVDe() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"hgtvde", "de.hgtv.com", "eu1-prod.disco-api.com", "hgtv", "de", "hgtv", discoveryDPlayRoute, false, true, false, "", "", 0})
}
func NewHGTVUsa() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"hgtvusa", "hgtv.com", "us1-prod-direct.watch.hgtv.com", "go", "us", "hgtv", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewInvestigationDiscovery() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"investigationdiscovery", "investigationdiscovery.com", "us1-prod-direct.investigationdiscovery.com", "go", "us", "ids", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewScienceChannel() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"sciencechannel", "sciencechannel.com", "us1-prod-direct.sciencechannel.com", "go", "us", "sci", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewTLC() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"tlc", "tlc.com", "us1-prod-direct.tlc.com", "go", "us", "tlc", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewTravelChannel() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"travelchannel", "travelchannel.com", "us1-prod-direct.watch.travelchannel.com", "go", "us", "trav", discoveryVideoRoute, false, false, false, "", "", 0})
}
func NewTele5() DiscoveryDPlay {
	return newDiscoveryDPlay(discoveryConfig{"tele5", "tele5.de", "eu1-prod.disco-api.com", "dmaxde", "de", "", discoveryDPlayRoute, false, true, false, "", "", 0})
}

var _ Extractor = DiscoveryDPlay{}
