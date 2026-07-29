package ytdlp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hds"
)

// makeTestBox wraps payload as an ISO box with the given four-cc.
func makeTestBox(kind string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], kind)
	copy(out[8:], payload)
	return out
}

// makeTestABST builds an ABST payload with one ASRT (3 fragments) and one
// AFRT (3 fragments). Deterministic and self-contained.
func makeTestABST() []byte {
	return makeTestABSTWithLive(false)
}

func makeTestABSTWithLive(live bool) []byte {
	body := make([]byte, 0, 128)
	// version + flags + bootstrapinfo version
	body = append(body, 0, 0, 0, 0, 0, 0, 0, 0)
	// profile flags; bit 0x20 is the HDS live marker.
	var profileFlags byte
	if live {
		profileFlags = 0x20
	}
	body = append(body, profileFlags)
	// timescale
	body = append(body, 0, 0, 0, 0x03)
	// current media time + smpte offset (8 + 8)
	body = append(body, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	// movie identifier
	body = append(body, 0x00)
	// server count + server entries (none)
	body = append(body, 0x00)
	// quality count + quality entries (none)
	body = append(body, 0x00)
	// drm data
	body = append(body, 0x00)
	// metadata
	body = append(body, 0x00)
	// segments count = 1
	body = append(body, 0x01)
	// ASRT body
	asrt := make([]byte, 0, 16)
	asrt = append(asrt, 0, 0, 0, 0) // version+flags
	asrt = append(asrt, 0x00)       // quality count
	asrt = append(asrt, 0, 0, 0, 1) // segment run count = 1
	asrt = append(asrt, 0, 0, 0, 1) // first segment = 1
	asrt = append(asrt, 0, 0, 0, 3) // fragments per segment = 3
	body = append(body, makeTestBox("asrt", asrt)...)
	// fragments count = 1
	body = append(body, 0x01)
	// AFRT body
	afrt := make([]byte, 0, 32)
	afrt = append(afrt, 0, 0, 0, 0)    // version+flags
	afrt = append(afrt, 0, 0, 0, 0x03) // timescale
	afrt = append(afrt, 0x00)          // quality count
	afrt = append(afrt, 0, 0, 0, 3)    // fragment count = 3
	// Three fragments: first=1,ts=0,dur=1000; first=2,ts=1000,dur=1000; first=3,ts=2000,dur=1000
	for i := 0; i < 3; i++ {
		afrt = append(afrt, 0, 0, 0, byte(i+1)) // first
		afrt = append(afrt, 0, 0, 0, 0, 0, 0, 0, 0)
		afrt = append(afrt, 0, 0, 0x03, 0xE8) // duration
	}
	body = append(body, makeTestBox("afrt", afrt)...)
	return makeTestBox("abst", body)
}

// makeTestFragment returns an mdat box wrapping the supplied payload bytes.
func makeTestFragment(payload string) []byte {
	return makeTestBox("mdat", []byte(payload))
}

// hdsTestRoundTripper serves the deterministic fixture set used by every HDS
// product test. It tracks every request URL so tests can assert fetch
// ordering and credential scope.
type hdsTestRoundTripper struct {
	calls []string
}

func (rt *hdsTestRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls = append(rt.calls, req.URL.String())
	body := ""
	contentType := "application/octet-stream"
	switch {
	case strings.HasSuffix(req.URL.Path, "/manifest.f4m"):
		body = `<?xml version="1.0"?>
<manifest xmlns="http://ns.adobe.com/f4m/1.0">
  <baseURL>http://cdn.example.invalid/</baseURL>
  <media url="media.mp4" bitrate="800" width="640" height="360"/>
  <bootstrapInfo url="http://cdn.example.invalid/bootstrap.bin"/>
</manifest>`
		contentType = "application/xml"
	case strings.HasSuffix(req.URL.Path, "/bootstrap.bin"):
		body = string(makeTestABST())
	case strings.HasSuffix(req.URL.Path, "Seg1-Frag1"):
		body = string(makeTestFragment("AAA"))
	case strings.HasSuffix(req.URL.Path, "Seg1-Frag2"):
		body = string(makeTestFragment("BBB"))
	case strings.HasSuffix(req.URL.Path, "Seg1-Frag3"):
		body = string(makeTestFragment("CCC"))
	default:
		return nil, errors.New("unexpected HDS request: " + req.URL.Redacted())
	}
	header := make(http.Header)
	header.Set("Content-Type", contentType)
	header.Set("Content-Length", strconv.Itoa(len(body)))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// hdsTestRoundTripperFunc adapts a closure to http.RoundTripper.
type hdsTestRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f hdsTestRoundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// flvHeaderConst is the 13-byte FLV header we expect at the start of every
// successful download. The values match yt-dlp's write_flv_header.
const flvHeaderConst = "FLV\x01\x05\x00\x00\x00\x09\x00\x00\x00\x00"

// newHDSOperation wires a fresh operation for tests. It accepts a custom
// roundTripper; callers can compose behavior by wrapping hdsTestRoundTripper.
func newHDSOperation(t *testing.T, rt http.RoundTripper) (*operation, string, string) {
	t.Helper()
	client, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	root := t.TempDir()
	dest := filepath.Join(root, "out.flv")
	op := &operation{
		client:        NewClient(),
		request:       Request{OutputDir: root},
		transport:     client,
		registry:      productRegistry(),
		rootExtractor: new(string),
	}
	return op, root, dest
}

// TestProductHDSSelectedDownloadDispatches exercises the f4m case in the
// product download dispatch: a selection with Protocol="f4m" must reach the
// HDS downloader and produce a valid FLV byte sequence on disk.
func TestProductHDSSelectedDownloadDispatches(t *testing.T) {
	rt := &hdsTestRoundTripper{}
	op, root, dest := newHDSOperation(t, rt)
	selection := mediaformat.Selection{
		ID:       "f4m-vod",
		URL:      "http://cdn.example.invalid/manifest.f4m",
		Protocol: "f4m",
		Headers:  http.Header{},
		TBR:      800,
	}
	path, _, err := op.downloadSelection(context.Background(), selection, root, dest, nil)
	if err != nil {
		t.Fatalf("downloadSelection: %v", err)
	}
	if path == "" {
		t.Fatalf("empty result path")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if string(data[:len(flvHeaderConst)]) != flvHeaderConst {
		t.Fatalf("FLV header mismatch: %q", data[:len(flvHeaderConst)])
	}
	if !strings.HasSuffix(string(data[len(flvHeaderConst):]), "AAABBBCCC") {
		t.Fatalf("FLV body missing expected fragment payload: %q", data[len(flvHeaderConst):])
	}
	if len(rt.calls) < 5 {
		t.Fatalf("expected at least 5 HDS fetches, got %d", len(rt.calls))
	}
}

// TestProductHDSCleanupOnFailure verifies that a persistent 5xx on the
// bootstrap fetch leaves no destination file behind and surfaces the error.
func TestProductHDSCleanupOnFailure(t *testing.T) {
	rt := hdsTestRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "/bootstrap.bin") {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     http.Header{},
				Request:    req,
			}, nil
		}
		return (&hdsTestRoundTripper{}).RoundTrip(req)
	})
	op, root, dest := newHDSOperation(t, rt)
	selection := mediaformat.Selection{
		ID:       "f4m-vod",
		URL:      "http://cdn.example.invalid/manifest.f4m",
		Protocol: "f4m",
		Headers:  http.Header{},
	}
	_, _, err := op.downloadSelection(context.Background(), selection, root, dest, nil)
	if err == nil {
		t.Fatalf("expected bootstrap failure error")
	}
	if _, statErr := os.Stat(dest); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination should be cleaned up on failure, got %v", statErr)
	}
}

// TestProductHDSF4MDispatchHandlesAltProtocol also accepts "f4m_native".
func TestProductHDSF4MDispatchHandlesAltProtocol(t *testing.T) {
	rt := &hdsTestRoundTripper{}
	op, root, dest := newHDSOperation(t, rt)
	selection := mediaformat.Selection{
		ID:       "f4m-native",
		URL:      "http://cdn.example.invalid/manifest.f4m",
		Protocol: "f4m_native",
		Headers:  http.Header{},
	}
	_, _, err := op.downloadSelection(context.Background(), selection, root, dest, nil)
	if err != nil {
		t.Fatalf("downloadSelection: %v", err)
	}
}

type raiHDSBridgeRoundTripper struct {
	pageURL     string
	manifestURL string
	manifest    string
	bootstrap   []byte
	fragment    []byte
	calls       []string
	seenAuth    []string
	seenHeaders map[string]http.Header
	malformed   bool
	drm         bool
	live        bool
}

func (rt *raiHDSBridgeRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls = append(rt.calls, req.URL.String())
	rt.seenAuth = append(rt.seenAuth, req.Header.Get("Authorization")+"|"+req.Header.Get("Cookie"))
	if rt.seenHeaders == nil {
		rt.seenHeaders = make(map[string]http.Header)
	}
	rt.seenHeaders[req.URL.String()] = req.Header.Clone()
	body := []byte(nil)
	contentType := "application/octet-stream"
	switch {
	case req.URL.String() == rt.pageURL+".json":
		body = []byte(`{"id":"ContentItem-cb27157f-9dd0-4aee-b788-b1f67643a391","video":{"content_url":"https://relinker.rai.it/resolve?sig=SIGNED"}}`)
		contentType = "application/json"
	case req.URL.Host == "relinker.rai.it":
		body = []byte(`<root><url type="content">https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&amp;dup=one&amp;dup=two</url><is_live>N</is_live></root>`)
		contentType = "application/xml"
	case req.URL.String() == rt.manifestURL:
		if rt.malformed {
			body = []byte(`<manifest xmlns="http://ns.adobe.com/f4m/1.0"><media`)
		} else if rt.drm {
			body = []byte(`<manifest xmlns="http://ns.adobe.com/f4m/1.0"><drmAdditionalHeader>encrypted</drmAdditionalHeader><media url="media.mp4" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`)
		} else {
			body = []byte(rt.manifest)
		}
		contentType = "application/xml"
	case strings.HasSuffix(req.URL.Path, "/bootstrap.bin"):
		if rt.bootstrap == nil {
			rt.bootstrap = makeTestABSTWithLive(rt.live)
		}
		body = rt.bootstrap
	case strings.HasSuffix(req.URL.Path, "media.mp4Seg1-Frag1"):
		body = rt.fragment
	case strings.HasSuffix(req.URL.Path, "media.mp4Seg1-Frag2"):
		body = makeTestFragment("RAI-HDS-2")
	case strings.HasSuffix(req.URL.Path, "media.mp4Seg1-Frag3"):
		body = makeTestFragment("RAI-HDS-3")
	default:
		return nil, errors.New("unexpected Rai/HDS bridge request: " + req.URL.Redacted())
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {contentType}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    req,
	}, nil
}

func TestProductRaiF4MExtractionBridgesIntoHDSAndAssemblesFLV(t *testing.T) {
	pageURL := "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391"
	manifestURL := "https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&dup=one&dup=two&hdcore=3.7.0&plugin=aasp-3.7.0.39.44"
	rt := &raiHDSBridgeRoundTripper{
		pageURL:     pageURL,
		manifestURL: manifestURL,
		manifest:    `<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4?frag=one&amp;frag=two" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`,
		bootstrap:   makeTestABST(),
		fragment:    makeTestFragment("RAI-HDS"),
	}
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	extracted, err := extractor.NewRaiPlay().Extract(context.Background(), extractor.Request{
		URL: pageURL + ".html", Transport: transport,
	})
	if err != nil {
		t.Fatalf("Rai extraction: %v", err)
	}
	selection, err := mediaformat.Best(extracted.Info)
	if err != nil {
		t.Fatalf("format selection: %v", err)
	}
	if selection.Protocol != "f4m_native" || selection.ID != "hds" || selection.Ext != "flv" {
		t.Fatalf("selection = %+v, want hds/f4m_native/flv", selection)
	}
	if selection.URL != manifestURL {
		t.Fatalf("selected manifest URL = %q, want %q", selection.URL, manifestURL)
	}
	selection.Headers = http.Header{
		"Authorization":       {"rai-auth-sentinel"},
		"Cookie":              {"rai-cookie-sentinel"},
		"Proxy-Authorization": {"rai-proxy-sentinel"},
		"Referer":             {"https://page.example.test/rai"},
	}

	root := t.TempDir()
	destination := filepath.Join(root, "rai.flv")
	op := &operation{
		client:    NewClient(),
		request:   Request{OutputDir: root, Overwrite: true, Downloader: DownloaderOptions{Attempts: 1}},
		transport: transport,
	}
	path, _, err := op.downloadSelection(context.Background(), selection, root, destination, nil)
	if err != nil {
		t.Fatalf("HDS product download: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read FLV: %v", err)
	}
	if !strings.HasPrefix(string(data), flvHeaderConst) || !strings.HasSuffix(string(data), "RAI-HDSRAI-HDS-2RAI-HDS-3") {
		t.Fatalf("assembled FLV = %q, want FLV header and RAI-HDS payload", data)
	}
	wantFragmentURLs := []string{
		"https://cdn.rai.test/stream/media.mp4Seg1-Frag1?frag=one&frag=two",
		"https://cdn.rai.test/stream/media.mp4Seg1-Frag2?frag=one&frag=two",
		"https://cdn.rai.test/stream/media.mp4Seg1-Frag3?frag=one&frag=two",
	}
	if !containsURL(rt.calls, manifestURL) {
		t.Fatalf("HDS calls = %v, want signed manifest", rt.calls)
	}
	for _, want := range wantFragmentURLs {
		if !containsURL(rt.calls, want) {
			t.Fatalf("HDS calls = %v, want signed duplicate query on %s", rt.calls, want)
		}
	}
	for _, requestURL := range append([]string{manifestURL}, wantFragmentURLs...) {
		headers, ok := rt.seenHeaders[requestURL]
		if !ok {
			t.Fatalf("missing recorded HDS request headers for %s", requestURL)
		}
		for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
			if got := headers.Get(key); got != "" {
				t.Fatalf("%s leaked to HDS URL %s: %q", key, requestURL, got)
			}
		}
	}
}

func TestProductRaiF4MBridgeFailureCleansDestinationAndPreservesCategory(t *testing.T) {
	pageURL := "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391"
	manifestURL := "https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&dup=one&dup=two&hdcore=3.7.0&plugin=aasp-3.7.0.39.44"
	rt := &raiHDSBridgeRoundTripper{
		pageURL:     pageURL,
		manifestURL: manifestURL,
		manifest:    `<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`,
		bootstrap:   makeTestABST(),
		fragment:    makeTestFragment("never-written"),
		malformed:   true,
	}
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	extracted, err := extractor.NewRaiPlay().Extract(context.Background(), extractor.Request{URL: pageURL + ".html", Transport: transport})
	if err != nil {
		t.Fatalf("Rai extraction: %v", err)
	}
	selection, err := mediaformat.Best(extracted.Info)
	if err != nil {
		t.Fatalf("format selection: %v", err)
	}
	root := t.TempDir()
	destination := filepath.Join(root, "rai-failed.flv")
	op := &operation{
		client:    NewClient(),
		request:   Request{OutputDir: root, Overwrite: true, Downloader: DownloaderOptions{Attempts: 1}},
		transport: transport,
	}
	_, _, err = op.downloadSelection(context.Background(), selection, root, destination, nil)
	if !errors.Is(err, hds.ErrInvalidManifest) {
		t.Fatalf("error = %v, want ErrInvalidManifest", err)
	}
	if !IsCategory(categorized("Rai HDS bridge", err), ErrorInvalidInput) {
		t.Fatalf("error category = %v, want invalid_input", categorized("Rai HDS bridge", err))
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("failed destination exists: %v", statErr)
	}
}

func extractRaiBridgeSelection(t *testing.T, rt *raiHDSBridgeRoundTripper) (*network.Client, mediaformat.Selection) {
	t.Helper()
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatalf("network.New: %v", err)
	}
	extracted, err := extractor.NewRaiPlay().Extract(context.Background(), extractor.Request{
		URL: rt.pageURL + ".html", Transport: transport,
	})
	if err != nil {
		t.Fatalf("Rai extraction: %v", err)
	}
	selection, err := mediaformat.Best(extracted.Info)
	if err != nil {
		t.Fatalf("format selection: %v", err)
	}
	return transport, selection
}

func TestProductRaiF4MBridgeCancellationCleansDestination(t *testing.T) {
	pageURL := "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391"
	manifestURL := "https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&dup=one&dup=two&hdcore=3.7.0&plugin=aasp-3.7.0.39.44"
	rt := &raiHDSBridgeRoundTripper{
		pageURL:     pageURL,
		manifestURL: manifestURL,
		manifest:    `<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`,
		bootstrap:   makeTestABST(),
		fragment:    makeTestFragment("never-written"),
	}
	transport, selection := extractRaiBridgeSelection(t, rt)
	root := t.TempDir()
	destination := filepath.Join(root, "rai-canceled.flv")
	op := &operation{
		client:    NewClient(),
		request:   Request{OutputDir: root, Overwrite: true, Downloader: DownloaderOptions{Attempts: 1}},
		transport: transport,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := op.downloadSelection(ctx, selection, root, destination, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if containsURL(rt.calls, manifestURL) {
		t.Fatalf("manifest fetched after cancellation: %v", rt.calls)
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("canceled destination exists: %v", statErr)
	}
}

func TestProductRaiF4MBridgeSizeBoundCleansDestinationAndPreservesCategory(t *testing.T) {
	pageURL := "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391"
	manifestURL := "https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&dup=one&dup=two&hdcore=3.7.0&plugin=aasp-3.7.0.39.44"
	rt := &raiHDSBridgeRoundTripper{
		pageURL:     pageURL,
		manifestURL: manifestURL,
		manifest:    `<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`,
		bootstrap:   makeTestABST(),
		fragment:    makeTestFragment("RAI-HDS"),
	}
	transport, selection := extractRaiBridgeSelection(t, rt)
	root := t.TempDir()
	destination := filepath.Join(root, "rai-bounded.flv")
	op := &operation{
		client: NewClient(),
		request: Request{OutputDir: root, Overwrite: true, Downloader: DownloaderOptions{
			Attempts: 1, MaxBytes: 20, MaxSegmentBytes: 64,
		}},
		transport: transport,
	}
	_, _, err := op.downloadSelection(context.Background(), selection, root, destination, nil)
	if !errors.Is(err, hds.ErrFragmentTooLarge) {
		t.Fatalf("error = %v, want ErrFragmentTooLarge", err)
	}
	if !IsCategory(categorized("Rai HDS bridge", err), ErrorInvalidInput) {
		t.Fatalf("error category = %v, want invalid_input", categorized("Rai HDS bridge", err))
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("bounded destination exists: %v", statErr)
	}
}

func TestProductRaiF4MBridgePreservesLiveAndDRMRestrictions(t *testing.T) {
	for _, tc := range []struct {
		name         string
		drm, live    bool
		wantSentinel error
	}{
		{name: "drm", drm: true, wantSentinel: hds.ErrUnsupportedDRM},
		{name: "live", live: true, wantSentinel: hds.ErrUnsupportedLive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pageURL := "https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391"
			manifestURL := "https://cdn.rai.test/stream/manifest.f4m?token=SIGNED%2BVALUE&dup=one&dup=two&hdcore=3.7.0&plugin=aasp-3.7.0.39.44"
			rt := &raiHDSBridgeRoundTripper{
				pageURL:     pageURL,
				manifestURL: manifestURL,
				manifest:    `<?xml version="1.0"?><manifest xmlns="http://ns.adobe.com/f4m/1.0"><media url="media.mp4" bitrate="800"/><bootstrapInfo url="https://cdn.rai.test/stream/bootstrap.bin"/></manifest>`,
				drm:         tc.drm,
				live:        tc.live,
				fragment:    makeTestFragment("never-written"),
			}
			transport, err := network.New(network.Config{RoundTripper: rt})
			if err != nil {
				t.Fatalf("network.New: %v", err)
			}
			extracted, err := extractor.NewRaiPlay().Extract(context.Background(), extractor.Request{URL: pageURL + ".html", Transport: transport})
			if err != nil {
				t.Fatalf("Rai extraction: %v", err)
			}
			selection, err := mediaformat.Best(extracted.Info)
			if err != nil {
				t.Fatalf("format selection: %v", err)
			}
			root := t.TempDir()
			destination := filepath.Join(root, tc.name+".flv")
			op := &operation{
				client:    NewClient(),
				request:   Request{OutputDir: root, Overwrite: true, Downloader: DownloaderOptions{Attempts: 1}},
				transport: transport,
			}
			_, _, err = op.downloadSelection(context.Background(), selection, root, destination, nil)
			if !errors.Is(err, tc.wantSentinel) {
				t.Fatalf("error = %v, want %v", err, tc.wantSentinel)
			}
			if !IsCategory(categorized("Rai HDS bridge", err), ErrorUnsupported) {
				t.Fatalf("error category = %v, want unsupported", categorized("Rai HDS bridge", err))
			}
			if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("failed destination exists: %v", statErr)
			}
		})
	}
}

func containsURL(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// TestProductHDSCategorizesLiveBootstrap verifies that hds.ErrUnsupportedLive
// is surfaced as ErrorUnsupported so callers see a stable user-visible
// category.
func TestProductHDSCategorizesLiveBootstrap(t *testing.T) {
	categorizedErr := categorized("hds download", hds.ErrUnsupportedLive)
	if !IsCategory(categorizedErr, ErrorUnsupported) {
		t.Fatalf("expected ErrorUnsupported category, got %v", categorizedErr)
	}
}

// TestProductHDSCategorizesInvalidManifest verifies that hds.ErrInvalidManifest
// is surfaced as ErrorInvalidInput.
func TestProductHDSCategorizesInvalidManifest(t *testing.T) {
	categorizedErr := categorized("hds download", hds.ErrInvalidManifest)
	if !IsCategory(categorizedErr, ErrorInvalidInput) {
		t.Fatalf("expected ErrorInvalidInput category, got %v", categorizedErr)
	}
}

// TestProductHDSCategorizesUnsupportedVariants asserts the exact user-visible
// category for every sentinel exposed by the hds package. Live, DRM, and
// empty manifests surface as ErrorUnsupported (the user cannot recover by
// retrying with the same URL); invalid manifests, bootstrap/config errors,
// fragment-size and count limits, unsafe destinations, and media descriptor
// errors surface as ErrorInvalidInput (the user can usually recover by
// checking their inputs or trying a different selection). Misclassifying any
// of these as a network error would let structural HDS failures leak to
// user-visible retries.
func TestProductHDSCategorizesUnsupportedVariants(t *testing.T) {
	cases := []struct {
		name     string
		sentinel error
		want     ErrorCategory
	}{
		{"live", hds.ErrUnsupportedLive, ErrorUnsupported},
		{"drm", hds.ErrUnsupportedDRM, ErrorUnsupported},
		{"empty", hds.ErrUnsupportedEmpty, ErrorUnsupported},
		{"invalid-manifest", hds.ErrInvalidManifest, ErrorInvalidInput},
		{"invalid-bootstrap", hds.ErrInvalidBootstrap, ErrorInvalidInput},
		{"invalid-media", hds.ErrInvalidMedia, ErrorInvalidInput},
		{"invalid-config", hds.ErrInvalidConfig, ErrorInvalidInput},
		{"frag-too-large", hds.ErrFragmentTooLarge, ErrorInvalidInput},
		{"too-many-segs", hds.ErrTooManySegments, ErrorInvalidInput},
		{"too-many-frags", hds.ErrTooManyFragments, ErrorInvalidInput},
		{"unsafe-dest", hds.ErrUnsafeDestination, ErrorInvalidInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := categorized("hds download", tc.sentinel)
			if !IsCategory(got, tc.want) {
				t.Fatalf("hds sentinel %s not categorized as %s (raw: %v)", tc.name, tc.want, got)
			}
			// Cross-check that no other category claims it.
			for _, other := range []ErrorCategory{
				ErrorNetwork, ErrorAuthentication, ErrorSecurity,
				ErrorCancelled, ErrorInternal,
			} {
				if other == tc.want {
					continue
				}
				if IsCategory(got, other) {
					t.Fatalf("hds sentinel %s unexpectedly also categorized as %s", tc.name, other)
				}
			}
		})
	}
}

// TestBoundedFloatToInt64Boundary exercises every interesting boundary of the
// helper used to convert mediaformat.Selection.TBR (float64) into the bounded
// int64 the HDS downloader expects. The helper must never error and must
// never panic on NaN, ±Inf, or out-of-int64-range values.
func TestBoundedFloatToInt64Boundary(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want int64
	}{
		{"zero", 0, 0},
		{"negative", -1, 0},
		{"large-negative", -1e9, 0},
		{"nan", math.NaN(), 0},
		{"pinf", math.Inf(+1), math.MaxInt64},
		{"ninf", math.Inf(-1), 0},
		{"one", 1, 1},
		{"typical-tbr", 800.5, 800},
		{"max-int-as-float", float64(math.MaxInt64), math.MaxInt64},
		{"just-above-max", float64(math.MaxInt64) * 2, math.MaxInt64},
		// Float64 cannot represent MaxInt64-1 precisely; pick a value clearly
		// below the bound to verify lossy-but-bounded truncation.
		{"near-one-billion", 999_999_999.999, 999999999},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boundedFloatToInt64(tc.in)
			if got != tc.want {
				t.Fatalf("boundedFloatToInt64(%v) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}
