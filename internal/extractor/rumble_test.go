package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestRumbleEmbedAndLazyChannel(t *testing.T) {
	fixture := readPublicFixture(t, "rumble", "success.json")
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.URL.Host != "rumble.com" || r.URL.Path != "/embedJS/u3/" || r.URL.Query().Get("v") != "vfixture" {
			t.Fatalf("endpoint=%s", r.URL)
		}
		return 200, fixture, nil
	}, pages: map[string][]byte{"https://rumble.com/c/fixture?page=1": []byte(`<a class="video-item--a" href="/vfixture-public-video.html">video</a>`)}}
	result, err := NewRumble().Extract(context.Background(), Request{URL: "https://rumble.com/embed/vfixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	channel, err := NewRumble().Extract(context.Background(), Request{URL: "https://rumble.com/c/fixture", Transport: transport})
	if err != nil || !channel.IsPlaylist() {
		t.Fatalf("channel=%v %v", channel.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), channel.Entries, 2)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}
func TestRumbleFailuresRoutingAndCancel(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{403, ErrAuthentication}, {404, ErrUnavailable}, {451, ErrRegionRestricted}} {
		transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return test.status, nil, nil }}
		_, err := NewRumble().Extract(context.Background(), Request{URL: "https://rumble.com/embed/vfixture", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d err=%v", test.status, err)
		}
	}
	u, _ := url.Parse("https://rumble.com/vfixture-a-video.html")
	if !NewRumble().Suitable(u) {
		t.Fatal("video route")
	}
	u, _ = url.Parse("https://rumble.com/videos")
	if NewRumble().Suitable(u) {
		t.Fatal("listing claimed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewRumble().Extract(ctx, Request{URL: "https://rumble.com/embed/vfixture", Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
func FuzzRumbleVariants(f *testing.F) {
	f.Add([]byte(`[{"url":"https://media.invalid/a.mp4"}]`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_ = rumbleVariants(data)
	})
}
