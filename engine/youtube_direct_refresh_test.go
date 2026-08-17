package engine

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestYouTubeDirectRefreshSecondClientSucceedsAfterFirstRejected(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(
			youtubeDirectRefreshCandidate{url: original.URL, client: "ANDROID_VR", size: 1000},
			youtubeDirectRefreshCandidate{url: "https://web.googlevideo.com/videoplayback?sig=web", client: "WEB", size: 1000},
			youtubeDirectRefreshCandidate{url: "https://tv.googlevideo.com/videoplayback?sig=tv", client: "TV", size: 1000},
		)}, nil
	}}
	refresh := operation.youtubeDirectRefresh(original)
	if refresh == nil {
		t.Fatal("refresh callback is nil")
	}
	first, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden, Offset: 400, Total: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if first.URL != "https://web.googlevideo.com/videoplayback?sig=web" || first.ExpectedBytes != 1000 {
		t.Fatalf("first refresh = %#v", first)
	}
	second, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden, Offset: 400, Total: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if second.URL != "https://tv.googlevideo.com/videoplayback?sig=tv" {
		t.Fatalf("second refresh = %#v", second)
	}
}

func TestYouTubeDirectRefreshDoesNotRepeatRejectedCombination(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	candidate := youtubeDirectRefreshCandidate{url: "https://web.googlevideo.com/videoplayback?sig=fresh", client: "WEB", size: 1000}
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(
			youtubeDirectRefreshCandidate{url: original.URL, client: original.YouTubeClient, size: original.Filesize},
			candidate,
		)}, nil
	}}
	refresh := operation.youtubeDirectRefresh(original)
	first, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if err != nil || first.URL != candidate.url {
		t.Fatalf("first refresh = %#v, %v", first, err)
	}
	_, err = refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("repeated candidate error = %v", err)
	}
}

func TestYouTubeDirectRefreshExhaustsWhenOnlyRejectedCandidatesRemain(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(youtubeDirectRefreshCandidate{
			url: original.URL, client: original.YouTubeClient, size: original.Filesize,
		})}, nil
	}}
	_, err := operation.youtubeDirectRefresh(original)(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("exhaustion error = %v", err)
	}
}

func TestYouTubeDirectRefreshDoesNotRepeatRejectedURLEvenWithDifferentClient(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(
			youtubeDirectRefreshCandidate{url: original.URL, client: "WEB", size: original.Filesize},
		)}, nil
	}}
	_, err := operation.youtubeDirectRefresh(original)(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("same URL with a different client error = %v", err)
	}
}

func TestYouTubeDirectRefreshFallsBackToNewURLFromAttemptedClient(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	fresh := "https://vr.googlevideo.com/videoplayback?sig=fresh"
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(youtubeDirectRefreshCandidate{
			url: fresh, client: original.YouTubeClient, size: original.Filesize,
		})}, nil
	}}
	result, err := operation.youtubeDirectRefresh(original)(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if err != nil || result.URL != fresh {
		t.Fatalf("fallback refresh = %#v, %v", result, err)
	}
}

func TestYouTubeDirectRefreshRejectsRepresentationMismatch(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(youtubeDirectRefreshCandidate{
			url: "https://fresh.googlevideo.com/videoplayback?sig=fresh", client: "WEB", size: 999,
		})}, nil
	}}
	_, err := operation.youtubeDirectRefresh(original)(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("error = %v", err)
	}
}

func TestYouTubeDirectRefreshHonorsCancellation(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	operation := &operation{youtubeDirectExtract: func(ctx context.Context, _ string) (Extraction, error) {
		<-ctx.Done()
		return Extraction{}, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := operation.youtubeDirectRefresh(original)(ctx, downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func TestAnnotateYouTubeDirectSelectionUsesCanonicalItagForDeduplicatedIDs(t *testing.T) {
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture0001")},
		value.Field{Key: "webpage_url", Value: value.String("https://www.youtube.com/watch?v=fixture0001")},
	))
	for _, id := range []string{"18-0", "18-1"} {
		plans := []mediaformat.OutputPlan{{Tracks: []mediaformat.Selection{{
			ID: id, URL: "https://media.googlevideo.com/videoplayback?sig=" + id, YouTubeItag: 18, YouTubeClient: "WEB",
			Ext: "mp4", Filesize: 1000, VCodec: "avc1.42001E", ACodec: "mp4a.40.2", Width: 640, Height: 360, FPS: 30,
		}}}}
		annotateYouTubeDirectPlans("youtube", info, plans)
		selection := plans[0].Tracks[0]
		if selection.YouTubeItag != 18 || selection.YouTubeSourceURL == "" || selection.YouTubeVideoID != "fixture0001" {
			t.Fatalf("selection %q was not annotated: %#v", id, selection)
		}
		operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
			return Extraction{Info: youtubeDirectCombinedRefreshInfo(id, selection.URL)}, nil
		}}
		refresh := operation.youtubeDirectRefresh(selection)
		if refresh == nil {
			t.Fatalf("selection %q did not install a refresh callback", id)
		}
		result, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
		if err != nil || result.URL != "https://fresh.googlevideo.com/videoplayback?sig=combined" {
			t.Fatalf("combined refresh %q = %#v, %v", id, result, err)
		}
	}
}

func TestAnnotateYouTubeDirectSelectionRejectsAmbiguousOrMalformedIDs(t *testing.T) {
	for _, id := range []string{"18-0", "18-1", "18-x", "18-drc-0", "-18", "+18", ""} {
		selection := mediaformat.Selection{ID: id, URL: "https://media.googlevideo.com/videoplayback"}
		annotateYouTubeDirectSelection(&selection, "https://www.youtube.com/watch?v=fixture0001", "fixture0001")
		if selection.YouTubeSourceURL != "" || selection.YouTubeItag != 0 {
			t.Fatalf("ambiguous ID %q was accepted: %#v", id, selection)
		}
		operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) { return Extraction{}, nil }}
		if operation.youtubeDirectRefresh(selection) != nil {
			t.Fatalf("ambiguous ID %q installed a refresh callback", id)
		}
	}
}

func TestAnnotateYouTubeDirectSelectionParsesKnownItagSuffixes(t *testing.T) {
	for _, id := range []string{"140", "140-drc", "137-sr"} {
		selection := mediaformat.Selection{ID: id, URL: "https://media.googlevideo.com/videoplayback"}
		annotateYouTubeDirectSelection(&selection, "https://www.youtube.com/watch?v=fixture0001", "fixture0001")
		if selection.YouTubeItag <= 0 || selection.YouTubeSourceURL == "" {
			t.Fatalf("known ID %q was not annotated: %#v", id, selection)
		}
	}
}

func TestYouTubeDirectRefreshPreservesIndependentSiblingState(t *testing.T) {
	video := youtubeDirectOriginalSelection()
	audio := mediaformat.Selection{
		ID: "140", URL: "https://old.googlevideo.com/videoplayback?sig=audio", Ext: "m4a",
		Filesize: 500, VCodec: "none", ACodec: "mp4a.40.2",
		YouTubeSourceURL: video.YouTubeSourceURL, YouTubeVideoID: video.YouTubeVideoID,
		YouTubeItag: 140, YouTubeClient: "ANDROID_VR",
	}
	operation := &operation{youtubeDirectExtract: func(_ context.Context, _ string) (Extraction, error) {
		return Extraction{Info: value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("fixture0001")},
			value.Field{Key: "formats", Value: value.List(
				youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{url: video.URL, client: "ANDROID_VR", size: 1000, id: "299", itag: 299, ext: "mp4", vcodec: "avc1.64002a", acodec: "none", width: 1920, height: 1080, fps: 60}),
				youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{url: "https://web.googlevideo.com/videoplayback?sig=video", client: "WEB", size: 1000, id: "299", itag: 299, ext: "mp4", vcodec: "avc1.64002a", acodec: "none", width: 1920, height: 1080, fps: 60}),
				youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{url: audio.URL, client: "ANDROID_VR", size: 500, id: "140", itag: 140, ext: "m4a", vcodec: "none", acodec: "mp4a.40.2"}),
				youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{url: "https://web.googlevideo.com/videoplayback?sig=audio", client: "WEB", size: 500, id: "140", itag: 140, ext: "m4a", vcodec: "none", acodec: "mp4a.40.2"}),
			)},
		))}, nil
	}}
	videoRefresh := operation.youtubeDirectRefresh(video)
	audioRefresh := operation.youtubeDirectRefresh(audio)
	if videoRefresh == nil || audioRefresh == nil {
		t.Fatal("sibling refresh callbacks are nil")
	}
	if _, err := videoRefresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden}); err != nil {
		t.Fatal(err)
	}
	if _, err := videoRefresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden}); !errors.Is(err, errYouTubeDirectRefreshRejected) {
		t.Fatalf("video exhaustion error = %v", err)
	}
	audioResult, err := audioRefresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden})
	if err != nil || audioResult.URL != "https://web.googlevideo.com/videoplayback?sig=audio" {
		t.Fatalf("audio sibling refresh = %#v, %v", audioResult, err)
	}

	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	first := []mediaformat.Selection{video, audio}
	workspace, err := prepareNTrackWorkspace(root, destination, first, false)
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(workspace, "completed-sibling")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	refreshedVideo := video
	refreshedVideo.URL = "https://web.googlevideo.com/videoplayback?sig=video"
	refreshedAudio := audio
	refreshedAudio.URL = "https://web.googlevideo.com/videoplayback?sig=audio"
	reused, err := prepareNTrackWorkspace(root, destination, []mediaformat.Selection{refreshedVideo, refreshedAudio}, false)
	if err != nil {
		t.Fatal(err)
	}
	if reused != workspace {
		t.Fatalf("workspace = %q, want %q", reused, workspace)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("completed sibling workspace was not preserved: %v", err)
	}
}

func youtubeDirectOriginalSelection() mediaformat.Selection {
	return mediaformat.Selection{
		ID: "299", URL: "https://old.googlevideo.com/videoplayback?sig=old", Ext: "mp4",
		Filesize: 1000, VCodec: "avc1.64002a", ACodec: "none", Width: 1920, Height: 1080, FPS: 60,
		YouTubeSourceURL: "https://www.youtube.com/watch?v=fixture0001", YouTubeVideoID: "fixture0001",
		YouTubeItag: 299, YouTubeClient: "ANDROID_VR",
	}
}

type youtubeDirectRefreshCandidate struct {
	url    string
	client string
	size   int64
	id     string
	itag   int64
	ext    string
	vcodec string
	acodec string
	width  int64
	height int64
	fps    int64
}

func youtubeDirectRefreshInfo(candidates ...youtubeDirectRefreshCandidate) value.Info {
	formats := make([]value.Value, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.id == "" {
			candidate.id = "299"
			candidate.itag = 299
			candidate.ext = "mp4"
			candidate.vcodec = "avc1.64002a"
			candidate.acodec = "none"
			candidate.width = 1920
			candidate.height = 1080
			candidate.fps = 60
		}
		formats = append(formats, youtubeDirectRefreshFormat(candidate))
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture0001")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))
}

func youtubeDirectCombinedRefreshInfo(id, originalURL string) value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture0001")},
		value.Field{Key: "formats", Value: value.List(
			youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{
				url: originalURL, client: "WEB", size: 1000, id: id, itag: 18, ext: "mp4",
				vcodec: "avc1.42001E", acodec: "mp4a.40.2", width: 640, height: 360, fps: 30,
			}),
			youtubeDirectRefreshFormat(youtubeDirectRefreshCandidate{
				url: "https://fresh.googlevideo.com/videoplayback?sig=combined", client: "TV", size: 1000, id: id, itag: 18, ext: "mp4",
				vcodec: "avc1.42001E", acodec: "mp4a.40.2", width: 640, height: 360, fps: 30,
			}),
		)},
	))
}

func youtubeDirectRefreshFormat(candidate youtubeDirectRefreshCandidate) value.Value {
	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(candidate.id)},
		value.Field{Key: "url", Value: value.String(candidate.url)},
		value.Field{Key: "ext", Value: value.String(candidate.ext)},
		value.Field{Key: "filesize", Value: value.Int(candidate.size)},
		value.Field{Key: "vcodec", Value: value.String(candidate.vcodec)},
		value.Field{Key: "acodec", Value: value.String(candidate.acodec)},
		value.Field{Key: "_youtube_itag", Value: value.Int(candidate.itag)},
		value.Field{Key: "_youtube_client", Value: value.String(candidate.client)},
	)
	if candidate.width > 0 {
		format.Set("width", value.Int(candidate.width))
	}
	if candidate.height > 0 {
		format.Set("height", value.Int(candidate.height))
	}
	if candidate.fps > 0 {
		format.Set("fps", value.Int(candidate.fps))
	}
	return value.ObjectValue(format)
}
