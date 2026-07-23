package hls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/fragment"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestDownloadMasterByteRangeMapAndAES128(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("abcdef0123456789")
	encrypted := encryptSegment(t, []byte("secret-"), key, iv)
	blob := []byte("skip-range-tail")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nhigh.m3u8\n")
		case "/low.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nlow.bin\n#EXT-X-ENDLIST\n")
		case "/high.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MAP:URI=init.bin\n#EXTINF:1,\nplain.bin\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%s\n#EXTINF:1,\nsecret.bin\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-BYTERANGE:5@5\n#EXTINF:1,\nblob.bin\n#EXT-X-ENDLIST\n", fmt.Sprintf("%x", iv))
		case "/init.bin":
			_, _ = writer.Write([]byte("init-"))
		case "/plain.bin":
			_, _ = writer.Write([]byte("plain-"))
		case "/key.bin":
			_, _ = writer.Write(key)
		case "/secret.bin":
			_, _ = writer.Write(encrypted)
		case "/blob.bin":
			writer.WriteHeader(http.StatusPartialContent)
			_, _ = writer.Write(blob[5:10])
		case "/low.bin":
			_, _ = writer.Write([]byte("wrong"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "hls.bin")
	result, err := NewDownloader(transport, Config{PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/master.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if got, want := string(contents), "init-plain-secret-range"; got != want {
		t.Fatalf("HLS contents = %q, want %q", got, want)
	}
	if result.Bytes != int64(len(contents)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDownloadLivePollDeduplicatesSegments(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n")
			} else {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n#EXTINF:1,\n11.bin\n#EXT-X-ENDLIST\n")
			}
		case "/10.bin":
			_, _ = writer.Write([]byte("ten-"))
		case "/11.bin":
			_, _ = writer.Write([]byte("eleven"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "ten-eleven" || polls.Load() != 2 {
		t.Fatalf("contents = %q, polls = %d", contents, polls.Load())
	}
}

func TestDownloadLowLatencyPartsAreReplacedByCompletedSegment(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-PART:DURATION=0.5,URI=10.0.bin\n#EXT-X-PART:DURATION=0.5,URI=10.1.bin\n")
			} else {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n#EXT-X-PART:DURATION=0.5,URI=11.0.bin\n#EXT-X-PART:DURATION=0.5,URI=11.1.bin\n#EXT-X-ENDLIST\n")
			}
		case "/10.bin":
			_, _ = writer.Write([]byte("complete-ten-"))
		case "/10.0.bin", "/10.1.bin":
			_, _ = writer.Write([]byte("duplicate"))
		case "/11.0.bin":
			_, _ = writer.Write([]byte("eleven-part-a-"))
		case "/11.1.bin":
			_, _ = writer.Write([]byte("eleven-part-b"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "low-latency.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "complete-ten-eleven-part-a-eleven-part-b" || polls.Load() != 2 {
		t.Fatalf("contents=%q polls=%d error=%v", contents, polls.Load(), err)
	}
}

func TestDownloadLiveHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".m3u8") {
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nseg.bin\n")
		} else {
			_, _ = writer.Write([]byte("segment"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{PollInterval: time.Second}).Download(ctx, server.URL+"/live.m3u8", root, filepath.Join(root, "cancel.bin"), false, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Download() error = %v, context = %v", err, ctx.Err())
	}
}

func TestDownloadPropagatesSelectedFormatHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Referer") != "https://origin.example/watch" || request.Header.Get("X-Media-Token") != "fixture" {
			http.Error(writer, "missing format headers", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nsegment.bin\n#EXT-X-ENDLIST\n")
		case "/segment.bin":
			_, _ = writer.Write([]byte("protected"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "protected.bin")
	_, err := NewDownloader(transport, Config{Headers: http.Header{
		"Referer":       []string{"https://origin.example/watch"},
		"X-Media-Token": []string{"fixture"},
	}}).Download(context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "protected" {
		t.Fatalf("contents = %q, error = %v", contents, err)
	}
}

// Regression derived from yt-dlp aefce1eea: an empty test fragment list must
// remain empty and fail explicitly rather than manufacturing a nil fragment.
func TestDownloadEmptyPlaylistReturnsNoSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-ENDLIST\n")
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/empty.m3u8", root, filepath.Join(root, "empty.bin"), false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("empty playlist error = %v", err)
	}
}

func TestDownloadSuppressesAttributedVODAdvertisements(t *testing.T) {
	fixture, err := os.ReadFile("../../../conformance/media/hls_ads/mixed-vod.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mixed-vod.m3u8":
			_, _ = writer.Write(fixture)
		case "/media-40.bin":
			_, _ = writer.Write([]byte("forty-"))
		case "/media-42.bin":
			_, _ = writer.Write([]byte("forty-two-"))
		case "/media-44.bin":
			_, _ = writer.Write([]byte("forty-four"))
		case "/anvato-ad-41.bin", "/uplynk-ad-43.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "suppressed.bin")
	result, err := NewDownloader(transport, Config{MaxSegments: 3}).Download(
		context.Background(), server.URL+"/mixed-vod.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "forty-forty-two-forty-four" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(contents)), "235f2f70c5d54c69777f1f36a19a0f23929a545457420b651de1958b3bfea86e"; got != want {
		t.Fatalf("output SHA-256=%s want %s", got, want)
	}
	if advertisementHits.Load() != 0 || result.Downloaded != 3 {
		t.Fatalf("ad hits=%d result=%#v", advertisementHits.Load(), result)
	}
}

func TestDownloadLiveAdvertisementReclassificationAndCompleteReplacement(t *testing.T) {
	var polls atomic.Int32
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#ANVATO-SEGMENT-INFO:type=ad
#EXTINF:1,
ad-10.bin
#EXT-X-PART:DURATION=0.5,URI="ad-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-11.1.bin"
`)
			case 2:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:1,
media-10.bin
#UPLYNK-SEGMENT,ad
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.1.bin"
`)
			default:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:1,
media-10.bin
#EXTINF:1,
media-11.bin
#EXT-X-ENDLIST
`)
			}
		case "/media-10.bin":
			_, _ = writer.Write([]byte("ten-"))
		case "/media-11.bin":
			_, _ = writer.Write([]byte("eleven"))
		case "/ad-10.bin", "/ad-11.0.bin", "/ad-11.1.bin", "/ad-new-11.0.bin", "/ad-new-11.1.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live-suppressed.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "ten-eleven" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if polls.Load() != 3 || advertisementHits.Load() != 0 {
		t.Fatalf("polls=%d ad hits=%d", polls.Load(), advertisementHits.Load())
	}
}

func TestDownloadAdvertisementKeysMapsAndPhysicalAESIV(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, aes.BlockSize)
	iv[len(iv)-1] = 6 // Ad is physical sequence 5; retained media is sequence 6.
	encrypted := encryptSegment(t, []byte("media-secret"), key, iv)
	var adResourceHits atomic.Int32
	var mediaKeyHits atomic.Int32
	var mediaMapHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:5
#ANVATO-SEGMENT-INFO:type=ad
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXTINF:1,
ad-5.bin
#ANVATO-SEGMENT-INFO:type=master
#EXT-X-MAP:URI="media-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="media-key.bin"
#EXTINF:1,
media-6.bin
#EXT-X-ENDLIST
`)
		case "/ad-init.bin", "/ad-key.bin", "/ad-5.bin":
			adResourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		case "/media-init.bin":
			mediaMapHits.Add(1)
			_, _ = writer.Write([]byte("init-"))
		case "/media-key.bin":
			mediaKeyHits.Add(1)
			_, _ = writer.Write(key)
		case "/media-6.bin":
			_, _ = writer.Write(encrypted)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "encrypted.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "init-media-secret" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if adResourceHits.Load() != 0 || mediaMapHits.Load() != 1 || mediaKeyHits.Load() != 1 {
		t.Fatalf("ad=%d map=%d key=%d", adResourceHits.Load(), mediaMapHits.Load(), mediaKeyHits.Load())
	}
}

func TestDownloadAllAdvertisementsReturnsNoSegmentsWithoutScratch(t *testing.T) {
	var resourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ads.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#UPLYNK-SEGMENT,ad
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin"
#EXTINF:1,
ad.bin
#EXT-X-ENDLIST
`)
		default:
			resourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "ads.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/ads.m3u8", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("error=%v", err)
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", resourceHits.Load())
	}
	for _, path := range []string{destination, destination + ".part", destination + ".fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scratch path %q exists or returned unexpected error: %v", path, statErr)
		}
	}
}

func encryptSegment(t *testing.T, plaintext, key, iv []byte) []byte {
	t.Helper()
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	input := append(append([]byte(nil), plaintext...), bytesOf(byte(padding), padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len(input))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(output, input)
	return output
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
