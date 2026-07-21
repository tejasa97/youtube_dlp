package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

const youtubePlayerAPIURL = "https://www.youtube.com/youtubei/v1/player?prettyPrint=false"

type youtubeClientProfile struct {
	Name          string
	ClientName    string
	ClientID      string
	ClientVersion string
	UserAgent     string
	Context       map[string]any
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
	},
	{
		Name:          "android_vr",
		ClientName:    "ANDROID_VR",
		ClientID:      "28",
		ClientVersion: "1.65.10",
		UserAgent:     "com.google.android.apps.youtube.vr.oculus/1.65.10 (Linux; U; Android 12L; Quest 3 Build/SQ3A.220605.009.A1) gzip",
		Context: map[string]any{
			"androidSdkVersion": 32, "osName": "Android", "osVersion": "12L",
			"deviceMake": "Oculus", "deviceModel": "Quest 3",
		},
	},
}

func requestYouTubePlayer(ctx context.Context, transport Transport, videoID string, profile youtubeClientProfile) (youtubePlayerResponse, error) {
	clientContext := make(map[string]any, len(profile.Context)+2)
	for key, item := range profile.Context {
		clientContext[key] = item
	}
	clientContext["clientName"] = profile.ClientName
	clientContext["clientVersion"] = profile.ClientVersion
	body, err := json.Marshal(map[string]any{
		"context": map[string]any{"client": clientContext},
		"videoId": videoID, "contentCheckOk": true, "racyCheckOk": true,
	})
	if err != nil {
		return youtubePlayerResponse{}, fmt.Errorf("encode YouTube client request: %w", err)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("User-Agent", profile.UserAgent)
	headers.Set("X-Youtube-Client-Name", profile.ClientID)
	headers.Set("X-Youtube-Client-Version", profile.ClientVersion)
	var player youtubePlayerResponse
	if err := RequestJSON(ctx, transport, http.MethodPost, youtubePlayerAPIURL, body, headers, &player); err != nil {
		return youtubePlayerResponse{}, fmt.Errorf("YouTube %s player request: %w", profile.Name, err)
	}
	if player.VideoDetails.VideoID != "" && player.VideoDetails.VideoID != videoID {
		return youtubePlayerResponse{}, fmt.Errorf("%w: %s response video id mismatch", ErrInvalidMetadata, profile.Name)
	}
	return player, nil
}

func recoverYouTubeFormats(ctx context.Context, transport Transport, videoID string) (youtubePlayerResponse, error) {
	var firstRequestError error
	for _, profile := range youtubeFormatRecoveryClients {
		player, err := requestYouTubePlayer(ctx, transport, videoID, profile)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return youtubePlayerResponse{}, err
			}
			if firstRequestError == nil {
				firstRequestError = err
			}
			continue
		}
		if player.PlayabilityStatus.Status == "OK" && hasYouTubeFormatCandidates(player) {
			return player, nil
		}
	}
	if firstRequestError != nil {
		return youtubePlayerResponse{}, firstRequestError
	}
	return youtubePlayerResponse{}, fmt.Errorf("%w: YouTube returned no URL-bearing formats from fallback clients", ErrUnavailable)
}
