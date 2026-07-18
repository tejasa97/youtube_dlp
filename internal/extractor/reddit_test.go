package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestRedditPublicListing(t *testing.T) {
	fixture := readPublicFixture(t, "reddit", "success.json")
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.URL.Host != "www.reddit.com" || r.URL.Path != "/r/videos/comments/abc123/.json" {
			t.Fatalf("endpoint=%s", r.URL)
		}
		if r.URL.Query().Get("raw_json") != "1" {
			t.Fatal("raw_json missing")
		}
		return 200, fixture, nil
	}}
	result, err := NewReddit().Extract(context.Background(), Request{URL: "https://old.reddit.com/r/videos/comments/abc123/a-title/?tracking=discarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats=%d", len(formats))
	}
	if id, _ := result.Info.ID(); id != "post1" {
		t.Fatalf("id=%q", id)
	}
}
func TestRedditCategorizesFailures(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{403, ErrAuthentication}, {404, ErrUnavailable}, {451, ErrRegionRestricted}} {
		transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return test.status, nil, nil }}
		_, err := NewReddit().Extract(context.Background(), Request{URL: "https://www.reddit.com/comments/abc123", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d error=%v", test.status, err)
		}
	}
	transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return 200, []byte(`[`), nil }}
	_, err := NewReddit().Extract(context.Background(), Request{URL: "https://www.reddit.com/comments/abc123", Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}
}

func TestRedditEmbeddedMediaPlaylist(t *testing.T) {
	fixture := []byte(`[{"data":{"children":[{"data":{"id":"post1","title":"Fixture Reddit","media_metadata":{"a":{"s":{"mp4":"https://media.invalid/a.mp4"}},"b":{"s":{"gif":"https://media.invalid/b.mp4"}}}}}]}}]`)
	transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return 200, fixture, nil }}
	result, err := NewReddit().Extract(context.Background(), Request{URL: "https://www.reddit.com/comments/abc123", Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("playlist=%v err=%v", result.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}
func TestRedditRoutingAndCancel(t *testing.T) {
	for _, raw := range []string{"https://www.reddit.com/r/x/comments/abc123/", "https://redditmedia.com/comments/abc123"} {
		u, _ := url.Parse(raw)
		if !NewReddit().Suitable(u) {
			t.Fatalf("not suitable %q", raw)
		}
	}
	u, _ := url.Parse("https://www.reddit.com/r/x/about")
	if NewReddit().Suitable(u) {
		t.Fatal("claimed non-post")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewReddit().Extract(ctx, Request{URL: "https://www.reddit.com/comments/abc123", Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
