package ytdlp

import (
	"context"
	"errors"
	"io"
	"net/url"

	"github.com/tejasa97/youtube_dlp/engine"
	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/javascript/supervisor"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

// broadCompatibilityComposition is the sole owner of the complete first-party
// catalog and its provider-specific support hooks. The engine receives this
// composition explicitly and never discovers providers.
func broadCompatibilityComposition() engine.Composition {
	return engine.NewComposition[extractor.Request](
		func(config engine.ClientProviderConfig) []providerapi.Provider[extractor.Request] {
			return broadCompatibilityProviders(config.InstalledPlugins, config.PluginPermissionApprover)
		},
		broadProviderRequest,
		engine.ProviderHooks{
			ChallengeSolverFactory: broadChallengeSolver,
			ClassifyError:          broadClassifyProviderError,
			ValidatePolicy:         broadValidatePolicy,
			ValidateURL:            broadValidateURL,
			NetworkError:           broadNetworkError,
			StatusError:            broadStatusError,
			ValidateResponse:       broadValidateResponse,
			ValidateAsset:          broadValidateAsset,
			ServiceIdentity:        broadServiceIdentity,
			Reload:                 broadReload,
		},
	)
}

func productRegistry() *extractor.Registry {
	return extractor.NewRegistry(broadCompatibilityProviders(nil, nil)...)
}

func broadProviderRequest(operation providerapi.Operation, request engine.Request) extractor.Request {
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

func broadChallengeSolver(path string) (providerapi.ChallengeSolver, io.Closer, error) {
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
func (candidate legacyInstalledPluginExtractor) Extract(ctx context.Context, request extractor.Request) (engine.Extraction, error) {
	return candidate.provider.Extract(ctx, request.NeutralRequest())
}

func broadValidatePolicy(policy string) error {
	switch policy {
	case "ted", "dailymotion", "niconico":
		return nil
	default:
		return &engine.Error{Category: engine.ErrorUnsupported, Err: engine.ErrUnavailable}
	}
}

func broadValidateURL(request providerapi.URLPolicyRequest) error {
	var allowed bool
	switch request.Policy {
	case "ted":
		allowed = extractor.TedAttributableURL(request.URL, request.Role)
	case "dailymotion":
		allowed = extractor.DailymotionAttributableURL(request.URL, request.Role)
	case "niconico":
		if !extractor.NiconicoMediaURLAllowed(request.URL) {
			return &engine.Error{Category: engine.ErrorSecurity, Err: errNiconicoMediaHost}
		}
		return nil
	}
	if !allowed {
		return &engine.Error{Category: engine.ErrorUnsupported, Err: engine.ErrUnavailable}
	}
	return nil
}

func broadNetworkError(policy string) error {
	if policy == "ted" {
		return extractor.ErrTedNetwork
	}
	return extractor.ErrDailymotionNetwork
}

func broadStatusError(request providerapi.StatusErrorRequest) error {
	if request.Policy == "ted" {
		return extractor.TedStatusError(request.Status)
	}
	return extractor.DailymotionStatusError(request.Status)
}

var (
	errNiconicoMediaHost     = errors.New("niconico media host rejected")
	errNiconicoMediaResponse = errors.New("niconico media response rejected")
	errNiconicoMediaStatus   = errors.New("niconico media status rejected")
	errNiconicoMediaRedirect = errors.New("niconico media redirect rejected")
)

type niconicoMediaStatusError struct{ status int }

func (err *niconicoMediaStatusError) Error() string {
	if err != nil && err.status >= 300 && err.status < 400 {
		return errNiconicoMediaRedirect.Error()
	}
	return errNiconicoMediaResponse.Error()
}
func (err *niconicoMediaStatusError) Unwrap() error {
	if err != nil && err.status >= 300 && err.status < 400 {
		return errNiconicoMediaRedirect
	}
	return errNiconicoMediaStatus
}
func (err *niconicoMediaStatusError) StatusCode() int {
	if err == nil {
		return 0
	}
	return err.status
}

func broadValidateResponse(request providerapi.PolicyResponseRequest) error {
	if request.Policy != "niconico" {
		return nil
	}
	if !request.HasBody || request.Status == 0 {
		return errNiconicoMediaResponse
	}
	if request.Status < 200 || request.Status >= 300 {
		category := engine.ErrorNetwork
		if request.Status >= 300 && request.Status < 400 {
			category = engine.ErrorSecurity
		}
		return &engine.Error{Category: category, Err: &niconicoMediaStatusError{status: request.Status}}
	}
	return nil
}

func broadValidateAsset(request providerapi.URLPolicyRequest) error {
	validator, err := extractor.AssetURLValidator(request.Policy)
	if err != nil || validator == nil {
		return err
	}
	if err := validator(request.URL); err != nil {
		return &engine.Error{Category: engine.ErrorSecurity, Err: err}
	}
	return nil
}

func broadReload(ctx context.Context, operation providerapi.Operation, _ engine.Request, request providerapi.ReloadRequest) (engine.Extraction, error) {
	director, _ := operation.POTResolver.(*youtubepot.Director)
	return extractor.ReloadYouTubePlayer(ctx, operation.Request.Transport, extractor.YouTubeReloadRequest{
		VideoID: request.MediaID, VisitorData: request.VisitorData, WebpageURL: request.WebpageURL,
		ReloadToken: request.Token, ClientName: request.ClientName, ClientID: request.ClientID,
		ClientVersion: request.ClientVersion, UserAgent: request.UserAgent,
		DurationSec: request.DurationSeconds, Tokens: director,
	})
}

func broadClassifyProviderError(err error) (providerapi.ErrorClass, bool) {
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
	case errors.Is(err, extractor.ErrVKRateLimited), errors.Is(err, extractor.ErrVKNetwork),
		errors.Is(err, extractor.ErrVKInvalidStatus), errors.Is(err, extractor.ErrVKRepeatedPage):
		return providerapi.ErrorNetwork, true
	default:
		return "", false
	}
}

func broadServiceIdentity(request providerapi.ServiceRequest) (string, bool) {
	if request.Capability == "sponsorblock" && request.Provider == "youtube" {
		return "YouTube", true
	}
	return "", false
}

func categorized(op string, err error) error { return engine.Categorize(op, err) }
