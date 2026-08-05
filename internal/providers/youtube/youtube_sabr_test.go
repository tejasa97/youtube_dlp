package youtube

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestYouTubeEmitsSABRFormatsWhenOnlyServerABRPresent(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-only-watch.html"),
		}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("formats=%d", len(formats))
	}
	for _, item := range formats {
		format, _ := item.Object()
		protocol, _ := format.Lookup("protocol").StringValue()
		if protocol != "youtube_sabr_ump" {
			t.Fatalf("protocol=%q", protocol)
		}
		if sabr, _ := format.Lookup("_youtube_sabr").Bool(); !sabr {
			t.Fatal("missing sabr marker")
		}
		if rawURL, _ := format.Lookup("url").StringValue(); rawURL != "" {
			t.Fatal("SABR formats must not expose direct media URLs in metadata")
		}
		clientVersion, _ := format.Lookup("_youtube_sabr_client_version").StringValue()
		userAgent, _ := format.Lookup("_youtube_sabr_user_agent").StringValue()
		clientID, _ := format.Lookup("_youtube_sabr_client_id").Int()
		if clientVersion != "2.fixture" || userAgent != "fixture-agent" || clientID != 1 {
			t.Fatalf("identity clientID=%d version=%q agent=%q", clientID, clientVersion, userAgent)
		}
	}
}

func TestYouTubeSABRRejectsMissingClientIdentity(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"INNERTUBE_CONTEXT_CLIENT_NAME":1,"INNERTUBE_CLIENT_VERSION":"2.fixture","INNERTUBE_CONTEXT":{"client":{"clientName":"WEB","userAgent":"fixture-agent"}}`),
		[]byte(""),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) || !strings.Contains(err.Error(), "transport identity") {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRRejectsMismatchedWEBClientName(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"clientName":"WEB"`),
		[]byte(`"clientName":"ANDROID"`),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRRejectsMismatchedContextClientID(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"INNERTUBE_CONTEXT_CLIENT_NAME":1`),
		[]byte(`"INNERTUBE_CONTEXT_CLIENT_NAME":3`),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRFallsBackToSecondValidCandidate(t *testing.T) {
	bad := youtubePlayerResponse{}
	bad.StreamingData.ServerABRURL = "https://evil.example/videoplayback"
	bad.clientID = 1
	bad.clientVersion = "bad"
	bad.userAgent = "bad"
	bad.StreamingData.AdaptiveFormats = []youtubeFormat{{Itag: 999, MimeType: "video/mp4; codecs=\"avc1\"", LastModified: "1"}}

	good := youtubePlayerResponse{}
	good.StreamingData.ServerABRURL = "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture"
	good.clientID = 3
	good.clientVersion = "21.26.364"
	good.userAgent = "android-agent"
	good.clientName = "ANDROID"
	good.PlayerConfig.MediaCommonConfig.MediaUstreamerRequestConfig.VideoPlaybackUstreamerConfig = "Zml4dHVyZS11c3RyZWFtZXI="
	good.StreamingData.AdaptiveFormats = []youtubeFormat{{Itag: 137, MimeType: "video/mp4; codecs=\"avc1.640028\"", LastModified: "1700000000000"}}

	values, err := buildYouTubeSABRFormats(context.Background(), []youtubePlayerResponse{bad, good}, "https://www.youtube.com/watch?v=fixture0001", "fixture0001", 42, true, "not_live")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("values=%d", len(values))
	}
	object, _ := values[0].Object()
	clientID, _ := object.Lookup("_youtube_sabr_client_id").Int()
	if clientID != 3 {
		t.Fatalf("clientID=%d", clientID)
	}
	itag, _ := object.Lookup("_youtube_sabr_itag").Int()
	if itag != 137 {
		t.Fatalf("itag=%d", itag)
	}
}

func TestYouTubeSABRCandidateDoesNotMergeOtherPlayerFormats(t *testing.T) {
	primary := youtubePlayerResponse{}
	primary.StreamingData.ServerABRURL = "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr?sig=fixture"
	primary.clientID = 1
	primary.clientVersion = "2.fixture"
	primary.userAgent = "fixture-agent"
	primary.PlayerConfig.MediaCommonConfig.MediaUstreamerRequestConfig.VideoPlaybackUstreamerConfig = "Zml4dHVyZS11c3RyZWFtZXI="
	primary.StreamingData.AdaptiveFormats = []youtubeFormat{{Itag: 137, MimeType: "video/mp4; codecs=\"avc1.640028\"", LastModified: "1700000000000"}}

	other := youtubePlayerResponse{}
	other.StreamingData.ServerABRURL = "https://rr2---sn-fixture.googlevideo.com/videoplayback/sabr?sig=other"
	other.clientID = 3
	other.clientVersion = "other"
	other.userAgent = "other-agent"
	other.StreamingData.AdaptiveFormats = []youtubeFormat{{Itag: 140, MimeType: "audio/mp4; codecs=\"mp4a.40.2\"", LastModified: "1700000000000"}}

	values, err := buildYouTubeSABRFormats(context.Background(), []youtubePlayerResponse{primary, other}, "https://www.youtube.com/watch?v=fixture0001", "fixture0001", 42, true, "not_live")
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 1 {
		t.Fatalf("values=%d", len(values))
	}
	object, _ := values[0].Object()
	itag, _ := object.Lookup("_youtube_sabr_itag").Int()
	if itag != 137 {
		t.Fatalf("itag=%d", itag)
	}
	server, _ := object.Lookup("_youtube_sabr_server_url").StringValue()
	if !strings.Contains(server, "rr1---") {
		t.Fatalf("server=%q", server)
	}
}

func TestYouTubeSABRRejectsUnrepresentableLastModified(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"lastModified": "1700000000000"`),
		[]byte(`"lastModified": "9223372036854775808"`),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats=%d", len(formats))
	}
}

func TestYouTubeRejectsSABRWithoutUstreamerConfig(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"videoPlaybackUstreamerConfig": "Zml4dHVyZS11c3RyZWFtZXI="`),
		[]byte(`"videoPlaybackUstreamerConfig": ""`),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubePrefersOrdinaryFormatsOverSABR(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{
			"3": readYouTubeFixture(t, "android-player.json"),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	for _, item := range formats {
		format, _ := item.Object()
		if protocol, _ := format.Lookup("protocol").StringValue(); protocol == "youtube_sabr_ump" {
			t.Fatal("recovered URL-bearing formats must remain preferred over SABR")
		}
	}
}

func TestYouTubeRejectsLiveSABRMetadata(t *testing.T) {
	page := bytes.Replace(
		readYouTubeFixture(t, "sabr-only-watch.html"),
		[]byte(`"shortDescription": "offline SABR-only finite VOD fixture"`),
		[]byte(`"shortDescription": "offline SABR-only finite VOD fixture", "isLive": true`),
		1,
	)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3":  []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
			"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrUnsupported) || !strings.Contains(err.Error(), "live") {
		t.Fatalf("err=%v", err)
	}
}
