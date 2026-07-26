package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDownloadHLSCopiesMediaAndForwardsSafeHeaders(t *testing.T) {
	tools := requireToolset(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	segmentPath := filepath.Join(root, "segment.ts")
	if _, err := tools.execute(ctx, tools.ffmpeg, []string{
		"-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc=size=32x32:rate=10:duration=0.4",
		"-an", "-c:v", "mpeg2video", "-f", "mpegts", segmentPath,
	}, nil); err != nil {
		t.Fatalf("generate MPEG-TS: %v", err)
	}
	segment, err := os.ReadFile(segmentPath)
	if err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Referer") != "https://page.example/watch" {
			http.Error(writer, "missing referer", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/index.m3u8":
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.4,\nsegment.ts\n#EXT-X-ENDLIST\n")
		case "/segment.ts":
			_, _ = writer.Write(segment)
		case "/local.m3u8":
			local := (&url.URL{Scheme: "file", Path: segmentPath}).String()
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:0.4,\n%s\n#EXT-X-ENDLIST\n", local)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	destination := filepath.Join(root, "output.ts")
	if err := tools.DownloadHLS(
		ctx, server.URL+"/index.m3u8", destination,
		http.Header{"Referer": {"https://page.example/watch"}},
		false, nil,
	); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(destination); err != nil || info.Size() == 0 {
		t.Fatalf("output missing: %v", err)
	}
	localDestination := filepath.Join(root, "local-output.ts")
	err = tools.DownloadHLS(
		ctx, server.URL+"/local.m3u8", localDestination,
		http.Header{"Referer": {"https://page.example/watch"}},
		false, nil,
	)
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("file segment error=%v", err)
	}
	if _, statErr := os.Stat(localDestination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("file segment destination exists: %v", statErr)
	}
}

func TestDownloadHLSRejectsUnsafeInputsBeforeExecution(t *testing.T) {
	tools := &Toolset{ffmpeg: filepath.Join(t.TempDir(), "must-not-run"), maxOutput: 1024}
	destination := filepath.Join(t.TempDir(), "output.mp4")
	for _, test := range []struct {
		name, rawURL string
		headers      http.Header
		want         error
	}{
		{"userinfo URL", "https://user@example.test/index.m3u8", nil, ErrInvalidOperation},
		{"file URL", "file:///tmp/index.m3u8", nil, ErrInvalidOperation},
		{"authorization", "https://example.test/index.m3u8", http.Header{"Authorization": {"Bearer secret"}}, ErrUnsafeHLSHeaders},
		{"cookie", "https://example.test/index.m3u8", http.Header{"Cookie": {"secret=value"}}, ErrUnsafeHLSHeaders},
		{"api key", "https://example.test/index.m3u8", http.Header{"X-Api-Key": {"secret"}}, ErrUnsafeHLSHeaders},
		{"playback token", "https://example.test/index.m3u8", http.Header{"X-Playback-Token": {"secret"}}, ErrUnsafeHLSHeaders},
		{"newline", "https://example.test/index.m3u8", http.Header{"Referer": {"ok\r\nX-Secret: value"}}, ErrUnsafeHLSHeaders},
		{"invalid referer", "https://example.test/index.m3u8", http.Header{"Referer": {"opaque-secret"}}, ErrUnsafeHLSHeaders},
		{"invalid name", "https://example.test/index.m3u8", http.Header{"Bad Header": {"value"}}, ErrUnsafeHLSHeaders},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := tools.DownloadHLS(context.Background(), test.rawURL, destination, test.headers, false, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v", err)
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("destination exists: %v", statErr)
			}
		})
	}
}

func TestDownloadHLSSanitizesManifestURLFromFailure(t *testing.T) {
	tools := requireToolset(t)
	const secret = "opaque-path-secret-713"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("not an HLS manifest"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "output.mp4")
	err := tools.DownloadHLS(
		context.Background(), server.URL+"/"+secret+"/index.m3u8?opaque="+secret,
		destination, nil, false, nil,
	)
	if !errors.Is(err, ErrMediaFailure) {
		t.Fatalf("error=%v", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("failure leaks manifest URL: %v", err)
	}
}

func TestDownloadHLSCancellationLeavesNoOutput(t *testing.T) {
	tools := &Toolset{ffmpeg: filepath.Join(t.TempDir(), "must-not-run"), maxOutput: 1024}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	destination := filepath.Join(t.TempDir(), "output.mp4")
	err := tools.DownloadHLS(ctx, "https://example.test/index.m3u8", destination, nil, false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination exists: %v", statErr)
	}
}

func TestDiscoverFFmpegDoesNotRequireFFprobe(t *testing.T) {
	tools, err := DiscoverFFmpeg(Config{FFprobePath: filepath.Join(t.TempDir(), "missing")})
	if errors.Is(err, ErrFFmpegUnavailable) {
		t.Skip("ffmpeg unavailable")
	}
	if err != nil || tools.ffmpeg == "" || tools.ffprobe != "" {
		t.Fatalf("tools=%#v error=%v", tools, err)
	}
}
