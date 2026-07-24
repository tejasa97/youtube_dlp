package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestPlaylistItemsConformanceFixture(t *testing.T) {
	var fixture struct {
		Length int `json:"length"`
		Cases  []struct {
			Spec    string `json:"spec"`
			Indexes []int  `json:"indexes"`
		} `json:"cases"`
	}
	payload, err := os.ReadFile(filepath.Join("..", "..", "conformance", "playlists", "items.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Length <= 0 || len(fixture.Cases) == 0 {
		t.Fatal("empty playlist-items conformance fixture")
	}
	for _, testCase := range fixture.Cases {
		t.Run(testCase.Spec, func(t *testing.T) {
			specs, err := parsePlaylistItems(testCase.Spec)
			if err != nil {
				t.Fatal(err)
			}
			indexes, err := expandPlaylistItemSpecs(context.Background(), specs, fixture.Length)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(indexes, testCase.Indexes) {
				t.Fatalf("indexes = %v; want %v", indexes, testCase.Indexes)
			}
		})
	}
}

func TestParsePlaylistItemsRejectsMalformedAndUnboundedInput(t *testing.T) {
	invalid := []string{
		"", ",", "1,,2", "1,", ",1", "abc", "1:2:0", "1:2:3:4", "1 2", "INF",
		"1000000001", "-1000000001", strings.Repeat("1", maxPlaylistItemSpecBytes+1),
		strings.Repeat("1,", maxPlaylistItemSegments) + "1",
	}
	for _, input := range invalid {
		if _, err := parsePlaylistItems(input); !errors.Is(err, errInvalidPlaylistItems) {
			t.Errorf("parsePlaylistItems(%q) error = %v", input, err)
		}
	}
}

func TestOperationPlaylistItemsPreservesRequestedOrderAndStopsPagination(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{Items: "4,2"}, &pages)
	if pages.Load() != 2 {
		t.Fatalf("page fetches = %d; want 2", pages.Load())
	}
	assertSelectedPlaylist(t, result, []string{"item-4", "item-2"}, []float64{4, 2})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media4.mp4", "/media2.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestPlaylistItemsFiniteWideStepDoesNotOverfetch(t *testing.T) {
	specs := mustParsePlaylistItems(t, "1:20000:30000")
	limit, finite, err := finitePlaylistItemCollectionLimit(context.Background(), specs)
	if err != nil || !finite || limit != 1 {
		t.Fatalf("collection limit = %d, finite %v, error %v; want 1, true, nil", limit, finite, err)
	}
	specs = mustParsePlaylistItems(t, "20000")
	limit, finite, err = finitePlaylistItemCollectionLimit(context.Background(), specs)
	if err != nil || !finite || limit != maxPlaylistEntries+1 {
		t.Fatalf("large index limit = %d, finite %v, error %v", limit, finite, err)
	}
}

func TestOperationPlaylistItemsZeroDoesNotFetchEntries(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{Items: "0"}, &pages)
	if pages.Load() != 0 || len(result.Entries) != 0 || len(requests()) != 0 {
		t.Fatalf("pages=%d entries=%d requests=%v", pages.Load(), len(result.Entries), requests())
	}
}

func TestOperationPlaylistItemsNegativeIndexConsumesBoundedSequence(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	result := runSelectionFixture(t, server.URL, PlaylistOptions{Items: "-1"}, &pages)
	if pages.Load() != 3 {
		t.Fatalf("page fetches = %d; want 3", pages.Load())
	}
	assertSelectedPlaylist(t, result, []string{"item-5"}, []float64{5})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media5.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestOperationPlaylistItemsTakePrecedenceAndReverseAfterSelection(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var warning string
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warning = event.Message
		}
		return nil
	}))
	operation := &operation{
		client: client, request: Request{SkipDownload: true, Playlist: PlaylistOptions{
			Start: 4, End: 5, Items: "2,4", Reverse: true,
		}}, transport: transport,
		registry: extractor.NewRegistry(&selectionFixtureExtractor{pageFetches: &pages}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	assertSelectedPlaylist(t, result, []string{"item-4", "item-2"}, []float64{4, 2})
	if warning != "playlist items override playlist start and end" {
		t.Fatalf("warning = %q", warning)
	}
}

func TestPlaylistItemsOverrideRangeWarningPolicy(t *testing.T) {
	for _, options := range []PlaylistOptions{{Items: "2"}, {Items: "2", Start: 1}, {Items: "2", End: -1}} {
		if playlistItemsOverrideRange(options) {
			t.Errorf("unexpected warning for %+v", options)
		}
	}
	for _, options := range []PlaylistOptions{{Items: "2", Start: 2}, {Items: "2", End: 3}} {
		if !playlistItemsOverrideRange(options) {
			t.Errorf("missing warning for %+v", options)
		}
	}
}

func TestPlaylistItemsOverrideRangeWarningEmittedOnce(t *testing.T) {
	count := 0
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventMetadataWarning {
				count++
			}
			return nil
		})),
		request: Request{Playlist: PlaylistOptions{Items: "2", Start: 3}},
	}
	if err := operation.emitPlaylistItemsRangeWarning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := operation.emitPlaylistItemsRangeWarning(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("warning count = %d; want 1", count)
	}
}

func TestPlaylistItemsIteratorCancellationAndLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	iterator := &playlistItemsIterator{
		source: &cancellingEntryIterator{cancel: cancel},
		specs:  mustParsePlaylistItems(t, "-1"),
	}
	if _, ok, err := iterator.Next(ctx); ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() = ok %v, error %v; want cancellation", ok, err)
	}

	entries := make([]extractor.Entry, maxPlaylistEntries+1)
	limited := &playlistItemsIterator{
		source: extractor.StaticEntries(entries...).Iterator(),
		specs:  mustParsePlaylistItems(t, "-1"),
	}
	if _, ok, err := limited.Next(context.Background()); ok || !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("limited Next() = ok %v, error %v; want playlist limit", ok, err)
	}
}

func TestPlaylistItemsValidation(t *testing.T) {
	if err := validateRequestOptions(Request{Playlist: PlaylistOptions{Items: "2,4"}}); err != nil {
		t.Fatal(err)
	}
	err := validateRequestOptions(Request{Playlist: PlaylistOptions{Items: "1::0"}})
	if !errors.Is(err, errInvalidRequestOptions) || !errors.Is(err, errInvalidPlaylistItems) {
		t.Fatalf("validation error = %v", err)
	}
	secret := "invalid-secret-value"
	err = validateRequestOptions(Request{Playlist: PlaylistOptions{Items: secret}})
	if !errors.Is(err, errInvalidPlaylistItems) || strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error leaked malformed input: %v", err)
	}
}

func TestPlaylistItemsIteratorFactoryPreservesValidationCause(t *testing.T) {
	_, err := newPlaylistEntryIterator(extractor.StaticEntries().Iterator(), PlaylistOptions{Items: "1::0"})
	if !errors.Is(err, errInvalidPlaylistItems) {
		t.Fatalf("iterator factory error = %v; want playlist-items cause", err)
	}
}

func FuzzPlaylistItemsParserAndExpansion(f *testing.F) {
	for _, seed := range []string{"2,4", "2-4,3", "::-1", "-15::2", "1:inf:2", "0--2:2"} {
		f.Add(seed, uint8(10))
	}
	f.Fuzz(func(t *testing.T, input string, rawLength uint8) {
		specs, err := parsePlaylistItems(input)
		if err != nil {
			return
		}
		length := int(rawLength % 101)
		indexes, err := expandPlaylistItemSpecs(context.Background(), specs, length)
		if err != nil {
			t.Fatal(err)
		}
		seen := make(map[int]bool, len(indexes))
		for _, index := range indexes {
			if index < 1 || index > length || seen[index] {
				t.Fatalf("invalid indexes %v for length %d", indexes, length)
			}
			seen[index] = true
		}
	})
}

func mustParsePlaylistItems(t *testing.T, input string) []playlistItemSpec {
	t.Helper()
	specs, err := parsePlaylistItems(input)
	if err != nil {
		t.Fatal(err)
	}
	return specs
}
