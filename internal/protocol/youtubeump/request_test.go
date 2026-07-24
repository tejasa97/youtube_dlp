package youtubeump

import (
	"bytes"
	"errors"
	"testing"
)

func TestPlaybackRequestOmitsPlaybackCookie(t *testing.T) {
	visitor := []byte("visitor-data-must-not-be-cookie")
	body, err := playbackRequest{
		Format:          FormatID{Itag: 137},
		TrackKind:       "video",
		UstreamerConfig: []byte("ustreamer"),
		ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
		POToken:         []byte("pot"),
	}.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, visitor) {
		t.Fatalf("visitor data leaked into request body")
	}
	streamer, ok, err := streamerContextBytes(body)
	if err != nil || !ok {
		t.Fatalf("streamer=%v ok=%v err=%v", streamer, ok, err)
	}
	if containsProtobufField(streamer, fStreamerCtxPlaybackCookie) {
		t.Fatal("playback_cookie field must be omitted")
	}
}

func TestPlaybackRequestOmitsVisitorDataEvenWhenConfigured(t *testing.T) {
	_ = Config{VisitorData: "Zm9vYmFyLXZpc2l0b3ItZGF0YQ=="}
	body, err := playbackRequest{
		Format:          FormatID{Itag: 137},
		TrackKind:       "video",
		UstreamerConfig: []byte("ustreamer"),
		ClientInfo:      ClientInfo{ClientName: 1},
	}.marshal()
	if err != nil {
		t.Fatal(err)
	}
	streamer, ok, err := streamerContextBytes(body)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if containsProtobufField(streamer, fStreamerCtxPlaybackCookie) {
		t.Fatal("visitor data must not be encoded as playback_cookie")
	}
	if bytes.Contains(body, []byte("visitor")) {
		t.Fatal("visitor marker leaked into request body")
	}
}

func TestPlaybackRequestMarshalRejectsInvalidBufferedRange(t *testing.T) {
	_, err := playbackRequest{
		Format:          FormatID{Itag: 137},
		TrackKind:       "video",
		UstreamerConfig: []byte("ustreamer"),
		ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
		BufferedRanges: []BufferedRange{{
			FormatID:    FormatID{Itag: 137},
			StartTimeMs: -1,
			DurationMs:  1000,
		}},
	}.marshal()
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
}

func TestPlaybackRequestMarshalAcceptsInternallyGeneratedState(t *testing.T) {
	body, err := playbackRequest{
		Format:          FormatID{Itag: 137},
		TrackKind:       "video",
		UstreamerConfig: []byte("ustreamer"),
		ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
		PlayerTimeMs:    4000,
		SelectedFormat:  true,
		BufferedRanges: []BufferedRange{{
			FormatID:          FormatID{Itag: 137},
			StartTimeMs:       0,
			DurationMs:        4000,
			StartSegmentIndex: 0,
			EndSegmentIndex:   0,
		}},
	}.marshal()
	if err != nil {
		t.Fatal(err)
	}
	playerTime, bufferedCount, selected, err := decodePlaybackRequestBody(body)
	if err != nil || playerTime != 4000 || bufferedCount != 1 || !selected {
		t.Fatalf("playerTime=%d buffered=%d selected=%v err=%v", playerTime, bufferedCount, selected, err)
	}
}

func TestValidateSABRURLPolicy(t *testing.T) {
	for _, test := range []struct {
		url     string
		allowed bool
	}{
		{"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=abc", true},
		{"https://googlevideo.com/videoplayback", true},
		{"https://rr1---sn-fixture.googlevideo.com.evil.example/videoplayback", false},
		{"https://youtube.com/videoplayback", false},
		{"https://www.youtube.com/videoplayback", false},
		{"https://rr1---sn-fixture.googlevideo.com:443/videoplayback", false},
		{"https://googlevideo.com:/videoplayback", false},
		{"https://rr1---sn-fixture.googlevideo.com/videoplayback/%2fextra", false},
		{"https://rr1---sn-fixture.googlevideo.com/videoplayback/%5cextra", false},
		{"http://rr1---sn-fixture.googlevideo.com/videoplayback", false},
		{"https://user@rr1---sn-fixture.googlevideo.com/videoplayback", false},
		{"https://rr1---sn-fixture.googlevideo.com/videoplayback#frag", false},
		{"https://sabr.example/stream", false},
	} {
		_, err := ValidateSABRURL(test.url)
		if test.allowed && err != nil {
			t.Fatalf("url=%q err=%v", test.url, err)
		}
		if !test.allowed && err == nil {
			t.Fatalf("url=%q expected rejection", test.url)
		}
	}
}

func TestRequestURLPreservesSignedQueryBytes(t *testing.T) {
	serverURL := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?expire=1&sig=fixture%2Btoken"
	got, err := requestURL(serverURL, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got != serverURL+"&rn=2" {
		t.Fatalf("got=%q", got)
	}
}
