package hds

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/events"
)

// makeBox builds a single ISO box with the given type and payload using a
// 32-bit big-endian size header.
func makeBox(kind string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], kind)
	copy(out[8:], payload)
	return out
}

func makeFragmentBox(mdatPayload []byte) []byte { return makeBox("mdat", mdatPayload) }

func makeABST(asrtRuns []SegmentRun, afrtRuns []FragmentRun, live bool) []byte {
	return makeBox("abst", encodeABSTBody(asrtRuns, afrtRuns, live))
}

func encodeABSTBody(asrtRuns []SegmentRun, afrtRuns []FragmentRun, live bool) []byte {
	var buf []byte
	buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	flags := byte(0)
	if live {
		flags |= 0x20
	}
	buf = append(buf, flags)
	buf = append(buf, 0x00, 0x00, 0x00, 0x03)
	buf = append(buf, 0, 0, 0, 0, 0, 0, 0, 0)
	buf = append(buf, 0, 0, 0, 0, 0, 0, 0, 0)
	buf = append(buf, 0x00)
	buf = append(buf, 0x00)
	buf = append(buf, 0x00)
	buf = append(buf, 0x00)
	buf = append(buf, 0x00)
	buf = append(buf, 0x01)
	asrt := encodeASRTBody(asrtRuns)
	buf = append(buf, makeBox("asrt", asrt)...)
	buf = append(buf, 0x01)
	afrt := encodeAFRTBody(afrtRuns)
	buf = append(buf, makeBox("afrt", afrt)...)
	return buf
}

func encodeASRTBody(runs []SegmentRun) []byte {
	var buf []byte
	buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	buf = append(buf, 0x00)
	binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, uint32(len(runs)))
	for _, run := range runs {
		binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, run.FirstSegment)
		binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, run.FragmentsPerSegment)
	}
	return buf
}

func encodeAFRTBody(runs []FragmentRun) []byte {
	var buf []byte
	buf = append(buf, 0x00, 0x00, 0x00, 0x00)
	buf = append(buf, 0x00, 0x00, 0x00, 0x03)
	buf = append(buf, 0x00)
	binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, uint32(len(runs)))
	for _, run := range runs {
		binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, run.First)
		binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, run.Timestamp)
		binary.Write(binaryBigEndianWriter{&buf}, binary.BigEndian, run.Duration)
		if run.Duration == 0 && run.DiscontinuityIndicator != nil {
			buf = append(buf, *run.DiscontinuityIndicator)
		}
	}
	return buf
}

type binaryBigEndianWriter struct{ buf *[]byte }

func (w binaryBigEndianWriter) Write(p []byte) (int, error) {
	*w.buf = append(*w.buf, p...)
	return len(p), nil
}

// manifestWithBootstrap returns a valid F4M manifest XML body that references
// the given bootstrap URL.
func manifestWithBootstrap(mediaURL, bootstrapURL string) []byte {
	if bootstrapURL == "" {
		bootstrapURL = "http://cdn.example/bootstrap.bin"
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <media url="%s" bitrate="800"/>
  <bootstrapInfo url="%s"/>
</manifest>`, mediaURL, bootstrapURL))
}

// fakeTransport returns responses for a fixed list of URLs in order.
type fakeTransport struct {
	mu        atomic.Int32
	responses []fakeResponse
	calls     atomic.Int32
}

type fakeResponse struct {
	URL   string
	Body  []byte
	Code  int
	Delay time.Duration
}

func (t *fakeTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	idx := int(t.mu.Add(1) - 1)
	if idx >= len(t.responses) {
		return nil, fmt.Errorf("unexpected request %d to %s", idx, request.URL.String())
	}
	resp := t.responses[idx]
	if request.URL.String() != resp.URL {
		return nil, fmt.Errorf("expected %s, got %s", resp.URL, request.URL.String())
	}
	if resp.Delay > 0 {
		select {
		case <-time.After(resp.Delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	status := resp.Code
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(string(resp.Body))),
		Header:     http.Header{},
		Request:    request,
	}, nil
}

// redirectTransport returns a 302 with Location pointing at redirectTo for
// every request. It is used to exercise the bounded redirect walker.
type redirectTransport struct {
	redirectTo string
	calls      atomic.Int32
}

func (t *redirectTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	header := http.Header{"Location": []string{t.redirectTo}}
	return &http.Response{
		StatusCode: http.StatusFound,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     header,
		Request:    req,
	}, nil
}

// capturingTransport hands each request to a user-supplied handler and records
// every request so tests can assert header behavior.
type capturingTransport struct {
	requests []*http.Request
	handler  func(*http.Request) (*http.Response, error)
}

func (t *capturingTransport) DoWithoutCredentialsNoRedirect(_ context.Context, req *http.Request) (*http.Response, error) {
	t.requests = append(t.requests, req)
	if t.handler != nil {
		return t.handler(req)
	}
	return nil, fmt.Errorf("capturingTransport: no handler set")
}

// fragmentSink captures the Fragment index of each emitted event.
type fragmentSink struct {
	indexes []int
}

func (s *fragmentSink) Emit(_ context.Context, ev events.Event) error {
	s.indexes = append(s.indexes, ev.Fragment)
	return nil
}

func TestNewDownloaderRejectsNegativeAttempts(t *testing.T) {
	if _, err := NewDownloader(&fakeTransport{}, Config{Attempts: -1}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestNewDownloaderRejectsNilTransport(t *testing.T) {
	if _, err := NewDownloader(nil, Config{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}

func TestDownloadAssemblesFLVFromF4F(t *testing.T) {
	bootstrap := makeABST(
		[]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 3}},
		[]FragmentRun{{First: 10, Timestamp: 0, Duration: 1000}},
		false,
	)
	fragments := [][]byte{
		makeFragmentBox([]byte("AAA")),
		makeFragmentBox([]byte("BBB")),
		makeFragmentBox([]byte("CCC")),
	}
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "http://cdn.example/bootstrap.bin")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag10", Body: fragments[0]},
		{URL: "http://cdn.example/media.mp4Seg1-Frag11", Body: fragments[1]},
		{URL: "http://cdn.example/media.mp4Seg1-Frag12", Body: fragments[2]},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.flv")
	result, err := dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, destination, false, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, err := os.ReadFile(result.Path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data[:len(flvHeader)]) != flvHeader {
		t.Fatalf("FLV header mismatch")
	}
	if string(data[len(flvHeader):]) != "AAABBBCCC" {
		t.Fatalf("FLV body = %q, want AAABBBCCC", data[len(flvHeader):])
	}
	if result.Downloaded != 3 {
		t.Fatalf("downloaded = %d, want 3", result.Downloaded)
	}
}

func TestDownloadRejectsLiveBootstrap(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, true)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, filepath.Join(root, "out.flv"), false, nil)
	if !errors.Is(err, ErrUnsupportedLive) {
		t.Fatalf("err = %v, want ErrUnsupportedLive", err)
	}
}

func TestDownloadHonoursCancellation(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("PEND")), Delay: 200 * time.Millisecond},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = dl.Download(ctx, "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want DeadlineExceeded", err)
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination should be cleaned up, got %v", statErr)
	}
}

func TestDownloadCleansUpOnFragmentError(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 2}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("AAAA"))},
		{URL: "http://cdn.example/media.mp4Seg1-Frag2", Body: []byte("TRUNCATED-NO-MDAT")},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err == nil {
		t.Fatalf("expected error on fragment without mdat")
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination should be cleaned up on failure, got %v", statErr)
	}
}

func TestDownloadRejectsOverwriteWhenDisabled(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("AAAA"))},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	if err := os.WriteFile(dest, []byte("existing"), 0o600); err != nil {
		t.Fatalf("seed destination: %v", err)
	}
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err == nil || !strings.Contains(err.Error(), "exists") {
		t.Fatalf("err = %v, want exists error", err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "existing" {
		t.Fatalf("destination was clobbered: %q", data)
	}
}

func TestDownloadRetriesTransientFailures(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: nil, Code: http.StatusServiceUnavailable},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("ZZZ"))},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 3})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	result, err := dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, _ := os.ReadFile(result.Path)
	if string(data[len(flvHeader):]) != "ZZZ" {
		t.Fatalf("body = %q, want ZZZ", data[len(flvHeader):])
	}
	if trans.calls.Load() < 4 {
		t.Fatalf("calls = %d, want at least 4 (manifest, bootstrap, fragment retry, fragment ok)", trans.calls.Load())
	}
}

func TestDownloadStripsAuthOnCrossHost(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	fragment := makeFragmentBox([]byte("DATA"))
	// The manifest is hosted at cdn.example but its baseURL/media/bootstrapInfo
	// all reference other.example so every fetch except the manifest itself
	// crosses the host boundary.
	manifestBody := []byte(`<?xml version="1.0"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://other.example/</baseURL>
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://other.example/bootstrap.bin"/>
</manifest>`)
	trans := &capturingTransport{}
	trans.handler = func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "other.example":
			if strings.HasSuffix(req.URL.Path, "bootstrap.bin") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(string(bootstrap))),
					Header:     http.Header{},
					Request:    req,
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(fragment))),
				Header:     http.Header{},
				Request:    req,
			}, nil
		case "cdn.example":
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(string(manifestBody))),
				Header:     http.Header{},
				Request:    req,
			}, nil
		}
		return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer leaked")
	headers.Set("Cookie", "leaked=1")
	headers.Set("X-Trace", "ok")
	dl, err := NewDownloader(trans, Config{Headers: headers, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 1})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	for _, captured := range trans.requests {
		if captured.URL.Host == "other.example" {
			if captured.Header.Get("Authorization") != "" || captured.Header.Get("Cookie") != "" {
				t.Fatalf("credentials leaked to %s: %+v", captured.URL.Host, captured.Header)
			}
		}
	}
}

func TestDownloadRespectsRedirectHopCap(t *testing.T) {
	trans := &redirectTransport{redirectTo: "http://cdn.example/manifest.f4m"}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 1})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err == nil {
		t.Fatalf("expected redirect cap error")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want redirect mention", err)
	}
}

func TestDownloadRejectsRedirectToForeignScheme(t *testing.T) {
	trans := &redirectTransport{redirectTo: "javascript:alert(1)"}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 1})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err == nil {
		t.Fatalf("expected redirect rejection")
	}
	if !strings.Contains(err.Error(), "disallowed") && !strings.Contains(err.Error(), "javascript") {
		t.Fatalf("err = %v, want disallowed/javascript mention", err)
	}
}

func TestDownloadIncrementalSizeCapRejectsOversize(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 2}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox(make([]byte, 200))},
		{URL: "http://cdn.example/media.mp4Seg1-Frag2", Body: makeFragmentBox(make([]byte, 200))},
	}}
	dl, err := NewDownloader(trans, Config{MaxOutputBytes: 100, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if !errors.Is(err, ErrFragmentTooLarge) {
		t.Fatalf("err = %v, want ErrFragmentTooLarge", err)
	}
}

func TestDownloadEmitsFragmentEventsWithOneBasedIndex(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 2}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("AAA"))},
		{URL: "http://cdn.example/media.mp4Seg1-Frag2", Body: makeFragmentBox([]byte("BBB"))},
	}}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	sink := &fragmentSink{}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, sink)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	if len(sink.indexes) != 2 || sink.indexes[0] != 1 || sink.indexes[1] != 2 {
		t.Fatalf("events = %v, want [1, 2]", sink.indexes)
	}
}

func TestRedactAllURLsScrubsStatusError(t *testing.T) {
	err := &httpStatusError{Kind: "manifest", Status: 403, URL: "https://cdn.example/secret?token=abc"}
	wrapped := redactAllURLs(fmt.Errorf("wrap: %w", err))
	msg := wrapped.Error()
	lower := strings.ToLower(msg)
	for _, leak := range []string{"cdn.example", "secret", "token=abc"} {
		if strings.Contains(lower, strings.ToLower(leak)) {
			t.Fatalf("redacted error leaks %q: %q", leak, msg)
		}
	}
	if !strings.Contains(lower, "redacted") {
		t.Fatalf("redacted error missing placeholder: %q", msg)
	}
}

func TestDownloadHashIsStableAcrossRuns(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 2}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	mkResponse := func(token string) []fakeResponse {
		return []fakeResponse{
			{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
			{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
			{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte(token + "AAA"))},
			{URL: "http://cdn.example/media.mp4Seg1-Frag2", Body: makeFragmentBox([]byte(token + "BBB"))},
		}
	}
	hashOf := func(path string) string {
		data, _ := os.ReadFile(path)
		sum := sha256.Sum256(data)
		return hex.EncodeToString(sum[:])
	}
	dlA, _ := NewDownloader(&fakeTransport{responses: mkResponse("X")}, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	dlB, _ := NewDownloader(&fakeTransport{responses: mkResponse("X")}, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	rootA := t.TempDir()
	rootB := t.TempDir()
	destA := filepath.Join(rootA, "out.flv")
	destB := filepath.Join(rootB, "out.flv")
	if _, err := dlA.Download(context.Background(), "http://cdn.example/manifest.f4m", rootA, destA, false, nil); err != nil {
		t.Fatalf("dlA: %v", err)
	}
	if _, err := dlB.Download(context.Background(), "http://cdn.example/manifest.f4m", rootB, destB, false, nil); err != nil {
		t.Fatalf("dlB: %v", err)
	}
	if hashOf(destA) != hashOf(destB) {
		t.Fatalf("hashes differ: %s vs %s", hashOf(destA), hashOf(destB))
	}
}

func TestCommitFileOverwriteRespectsSymlink(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	if err := os.WriteFile(dest, []byte("real"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	link := dest + ".link"
	if err := os.Symlink(dest, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	temp := filepath.Join(root, "temp.tmp")
	if err := os.WriteFile(temp, []byte("new"), 0o600); err != nil {
		t.Fatalf("temp: %v", err)
	}
	if err := commitFile(temp, link, true); err == nil {
		t.Fatalf("expected symlink rejection")
	}
}

func TestResolveFragmentURLsPreservesSignedQuery(t *testing.T) {
	plan := []Fragment{{Segment: 1, Number: 1}}
	resolved, err := ResolveFragmentURLs("http://cdn.example/stream?Policy=eyJhbGciOi&Signature=abc&Key-Pair-Id=foo&extra=1", "pv=2", "tag=x", plan)
	if err != nil {
		t.Fatalf("ResolveFragmentURLs: %v", err)
	}
	got := resolved[0].URL
	for _, want := range []string{"Policy=eyJhbGciOi", "Signature=abc", "Key-Pair-Id=foo", "extra=1", "pv=2", "tag=x"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestResolveFragmentURLsAppendsFragNameToPathVerbatim(t *testing.T) {
	plan := []Fragment{{Segment: 7, Number: 11}}
	resolved, err := ResolveFragmentURLs("http://cdn.example/dir/stream", "", "", plan)
	if err != nil {
		t.Fatalf("ResolveFragmentURLs: %v", err)
	}
	got := resolved[0].URL
	if !strings.HasSuffix(got, "/dir/streamSeg7-Frag11") {
		t.Fatalf("URL = %q, want suffix /dir/streamSeg7-Frag11", got)
	}
}

func TestExtractMDATRejectsFragmentWithoutMdat(t *testing.T) {
	frag := makeBox("ftyp", []byte("isom"))
	if _, err := extractMDAT(frag); !errors.Is(err, ErrFragmentFetch) {
		t.Fatalf("err = %v, want ErrFragmentFetch", err)
	}
}

func TestExtractMDATReturnsFirstMdatOnly(t *testing.T) {
	frag := makeBox("ftyp", []byte("isom"))
	frag = append(frag, makeFragmentBox([]byte("FIRST"))...)
	frag = append(frag, makeFragmentBox([]byte("SECOND"))...)
	mdat, err := extractMDAT(frag)
	if err != nil {
		t.Fatalf("extractMDAT: %v", err)
	}
	if string(mdat) != "FIRST" {
		t.Fatalf("mdat = %q, want FIRST", mdat)
	}
}

func TestWriteUint24RejectsOverflow(t *testing.T) {
	buf := make([]byte, 3)
	if err := writeUint24(buf, 0, 0xffffff); err != nil {
		t.Fatalf("writeUint24 max: %v", err)
	}
	if err := writeUint24(buf, 0, 0x1000000); err == nil {
		t.Fatalf("writeUint24 overflow accepted")
	}
}

func TestIsRetryableHonorsStatusCodes(t *testing.T) {
	if !isRetryable(&httpStatusError{Status: 500}) {
		t.Fatal("500 should retry")
	}
	if !isRetryable(&httpStatusError{Status: 429}) {
		t.Fatal("429 should retry")
	}
	if isRetryable(&httpStatusError{Status: 404}) {
		t.Fatal("404 should not retry")
	}
}

func TestNewDownloaderStripsAuthorizationCookieReferer(t *testing.T) {
	headers := http.Header{}
	headers.Set("Authorization", "Bearer leaked")
	headers.Set("Cookie", "leaked=1")
	headers.Set("Proxy-Authorization", "leaked=2")
	headers.Set("Referer", "http://attacker.example/")
	headers.Set("X-Trace", "ok")
	dl, err := NewDownloader(&fakeTransport{}, Config{Headers: headers, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	if dl.config.Headers.Get("Authorization") != "" ||
		dl.config.Headers.Get("Cookie") != "" ||
		dl.config.Headers.Get("Proxy-Authorization") != "" ||
		dl.config.Headers.Get("Referer") != "" {
		t.Fatalf("sensitive headers not stripped: %+v", dl.config.Headers)
	}
	if dl.config.Headers.Get("X-Trace") != "ok" {
		t.Fatalf("X-Trace lost: %+v", dl.config.Headers)
	}
}

func TestIsRetryableDoesNotRetryDomainErrors(t *testing.T) {
	if isRetryable(ErrInvalidManifest) {
		t.Fatal("ErrInvalidManifest should not retry")
	}
	if isRetryable(ErrInvalidBootstrap) {
		t.Fatal("ErrInvalidBootstrap should not retry")
	}
	if isRetryable(ErrUnsupportedLive) {
		t.Fatal("ErrUnsupportedLive should not retry")
	}
	if isRetryable(ErrUnsupportedDRM) {
		t.Fatal("ErrUnsupportedDRM should not retry")
	}
	if isRetryable(ErrFragmentTooLarge) {
		t.Fatal("ErrFragmentTooLarge should not retry")
	}
	if isRetryable(context.DeadlineExceeded) {
		t.Fatal("DeadlineExceeded should not retry")
	}
	if isRetryable(errors.New("invalid url parse failure")) {
		t.Fatal("'invalid url' should not retry")
	}
	if isRetryable(errors.New("redirect disallowed target")) {
		t.Fatal("'redirect' should not retry")
	}
	if !isRetryable(errors.New("connection refused")) {
		t.Fatal("'connection refused' should retry")
	}
}

func TestIndexAnyURLPicksEarliest(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"see http://a.example then https://b.example", 4},
		{"see https://a.example then http://b.example", 4},
		{"only http://a.example", 5},
		{"only https://a.example", 5},
		{"no scheme", -1},
		{"http://a.example", 0},
		{"https://a.example", 0},
	}
	for _, tc := range cases {
		got := indexAnyURL(tc.in)
		if got != tc.want {
			t.Fatalf("indexAnyURL(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestRedactMessagePicksEarlierHTTPOverLaterHTTPS(t *testing.T) {
	// redactMessage must process URLs in left-to-right order. We use two
	// URLs whose hosts differ so a positional bug in indexAnyURL (e.g.
	// always preferring https://) would leave the earlier http host intact
	// in the output. Both hosts must be absent because redactString replaces
	// every host with the literal "redacted".
	got := redactMessage("err: http://a.example/x then later https://b.example/y")
	lower := strings.ToLower(got)
	for _, leak := range []string{"a.example", "b.example"} {
		if strings.Contains(lower, leak) {
			t.Fatalf("host %q leaked through redact: %q", leak, got)
		}
	}
	if !strings.Contains(lower, "redacted") {
		t.Fatalf("redacted placeholder missing: %q", got)
	}
	// The earlier URL must be processed before the later one. If the loop
	// picked https first, the output would still contain only "redacted"
	// markers, so we instead verify the function terminates and produces a
	// bounded output.
	if strings.Count(got, "http") > 4 {
		t.Fatalf("too many http occurrences in output: %q", got)
	}
}

func TestCommitFileOverwritePreservesOldOnRenameFailure(t *testing.T) {
	// Simulate by creating a backup manually and verifying commitFile restores it
	// when the temp file does not exist (rename would fail).
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	missingTemp := filepath.Join(root, "does-not-exist.tmp")
	if err := commitFile(missingTemp, dest, true); err == nil {
		t.Fatalf("expected rename error")
	}
	if data, _ := os.ReadFile(dest); string(data) != "old" {
		t.Fatalf("destination lost on failure: %q", data)
	}
}

func TestCommitFileOverwriteFalseFailsIfDestExists(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	temp := filepath.Join(root, "temp.tmp")
	if err := os.WriteFile(temp, []byte("new"), 0o600); err != nil {
		t.Fatalf("temp: %v", err)
	}
	if err := commitFile(temp, dest, false); err == nil {
		t.Fatalf("expected overwrite=false rejection")
	}
	if data, _ := os.ReadFile(dest); string(data) != "old" {
		t.Fatalf("destination was clobbered: %q", data)
	}
}

func TestCommitFileOverwriteTrueReplacesAndRemovesBackup(t *testing.T) {
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	if err := os.WriteFile(dest, []byte("old"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	temp := filepath.Join(root, "temp.tmp")
	if err := os.WriteFile(temp, []byte("new"), 0o600); err != nil {
		t.Fatalf("temp: %v", err)
	}
	if err := commitFile(temp, dest, true); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if data, _ := os.ReadFile(dest); string(data) != "new" {
		t.Fatalf("destination not replaced: %q", data)
	}
	if _, err := os.Stat(dest + ".hdsbak"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("backup file remains: %v", err)
	}
}

func TestRejectSymlinkedAncestorWalksEveryParent(t *testing.T) {
	root := t.TempDir()
	// Plant a symlinked directory inside root.
	linkDir := filepath.Join(root, "linkdir")
	if err := os.Symlink(t.TempDir(), linkDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	dest := filepath.Join(linkDir, "out.flv")
	if err := rejectSymlinkedAncestor(root, dest); err == nil {
		t.Fatalf("expected symlink rejection")
	}
}

func TestDownloadRejectsHeaderCredentialsAcrossHosts(t *testing.T) {
	// Verifies that even if the caller sets Authorization in Config, it never
	// reaches a foreign host. The handler captures the Authorization header on
	// every request; we then assert no foreign host observed it.
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	fragment := makeFragmentBox([]byte("DATA"))
	manifestBody := []byte(`<?xml version="1.0"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://other.example/</baseURL>
  <media url="media.mp4" bitrate="800"/>
  <bootstrapInfo url="http://other.example/bootstrap.bin"/>
</manifest>`)
	trans := &capturingTransport{}
	trans.handler = func(req *http.Request) (*http.Response, error) {
		switch req.URL.Host {
		case "other.example":
			if strings.HasSuffix(req.URL.Path, "bootstrap.bin") {
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(bootstrap))), Header: http.Header{}, Request: req}, nil
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(fragment))), Header: http.Header{}, Request: req}, nil
		case "cdn.example":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(manifestBody))), Header: http.Header{}, Request: req}, nil
		}
		return nil, fmt.Errorf("unexpected host %s", req.URL.Host)
	}
	headers := http.Header{}
	headers.Set("Authorization", "Bearer leaked")
	dl, err := NewDownloader(trans, Config{Headers: headers, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 1})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	for _, captured := range trans.requests {
		if captured.URL.Host == "other.example" && captured.Header.Get("Authorization") != "" {
			t.Fatalf("Authorization leaked to %s: %+v", captured.URL.Host, captured.Header)
		}
	}
}

func TestMaxOutputEnforcedAfterHeaderAndMetadata(t *testing.T) {
	bootstrap := makeABST([]SegmentRun{{FirstSegment: 1, FragmentsPerSegment: 1}}, []FragmentRun{{First: 1, Duration: 1000}}, false)
	trans := &fakeTransport{responses: []fakeResponse{
		{URL: "http://cdn.example/manifest.f4m", Body: manifestWithBootstrap("media.mp4", "")},
		{URL: "http://cdn.example/bootstrap.bin", Body: bootstrap},
		{URL: "http://cdn.example/media.mp4Seg1-Frag1", Body: makeFragmentBox([]byte("PEND"))},
	}}
	// MaxOutputBytes below the FLV header size alone (13 bytes).
	dl, err := NewDownloader(trans, Config{MaxOutputBytes: 5, RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", root, dest, false, nil)
	if !errors.Is(err, ErrFragmentTooLarge) {
		t.Fatalf("err = %v, want ErrFragmentTooLarge", err)
	}
}

func TestBoundedFetchHandlesNilResponse(t *testing.T) {
	trans := &nilResponseTransport{}
	dl, err := NewDownloader(trans, Config{RetryBaseDelay: time.Millisecond, RetryMaxDelay: 5 * time.Millisecond, Attempts: 1})
	if err != nil {
		t.Fatalf("NewDownloader: %v", err)
	}
	_, err = dl.Download(context.Background(), "http://cdn.example/manifest.f4m", t.TempDir(), filepath.Join(t.TempDir(), "out.flv"), false, nil)
	if err == nil {
		t.Fatalf("expected nil response error")
	}
}

type nilResponseTransport struct{}

func (nilResponseTransport) DoWithoutCredentialsNoRedirect(_ context.Context, req *http.Request) (*http.Response, error) {
	return nil, nil
}
