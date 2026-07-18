package extractor

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestBilibiliHydrationAndAnthology(t *testing.T) {
	raw := "https://www.bilibili.com/video/BV1fixture01"
	page := readPublicFixture(t, "bilibili", "success.html")
	transport := &publicExtractorTransport{pages: map[string][]byte{raw: page}}
	result, err := NewBilibili().Extract(context.Background(), Request{URL: raw + "?tracking=discarded", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	playlistPage := []byte(`<script>window.__INITIAL_STATE__={"videoData":{"bvid":"BV1fixture01","title":"Fixture","pages":[{"page":1,"part":"one"},{"page":2,"part":"two"}]}};</script>`)
	transport.pages[raw] = playlistPage
	playlist, err := NewBilibili().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err != nil || !playlist.IsPlaylist() {
		t.Fatalf("playlist=%v %v", playlist.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 3)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d %v", len(entries), err)
	}
}
func TestBilibiliFailuresRoutingAndCancel(t *testing.T) {
	raw := "https://www.bilibili.com/video/BV1fixture01"
	for _, test := range []struct {
		page []byte
		want error
	}{{[]byte(`<html>login</html>`), ErrAuthentication}, {[]byte(`<html>geo-restricted</html>`), ErrRegionRestricted}, {[]byte(`<html></html>`), ErrInvalidMetadata}} {
		transport := &publicExtractorTransport{pages: map[string][]byte{raw: test.page}}
		_, err := NewBilibili().Extract(context.Background(), Request{URL: raw, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("error=%v want %v", err, test.want)
		}
	}
	u, _ := url.Parse("https://www.bilibili.com/festival/demo?bvid=BV1fixture01")
	if !NewBilibili().Suitable(u) {
		t.Fatal("festival route")
	}
	u, _ = url.Parse("https://www.bilibili.com/bangumi/play/ep1")
	if NewBilibili().Suitable(u) {
		t.Fatal("bangumi claimed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewBilibili().Extract(ctx, Request{URL: raw, Transport: &publicExtractorTransport{}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
func FuzzParseBilibiliPage(f *testing.F) {
	f.Add(readPublicFixture(f, "bilibili", "success.html"))
	f.Add([]byte(`<script>window.__INITIAL_STATE__={};</script>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseBilibiliPage(data, "BV1fixture01", 0, "https://www.bilibili.com/video/BV1fixture01")
	})
}
