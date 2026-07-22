package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

func TestYouTubeExtractsManualAutomaticTranslatedAndProtectedCaptions(t *testing.T) {
	var calls atomic.Int32
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
			ProviderName: "fixture",
			Function: func(_ context.Context, request youtubepot.Request) (youtubepot.Response, error) {
				if request.Context != youtubepot.ContextSubs || request.Client != "WEB" || request.VideoID != "fixture0001" || request.VisitorData != "caption-visitor" {
					t.Fatalf("token request = %#v", request)
				}
				calls.Add(1)
				return youtubepot.Response{Token: "c3Vicw"}, nil
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: readYouTubeFixture(t, "captions-watch.html"),
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, YouTubePOT: director,
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d", calls.Load())
	}
	assertYouTubeCaptionLanguage(t, result.Info.Lookup("subtitles"), "en", "", "manual")
	assertYouTubeCaptionLanguage(t, result.Info.Lookup("automatic_captions"), "es", "", "automatic")
	assertYouTubeCaptionLanguage(t, result.Info.Lookup("automatic_captions"), "es-orig", "", "automatic")
	assertYouTubeCaptionLanguage(t, result.Info.Lookup("automatic_captions"), "fr", "fr", "automatic")
	if automatic, _ := result.Info.Lookup("automatic_captions").Object(); !automatic.Lookup("fr-en").IsMissing() {
		t.Fatalf("manual translations were generated without opt-in: %#v", automatic)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if language, _ := format.Lookup("language").StringValue(); language != "es" {
		t.Fatalf("audio language = %q", language)
	}

	translated, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, YouTubePOT: director, YouTubeTranslatedCaptions: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertYouTubeCaptionLanguage(t, translated.Info.Lookup("automatic_captions"), "fr-en", "fr", "manual")
}

func assertYouTubeCaptionLanguage(t *testing.T, collectionValue value.Value, language, translatedLanguage, keep string) {
	t.Helper()
	collection, ok := collectionValue.Object()
	if !ok {
		t.Fatalf("caption collection is absent")
	}
	entries, ok := collection.Lookup(language).ListValue()
	if !ok || len(entries) != len(youtubeSubtitleFormats) {
		t.Fatalf("caption language %q = %#v", language, collection.Lookup(language))
	}
	for index, item := range entries {
		entry, _ := item.Object()
		extension, _ := entry.Lookup("ext").StringValue()
		rawURL, _ := entry.Lookup("url").StringValue()
		parsed, err := url.Parse(rawURL)
		if err != nil || extension != youtubeSubtitleFormats[index] || parsed.Query().Get("fmt") != extension ||
			parsed.Query().Get("pot") != "c3Vicw" || parsed.Query().Get("potc") != "1" || parsed.Query().Get("c") != "WEB" ||
			parsed.Query().Get("keep") != keep || parsed.Query().Has("xosf") || parsed.Query().Get("tlang") != translatedLanguage {
			t.Fatalf("caption[%d] = %#v parsed=%#v error=%v", index, entry, parsed, err)
		}
	}
}

func TestYouTubeCaptionTokenRequirementAndIsolation(t *testing.T) {
	player := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001&lang=en&exp=xpe")
	withoutToken, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player}, "fixture0001", nil, false)
	if err != nil || withoutToken.subtitles.Len() != 0 {
		t.Fatalf("required token miss = %#v, %v", withoutToken.subtitles, err)
	}

	never, err := youtubepot.New(youtubepot.Config{Policy: youtubepot.FetchNever})
	if err != nil {
		t.Fatal(err)
	}
	withoutToken, err = normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player}, "fixture0001", never, false)
	if err != nil || withoutToken.subtitles.Len() != 0 {
		t.Fatalf("never policy = %#v, %v", withoutToken.subtitles, err)
	}

	var calls atomic.Int32
	director, err := youtubepot.New(youtubepot.Config{Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, youtubepot.Request) (youtubepot.Response, error) {
			calls.Add(1)
			return youtubepot.Response{Token: "c3Vicw"}, nil
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	withToken, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player, player}, "fixture0001", director, false)
	if err != nil || withToken.subtitles.Len() != 1 || calls.Load() != 1 {
		t.Fatalf("protected captions = %#v calls=%d error=%v", withToken.subtitles, calls.Load(), err)
	}

	optional := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001&lang=en")
	optionalResult, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{optional}, "fixture0001", nil, false)
	if err != nil || optionalResult.subtitles.Len() != 1 {
		t.Fatalf("optional captions = %#v, %v", optionalResult.subtitles, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := normalizeYouTubeCaptions(ctx, []youtubePlayerResponse{player}, "fixture0001", director, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestYouTubeCaptionTokensAreIsolatedPerClient(t *testing.T) {
	web := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001&lang=en&exp=xpe&source=web")
	android := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001&lang=en&exp=xpe&source=android")
	android.clientName = "ANDROID"
	android.visitorData = "android-visitor"
	var calls atomic.Int32
	director, err := youtubepot.New(youtubepot.Config{Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
		ProviderName: "fixture",
		Function: func(_ context.Context, request youtubepot.Request) (youtubepot.Response, error) {
			calls.Add(1)
			if request.Client == "WEB" {
				return youtubepot.Response{Token: "d2Vi"}, nil
			}
			if request.Client == "ANDROID" && request.VisitorData == "android-visitor" {
				return youtubepot.Response{Token: "YW5kcm9pZA"}, nil
			}
			return youtubepot.Response{}, youtubepot.ErrRejected
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{web, android}, "fixture0001", director, false)
	if err != nil || calls.Load() != 2 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
	entries, _ := result.subtitles.Lookup("en").ListValue()
	if len(entries) != 2*len(youtubeSubtitleFormats) {
		t.Fatalf("entries = %d", len(entries))
	}
	clients := make(map[string]string)
	for _, item := range entries {
		entry, _ := item.Object()
		rawURL, _ := entry.Lookup("url").StringValue()
		parsed, _ := url.Parse(rawURL)
		clients[parsed.Query().Get("c")] = parsed.Query().Get("pot")
	}
	if clients["WEB"] != "d2Vi" || clients["ANDROID"] != "YW5kcm9pZA" {
		t.Fatalf("client tokens = %#v", clients)
	}
}

func TestYouTubeCaptionValidationBoundsAndHostRules(t *testing.T) {
	for _, rawURL := range []string{
		"http://www.youtube.com/api/timedtext?v=fixture0001",
		"https://attacker.example/api/timedtext?v=fixture0001",
		"https://user@www.youtube.com/api/timedtext?v=fixture0001",
		"https://www.youtube.com:444/api/timedtext?v=fixture0001",
		"https://www.youtube.com/api/private?v=fixture0001",
		"https://www.youtube.com/api/timedtext?v=fixture0001#token",
	} {
		player := youtubeCaptionPlayerFixture(rawURL)
		result, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player}, "fixture0001", nil, false)
		if err != nil || result.subtitles.Len() != 0 {
			t.Fatalf("hostile URL %q = %#v, %v", rawURL, result.subtitles, err)
		}
	}

	tooMany := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001")
	tooMany.Captions.Tracklist.CaptionTracks = make([]youtubeCaptionTrack, youtubeMaxCaptionTracks+1)
	if _, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{tooMany}, "fixture0001", nil, false); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("track limit = %v", err)
	}
	longText := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001")
	longText.Captions.Tracklist.CaptionTracks[0].Name.SimpleText = strings.Repeat("x", youtubeMaxCaptionTextBytes+1)
	if _, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{longText}, "fixture0001", nil, false); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("text limit = %v", err)
	}
	base, err := parseYouTubeCaptionURL("https://www.youtube.com/api/timedtext?v=fixture0001")
	if err != nil {
		t.Fatal(err)
	}
	used := youtubeMaxCaptionOutputTotalBytes
	if err := addYouTubeCaptionFormats(value.NewObject(), make(map[string]bool), "en", "English", youtubeCaptionCandidate{base: base, clientName: "WEB"}, youtubeCaptionTokenState{}, "", &used); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("output limit = %v", err)
	}
}

func TestYouTubeAndroidPlayerTokenMakesGVSTokenOptional(t *testing.T) {
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
			ProviderName: "fixture",
			Function: func(_ context.Context, request youtubepot.Request) (youtubepot.Response, error) {
				if request.Context == youtubepot.ContextPlayer {
					return youtubepot.Response{Token: "cGxheWVy"}, nil
				}
				return youtubepot.Response{}, youtubepot.ErrRejected
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html")}},
		responses: map[string][]byte{
			"3": readYouTubeFixture(t, "android-player.json"), "28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport, YouTubePOT: director})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats = %d", len(formats))
	}
}

func TestYouTubeTokenProviderCancellationStopsRecovery(t *testing.T) {
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
			ProviderName: "fixture",
			Function: func(_ context.Context, request youtubepot.Request) (youtubepot.Response, error) {
				if request.Context == youtubepot.ContextPlayer {
					return youtubepot.Response{Token: "cGxheWVy"}, nil
				}
				return youtubepot.Response{}, context.Canceled
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html")}},
		responses: map[string][]byte{
			"3": readYouTubeFixture(t, "android-player.json"), "28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	_, err = NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport, YouTubePOT: director})
	if !errors.Is(err, context.Canceled) || len(transport.requests) != 1 {
		t.Fatalf("error=%v requests=%d", err, len(transport.requests))
	}
}

func TestYouTubeGVSRequiredFormatFilteringPreservesItag18AndHLS(t *testing.T) {
	player := youtubePlayerResponse{}
	player.StreamingData.Formats = []youtubeFormat{
		{Itag: 18, URL: "https://media.example/combined.mp4"},
		{Itag: 137, URL: "https://media.example/video.mp4"},
	}
	player.StreamingData.AdaptiveFormats = []youtubeFormat{{Itag: 140, URL: "https://media.example/audio.m4a"}}
	player.StreamingData.HLSManifestURL = "https://media.example/live.m3u8"
	player.StreamingData.DASHManifestURL = "https://media.example/manifest.mpd"
	if !youtubePlayerHasGVSRequiredFormats(player) {
		t.Fatal("required GVS formats were not detected")
	}
	dropYouTubeGVSRequiredFormats(&player)
	if len(player.StreamingData.Formats) != 1 || player.StreamingData.Formats[0].Itag != 18 ||
		len(player.StreamingData.AdaptiveFormats) != 0 || player.StreamingData.HLSManifestURL == "" || player.StreamingData.DASHManifestURL != "" {
		t.Fatalf("filtered player = %#v", player.StreamingData)
	}
}

func youtubeCaptionPlayerFixture(rawURL string) youtubePlayerResponse {
	player := youtubePlayerResponse{clientName: "WEB", visitorData: "visitor", playerURL: youtubePlayerURL}
	track := youtubeCaptionTrack{BaseURL: rawURL, VssID: ".en", LanguageCode: "en"}
	track.Name.SimpleText = "English"
	player.Captions.Tracklist.CaptionTracks = []youtubeCaptionTrack{track}
	return player
}

func FuzzParseYouTubeCaptionURL(f *testing.F) {
	f.Add("https://www.youtube.com/api/timedtext?v=fixture0001&lang=en")
	f.Add("https://attacker.example/api/timedtext?pot=secret")
	f.Add("")
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := parseYouTubeCaptionURL(rawURL)
		if err == nil {
			if parsed.Scheme != "https" || !strings.HasSuffix(strings.ToLower(parsed.Hostname()), "youtube.com") || parsed.Path != "/api/timedtext" || len(parsed.String()) > youtubeMaxCaptionBaseURLBytes {
				t.Fatalf("unsafe caption URL accepted: %q", parsed)
			}
		}
	})
}

func FuzzNormalizeYouTubeCaptionTrack(f *testing.F) {
	f.Add("https://www.youtube.com/api/timedtext?v=fixture0001", ".en", "English", "")
	f.Add("https://attacker.example/api/timedtext?pot=secret", ".a.es", "Spanish", "asr")
	f.Fuzz(func(t *testing.T, rawURL, language, name, kind string) {
		player := youtubeCaptionPlayerFixture(rawURL)
		player.Captions.Tracklist.CaptionTracks[0].VssID = language
		player.Captions.Tracklist.CaptionTracks[0].LanguageCode = language
		player.Captions.Tracklist.CaptionTracks[0].Name.SimpleText = name
		player.Captions.Tracklist.CaptionTracks[0].Kind = kind
		result, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player}, "fixture0001", nil, false)
		if err != nil {
			return
		}
		for _, collection := range []*value.Object{result.subtitles, result.automaticCaptions} {
			for _, field := range collection.Fields() {
				if !validYouTubeCaptionLanguage(field.Key) {
					t.Fatalf("invalid language emitted: %q", field.Key)
				}
				entries, ok := field.Value.ListValue()
				if !ok || len(entries) > len(youtubeSubtitleFormats)*(youtubeMaxTranslationLanguages+2) {
					t.Fatalf("invalid caption entries: %#v", field.Value)
				}
			}
		}
	})
}

func TestYouTubeCaptionErrorsDoNotExposeTokens(t *testing.T) {
	secret := "c2VjcmV0"
	director, err := youtubepot.New(youtubepot.Config{Providers: []youtubepot.Provider{youtubepot.ProviderFunc{
		ProviderName: "fixture",
		Function: func(context.Context, youtubepot.Request) (youtubepot.Response, error) {
			return youtubepot.Response{}, fmt.Errorf("provider leaked %s", secret)
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	player := youtubeCaptionPlayerFixture("https://www.youtube.com/api/timedtext?v=fixture0001&exp=xpe")
	result, err := normalizeYouTubeCaptions(context.Background(), []youtubePlayerResponse{player}, "fixture0001", director, false)
	if err != nil || result.subtitles.Len() != 0 || strings.Contains(fmt.Sprint(err), secret) {
		t.Fatalf("result=%#v error=%v", result.subtitles, err)
	}
}
