// Package youtube composes the complete first-party YouTube provider family
// with the public provider-neutral engine.
package youtube

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/tejasa97/youtube_dlp/engine"
	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/javascript/supervisor"
	internalyoutube "github.com/tejasa97/youtube_dlp/internal/providers/youtube"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

// ProviderNames returns the complete YouTube family in routing order. The
// returned slice is a copy and cannot modify a future composition.
func ProviderNames() []string {
	return []string{
		"youtube_music_search",
		"youtube_music_browse",
		"youtube_search",
		"youtube_hashtag",
		"youtube_alias_tab",
		"youtube_handle_tab",
		"youtube_channel_tab",
		"youtube",
	}
}

// NewComposition returns the explicit complete first-party YouTube composition.
// It contains no broad compatibility catalog, plugins, or non-YouTube
// providers. Reusing the returned composition across short-lived Clients also
// reuses its bounded in-memory completed-player preprocessing cache; helpers and
// in-flight calls remain client-local. Engine request fields remain the product
// request surface; use WithPOTProviders only when an application needs explicit
// PO-token sources.
func NewComposition() engine.Composition {
	preprocessedPlayers := ejs.NewPreprocessedPlayerCache()
	return engine.NewComposition[internalyoutube.Request](
		func(engine.ClientProviderConfig) []providerapi.Provider[internalyoutube.Request] {
			return completeProviders()
		},
		adaptRequest,
		engine.ProviderHooks{
			ChallengeSolverFactory: func(path string) (providerapi.ChallengeSolver, io.Closer, error) {
				return newChallengeSolver(path, preprocessedPlayers)
			},
			ClassifyError:   classifyError,
			ServiceIdentity: serviceIdentity,
			Reload:          reload,
		},
	).WithPersistentChallengeSolver(
		func(config engine.ChallengeSolverConfig) (providerapi.ChallengeSolver, io.Closer, error) {
			return newPersistentChallengeSolver(config, preprocessedPlayers)
		},
		func(ctx context.Context, options engine.EJSPreprocessedPlayerCacheOptions) error {
			return ejs.ClearPersistentPlayerCache(ctx, ejs.PersistentPlayerCacheOptions{Directory: options.Directory, TTL: options.TTL, MaxEntries: options.MaxEntries})
		},
	)
}

func completeProviders() []providerapi.Provider[internalyoutube.Request] {
	return []providerapi.Provider[internalyoutube.Request]{
		internalyoutube.NewYouTubeMusicSearch(),
		internalyoutube.NewYouTubeMusicBrowse(),
		internalyoutube.NewYouTubeSearch(),
		internalyoutube.NewYouTubeHashtag(),
		internalyoutube.NewYouTubeAliasTab(),
		internalyoutube.NewYouTubeHandleTab(),
		internalyoutube.NewYouTubeChannelTab(),
		internalyoutube.NewYouTube(),
	}
}

func adaptRequest(operation providerapi.Operation, request engine.Request) internalyoutube.Request {
	director, _ := operation.POTResolver.(*youtubepot.Director)
	return internalyoutube.NewRequest(operation.Request, internalyoutube.Options{
		ChallengeSolver:    operation.ChallengeSolver,
		POT:                director,
		TranslatedCaptions: request.YouTubeTranslatedCaptions,
		LiveFromStart:      request.LiveFromStart,
		Comments: internalyoutube.CommentOptions{
			Enabled: request.YouTubeComments.Enabled, Sort: request.YouTubeComments.Sort,
			MaxComments: request.YouTubeComments.MaxComments, MaxParents: request.YouTubeComments.MaxParents,
			MaxReplies: request.YouTubeComments.MaxReplies, MaxRepliesPerThread: request.YouTubeComments.MaxRepliesPerThread,
			MaxDepth: request.YouTubeComments.MaxDepth,
		},
	})
}

func newChallengeSolver(path string, preprocessedPlayers *ejs.PreprocessedPlayerCache) (providerapi.ChallengeSolver, io.Closer, error) {
	return newPersistentChallengeSolver(engine.ChallengeSolverConfig{Path: path}, preprocessedPlayers)
}

func newPersistentChallengeSolver(config engine.ChallengeSolverConfig, preprocessedPlayers *ejs.PreprocessedPlayerCache) (providerapi.ChallengeSolver, io.Closer, error) {
	hash, err := ejs.BundledScriptHash()
	if err != nil {
		return nil, nil, err
	}
	client, err := supervisor.New(supervisor.Config{Path: config.Path, MemoryBytes: ejs.SolverMemoryBytes, TrustedScriptHash: hash})
	if err != nil {
		return nil, nil, err
	}
	solver, err := ejs.NewWithPersistentPlayerCache(client, preprocessedPlayers, ejs.PersistentPlayerCacheOptions{
		Directory: config.EJSPreprocessedPlayerCache.Directory, TTL: config.EJSPreprocessedPlayerCache.TTL, MaxEntries: config.EJSPreprocessedPlayerCache.MaxEntries,
	})
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return solver, client, nil
}

func classifyError(err error) (providerapi.ErrorClass, bool) {
	switch {
	case errors.Is(err, internalyoutube.ErrAuthentication):
		return providerapi.ErrorAuthentication, true
	case errors.Is(err, internalyoutube.ErrUnsupported), errors.Is(err, internalyoutube.ErrUnavailable),
		errors.Is(err, internalyoutube.ErrChallengeSolver), errors.Is(err, internalyoutube.ErrTransportIsolation):
		return providerapi.ErrorUnsupported, true
	case errors.Is(err, internalyoutube.ErrYouTubeAliasTabNetwork), errors.Is(err, internalyoutube.ErrYouTubeAliasTabRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeChannelNetwork), errors.Is(err, internalyoutube.ErrYouTubeChannelRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeCommentsNetwork), errors.Is(err, internalyoutube.ErrYouTubeCommentsRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeHandleTabNetwork), errors.Is(err, internalyoutube.ErrYouTubeHandleTabRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeMusicBrowseNetwork), errors.Is(err, internalyoutube.ErrYouTubeMusicBrowseRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeMusicSearchNetwork), errors.Is(err, internalyoutube.ErrYouTubeMusicSearchRateLimited),
		errors.Is(err, internalyoutube.ErrYouTubeSearchNetwork), errors.Is(err, internalyoutube.ErrYouTubeSearchRateLimited):
		return providerapi.ErrorNetwork, true
	default:
		return "", false
	}
}

func serviceIdentity(request providerapi.ServiceRequest) (string, bool) {
	if request.Capability == "sponsorblock" {
		return "YouTube", true
	}
	return "", false
}

func reload(ctx context.Context, operation providerapi.Operation, _ engine.Request, request providerapi.ReloadRequest) (engine.Extraction, error) {
	director, _ := operation.POTResolver.(*youtubepot.Director)
	return internalyoutube.ReloadYouTubePlayer(ctx, operation.Request.Transport, internalyoutube.YouTubeReloadRequest{
		VideoID: request.MediaID, VisitorData: request.VisitorData, WebpageURL: request.WebpageURL,
		ReloadToken: request.Token, ClientName: request.ClientName, ClientID: request.ClientID,
		ClientVersion: request.ClientVersion, UserAgent: request.UserAgent,
		DurationSec: request.DurationSeconds, Tokens: director,
	})
}

// POTFetchPolicy controls when an explicit PO-token provider chain is used.
type POTFetchPolicy string

const (
	POTFetchNever  POTFetchPolicy = "never"
	POTFetchAuto   POTFetchPolicy = "auto"
	POTFetchAlways POTFetchPolicy = "always"
)

// POTRequest and POTResponse are bounded, redacted token binding DTOs.
type POTContext = engine.POTContext
type POTRequest = engine.POTRequest
type POTResponse = engine.POTResponse

const (
	POTContextGVS    = engine.POTContextGVS
	POTContextPlayer = engine.POTContextPlayer
	POTContextSubs   = engine.POTContextSubs
)

// POTProvider supplies tokens for a named explicit PO-token source.
type POTProvider interface {
	Name() string
	Provide(context.Context, POTRequest) (POTResponse, error)
}

// POTProviderFunc adapts a function into a POTProvider.
type POTProviderFunc struct {
	ProviderName string
	Function     func(context.Context, POTRequest) (POTResponse, error)
}

func (provider POTProviderFunc) Name() string { return provider.ProviderName }
func (provider POTProviderFunc) Provide(ctx context.Context, request POTRequest) (POTResponse, error) {
	if provider.Function == nil {
		return POTResponse{}, errors.New("YouTube PO-token provider function is nil")
	}
	return provider.Function(ctx, request)
}

// POTConfig configures an explicit, process-local token provider chain.
// Leaving it unused preserves the normal no-token configuration.
type POTConfig struct {
	Policy      POTFetchPolicy
	CacheSize   int
	RefreshSkew time.Duration
	Providers   []POTProvider
}

// WithPOTProviders configures an engine client with the package's typed
// PO-token director. It does not register providers globally or affect other
// compositions.
func WithPOTProviders(config POTConfig) engine.Option {
	providers := make([]youtubepot.Provider, 0, len(config.Providers))
	for _, candidate := range config.Providers {
		if candidate != nil {
			providers = append(providers, potProviderAdapter{provider: candidate})
		}
	}
	director, err := youtubepot.New(youtubepot.Config{
		Providers: providers, Policy: youtubepot.FetchPolicy(config.Policy),
		CacheSize: config.CacheSize, RefreshSkew: config.RefreshSkew,
	})
	return engine.WithPOTResolver(director, err)
}

type potProviderAdapter struct{ provider POTProvider }

func (adapter potProviderAdapter) Name() string { return adapter.provider.Name() }
func (adapter potProviderAdapter) Provide(ctx context.Context, request youtubepot.Request) (youtubepot.Response, error) {
	return adapter.provider.Provide(ctx, request)
}
