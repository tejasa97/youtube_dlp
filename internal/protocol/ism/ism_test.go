package ism

import (
	"context"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const fixture = `<SmoothStreamingMedia TimeScale="10" Duration="30"><StreamIndex Type="video" Url="video/QualityLevels({bitrate})/Fragments(video={start time})"><QualityLevel Bitrate="100" FourCC="H264"/><QualityLevel Bitrate="200" FourCC="H264"/><c t="0" d="10" r="2"/></StreamIndex></SmoothStreamingMedia>`

func TestAddressAndDownload(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Manifest" {
			_, _ = w.Write([]byte(fixture))
			return
		}
		_, _ = w.Write([]byte(filepath.Base(r.URL.Path)))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{MaxSegments: 4}).Download(context.Background(), server.URL+"/Manifest", root, filepath.Join(root, "out"), false, nil)
	if err != nil || len(result.Tracks) != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	body, _ := os.ReadFile(filepath.Join(root, "out"))
	if string(body) != "Fragments(video=0)Fragments(video=10)Fragments(video=20)" {
		t.Fatalf("body=%q", body)
	}
}
func TestRejectsUnboundedTimeline(t *testing.T) {
	manifest, err := Parse("https://example.test/Manifest", []byte(`<SmoothStreamingMedia><StreamIndex Type="video" Url="x"><QualityLevel Bitrate="1"/><c d="1" r="-1"/></StreamIndex></SmoothStreamingMedia>`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Address("https://example.test/Manifest", manifest, manifest.Streams[0], 3)
	if err == nil {
		t.Fatal("Address accepted unbounded repeat")
	}
}
func FuzzParse(f *testing.F) {
	f.Add([]byte(fixture))
	f.Fuzz(func(t *testing.T, body []byte) { _, _ = Parse("https://example.test/Manifest", body) })
}
