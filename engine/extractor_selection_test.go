package engine

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/extractor"
	"github.com/tejasa97/ytdlp-go/internal/network"
	"github.com/tejasa97/ytdlp-go/internal/testserver"
	"github.com/tejasa97/ytdlp-go/internal/value"
)

type selectionRootExtractor struct {
	transparent bool
}

func (selectionRootExtractor) Name() string { return "selection_root" }
func (selectionRootExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "selection.invalid" && parsed.Path == "/root"
}
func (root selectionRootExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	entry := extractor.Entry{
		URL: "https://selection.invalid/child", ExtractorKey: "selection_child",
		Transparent: root.transparent, Title: "parent title",
	}
	return extractor.URLResult(entry)
}

type selectionChildExtractor struct {
	calls atomic.Int32
}

func (child *selectionChildExtractor) Name() string { return "selection_child" }
func (*selectionChildExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "selection.invalid" &&
		(parsed.Path == "/child" || strings.HasPrefix(parsed.Path, "/child-"))
}
func (child *selectionChildExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	child.calls.Add(1)
	return extractor.Media(selectionMediaInfo()), nil
}

type selectionPlaylistExtractor struct{}

func (selectionPlaylistExtractor) Name() string { return "selection_playlist" }
func (selectionPlaylistExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "selection.invalid" && parsed.Path == "/playlist"
}
func (selectionPlaylistExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	return extractor.Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("selection-playlist")},
		value.Field{Key: "title", Value: value.String("Selection playlist")},
	)), extractor.StaticEntries(
		extractor.Entry{URL: "https://selection.invalid/child-1", ExtractorKey: "selection_child"},
		extractor.Entry{URL: "https://selection.invalid/child-2", ExtractorKey: "selection_child"},
	))
}

func selectionMediaInfo() value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("selection-child")},
		value.Field{Key: "title", Value: value.String("child title")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("direct")},
			value.Field{Key: "url", Value: value.String("https://media.invalid/selection.mp4")},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)))},
	))
}

func newSelectionOperation(t *testing.T, request Request, registry *extractor.Registry) *operation {
	t.Helper()
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	return &operation{
		client: newBroadTestClient(), request: request, registry: wrapLegacyRegistry(registry),
		compatibility: compatibility, rootExtractor: &root,
	}
}

func TestProductExtractorSelectionControlsRootAndURLResultReentry(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-transparent", true: "transparent"}[transparent], func(t *testing.T) {
			parent := selectionRootExtractor{transparent: transparent}
			child := &selectionChildExtractor{}
			registry := extractor.NewRegistry(parent, child)
			request := Request{SkipDownload: true}
			if err := registry.ConfigureSelection([]string{"selection_root", "selection_child"}); err != nil {
				t.Fatal(err)
			}
			operation := newSelectionOperation(t, request, registry)
			result, err := operation.process(context.Background(), "https://selection.invalid/root", "", nil, make(map[string]bool), 0)
			if err != nil {
				t.Fatal(err)
			}
			if result.Extractor != "selection_child" || child.calls.Load() != 1 {
				t.Fatalf("result=%+v child calls=%d", result, child.calls.Load())
			}
			if transparent && !strings.Contains(string(result.InfoJSON), "parent title") {
				t.Fatalf("transparent result lost parent metadata: %s", result.InfoJSON)
			}
			if !transparent && strings.Contains(string(result.InfoJSON), "parent title") {
				t.Fatalf("non-transparent result retained parent metadata: %s", result.InfoJSON)
			}
		})
	}

	parent := selectionRootExtractor{}
	child := &selectionChildExtractor{}
	registry := extractor.NewRegistry(parent, child)
	if err := registry.ConfigureSelection([]string{"selection_root"}); err != nil {
		t.Fatal(err)
	}
	operation := newSelectionOperation(t, Request{SkipDownload: true}, registry)
	_, err := operation.process(context.Background(), "https://selection.invalid/root", "", nil, make(map[string]bool), 0)
	if !errors.Is(err, extractor.ErrSelectionDisabled) || child.calls.Load() != 0 {
		t.Fatalf("disabled child error=%v child calls=%d", err, child.calls.Load())
	}
}

func TestProductExtractorSelectionReusesPlaylistRegistryPolicy(t *testing.T) {
	// The playlist child URLs use a distinct path family from the URL-result
	// child, but the same registry key and extractor instance.
	playlistChild := &selectionChildExtractor{}
	registry := extractor.NewRegistry(selectionPlaylistExtractor{}, playlistChild)
	if err := registry.ConfigureSelection([]string{"selection_playlist", "selection_child"}); err != nil {
		t.Fatal(err)
	}
	operation := newSelectionOperation(t, Request{SkipDownload: true}, registry)
	result, err := operation.process(context.Background(), "https://selection.invalid/playlist", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || playlistChild.calls.Load() != 2 {
		t.Fatalf("playlist result entries=%d child calls=%d", len(result.Entries), playlistChild.calls.Load())
	}
}

func TestProductExtractorSelectionCancellationAndSecretSafeFailure(t *testing.T) {
	child := &selectionChildExtractor{}
	registry := extractor.NewRegistry(selectionRootExtractor{}, child)
	if err := registry.ConfigureSelection([]string{"selection_root"}); err != nil {
		t.Fatal(err)
	}
	operation := newSelectionOperation(t, Request{SkipDownload: true}, registry)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := operation.process(ctx, "https://selection.invalid/root", "", nil, make(map[string]bool), 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}

	secretURL := "https://user:selection-secret@example.invalid/child"
	_, err := registry.SelectFor(secretURL, "disabled-selection-key")
	if err == nil || strings.Contains(err.Error(), "selection-secret") || strings.Contains(err.Error(), secretURL) {
		t.Fatalf("secret-bearing selection error=%v", err)
	}
}

func TestProductExtractorSelectionPreflightAndConcurrentClients(t *testing.T) {
	var called atomic.Bool
	client := newBroadTestClient()
	client.transportFactory = func(config network.Config) (*network.Client, error) {
		called.Store(true)
		return network.New(config)
	}
	const secretRule = "[selection-secret"
	_, err := client.Run(context.Background(), Request{
		URL:                "https://example.invalid/video",
		ExtractorSelection: ExtractorSelectionOptions{Rules: []string{secretRule}},
	})
	if err == nil || called.Load() || !errors.Is(err, errInvalidRequestOptions) || strings.Contains(err.Error(), "selection-secret") {
		t.Fatalf("preflight error=%v network called=%v", err, called.Load())
	}

	server := testserver.New()
	defer server.Close()
	const workers = 8
	var group sync.WaitGroup
	results := make(chan string, workers)
	errorsCh := make(chan error, workers)
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			request := Request{
				URL: server.URL + "/page", SkipDownload: true,
				ExtractorSelection: ExtractorSelectionOptions{Rules: []string{"fixture"}},
			}
			result, runErr := newBroadTestClient().Run(context.Background(), request)
			if runErr != nil {
				errorsCh <- runErr
				return
			}
			results <- result.Extractor + ":" + string(rune('0'+index))
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if len(results) != workers {
		t.Fatalf("concurrent results=%d, want %d", len(results), workers)
	}
	for result := range results {
		if !strings.HasPrefix(result, "fixture:") {
			t.Fatalf("concurrent result=%q", result)
		}
	}
}

func TestProductExtractorSelectionEmptyRulesUseDefaultGenericRouting(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	result, err := newBroadTestClient().Run(context.Background(), Request{
		URL: server.URL + "/media", SkipDownload: true,
		ExtractorSelection: ExtractorSelectionOptions{Rules: nil},
	})
	if err != nil || result.Extractor != "generic" {
		t.Fatalf("empty rules result=%+v err=%v, want generic default routing", result, err)
	}
}
