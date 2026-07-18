package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
)

func TestMixcloudPublicGraphQLAndLazyCollection(t *testing.T) {
	fixture := readPublicFixture(t, "mixcloud", "success.json")
	calls := 0
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.Method != http.MethodPost || r.URL.String() != mixcloudGraphQLEndpoint {
			t.Fatalf("request=%s %s", r.Method, r.URL)
		}
		calls++
		return 200, fixture, nil
	}}
	result, err := NewMixcloud().Extract(context.Background(), Request{URL: "https://www.mixcloud.com/fixture/fixture-mix/", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats=%d", len(formats))
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	collection := []byte(`{"data":{"userLookup":{"displayName":"Fixture uploads","uploads":{"edges":[{"node":{"id":"one","name":"One","slug":"one","url":"https://www.mixcloud.com/fixture/one/","owner":{"username":"fixture"}}}],"pageInfo":{"hasNextPage":false}}}}}`)
	transport.api = func(context.Context, *http.Request) (int, []byte, error) { return 200, collection, nil }
	playlist, err := NewMixcloud().Extract(context.Background(), Request{URL: "https://www.mixcloud.com/fixture/uploads/", Transport: transport})
	if err != nil || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%v err=%v", playlist.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 2)
	if err != nil || len(entries) != 1 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}
func TestMixcloudErrorsRoutingAndCancel(t *testing.T) {
	for _, test := range []struct {
		status int
		want   error
	}{{403, ErrAuthentication}, {404, ErrUnavailable}, {451, ErrRegionRestricted}} {
		transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return test.status, nil, nil }}
		_, err := NewMixcloud().Extract(context.Background(), Request{URL: "https://www.mixcloud.com/fixture/a-mix/", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status=%d err=%v", test.status, err)
		}
	}
	u, _ := url.Parse("https://www.mixcloud.com/fixture/playlists/list/")
	if !NewMixcloud().Suitable(u) {
		t.Fatal("playlist route")
	}
	u, _ = url.Parse("https://www.mixcloud.com/fixture/invalid/path/")
	if NewMixcloud().Suitable(u) {
		t.Fatal("invalid route")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewMixcloud().Extract(ctx, Request{URL: "https://www.mixcloud.com/fixture/a-mix/", Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
func FuzzMixcloudStreamURL(f *testing.F) {
	f.Add("https://media.invalid/direct.m4a")
	f.Add("not-base64")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > 1<<20 {
			t.Skip()
		}
		_ = mixcloudStreamURL(raw)
	})
}
