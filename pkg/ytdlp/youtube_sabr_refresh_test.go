package ytdlp

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestYouTubeSABRRefreshCoordinatorIdentityAndRedaction(t *testing.T) {
	var extracts atomic.Int32
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(_ context.Context, _ string) (extractor.Extraction, error) {
		extracts.Add(1)
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObject(137, "video", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fresh%2Btoken"),
				sabrRefreshFormatObject(140, "audio", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fresh%2Btoken"),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	video := mediaformat.Selection{
		YouTubeSABR: true, YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture",
		YouTubeSABRVisitorData: "visitor", YouTubeSABRItag: 137, YouTubeSABRTrack: "video",
		YouTubeSABRDurationSec: 10, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
		YouTubeSABRUstreamerConfig: "Zml4dHVyZS11c3RyZWFtZXI=",
	}
	audio := video
	audio.YouTubeSABRItag = 140
	audio.YouTubeSABRTrack = "audio"
	incompatible := video
	incompatible.YouTubeSABRClientName = "ANDROID"

	videoMat, err := coordinator.refreshFunc(video)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	audioMat, err := coordinator.refreshFunc(audio)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if extracts.Load() != 1 {
		t.Fatalf("compatible A/V must share one extraction, got %d", extracts.Load())
	}
	if videoMat.Format.Itag != 137 || audioMat.Format.Itag != 140 {
		t.Fatalf("itag mismatch video=%d audio=%d", videoMat.Format.Itag, audioMat.Format.Itag)
	}
	if _, err := coordinator.refreshFunc(incompatible)(context.Background()); err == nil && extracts.Load() < 2 {
		t.Fatal("expected incompatible client to miss shared materials")
	}
	if extracts.Load() < 2 {
		t.Fatalf("incompatible client must not reuse extraction group, extracts=%d", extracts.Load())
	}
}

func TestYouTubeSABRRefreshRejectsUntrustedHost(t *testing.T) {
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(context.Context, string) (extractor.Extraction, error) {
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObject(137, "video", "https://evil.example/videoplayback?sig=x"),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	selection := mediaformat.Selection{
		YouTubeSABR: true, YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture",
		YouTubeSABRItag: 137, YouTubeSABRTrack: "video", YouTubeSABRDurationSec: 10,
		YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
	}
	if _, err := coordinator.refreshFunc(selection)(context.Background()); !errors.Is(err, youtubeump.ErrRefreshRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRRefreshCancelledWaiterReturnsPromptly(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var extracts atomic.Int32
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(ctx context.Context, _ string) (extractor.Extraction, error) {
		extracts.Add(1)
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return extractor.Extraction{}, ctx.Err()
		}
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObject(137, "video", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fresh%2Btoken"),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	selection := mediaformat.Selection{
		YouTubeSABR: true, YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture",
		YouTubeSABRVisitorData: "visitor", YouTubeSABRItag: 137, YouTubeSABRTrack: "video",
		YouTubeSABRDurationSec: 10, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
		YouTubeSABRUstreamerConfig: "Zml4dHVyZS11c3RyZWFtZXI=",
	}
	leaderDone := make(chan error, 1)
	go func() {
		_, err := coordinator.refreshFunc(selection)(context.Background())
		leaderDone <- err
	}()
	<-started
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	if _, err := coordinator.refreshFunc(selection)(cancelCtx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("cancelled waiter blocked on network flight")
	}
	close(release)
	if err := <-leaderDone; err != nil {
		t.Fatalf("leader err=%v", err)
	}
	if extracts.Load() != 1 {
		t.Fatalf("extracts=%d", extracts.Load())
	}
}

func TestYouTubeSABRReloadUsesTokenViaFocusedAPI(t *testing.T) {
	var sawToken atomic.Bool
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.reloadPlayer = func(_ context.Context, selection mediaformat.Selection, token string) (extractor.Extraction, error) {
		if token != "reload-token-fixture" || selection.YouTubeSABRVideoID != "fixture0001" {
			t.Fatalf("unexpected reload args")
		}
		sawToken.Store(true)
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObject(137, "video", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=reloaded"),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	selection := mediaformat.Selection{
		YouTubeSABR: true, YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture", YouTubeSABRUserAgent: "ua",
		YouTubeSABRVisitorData: "visitor", YouTubeSABRItag: 137, YouTubeSABRTrack: "video",
		YouTubeSABRDurationSec: 10, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
		YouTubeSABRUstreamerConfig: "Zml4dHVyZS11c3RyZWFtZXI=",
	}
	material, err := coordinator.reloadFunc(selection)(context.Background(), youtubeump.ReloadRequest{
		VideoID: "fixture0001", Token: "reload-token-fixture",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawToken.Load() || !strings.Contains(material.ServerURL, "sig=reloaded") {
		t.Fatalf("reload material=%#v", material)
	}
}

func sabrRefreshFormatObject(itag int64, track, serverURL string) value.Value {
	return value.ObjectValue(value.NewObject(
		value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
		value.Field{Key: "_youtube_sabr_track", Value: value.String(track)},
		value.Field{Key: "_youtube_sabr_itag", Value: value.Int(itag)},
		value.Field{Key: "_youtube_sabr_server_url", Value: value.String(serverURL)},
		value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
		value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
		value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
		value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
		value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
		value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
		value.Field{Key: "_youtube_sabr_visitor_data", Value: value.String("visitor")},
		value.Field{Key: "_youtube_client", Value: value.String("WEB")},
	))
}

func TestYouTubeSABRRefreshIdentityKeys(t *testing.T) {
	video := mediaformat.Selection{
		YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture", YouTubeSABRVisitorData: "v",
		YouTubeSABRItag: 137, YouTubeSABRTrack: "video", YouTubeSABRDurationSec: 10,
	}
	audio := video
	audio.YouTubeSABRItag = 140
	audio.YouTubeSABRTrack = "audio"
	if youtubeSABRRefreshIdentity(video) == youtubeSABRRefreshIdentity(audio) {
		t.Fatal("A/V identities must differ by itag/track")
	}
	if youtubeSABRExtractionGroup(video) != youtubeSABRExtractionGroup(audio) {
		t.Fatal("compatible A/V must share extraction group")
	}
	drc := video
	drc.YouTubeSABRDrc = true
	if youtubeSABRRefreshIdentity(video) == youtubeSABRRefreshIdentity(drc) {
		t.Fatal("DRC must bind refresh identity")
	}
	if strings.Contains(youtubeSABRRefreshIdentity(video), "sig=") {
		t.Fatal("identity must not include signed URL material")
	}
}
