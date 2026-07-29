package ytdlp

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	// Preparation accepts at most format.MaxNormalizedFormats candidates. Do
	// not impose a smaller availability ceiling which makes --check-all-formats
	// fail for otherwise valid normalized entries.
	availabilityMaxProbes     = mediaformat.MaxNormalizedFormats
	availabilityMaxProbeBytes = 2 << 20
	availabilityMaxTotalBytes = 16 << 20
	availabilityMaxRedirects  = 5
	availabilityProbeTimeout  = 5 * time.Second
)

var errAvailabilityProbe = errors.New("format availability probe failed")

// formatAvailabilityChecker is an operation-scoped, bounded adapter injected
// into format.EvaluationOptions. It deliberately lives at the product/network
// boundary: internal/format only observes the typed availability interface and
// performs no IO.
//
// Redirects are followed manually so a format's Authorization,
// Proxy-Authorization, and explicit Cookie headers are retained only on the
// original origin. This is the same conservative sensitive-header policy used
// by net/http redirects, expressed here because probes must cap redirect hops
// and report redacted errors. The operation cookie jar is still consulted by
// network.Client on each destination as normal scoped-cookie traffic.
type formatAvailabilityChecker struct {
	ctx       context.Context
	transport *network.Client

	mu          sync.Mutex
	baseHeaders http.Header
	mode        FormatCheckMode
	cache       map[string]availabilityResult
	probes      int
	bytes       int64
}

type availabilityResult struct {
	ok  bool
	err error
}

func newFormatAvailabilityChecker(ctx context.Context, transport *network.Client, mode FormatCheckMode) *formatAvailabilityChecker {
	return &formatAvailabilityChecker{
		ctx: ctx, transport: transport, mode: mode, cache: make(map[string]availabilityResult),
	}
}

func (checker *formatAvailabilityChecker) setBaseHeaders(headers http.Header) {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	checker.baseHeaders = headers.Clone()
}

func (checker *formatAvailabilityChecker) IsAvailable(format *value.Object) (bool, error) {
	if err := checker.ctx.Err(); err != nil {
		return false, err
	}
	if format == nil {
		return false, nil
	}
	if checker.mode == FormatCheckAuto && !requiresAutomaticFormatCheck(format) {
		return true, nil
	}
	rawURL, ok := format.Lookup("url").StringValue()
	if !ok || rawURL == "" {
		return false, nil
	}
	headers, err := checker.headersFor(format)
	if err != nil {
		return false, err
	}
	protocol, _ := format.Lookup("protocol").StringValue()
	key := availabilityCacheKey(rawURL, protocol, headers)
	checker.mu.Lock()
	if cached, found := checker.cache[key]; found {
		checker.mu.Unlock()
		return cached.ok, cached.err
	}
	if checker.probes >= availabilityMaxProbes {
		checker.mu.Unlock()
		return false, ErrFormatCheckLimit
	}
	checker.probes++
	checker.mu.Unlock()

	ok, probeErr := checker.probe(rawURL, protocol, headers)
	if probeErr != nil && checker.ctx.Err() != nil {
		probeErr = checker.ctx.Err()
	} else if probeErr != nil && !errors.Is(probeErr, ErrFormatCheckLimit) {
		// URL, credentials, and server-provided detail must never reach product
		// errors. An unavailable candidate is not a fatal selection error.
		probeErr = nil
	}
	checker.mu.Lock()
	checker.cache[key] = availabilityResult{ok: ok, err: probeErr}
	checker.mu.Unlock()
	return ok, probeErr
}

func requiresAutomaticFormatCheck(format *value.Object) bool {
	if format == nil {
		return false
	}
	needsTesting, _ := format.Lookup("__needs_testing").Bool()
	hasDRM, _ := format.Lookup("has_drm").Bool()
	return needsTesting || hasDRM
}

func (checker *formatAvailabilityChecker) headersFor(format *value.Object) (http.Header, error) {
	checker.mu.Lock()
	base := checker.baseHeaders.Clone()
	checker.mu.Unlock()
	if base == nil {
		base = make(http.Header)
	}
	formatHeaders, err := mediaformat.MergeHeaders(format.Lookup("http_headers"))
	if err != nil {
		return nil, err
	}
	for key, values := range formatHeaders {
		base[key] = append([]string(nil), values...)
	}
	return base, nil
}

func availabilityCacheKey(rawURL, protocol string, headers http.Header) string {
	// Keep values out of logs but include them in the opaque key: signed media
	// URLs can legitimately be reused with different bearer tokens.
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, strings.ToLower(key))
	}
	for index := 1; index < len(keys); index++ {
		for prior := index; prior > 0 && keys[prior] < keys[prior-1]; prior-- {
			keys[prior], keys[prior-1] = keys[prior-1], keys[prior]
		}
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, rawURL)
	_, _ = io.WriteString(hash, "\x00"+strings.ToLower(protocol))
	for _, key := range keys {
		_, _ = io.WriteString(hash, "\x00"+key)
		for _, entry := range headers.Values(key) {
			_, _ = io.WriteString(hash, "\x00"+entry)
		}
	}
	return string(hash.Sum(nil))
}

func (checker *formatAvailabilityChecker) probe(rawURL, protocol string, headers http.Header) (bool, error) {
	ctx, cancel := context.WithTimeout(checker.ctx, availabilityProbeTimeout)
	defer cancel()
	protocol = strings.ToLower(protocol)
	switch {
	case strings.Contains(protocol, "m3u8") || protocol == "hls" || protocol == "hls-aes":
		body, status, err := checker.getBounded(ctx, rawURL, headers, availabilityMaxProbeBytes)
		if err != nil || status < 200 || status >= 300 {
			return false, err
		}
		playlist, parseErr := hls.Parse(rawURL, body)
		if parseErr != nil {
			return false, parseErr
		}
		return checker.probeHLSFragment(ctx, playlist, headers)
	case strings.Contains(protocol, "dash"):
		body, status, err := checker.getBounded(ctx, rawURL, headers, availabilityMaxProbeBytes)
		if err != nil || status < 200 || status >= 300 {
			return false, err
		}
		manifest, parseErr := dash.Parse(rawURL, body)
		if parseErr != nil {
			return false, parseErr
		}
		return checker.probeDASHFragment(ctx, manifest, headers)
	case protocol == "ism" || protocol == "mss":
		body, status, err := checker.getBounded(ctx, rawURL, headers, availabilityMaxProbeBytes)
		if err != nil || status < 200 || status >= 300 {
			return false, err
		}
		manifest, parseErr := ism.Parse(rawURL, body)
		if parseErr != nil {
			return false, parseErr
		}
		return checker.probeISMFragment(ctx, rawURL, manifest, headers)
	default:
		// A one-byte range GET proves the media endpoint is readable; HEAD is
		// only a compatibility fallback for servers that reject range GETs.
		_, status, err := checker.getBounded(ctx, rawURL, headers, 1)
		if err == nil && status >= 200 && status < 400 {
			return true, nil
		}
		if err != nil || (status != http.StatusMethodNotAllowed && status != http.StatusNotImplemented) {
			return false, err
		}
		status, err = checker.head(ctx, rawURL, headers)
		return err == nil && status >= 200 && status < 400, err
	}
}

func (checker *formatAvailabilityChecker) probeHLSFragment(ctx context.Context, playlist hls.Playlist, headers http.Header) (bool, error) {
	if len(playlist.Variants) > 0 {
		body, status, err := checker.getBounded(ctx, playlist.Variants[0].URL, headers, availabilityMaxProbeBytes)
		if err != nil || status < 200 || status >= 300 {
			return false, err
		}
		parsed, parseErr := hls.Parse(playlist.Variants[0].URL, body)
		if parseErr != nil {
			return false, parseErr
		}
		playlist = parsed
	}
	if playlist.Media == nil || len(playlist.Media.Segments) == 0 {
		return false, errAvailabilityProbe
	}
	_, status, err := checker.getBounded(ctx, playlist.Media.Segments[0].URL, headers, 1)
	return err == nil && status >= 200 && status < 400, err
}

func (checker *formatAvailabilityChecker) probeDASHFragment(ctx context.Context, manifest dash.MPD, headers http.Header) (bool, error) {
	for _, representation := range manifest.Representations {
		for _, segment := range representation.Segments {
			if segment.URL == "" {
				continue
			}
			_, status, err := checker.getBounded(ctx, segment.URL, headers, 1)
			return err == nil && status >= 200 && status < 400, err
		}
	}
	return false, errAvailabilityProbe
}

func (checker *formatAvailabilityChecker) probeISMFragment(ctx context.Context, manifestURL string, manifest ism.Manifest, headers http.Header) (bool, error) {
	for _, stream := range manifest.Streams {
		segments, err := ism.Address(manifestURL, manifest, stream, 1)
		if err != nil || len(segments) == 0 {
			continue
		}
		_, status, getErr := checker.getBounded(ctx, segments[0].URL, headers, 1)
		return getErr == nil && status >= 200 && status < 400, getErr
	}
	return false, errAvailabilityProbe
}

func (checker *formatAvailabilityChecker) head(ctx context.Context, rawURL string, headers http.Header) (int, error) {
	response, err := checker.doRedirects(ctx, http.MethodHead, rawURL, headers, 0)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func (checker *formatAvailabilityChecker) getBounded(ctx context.Context, rawURL string, headers http.Header, limit int64) ([]byte, int, error) {
	response, err := checker.doRedirects(ctx, http.MethodGet, rawURL, headers, limit)
	if err != nil {
		return nil, 0, err
	}
	defer response.Body.Close()
	// A server may ignore Range and reply 200 with the complete media. Reading
	// through a strict LimitReader still proves the endpoint is usable without
	// consuming more than the availability budget or downloading the file.
	body, err := io.ReadAll(io.LimitReader(response.Body, limit))
	if err != nil {
		return nil, response.StatusCode, errAvailabilityProbe
	}
	if err := checker.recordBytes(int64(len(body))); err != nil {
		return nil, response.StatusCode, err
	}
	return body, response.StatusCode, nil
}

func (checker *formatAvailabilityChecker) recordBytes(amount int64) error {
	checker.mu.Lock()
	defer checker.mu.Unlock()
	if amount < 0 || checker.bytes > availabilityMaxTotalBytes-amount {
		return ErrFormatCheckLimit
	}
	checker.bytes += amount
	return nil
}

func (checker *formatAvailabilityChecker) doRedirects(ctx context.Context, method, rawURL string, headers http.Header, rangeLength int64) (*http.Response, error) {
	current, err := url.Parse(rawURL)
	if err != nil || current.Scheme == "" || current.Host == "" {
		return nil, errAvailabilityProbe
	}
	origin := originOf(current)
	seen := make(map[string]struct{}, availabilityMaxRedirects+1)
	for hop := 0; hop <= availabilityMaxRedirects; hop++ {
		identity := current.String()
		if _, exists := seen[identity]; exists {
			return nil, errAvailabilityProbe
		}
		seen[identity] = struct{}{}
		request, requestErr := http.NewRequestWithContext(ctx, method, current.String(), nil)
		if requestErr != nil {
			return nil, errAvailabilityProbe
		}
		request.Header = headers.Clone()
		if request.Header == nil {
			request.Header = make(http.Header)
		}
		if originOf(current) != origin {
			for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
				request.Header.Del(key)
			}
		}
		if rangeLength > 0 {
			request.Header.Set("Range", fmt.Sprintf("bytes=0-%d", rangeLength-1))
		}
		response, doErr := checker.transport.DoNoRedirect(ctx, request)
		if doErr != nil {
			if errors.Is(doErr, context.Canceled) || errors.Is(doErr, context.DeadlineExceeded) {
				return nil, doErr
			}
			return nil, errAvailabilityProbe
		}
		if response.StatusCode < 300 || response.StatusCode >= 400 {
			return response, nil
		}
		location, parseErr := response.Location()
		response.Body.Close()
		if parseErr != nil {
			return nil, errAvailabilityProbe
		}
		current = current.ResolveReference(location)
		if current.Scheme != "http" && current.Scheme != "https" {
			return nil, errAvailabilityProbe
		}
	}
	return nil, errAvailabilityProbe
}

func originOf(candidate *url.URL) string {
	if candidate == nil {
		return ""
	}
	return strings.ToLower(candidate.Scheme) + "://" + strings.ToLower(candidate.Host)
}
