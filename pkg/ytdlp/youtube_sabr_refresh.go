package ytdlp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

// youtubeSABRRefreshCoordinator re-extracts SABR inventory for one operation.
// Compatible A/V selections that share video/client/visitor identity share one
// extraction flight; per-itag materials are stored separately and never cross
// incompatible credentials.
type youtubeSABRRefreshCoordinator struct {
	operation *operation
	episode   *youtubepot.Episode

	mu          sync.Mutex
	attempts    int
	extractions map[string]bool
	materials   map[string]youtubeump.RefreshMaterial
	extract     func(context.Context, string) (extractor.Extraction, error)
}

func newYouTubeSABRRefreshCoordinator(operation *operation) *youtubeSABRRefreshCoordinator {
	coordinator := &youtubeSABRRefreshCoordinator{
		operation:   operation,
		episode:     youtubepot.NewEpisode(0),
		extractions: make(map[string]bool),
		materials:   make(map[string]youtubeump.RefreshMaterial),
	}
	coordinator.extract = coordinator.extractSource
	return coordinator
}

func (coordinator *youtubeSABRRefreshCoordinator) refreshFunc(selection mediaformat.Selection) youtubeump.RefreshFunc {
	return func(ctx context.Context) (youtubeump.RefreshMaterial, error) {
		return coordinator.refresh(ctx, selection)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) reloadFunc(selection mediaformat.Selection) youtubeump.ReloadFunc {
	return func(ctx context.Context, req youtubeump.ReloadRequest) (youtubeump.RefreshMaterial, error) {
		if req.VideoID != "" && req.VideoID != selection.YouTubeSABRVideoID {
			return youtubeump.RefreshMaterial{}, youtubeump.ErrReloadRejected
		}
		_ = req.Token
		return coordinator.refresh(ctx, selection)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) poTokenSource(selection mediaformat.Selection) youtubeump.POTokenSource {
	return func(ctx context.Context) ([]byte, error) {
		return resolveYouTubeSABRPOTokenEpisode(ctx, coordinator.operation, selection, coordinator.episode)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) refresh(ctx context.Context, selection mediaformat.Selection) (youtubeump.RefreshMaterial, error) {
	if coordinator == nil || coordinator.operation == nil {
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
	}
	sourceURL := selection.YouTubeSourceURL
	identity := youtubeSABRRefreshIdentity(selection)
	group := youtubeSABRExtractionGroup(selection)
	if sourceURL == "" || identity == "" || group == "" {
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return youtubeump.RefreshMaterial{}, err
	}
	if material, ok := coordinator.materials[identity]; ok {
		return cloneRefreshMaterial(material), nil
	}
	if !coordinator.extractions[group] {
		if coordinator.attempts >= youtubeump.MaxSabrRefreshAttempts {
			return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshBudget
		}
		coordinator.attempts++
		extracted, err := coordinator.extract(ctx, sourceURL)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return youtubeump.RefreshMaterial{}, err
			}
			return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
		}
		if err := coordinator.storeExtraction(ctx, selection, extracted); err != nil {
			return youtubeump.RefreshMaterial{}, err
		}
		coordinator.extractions[group] = true
	}
	material, ok := coordinator.materials[identity]
	if !ok {
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
	}
	return cloneRefreshMaterial(material), nil
}

func (coordinator *youtubeSABRRefreshCoordinator) storeExtraction(ctx context.Context, selection mediaformat.Selection, extracted extractor.Extraction) error {
	formats, _ := extracted.Info.Formats()
	for _, candidate := range formats {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		material, matchOK, err := sabrMaterialFromObject(ctx, coordinator.operation, selection, object, coordinator.episode)
		if err != nil {
			return err
		}
		if !matchOK {
			continue
		}
		peer := mediaformat.Selection{
			YouTubeSABRVideoID:      material.VideoID,
			YouTubeSABRClientName:   selection.YouTubeSABRClientName,
			YouTubeSABRVisitorData:  material.VisitorData,
			YouTubeSABRItag:         int64(material.Format.Itag),
			YouTubeSABRTrack:        trackKindFromMaterial(object),
			YouTubeSABRDurationSec:  material.DurationSec,
			YouTubeSABRDrc:          material.DrcEnabled,
			YouTubeSABRAudioTrackID: material.AudioTrackID,
		}
		key := youtubeSABRRefreshIdentity(peer)
		if key == "" {
			continue
		}
		coordinator.materials[key] = material
	}
	return nil
}

func sabrMaterialFromObject(ctx context.Context, operation *operation, selection mediaformat.Selection, object *value.Object, episode *youtubepot.Episode) (youtubeump.RefreshMaterial, bool, error) {
	sabr, _ := object.Lookup("_youtube_sabr").Bool()
	if !sabr {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	videoID, _ := object.Lookup("_youtube_sabr_video_id").StringValue()
	clientName, _ := object.Lookup("_youtube_client").StringValue()
	visitor, _ := object.Lookup("_youtube_sabr_visitor_data").StringValue()
	track, _ := object.Lookup("_youtube_sabr_track").StringValue()
	itag, _ := object.Lookup("_youtube_sabr_itag").Int()
	duration, _ := object.Lookup("_youtube_sabr_duration_sec").Int()
	if videoID != selection.YouTubeSABRVideoID || clientName != selection.YouTubeSABRClientName {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if selection.YouTubeSABRVisitorData != "" && visitor != "" && visitor != selection.YouTubeSABRVisitorData {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	serverURL, _ := object.Lookup("_youtube_sabr_server_url").StringValue()
	ustreamerB64, _ := object.Lookup("_youtube_sabr_ustreamer_config").StringValue()
	ustreamer, err := base64.StdEncoding.DecodeString(ustreamerB64)
	if err != nil || serverURL == "" || len(ustreamer) == 0 {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if _, err := youtubeump.ValidateSABRURL(serverURL); err != nil {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	lastModified, _ := object.Lookup("_youtube_sabr_last_modified").Int()
	xtags, _ := object.Lookup("_youtube_sabr_xtags").StringValue()
	clientID, _ := object.Lookup("_youtube_sabr_client_id").Int()
	clientVersion, _ := object.Lookup("_youtube_sabr_client_version").StringValue()
	userAgent, _ := object.Lookup("_youtube_sabr_user_agent").StringValue()
	drc, _ := object.Lookup("_youtube_sabr_drc").Bool()
	audioTrackID, _ := object.Lookup("_youtube_sabr_audio_track_id").StringValue()
	format, err := youtubeump.FormatIDFromItag(itag, lastModified, xtags)
	if err != nil {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	clientInfo, err := youtubeump.ClientInfoFromID(clientID, clientVersion)
	if err != nil {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	peerSelection := selection
	peerSelection.YouTubeSABRItag = itag
	peerSelection.YouTubeSABRTrack = track
	peerSelection.YouTubeSABRDurationSec = duration
	peerSelection.YouTubeSABRDrc = drc
	peerSelection.YouTubeSABRAudioTrackID = audioTrackID
	peerSelection.YouTubeSABRVisitorData = visitor
	poToken, err := resolveYouTubeSABRPOTokenEpisode(ctx, operation, peerSelection, episode)
	if err != nil {
		return youtubeump.RefreshMaterial{}, false, err
	}
	_ = track
	return youtubeump.RefreshMaterial{
		ServerURL:       serverURL,
		UstreamerConfig: ustreamer,
		POToken:         poToken,
		Format:          format,
		ClientInfo:      clientInfo,
		VisitorData:     visitor,
		DurationSec:     duration,
		UserAgent:       userAgent,
		DrcEnabled:      drc,
		AudioTrackID:    audioTrackID,
		VideoID:         videoID,
	}, true, nil
}

func trackKindFromMaterial(object *value.Object) string {
	track, _ := object.Lookup("_youtube_sabr_track").StringValue()
	return track
}

func (coordinator *youtubeSABRRefreshCoordinator) extractSource(ctx context.Context, sourceURL string) (extractor.Extraction, error) {
	operation := coordinator.operation
	if operation == nil || operation.client == nil || operation.transport == nil {
		return extractor.Extraction{}, youtubeump.ErrRefreshRejected
	}
	return extractor.NewYouTube().Extract(ctx, extractor.Request{
		URL: sourceURL, Transport: operation.transport, ChallengeSolver: operation.solver,
		Credentials: operation.credentials, YouTubePOT: operation.client.youtubePOT,
	})
}

func youtubeSABRRefreshIdentity(selection mediaformat.Selection) string {
	if selection.YouTubeSABRVideoID == "" || selection.YouTubeSABRClientName == "" || selection.YouTubeSABRItag <= 0 || selection.YouTubeSABRTrack == "" {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%s\x00%d\x00%s\x00%d",
		selection.YouTubeSABRVideoID,
		selection.YouTubeSABRClientName,
		selection.YouTubeSABRVisitorData,
		selection.YouTubeSABRItag,
		selection.YouTubeSABRTrack,
		selection.YouTubeSABRDurationSec,
	)
}

func youtubeSABRExtractionGroup(selection mediaformat.Selection) string {
	if selection.YouTubeSABRVideoID == "" || selection.YouTubeSABRClientName == "" {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%s", selection.YouTubeSABRVideoID, selection.YouTubeSABRClientName, selection.YouTubeSABRVisitorData)
}

func cloneRefreshMaterial(material youtubeump.RefreshMaterial) youtubeump.RefreshMaterial {
	material.UstreamerConfig = append([]byte(nil), material.UstreamerConfig...)
	material.POToken = append([]byte(nil), material.POToken...)
	return material
}

func resolveYouTubeSABRPOTokenEpisode(ctx context.Context, operation *operation, selected mediaformat.Selection, episode *youtubepot.Episode) ([]byte, error) {
	if operation == nil || operation.client == nil || operation.client.youtubePOT == nil {
		return nil, nil
	}
	token, ok, err := operation.client.youtubePOT.ResolveEpisode(ctx, youtubepot.Request{
		Context:     youtubepot.ContextGVS,
		Client:      selected.YouTubeSABRClientName,
		VisitorData: selected.YouTubeSABRVisitorData,
		VideoID:     selected.YouTubeSABRVideoID,
	}, false, true, episode)
	if err != nil {
		return nil, err
	}
	if !ok || token == "" {
		return nil, nil
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(token); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(token); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("%w: invalid SABR PO token encoding", errInvalidRequestOptions)
}
