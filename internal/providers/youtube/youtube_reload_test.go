package youtube

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestReloadYouTubePlayerPlacesReloadPlaybackContext(t *testing.T) {
	const secret = "reload-token-secret-fixture"
	playerJSON := `{
		"playabilityStatus":{"status":"OK"},
		"videoDetails":{"videoId":"fixture0001","lengthSeconds":"10"},
		"streamingData":{
			"serverAbrStreamingUrl":"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=x",
			"adaptiveFormats":[{
				"itag":137,
				"mimeType":"video/mp4; codecs=\"avc1.640028\"",
				"bitrate":1000,
				"width":1920,"height":1080,
				"lastModified":"1"
			}]
		},
		"playerConfig":{"mediaCommonConfig":{"mediaUstreamerRequestConfig":{"videoPlaybackUstreamerConfig":"Zml4dHVyZS11c3RyZWFtZXI="}}}
	}`
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{}},
		responses:       map[string][]byte{"1": []byte(playerJSON)},
	}
	result, err := ReloadYouTubePlayer(context.Background(), transport, YouTubeReloadRequest{
		VideoID: "fixture0001", ReloadToken: secret, ClientName: "WEB", ClientID: "1",
		ClientVersion: "fixture", UserAgent: "fixture-agent", VisitorData: "visitor",
		DurationSec: 10, WebpageURL: "https://www.youtube.com/watch?v=fixture0001",
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("expected SABR formats from reload")
	}
	if len(transport.bodies) != 1 {
		t.Fatalf("bodies=%d", len(transport.bodies))
	}
	var payload map[string]any
	if err := json.Unmarshal(transport.bodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	playback, _ := payload["playbackContext"].(map[string]any)
	reload, _ := playback["reloadPlaybackContext"].(map[string]any)
	params, _ := reload["reloadPlaybackParams"].(map[string]any)
	token, _ := params["token"].(string)
	if token != secret {
		t.Fatalf("reload token placement missing: %#v", payload["playbackContext"])
	}
	if text := string(transport.bodies[0]); strings.Count(text, secret) != 1 {
		t.Fatalf("unexpected token occurrences")
	}
}

func TestReloadYouTubePlayerRejectsEmptyToken(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{}},
		responses:       map[string][]byte{"1": []byte(`{}`)},
	}
	_, err := ReloadYouTubePlayer(context.Background(), transport, YouTubeReloadRequest{
		VideoID: "fixture0001", ClientName: "WEB", ClientID: "1",
		ClientVersion: "fixture", UserAgent: "ua",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if len(transport.bodies) != 0 {
		t.Fatal("must not issue player request without reload token")
	}
}

func TestReloadYouTubePlayerRejectsInvalidUTF8Token(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe, 's', 'e', 'c', 'r', 'e', 't'})
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{}},
		responses:       map[string][]byte{"1": []byte(`{}`)},
	}
	_, err := ReloadYouTubePlayer(context.Background(), transport, YouTubeReloadRequest{
		VideoID: "fixture0001", ReloadToken: invalid, ClientName: "WEB", ClientID: "1",
		ClientVersion: "fixture", UserAgent: "fixture-agent", VisitorData: "visitor",
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
	if len(transport.bodies) != 0 || len(transport.requests) != 0 {
		t.Fatal("must not issue player request for invalid UTF-8 reload token")
	}
	if strings.Contains(err.Error(), invalid) || strings.Contains(err.Error(), "\xff") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("reload token leaked: %v", err)
	}
}

func TestRequestYouTubePlayerReloadRejectsInvalidUTF8Token(t *testing.T) {
	invalid := string([]byte{0xff, 0xfe, 't', 'o', 'k'})
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{}},
		responses:       map[string][]byte{"1": []byte(`{}`)},
	}
	_, err := requestYouTubePlayerReload(context.Background(), transport, "fixture0001", "visitor", "", youtubeClientProfile{
		Name: "web", ClientName: "WEB", ClientID: "1", ClientVersion: "fixture", UserAgent: "ua",
	}, nil, invalid)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
	if len(transport.bodies) != 0 || len(transport.requests) != 0 {
		t.Fatal("must not issue player request for invalid UTF-8 reload token")
	}
	if strings.Contains(err.Error(), invalid) || strings.Contains(err.Error(), "\xff") {
		t.Fatalf("reload token leaked: %v", err)
	}
}
