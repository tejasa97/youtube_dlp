// Package youtubepot provides a bounded, secret-safe PO-token provider and
// cache boundary for YouTube protected playback. It generates no token itself;
// callers explicitly supply native Go or out-of-process providers.
package youtubepot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MaxProviders  = 16
	MaxCacheItems = 256
	MaxTokenBytes = 16 << 10
	MaxTTL        = 7 * 24 * time.Hour
	DefaultTTL    = 5 * time.Minute
)

var (
	ErrInvalidRequest = errors.New("invalid YouTube PO-token request")
	ErrInvalidToken   = errors.New("invalid YouTube PO token")
	ErrRejected       = errors.New("YouTube PO-token provider rejected request")
	ErrUnavailable    = errors.New("YouTube PO token unavailable")
	ErrLimit          = errors.New("YouTube PO-token resource limit exceeded")
)

type Context string

const (
	ContextGVS    Context = "gvs"
	ContextPlayer Context = "player"
	ContextSubs   Context = "subs"
)

type FetchPolicy string

const (
	FetchNever  FetchPolicy = "never"
	FetchAuto   FetchPolicy = "auto"
	FetchAlways FetchPolicy = "always"
)

type Request struct {
	Context       Context
	Client        string
	VisitorData   string
	DataSyncID    string
	VideoID       string
	PlayerURL     string
	Authenticated bool
	BypassCache   bool
}

func (Request) String() string   { return "[redacted YouTube PO-token request]" }
func (Request) GoString() string { return "youtubepot.Request{[redacted]}" }

type Response struct {
	Token     string
	ExpiresAt time.Time
}

func (Response) String() string   { return "[redacted YouTube PO-token response]" }
func (Response) GoString() string { return "youtubepot.Response{[redacted]}" }

type Provider interface {
	Name() string
	Provide(context.Context, Request) (Response, error)
}

type ProviderFunc struct {
	ProviderName string
	Function     func(context.Context, Request) (Response, error)
}

func (provider ProviderFunc) Name() string { return provider.ProviderName }
func (provider ProviderFunc) Provide(ctx context.Context, request Request) (Response, error) {
	if provider.Function == nil {
		return Response{}, ErrRejected
	}
	return provider.Function(ctx, request)
}

type Clock interface{ Now() time.Time }

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

type Config struct {
	Providers []Provider
	Policy    FetchPolicy
	CacheSize int
	Clock     Clock
}

type cacheItem struct {
	response Response
	used     uint64
}

// Director serializes no provider work and is safe for concurrent operations.
// Its in-memory cache stores token values only for the configured process
// lifetime; keys are hashes of bounded binding fields.
type Director struct {
	providers []Provider
	policy    FetchPolicy
	maximum   int
	clock     Clock

	mu      sync.Mutex
	serial  uint64
	entries map[string]cacheItem
}

var providerNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9._-]{0,62}[a-z0-9])?$`)
var clientPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
var videoIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func New(config Config) (*Director, error) {
	if len(config.Providers) > MaxProviders {
		return nil, ErrLimit
	}
	if config.Policy == "" {
		config.Policy = FetchAuto
	}
	if config.Policy != FetchNever && config.Policy != FetchAuto && config.Policy != FetchAlways {
		return nil, ErrInvalidRequest
	}
	if config.CacheSize == 0 {
		config.CacheSize = 64
	}
	if config.CacheSize < 1 || config.CacheSize > MaxCacheItems {
		return nil, ErrLimit
	}
	if config.Clock == nil {
		config.Clock = realClock{}
	}
	providers := append([]Provider(nil), config.Providers...)
	seen := make(map[string]bool, len(providers))
	for _, provider := range providers {
		name, ok := safeProviderName(provider)
		if !ok || !providerNamePattern.MatchString(name) || seen[name] {
			return nil, ErrInvalidRequest
		}
		seen[name] = true
	}
	return &Director{providers: providers, policy: config.Policy, maximum: config.CacheSize, clock: config.Clock, entries: make(map[string]cacheItem)}, nil
}

func safeProviderName(provider Provider) (name string, ok bool) {
	defer func() {
		if recover() != nil {
			name, ok = "", false
		}
	}()
	if provider == nil {
		return "", false
	}
	return provider.Name(), true
}

// Resolve returns (token, true, nil) on success. Optional misses return
// ("", false, nil); required misses return ErrUnavailable. Provider error text
// and token material are never propagated into diagnostics.
func (director *Director) Resolve(ctx context.Context, request Request, required bool) (string, bool, error) {
	if director == nil || ctx == nil || !validRequest(request) {
		return "", false, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if director.policy == FetchNever || director.policy == FetchAuto && !required {
		return "", false, nil
	}
	key := cacheKey(request)
	if !request.BypassCache {
		if response, ok := director.cached(key); ok {
			return response.Token, true, nil
		}
	}
	for _, provider := range director.providers {
		response, err := callProvider(ctx, provider, request)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", false, err
			}
			continue
		}
		normalized, err := normalizeResponse(response, director.clock.Now())
		if err != nil {
			continue
		}
		director.store(key, normalized)
		return normalized.Token, true, nil
	}
	if required {
		return "", false, ErrUnavailable
	}
	return "", false, nil
}

func validRequest(request Request) bool {
	if request.Context != ContextGVS && request.Context != ContextPlayer && request.Context != ContextSubs ||
		!clientPattern.MatchString(request.Client) || len(request.VisitorData) > 4096 || len(request.DataSyncID) > 4096 || len(request.PlayerURL) > 8192 {
		return false
	}
	if request.VideoID != "" && !videoIDPattern.MatchString(request.VideoID) {
		return false
	}
	if request.Context == ContextPlayer && request.VideoID == "" {
		return false
	}
	if request.Context == ContextGVS && !request.Authenticated && request.VisitorData == "" && request.VideoID == "" {
		return false
	}
	if request.Context == ContextGVS && request.Authenticated && request.DataSyncID == "" {
		return false
	}
	return true
}

func callProvider(ctx context.Context, provider Provider, request Request) (response Response, err error) {
	defer func() {
		if recover() != nil {
			response, err = Response{}, ErrUnavailable
		}
	}()
	return provider.Provide(ctx, request)
}

func normalizeResponse(response Response, now time.Time) (Response, error) {
	token, err := NormalizeToken(response.Token)
	if err != nil {
		return Response{}, err
	}
	now = now.UTC()
	if response.ExpiresAt.IsZero() {
		response.ExpiresAt = now.Add(DefaultTTL)
	} else {
		response.ExpiresAt = response.ExpiresAt.UTC()
	}
	if !response.ExpiresAt.After(now) || response.ExpiresAt.After(now.Add(MaxTTL)) {
		return Response{}, ErrInvalidToken
	}
	response.Token = token
	return response, nil
}

func NormalizeToken(input string) (string, error) {
	if input == "" || len(input) > MaxTokenBytes || strings.TrimSpace(input) != input || strings.ContainsAny(input, "?&#%\r\n\t ") {
		return "", ErrInvalidToken
	}
	padding := strings.Repeat("=", (4-len(input)%4)%4)
	decoded, err := base64.URLEncoding.Strict().DecodeString(input + padding)
	if err != nil || len(decoded) == 0 || len(decoded) > MaxTokenBytes {
		return "", ErrInvalidToken
	}
	return base64.RawURLEncoding.EncodeToString(decoded), nil
}

func cacheKey(request Request) string {
	bindings := []string{
		"v1", string(request.Context), request.Client, request.VisitorData,
		request.DataSyncID, request.VideoID, request.PlayerURL, fmt.Sprint(request.Authenticated),
	}
	digest := sha256.Sum256([]byte(strings.Join(bindings, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (director *Director) cached(key string) (Response, bool) {
	director.mu.Lock()
	defer director.mu.Unlock()
	item, ok := director.entries[key]
	if !ok {
		return Response{}, false
	}
	if !item.response.ExpiresAt.After(director.clock.Now().UTC()) {
		delete(director.entries, key)
		return Response{}, false
	}
	director.serial++
	item.used = director.serial
	director.entries[key] = item
	return item.response, true
}

func (director *Director) store(key string, response Response) {
	director.mu.Lock()
	defer director.mu.Unlock()
	director.serial++
	director.entries[key] = cacheItem{response: response, used: director.serial}
	if len(director.entries) <= director.maximum {
		return
	}
	keys := make([]string, 0, len(director.entries))
	for candidate := range director.entries {
		keys = append(keys, candidate)
	}
	sort.Strings(keys)
	evict := keys[0]
	for _, candidate := range keys[1:] {
		if director.entries[candidate].used < director.entries[evict].used {
			evict = candidate
		}
	}
	delete(director.entries, evict)
}
