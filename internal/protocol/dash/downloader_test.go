package dash

import (
	"context"
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

func TestDownloadSeparateAudioVideoTracks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><SegmentTemplate duration="1" initialization="v-init" media="v-$Number$"/><Representation id="v" bandwidth="1000"/></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><SegmentTemplate duration="1" initialization="a-init" media="a-$Number$"/><Representation id="a" bandwidth="128"/></AdaptationSet>
</Period></MPD>`)
		case "/v-init":
			_, _ = writer.Write([]byte("VI"))
		case "/v-1", "/v-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		case "/a-init":
			_, _ = writer.Write([]byte("AI"))
		case "/a-1", "/a-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "dash.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired || len(result.Tracks) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, track := range result.Tracks {
		contents, _ := os.ReadFile(track.Download.Path)
		if len(contents) != 4 {
			t.Fatalf("track %s contents = %q", track.Representation.ID, contents)
		}
	}
}

func TestDownloadDynamicMPDPollsAndDeduplicates(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			repeat := 0
			if polls.Add(1) > 1 {
				repeat = 1
			}
			_, _ = fmt.Fprintf(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="1" r="%d"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, repeat)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "live.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if string(contents) != "/0.m4s/1.m4s" || polls.Load() != 2 {
		t.Fatalf("contents = %q, polls = %d", contents, polls.Load())
	}
}

func TestDownloadRetriesTransientMPDWithoutDuplicateSegments(t *testing.T) {
	var manifestAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			if manifestAttempts.Add(1) == 1 {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment-$Number$"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "retry.bin")
	result, err := NewDownloader(transport, Config{DynamicPolls: 1, Attempts: 3, RetryBaseDelay: time.Millisecond}).Download(
		context.Background(), server.URL+"/live.mpd", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if manifestAttempts.Load() != 2 {
		t.Fatalf("manifest attempts = %d, want 2", manifestAttempts.Load())
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := string(contents), "/segment-1"; got != want {
		t.Fatalf("contents = %q, want %q", got, want)
	}
}

func TestDynamicMPDPolicyDefaultsAllow(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte("dynamic-allowed"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil || string(contents) != "dynamic-allowed" {
		t.Fatalf("contents = %q, error = %v", contents, readErr)
	}
}

func TestDynamicMPDPolicyDenyRunsParseAndURLValidationBeforeTransfer(t *testing.T) {
	var nonManifestRequests atomic.Int32
	var validatedURLs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="v"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="0-7"/></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		nonManifestRequests.Add(1)
		http.Error(writer, "dynamic policy should stop before index/media", http.StatusInternalServerError)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{
		DynamicMPDPolicy: DynamicMPDPolicyDeny,
		URLValidator: func(string) error {
			validatedURLs.Add(1)
			return nil
		},
	}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if !errors.Is(err, ErrDynamicMPDUnsupported) {
		t.Fatalf("error = %v, want ErrDynamicMPDUnsupported", err)
	}
	if validatedURLs.Load() < 2 {
		t.Fatalf("validated URLs = %d, want manifest and representation URL validation", validatedURLs.Load())
	}
	if nonManifestRequests.Load() != 0 {
		t.Fatalf("index/media requests = %d, want 0", nonManifestRequests.Load())
	}
}

func TestDynamicMPDPolicyDenyLeavesStaticMPDUnaffected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/manifest.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD mediaPresentationDuration="PT1S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="segment.m4s" duration="1"/></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		_, _ = writer.Write([]byte("static-allowed"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicMPDPolicy: DynamicMPDPolicyDeny}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, readErr := os.ReadFile(result.Tracks[0].Download.Path)
	if readErr != nil || string(contents) != "static-allowed" {
		t.Fatalf("contents = %q, error = %v", contents, readErr)
	}
}

func TestDownloadCancellationDuringMPDFetchDoesNotRetryOrCreateOutput(t *testing.T) {
	var manifestAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		manifestAttempts.Add(1)
		<-request.Context().Done()
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cancel.bin")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{Attempts: 5, RetryBaseDelay: time.Millisecond}).Download(
		ctx, server.URL+"/live.mpd", root, destination, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if manifestAttempts.Load() != 1 {
		t.Fatalf("manifest attempts = %d, want 1", manifestAttempts.Load())
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadMPDRetryRedactsURLAndPreservesHeaderIsolation(t *testing.T) {
	var seenAuthorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seenAuthorization.Store(request.Header.Get("Authorization"))
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	header := http.Header{"Authorization": {"Bearer dash-secret"}}
	downloader := NewDownloader(transport, Config{Headers: header, Attempts: 2, RetryBaseDelay: time.Millisecond})
	header.Set("Authorization", "Bearer mutated-after-construction")
	root := t.TempDir()
	_, err := downloader.Download(context.Background(), server.URL+"/live.mpd?token=manifest-secret", root, filepath.Join(root, "out.bin"), false, nil)
	if err == nil {
		t.Fatal("expected transient MPD failure")
	}
	if got := seenAuthorization.Load().(string); got != "Bearer dash-secret" {
		t.Fatalf("Authorization = %q, want cloned header", got)
	}
	if strings.Contains(err.Error(), "manifest-secret") || strings.Contains(err.Error(), "dash-secret") || strings.Contains(err.Error(), "mutated-after-construction") {
		t.Fatalf("error leaked sensitive data: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP status 503") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("error = %v, want redacted status evidence", err)
	}
}

func TestDownloadRejectsUnboundedProtocolRetryConfiguration(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	for name, test := range map[string]struct {
		config Config
		want   error
	}{
		"attempts":          {config: Config{Attempts: maxDASHAttempts + 1}, want: fragment.ErrTooManyAttempts},
		"negative attempts": {config: Config{Attempts: -1}, want: fragment.ErrTooManyAttempts},
		"delay":             {config: Config{RetryBaseDelay: maxDASHRetryDelay + time.Nanosecond}, want: fragment.ErrTooManyAttempts},
		"polls":             {config: Config{DynamicPolls: maxDASHPolls + 1}, want: ErrUnsupportedAddressing},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewDownloader(transport, test.config).Download(context.Background(), server.URL+"/live.mpd", t.TempDir(), filepath.Join(t.TempDir(), "out.bin"), false, nil)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("requests = %d, want 0", requests.Load())
	}
}

func TestDownloadDynamicUsesManifestPollIntervalAndCancels(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		polls.Add(1)
		_, _ = fmt.Fprint(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT5S"><Period><AdaptationSet contentType="video"><Representation id="v"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	destination := filepath.Join(root, "cancelled.bin")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(ctx, server.URL+"/live.mpd", root, destination, false, nil)
	if err != context.DeadlineExceeded {
		t.Fatalf("Download() error = %v", err)
	}
	if polls.Load() != 1 {
		t.Fatalf("polls = %d", polls.Load())
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not be finalized: %v", statErr)
	}
}

func TestDownloadDynamicDoesNotCollideSameRepresentationID(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			t := polls.Add(1) - 1
			_, _ = fmt.Fprintf(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period>
<AdaptationSet contentType="video"><Representation id="same" bandwidth="1000"><SegmentTemplate media="v-$Time$"><SegmentTimeline><S t="%d" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="same" bandwidth="128"><SegmentTemplate media="a-$Time$"><SegmentTimeline><S t="%d" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet>
</Period></MPD>`, t, t)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "same.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, track := range result.Tracks {
		contents, readErr := os.ReadFile(track.Download.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		prefix := "/v-"
		if track.Representation.ContentType == "audio" {
			prefix = "/a-"
		}
		if got, want := string(contents), prefix+"0"+prefix+"1"; got != want {
			t.Fatalf("%s contents = %q, want %q", track.Representation.ContentType, got, want)
		}
	}
}
