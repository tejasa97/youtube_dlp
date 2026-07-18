package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestTwitterSyndicationMetadata(t *testing.T) {
	fixture := readPublicFixture(t, "twitter", "success.json")
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.URL.Host != "cdn.syndication.twimg.com" || r.URL.Query().Get("id") != "1234567890123" {
			t.Fatalf("endpoint=%s", r.URL)
		}
		return 200, fixture, nil
	}}
	result, err := NewTwitter().Extract(context.Background(), Request{URL: "https://x.com/fixture/status/1234567890123?tracking=discarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	if id, _ := result.Info.ID(); id != "media1" {
		t.Fatalf("id=%q", id)
	}
}
func TestTwitterFailureRoutingAndCancel(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{403, ErrAuthentication}, {404, ErrUnavailable}, {451, ErrRegionRestricted}} {
		transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return test.status, nil, nil }}
		_, err := NewTwitter().Extract(context.Background(), Request{URL: "https://twitter.com/fixture/status/1234567890123", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d err=%v", test.status, err)
		}
	}
	u, _ := url.Parse("https://twitter.com/i/web/status/1234567890123")
	if !NewTwitter().Suitable(u) {
		t.Fatal("i/web route")
	}
	u, _ = url.Parse("https://twitter.com/fixture/home")
	if NewTwitter().Suitable(u) {
		t.Fatal("non-status route")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewTwitter().Extract(ctx, Request{URL: "https://twitter.com/fixture/status/1234567890123", Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestTwitterMultiMediaPlaylist(t *testing.T) {
	fixture := []byte(`{"id_str":"1234567890123","full_text":"Fixture post","extended_entities":{"media":[{"id_str":"one","video_info":{"variants":[{"url":"https://media.invalid/one.mp4","content_type":"video/mp4"}]}},{"id_str":"two","video_info":{"variants":[{"url":"https://media.invalid/two.mp4","content_type":"video/mp4"}]}}]}}`)
	transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return 200, fixture, nil }}
	result, err := NewTwitter().Extract(context.Background(), Request{URL: "https://twitter.com/fixture/status/1234567890123", Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("playlist=%v err=%v", result.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}
