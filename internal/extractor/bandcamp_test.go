package extractor

import (
	"context"
	"errors"
	"net/url"
	"testing"
)

func TestBandcampPublicTrackAndAlbum(t *testing.T) {
	raw := "https://fixture.bandcamp.com/track/fixture-track"
	page := readPublicFixture(t, "bandcamp", "success.html")
	transport := &publicExtractorTransport{pages: map[string][]byte{raw: page}}
	result, err := NewBandcamp().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats=%d", len(formats))
	}
	albumPage := []byte(`<body data-tralbum="{&quot;artist&quot;:&quot;Fixture&quot;,&quot;current&quot;:{&quot;title&quot;:&quot;Album&quot;},&quot;trackinfo&quot;:[{&quot;track_id&quot;:1,&quot;title&quot;:&quot;One&quot;,&quot;title_link&quot;:&quot;/track/one&quot;},{&quot;track_id&quot;:2,&quot;title&quot;:&quot;Two&quot;,&quot;title_link&quot;:&quot;/track/two&quot;}]}">`)
	albumURL := "https://fixture.bandcamp.com/album/fixture-album"
	transport.pages[albumURL] = albumPage
	playlist, err := NewBandcamp().Extract(context.Background(), Request{URL: albumURL, Transport: transport})
	if err != nil || !playlist.IsPlaylist() {
		t.Fatalf("album=%v %v", playlist.IsPlaylist(), err)
	}
	entries, err := CollectEntries(context.Background(), playlist.Entries, 3)
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries=%d %v", len(entries), err)
	}
}
func TestBandcampFailuresRoutingAndCancel(t *testing.T) {
	raw := "https://fixture.bandcamp.com/track/fixture-track"
	transport := &publicExtractorTransport{pages: map[string][]byte{raw: []byte(`<body data-tralbum="{">`)}}
	_, err := NewBandcamp().Extract(context.Background(), Request{URL: raw, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}
	u, _ := url.Parse("https://fixture.bandcamp.com/album/a")
	if !NewBandcamp().Suitable(u) {
		t.Fatal("album route")
	}
	u, _ = url.Parse("https://bandcamp.com/track/a")
	if NewBandcamp().Suitable(u) {
		t.Fatal("bare host claimed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewBandcamp().Extract(ctx, Request{URL: raw, Transport: transport})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}
