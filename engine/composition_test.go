package engine

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	providerapi "github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/enginetest"
)

func focusedTestComposition(catalogCalls, providerCalls *atomic.Int32) Composition {
	return NewComposition[enginetest.Request](
		func(ClientProviderConfig) []providerapi.Provider[enginetest.Request] {
			catalogCalls.Add(1)
			return []providerapi.Provider[enginetest.Request]{enginetest.Provider{
				ProviderName: "focused", Host: "fixture.example",
				ExtractFunc: func(ctx context.Context, request enginetest.Request) (providerapi.Extraction, error) {
					providerCalls.Add(1)
					return enginetest.Provider{ProviderName: "focused"}.Extract(ctx, request)
				},
			}}
		},
		func(operation providerapi.Operation, request Request) enginetest.Request {
			return enginetest.Request{Request: operation.Request, Label: request.URL}
		},
		ProviderHooks{},
	)
}

func TestExplicitCompositionRunsWithoutBroadFallback(t *testing.T) {
	var catalogs, providers atomic.Int32
	client := NewClient(focusedTestComposition(&catalogs, &providers))
	result, err := client.Run(context.Background(), Request{URL: "https://fixture.example/watch", SkipDownload: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "focused" || catalogs.Load() != 1 || providers.Load() != 1 {
		t.Fatalf("result=%#v catalogs=%d providers=%d", result, catalogs.Load(), providers.Load())
	}
	_, err = client.Run(context.Background(), Request{URL: "https://example.com/media.mp4", SkipDownload: true})
	if !IsCategory(err, ErrorUnsupported) || providers.Load() != 1 {
		t.Fatalf("fallback error=%v providers=%d", err, providers.Load())
	}
}

func TestCompositionBuildsOperationLocalRuntimes(t *testing.T) {
	var catalogs, providers atomic.Int32
	client := NewClient(focusedTestComposition(&catalogs, &providers))
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := client.Run(context.Background(), Request{URL: "https://fixture.example/watch", SkipDownload: true})
			if err != nil {
				errorsSeen <- err
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	if catalogs.Load() != 2 || providers.Load() != 2 {
		t.Fatalf("catalogs=%d providers=%d", catalogs.Load(), providers.Load())
	}
}

func TestBroadTestCompositionPreservesYouTubePositions(t *testing.T) {
	runtime, err := broadTestComposition().newRuntime(Request{}, ClientProviderConfig{})
	if err != nil {
		t.Fatal(err)
	}
	names := runtime.Names()
	want := []string{"youtube_music_search", "youtube_music_browse", "youtube_search", "youtube_hashtag", "youtube_alias_tab", "youtube_handle_tab", "youtube_channel_tab", "youtube"}
	const first = 7
	if got := names[first : first+len(want)]; !reflect.DeepEqual(got, want) {
		t.Fatalf("YouTube positions=%v want=%v", got, want)
	}
}

func TestZeroCompositionFailsClosed(t *testing.T) {
	client := NewClient(Composition{})
	_, err := client.Run(context.Background(), Request{URL: "https://example.com/media.mp4", SkipDownload: true})
	if !IsCategory(err, ErrorInternal) || !errors.Is(err, providerapi.ErrInvalidBundle) {
		t.Fatalf("error=%v", err)
	}
}
