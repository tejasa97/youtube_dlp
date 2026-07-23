package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
)

type youtubeLiveRefreshCoordinator struct {
	operation *operation

	mu        sync.Mutex
	cachedAt  time.Time
	cacheFor  time.Duration
	stillLive bool
	formats   map[string]youtubelive.LiveRefreshResult
	extract   func(context.Context, string) (extractor.Extraction, error)
}

func newYouTubeLiveRefreshCoordinator(operation *operation) *youtubeLiveRefreshCoordinator {
	cacheFor := operation.request.Downloader.LivePollInterval
	if cacheFor <= 0 {
		cacheFor = 5 * time.Second
	}
	coordinator := &youtubeLiveRefreshCoordinator{operation: operation, cacheFor: cacheFor}
	coordinator.extract = coordinator.extractSource
	return coordinator
}

func (coordinator *youtubeLiveRefreshCoordinator) callback(selection mediaformat.Selection) youtubelive.LiveRefreshFunc {
	identity := youtubeLiveIdentity(selection.YouTubeItag, selection.YouTubeClient)
	sourceURL := selection.YouTubeSourceURL
	return func(ctx context.Context, _ youtubelive.LiveRefreshRequest) (youtubelive.LiveRefreshResult, error) {
		return coordinator.refresh(ctx, sourceURL, identity)
	}
}

func (coordinator *youtubeLiveRefreshCoordinator) refresh(ctx context.Context, sourceURL, identity string) (youtubelive.LiveRefreshResult, error) {
	if coordinator == nil || coordinator.operation == nil || sourceURL == "" || identity == "" {
		return youtubelive.LiveRefreshResult{}, youtubelive.ErrLiveRefreshFailed
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return youtubelive.LiveRefreshResult{}, err
	}
	now := time.Now()
	if coordinator.formats == nil || now.Sub(coordinator.cachedAt) >= coordinator.cacheFor {
		if err := coordinator.reload(ctx, sourceURL); err != nil {
			return youtubelive.LiveRefreshResult{}, err
		}
		coordinator.cachedAt = time.Now()
	}
	result, ok := coordinator.formats[identity]
	if !ok {
		if !coordinator.stillLive {
			return youtubelive.LiveRefreshResult{StillLive: false}, nil
		}
		return youtubelive.LiveRefreshResult{}, youtubelive.ErrLiveRefreshFailed
	}
	result.StillLive = coordinator.stillLive
	return result, nil
}

func (coordinator *youtubeLiveRefreshCoordinator) reload(ctx context.Context, sourceURL string) error {
	extracted, err := coordinator.extract(ctx, sourceURL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		return youtubelive.ErrLiveRefreshFailed
	}
	status, _ := extracted.Info.Lookup("live_status").StringValue()
	coordinator.stillLive = status == "is_live"
	coordinator.formats = make(map[string]youtubelive.LiveRefreshResult)
	formats, _ := extracted.Info.Formats()
	for _, candidate := range formats {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		itag, _ := object.Lookup("_youtube_itag").Int()
		client, _ := object.Lookup("_youtube_client").StringValue()
		rawURL, _ := object.Lookup("url").StringValue()
		if itag <= 0 || client == "" || rawURL == "" {
			continue
		}
		headers, headerErr := mediaformat.MergeHeaders(
			extracted.Info.Lookup("http_headers"), object.Lookup("http_headers"))
		if headerErr != nil {
			return fmt.Errorf("%w: refreshed headers", youtubelive.ErrLiveRefreshFailed)
		}
		coordinator.formats[youtubeLiveIdentity(itag, client)] = youtubelive.LiveRefreshResult{
			URL: rawURL, Headers: headers, StillLive: coordinator.stillLive,
		}
	}
	return nil
}

func (coordinator *youtubeLiveRefreshCoordinator) extractSource(ctx context.Context, sourceURL string) (extractor.Extraction, error) {
	operation := coordinator.operation
	if operation == nil || operation.client == nil || operation.transport == nil {
		return extractor.Extraction{}, youtubelive.ErrLiveRefreshFailed
	}
	return extractor.NewYouTube().Extract(ctx, extractor.Request{
		URL: sourceURL, Transport: operation.transport, ChallengeSolver: operation.solver,
		Credentials: operation.credentials, YouTubePOT: operation.client.youtubePOT,
		YouTubeLiveFromStart: true,
	})
}

func youtubeLiveIdentity(itag int64, client string) string {
	if itag <= 0 || client == "" {
		return ""
	}
	return fmt.Sprintf("%d\x00%s", itag, client)
}

type lockedEventSink struct {
	mu   sync.Mutex
	sink events.Sink
}

func (sink *lockedEventSink) Emit(ctx context.Context, event events.Event) error {
	sink.mu.Lock()
	defer sink.mu.Unlock()
	return sink.sink.Emit(ctx, event)
}
