package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// vimeoAuthenticatedViewerEndpoint is the exact origin-locked route used to
// mint an authenticated Vimeo viewer token. The path mirrors the public
// _next/viewer payload so the JWT shape is identical to the anonymous case.
const vimeoAuthenticatedViewerEndpoint = "https://vimeo.com/_next/viewer"

// vimeoAuthenticatedViewerMaxJWTBytes matches the album path's enforced
// upper bound on the JWT string before any payload decoding happens.
const vimeoAuthenticatedViewerMaxJWTBytes = 8 << 10

// vimeoAuthenticatedViewerMaxCookieBytes is the absolute hard cap on the
// authenticated `vimeo` cookie value. The cookie carries the user's session
// identifier; oversized values are rejected before any header construction.
const vimeoAuthenticatedViewerMaxCookieBytes = 8 << 10

// vimeoAuthenticatedViewerMaxJWTPayload bounds the base64url-decoded payload
// the same way the album path does, so a malicious payload cannot exhaust
// memory before we surface ErrInvalidMetadata.
const vimeoAuthenticatedViewerMaxJWTPayload = 4 << 10

// vimeoAuthenticatedViewerRefreshLead mirrors the album provider's policy of
// refreshing a token that has less than two minutes remaining. Honoring the
// same lead keeps the two providers interchangeable for callers.
const vimeoAuthenticatedViewerRefreshLead = 2 * time.Minute

// vimeoAuthenticatedViewerJWTPattern enforces the three-segment base64url
// structure of a Vimeo viewer JWT. Mirrors vimeoAlbumJWTPattern so the
// authenticated path applies the same shape gate.
var vimeoAuthenticatedViewerJWTPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)

// vimeoAuthenticatedTransport is deliberately narrower than Transport. To
// fetch an authenticated viewer token we must read the operation cookie jar
// to find the user's `vimeo` session cookie and we must refuse to follow
// redirects, so the credential cannot be redirected to another origin.
type vimeoAuthenticatedTransport interface {
	Transport
	Cookies(string) ([]*http.Cookie, error)
	DoNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// vimeoAuthenticatedViewerTokenProvider caches the JWT minted by
// https://vimeo.com/_next/viewer for an authenticated session. It is a sibling
// of vimeoViewerTokenProvider — the anonymous provider must not be reused,
// aliased, or otherwise mutated. The cache is process-local and safe for
// concurrent use.
type vimeoAuthenticatedViewerTokenProvider struct {
	transport vimeoAuthenticatedTransport
	now       func() time.Time
	mu        sync.Mutex
	token     string
	expires   int64
}

// vimeoAuthenticatedViewerTokenProviderFromTransport constructs a provider
// over a transport that exposes the cookie jar and the no-redirect execution
// path. Anything else fails closed with ErrAuthentication; we never silently
// fall back to the anonymous jar or a redirect-following GET.
func vimeoAuthenticatedViewerTokenProviderFromTransport(transport Transport, now func() time.Time) (*vimeoAuthenticatedViewerTokenProvider, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: missing Vimeo authenticated transport", ErrAuthentication)
	}
	authTransport, ok := transport.(vimeoAuthenticatedTransport)
	if !ok {
		return nil, ErrAuthentication
	}
	if now == nil {
		now = time.Now
	}
	return &vimeoAuthenticatedViewerTokenProvider{transport: authTransport, now: now}, nil
}

// get returns a cached JWT when it still has the refresh lead remaining, or
// otherwise fetches a fresh one. A token that arrives too close to expiry is
// rejected instead of cached so the next call never silently returns a
// near-dead credential.
func (provider *vimeoAuthenticatedViewerTokenProvider) get(ctx context.Context) (string, error) {
	if provider == nil {
		return "", fmt.Errorf("%w: missing Vimeo authenticated viewer token provider", ErrInvalidMetadata)
	}
	if err := contextError(ctx); err != nil {
		return "", err
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return "", err
	}
	now := time.Now
	if provider.now != nil {
		now = provider.now
	}
	current := now().Unix()
	if provider.token != "" && provider.expires-current >= int64(vimeoAuthenticatedViewerRefreshLead/time.Second) {
		return provider.token, nil
	}
	token, expires, err := provider.fetch(ctx)
	if err != nil {
		return "", err
	}
	if expires-now().Unix() < int64(vimeoAuthenticatedViewerRefreshLead/time.Second) {
		return "", ErrAuthentication
	}
	provider.token, provider.expires = token, expires
	return token, nil
}

// invalidate clears the cached token when it matches the in-flight value. A
// mismatched token is intentionally left alone so concurrent callers do not
// race against a refresh that already installed a fresh value.
func (provider *vimeoAuthenticatedViewerTokenProvider) invalidate(token string) {
	if provider == nil {
		return
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if provider.token == token {
		provider.token, provider.expires = "", 0
	}
}

// withVimeoAuthenticatedViewerToken runs callback exactly once with a fresh
// JWT, and at most one extra time after a 401/403. A terminal 401/403 after
// that refresh is normalized to ErrAuthentication so callers never expose a
// transport-level status for an exhausted authenticated session. A nil
// provider or callback is rejected up front so callers cannot accidentally
// short-circuit a refresh.
func withVimeoAuthenticatedViewerToken(ctx context.Context, provider *vimeoAuthenticatedViewerTokenProvider, callback func(string) error) error {
	if provider == nil || callback == nil {
		return fmt.Errorf("%w: missing Vimeo authenticated viewer token provider", ErrInvalidMetadata)
	}
	if err := contextError(ctx); err != nil {
		return err
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := contextError(ctx); err != nil {
			return err
		}
		token, err := provider.get(ctx)
		if err != nil {
			return err
		}
		err = callback(token)
		var status *HTTPStatusError
		if errors.As(err, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden) {
			if attempt == 0 {
				provider.invalidate(token)
				continue
			}
			return ErrAuthentication
		}
		return err
	}
	return ErrAuthentication
}

// fetch performs one authenticated viewer request and validates the returned
// JWT. Cookie reads, network access, and JWT parsing each honor cancellation
// and surface fixed diagnostics that never contain cookie, token, or body
// bytes.
func (provider *vimeoAuthenticatedViewerTokenProvider) fetch(ctx context.Context) (string, int64, error) {
	if provider == nil || provider.transport == nil {
		return "", 0, ErrAuthentication
	}
	if err := contextError(ctx); err != nil {
		return "", 0, err
	}
	cookies, err := provider.transport.Cookies("https://vimeo.com")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", 0, err
		}
		return "", 0, ErrAuthentication
	}
	if err := contextError(ctx); err != nil {
		return "", 0, err
	}
	if !hasVimeoAuthenticatedCookie(cookies) {
		return "", 0, ErrAuthentication
	}
	headers := http.Header{}
	headers.Set("Accept", "application/json")
	var viewer struct {
		JWT string `json:"jwt"`
	}
	err = requestJSON(ctx, provider.transport.DoNoRedirect, http.MethodGet, vimeoAuthenticatedViewerEndpoint, nil, headers, &viewer)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", 0, err
		}
		var status *HTTPStatusError
		if errors.As(err, &status) {
			switch status.Code {
			case http.StatusUnauthorized, http.StatusForbidden:
				return "", 0, ErrAuthentication
			case http.StatusNotFound, http.StatusGone:
				return "", 0, ErrUnavailable
			}
			if status.Code >= http.StatusMultipleChoices && status.Code < http.StatusBadRequest {
				return "", 0, ErrAuthentication
			}
			return "", 0, err
		}
		if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) {
			return "", 0, fmt.Errorf("%w: malformed Vimeo authenticated viewer response", ErrInvalidMetadata)
		}
		return "", 0, err
	}
	token, expires, err := parseVimeoAuthenticatedViewerJWT(viewer.JWT)
	if err != nil {
		return "", 0, err
	}
	return token, expires, nil
}

// hasVimeoAuthenticatedCookie locates the non-empty `vimeo` cookie in the jar
// entry for https://vimeo.com. The cookie value is bounded and scanned for
// CR/LF/NUL bytes so it cannot be smuggled into headers or error messages.
func hasVimeoAuthenticatedCookie(cookies []*http.Cookie) bool {
	for _, cookie := range cookies {
		if cookie == nil {
			continue
		}
		if cookie.Name != "vimeo" {
			continue
		}
		value := cookie.Value
		if value == "" {
			return false
		}
		if len(value) > vimeoAuthenticatedViewerMaxCookieBytes {
			return false
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return false
		}
		return true
	}
	return false
}

// parseVimeoAuthenticatedViewerJWT validates the JWT shape and payload
// against the same local bounds the album path enforces. All failures use
// one fixed diagnostic so payload and token bytes cannot enter the error
// chain.
func parseVimeoAuthenticatedViewerJWT(rawJWT string) (string, int64, error) {
	invalid := func() (string, int64, error) {
		return "", 0, fmt.Errorf("%w: malformed Vimeo authenticated viewer token", ErrInvalidMetadata)
	}
	if rawJWT == "" || len(rawJWT) > vimeoAuthenticatedViewerMaxJWTBytes || !vimeoAuthenticatedViewerJWTPattern.MatchString(rawJWT) {
		return invalid()
	}
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return invalid()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || len(payload) > vimeoAuthenticatedViewerMaxJWTPayload {
		return invalid()
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	var claims struct {
		Expires json.RawMessage `json:"exp"`
	}
	if err := decoder.Decode(&claims); err != nil || ensureJSONEOF(decoder) != nil {
		return invalid()
	}
	rawExpires := strings.TrimSpace(string(claims.Expires))
	if rawExpires == "" || rawExpires[0] == '"' {
		return invalid()
	}
	expires, err := strconv.ParseInt(rawExpires, 10, 64)
	if err != nil || expires <= 0 {
		return invalid()
	}
	return rawJWT, expires, nil
}
