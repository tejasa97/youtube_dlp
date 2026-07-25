package ytdlp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"sync"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

// youtubeSABRRefreshCoordinator re-extracts SABR inventory for one operation.
// Compatible A/V selections that share video/client/visitor identity share one
// context-aware extraction flight; per-itag materials are stored separately.
// Mutexes cover only map/flight bookkeeping — extract I/O runs unlocked.
type youtubeSABRRefreshCoordinator struct {
	operation *operation
	episode   *youtubepot.Episode

	mu        sync.Mutex
	attempts  int
	materials map[string]youtubeump.RefreshMaterial
	flights   map[string]*sabrExtractionFlight

	extract      func(context.Context, string) (extractor.Extraction, error)
	reloadPlayer func(context.Context, mediaformat.Selection, string) (extractor.Extraction, error)
}

// sabrExtractionFlight owns shared extraction work independent of any single
// caller's context. Waiters select between completion and their own cancel.
// When the last waiter leaves, shared work is canceled.
type sabrExtractionFlight struct {
	done      chan struct{}
	cancel    context.CancelFunc
	waiters   int32
	abandoned bool
	err       error
}

func newYouTubeSABRRefreshCoordinator(operation *operation) *youtubeSABRRefreshCoordinator {
	coordinator := &youtubeSABRRefreshCoordinator{
		operation: operation,
		episode:   youtubepot.NewEpisode(0),
		materials: make(map[string]youtubeump.RefreshMaterial),
		flights:   make(map[string]*sabrExtractionFlight),
	}
	coordinator.extract = coordinator.extractSource
	coordinator.reloadPlayer = coordinator.reloadSource
	return coordinator
}

func (coordinator *youtubeSABRRefreshCoordinator) refreshFunc(selection mediaformat.Selection) youtubeump.RefreshFunc {
	return func(ctx context.Context) (youtubeump.RefreshMaterial, error) {
		return coordinator.refresh(ctx, selection)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) reloadFunc(selection mediaformat.Selection) youtubeump.ReloadFunc {
	return func(ctx context.Context, req youtubeump.ReloadRequest) (youtubeump.RefreshMaterial, error) {
		if req.Token == "" {
			return youtubeump.RefreshMaterial{}, youtubeump.ErrReloadRejected
		}
		if req.VideoID != "" && req.VideoID != selection.YouTubeSABRVideoID {
			return youtubeump.RefreshMaterial{}, youtubeump.ErrReloadRejected
		}
		return coordinator.reload(ctx, selection, req.Token)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) poTokenSource(selection mediaformat.Selection) youtubeump.POTokenSource {
	return func(ctx context.Context) ([]byte, error) {
		return resolveYouTubeSABRPOTokenEpisode(ctx, coordinator.operation, selection, coordinator.episode)
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) refresh(ctx context.Context, selection mediaformat.Selection) (youtubeump.RefreshMaterial, error) {
	return coordinator.runFlight(ctx, selection, youtubeSABRExtractionGroup(selection), func(runCtx context.Context) (extractor.Extraction, error) {
		sourceURL := selection.YouTubeSourceURL
		if sourceURL == "" {
			return extractor.Extraction{}, youtubeump.ErrRefreshRejected
		}
		return coordinator.extract(runCtx, sourceURL)
	})
}

func (coordinator *youtubeSABRRefreshCoordinator) reload(ctx context.Context, selection mediaformat.Selection, token string) (youtubeump.RefreshMaterial, error) {
	group := youtubeSABRExtractionGroup(selection) + "\x00reload"
	return coordinator.runFlight(ctx, selection, group, func(runCtx context.Context) (extractor.Extraction, error) {
		return coordinator.reloadPlayer(runCtx, selection, token)
	})
}

func (coordinator *youtubeSABRRefreshCoordinator) runFlight(
	ctx context.Context,
	selection mediaformat.Selection,
	group string,
	lead func(context.Context) (extractor.Extraction, error),
) (youtubeump.RefreshMaterial, error) {
	if coordinator == nil || coordinator.operation == nil {
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
	}
	if err := ctx.Err(); err != nil {
		return youtubeump.RefreshMaterial{}, err
	}
	identity := youtubeSABRRefreshIdentity(selection)
	if identity == "" || group == "" {
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
	}

	coordinator.mu.Lock()
	if material, ok := coordinator.materials[identity]; ok {
		coordinator.mu.Unlock()
		return cloneRefreshMaterial(material), nil
	}
	if existing := coordinator.flights[group]; existing != nil {
		if existing.abandoned {
			delete(coordinator.flights, group)
		} else {
			existing.waiters++
			coordinator.mu.Unlock()
			return coordinator.waitFlight(ctx, existing, identity)
		}
	}
	if coordinator.attempts >= youtubeump.MaxSabrRefreshAttempts {
		coordinator.mu.Unlock()
		return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshBudget
	}
	coordinator.attempts++
	workCtx, cancel := context.WithCancel(context.Background())
	flight := &sabrExtractionFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	coordinator.flights[group] = flight
	coordinator.mu.Unlock()

	go func() {
		extracted, err := lead(workCtx)
		if err == nil {
			err = coordinator.storeExtraction(workCtx, selection, extracted)
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			err = youtubeump.ErrRefreshRejected
		}
		flight.err = err
		coordinator.mu.Lock()
		if coordinator.flights[group] == flight {
			delete(coordinator.flights, group)
		}
		coordinator.mu.Unlock()
		close(flight.done)
	}()

	return coordinator.waitFlight(ctx, flight, identity)
}

func (coordinator *youtubeSABRRefreshCoordinator) waitFlight(ctx context.Context, flight *sabrExtractionFlight, identity string) (youtubeump.RefreshMaterial, error) {
	select {
	case <-flight.done:
		if flight.err != nil {
			return youtubeump.RefreshMaterial{}, flight.err
		}
		coordinator.mu.Lock()
		material, ok := coordinator.materials[identity]
		coordinator.mu.Unlock()
		if !ok {
			return youtubeump.RefreshMaterial{}, youtubeump.ErrRefreshRejected
		}
		return cloneRefreshMaterial(material), nil
	case <-ctx.Done():
		coordinator.mu.Lock()
		flight.waiters--
		if flight.waiters == 0 {
			flight.abandoned = true
			flight.cancel()
		}
		coordinator.mu.Unlock()
		return youtubeump.RefreshMaterial{}, ctx.Err()
	}
}

func (coordinator *youtubeSABRRefreshCoordinator) storeExtraction(ctx context.Context, selection mediaformat.Selection, extracted extractor.Extraction) error {
	formats, _ := extracted.Info.Formats()
	stored := make(map[string]youtubeump.RefreshMaterial)
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
			YouTubeSABRVideoID:         material.VideoID,
			YouTubeSABRClientName:      selection.YouTubeSABRClientName,
			YouTubeSABRClientID:        selection.YouTubeSABRClientID,
			YouTubeSABRClientVersion:   selection.YouTubeSABRClientVersion,
			YouTubeSABRVisitorData:     material.VisitorData,
			YouTubeSABRItag:            int64(material.Format.Itag),
			YouTubeSABRTrack:           trackKindFromMaterial(object),
			YouTubeSABRDurationSec:     material.DurationSec,
			YouTubeSABRDrc:             material.DrcEnabled,
			YouTubeSABRAudioTrackID:    material.AudioTrackID,
			YouTubeSABRUstreamerConfig: base64.StdEncoding.EncodeToString(material.UstreamerConfig),
		}
		key := youtubeSABRRefreshIdentity(peer)
		if key == "" {
			continue
		}
		stored[key] = material
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	for key, material := range stored {
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
	clientID, _ := object.Lookup("_youtube_sabr_client_id").Int()
	clientVersion, _ := object.Lookup("_youtube_sabr_client_version").StringValue()
	drc, _ := object.Lookup("_youtube_sabr_drc").Bool()
	audioTrackID, _ := object.Lookup("_youtube_sabr_audio_track_id").StringValue()
	if videoID == "" || videoID != selection.YouTubeSABRVideoID {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if clientName == "" || clientName != selection.YouTubeSABRClientName {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if visitor != selection.YouTubeSABRVisitorData {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if selection.YouTubeSABRClientID <= 0 || clientID != selection.YouTubeSABRClientID {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if selection.YouTubeSABRClientVersion == "" || clientVersion != selection.YouTubeSABRClientVersion {
		return youtubeump.RefreshMaterial{}, false, nil
	}
	if duration == 0 || duration != selection.YouTubeSABRDurationSec {
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
	userAgent, _ := object.Lookup("_youtube_sabr_user_agent").StringValue()
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

func (coordinator *youtubeSABRRefreshCoordinator) reloadSource(ctx context.Context, selection mediaformat.Selection, token string) (extractor.Extraction, error) {
	operation := coordinator.operation
	if operation == nil || operation.client == nil || operation.transport == nil {
		return extractor.Extraction{}, youtubeump.ErrReloadRejected
	}
	if selection.YouTubeSABRClientName == "" || selection.YouTubeSABRClientID <= 0 ||
		selection.YouTubeSABRClientVersion == "" || selection.YouTubeSABRUserAgent == "" {
		return extractor.Extraction{}, youtubeump.ErrReloadRejected
	}
	return extractor.ReloadYouTubePlayer(ctx, operation.transport, extractor.YouTubeReloadRequest{
		VideoID:       selection.YouTubeSABRVideoID,
		VisitorData:   selection.YouTubeSABRVisitorData,
		WebpageURL:    selection.YouTubeSourceURL,
		ReloadToken:   token,
		ClientName:    selection.YouTubeSABRClientName,
		ClientID:      strconv.FormatInt(selection.YouTubeSABRClientID, 10),
		ClientVersion: selection.YouTubeSABRClientVersion,
		UserAgent:     selection.YouTubeSABRUserAgent,
		DurationSec:   selection.YouTubeSABRDurationSec,
		Tokens:        operation.client.youtubePOT,
	})
}

// youtubeSABRRefreshIdentity binds cached refresh material to exact selection
// identity, including the decoded ustreamer-config SHA-256. Invalid expected
// ustreamer base64 and missing required client/format dimensions fail closed
// (empty key). Visitor/audio-track may be empty when the architecture permits.
func youtubeSABRRefreshIdentity(selection mediaformat.Selection) string {
	if selection.YouTubeSABRVideoID == "" || selection.YouTubeSABRClientName == "" ||
		selection.YouTubeSABRClientID <= 0 || selection.YouTubeSABRClientVersion == "" ||
		selection.YouTubeSABRItag <= 0 || selection.YouTubeSABRTrack == "" ||
		selection.YouTubeSABRDurationSec <= 0 || selection.YouTubeSABRUstreamerConfig == "" {
		return ""
	}
	ustreamer, err := base64.StdEncoding.DecodeString(selection.YouTubeSABRUstreamerConfig)
	if err != nil || len(ustreamer) == 0 {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%d\x00%s\x00%d\x00%t\x00%s\x00%s",
		selection.YouTubeSABRVideoID,
		selection.YouTubeSABRClientName,
		selection.YouTubeSABRClientID,
		selection.YouTubeSABRClientVersion,
		selection.YouTubeSABRVisitorData,
		selection.YouTubeSABRItag,
		selection.YouTubeSABRTrack,
		selection.YouTubeSABRDurationSec,
		selection.YouTubeSABRDrc,
		selection.YouTubeSABRAudioTrackID,
		youtubeump.HashUstreamerConfig(ustreamer),
	)
}

func youtubeSABRExtractionGroup(selection mediaformat.Selection) string {
	if selection.YouTubeSABRVideoID == "" || selection.YouTubeSABRClientName == "" ||
		selection.YouTubeSABRClientID <= 0 || selection.YouTubeSABRClientVersion == "" {
		return ""
	}
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s",
		selection.YouTubeSABRVideoID,
		selection.YouTubeSABRClientName,
		selection.YouTubeSABRClientID,
		selection.YouTubeSABRClientVersion,
		selection.YouTubeSABRVisitorData,
	)
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
