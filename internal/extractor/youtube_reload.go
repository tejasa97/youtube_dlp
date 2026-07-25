package extractor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

// Bound matches LuanRT/googlevideo MaxReloadTokenBytes used by youtubeump.
const youtubeMaxReloadTokenBytes = 4096

// YouTubeReloadRequest carries an attributable RELOAD_PLAYER_RESPONSE token for
// a focused /player reload. The token is never rendered by String/GoString.
type YouTubeReloadRequest struct {
	VideoID       string
	VisitorData   string
	PlayerURL     string
	WebpageURL    string
	ReloadToken   string
	ClientName    string
	ClientID      string
	ClientVersion string
	UserAgent     string
	DurationSec   int64
	Tokens        *youtubepot.Director
}

func (YouTubeReloadRequest) String() string   { return "[redacted YouTube reload request]" }
func (YouTubeReloadRequest) GoString() string { return "extractor.YouTubeReloadRequest{[redacted]}" }

// ReloadYouTubePlayer re-requests /player with playbackContext.reloadPlaybackContext
// .reloadPlaybackParams.token. Provenance: LuanRT/googlevideo
// examples/sabr-shaka-example/src/main.ts at d2fa40d761034a286cf60ee033653307a1295b0c.
func ReloadYouTubePlayer(ctx context.Context, transport Transport, req YouTubeReloadRequest) (Extraction, error) {
	if ctx == nil || transport == nil {
		return Extraction{}, ErrInvalidMetadata
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if req.VideoID == "" || req.ReloadToken == "" || req.ClientName == "" || req.ClientID == "" ||
		req.ClientVersion == "" || req.UserAgent == "" {
		return Extraction{}, fmt.Errorf("%w: incomplete reload identity", ErrInvalidMetadata)
	}
	if len(req.ReloadToken) > youtubeMaxReloadTokenBytes || strings.TrimSpace(req.ReloadToken) != req.ReloadToken {
		return Extraction{}, fmt.Errorf("%w: invalid reload token", ErrInvalidMetadata)
	}
	profile := youtubeClientProfile{
		Name:          strings.ToLower(req.ClientName),
		ClientName:    req.ClientName,
		ClientID:      req.ClientID,
		ClientVersion: req.ClientVersion,
		UserAgent:     req.UserAgent,
		PlayerPolicy:  youtubePOTPolicy{Recommended: true},
	}
	player, err := requestYouTubePlayerReload(ctx, transport, req.VideoID, req.VisitorData, req.PlayerURL, profile, req.Tokens, req.ReloadToken)
	if err != nil {
		return Extraction{}, err
	}
	if player.PlayabilityStatus.Status != "" && player.PlayabilityStatus.Status != "OK" {
		return Extraction{}, fmt.Errorf("%w: reload player status", ErrUnavailable)
	}
	if !hasYouTubeSABR([]youtubePlayerResponse{player}) {
		return Extraction{}, fmt.Errorf("%w: reload player missing SABR inventory", ErrInvalidMetadata)
	}
	webpageURL := req.WebpageURL
	if webpageURL == "" {
		webpageURL = "https://www.youtube.com/watch?v=" + req.VideoID
	}
	duration := req.DurationSec
	if duration <= 0 {
		if parsed, err := strconv.ParseInt(player.VideoDetails.LengthSeconds, 10, 64); err == nil && parsed > 0 {
			duration = parsed
		}
	}
	formats, err := buildYouTubeSABRFormats(ctx, []youtubePlayerResponse{player}, webpageURL, req.VideoID, duration, duration > 0, "")
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(req.VideoID)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))
	if duration > 0 {
		info.Set("duration", value.Int(duration))
	}
	return Extraction{Info: info}, nil
}
