package engine

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/downloader"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestYouTubeDirectRefreshRotatesDistinctClients(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	var extracts int
	operation := &operation{youtubeDirectExtract: func(_ context.Context, gotSource string) (Extraction, error) {
		extracts++
		if gotSource != original.YouTubeSourceURL {
			t.Fatalf("source = %q", gotSource)
		}
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
	second, err := refresh(context.Background(), downloader.RefreshRequest{StatusCode: http.StatusForbidden, Offset: 400, Total: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if first.URL != "https://web.googlevideo.com/videoplayback?sig=web" || second.URL != "https://tv.googlevideo.com/videoplayback?sig=tv" {
		t.Fatalf("rotation = %q then %q", first.URL, second.URL)
	}
	if first.URL == second.URL || extracts != 2 {
		t.Fatalf("reused candidate or extraction count: first=%q second=%q extracts=%d", first.URL, second.URL, extracts)
	}
}

func TestYouTubeDirectRefreshDoesNotRepeatRejectedCombination(t *testing.T) {
	original := youtubeDirectOriginalSelection()
	candidate := youtubeDirectRefreshCandidate{url: "https://web.googlevideo.com/videoplayback?sig=fresh", client: "WEB", size: 1000}
	operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) {
		return Extraction{Info: youtubeDirectRefreshInfo(candidate)}, nil
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
	for _, id := range []string{"18-0", "18-1"} {
		selection := mediaformat.Selection{
			ID: id, URL: "https://media.googlevideo.com/videoplayback", YouTubeItag: 18,
		}
		annotateYouTubeDirectSelection(&selection, "https://www.youtube.com/watch?v=fixture0001", "fixture0001")
		if selection.YouTubeItag != 18 || selection.YouTubeSourceURL == "" || selection.YouTubeVideoID != "fixture0001" {
			t.Fatalf("selection %q was not annotated: %#v", id, selection)
		}
		operation := &operation{youtubeDirectExtract: func(context.Context, string) (Extraction, error) { return Extraction{}, nil }}
		if operation.youtubeDirectRefresh(selection) == nil {
			t.Fatalf("selection %q did not install a refresh callback", id)
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
}

func youtubeDirectRefreshInfo(candidates ...youtubeDirectRefreshCandidate) value.Info {
	formats := make([]value.Value, 0, len(candidates))
	for _, candidate := range candidates {
		format := value.NewObject(
			value.Field{Key: "format_id", Value: value.String("299")},
			value.Field{Key: "url", Value: value.String(candidate.url)},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "filesize", Value: value.Int(candidate.size)},
			value.Field{Key: "vcodec", Value: value.String("avc1.64002a")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "width", Value: value.Int(1920)},
			value.Field{Key: "height", Value: value.Int(1080)},
			value.Field{Key: "fps", Value: value.Int(60)},
			value.Field{Key: "_youtube_itag", Value: value.Int(299)},
			value.Field{Key: "_youtube_client", Value: value.String(candidate.client)},
		)
		formats = append(formats, value.ObjectValue(format))
	}
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture0001")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	))
}
