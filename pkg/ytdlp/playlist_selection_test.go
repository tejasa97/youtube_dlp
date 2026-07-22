package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type selectionFixtureExtractor struct {
	pageFetches *atomic.Int32
}

func (*selectionFixtureExtractor) Name() string { return "selection-fixture" }

func (*selectionFixtureExtractor) Suitable(parsed *url.URL) bool { return parsed.Path == "/selection" }

func (fixture *selectionFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	base := parsed.Scheme + "://" + parsed.Host
	sequence, err := extractor.OnDemandEntries(2, func(_ context.Context, page int) ([]extractor.Entry, error) {
		fixture.pageFetches.Add(1)
		if page > 2 {
			return nil, nil
		}
		first, last := page*2+1, page*2+2
		if last > 5 {
			last = 5
		}
		entries := make([]extractor.Entry, 0, last-first+1)
		for index := first; index <= last; index++ {
			entries = append(entries, extractor.Entry{
				URL: base + "/media" + strconv.Itoa(index) + ".mp4", ExtractorKey: "generic",
				ID: "item-" + strconv.Itoa(index), Title: fmt.Sprintf("Item %d", index), Transparent: true,
			})
		}
		return entries, nil
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("selection")},
		value.Field{Key: "title", Value: value.String("Selection Fixture")},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return extractor.Playlist(info, sequence)
}

func TestOperationSlicesPlaylistLazilyAndPreservesSourceIndexes(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{Start: 2, End: 3}, &pages)
	if pages.Load() != 2 {
		t.Fatalf("page fetches = %d; want 2", pages.Load())
	}
	assertSelectedPlaylist(t, result, []string{"item-2", "item-3"}, []float64{2, 3})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media2.mp4", "/media3.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestOperationPlaylistEndStopsBeforeFetchingAnotherPage(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{End: 2}, &pages)
	if pages.Load() != 1 {
		t.Fatalf("page fetches = %d; want 1", pages.Load())
	}
	assertSelectedPlaylist(t, result, []string{"item-1", "item-2"}, []float64{1, 2})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media1.mp4", "/media2.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestOperationReversesSelectedRangeWithoutLosingSourceIndexes(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{Start: 2, End: 4, Reverse: true}, &pages)
	if pages.Load() != 2 {
		t.Fatalf("page fetches = %d; want 2", pages.Load())
	}
	assertSelectedPlaylist(t, result, []string{"item-4", "item-3", "item-2"}, []float64{4, 3, 2})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media4.mp4", "/media3.mp4", "/media2.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestPlaylistRangeValidation(t *testing.T) {
	for _, options := range []PlaylistOptions{
		{Start: -1}, {Start: maxPlaylistEntries + 1}, {End: -1}, {End: maxPlaylistEntries + 1}, {Start: 4, End: 3},
	} {
		if err := validateRequestOptions(Request{Playlist: options}); err == nil {
			t.Errorf("validateRequestOptions(%+v) succeeded", options)
		}
	}
	for _, options := range []PlaylistOptions{{}, {Start: 1}, {End: 3}, {Start: 2, End: 3}, {Reverse: true}} {
		if err := validateRequestOptions(Request{Playlist: options}); err != nil {
			t.Errorf("validateRequestOptions(%+v): %v", options, err)
		}
	}
}

func TestPlaylistSelectionConformanceFixture(t *testing.T) {
	type fixtureCase struct {
		Name            string   `json:"name"`
		Start           int      `json:"start"`
		End             int      `json:"end"`
		Reverse         bool     `json:"reverse"`
		IDs             []string `json:"ids"`
		PlaylistIndexes []int    `json:"playlist_indexes"`
	}
	var fixture struct {
		Cases []fixtureCase `json:"cases"`
	}
	payload, err := os.ReadFile(filepath.Join("..", "..", "conformance", "playlists", "selection.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Name, func(t *testing.T) {
			server, _ := selectionMediaServer(t)
			defer server.Close()
			var pages atomic.Int32
			result := runSelectionFixture(t, server.URL, PlaylistOptions{
				Start: testCase.Start, End: testCase.End, Reverse: testCase.Reverse,
			}, &pages)
			indexes := make([]float64, len(testCase.PlaylistIndexes))
			for index, playlistIndex := range testCase.PlaylistIndexes {
				indexes[index] = float64(playlistIndex)
			}
			assertSelectedPlaylist(t, result, testCase.IDs, indexes)
		})
	}
}

func TestSelectedPlaylistIteratorHonorsCancellationWhileSkipping(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	iterator := newSelectedPlaylistIterator(&cancellingEntryIterator{cancel: cancel}, PlaylistOptions{Start: 2})
	_, ok, err := iterator.Next(ctx)
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() = ok %v, error %v; want context cancellation", ok, err)
	}
}

type cancellingEntryIterator struct {
	cancel context.CancelFunc
	called bool
}

func (iterator *cancellingEntryIterator) Next(ctx context.Context) (extractor.Entry, bool, error) {
	if iterator.called {
		return extractor.Entry{}, false, ctx.Err()
	}
	iterator.called = true
	iterator.cancel()
	return extractor.Entry{URL: "https://example.invalid/first"}, true, nil
}

func FuzzSelectedPlaylistIterator(f *testing.F) {
	f.Add(uint8(1), uint8(0))
	f.Add(uint8(2), uint8(4))
	f.Add(uint8(7), uint8(3))
	f.Fuzz(func(t *testing.T, rawStart, rawEnd uint8) {
		start := int(rawStart%21) + 1
		end := int(rawEnd % 21)
		if end != 0 && end < start {
			end = start
		}
		entries := make([]extractor.Entry, 20)
		for index := range entries {
			entries[index] = extractor.Entry{ID: strconv.Itoa(index + 1)}
		}
		sequence := extractor.StaticEntries(entries...)
		iterator := newSelectedPlaylistIterator(sequence.Iterator(), PlaylistOptions{Start: start, End: end})
		var got []int
		for {
			entry, ok, err := iterator.Next(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			if entry.Entry.ID != strconv.Itoa(entry.SourceIndex) {
				t.Fatalf("entry ID %q does not match source index %d", entry.Entry.ID, entry.SourceIndex)
			}
			got = append(got, entry.SourceIndex)
		}
		last := 20
		if end != 0 && end < last {
			last = end
		}
		var want []int
		for index := start; index <= last; index++ {
			want = append(want, index)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("indexes = %v; want %v (start=%d end=%d)", got, want, start, end)
		}
	})
}

func runSelectionFixture(t *testing.T, base string, options PlaylistOptions, pages *atomic.Int32) Result {
	t.Helper()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true, Playlist: options}, transport: transport,
		registry: extractor.NewRegistry(&selectionFixtureExtractor{pageFetches: pages}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), base+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertSelectedPlaylist(t *testing.T, result Result, wantIDs []string, wantIndexes []float64) {
	t.Helper()
	if len(result.Entries) != len(wantIDs) {
		t.Fatalf("result entries = %d; want %d", len(result.Entries), len(wantIDs))
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	entries, ok := metadata["entries"].([]any)
	if !ok || len(entries) != len(wantIDs) {
		t.Fatalf("metadata entries = %#v", metadata["entries"])
	}
	for index, raw := range entries {
		entry := raw.(map[string]any)
		if entry["id"] != wantIDs[index] || entry["playlist_index"] != wantIndexes[index] {
			t.Errorf("entry %d = %#v; want id=%q playlist_index=%v", index, entry, wantIDs[index], wantIndexes[index])
		}
	}
}

func selectionMediaServer(t *testing.T) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		requests = append(requests, request.URL.Path)
		mu.Unlock()
		writer.Header().Set("Content-Type", "video/mp4")
		_, _ = writer.Write([]byte("fixture"))
	}))
	return server, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), requests...)
	}
}
