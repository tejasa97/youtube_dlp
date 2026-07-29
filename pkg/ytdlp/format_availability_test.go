package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func availabilityTestChecker(t *testing.T, mode FormatCheckMode) *formatAvailabilityChecker {
	t.Helper()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return newFormatAvailabilityChecker(context.Background(), transport, mode)
}

func availabilityFormat(rawURL string, headers http.Header, fields ...value.Field) *value.Object {
	base := []value.Field{{Key: "url", Value: value.String(rawURL)}, {Key: "protocol", Value: value.String("https")}}
	if len(headers) != 0 {
		base = append(base, value.Field{Key: "http_headers", Value: value.ObjectValue(headerObject(headers))})
	}
	base = append(base, fields...)
	return value.NewObject(base...)
}

func headerObject(headers http.Header) *value.Object {
	fields := make([]value.Field, 0, len(headers))
	for key, values := range headers {
		if len(values) == 0 {
			continue
		}
		// The normalized format contract uses one string per header field.
		fields = append(fields, value.Field{Key: key, Value: value.String(values[len(values)-1])})
	}
	return value.NewObject(fields...)
}

func TestFormatAvailabilityUsesBoundedRangeGETBeforeHEAD(t *testing.T) {
	var gets, heads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			gets.Add(1)
			if request.Header.Get("Range") != "bytes=0-0" {
				t.Errorf("Range = %q", request.Header.Get("Range"))
			}
			// Deliberately ignore Range and expose a large body. The checker must
			// accept the bounded prefix without consuming the media.
			_, _ = writer.Write(make([]byte, 4096))
		case http.MethodHead:
			heads.Add(1)
			writer.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	ok, err := checker.IsAvailable(availabilityFormat(server.URL, nil))
	if err != nil || !ok {
		t.Fatalf("IsAvailable = %v, %v", ok, err)
	}
	if gets.Load() != 1 || heads.Load() != 0 {
		t.Fatalf("GET=%d HEAD=%d, want range GET only", gets.Load(), heads.Load())
	}
}

func TestFormatAvailabilityCacheIncludesCredentialValues(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Header.Get("Authorization") != "Bearer good" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	bad, err := checker.IsAvailable(availabilityFormat(server.URL, http.Header{"Authorization": {"Bearer bad"}}))
	if err != nil || bad {
		t.Fatalf("bad credential = %v, %v", bad, err)
	}
	good, err := checker.IsAvailable(availabilityFormat(server.URL, http.Header{"Authorization": {"Bearer good"}}))
	if err != nil || !good {
		t.Fatalf("good credential = %v, %v", good, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d, want separate probes", calls.Load())
	}
	goodAgain, err := checker.IsAvailable(availabilityFormat(server.URL, http.Header{"Authorization": {"Bearer good"}}))
	if err != nil || !goodAgain || calls.Load() != 2 {
		t.Fatalf("cached credential result=%v err=%v calls=%d", goodAgain, err, calls.Load())
	}
}

func TestFormatAvailabilityAutoSkipsOrdinaryButChecksNeedsTesting(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		writer.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckAuto)
	ok, err := checker.IsAvailable(availabilityFormat(server.URL, nil))
	if err != nil || !ok || calls.Load() != 0 {
		t.Fatalf("ordinary auto = %v, %v calls=%d", ok, err, calls.Load())
	}
	ok, err = checker.IsAvailable(availabilityFormat(server.URL, nil, value.Field{Key: "__needs_testing", Value: value.Bool(true)}))
	if err != nil || ok || calls.Load() != 1 {
		t.Fatalf("needs-testing auto = %v, %v calls=%d", ok, err, calls.Load())
	}
}

func TestFormatAvailabilityHLSProbesResolvedMediaSegment(t *testing.T) {
	var master, media, segment atomic.Int32
	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/master.m3u8":
			master.Add(1)
			_, _ = writer.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nmedia.m3u8\n"))
		case "/media.m3u8":
			media.Add(1)
			_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n"))
		case "/segment.ts":
			segment.Add(1)
			_, _ = writer.Write([]byte("x"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	serverURL = server.URL
	checker := availabilityTestChecker(t, FormatCheckSelected)
	format := value.NewObject(
		value.Field{Key: "url", Value: value.String(serverURL + "/master.m3u8")},
		value.Field{Key: "protocol", Value: value.String("m3u8_native")},
	)
	ok, err := checker.IsAvailable(format)
	if err != nil || !ok {
		t.Fatalf("IsAvailable = %v, %v", ok, err)
	}
	if master.Load() != 1 || media.Load() != 1 || segment.Load() != 1 {
		t.Fatalf("master=%d media=%d segment=%d", master.Load(), media.Load(), segment.Load())
	}
}

func TestFormatAvailabilityRedirectStripsExplicitCredentials(t *testing.T) {
	var targetAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetAuthorization = request.Header.Get("Authorization")
		_, _ = writer.Write([]byte("x"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer origin.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	ok, err := checker.IsAvailable(availabilityFormat(origin.URL, http.Header{"Authorization": {"Bearer secret"}}))
	if err != nil || !ok {
		t.Fatalf("IsAvailable = %v, %v", ok, err)
	}
	if targetAuthorization != "" {
		t.Fatalf("credential forwarded across origin: %q", targetAuthorization)
	}
}

func TestFormatAvailabilitySendsFormatCookieToOriginalOrigin(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Cookie") != "session=required" {
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	ok, err := checker.IsAvailable(availabilityFormat(server.URL, http.Header{"Cookie": {"session=required"}}))
	if err != nil || !ok {
		t.Fatalf("format cookie result=%v err=%v", ok, err)
	}
}

func TestFormatAvailabilityOversizedManifestReturnsLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(make([]byte, availabilityMaxProbeBytes+1))
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	format := value.NewObject(value.Field{Key: "url", Value: value.String(server.URL)}, value.Field{Key: "protocol", Value: value.String("m3u8_native")})
	ok, err := checker.IsAvailable(format)
	if ok || !errors.Is(err, ErrFormatCheckLimit) {
		t.Fatalf("oversized result=%v err=%v", ok, err)
	}
}

func TestFormatAvailabilityDASHProbesInitialResource(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Range") != "bytes=0-0" {
			t.Errorf("range=%q", r.Header.Get("Range"))
		}
		_, _ = w.Write([]byte("x"))
	}))
	defer server.Close()
	ok, err := availabilityTestChecker(t, FormatCheckSelected).probeDASHFragment(context.Background(), dash.MPD{Representations: []dash.Representation{{Segments: []dash.Segment{{URL: server.URL}}}}}, nil)
	if err != nil || !ok || hits.Load() != 1 {
		t.Fatalf("ok=%v err=%v hits=%d", ok, err, hits.Load())
	}
}

func TestFormatAvailabilityISMProbesInitialResource(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte("x")) }))
	defer server.Close()
	manifest := ism.Manifest{Timescale: 1, Duration: 1, Streams: []ism.Stream{{Type: "video", URL: "{bitrate}/{start time}", Qualities: []ism.Quality{{Bitrate: 1}}, Chunks: []ism.Chunk{{Time: 0, Duration: 1}}}}}
	ok, err := availabilityTestChecker(t, FormatCheckSelected).probeISMFragment(context.Background(), server.URL+"/manifest", manifest, nil)
	if err != nil || !ok || hits.Load() != 1 {
		t.Fatalf("ok=%v err=%v hits=%d", ok, err, hits.Load())
	}
}

func TestFormatAvailabilityRangeHonoringOversizedManifestReturnsLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-1/99999999")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("#"))
	}))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	_, _, err := checker.getDocument(context.Background(), server.URL, nil, 2)
	if !errors.Is(err, ErrFormatCheckLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestFormatAvailabilityAggregateBudgetReturnsLimit(t *testing.T) {
	checker := availabilityTestChecker(t, FormatCheckSelected)
	checker.bytes = availabilityMaxTotalBytes
	if err := checker.recordBytes(1); !errors.Is(err, ErrFormatCheckLimit) {
		t.Fatalf("err=%v", err)
	}
}

func TestFormatCheckNoneMakesNoProbeRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	if shouldCheckFormats(FormatCheckNone, false) {
		t.Fatal("none enabled checker")
	}
	if calls.Load() != 0 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestFormatCheckAllReusesProbeCacheDuringPlanning(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); _, _ = w.Write([]byte("x")) }))
	defer server.Close()
	checker := availabilityTestChecker(t, FormatCheckAll)
	format := availabilityFormat(server.URL, nil)
	if _, err := checker.IsAvailable(format); err != nil {
		t.Fatal(err)
	}
	if _, err := checker.IsAvailable(format); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d want one cached identity", calls.Load())
	}
}

func TestFormatAvailabilityInternalTimeoutIsUnavailable(t *testing.T) {
	checker := availabilityTestChecker(t, FormatCheckSelected)
	checker.timeout = 10 * time.Millisecond
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	// Only the checker child deadline expires; the operation context stays live.
	ok, err := checker.IsAvailable(availabilityFormat(server.URL, nil))
	if ok || err != nil {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestFormatAvailabilityCrossOriginRedirectUsesOnlyDestinationScopedJarCookies(t *testing.T) {
	var cookie, authorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/set" {
			http.SetCookie(w, &http.Cookie{Name: "destination", Value: "jar", Path: "/"})
			return
		}
		cookie = r.Header.Get("Cookie")
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("x"))
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer origin.Close()
	checker := availabilityTestChecker(t, FormatCheckSelected)
	if _, _, err := checker.transport.ReadPage(context.Background(), target.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	ok, err := checker.IsAvailable(availabilityFormat(origin.URL, http.Header{"Cookie": {"origin=secret"}, "Authorization": {"Bearer secret"}}))
	if err != nil || !ok || cookie != "destination=jar" || authorization != "" {
		t.Fatalf("ok=%v err=%v destination cookie=%q authorization=%q", ok, err, cookie, authorization)
	}
}

func TestFormatCheckSelectedFallsBackFromUnavailablePreferred(t *testing.T) {
	formats := value.List(
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("bad")}, value.Field{Key: "url", Value: value.String("https://bad.invalid")}, value.Field{Key: "height", Value: value.Int(1080)})),
		value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("good")}, value.Field{Key: "url", Value: value.String("https://good.invalid")}, value.Field{Key: "height", Value: value.Int(720)})),
	)
	info := value.NewInfo(value.NewObject(value.Field{Key: "formats", Value: formats}))
	prepared, err := mediaformat.Prepare(info, mediaformat.Options{})
	if err != nil {
		t.Fatal(err)
	}
	selector, err := mediaformat.ParseSelector("best/best")
	if err != nil {
		t.Fatal(err)
	}
	plans, err := prepared.PlanWithOptions(selector, mediaformat.EvaluationOptions{Availability: mediaformat.FormatAvailabilityFunc(func(o *value.Object) (bool, error) {
		id, _ := o.Lookup("format_id").StringValue()
		return id == "good", nil
	})})
	if err != nil || len(plans) != 1 || plans[0].Tracks[0].ID != "good" {
		t.Fatalf("plans=%#v err=%v", plans, err)
	}
}

func TestFormatAvailabilityParentCancellationAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	checker := newFormatAvailabilityChecker(ctx, transport, FormatCheckSelected)
	ok, err := checker.IsAvailable(availabilityFormat("https://example.invalid/media", nil))
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("IsAvailable = %v, %v", ok, err)
	}
}
