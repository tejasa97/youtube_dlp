package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/events"
	"github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/youtubeump"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestYouTubeSABRProductDownloadDispatch(t *testing.T) {
	var lastRequest *http.Request
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			lastRequest = request
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	path, bytesWritten, err := operation.downloadSelection(context.Background(), sabrSelection(t), root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytesWritten != int64(len("INITfixture")) {
		t.Fatalf("path=%q bytes=%d", path, bytesWritten)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "INITfixture" {
		t.Fatalf("payload=%q", got)
	}
	if lastRequest == nil ||
		lastRequest.Method != http.MethodPost ||
		lastRequest.Header.Get("Content-Type") != "application/x-protobuf" ||
		lastRequest.Header.Get("Accept") != "application/vnd.yt-ump" {
		t.Fatalf("request=%#v", lastRequest)
	}
	if lastRequest.Header.Get("Cookie") != "" || lastRequest.Header.Get("Authorization") != "" {
		t.Fatalf("credential headers leaked: %#v", lastRequest.Header)
	}
	if !strings.Contains(lastRequest.URL.String(), "rn=0") {
		t.Fatalf("url=%s", lastRequest.URL)
	}
}

func TestYouTubeSABRProductDownloadViaDefaultSelection(t *testing.T) {
	info := sabrSelectionInfoWithAudio(t)
	selections, err := format.Default(info, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 2 {
		t.Fatalf("selections=%d", len(selections))
	}
	for _, selection := range selections {
		if selection.URL != "" {
			t.Fatalf("SABR selection must keep empty metadata URL, got %q", selection.URL)
		}
		if !selection.YouTubeSABR || selection.YouTubeSABRServerURL == "" {
			t.Fatalf("selection=%+v", selection)
		}
	}
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	path, bytesWritten, err := operation.downloadSelection(context.Background(), selections[0], root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytesWritten != int64(len("INITfixture")) {
		t.Fatalf("path=%q bytes=%d", path, bytesWritten)
	}
}

func TestYouTubeSABRRejectsExternalDownloader(t *testing.T) {
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{External: &ExternalDownloader{Executable: "curl"}},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	_, _, err = operation.downloadSelection(context.Background(), sabrSelection(t), root, filepath.Join(root, "out.mp4"), nil)
	if err == nil || !strings.Contains(err.Error(), "external downloaders") {
		t.Fatalf("err=%v", err)
	}
	if got := categorized("download SABR", err); !IsCategory(got, ErrorUnsupported) {
		t.Fatalf("category=%v", got)
	}
}

func TestYouTubeSABRTrackDestinationDeterministic(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "final.mp4")
	video := sabrSelection(t)
	video.YouTubeSABRTrack = "video"
	video.YouTubeSABRItag = 137
	video.Ext = "mp4"
	audio := sabrSelection(t)
	audio.YouTubeSABRTrack = "audio"
	audio.YouTubeSABRItag = 140
	audio.Ext = "m4a"
	if got := sabrTrackDestination(destination, video); got != destination+".sabr.video.137.mp4" {
		t.Fatalf("video=%q", got)
	}
	if got := sabrTrackDestination(destination, audio); got != destination+".sabr.audio.140.m4a" {
		t.Fatalf("audio=%q", got)
	}
}

func TestYouTubeSABRAsymmetricTrackResume(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "final.mp4")
	info := sabrSelectionInfoWithAudio(t)
	selections, err := format.Default(info, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 2 {
		t.Fatalf("selections=%d", len(selections))
	}
	for index := range selections {
		selections[index].YouTubeSABRDurationSec = 10
		selections[index].YouTubeSABRVideoID = "fixture0001"
	}
	audioSel, videoSel := selections[0], selections[1]
	if audioSel.YouTubeSABRTrack != "audio" {
		audioSel, videoSel = videoSel, audioSel
	}
	audioDest := sabrTrackDestination(destination, audioSel)
	videoDest := sabrTrackDestination(destination, videoSel)

	audioBody := sabrFixtureUMP(t, int32(audioSel.YouTubeSABRItag), []byte("AINI"), []byte("audio"))
	videoRoundOne := buildPartialSABRUMP(t, int32(videoSel.YouTubeSABRItag), []byte("VINI"), []byte("seg0"), 4000)
	videoRoundTwo := buildPartialSABRUMP(t, int32(videoSel.YouTubeSABRItag), nil, []byte("seg1!!"), 6000)

	var audioGETs, videoGETs atomic.Int32
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			itag := sabrRequestItag(body)
			switch itag {
			case int32(audioSel.YouTubeSABRItag):
				audioGETs.Add(1)
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
					Body:       io.NopCloser(bytes.NewReader(audioBody)),
					Request:    request,
				}, nil
			case int32(videoSel.YouTubeSABRItag):
				n := videoGETs.Add(1)
				payload := videoRoundOne
				if n > 1 {
					payload = videoRoundTwo
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
					Body:       io.NopCloser(bytes.NewReader(payload)),
					Request:    request,
				}, nil
			default:
				return nil, fmt.Errorf("unexpected itag %d", itag)
			}
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
		sabrMerge: func(ctx context.Context, video, audio, dest string, overwrite bool, sink events.Sink) error {
			_ = ctx
			_ = overwrite
			_ = sink
			videoBytes, err := os.ReadFile(video)
			if err != nil {
				return err
			}
			audioBytes, err := os.ReadFile(audio)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, append(append([]byte(nil), videoBytes...), audioBytes...), 0o600)
		},
	}
	if _, err := downloadYouTubeSABRSelection(context.Background(), operation, audioSel, root, audioDest, nil, true, nil); err != nil {
		t.Fatal(err)
	}
	identity, err := sabrResumeIdentity(audioSel)
	if err != nil {
		t.Fatal(err)
	}
	complete, _, err := sabrTrackComplete(audioDest, identity)
	if err != nil || !complete {
		t.Fatalf("audio complete=%v err=%v", complete, err)
	}
	if _, err := os.Stat(youtubeump.CompletionMarkerPath(audioDest)); err != nil {
		t.Fatalf("pair audio completion marker missing: %v", err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	sink := events.SinkFunc(func(ctx context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && strings.Contains(event.Path, "video") && event.Bytes >= int64(len("VINIseg0")) {
			cancel()
		}
		return nil
	})
	_, err = downloadYouTubeSABRSelection(cancelCtx, operation, videoSel, root, videoDest, sink, true, nil)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("expected video cancel, got %v", err)
	}
	partPath, statePath := youtubeump.CheckpointPaths(videoDest)
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("video part missing: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("video checkpoint missing: %v", err)
	}

	beforeAudio := audioGETs.Load()
	beforeVideo := videoGETs.Load()
	path, _, err := operation.downloadYouTubeSABRPair(context.Background(), []format.Selection{videoSel, audioSel}, root, destination, nil)
	if err != nil {
		t.Fatalf("asymmetric resume pair: %v", err)
	}
	if path != destination {
		t.Fatalf("path=%q", path)
	}
	if audioGETs.Load() != beforeAudio {
		t.Fatal("completed audio was re-downloaded")
	}
	if videoGETs.Load() <= beforeVideo {
		t.Fatal("partial video was not resumed")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("merged output missing: %v", err)
	}
	if string(got) != "VINIseg0seg1!!AINIaudio" {
		t.Fatalf("merged payload=%q", got)
	}
	if _, err := os.Stat(audioDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audio sidecar should be cleaned: %v", err)
	}
	if _, err := os.Stat(videoDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("video sidecar should be cleaned: %v", err)
	}
	if _, err := os.Stat(youtubeump.CompletionMarkerPath(audioDest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("audio completion marker remains after publish")
	}
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("video part remains after publish")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("video checkpoint remains after publish")
	}
}

func TestYouTubeSABRMergeRetryPreservesCompletedTracks(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	destination := filepath.Join(root, "final.mp4")
	info := sabrSelectionInfoWithAudio(t)
	selections, err := format.Default(info, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range selections {
		selections[index].YouTubeSABRDurationSec = 10
		selections[index].YouTubeSABRVideoID = "fixture0001"
	}
	video, audio := selections[0], selections[1]
	if audio.VCodec != "" && audio.VCodec != "none" {
		video, audio = audio, video
	}
	videoDest := sabrTrackDestination(destination, video)
	audioDest := sabrTrackDestination(destination, audio)
	writeSABRFixtureTrack(t, ffmpegPath, videoDest, false)
	writeSABRFixtureTrack(t, ffmpegPath, audioDest, true)
	writeSABRCompletionMarker(t, video, videoDest)
	writeSABRCompletionMarker(t, audio, audioDest)

	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatal("completed SABR tracks must not re-download")
			return nil, errors.New("unreachable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}

	// Failed merge: destination path is an existing directory.
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	_, _, err = operation.downloadYouTubeSABRPair(context.Background(), selections, root, destination, nil)
	if err == nil {
		t.Fatal("expected merge failure against directory destination")
	}
	if _, statErr := os.Stat(videoDest); statErr != nil {
		t.Fatalf("video track removed after merge failure: %v", statErr)
	}
	if _, statErr := os.Stat(audioDest); statErr != nil {
		t.Fatalf("audio track removed after merge failure: %v", statErr)
	}
	if _, statErr := os.Stat(youtubeump.CompletionMarkerPath(videoDest)); statErr != nil {
		t.Fatalf("video marker removed after merge failure: %v", statErr)
	}
	if _, statErr := os.Stat(youtubeump.CompletionMarkerPath(audioDest)); statErr != nil {
		t.Fatalf("audio marker removed after merge failure: %v", statErr)
	}
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}

	_, _, err = operation.downloadYouTubeSABRPair(context.Background(), selections, root, destination, nil)
	if err != nil {
		t.Fatalf("successful merge retry: %v", err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("merged destination missing: %v", err)
	}
	if _, err := os.Stat(videoDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("video track should be cleaned after successful merge: %v", err)
	}
	if _, err := os.Stat(audioDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audio track should be cleaned after successful merge: %v", err)
	}
	if _, err := os.Stat(youtubeump.CompletionMarkerPath(videoDest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("video completion marker remains")
	}
}

func TestYouTubeSABRStaleSidecarIdentityRejected(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "final.mp4")
	audio := sabrSelection(t)
	audio.YouTubeSABRTrack = "audio"
	audio.YouTubeSABRItag = 140
	audio.Ext = "m4a"
	audio.VCodec = "none"
	audio.ACodec = "mp4a"
	audio.YouTubeSABRVideoID = "fixture0001"
	audioDest := sabrTrackDestination(destination, audio)
	if err := os.WriteFile(audioDest, []byte("stale-audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Marker for a different video identity.
	stale := audio
	stale.YouTubeSABRVideoID = "other-video"
	writeSABRCompletionMarker(t, stale, audioDest)

	identity, err := sabrResumeIdentity(audio)
	if err != nil {
		t.Fatal(err)
	}
	complete, _, err := sabrTrackComplete(audioDest, identity)
	if err != nil {
		t.Fatal(err)
	}
	if complete {
		t.Fatal("stale sidecar accepted")
	}
}

func TestYouTubeSABRResumeIdentityRequired(t *testing.T) {
	selection := sabrSelection(t)
	selection.YouTubeSABRVideoID = ""
	if _, err := sabrResumeIdentity(selection); err == nil || !errors.Is(err, youtubeump.ErrMissingConfig) {
		t.Fatalf("empty video id: %v", err)
	}
	selection.YouTubeSABRVideoID = strings.Repeat("v", 129)
	if _, err := sabrResumeIdentity(selection); err == nil || !errors.Is(err, youtubeump.ErrMissingConfig) {
		t.Fatalf("oversized video id: %v", err)
	}

	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			t.Fatal("download must not start without video id")
			return nil, errors.New("unreachable")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	empty := sabrSelection(t)
	empty.YouTubeSABRVideoID = ""
	_, _, err = operation.downloadSelection(context.Background(), empty, root, destination, nil)
	if err == nil || !errors.Is(err, youtubeump.ErrMissingConfig) {
		t.Fatalf("empty video id download: %v", err)
	}
	if !IsCategory(categorized("SABR", err), ErrorInvalidInput) {
		t.Fatalf("category=%v", categorized("SABR", err))
	}
	oversized := sabrSelection(t)
	oversized.YouTubeSABRVideoID = strings.Repeat("x", 200)
	_, _, err = operation.downloadSelection(context.Background(), oversized, root, destination, nil)
	if err == nil || !errors.Is(err, youtubeump.ErrMissingConfig) {
		t.Fatalf("oversized video id download: %v", err)
	}
}

func TestYouTubeSABRStandaloneClearsCompletionMarker(t *testing.T) {
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	markerPath := youtubeump.CompletionMarkerPath(destination)
	if err := os.WriteFile(markerPath, []byte(`{"stale":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := operation.downloadSelection(context.Background(), sabrSelection(t), root, destination, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("media missing: %v", err)
	}
	if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success left completion marker")
	}
	partPath, statePath := youtubeump.CheckpointPaths(destination)
	if _, err := os.Stat(partPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success left part file")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("standalone success left checkpoint")
	}
}

func TestYouTubeSABRPairConcurrentCancellationPreservesPeer(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "final.mp4")
	info := sabrSelectionInfoWithAudio(t)
	selections, err := format.Default(info, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for index := range selections {
		selections[index].YouTubeSABRDurationSec = 10
		selections[index].YouTubeSABRVideoID = "fixture0001"
	}
	audioSel, videoSel := selections[0], selections[1]
	if audioSel.YouTubeSABRTrack != "audio" {
		audioSel, videoSel = videoSel, audioSel
	}
	audioDest := sabrTrackDestination(destination, audioSel)
	videoDest := sabrTrackDestination(destination, videoSel)

	audioRoundOne := buildPartialSABRUMP(t, int32(audioSel.YouTubeSABRItag), []byte("AINI"), []byte("a0"), 4000)
	audioResume := buildPartialSABRUMP(t, int32(audioSel.YouTubeSABRItag), nil, []byte("a1!!!!"), 6000)
	videoComplete := sabrFixtureUMP(t, int32(videoSel.YouTubeSABRItag), []byte("VINI"), []byte("video-full"))

	var (
		audioCalls, videoCalls atomic.Int32
		audioCanceled          atomic.Bool
		retrying               atomic.Bool
	)
	audioBlocked := make(chan struct{})
	var audioBlockedOnce atomic.Bool
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(request.Body)
			itag := sabrRequestItag(body)
			if itag == int32(videoSel.YouTubeSABRItag) {
				videoCalls.Add(1)
				if !retrying.Load() {
					select {
					case <-audioBlocked:
					case <-request.Context().Done():
						return nil, request.Context().Err()
					case <-time.After(3 * time.Second):
						return nil, errors.New("timed out waiting for audio progress")
					}
					return nil, errors.New("video transport failure")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
					Body:       io.NopCloser(bytes.NewReader(videoComplete)),
					Request:    request,
				}, nil
			}
			if itag != int32(audioSel.YouTubeSABRItag) {
				return nil, fmt.Errorf("unexpected itag %d", itag)
			}
			n := audioCalls.Add(1)
			if !retrying.Load() {
				if n == 1 {
					return &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
						Body:       io.NopCloser(bytes.NewReader(audioRoundOne)),
						Request:    request,
					}, nil
				}
				if audioBlockedOnce.CompareAndSwap(false, true) {
					close(audioBlocked)
				}
				select {
				case <-request.Context().Done():
					audioCanceled.Store(true)
					return nil, request.Context().Err()
				case <-time.After(3 * time.Second):
					return nil, errors.New("audio peer hung without cancellation")
				}
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(audioResume)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{Attempts: 1}, Overwrite: true},
		sabrMerge: func(ctx context.Context, video, audio, dest string, overwrite bool, sink events.Sink) error {
			_ = ctx
			_ = overwrite
			_ = sink
			videoBytes, err := os.ReadFile(video)
			if err != nil {
				return err
			}
			audioBytes, err := os.ReadFile(audio)
			if err != nil {
				return err
			}
			return os.WriteFile(dest, append(append([]byte(nil), videoBytes...), audioBytes...), 0o600)
		},
	}
	errCh := make(chan error, 1)
	go func() {
		_, _, pairErr := operation.downloadYouTubeSABRPair(context.Background(), []format.Selection{videoSel, audioSel}, root, destination, nil)
		errCh <- pairErr
	}()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected pair failure")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pair download hung")
	}
	if !audioCanceled.Load() {
		t.Fatal("in-flight audio peer was not context-cancelled")
	}
	partPath, statePath := youtubeump.CheckpointPaths(audioDest)
	if _, err := os.Stat(partPath); err != nil {
		t.Fatalf("audio part missing after peer failure: %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("audio checkpoint missing after peer failure: %v", err)
	}
	identity, err := sabrResumeIdentity(audioSel)
	if err != nil {
		t.Fatal(err)
	}
	if complete, _, err := sabrTrackComplete(audioDest, identity); err != nil {
		t.Fatal(err)
	} else if complete {
		t.Fatal("audio should remain incomplete after cancelled peer")
	}

	retrying.Store(true)
	beforeAudio := audioCalls.Load()
	beforeVideo := videoCalls.Load()
	path, _, err := operation.downloadYouTubeSABRPair(context.Background(), []format.Selection{videoSel, audioSel}, root, destination, nil)
	if err != nil {
		t.Fatalf("retry after peer cancel: %v", err)
	}
	if path != destination {
		t.Fatalf("path=%q", path)
	}
	if audioCalls.Load() <= beforeAudio {
		t.Fatal("audio did not resume after cancellation")
	}
	if videoCalls.Load() <= beforeVideo {
		t.Fatal("video did not download on retry")
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "VINIvideo-fullAINIa0a1!!!!" {
		t.Fatalf("merged payload=%q", got)
	}
	if _, err := os.Stat(audioDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("audio sidecar remains: %v", err)
	}
	if _, err := os.Stat(videoDest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("video sidecar remains: %v", err)
	}
}

func TestYouTubeSABRCategorizesFailures(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{youtubeump.ErrMissingConfig, ErrorInvalidInput},
		{youtubeump.ErrCheckpointInvalid, ErrorInvalidInput},
		{youtubeump.ErrUnsupportedDirective, ErrorUnsupported},
		{youtubeump.ErrRedirect, ErrorNetwork},
		{youtubeump.ErrInvalidMediaState, ErrorNetwork},
		{youtubeump.ErrTruncatedStream, ErrorNetwork},
		{youtubeump.ErrRoundsExhausted, ErrorNetwork},
		{context.Canceled, ErrorCancelled},
	} {
		if got := categorized("SABR", test.err); !IsCategory(got, test.category) {
			t.Fatalf("categorized(%v)=%v want %s", test.err, got, test.category)
		}
	}
}

func TestYouTubeSABRRequestFailurePreservesCause(t *testing.T) {
	wrapped := fmt.Errorf("%w: redacted: %w", youtubeump.ErrDownloadFailed, context.Canceled)
	if got := categorized("SABR", wrapped); !IsCategory(got, ErrorCancelled) || !errors.Is(got, context.Canceled) {
		t.Fatalf("got=%v", got)
	}
}

func TestYouTubeSABRRejectedProviderDoesNotBlockDownload(t *testing.T) {
	client := newBroadTestClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{
			ProviderName: "fixture",
			Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
				return YouTubePOTResponse{}, errors.New("secret-provider-detail")
			},
		}},
	}))
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client:    client,
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	_, _, err = operation.downloadSelection(context.Background(), sabrSelection(t), root, destination, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRExtractionJSONOmitsPOTToken(t *testing.T) {
	info := sabrSelectionInfo(t)
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "pot_token") || strings.Contains(string(encoded), "Zm9v") {
		t.Fatalf("json=%s", encoded)
	}
}

func TestYouTubeSABRPOTResolvedAtDownloadWithoutMetadataLeak(t *testing.T) {
	const secret = "c2VjcmV0LXRva2Vu"
	client := newBroadTestClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{
			ProviderName: "fixture",
			Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
				return YouTubePOTResponse{Token: secret}, nil
			},
		}},
	}))
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	var lastBody []byte
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			lastBody, _ = io.ReadAll(request.Body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := sabrSelection(t)
	selection.YouTubeSABRVideoID = "fixture0001"
	selection.YouTubeSABRClientName = "WEB"
	operation := &operation{
		client:    client,
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	_, _, err = operation.downloadSelection(context.Background(), selection, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lastBody, []byte("secret-token")) {
		t.Fatalf("PO token not forwarded to request body")
	}
	if strings.Contains(string(lastBody), secret) {
		t.Fatalf("raw token leaked on wire encoding")
	}
	encoded, err := json.Marshal(sabrSelectionInfo(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "secret-token") {
		t.Fatalf("token leaked into metadata json")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func sabrSelection(t *testing.T) format.Selection {
	t.Helper()
	selection, err := format.Best(sabrSelectionInfo(t))
	if err != nil {
		t.Fatal(err)
	}
	selection.YouTubeSABRDurationSec = 10
	selection.YouTubeSABRVideoID = "fixture0001"
	selection.YouTubeSABRClientName = "WEB"
	return selection
}

func sabrSelectionInfo(t *testing.T) value.Info {
	t.Helper()
	return value.NewInfo(value.NewObject(
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("137")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
			value.Field{Key: "_youtube_sabr_track", Value: value.String("video")},
			value.Field{Key: "_youtube_sabr_itag", Value: value.Int(137)},
			value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
			value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
			value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
			value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
			value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
			value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
			value.Field{Key: "_youtube_client", Value: value.String("WEB")},
			value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
		)))},
	))
}

func sabrSelectionInfoWithAudio(t *testing.T) value.Info {
	t.Helper()
	return value.NewInfo(value.NewObject(
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("137")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
				value.Field{Key: "vcodec", Value: value.String("avc1")},
				value.Field{Key: "acodec", Value: value.String("none")},
				value.Field{Key: "height", Value: value.Int(1080)},
				value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
				value.Field{Key: "_youtube_sabr_track", Value: value.String("video")},
				value.Field{Key: "_youtube_sabr_itag", Value: value.Int(137)},
				value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
				value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
				value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
				value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
				value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
				value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
				value.Field{Key: "_youtube_client", Value: value.String("WEB")},
				value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("140")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("mp4a")},
				value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
				value.Field{Key: "_youtube_sabr_track", Value: value.String("audio")},
				value.Field{Key: "_youtube_sabr_itag", Value: value.Int(140)},
				value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
				value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
				value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
				value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
				value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
				value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
				value.Field{Key: "_youtube_client", Value: value.String("WEB")},
				value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
			)),
		)},
	))
}

func sabrFixtureUMP(t *testing.T, itag int32, init, media []byte) []byte {
	t.Helper()
	return bytes.Join([][]byte{
		encodeSabrPart(42, encodeSabrFormatInit(itag)),
		encodeSabrPart(20, encodeSabrMediaHeader(1, itag, true, 0, 0, int64(len(init)))),
		encodeSabrPart(21, append(encodeSabrUMPVarint(1), init...)),
		encodeSabrPart(22, encodeSabrUMPVarint(1)),
		encodeSabrPart(20, encodeSabrMediaHeader(2, itag, false, 0, 10000, int64(len(media)))),
		encodeSabrPart(21, append(encodeSabrUMPVarint(2), media...)),
		encodeSabrPart(22, encodeSabrUMPVarint(2)),
	}, nil)
}

func encodeSabrFormatInit(itag int32) []byte {
	return appendSabrKeyBytes(nil, 2, encodeSabrFieldVarint(nil, 1, uint64(itag)))
}

func encodeSabrMediaHeader(id uint32, itag int32, init bool, sequence uint64, duration, length int64) []byte {
	buf := encodeSabrFieldVarint(nil, 1, uint64(id))
	buf = encodeSabrFieldVarint(buf, 3, uint64(itag))
	if init {
		buf = encodeSabrFieldVarint(buf, 8, 1)
	}
	if sequence != 0 || init {
		buf = encodeSabrFieldVarint(buf, 9, sequence)
	}
	if duration != 0 {
		buf = encodeSabrFieldVarint(buf, 12, uint64(duration))
	}
	if length != 0 {
		buf = encodeSabrFieldVarint(buf, 14, uint64(length))
	}
	return buf
}

func encodeSabrPart(partType int, payload []byte) []byte {
	return append(append(encodeSabrUMPVarint(uint64(partType)), encodeSabrUMPVarint(uint64(len(payload)))...), payload...)
}

func encodeSabrUMPVarint(value uint64) []byte {
	switch {
	case value <= 0x7F:
		return []byte{byte(value)}
	case value <= 0x3FFF:
		return []byte{byte(0x80 | (value & 0x3F)), byte(value >> 6)}
	default:
		return []byte{0xF0, byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)}
	}
}

func encodeSabrFieldVarint(buf []byte, field uint64, value uint64) []byte {
	key := field<<3 | 0
	buf = appendSabrU64(buf, key)
	return appendSabrU64(buf, value)
}

func appendSabrKeyBytes(buf []byte, field uint64, value []byte) []byte {
	key := field<<3 | 2
	buf = appendSabrU64(buf, key)
	buf = appendSabrU64(buf, uint64(len(value)))
	return append(buf, value...)
}

func appendSabrU64(buf []byte, value uint64) []byte {
	for value >= 0x80 {
		buf = append(buf, byte(value)|0x80)
		value >>= 7
	}
	return append(buf, byte(value))
}

func buildPartialSABRUMP(t *testing.T, itag int32, init, media []byte, durationMs int64) []byte {
	t.Helper()
	parts := make([][]byte, 0, 8)
	parts = append(parts, encodeSabrPart(42, encodeSabrFormatInit(itag)))
	headerID := uint32(1)
	if len(init) > 0 {
		parts = append(parts,
			encodeSabrPart(20, encodeSabrMediaHeader(headerID, itag, true, 0, 0, int64(len(init)))),
			encodeSabrPart(21, append(encodeSabrUMPVarint(uint64(headerID)), init...)),
			encodeSabrPart(22, encodeSabrUMPVarint(uint64(headerID))),
		)
		headerID++
	}
	if len(media) > 0 {
		seq := uint64(0)
		if len(init) == 0 {
			seq = 1
		}
		parts = append(parts,
			encodeSabrPart(20, encodeSabrMediaHeader(headerID, itag, false, seq, durationMs, int64(len(media)))),
			encodeSabrPart(21, append(encodeSabrUMPVarint(uint64(headerID)), media...)),
			encodeSabrPart(22, encodeSabrUMPVarint(uint64(headerID))),
		)
	}
	return bytes.Join(parts, nil)
}

func sabrRequestItag(body []byte) int32 {
	reader := body
	for len(reader) > 0 {
		key, n := readSabrTestVarint(reader)
		if n <= 0 {
			return 0
		}
		reader = reader[n:]
		field, wire := key>>3, key&7
		if wire != 2 {
			if wire == 0 {
				_, n = readSabrTestVarint(reader)
				if n <= 0 {
					return 0
				}
				reader = reader[n:]
				continue
			}
			return 0
		}
		size, n := readSabrTestVarint(reader)
		if n <= 0 || int(size) > len(reader[n:]) {
			return 0
		}
		payload := reader[n : n+int(size)]
		reader = reader[n+int(size):]
		switch field {
		case 2, 16, 17: // selected / preferred audio / preferred video FormatID
			if itag := sabrFormatItag(payload); itag != 0 {
				return itag
			}
		}
	}
	return 0
}

func sabrFormatItag(payload []byte) int32 {
	inner := payload
	for len(inner) > 0 {
		ikey, in := readSabrTestVarint(inner)
		if in <= 0 {
			return 0
		}
		inner = inner[in:]
		ifield, iwire := ikey>>3, ikey&7
		if iwire == 0 {
			value, vn := readSabrTestVarint(inner)
			if vn <= 0 {
				return 0
			}
			inner = inner[vn:]
			if ifield == 1 {
				return int32(value)
			}
			continue
		}
		if iwire == 2 {
			isize, sn := readSabrTestVarint(inner)
			if sn <= 0 || int(isize) > len(inner[sn:]) {
				return 0
			}
			inner = inner[sn+int(isize):]
			continue
		}
		return 0
	}
	return 0
}

func readSabrTestVarint(data []byte) (uint64, int) {
	var value uint64
	for index := 0; index < len(data) && index < 10; index++ {
		b := data[index]
		value |= uint64(b&0x7f) << (7 * index)
		if b < 0x80 {
			return value, index + 1
		}
	}
	return 0, 0
}

func writeSABRFixtureTrack(t *testing.T, ffmpegPath, destination string, audioOnly bool) {
	t.Helper()
	args := []string{"-nostdin", "-y", "-f", "lavfi"}
	if audioOnly {
		args = append(args, "-i", "sine=frequency=440:duration=0.2", "-c:a", "aac")
	} else {
		args = append(args, "-i", "color=c=black:s=16x16:d=0.2", "-c:v", "libx264", "-pix_fmt", "yuv420p")
	}
	args = append(args, destination)
	output, err := exec.Command(ffmpegPath, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg fixture: %v: %s", err, output)
	}
}

func writeSABRCompletionMarker(t *testing.T, selection format.Selection, destination string) {
	t.Helper()
	identity, err := sabrResumeIdentity(selection)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := youtubeump.WriteCompletionMarker(destination, identity, info.Size()); err != nil {
		t.Fatal(err)
	}
}
