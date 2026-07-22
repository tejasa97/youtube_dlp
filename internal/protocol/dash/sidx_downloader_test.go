package dash

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

// sidxTestMedia builds a synthetic media resource with an init segment,
// a SIDX box, and two media subsegments. Returns the full resource bytes
// and the indexRange string for the MPD.
func sidxTestMedia() ([]byte, string) {
	// Layout: [init: 0-99] [sidx: 100-...] [media1] [media2]
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("MEDIA_SEGMENT_ONE_DATA_")
	media2 := []byte("MEDIA_SEGMENT_TWO_DATA_")

	refs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	sidxBox := buildSIDX(0, 1, 48000, 0, 0, refs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, sidxBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(sidxBox)-1)
	return resource, indexRange
}

func TestDownloadSIDXExactRangeHeader(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	var gotRange atomic.Value
	var once atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			// Capture only the first range request (the SIDX fetch).
			if once.CompareAndSwap(false, true) {
				gotRange.Store(r.Header.Get("Range"))
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The first range request should be for the SIDX index.
	expected := "bytes=" + indexRange
	if got := gotRange.Load().(string); got != expected {
		t.Fatalf("Range header = %q, want %q", got, expected)
	}
}

func TestDownloadSIDXHeadersPropagated(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	var mpdAuth, sidxAuth, mediaAuth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("X-Custom-Auth")
		switch r.URL.Path {
		case "/manifest.mpd":
			mpdAuth.Store(auth)
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				sidxAuth.Store(auth)
			} else {
				mediaAuth.Store(auth)
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	headers := http.Header{"X-Custom-Auth": {"secret-token"}}
	_, err := NewDownloader(transport, Config{Headers: headers}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := mpdAuth.Load(); v != "secret-token" {
		t.Fatalf("MPD auth = %v", v)
	}
	if v := sidxAuth.Load(); v != "secret-token" {
		t.Fatalf("SIDX auth = %v", v)
	}
	if v := mediaAuth.Load(); v != "secret-token" {
		t.Fatalf("media auth = %v", v)
	}
}

func TestDownloadSIDX206Success(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	// Expected: init(100 bytes) + media1 + media2
	expected := string(make([]byte, 100)) + "MEDIA_SEGMENT_ONE_DATA_" + "MEDIA_SEGMENT_TWO_DATA_"
	expected = strings.ReplaceAll(expected, "\x00", "I")
	if string(contents) != expected {
		t.Fatalf("contents length = %d, want %d", len(contents), len(expected))
	}
}

func TestDownloadSIDX200Fallback(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Ignore range, serve full resource with 200.
				w.WriteHeader(http.StatusOK)
				w.Write(resource)
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if len(contents) != 100+23+23 {
		t.Fatalf("contents length = %d", len(contents))
	}
}

func TestDownloadSIDXInvalidContentRange(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Return wrong Content-Range.
				parts := strings.SplitN(indexRange, "-", 2)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%s/%d", parts[1], len(resource)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(resource[100:])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadSIDXTruncatedResponse(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Return truncated SIDX (only 10 bytes).
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %s/%d", indexRange, len(resource)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(resource[100:110])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected error for truncated SIDX")
	}
}

func TestDownloadSIDXOversized200Response(t *testing.T) {
	_, indexRange := sidxTestMedia()
	// Create a huge resource that exceeds maxIndexRangeBytes.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Claim huge content length to trigger rejection.
				w.Header().Set("Content-Length", fmt.Sprintf("%d", maxIndexRangeBytes+1))
				w.WriteHeader(http.StatusOK)
				return
			}
			serveRange(w, r, make([]byte, 200))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadSIDXOrderedInitAndMediaAssembly(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	// Verify ordering: init first, then media1, then media2.
	if len(contents) < 100 {
		t.Fatalf("contents too short: %d", len(contents))
	}
	if contents[0] != 'I' {
		t.Fatalf("first byte = %c, want I", contents[0])
	}
	if string(contents[100:123]) != "MEDIA_SEGMENT_ONE_DATA_" {
		t.Fatalf("media1 = %q", contents[100:123])
	}
	if string(contents[123:146]) != "MEDIA_SEGMENT_TWO_DATA_" {
		t.Fatalf("media2 = %q", contents[123:146])
	}
}

func TestDownloadSIDXRetryTransientIndexFailure(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				if attempts.Add(1) == 1 {
					w.WriteHeader(http.StatusServiceUnavailable)
					return
				}
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	// The index fetch itself doesn't retry (it's a single fetch), but the
	// fragment downloader retries media segments. For index retry, we need
	// to verify the error propagates. Since our fetchIndexRange doesn't retry,
	// a 503 on the first attempt will fail.
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected error for 503 on index fetch")
	}
}

func TestDownloadSIDXRetryTransientMediaFailure(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	var mediaAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				serveRange(w, r, resource)
				return
			}
			// Fail the first media segment request, succeed on retry.
			if mediaAttempts.Add(1) == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{Attempts: 3, RetryBaseDelay: time.Millisecond}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if len(contents) != 146 {
		t.Fatalf("contents length = %d", len(contents))
	}
}

func TestDownloadSIDXCancellationDuringIndexRetrieval(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			// Delay to allow cancellation.
			time.Sleep(200 * time.Millisecond)
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	dest := filepath.Join(root, "out.mp4")
	_, err := NewDownloader(transport, Config{}).Download(ctx, server.URL+"/manifest.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadSIDXCancellationDuringSegmentDownload(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	var mediaHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				serveRange(w, r, resource)
				return
			}
			if mediaHits.Add(1) >= 2 {
				time.Sleep(200 * time.Millisecond)
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	dest := filepath.Join(root, "out.mp4")
	_, err := NewDownloader(transport, Config{}).Download(ctx, server.URL+"/manifest.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist on cancellation: %v", statErr)
	}
}

func TestDownloadSIDXNoOutputOnFailure(t *testing.T) {
	_, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			w.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadSIDXAudioVideoMergeRequired(t *testing.T) {
	videoResource, videoIndexRange := sidxTestMedia()
	audioResource, audioIndexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="a" bandwidth="128"><BaseURL>audio.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
</Period></MPD>`, videoIndexRange, audioIndexRange)
		case "/video.mp4":
			serveRange(w, r, videoResource)
		case "/audio.mp4":
			serveRange(w, r, audioResource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired || len(result.Tracks) != 2 {
		t.Fatalf("result = %#v", result)
	}
}

func TestDownloadSIDXAtomicPublication(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Verify the final file exists and no .part or .fragments remain.
	if _, statErr := os.Stat(dest); statErr != nil {
		t.Fatalf("destination missing: %v", statErr)
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf(".part file should not exist")
	}
	if _, statErr := os.Stat(dest + ".fragments"); !os.IsNotExist(statErr) {
		t.Fatalf(".fragments dir should not exist")
	}
	if result.Tracks[0].Download.Path != dest {
		t.Fatalf("path = %q", result.Tracks[0].Download.Path)
	}
}

func TestDownloadSIDXDynamicRejected(t *testing.T) {
	_, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT1S"><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, make([]byte, 200))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "dynamic") {
		t.Fatalf("err = %v, want dynamic rejection", err)
	}
}

// serveRange implements HTTP range serving for test resources.
func serveRange(w http.ResponseWriter, r *http.Request, resource []byte) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.WriteHeader(http.StatusOK)
		w.Write(resource)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if start >= int64(len(resource)) || end >= int64(len(resource)) || start > end {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(resource)))
	w.WriteHeader(http.StatusPartialContent)
	w.Write(resource[start : end+1])
}

func TestDownloadSIDXMissingContentRange(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// 206 without Content-Range header.
				parts := strings.SplitN(indexRange, "-", 2)
				var start, end int64
				fmt.Sscanf(parts[0], "%d", &start)
				fmt.Sscanf(parts[1], "%d", &end)
				w.WriteHeader(http.StatusPartialContent)
				w.Write(resource[start : end+1])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "missing Content-Range") {
		t.Fatalf("err = %v, want missing Content-Range", err)
	}
}

func TestDownloadSIDXMalformedContentRange(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Malformed Content-Range with junk after numbers.
				w.Header().Set("Content-Range", "bytes 100junk-155junk/999")
				w.WriteHeader(http.StatusPartialContent)
				parts := strings.SplitN(indexRange, "-", 2)
				var start, end int64
				fmt.Sscanf(parts[0], "%d", &start)
				fmt.Sscanf(parts[1], "%d", &end)
				w.Write(resource[start : end+1])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("err = %v, want Content-Range mismatch", err)
	}
}

func TestDownloadSIDXMismatchedContentRange(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Valid format but wrong offset.
				w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-55/%d", len(resource)))
				w.WriteHeader(http.StatusPartialContent)
				parts := strings.SplitN(indexRange, "-", 2)
				var start, end int64
				fmt.Sscanf(parts[0], "%d", &start)
				fmt.Sscanf(parts[1], "%d", &end)
				w.Write(resource[start : end+1])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("err = %v, want Content-Range mismatch", err)
	}
}

func TestDownloadSIDXInitFullOverlapRejected(t *testing.T) {
	// Build a resource where the init range fully overlaps the first media range.
	media1 := []byte("MEDIA_SEGMENT_ONE_DATA_")
	media2 := []byte("MEDIA_SEGMENT_TWO_DATA_")
	refs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	sidxBox := buildSIDX(0, 1, 48000, 0, 0, refs)
	// Layout: [sidx at 0] [media1] [media2]
	var resource []byte
	resource = append(resource, sidxBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)
	// Init range 0-99 fully overlaps the sidx+media region.
	indexRange := fmt.Sprintf("0-%d", len(sidxBox)-1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v, want overlap rejection", err)
	}
}

func TestDownloadSIDXInitPartialOverlapRejected(t *testing.T) {
	// Init range partially overlaps the first media range.
	media1 := []byte("MEDIA_SEGMENT_ONE_DATA_")
	refs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	sidxBox := buildSIDX(0, 1, 48000, 0, 0, refs)
	// Layout: [sidx at 0] [media1 at len(sidx)]
	var resource []byte
	resource = append(resource, sidxBox...)
	resource = append(resource, media1...)
	// media1 starts at len(sidxBox). Init range ends inside media1.
	mediaStart := len(sidxBox)
	initEnd := mediaStart + 5 // partial overlap
	indexRange := fmt.Sprintf("0-%d", len(sidxBox)-1)
	initRange := fmt.Sprintf("0-%d", initEnd)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="%s"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange, initRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v, want overlap rejection", err)
	}
}

func TestDownloadSIDXInitOverlapWithLaterReferenceRejected(t *testing.T) {
	// Init range does not overlap first media range but overlaps the second.
	media1 := []byte("AAAA")
	media2 := []byte("BBBBBBBB")
	refs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 1000},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 1000},
	}
	sidxBox := buildSIDX(0, 1, 1000, 0, 0, refs)
	var resource []byte
	resource = append(resource, sidxBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)
	// media2 starts at len(sidx)+4. Set init range to overlap media2.
	media2Start := len(sidxBox) + len(media1)
	initStart := media2Start + 2
	initEnd := media2Start + 5
	indexRange := fmt.Sprintf("0-%d", len(sidxBox)-1)
	initRange := fmt.Sprintf("%d-%d", initStart, initEnd)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="%s"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange, initRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("err = %v, want overlap rejection", err)
	}
}

func TestDownloadSIDXInitNoOverlapSucceeds(t *testing.T) {
	// Init range is before all media ranges — should succeed.
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, dest, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if len(contents) != 146 {
		t.Fatalf("contents length = %d, want 146", len(contents))
	}
}

func TestDownloadSIDXHostileRangeOverflowNoPanic(t *testing.T) {
	// A hostile indexRange like MaxInt64-MaxInt64 passes parseByteRange with
	// length 1. The 200 fallback must not panic on slice bounds.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprint(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="9223372036854775807-9223372036854775807"/></Representation></AdaptationSet></Period></MPD>`)
		case "/video.mp4":
			// Server ignores Range and returns 200 with a small body.
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("small body"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected error for hostile range, got nil")
	}
	if !strings.Contains(err.Error(), "too short") && !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("err = %v, want categorized error without panic", err)
	}
}

func TestValidContentRangeTotalValidation(t *testing.T) {
	// expectedStart=100, expectedLength=56 → END=155
	tests := []struct {
		name   string
		header string
		want   bool
	}{
		{"valid star total", "bytes 100-155/*", true},
		{"valid numeric total", "bytes 100-155/999", true},
		{"valid total equals end+1", "bytes 100-155/156", true},
		{"empty total", "bytes 100-155/", false},
		{"junk total", "bytes 100-155/junk", false},
		{"total equals end", "bytes 100-155/155", false},
		{"total less than end", "bytes 100-155/100", false},
		{"trailing junk after total", "bytes 100-155/999junk", false},
		{"negative total", "bytes 100-155/-1", false},
		{"space in total", "bytes 100-155/ 999", false},
		{"zero total", "bytes 100-155/0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validContentRange(tt.header, 100, 56)
			if got != tt.want {
				t.Errorf("validContentRange(%q, 100, 56) = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

func TestDownloadSIDXContentRangeEmptyTotal(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Content-Range with empty total after slash.
				parts := strings.SplitN(indexRange, "-", 2)
				var start, end int64
				fmt.Sscanf(parts[0], "%d", &start)
				fmt.Sscanf(parts[1], "%d", &end)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/", start, end))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(resource[start : end+1])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("err = %v, want Content-Range mismatch for empty total", err)
	}
}

func TestDownloadSIDXContentRangeInconsistentTotal(t *testing.T) {
	resource, indexRange := sidxTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+indexRange {
				// Content-Range with total <= END (inconsistent).
				parts := strings.SplitN(indexRange, "-", 2)
				var start, end int64
				fmt.Sscanf(parts[0], "%d", &start)
				fmt.Sscanf(parts[1], "%d", &end)
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, end))
				w.WriteHeader(http.StatusPartialContent)
				w.Write(resource[start : end+1])
				return
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("err = %v, want Content-Range mismatch for inconsistent total", err)
	}
}
