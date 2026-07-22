package dash

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestParseRejectsPeriodCountBeyondConcatBoundary(t *testing.T) {
	input := []byte("<MPD>" + strings.Repeat("<Period/>", maxPeriods+1) + "</MPD>")
	if _, err := Parse("https://example.test/manifest.mpd", input); !errors.Is(err, ErrInvalidMPD) {
		t.Fatalf("Parse() error = %v", err)
	}
}

func TestParseMultiPeriodFixture(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "..", "conformance", "media", "dash")
	input, err := os.ReadFile(filepath.Join(fixtureRoot, "multi_period.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		PeriodCount       int      `json:"period_count"`
		PeriodIDs         []string `json:"period_ids"`
		RepresentationIDs []string `json:"representation_ids"`
		URLs              []string `json:"urls"`
	}
	expectedBytes, err := os.ReadFile(filepath.Join(fixtureRoot, "multi_period.expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(expectedBytes, &expected); err != nil {
		t.Fatal(err)
	}
	mpd, err := Parse("https://media.example.test/root/manifest.mpd", input)
	if err != nil {
		t.Fatal(err)
	}
	var periodIDs, representationIDs, URLs []string
	for _, representation := range mpd.Representations {
		periodIDs = append(periodIDs, representation.PeriodID)
		representationIDs = append(representationIDs, representation.ID)
		for _, segment := range representation.Segments {
			URLs = append(URLs, segment.URL)
		}
	}
	if mpd.PeriodCount != expected.PeriodCount || !reflect.DeepEqual(periodIDs, expected.PeriodIDs) ||
		!reflect.DeepEqual(representationIDs, expected.RepresentationIDs) || !reflect.DeepEqual(URLs, expected.URLs) {
		t.Fatalf("parsed MPD = %#v, period IDs = %v, representation IDs = %v, URLs = %v", mpd, periodIDs, representationIDs, URLs)
	}
}

func TestParseRecordsPeriodIdentityAndFragmentedState(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD><Period id="opening"><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v1" bandwidth="100"><SegmentList><SegmentURL media="one.m4s"/></SegmentList></Representation></AdaptationSet></Period><Period id="feature"><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v2" bandwidth="100"><BaseURL>two.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	if mpd.PeriodCount != 2 || len(mpd.Representations) != 2 {
		t.Fatalf("MPD = %#v", mpd)
	}
	first, second := mpd.Representations[0], mpd.Representations[1]
	if first.PeriodIndex != 0 || first.PeriodID != "opening" || !first.Fragmented {
		t.Fatalf("first representation = %#v", first)
	}
	if second.PeriodIndex != 1 || second.PeriodID != "feature" || second.Fragmented {
		t.Fatalf("second representation = %#v", second)
	}
}

func TestSelectMultiPeriodChoosesHighestCommonSignature(t *testing.T) {
	representation := func(period int, id string, bandwidth int64, segment string) Representation {
		return Representation{
			ID: id, PeriodIndex: period, Fragmented: true, ContentType: "video",
			MimeType: "video/mp4", Codecs: "avc1.4d401f", Bandwidth: bandwidth,
			Width: 1280, Height: 720, Segments: []Segment{{URL: segment}},
		}
	}
	mpd := MPD{PeriodCount: 2, Representations: []Representation{
		representation(0, "p0-low", 100, "p0-low"),
		representation(0, "p0-common", 200, "p0-common"),
		representation(0, "p0-only", 300, "p0-only"),
		representation(1, "p1-low", 100, "p1-low"),
		representation(1, "p1-common", 200, "p1-common"),
	}}
	selected, err := selectRepresentations(mpd)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Bandwidth != 200 {
		t.Fatalf("selected = %#v", selected)
	}
	var URLs []string
	for _, segment := range selected[0].Segments {
		URLs = append(URLs, segment.URL)
	}
	if !reflect.DeepEqual(URLs, []string{"p0-common", "p1-common"}) {
		t.Fatalf("URLs = %v", URLs)
	}
}

func TestSelectMultiPeriodRejectsUnsafeCombinations(t *testing.T) {
	base := Representation{Fragmented: true, ContentType: "video", MimeType: "video/mp4", Bandwidth: 100, Width: 640, Height: 360, Segments: []Segment{{URL: "segment"}}}
	for _, test := range []struct {
		name string
		mpd  MPD
	}{
		{
			name: "dynamic",
			mpd:  MPD{Dynamic: true, PeriodCount: 2, Representations: []Representation{base}},
		},
		{
			name: "codec mismatch",
			mpd: MPD{PeriodCount: 2, Representations: []Representation{
				withPeriodAndCodec(base, 0, "avc1"), withPeriodAndCodec(base, 1, "hev1"),
			}},
		},
		{
			name: "language mismatch",
			mpd: MPD{PeriodCount: 2, Representations: []Representation{
				withPeriodAndLanguage(base, 0, "en"), withPeriodAndLanguage(base, 1, "fr"),
			}},
		},
		{
			name: "unfragmented",
			mpd: MPD{PeriodCount: 2, Representations: []Representation{
				withPeriodAndFragmented(base, 0, false), withPeriodAndFragmented(base, 1, false),
			}},
		},
		{
			name: "empty period",
			mpd:  MPD{PeriodCount: 2, Representations: []Representation{withPeriodAndCodec(base, 0, "avc1")}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := selectRepresentations(test.mpd); !errors.Is(err, ErrUnsupportedAddressing) {
				t.Fatalf("selectRepresentations() error = %v", err)
			}
		})
	}
}

func TestDownloadMultiPeriodConcatenatesFragmentsInManifestOrder(t *testing.T) {
	server := multiPeriodServer(t, nil)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	result, err := NewDownloader(transport, Config{FragmentConcurrency: 1}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MultiPeriod || result.MergeRequired || len(result.Tracks) != 1 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Tracks[0].PeriodDownloads) != 2 {
		t.Fatalf("period downloads = %#v", result.Tracks[0].PeriodDownloads)
	}
	var contents []byte
	for _, download := range result.Tracks[0].PeriodDownloads {
		period, readErr := os.ReadFile(download.Path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		contents = append(contents, period...)
	}
	if string(contents) != "P1-INITP1-MEDIAP2-INITP2-MEDIA" {
		t.Fatalf("contents = %q", contents)
	}
}

func TestDownloadMultiPeriodFailureDoesNotPublishTrack(t *testing.T) {
	server := multiPeriodServer(t, func(writer http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path == "/p2-media.m4s" {
			writer.WriteHeader(http.StatusBadGateway)
			return true
		}
		return false
	})
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "failed.mp4")
	_, err := NewDownloader(transport, Config{FragmentConcurrency: 1, Attempts: 1}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, destination, false, nil)
	if err == nil {
		t.Fatal("expected later-period failure")
	}
	for period := 0; period < 2; period++ {
		if _, statErr := os.Stat(fmt.Sprintf("%s.video.period-%04d", destination, period)); !os.IsNotExist(statErr) {
			t.Fatalf("period %d track should not be published: %v", period, statErr)
		}
	}
}

func TestDownloadMultiPeriodCancellationDoesNotPublishTrack(t *testing.T) {
	server := multiPeriodServer(t, func(_ http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/p2-media.m4s" {
			return false
		}
		<-request.Context().Done()
		return true
	})
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cancelled.mp4")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{FragmentConcurrency: 1, Attempts: 1}).Download(
		ctx, server.URL+"/manifest.mpd", root, destination, false, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download() error = %v", err)
	}
	for period := 0; period < 2; period++ {
		if _, statErr := os.Stat(fmt.Sprintf("%s.video.period-%04d", destination, period)); !os.IsNotExist(statErr) {
			t.Fatalf("period %d track should not be published: %v", period, statErr)
		}
	}
}

func withPeriodAndCodec(representation Representation, period int, codec string) Representation {
	representation.PeriodIndex = period
	representation.Codecs = codec
	return representation
}

func withPeriodAndFragmented(representation Representation, period int, fragmented bool) Representation {
	representation.PeriodIndex = period
	representation.Fragmented = fragmented
	return representation
}

func withPeriodAndLanguage(representation Representation, period int, language string) Representation {
	representation.PeriodIndex = period
	representation.Language = language
	return representation
}

func multiPeriodServer(t *testing.T, override func(http.ResponseWriter, *http.Request) bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if override != nil && override(writer, request) {
			return
		}
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static"><Period id="p1"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1" bandwidth="100"><Representation id="v1" bandwidth="100"><SegmentList><Initialization sourceURL="p1-init.mp4"/><SegmentURL media="p1-media.m4s"/></SegmentList></Representation></AdaptationSet></Period><Period id="p2"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="v2" bandwidth="100"><SegmentList><Initialization sourceURL="p2-init.mp4"/><SegmentURL media="p2-media.m4s"/></SegmentList></Representation></AdaptationSet></Period></MPD>`)
		case "/p1-init.mp4":
			_, _ = writer.Write([]byte("P1-INIT"))
		case "/p1-media.m4s":
			_, _ = writer.Write([]byte("P1-MEDIA"))
		case "/p2-init.mp4":
			_, _ = writer.Write([]byte("P2-INIT"))
		case "/p2-media.m4s":
			_, _ = writer.Write([]byte("P2-MEDIA"))
		default:
			http.NotFound(writer, request)
		}
	}))
}
