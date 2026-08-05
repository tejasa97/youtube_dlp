package engine

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubeump"
	"github.com/tejasa97/youtube_dlp/internal/value"
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
	video := sabrRefreshSelection(137, "video")
	audio := sabrRefreshSelection(140, "audio")
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
	if _, err := coordinator.refreshFunc(sabrRefreshSelection(137, "video"))(context.Background()); !errors.Is(err, youtubeump.ErrRefreshRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRRefreshCancelledCreatorDoesNotPoisonWaiters(t *testing.T) {
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
	selection := sabrRefreshSelection(137, "video")
	group := youtubeSABRExtractionGroup(selection)
	creatorCtx, cancelCreator := context.WithCancel(context.Background())
	creatorErr := make(chan error, 1)
	go func() {
		_, err := coordinator.refreshFunc(selection)(creatorCtx)
		creatorErr <- err
	}()
	<-started
	waiterResult := make(chan error, 1)
	go func() {
		_, err := coordinator.refreshFunc(selection)(context.Background())
		waiterResult <- err
	}()
	waitForSABRFlightWaiters(t, coordinator, group, 2)
	start := time.Now()
	cancelCreator()
	if err := <-creatorErr; !errors.Is(err, context.Canceled) {
		t.Fatalf("creator err=%v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("canceled creator did not return promptly")
	}
	close(release)
	if err := <-waiterResult; err != nil {
		t.Fatalf("waiter err=%v", err)
	}
	if extracts.Load() != 1 {
		t.Fatalf("extracts=%d", extracts.Load())
	}
}

func TestYouTubeSABRRefreshAllCallersCancelAbandonsFlight(t *testing.T) {
	started := make(chan struct{})
	extractCanceled := make(chan struct{})
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(ctx context.Context, _ string) (extractor.Extraction, error) {
		close(started)
		<-ctx.Done()
		close(extractCanceled)
		return extractor.Extraction{}, ctx.Err()
	}
	selection := sabrRefreshSelection(137, "video")
	group := youtubeSABRExtractionGroup(selection)
	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())
	err1 := make(chan error, 1)
	err2 := make(chan error, 1)
	go func() { _, err := coordinator.refreshFunc(selection)(ctx1); err1 <- err }()
	<-started
	go func() { _, err := coordinator.refreshFunc(selection)(ctx2); err2 <- err }()
	waitForSABRFlightWaiters(t, coordinator, group, 2)
	cancel1()
	cancel2()
	if !errors.Is(<-err1, context.Canceled) || !errors.Is(<-err2, context.Canceled) {
		t.Fatal("expected both callers canceled")
	}
	select {
	case <-extractCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("extractor did not observe abandonment cancel")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		coordinator.mu.Lock()
		_, present := coordinator.flights[group]
		coordinator.mu.Unlock()
		if !present {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("abandoned flight leaked in map")
		}
		time.Sleep(time.Millisecond)
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
	selection := sabrRefreshSelection(137, "video")
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

func TestYouTubeSABRRefreshDifferentUstreamerNeverSharesCache(t *testing.T) {
	var extracts atomic.Int32
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(_ context.Context, _ string) (extractor.Extraction, error) {
		n := extracts.Add(1)
		ustreamer := "Zml4dHVyZS11c3RyZWFtZXI="
		if n > 1 {
			ustreamer = base64.StdEncoding.EncodeToString([]byte("other-ustreamer"))
		}
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObjectWithUstreamer(137, "video", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fresh%2Btoken", ustreamer),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	first := sabrRefreshSelection(137, "video")
	second := first
	second.YouTubeSABRUstreamerConfig = base64.StdEncoding.EncodeToString([]byte("other-ustreamer"))
	if youtubeSABRRefreshIdentity(first) == youtubeSABRRefreshIdentity(second) {
		t.Fatal("different ustreamer configs must not share identity")
	}
	if _, err := coordinator.refreshFunc(first)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.refreshFunc(second)(context.Background()); err != nil {
		t.Fatal(err)
	}
	if extracts.Load() != 2 {
		t.Fatalf("incompatible ustreamer must not reuse cached material; extracts=%d", extracts.Load())
	}
}

func TestYouTubeSABRRefreshMalformedAndMissingIdentityFailClosed(t *testing.T) {
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	var extracts atomic.Int32
	coordinator.extract = func(context.Context, string) (extractor.Extraction, error) {
		extracts.Add(1)
		return extractor.Extraction{}, nil
	}
	malformed := sabrRefreshSelection(137, "video")
	malformed.YouTubeSABRUstreamerConfig = "%%%"
	if youtubeSABRRefreshIdentity(malformed) != "" {
		t.Fatal("malformed ustreamer must not produce identity")
	}
	if _, err := coordinator.refreshFunc(malformed)(context.Background()); !errors.Is(err, youtubeump.ErrRefreshRejected) {
		t.Fatalf("malformed err=%v", err)
	}
	missingClient := sabrRefreshSelection(137, "video")
	missingClient.YouTubeSABRClientID = 0
	if youtubeSABRRefreshIdentity(missingClient) != "" {
		t.Fatal("missing client id must fail closed")
	}
	if _, err := coordinator.refreshFunc(missingClient)(context.Background()); !errors.Is(err, youtubeump.ErrRefreshRejected) {
		t.Fatalf("missing client id err=%v", err)
	}
	missingVersion := sabrRefreshSelection(137, "video")
	missingVersion.YouTubeSABRClientVersion = ""
	if youtubeSABRRefreshIdentity(missingVersion) != "" {
		t.Fatal("missing client version must fail closed")
	}
	if extracts.Load() != 0 {
		t.Fatalf("invalid identity must not extract; extracts=%d", extracts.Load())
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
	selection := sabrRefreshSelection(137, "video")
	selection.YouTubeSABRUserAgent = "ua"
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

func TestYouTubeSABRRefreshConcurrentRepeatSafe(t *testing.T) {
	var extracts atomic.Int32
	coordinator := newYouTubeSABRRefreshCoordinator(&operation{})
	coordinator.extract = func(_ context.Context, _ string) (extractor.Extraction, error) {
		extracts.Add(1)
		time.Sleep(5 * time.Millisecond)
		info := value.NewInfo(value.NewObject(
			value.Field{Key: "formats", Value: value.List(
				sabrRefreshFormatObject(137, "video", "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fresh%2Btoken"),
			)},
		))
		return extractor.Extraction{Info: info}, nil
	}
	selection := sabrRefreshSelection(137, "video")
	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for index := 0; index < 16; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := coordinator.refreshFunc(selection)(context.Background())
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if extracts.Load() != 1 {
		t.Fatalf("shared completed result extracts=%d", extracts.Load())
	}
}

func sabrRefreshSelection(itag int64, track string) mediaformat.Selection {
	return mediaformat.Selection{
		YouTubeSABR: true, YouTubeSABRVideoID: "fixture0001", YouTubeSABRClientName: "WEB",
		YouTubeSABRClientID: 1, YouTubeSABRClientVersion: "fixture",
		YouTubeSABRVisitorData: "visitor", YouTubeSABRItag: itag, YouTubeSABRTrack: track,
		YouTubeSABRDurationSec: 10, YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001",
		YouTubeSABRUstreamerConfig: "Zml4dHVyZS11c3RyZWFtZXI=",
	}
}

func sabrRefreshFormatObject(itag int64, track, serverURL string) value.Value {
	return sabrRefreshFormatObjectWithUstreamer(itag, track, serverURL, "Zml4dHVyZS11c3RyZWFtZXI=")
}

func sabrRefreshFormatObjectWithUstreamer(itag int64, track, serverURL, ustreamerB64 string) value.Value {
	return value.ObjectValue(value.NewObject(
		value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
		value.Field{Key: "_youtube_sabr_track", Value: value.String(track)},
		value.Field{Key: "_youtube_sabr_itag", Value: value.Int(itag)},
		value.Field{Key: "_youtube_sabr_server_url", Value: value.String(serverURL)},
		value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String(ustreamerB64)},
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
	video := sabrRefreshSelection(137, "video")
	audio := sabrRefreshSelection(140, "audio")
	if youtubeSABRRefreshIdentity(video) == "" || youtubeSABRRefreshIdentity(audio) == "" {
		t.Fatal("expected valid identities")
	}
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
	otherUstreamer := video
	otherUstreamer.YouTubeSABRUstreamerConfig = base64.StdEncoding.EncodeToString([]byte("other-ustreamer"))
	if youtubeSABRRefreshIdentity(video) == youtubeSABRRefreshIdentity(otherUstreamer) {
		t.Fatal("ustreamer hash must bind refresh identity")
	}
	if strings.Contains(youtubeSABRRefreshIdentity(video), "sig=") {
		t.Fatal("identity must not include signed URL material")
	}
}

func waitForSABRFlightWaiters(t *testing.T, coordinator *youtubeSABRRefreshCoordinator, group string, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		coordinator.mu.Lock()
		flight := coordinator.flights[group]
		var got int32
		if flight != nil {
			got = flight.waiters
		}
		coordinator.mu.Unlock()
		if got >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiters=%d want >= %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}
