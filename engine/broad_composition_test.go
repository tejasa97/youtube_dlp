package engine

import (
	"context"
	"errors"
	"io"
	"net/url"
	"time"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/javascript/supervisor"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

type YouTubePOTRequest = youtubepot.Request
type YouTubePOTResponse = youtubepot.Response
type YouTubePOTProvider = youtubepot.Provider
type YouTubePOTProviderFunc = youtubepot.ProviderFunc
type YouTubePOTFetchPolicy = youtubepot.FetchPolicy

const YouTubePOTFetchAlways = youtubepot.FetchAlways

type YouTubePOTConfig struct {
	Policy      YouTubePOTFetchPolicy
	CacheSize   int
	RefreshSkew time.Duration
	Providers   []YouTubePOTProvider
}

func WithYouTubePOTProviders(config YouTubePOTConfig) Option {
	resolver, err := youtubepot.New(youtubepot.Config{
		Policy: config.Policy, CacheSize: config.CacheSize, RefreshSkew: config.RefreshSkew,
		Providers: append([]youtubepot.Provider(nil), config.Providers...),
	})
	return WithPOTResolver(resolver, err)
}

func broadTestComposition() Composition {
	return NewComposition[extractor.Request](
		func(config ClientProviderConfig) []providerapi.Provider[extractor.Request] {
			return broadCompatibilityProviders(config.InstalledPlugins, config.PluginPermissionApprover)
		},
		broadTestProviderRequest,
		ProviderHooks{
			ChallengeSolverFactory: broadTestChallengeSolver,
			ClassifyError:          broadTestClassifyProviderError,
			ValidatePolicy:         broadTestValidatePolicy,
			ValidateURL:            broadTestValidateURL,
			NetworkError:           broadTestNetworkError,
			StatusError:            broadTestStatusError,
			ValidateResponse:       broadTestValidateResponse,
			ValidateAsset:          broadTestValidateAsset,
			ServiceIdentity:        broadTestServiceIdentity,
			Reload:                 broadTestReload,
		},
	)
}

func newBroadTestClient(options ...Option) *Client {
	return NewClient(broadTestComposition(), options...)
}

func legacyRuntime(providers ...extractor.Extractor) providerapi.Runtime[compositionState] {
	composition := NewComposition[extractor.Request](
		func(ClientProviderConfig) []providerapi.Provider[extractor.Request] { return providers },
		broadTestProviderRequest,
		broadTestComposition().hooks,
	)
	runtime, err := composition.newRuntime(Request{}, ClientProviderConfig{})
	if err != nil {
		panic(err)
	}
	return runtime
}

func wrapLegacyRegistry(registry *extractor.Registry) providerapi.Runtime[compositionState] {
	return legacyRegistryRuntime{registry: registry}
}

type legacyRegistryRuntime struct{ registry *extractor.Registry }

func (runtime legacyRegistryRuntime) ConfigureSelection(rules []string) error {
	return runtime.registry.ConfigureSelection(rules)
}
func (runtime legacyRegistryRuntime) Names() []string { return runtime.registry.Names() }
func (runtime legacyRegistryRuntime) Hooks() providerapi.Hooks[compositionState] {
	composed, _ := broadTestComposition().newRuntime(Request{}, ClientProviderConfig{})
	return composed.Hooks()
}
func (runtime legacyRegistryRuntime) Select(rawURL string) (providerapi.Selected[compositionState], error) {
	selected, err := runtime.registry.Select(rawURL)
	if err != nil {
		return nil, err
	}
	return legacySelectedProvider{provider: selected}, nil
}
func (runtime legacyRegistryRuntime) SelectFor(rawURL, key string) (providerapi.Selected[compositionState], error) {
	selected, err := runtime.registry.SelectFor(rawURL, key)
	if err != nil {
		return nil, err
	}
	return legacySelectedProvider{provider: selected}, nil
}
func (runtime legacyRegistryRuntime) SearchPrefix(prefix string) (providerapi.SearchSelected[compositionState], bool) {
	selected, ok := runtime.registry.SearchPrefix(prefix)
	if !ok {
		return nil, false
	}
	return legacySearchSelected{provider: selected}, true
}

type legacySearchSelected struct {
	provider extractor.SearchPrefixExtractor
}

func (selected legacySearchSelected) Name() string { return selected.provider.Name() }
func (selected legacySearchSelected) Suitable(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && selected.provider.Suitable(parsed)
}
func (selected legacySearchSelected) RetrySafe() bool {
	_, ok := selected.provider.(extractor.RetrySafeExtractor)
	return ok
}
func (selected legacySearchSelected) Extract(ctx context.Context, operation providerapi.Operation, state compositionState) (Extraction, error) {
	return selected.provider.Extract(ctx, broadTestProviderRequest(operation, state.Request))
}
func (selected legacySearchSelected) SearchQueryAllowed(query string) bool {
	return selected.provider.SearchQueryAllowed(query)
}

func productRuntime() providerapi.Runtime[compositionState] {
	return legacyRuntime(broadCompatibilityProviders(nil, nil)...)
}

type legacySelectedProvider struct{ provider extractor.Extractor }

func (selected legacySelectedProvider) Name() string { return selected.provider.Name() }
func (selected legacySelectedProvider) Suitable(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	return err == nil && selected.provider.Suitable(parsed)
}
func (selected legacySelectedProvider) RetrySafe() bool {
	_, ok := selected.provider.(extractor.RetrySafeExtractor)
	return ok
}
func (selected legacySelectedProvider) Extract(ctx context.Context, operation providerapi.Operation, state compositionState) (Extraction, error) {
	return selected.provider.Extract(ctx, broadTestProviderRequest(operation, state.Request))
}

func (operation *operation) extractLegacyWithRetry(ctx context.Context, selected extractor.Extractor, request extractor.Request, eventURL string) (Extraction, error) {
	providerOperation := providerapi.Operation{
		Request: providerapi.Request{
			URL: request.URL, SearchQueryOverride: request.SearchQueryOverride, Referer: request.Referer,
			Transport: request.Transport, Credentials: request.Credentials, VideoPassword: request.VideoPassword,
			NoPlaylist: request.NoPlaylist,
		},
		ChallengeSolver: request.ChallengeSolver,
		POTResolver:     request.YouTubePOT,
	}
	return operation.extractWithRetry(ctx, legacySelectedProvider{provider: selected}, providerOperation, eventURL)
}

func broadTestProviderRequest(operation providerapi.Operation, request Request) extractor.Request {
	legacy := extractor.Request{
		URL: operation.Request.URL, SearchQueryOverride: operation.Request.SearchQueryOverride,
		Referer: operation.Request.Referer, Transport: operation.Request.Transport,
		Credentials: operation.Request.Credentials, VideoPassword: operation.Request.VideoPassword,
		ChallengeSolver: operation.ChallengeSolver, NoPlaylist: operation.Request.NoPlaylist,
		YouTubeTranslatedCaptions: request.YouTubeTranslatedCaptions,
		YouTubeLiveFromStart:      request.LiveFromStart,
		YouTubeComments: extractor.YouTubeCommentOptions{
			Enabled: request.YouTubeComments.Enabled, Sort: request.YouTubeComments.Sort,
			MaxComments: request.YouTubeComments.MaxComments, MaxParents: request.YouTubeComments.MaxParents,
			MaxReplies: request.YouTubeComments.MaxReplies, MaxRepliesPerThread: request.YouTubeComments.MaxRepliesPerThread,
			MaxDepth: request.YouTubeComments.MaxDepth,
		},
		SoundCloudComments: extractor.SoundCloudCommentOptions{
			Enabled: request.SoundCloudComments.Enabled, Sort: request.SoundCloudComments.Sort,
			MaxComments: request.SoundCloudComments.MaxComments,
		},
		NHK: extractor.NHKOptions{RadiruArea: request.NHK.RadiruArea},
	}
	legacy.YouTubePOT, _ = operation.POTResolver.(*youtubepot.Director)
	return legacy
}

func broadTestChallengeSolver(path string) (providerapi.ChallengeSolver, io.Closer, error) {
	hash, err := ejs.BundledScriptHash()
	if err != nil {
		return nil, nil, err
	}
	client, err := supervisor.New(supervisor.Config{Path: path, MemoryBytes: ejs.SolverMemoryBytes, TrustedScriptHash: hash})
	if err != nil {
		return nil, nil, err
	}
	solver, err := ejs.New(client)
	if err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return solver, client, nil
}

type legacyInstalledPluginExtractor struct {
	provider providerapi.Provider[providerapi.Request]
}

func (candidate legacyInstalledPluginExtractor) Name() string { return candidate.provider.Name() }
func (legacyInstalledPluginExtractor) ExplicitOnly()          {}
func (legacyInstalledPluginExtractor) Suitable(*url.URL) bool { return false }
func (candidate legacyInstalledPluginExtractor) Extract(ctx context.Context, request extractor.Request) (Extraction, error) {
	return candidate.provider.Extract(ctx, request.NeutralRequest())
}

func broadTestValidateURL(request providerapi.URLPolicyRequest) error {
	var allowed bool
	switch request.Policy {
	case "ted":
		allowed = extractor.TedAttributableURL(request.URL, request.Role)
	case "dailymotion":
		allowed = extractor.DailymotionAttributableURL(request.URL, request.Role)
	case "niconico":
		if !extractor.NiconicoMediaURLAllowed(request.URL) {
			return &Error{Category: ErrorSecurity, Err: errNiconicoMediaHost}
		}
		return nil
	}
	if !allowed {
		return &Error{Category: ErrorUnsupported, Err: ErrUnavailable}
	}
	return nil
}

func broadTestValidatePolicy(policy string) error {
	switch policy {
	case "ted", "dailymotion", "niconico":
		return nil
	default:
		return &Error{Category: ErrorUnsupported, Err: ErrUnavailable}
	}
}

func broadTestNetworkError(policy string) error {
	if policy == "ted" {
		return extractor.ErrTedNetwork
	}
	return extractor.ErrDailymotionNetwork
}

func broadTestStatusError(request providerapi.StatusErrorRequest) error {
	if request.Policy == "ted" {
		return extractor.TedStatusError(request.Status)
	}
	return extractor.DailymotionStatusError(request.Status)
}

func broadTestValidateResponse(request providerapi.PolicyResponseRequest) error {
	if request.Policy != "niconico" {
		return nil
	}
	if !request.HasBody || request.Status == 0 {
		return errNiconicoMediaResponse
	}
	if request.Status < 200 || request.Status >= 300 {
		category := ErrorNetwork
		if request.Status >= 300 && request.Status < 400 {
			category = ErrorSecurity
		}
		return &Error{Category: category, Err: &niconicoMediaStatusError{status: request.Status}}
	}
	return nil
}

func broadTestValidateAsset(request providerapi.URLPolicyRequest) error {
	validator, err := extractor.AssetURLValidator(request.Policy)
	if err != nil || validator == nil {
		return err
	}
	if err := validator(request.URL); err != nil {
		return &Error{Category: ErrorSecurity, Err: err}
	}
	return nil
}

func broadTestReload(ctx context.Context, operation providerapi.Operation, _ Request, request providerapi.ReloadRequest) (Extraction, error) {
	director, _ := operation.POTResolver.(*youtubepot.Director)
	return extractor.ReloadYouTubePlayer(ctx, operation.Request.Transport, extractor.YouTubeReloadRequest{
		VideoID: request.MediaID, VisitorData: request.VisitorData, WebpageURL: request.WebpageURL,
		ReloadToken: request.Token, ClientName: request.ClientName, ClientID: request.ClientID,
		ClientVersion: request.ClientVersion, UserAgent: request.UserAgent,
		DurationSec: request.DurationSeconds, Tokens: director,
	})
}

func broadTestClassifyProviderError(err error) (providerapi.ErrorClass, bool) {
	switch {
	case errors.Is(err, extractor.ErrAuthentication), errors.Is(err, extractor.ErrWrongPassword),
		errors.Is(err, extractor.ErrTwitchSubscriberOnly), errors.Is(err, extractor.ErrVKAuthentication):
		return providerapi.ErrorAuthentication, true
	case errors.Is(err, extractor.ErrVKUnsafeAsset):
		return providerapi.ErrorSecurity, true
	case errors.Is(err, extractor.ErrUnavailable), errors.Is(err, extractor.ErrRegionRestricted),
		errors.Is(err, extractor.ErrChallengeSolver), errors.Is(err, extractor.ErrTransportProfile),
		errors.Is(err, extractor.ErrTransportIsolation), errors.Is(err, extractor.ErrVKUnavailable),
		errors.Is(err, extractor.ErrVKRegionRestricted), errors.Is(err, extractor.ErrVKNotLive):
		return providerapi.ErrorUnsupported, true
	case errors.Is(err, extractor.ErrVKRateLimited), errors.Is(err, extractor.ErrVKNetwork), errors.Is(err, extractor.ErrVKInvalidStatus), errors.Is(err, extractor.ErrVKRepeatedPage):
		return providerapi.ErrorNetwork, true
	default:
		return "", false
	}
}

func broadTestServiceIdentity(request providerapi.ServiceRequest) (string, bool) {
	if request.Capability == "sponsorblock" && request.Provider == "youtube" {
		return "YouTube", true
	}
	return "", false
}

func productRegistry() *extractor.Registry {
	return extractor.NewRegistry(broadCompatibilityProviders(nil, nil)...)
}
