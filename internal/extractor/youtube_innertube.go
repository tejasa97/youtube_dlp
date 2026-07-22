package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

const youtubePlayerAPIURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"

type youtubeClientProfile struct {
	Name          string
	ClientName    string
	ClientID      string
	ClientVersion string
	UserAgent     string
	Context       map[string]any
	GVSPolicy     youtubePOTPolicy
	PlayerPolicy  youtubePOTPolicy
	SubsPolicy    youtubePOTPolicy
}

type youtubePOTPolicy struct {
	Required                   bool
	Recommended                bool
	NotRequiredWithPlayerToken bool
}

func (policy youtubePOTPolicy) required(playerTokenProvided bool) bool {
	return policy.Required && !(policy.NotRequiredWithPlayerToken && playerTokenProvided)
}

// These profiles are intentionally small and data-driven. Their values match
// the pinned yt-dlp reference client table; they are format recovery clients,
// not a replacement for the webpage response's metadata.
var youtubeFormatRecoveryClients = []youtubeClientProfile{
	{
		Name:          "android",
		ClientName:    "ANDROID",
		ClientID:      "3",
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
		Name:          "android_vr",
		ClientName:    "ANDROID_VR",
		ClientID:      "28",
		ClientVersion: "1.65.10",
		UserAgent:     "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; eureka-user Build/SQ3A.220605.009.A1) gzip",
		Context: map[string]any{
			"androidSdkVersion": 32, "osName": "Android", "osVersion": "12L",
			"deviceMake": "Oculus", "deviceModel": "Quest 3",
		},
	},
}

func requestYouTubePlayer(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, profile youtubeClientProfile, tokens *youtubepot.Director) (youtubePlayerResponse, error) {
	clientContext := make(map[string]any, len(profile.Context)+3)
	for key, item := range profile.Context {
		clientContext[key] = item
	}
	clientContext["clientName"] = profile.ClientName
	clientContext["clientVersion"] = profile.ClientVersion
	if visitorData != "" {
		clientContext["visitorData"] = visitorData
	}
	payload := map[string]any{
		"context": map[string]any{"client": clientContext},
		"videoId": videoID,
		"playbackContext": map[string]any{
			"contentPlaybackContext": map[string]any{"html5Preference": "HTML5_PREF_WANTS"},
		},
		"contentCheckOk": true, "racyCheckOk": true,
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
		return youtubePlayerResponse{}, fmt.Errorf("YouTube %s player request: %w", profile.Name, err)
	}
	if player.VideoDetails.VideoID != "" && player.VideoDetails.VideoID != videoID {
		return youtubePlayerResponse{}, fmt.Errorf("%w: %s response video id mismatch", ErrInvalidMetadata, profile.Name)
	}
	player.clientName = profile.ClientName
	player.visitorData = visitorData
	player.playerURL = playerURL
	player.playerTokenProvided = playerTokenProvided
	player.subsPolicy = profile.SubsPolicy
	return player, nil
}

func recoverYouTubeFormats(ctx context.Context, transport Transport, videoID, visitorData, playerURL string, tokens *youtubepot.Director) ([]youtubePlayerResponse, error) {
	var firstRequestError error
	var recovered []youtubePlayerResponse
	for _, profile := range youtubeFormatRecoveryClients {
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
				required := profile.GVSPolicy.required(player.playerTokenProvided) && youtubePlayerHasGVSRequiredFormats(player)
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
