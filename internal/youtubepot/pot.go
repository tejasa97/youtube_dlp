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
	MaxProviders                 = 16
	MaxCacheItems                = 256
	MaxTokenBytes                = 16 << 10
	MaxTTL                       = 7 * 24 * time.Hour
	DefaultTTL                   = 5 * time.Minute
	MaxRefreshSkew               = 5 * time.Minute
	DefaultRefreshSkew           = 30 * time.Second
	MaxForcedRefreshPerOperation = 2
)

var (
	ErrInvalidRequest      = errors.New("invalid YouTube PO-token request")
	ErrInvalidToken        = errors.New("invalid YouTube PO token")
	ErrRejected            = errors.New("YouTube PO-token provider rejected request")
	ErrUnavailable         = errors.New("YouTube PO token unavailable")
	ErrLimit               = errors.New("YouTube PO-token resource limit exceeded")
	ErrTokenRejected       = errors.New("YouTube PO-token rejected")
	ErrForcedRefreshBudget = errors.New("YouTube PO-token forced refresh budget exhausted")
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
	Providers   []Provider
	Policy      FetchPolicy
	CacheSize   int
	Clock       Clock
	RefreshSkew time.Duration
}

type cacheItem struct {
	response Response
	used     uint64
}

type flight struct {
	done  chan struct{}
	token string
	ok    bool
	err   error
}

// Episode bounds forced cache-bypass refreshes for one download/operation.
// Compatible A/V identities that share a Director cache key also share the
// Director's single-flight; this Episode only gates BypassCache storms.
//
// Extension hook: SignalRejection is for embedding callers that observe an
// attributable typed ErrTokenRejected. The product SABR path does not invent
// PO rejection from SABR_ERROR or other untyped failures.
type Episode struct {
	mu            sync.Mutex
	forcedTotal   int
	maxForced     int
	pendingBypass bool
}

// NewEpisode constructs an operation-scoped forced-refresh budget.
// maxForced <= 0 selects MaxForcedRefreshPerOperation.
func NewEpisode(maxForced int) *Episode {
	if maxForced <= 0 || maxForced > MaxForcedRefreshPerOperation {
		maxForced = MaxForcedRefreshPerOperation
	}
	return &Episode{maxForced: maxForced}
}

// SignalRejection arms at most one forced bypass for this rejection episode.
// Only ErrTokenRejected (via errors.Is) is accepted as an attributable signal.
// This is an extension hook, not a product-integrated SABR recovery signal.
func (episode *Episode) SignalRejection(err error) error {
	if episode == nil {
		return ErrInvalidRequest
	}
	if !errors.Is(err, ErrTokenRejected) {
		return ErrInvalidRequest
	}
	episode.mu.Lock()
	defer episode.mu.Unlock()
	if episode.forcedTotal >= episode.maxForced {
		return ErrForcedRefreshBudget
	}
	if episode.pendingBypass {
		return nil
	}
	episode.pendingBypass = true
	return nil
}

func (episode *Episode) consumeBypass() bool {
	if episode == nil {
		return false
	}
	episode.mu.Lock()
	defer episode.mu.Unlock()
	if !episode.pendingBypass {
		return false
	}
	if episode.forcedTotal >= episode.maxForced {
		episode.pendingBypass = false
		return false
	}
	episode.pendingBypass = false
	episode.forcedTotal++
	return true
}

// Director serializes provider work per cache identity and is safe for
// concurrent operations. Its in-memory cache stores token values only for the
// configured process lifetime; keys are hashes of bounded binding fields.
type Director struct {
	providers []Provider
	policy    FetchPolicy
	maximum   int
	clock     Clock
	skew      time.Duration

	mu       sync.Mutex
	serial   uint64
	entries  map[string]cacheItem
	inflight map[string]*flight
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
	skew := config.RefreshSkew
	if skew == 0 {
		skew = DefaultRefreshSkew
	}
	if skew < 0 || skew > MaxRefreshSkew {
		return nil, ErrLimit
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
	return &Director{
		providers: providers,
		policy:    config.Policy,
		maximum:   config.CacheSize,
		clock:     config.Clock,
		skew:      skew,
		entries:   make(map[string]cacheItem),
		inflight:  make(map[string]*flight),
	}, nil
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
	return director.ResolvePolicy(ctx, request, required, false)
}

// ResolvePolicy additionally permits a caller to recommend an optional token.
// In auto mode recommended requests invoke providers but a miss remains
// non-fatal. This keeps the decision to fetch separate from the decision to
// fail when no token is available.
func (director *Director) ResolvePolicy(ctx context.Context, request Request, required, recommended bool) (string, bool, error) {
	return director.ResolveEpisode(ctx, request, required, recommended, nil)
}

// ResolveEpisode is ResolvePolicy with an optional operation-scoped Episode.
// When the episode has a pending attributable rejection bypass, the cache is
// skipped once and counted against the episode's forced-refresh budget.
func (director *Director) ResolveEpisode(ctx context.Context, request Request, required, recommended bool, episode *Episode) (string, bool, error) {
	if director == nil || ctx == nil || !validRequest(request) {
		return "", false, ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if director.policy == FetchNever {
		if required {
			return "", false, ErrUnavailable
		}
		return "", false, nil
	}
	if director.policy == FetchAuto && !required && !recommended {
		return "", false, nil
	}
	if episode != nil && episode.consumeBypass() {
		request.BypassCache = true
	}
	key := cacheKey(request)
	if !request.BypassCache {
		if response, ok := director.cached(key); ok {
			return response.Token, true, nil
		}
	}
	return director.resolveFlight(ctx, key, request, required)
}

func flightMapKey(cacheKey string, bypass bool) string {
	if bypass {
		return cacheKey + "\x00bypass"
	}
	return cacheKey
}

func (director *Director) resolveFlight(ctx context.Context, key string, request Request, required bool) (string, bool, error) {
	fkey := flightMapKey(key, request.BypassCache)
	director.mu.Lock()
	if !request.BypassCache {
		if item, ok := director.entries[key]; ok && item.response.ExpiresAt.After(director.clock.Now().UTC().Add(director.skew)) {
			director.serial++
			item.used = director.serial
			director.entries[key] = item
			director.mu.Unlock()
			return item.response.Token, true, nil
		}
	}
	if existing := director.inflight[fkey]; existing != nil {
		flight := existing
		director.mu.Unlock()
		return waitFlight(ctx, flight, required)
	}
	flight := &flight{done: make(chan struct{})}
	director.inflight[fkey] = flight
	director.mu.Unlock()

	// Provider work must outlive a canceled leader so compatible waiters are
	// not poisoned. Callers still observe their own context via waitFlight /
	// the post-flight check below.
	token, ok, err := director.fetchProviders(context.WithoutCancel(ctx), request)
	director.mu.Lock()
	flight.token, flight.ok, flight.err = token, ok, err
	delete(director.inflight, fkey)
	close(flight.done)
	director.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", false, err
	}
	if err != nil {
		return "", false, err
	}
	if !ok && required {
		return "", false, ErrUnavailable
	}
	return token, ok, nil
}

func waitFlight(ctx context.Context, flight *flight, required bool) (string, bool, error) {
	select {
	case <-ctx.Done():
		return "", false, ctx.Err()
	case <-flight.done:
		if flight.err != nil {
			return "", false, flight.err
		}
		if !flight.ok && required {
			return "", false, ErrUnavailable
		}
		return flight.token, flight.ok, nil
	}
}

func (director *Director) fetchProviders(ctx context.Context, request Request) (string, bool, error) {
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
		director.store(cacheKey(request), normalized)
		return normalized.Token, true, nil
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

// CacheKey exposes the binding identity hash for tests and compatible A/V
// coordination without revealing request fields.
func CacheKey(request Request) string {
	if !validRequest(request) {
		return ""
	}
	return cacheKey(request)
}

func (director *Director) cached(key string) (Response, bool) {
	director.mu.Lock()
	defer director.mu.Unlock()
	item, ok := director.entries[key]
	if !ok {
		return Response{}, false
	}
	deadline := director.clock.Now().UTC().Add(director.skew)
	if !item.response.ExpiresAt.After(deadline) {
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
