package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

const (
	youtubePlayerAPIURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"
	// MaxYouTubeClientAttempts caps anonymous or authenticated recovery rotations.
	MaxYouTubeClientAttempts = 8
)

type youtubeClientProfile struct {
	Name            string
	ClientName      string
	ClientID        string
	ClientVersion   string
	UserAgent       string
	Origin          string // empty => https://www.youtube.com
	RequireAuth     bool
	RequirePremium  bool
	SupportsCookies bool
	Context         map[string]any
	GVSPolicy       youtubePOTPolicy
	PlayerPolicy    youtubePOTPolicy
	SubsPolicy      youtubePOTPolicy
}

type youtubePOTPolicy struct {
	Required                   bool
	Recommended                bool
	NotRequiredWithPlayerToken bool
	NotRequiredForPremium      bool
}

func (policy youtubePOTPolicy) required(playerTokenProvided, premium bool) bool {
	if !policy.Required {
		return false
	}
	if policy.NotRequiredWithPlayerToken && playerTokenProvided {
		return false
	}
	if policy.NotRequiredForPremium && premium {
		return false
	}
	return true
}

func (profile youtubeClientProfile) origin() string {
	if profile.Origin != "" {
		return profile.Origin
	}
	return youtubeAuthOrigin
}

func (profile youtubeClientProfile) valid() bool {
	if profile.Name == "" || profile.ClientName == "" || profile.ClientID == "" ||
		profile.ClientVersion == "" || profile.UserAgent == "" {
		return false
	}
	if profile.ClientName == "WEB_REMIX" {
		// Music identity must never enter video format recovery.
		return false
	}
	if !youtubeSafeHeaderValue(profile.ClientName) || !youtubeSafeHeaderValue(profile.ClientID) ||
		!youtubeSafeHeaderValue(profile.ClientVersion) || !youtubeSafeHeaderValue(profile.UserAgent) {
		return false
	}
	if profile.RequirePremium && !profile.RequireAuth {
		return false
	}
	clientID, err := strconv.ParseInt(profile.ClientID, 10, 32)
	return err == nil && clientID > 0 && clientID <= math.MaxInt32
}

// Anonymous format-recovery clients from the pinned yt-dlp INNERTUBE_CLIENTS
// table (aefce1ee). Order is deterministic and bounded; values are exact.
var youtubeAnonymousFormatRecoveryClients = []youtubeClientProfile{
	{
		Name: "android", ClientName: "ANDROID", ClientID: "3",
		ClientVersion: "21.26.364",
		UserAgent:     "com.google.android.youtube/21.26.364 (Linux; U; Android 11) gzip",
		Context: map[string]any{
			"androidSdkVersion": 30, "osName": "Android", "osVersion": "11",
		},
		GVSPolicy: youtubePOTPolicy{
			Required: true, Recommended: true, NotRequiredWithPlayerToken: true,
		},
		PlayerPolicy: youtubePOTPolicy{Recommended: true},
	},
	{
		Name: "android_vr", ClientName: "ANDROID_VR", ClientID: "28",
		ClientVersion: "1.65.10",
		UserAgent:     "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		Context: map[string]any{
			"androidSdkVersion": 32, "osName": "Android", "osVersion": "12L",
			"deviceMake": "Oculus", "deviceModel": "Quest 3",
		},
	},
	{
		Name: "web_safari", ClientName: "WEB", ClientID: "1",
		ClientVersion: "2.20260708.00.00",
		UserAgent:     "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.5 Safari/605.1.15,gzip(gfe)",
		Context: map[string]any{
			"userAgent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/15.5 Safari/605.1.15,gzip(gfe)",
		},
		SupportsCookies: true,
		GVSPolicy: youtubePOTPolicy{
			Required: true, Recommended: true, NotRequiredForPremium: true,
		},
	},
	{
		Name: "ios", ClientName: "IOS", ClientID: "5",
		ClientVersion: "21.26.4",
		UserAgent:     "com.google.ios.youtube/21.26.4 (iPhone16,2; U; CPU iOS 18_3_2 like Mac OS X;)",
		Context: map[string]any{
			"deviceMake": "Apple", "deviceModel": "iPhone16,2",
			"osName": "iPhone", "osVersion": "18.3.2.22D82",
		},
		GVSPolicy: youtubePOTPolicy{
			Required: true, Recommended: true, NotRequiredWithPlayerToken: true,
		},
		PlayerPolicy: youtubePOTPolicy{Recommended: true},
	},
	{
		Name: "mweb", ClientName: "MWEB", ClientID: "2",
		ClientVersion: "2.20260708.05.00",
		UserAgent:     "Mozilla/5.0 (iPad; CPU OS 16_7_10 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1,gzip(gfe)",
		Context: map[string]any{
			"userAgent": "Mozilla/5.0 (iPad; CPU OS 16_7_10 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.6 Mobile/15E148 Safari/604.1,gzip(gfe)",
		},
		SupportsCookies: true,
		GVSPolicy: youtubePOTPolicy{
			Required: true, Recommended: true, NotRequiredForPremium: true,
		},
	},
}

// Authenticated format-recovery clients used after the webpage WEB retry.
// Never mixed with anonymous recovery and never include WEB_REMIX.
var youtubeAuthenticatedFormatRecoveryClients = []youtubeClientProfile{
	{
		Name: "tv_downgraded", ClientName: "TVHTML5", ClientID: "7",
		ClientVersion:   "5.20260707",
		UserAgent:       "Mozilla/5.0 (ChromiumStylePlatform) Cobalt/Version",
		RequireAuth:     true,
		SupportsCookies: true,
	},
	{
		Name: "web_creator", ClientName: "WEB_CREATOR", ClientID: "62",
		ClientVersion:   "1.20260708.06.00",
		UserAgent:       "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/123.0.0.0 Safari/537.36",
		RequireAuth:     true,
		RequirePremium:  true,
		SupportsCookies: true,
		GVSPolicy: youtubePOTPolicy{
			Required: true, Recommended: true, NotRequiredForPremium: true,
		},
	},
}

// Compatibility alias for older call sites/tests that referenced the anonymous list.
var youtubeFormatRecoveryClients = youtubeAnonymousFormatRecoveryClients

func requestYouTubePlayer(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, profile youtubeClientProfile, tokens *youtubepot.Director) (youtubePlayerResponse, error) {
	return requestYouTubePlayerReload(ctx, transport, videoID, visitorData, playerURL, profile, tokens, "")
}

// requestYouTubePlayerReload issues /player with an optional attributable
// reloadPlaybackContext. Placement is playbackContext.reloadPlaybackContext
// .reloadPlaybackParams.token per LuanRT/googlevideo examples/sabr-shaka-example
// at commit d2fa40d761034a286cf60ee033653307a1295b0c.
func requestYouTubePlayerReload(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, profile youtubeClientProfile, tokens *youtubepot.Director, reloadToken string) (youtubePlayerResponse, error) {
	if !profile.valid() {
		return youtubePlayerResponse{}, fmt.Errorf("%w: invalid recovery client profile", ErrInvalidMetadata)
	}
	if profile.RequireAuth {
		return youtubePlayerResponse{}, fmt.Errorf("%w: authenticated client requires SID boundary", ErrAuthentication)
	}
	if reloadToken != "" {
		if err := validateYouTubeReloadToken(reloadToken); err != nil {
			return youtubePlayerResponse{}, err
		}
	}
	clientContext := make(map[string]any, len(profile.Context)+3)
	for key, item := range profile.Context {
		clientContext[key] = item
	}
	clientContext["clientName"] = profile.ClientName
	clientContext["clientVersion"] = profile.ClientVersion
	if visitorData != "" {
		clientContext["visitorData"] = visitorData
	}
	playbackContext := map[string]any{
		"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"},
	}
	if reloadToken != "" {
		playbackContext["reloadPlaybackContext"] = map[string]any{
			"reloadPlaybackParams": map[string]any{"token": reloadToken},
		}
	}
	payload := map[string]any{
		"context":         map[string]any{"client": clientContext},
		"videoId":         videoID,
		"playbackContext": playbackContext,
		"contentCheckOk":  true,
		"racyCheckOk":     true,
	}
	playerTokenProvided := false
	if tokens != nil {
		token, ok, tokenErr := tokens.ResolvePolicy(ctx, youtubepot.Request{
			Context: youtubepot.ContextPlayer, Client: profile.ClientName, VisitorData: visitorData,
			VideoID: videoID, PlayerURL: playerURL,
		}, profile.PlayerPolicy.Required, profile.PlayerPolicy.Recommended)
		if tokenErr != nil {
			if errors.Is(tokenErr, context.Canceled) || errors.Is(tokenErr, context.DeadlineExceeded) {
				return youtubePlayerResponse{}, tokenErr
			}
			return youtubePlayerResponse{}, fmt.Errorf("%w: player token", ErrUnavailable)
		}
		if ok {
			payload["serviceIntegrityDimensions"] = map[string]any{"poToken": token}
			playerTokenProvided = true
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return youtubePlayerResponse{}, fmt.Errorf("encode YouTube client request: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", profile.UserAgent)
	headers.Set("X-Youtube-Client-Name", profile.ClientID)
	headers.Set("X-Youtube-Client-Version", profile.ClientVersion)
	if visitorData != "" {
		headers.Set("X-Goog-Visitor-Id", visitorData)
	}
	var player youtubePlayerResponse
	if err := RequestJSONWithoutCookies(ctx, transport, http.MethodPost, youtubePlayerAPIURL, body, headers, &player); err != nil {
		return youtubePlayerResponse{}, redactYouTubeClientError(profile.Name, err)
	}
	if player.VideoDetails.VideoID != "" && player.VideoDetails.VideoID != videoID {
		return youtubePlayerResponse{}, fmt.Errorf("%w: %s response video id mismatch", ErrInvalidMetadata, profile.Name)
	}
	return bindYouTubePlayerIdentity(player, profile, visitorData, playerURL, playerTokenProvided)
}

func bindYouTubePlayerIdentity(player youtubePlayerResponse, profile youtubeClientProfile, visitorData, playerURL string, playerTokenProvided bool) (youtubePlayerResponse, error) {
	player.clientName = profile.ClientName
	clientID, err := strconv.ParseInt(profile.ClientID, 10, 32)
	if err != nil || clientID <= 0 || clientID > math.MaxInt32 {
		return youtubePlayerResponse{}, fmt.Errorf("%w: invalid recovery client id", ErrInvalidMetadata)
	}
	player.clientID = int32(clientID)
	player.clientVersion = profile.ClientVersion
	player.userAgent = profile.UserAgent
	player.visitorData = visitorData
	player.playerURL = playerURL
	player.playerTokenProvided = playerTokenProvided
	player.subsPolicy = profile.SubsPolicy
	return player, nil
}

func recoverYouTubeFormats(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, tokens *youtubepot.Director) ([]youtubePlayerResponse, error) {
	return recoverYouTubeFormatsWithProfiles(ctx, transport, videoID, visitorData, playerURL, tokens, youtubeAnonymousFormatRecoveryClients, false)
}

func recoverYouTubeFormatsWithProfiles(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, tokens *youtubepot.Director, profiles []youtubeClientProfile, premium bool) ([]youtubePlayerResponse, error) {
	var firstRequestError error
	var recovered []youtubePlayerResponse
	attempts := 0
	for _, profile := range profiles {
		if attempts >= MaxYouTubeClientAttempts {
			break
		}
		if !profile.valid() || profile.RequireAuth {
			continue
		}
		if profile.RequirePremium && !premium {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempts++
		player, err := requestYouTubePlayer(ctx, transport, videoID, visitorData, playerURL, profile, tokens)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if errors.Is(err, ErrTransportIsolation) {
				return nil, err
			}
			if firstRequestError == nil {
				firstRequestError = err
			}
			continue
		}
		if player.PlayabilityStatus.Status == "OK" && hasYouTubeFormatCandidates(player) {
			if tokens != nil {
				required := profile.GVSPolicy.required(player.playerTokenProvided, premium) && youtubePlayerHasGVSRequiredFormats(player)
				token, ok, tokenErr := tokens.ResolvePolicy(ctx, youtubepot.Request{
					Context: youtubepot.ContextGVS, Client: profile.ClientName, VisitorData: visitorData,
					VideoID: videoID, PlayerURL: playerURL,
				}, required, profile.GVSPolicy.Recommended)
				if tokenErr != nil {
					if errors.Is(tokenErr, context.Canceled) || errors.Is(tokenErr, context.DeadlineExceeded) {
						return nil, tokenErr
					}
					if required {
						dropYouTubeGVSRequiredFormats(&player)
						if hasYouTubeFormatCandidates(player) {
							recovered = append(recovered, player)
							continue
						}
					}
					if firstRequestError == nil {
						firstRequestError = fmt.Errorf("%w: GVS token", ErrUnavailable)
					}
					continue
				}
				if required && !ok {
					dropYouTubeGVSRequiredFormats(&player)
					if hasYouTubeFormatCandidates(player) {
						recovered = append(recovered, player)
						continue
					}
					if firstRequestError == nil {
						firstRequestError = fmt.Errorf("%w: GVS token", ErrUnavailable)
					}
					continue
				}
				if ok {
					applyYouTubeGVSToken(&player, token)
				}
			}
			recovered = append(recovered, player)
		}
	}
	if len(recovered) > 0 {
		return recovered, nil
	}
	if firstRequestError != nil {
		return nil, firstRequestError
	}
	return nil, fmt.Errorf("%w: YouTube returned no URL-bearing formats from fallback clients", ErrUnavailable)
}

// recoverAuthenticatedYouTubeFormats tries the webpage WEB player, then bounded
// authenticated profiles. It never falls back to anonymous clients. The first
// successful format-bearing candidate wins (deterministic selection).
func recoverAuthenticatedYouTubeFormats(ctx context.Context, transport Transport, videoID string, pageConfig youtubePageConfig, responseVisitor, responseDataSync string, premium bool, now func() time.Time) ([]youtubePlayerResponse, error) {
	if now == nil {
		return nil, ErrAuthentication
	}
	var firstErr error
	webAuth := pageConfig.webAuthConfig(responseVisitor, responseDataSync)
	if webAuth.LoggedIn && webAuth.valid() {
		player, err := requestAuthenticatedYouTubeWEBPlayer(ctx, transport, videoID, webAuth, now)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			firstErr = err
		} else if avail := checkYouTubeAvailability(player.PlayabilityStatus); avail != nil {
			firstErr = avail
		} else if hasYouTubeFormatCandidates(player) {
			return []youtubePlayerResponse{player}, nil
		} else if firstErr == nil {
			firstErr = fmt.Errorf("%w: authenticated WEB player returned no URL-bearing formats", ErrUnavailable)
		}
	} else if firstErr == nil {
		firstErr = ErrAuthentication
	}

	attempts := 1 // webpage WEB counts toward the budget
	session := youtubeAuthSessionFromWEB(webAuth)
	for _, profile := range youtubeAuthenticatedFormatRecoveryClients {
		if attempts >= MaxYouTubeClientAttempts {
			break
		}
		if !profile.valid() || !profile.RequireAuth {
			continue
		}
		if profile.RequirePremium && !premium {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		attempts++
		player, err := requestAuthenticatedYouTubePlayer(ctx, transport, videoID, profile, session, now)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if avail := checkYouTubeAvailability(player.PlayabilityStatus); avail != nil {
			if firstErr == nil {
				firstErr = avail
			}
			continue
		}
		if hasYouTubeFormatCandidates(player) {
			return []youtubePlayerResponse{player}, nil
		}
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return nil, fmt.Errorf("%w: authenticated clients returned no URL-bearing formats", ErrUnavailable)
}

func youtubePlayerHasGVSRequiredFormats(player youtubePlayerResponse) bool {
	if player.StreamingData.DASHManifestURL != "" {
		return true
	}
	for _, formats := range [][]youtubeFormat{player.StreamingData.Formats, player.StreamingData.AdaptiveFormats} {
		for _, format := range formats {
			if format.Itag != 18 && (format.URL != "" || format.SignatureCipher != "") {
				return true
			}
		}
	}
	return false
}

func dropYouTubeGVSRequiredFormats(player *youtubePlayerResponse) {
	keep := func(formats []youtubeFormat) []youtubeFormat {
		kept := formats[:0]
		for _, format := range formats {
			if format.Itag == 18 {
				kept = append(kept, format)
			}
		}
		return kept
	}
	player.StreamingData.Formats = keep(player.StreamingData.Formats)
	player.StreamingData.AdaptiveFormats = keep(player.StreamingData.AdaptiveFormats)
	player.StreamingData.DASHManifestURL = ""
}

func applyYouTubeGVSToken(player *youtubePlayerResponse, token string) {
	for index := range player.StreamingData.Formats {
		applyYouTubeFormatToken(&player.StreamingData.Formats[index], token)
	}
	for index := range player.StreamingData.AdaptiveFormats {
		applyYouTubeFormatToken(&player.StreamingData.AdaptiveFormats[index], token)
	}
	player.StreamingData.HLSManifestURL = appendManifestToken(player.StreamingData.HLSManifestURL, token)
	player.StreamingData.DASHManifestURL = appendManifestToken(player.StreamingData.DASHManifestURL, token)
}

func applyYouTubeFormatToken(format *youtubeFormat, token string) {
	if format.URL != "" {
		format.URL = appendQueryToken(format.URL, token)
	}
	if format.SignatureCipher == "" {
		return
	}
	values, err := url.ParseQuery(format.SignatureCipher)
	if err != nil || values.Get("url") == "" {
		return
	}
	values.Set("url", appendQueryToken(values.Get("url"), token))
	format.SignatureCipher = values.Encode()
}

func appendQueryToken(rawURL, token string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return rawURL
	}
	query := parsed.Query()
	query.Set("pot", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func appendManifestToken(rawURL, token string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return rawURL
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/pot/" + token
	return parsed.String()
}

// redactYouTubeClientError keeps credentials and signed URLs out of diagnostics
// while preserving typed extractor categories.
func redactYouTubeClientError(clientName string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrUnsupported) || errors.Is(err, ErrJSONResponseTooLarge) {
		return fmt.Errorf("YouTube %s player request: %w", clientName, err)
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return fmt.Errorf("YouTube %s player request: %w", clientName, status)
	}
	return fmt.Errorf("YouTube %s player request failed", clientName)
}

// youtubePremiumSubscriber reports an attributable Premium logo/tooltip signal
// from ytInitialData, matching yt-dlp YoutubeIE._is_premium_subscriber.
func youtubePremiumSubscriber(initialData []byte) bool {
	if len(initialData) == 0 {
		return false
	}
	var root value.Value
	if json.Unmarshal(initialData, &root) != nil {
		return false
	}
	rootObject, ok := root.Object()
	if !ok {
		return false
	}
	tlr, ok := rootObject.Lookup("topbar").Object()
	if !ok {
		return false
	}
	logo, ok := tlr.Lookup("desktopTopbarRenderer").Object()
	if !ok {
		return false
	}
	logoObj, ok := logo.Lookup("logo").Object()
	if !ok {
		return false
	}
	renderer, ok := logoObj.Lookup("topbarLogoRenderer").Object()
	if !ok {
		return false
	}
	if icon := objectString(renderer, "iconImage", "iconType"); icon == "YOUTUBE_PREMIUM_LOGO" {
		return true
	}
	if tip := rendererText(renderer.Lookup("tooltipText")); strings.Contains(strings.ToLower(tip), "premium") {
		return true
	}
	return false
}
