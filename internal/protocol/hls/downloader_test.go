package hls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
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
