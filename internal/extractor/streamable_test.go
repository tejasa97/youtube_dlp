package extractor

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamableExtractsPublicMetadataAndFormats(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "shared", "streamable", "success.json"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "https://ajax.streamable.com/videos/fixture_1"
	transport := &publicExtractorTransport{pages: map[string][]byte{endpoint: fixture}}
	result, err := NewStreamable().Extract(context.Background(), Request{
		URL:       "https://streamable.com/e/fixture_1",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	id, idOK := result.Info.ID()
	title, titleOK := result.Info.Title()
	if !idOK || !titleOK || id != "fixture_1" || title != "Fixture reddit title" {
		t.Fatalf("metadata id=%q title=%q", id, title)
	}
	formats, ok := result.Info.Formats()
	if !ok || len(formats) != 2 {
		t.Fatalf("formats=%d ok=%v", len(formats), ok)
	}
	first, _ := formats[0].Object()
	formatID, _ := first.Get("format_id")
	if id, _ := formatID.StringValue(); id != "mp4" {
		t.Fatalf("first format=%q; want deterministic mp4", id)
	}
}

func TestStreamableUnavailableMalformedRoutingAndCancellation(t *testing.T) {
	endpoint := "https://ajax.streamable.com/videos/pending"
	transport := &publicExtractorTransport{pages: map[string][]byte{endpoint: []byte(`{"status":1,"title":"Pending","files":{}}`)}}
	_, err := NewStreamable().Extract(context.Background(), Request{URL: "https://streamable.com/pending", Transport: transport})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("pending error=%v", err)
	}
	transport.pages[endpoint] = []byte(`{"status":2`)
	_, err = NewStreamable().Extract(context.Background(), Request{URL: "https://streamable.com/pending", Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error=%v", err)
	}
	for _, raw := range []string{"https://streamable.com/a", "https://streamable.com/e/a", "https://streamable.com/s/a/token"} {
		u, _ := url.Parse(raw)
		if !NewStreamable().Suitable(u) {
			t.Fatalf("did not route %s", raw)
		}
	}
	for _, raw := range []string{"https://evil.example/a", "https://streamable.com:443/a", "https://streamable.com/a/b", "ftp://streamable.com/a"} {
		u, _ := url.Parse(raw)
		if NewStreamable().Suitable(u) {
			t.Fatalf("incorrectly routed %s", raw)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewStreamable().Extract(ctx, Request{URL: "https://streamable.com/pending", Transport: transport})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func FuzzStreamableRouting(f *testing.F) {
	for _, seed := range []string{"https://streamable.com/dnd1", "https://streamable.com/e/dnd1", "https://streamable.com/s/okkqk/drxjds", "https://evil.example/dnd1"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		u, err := url.Parse(raw)
		if err != nil {
			return
		}
		_ = NewStreamable().Suitable(u)
	})
}

func FuzzStreamableMetadata(f *testing.F) {
	fixture, err := os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "shared", "streamable", "success.json"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Add([]byte(`{"status":2,"title":"x","files":{"mp4":{"url":"https://media.example/x.mp4"}}}`))
	f.Add([]byte(`{"status":2} {}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		response, err := decodeStreamableResponse(body)
		if err != nil {
			return
		}
		_, _ = normalizeStreamable("fixture", "https://streamable.com/fixture", response)
	})
}
