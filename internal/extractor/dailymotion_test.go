package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
)

type publicExtractorTransport struct {
	pages map[string][]byte
	api   func(context.Context, *http.Request) (int, []byte, error)
}

func (t *publicExtractorTransport) Do(ctx context.Context, r *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if t.api == nil {
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	status, body, err := t.api(ctx, r)
	if err != nil {
		return nil, err
	}
	return publicExtractorResponse(status, body), nil
}
func (t *publicExtractorTransport) ReadPage(ctx context.Context, raw string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	body, ok := t.pages[raw]
	if !ok {
		return nil, nil, errors.New("unexpected public fixture page")
	}
	return append([]byte(nil), body...), make(http.Header), nil
}
func publicExtractorResponse(status int, body []byte) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}
}
func readPublicFixture(t testing.TB, site, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../conformance/extractors/public/" + site + "/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDailymotionPublicMetadata(t *testing.T) {
	fixture := readPublicFixture(t, "dailymotion", "success.json")
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.URL.String() != "https://www.dailymotion.com/player/metadata/video/xfixture" {
			t.Fatalf("endpoint %q", r.URL)
		}
		return http.StatusOK, fixture, nil
	}}
	result, err := NewDailymotion().Extract(context.Background(), Request{URL: "https://dai.ly/xfixture?tracking=discarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsPlaylist() {
		t.Fatal("unexpected playlist")
	}
	if id, _ := result.Info.ID(); id != "xfixture" {
		t.Fatalf("id=%q", id)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
}
func TestDailymotionFailuresAndCancellation(t *testing.T) {
	tests := []struct {
		body   []byte
		status int
		want   error
	}{{[]byte(`{"error":{"code":"DM007"}}`), 200, ErrRegionRestricted}, {[]byte(`{"error":{"title":"private"}}`), 200, ErrAuthentication}, {[]byte(`{"title":`), 200, ErrInvalidMetadata}, {nil, 404, ErrUnavailable}}
	for _, test := range tests {
		transport := &publicExtractorTransport{api: func(context.Context, *http.Request) (int, []byte, error) { return test.status, test.body, nil }}
		_, err := NewDailymotion().Extract(context.Background(), Request{URL: "https://www.dailymotion.com/video/xfixture", Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("error=%v want %v", err, test.want)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewDailymotion().Extract(ctx, Request{URL: "https://www.dailymotion.com/video/xfixture", Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestDailymotionPlaylist(t *testing.T) {
	fixture := readPublicFixture(t, "dailymotion", "playlist.json")
	transport := &publicExtractorTransport{api: func(_ context.Context, r *http.Request) (int, []byte, error) {
		if r.URL.Path != "/player/metadata/playlist/xfixture" {
			t.Fatalf("endpoint %q", r.URL)
		}
		return http.StatusOK, fixture, nil
	}}
	result, err := NewDailymotion().Extract(context.Background(), Request{URL: "https://geo.dailymotion.com/player/a.html?playlist=xfixture", Transport: transport})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("playlist=%v err=%v", result.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 3)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestDailymotionRoutes(t *testing.T) {
	for _, raw := range []string{"https://dai.ly/xfixture", "https://www.dailymotion.com/embed/video/xfixture", "https://geo.dailymotion.com/player/a.html?video=xfixture"} {
		u, _ := url.Parse(raw)
		if !NewDailymotion().Suitable(u) {
			t.Fatalf("not suitable %q", raw)
		}
	}
	u, _ := url.Parse("https://www.dailymotion.com/playlist/xfixture")
	if NewDailymotion().Suitable(u) {
		t.Fatal("playlist endpoint should not be claimed without a player query")
	}
}
func FuzzNormalizeDailymotion(f *testing.F) {
	f.Add(readPublicFixture(f, "dailymotion", "success.json"))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var metadata dailymotionMetadata
		_ = json.Unmarshal(data, &metadata)
		_, _ = normalizeDailymotion(metadata, "xfixture", "https://www.dailymotion.com/video/xfixture")
	})
}
