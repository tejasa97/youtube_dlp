package hls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestParseMasterSubtitles(t *testing.T) {
	manifest := []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="English",LANGUAGE="en",URI="subs_en.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="Français",LANGUAGE="fr",URI="subs_fr.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=1000
video.m3u8
`)
	renditions, err := ParseMasterSubtitles("https://cdn.example.test/master.m3u8", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(renditions) != 2 || renditions[0].Language != "en" || renditions[1].Language != "fr" {
		t.Fatalf("renditions = %#v", renditions)
	}
	if renditions[0].URL != "https://cdn.example.test/subs_en.m3u8" || renditions[1].URL != "https://cdn.example.test/subs_fr.m3u8" {
		t.Fatalf("resolved URLs = %#v", renditions)
	}
}

func TestParseMasterSubtitlesIgnoresNonSubtitleMedia(t *testing.T) {
	manifest := []byte(`#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",NAME="Main",URI="audio.m3u8"
#EXT-X-MEDIA:TYPE=CLOSED-CAPTIONS,GROUP-ID="cc",NAME="CC",URI="cc.m3u8"
`)
	renditions, err := ParseMasterSubtitles("https://cdn.example.test/master.m3u8", manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(renditions) != 0 {
		t.Fatalf("renditions = %#v", renditions)
	}
}

func TestParseMasterSubtitlesRejectsOversizedInput(t *testing.T) {
	if _, err := ParseMasterSubtitles("https://cdn.example.test/master.m3u8", make([]byte, maxPlaylistBytes+1)); err == nil {
		t.Fatal("expected oversized rejection")
	}
}

func FuzzParseMasterSubtitles(f *testing.F) {
	f.Add("https://cdn.example.test/master.m3u8", []byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID=\"subs\",NAME=\"English\",LANGUAGE=\"en\",URI=\"subs_en.m3u8\"\n"))
	f.Add("https://cdn.example.test/master.m3u8", []byte("#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,URI=\"audio.m3u8\"\n"))
	f.Fuzz(func(t *testing.T, rawURL string, data []byte) {
		if len(data) > maxPlaylistBytes {
			t.Skip()
		}
		renditions, err := ParseMasterSubtitles(rawURL, data)
		if err != nil {
			return
		}
		if len(renditions) > maxSubtitleRenditions {
			t.Fatalf("rendition overflow: %d", len(renditions))
		}
		for _, rendition := range renditions {
			if rendition.URL == "" {
				t.Fatalf("empty rendition URL: %#v", rendition)
			}
		}
	})
}

type subtitleAssemblyTransport struct {
	payloads map[string][]byte
}

func (transport *subtitleAssemblyTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(key) != "" {
			return nil, fmt.Errorf("credential leakage via %s", key)
		}
	}
	body, ok := transport.payloads[request.URL.String()]
	if !ok {
		return nil, fmt.Errorf("unexpected subtitle request: %s", request.URL)
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    request,
	}, nil
}

func TestAssembleWebVTTConcatenatesSegments(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXTINF:1,\nseg1.vtt\n#EXT-X-ENDLIST\n"),
		"https://cdn.example.test/seg0.vtt":     []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nfirst\n"),
		"https://cdn.example.test/seg1.vtt":     []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nsecond\n"),
	}}
	assembled, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes)
	if err != nil {
		t.Fatal(err)
	}
	text := string(assembled)
	if !strings.Contains(text, "first") || !strings.Contains(text, "second") || strings.Count(text, "WEBVTT") != 1 {
		t.Fatalf("assembled = %q", text)
	}
}

func TestAssembleWebVTTRejectsLivePlaylists(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n"),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("live playlist error = %v", err)
	}
}

func TestAssembleWebVTTRejectsEncryptedSegments(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin\"\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrUnsupportedEncryption) {
		t.Fatalf("encrypted playlist error = %v", err)
	}
}

func TestAssembleWebVTTRejectsInitializationMaps(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("init map error = %v", err)
	}
}

func TestAssembleWebVTTRejectsByteRangeSegments(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:128@0\nseg0.vtt\n#EXT-X-ENDLIST\n"),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("byte range error = %v", err)
	}
}

func TestAssembleWebVTTEnforcesConfiguredMaxBytes(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
		"https://cdn.example.test/seg0.vtt":     []byte("WEBVTT\n\n00:00.000 --> 00:01.000\n" + strings.Repeat("x", 64)),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", 32); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("max bytes error = %v", err)
	}
}

func TestAssembleWebVTTRejectsNilTransport(t *testing.T) {
	if _, err := AssembleWebVTT(context.Background(), nil, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("nil transport error = %v", err)
	}
}

func TestAssembleWebVTTContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
	}}
	if _, err := AssembleWebVTT(ctx, transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v", err)
	}
}

func TestAssembleWebVTTRejectsNoSegments(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXT-X-ENDLIST\n"),
	}}
	if _, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("no segments error = %v", err)
	}
}

func TestAssembleWebVTTUsesConfiguredMaxBytes(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
		"https://cdn.example.test/seg0.vtt":     []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n"),
	}}
	assembled, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(assembled)) > 4096 {
		t.Fatalf("assembled size %d exceeds max 4096", len(assembled))
	}
}

func TestAssembleWebVTTSkipsAdvertisementSegments(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/subs_en.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXTINF:1,\nseg1.vtt\n#EXT-X-ENDLIST\n"),
		"https://cdn.example.test/seg0.vtt":     []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nfirst\n"),
		"https://cdn.example.test/seg1.vtt":     []byte("WEBVTT\n\n00:01.000 --> 00:02.000\nsecond\n"),
	}}
	assembled, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/subs_en.m3u8", maxAssembledSubtitleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(assembled), "first") || !strings.Contains(string(assembled), "second") {
		t.Fatalf("assembled = %q", assembled)
	}
}

func TestAssembleWebVTTResolvesVariantPlaylist(t *testing.T) {
	transport := &subtitleAssemblyTransport{payloads: map[string][]byte{
		"https://cdn.example.test/master.m3u8":  []byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nvariant.m3u8\n"),
		"https://cdn.example.test/variant.m3u8": []byte("#EXTM3U\n#EXTINF:1,\nseg0.vtt\n#EXT-X-ENDLIST\n"),
		"https://cdn.example.test/seg0.vtt":     []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nvariant content\n"),
	}}
	assembled, err := AssembleWebVTT(context.Background(), transport, "https://cdn.example.test/master.m3u8", maxAssembledSubtitleBytes)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(assembled), "variant content") {
		t.Fatalf("assembled = %q", assembled)
	}
}
