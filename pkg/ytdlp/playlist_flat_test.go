package ytdlp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/archive"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestOperationFlatPlaylistRetainsEntriesWithoutChildExtraction(t *testing.T) {
	var fixture struct {
		Items   string `json:"items"`
		Reverse bool   `json:"reverse"`
		Entries []struct {
			ID            string  `json:"id"`
			PlaylistIndex float64 `json:"playlist_index"`
			Type          string  `json:"type"`
			ExtractorKey  string  `json:"extractor_key"`
		} `json:"entries"`
		ChildExtractions int `json:"child_extractions"`
		ChildDownloads   int `json:"child_downloads"`
	}
	payload, err := os.ReadFile(filepath.Join("..", "..", "conformance", "playlists", "flat.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}

	server, requests := selectionMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var pages atomic.Int32
	var extracted []string
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventExtracted {
			extracted = append(extracted, event.Extractor)
		}
		return nil
	}))
	operation := &operation{
		client: client,
		request: Request{Playlist: PlaylistOptions{
			Items: fixture.Items, Reverse: fixture.Reverse, Flat: true,
		}},
		transport: transport,
		registry: extractor.NewRegistry(
			&selectionFixtureExtractor{pageFetches: &pages}, extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), server.URL+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if pages.Load() != 2 {
		t.Fatalf("page fetches = %d; want 2", pages.Load())
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("flat playlist made child media requests: %v", got)
	}
	if len(extracted) != 1+fixture.ChildExtractions || !reflect.DeepEqual(extracted, []string{"selection-fixture"}) {
		t.Fatalf("extracted events = %v; want only the parent", extracted)
	}
	if result.Downloaded || result.Bytes != 0 || len(result.Entries) != len(fixture.Entries) {
		t.Fatalf("flat result = %#v", result)
	}

	for index, child := range result.Entries {
		want := fixture.Entries[index]
		if child.Extractor != want.ExtractorKey || child.Downloaded || child.Bytes != 0 {
			t.Errorf("child %d = %#v", index, child)
		}
		var metadata map[string]any
		if err := json.Unmarshal(child.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["_type"] != want.Type || metadata["id"] != want.ID ||
			metadata["playlist_index"] != want.PlaylistIndex || metadata["playlist_id"] != "selection" ||
			metadata["playlist_title"] != "Selection Fixture" || metadata["ie_key"] != want.ExtractorKey {
			t.Errorf("child %d metadata = %#v", index, metadata)
		}
		if rawURL, _ := metadata["url"].(string); !strings.HasSuffix(rawURL, "/media"+strings.TrimPrefix(want.ID, "item-")+".mp4") {
			t.Errorf("child %d URL = %q", index, rawURL)
		}
	}

	var parent map[string]any
	if err := json.Unmarshal(result.InfoJSON, &parent); err != nil {
		t.Fatal(err)
	}
	entries, ok := parent["entries"].([]any)
	if !ok || len(entries) != len(fixture.Entries) {
		t.Fatalf("parent entries = %#v", parent["entries"])
	}
	for index, raw := range entries {
		entry := raw.(map[string]any)
		if entry["id"] != fixture.Entries[index].ID || entry["playlist_index"] != fixture.Entries[index].PlaylistIndex {
			t.Errorf("parent entry %d = %#v", index, entry)
		}
	}
	if fixture.ChildDownloads != 0 || result.Downloaded {
		t.Fatalf("flat playlist unexpectedly downloaded children")
	}
}

func TestFlatPlaylistEntryValuePreservesURLResultShape(t *testing.T) {
	for _, transparent := range []bool{false, true} {
		entry := extractor.Entry{
			URL: "https://example.invalid/watch/one", ExtractorKey: "fixture",
			ID: "one", Title: "One", Transparent: transparent,
		}
		encoded, err := encodeInfo(flatPlaylistEntryInfo(entry, 3, "parent", "Parent"))
		if err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(encoded, &metadata); err != nil {
			t.Fatal(err)
		}
		wantType := "url"
		if transparent {
			wantType = "url_transparent"
		}
		if metadata["_type"] != wantType || metadata["playlist_index"] != float64(3) {
			t.Fatalf("transparent=%v metadata=%#v", transparent, metadata)
		}
	}
}

func TestOperationFlatPlaylistAppliesMetadataBeforeIncompleteMatchFilter(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{
		Playlist:        PlaylistOptions{Items: "2", Flat: true},
		ReplaceMetadata: []string{"title:Item:Renamed"},
		MatchFilters:    []string{"title=other"},
	}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	var pages atomic.Int32
	var events []Event
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			events = append(events, event)
			return nil
		})),
		request: request, compatibility: compatibility, transport: transport,
		registry: extractor.NewRegistry(
			&selectionFixtureExtractor{pageFetches: &pages}, extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), server.URL+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("flat playlist made child requests: %v", got)
	}
	if len(result.Entries) != 1 || !result.Entries[0].Skipped ||
		!strings.Contains(result.Entries[0].SkipReason, "Renamed 2") {
		t.Fatalf("flat filtered result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.Entries[0].InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "Renamed 2" {
		t.Fatalf("transformed title = %#v", metadata["title"])
	}
	var parent map[string]any
	if err := json.Unmarshal(result.InfoJSON, &parent); err != nil {
		t.Fatal(err)
	}
	parentEntries := parent["entries"].([]any)
	if title := parentEntries[0].(map[string]any)["title"]; title != "Renamed 2" {
		t.Fatalf("parent transformed title = %#v", title)
	}
	foundSkipEvent := false
	for _, event := range events {
		if event.Kind == EventMatchFilterSkipped && strings.Contains(event.Message, "Renamed 2") {
			foundSkipEvent = true
		}
	}
	if !foundSkipEvent {
		t.Fatalf("match-filter event missing: %#v", events)
	}
}

func TestOperationFlatPlaylistChecksArchiveWithoutRecording(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	if err := os.WriteFile(archivePath, []byte("generic item-2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var pages atomic.Int32
	var archiveEvent Event
	operation := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventArchiveMatch {
				archiveEvent = event
			}
			return nil
		})),
		request:   Request{Playlist: PlaylistOptions{Items: "2", Flat: true}},
		transport: transport, archive: store,
		registry: extractor.NewRegistry(
			&selectionFixtureExtractor{pageFetches: &pages}, extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), server.URL+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := requests(); len(got) != 0 {
		t.Fatalf("flat playlist made child requests: %v", got)
	}
	if len(result.Entries) != 1 || !result.Entries[0].Archived || !result.Archived {
		t.Fatalf("flat archived result = %#v", result)
	}
	if archiveEvent.Extractor != "generic" || archiveEvent.Message != "generic item-2" {
		t.Fatalf("archive event = %#v", archiveEvent)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil || string(data) != "generic item-2\n" {
		t.Fatalf("archive changed: %q, %v", data, err)
	}
}

func FuzzFlatPlaylistEntryValue(f *testing.F) {
	f.Add("id", "title", "https://example.invalid/video", true)
	f.Add("", "", "opaque:fixture", false)
	f.Fuzz(func(t *testing.T, id, title, rawURL string, transparent bool) {
		if len(id)+len(title)+len(rawURL) > 4096 {
			t.Skip()
		}
		info := flatPlaylistEntryInfo(extractor.Entry{
			URL: rawURL, ID: id, Title: title, ExtractorKey: "fixture", Transparent: transparent,
		}, 1, "playlist", "Playlist")
		encoded, err := encodeInfo(info)
		if err != nil {
			t.Fatal(err)
		}
		if !json.Valid(encoded) {
			t.Fatalf("invalid JSON: %q", encoded)
		}
	})
}
