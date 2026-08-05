package engine

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/events"
	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/fragment"
	"github.com/tejasa97/youtube_dlp/internal/media/ffmpeg"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/protocol/hls"
)

func TestHLSDelegatesEligibleSampleAESToFFmpegFallback(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Referer") != "https://page.example/watch" {
			http.Error(writer, "missing referer", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"session-key.bin\"\n#EXT-X-STREAM-INF:BANDWIDTH=10\nmedia.m3u8?token=selected-secret\n")
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n")
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	destination := filepath.Join(root, "output.mp4")
	var fallbackCalls int
	operation := operation{
		transport: transport,
		request:   Request{OutputDir: root},
		hlsFallback: func(
			_ context.Context,
			manifestURL, outputRoot, gotDestination string,
			headers http.Header,
			overwrite bool,
			_ events.Sink,
		) (fragment.Result, error) {
			fallbackCalls++
			if manifestURL != server.URL+"/media.m3u8?token=selected-secret" ||
				outputRoot != root || gotDestination != destination || overwrite ||
				headers.Get("Referer") != "https://page.example/watch" {
				t.Fatalf("fallback URL=%q root=%q destination=%q headers=%v overwrite=%t",
					manifestURL, outputRoot, gotDestination, headers, overwrite)
			}
			if err := os.WriteFile(gotDestination, []byte("decrypted"), 0o600); err != nil {
				return fragment.Result{}, err
			}
			return fragment.Result{Path: gotDestination, Bytes: 9}, nil
		},
	}
	path, size, err := operation.downloadSelection(
		context.Background(),
		mediaformat.Selection{
			URL: server.URL + "/master.m3u8", Protocol: "m3u8_native",
			Headers: http.Header{"Referer": {"https://page.example/watch"}},
		},
		root, destination, nil,
	)
	if err != nil || path != destination || size != 9 || fallbackCalls != 1 {
		t.Fatalf("path=%q size=%d calls=%d error=%v", path, size, fallbackCalls, err)
	}
}

func TestHLSNeverDelegatesDRMOrUnknownEncryption(t *testing.T) {
	for _, key := range []string{
		`METHOD=SAMPLE-AES,URI="skd://asset",KEYFORMAT="com.apple.streamingkeydelivery"`,
		`METHOD=SAMPLE-AES-CTR,URI="https://keys.example/key"`,
		`METHOD=PRIVATE,URI="https://keys.example/key"`,
	} {
		t.Run(strings.SplitN(key, ",", 2)[0], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-KEY:%s\n#EXTINF:1,\nsegment.ts\n", key)
			}))
			defer server.Close()
			transport, err := network.New(network.Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			operation := operation{
				transport: transport,
				request:   Request{OutputDir: t.TempDir()},
				hlsFallback: func(
					context.Context, string, string, string, http.Header, bool, events.Sink,
				) (fragment.Result, error) {
					t.Fatal("unsafe encryption was delegated")
					return fragment.Result{}, nil
				},
			}
			_, _, err = operation.downloadSelection(
				context.Background(),
				mediaformat.Selection{URL: server.URL, Protocol: "m3u8_native"},
				operation.request.OutputDir, filepath.Join(operation.request.OutputDir, "output.mp4"), nil,
			)
			var encryption *hls.EncryptionError
			if !errors.As(err, &encryption) || encryption.FFmpegEligible {
				t.Fatalf("error=%v encryption=%#v", err, encryption)
			}
		})
	}
}

func TestHLSFallbackUnavailableAndCancellationAreCategorized(t *testing.T) {
	if !IsCategory(categorized("download", ffmpeg.ErrUnsafeHLSHeaders), ErrorUnsupported) {
		t.Fatal("unsafe delegated HLS headers were not categorized as unsupported")
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"key.bin\"\n#EXTINF:1,\nsegment.ts\n")
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	selection := mediaformat.Selection{URL: server.URL, Protocol: "m3u8_native"}

	t.Run("unavailable", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		operation := operation{transport: transport, request: Request{OutputDir: root}}
		_, _, err := operation.downloadSelection(
			context.Background(), selection, root, filepath.Join(root, "missing.mp4"), nil,
		)
		if !errors.Is(err, ffmpeg.ErrFFmpegUnavailable) ||
			!errors.Is(err, hls.ErrUnsupportedEncryption) ||
			!IsCategory(categorized("download", err), ErrorUnsupported) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		operation := operation{
			transport: transport, request: Request{OutputDir: root},
			hlsFallback: func(
				context.Context, string, string, string, http.Header, bool, events.Sink,
			) (fragment.Result, error) {
				return fragment.Result{}, context.Canceled
			},
		}
		_, _, err := operation.downloadSelection(
			context.Background(), selection, root, filepath.Join(root, "cancel.mp4"), nil,
		)
		if !errors.Is(err, context.Canceled) ||
			!IsCategory(categorized("download", err), ErrorCancelled) {
			t.Fatalf("error=%v", err)
		}
	})

	t.Run("processing failure", func(t *testing.T) {
		operation := operation{
			transport: transport, request: Request{OutputDir: root},
			hlsFallback: func(
				context.Context, string, string, string, http.Header, bool, events.Sink,
			) (fragment.Result, error) {
				return fragment.Result{}, ffmpeg.ErrMediaFailure
			},
		}
		_, _, err := operation.downloadSelection(
			context.Background(), selection, root, filepath.Join(root, "failed.mp4"), nil,
		)
		if !errors.Is(err, ffmpeg.ErrMediaFailure) ||
			!IsCategory(categorized("download", err), ErrorInternal) {
			t.Fatalf("error=%v", err)
		}
	})
}
