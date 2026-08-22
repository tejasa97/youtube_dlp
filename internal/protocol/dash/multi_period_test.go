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
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/fragment"
	"github.com/tejasa97/ytdlp-go/internal/network"
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
		PeriodStartsMS    []int64  `json:"period_starts_ms"`
		PeriodDurationsMS []int64  `json:"period_durations_ms"`
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
	var periodStartsMS, periodDurationsMS []int64
	for _, period := range mpd.Periods {
		periodStartsMS = append(periodStartsMS, period.Start.Milliseconds())
		periodDurationsMS = append(periodDurationsMS, period.Duration.Milliseconds())
	}
	for _, representation := range mpd.Representations {
		periodIDs = append(periodIDs, representation.PeriodID)
		representationIDs = append(representationIDs, representation.ID)
		for _, segment := range representation.Segments {
			URLs = append(URLs, segment.URL)
		}
	}
	if mpd.PeriodCount != expected.PeriodCount || !reflect.DeepEqual(periodIDs, expected.PeriodIDs) ||
		!reflect.DeepEqual(periodStartsMS, expected.PeriodStartsMS) || !reflect.DeepEqual(periodDurationsMS, expected.PeriodDurationsMS) ||
		!reflect.DeepEqual(representationIDs, expected.RepresentationIDs) || !reflect.DeepEqual(URLs, expected.URLs) {
		t.Fatalf("parsed MPD = %#v, period IDs = %v, starts = %v, durations = %v, representation IDs = %v, URLs = %v", mpd, periodIDs, periodStartsMS, periodDurationsMS, representationIDs, URLs)
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
	mpd.Periods = testPeriods(2)
	mpd.PresentationDuration = 2 * time.Second
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
			test.mpd.Periods = testPeriods(test.mpd.PeriodCount)
			test.mpd.PresentationDuration = time.Duration(test.mpd.PeriodCount) * time.Second
			if _, err := selectRepresentations(test.mpd); !errors.Is(err, ErrUnsupportedAddressing) {
				t.Fatalf("selectRepresentations() error = %v", err)
			}
		})
	}
}

func TestSelectMultiPeriodRejectsDiscontinuousOrUnknownTiming(t *testing.T) {
	representation := func(period int) Representation {
		return Representation{
			ID: fmt.Sprintf("v%d", period), PeriodIndex: period, Fragmented: true,
			ContentType: "video", MimeType: "video/mp4", Codecs: "avc1", Bandwidth: 100,
			Segments: []Segment{{URL: fmt.Sprintf("p%d.m4s", period)}},
		}
	}
	for _, test := range []struct {
		name    string
		periods []Period
	}{
		{"gap", []Period{{Start: 0, Duration: time.Second, TimingKnown: true}, {Start: 2 * time.Second, Duration: time.Second, TimingKnown: true}}},
		{"overlap", []Period{{Start: 0, Duration: 2 * time.Second, TimingKnown: true}, {Start: time.Second, Duration: time.Second, TimingKnown: true}}},
		{"unknown", []Period{{Start: 0}, {}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mpd := MPD{PeriodCount: 2, Periods: test.periods, Representations: []Representation{representation(0), representation(1)}}
			if _, err := selectRepresentations(mpd); !errors.Is(err, ErrUnsupportedAddressing) {
				t.Fatalf("selectRepresentations() error = %v", err)
			}
		})
	}
}

func TestParseDerivesContiguousPeriodTiming(t *testing.T) {
	mpd, err := Parse("https://example.test/manifest.mpd", []byte(`<MPD mediaPresentationDuration="PT4S"><Period id="p1" duration="PT2S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="v1" bandwidth="100"><SegmentList><SegmentURL media="p1.m4s"/></SegmentList></Representation></AdaptationSet></Period><Period id="p2"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="v2" bandwidth="100"><SegmentList><SegmentURL media="p2.m4s"/></SegmentList></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Period{
		{ID: "p1", Start: 0, Duration: 2 * time.Second, TimingKnown: true},
		{ID: "p2", Start: 2 * time.Second, Duration: 2 * time.Second, TimingKnown: true},
	}
	if !reflect.DeepEqual(mpd.Periods, want) {
		t.Fatalf("Periods = %#v; want %#v", mpd.Periods, want)
	}
	if _, err := selectRepresentations(mpd); err != nil {
		t.Fatalf("selectRepresentations(): %v", err)
	}
}

func TestSelectDynamicMultiPeriodAllowsStableFragmentedMixedAddressing(t *testing.T) {
	mpd, err := Parse("https://example.test/root/manifest.mpd", []byte(`<MPD type="dynamic" mediaPresentationDuration="PT2S"><Period id="opening" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="opening-v" bandwidth="100"><SegmentTemplate media="opening-$Number$.m4s" initialization="opening-init.mp4" duration="1"/></Representation></AdaptationSet></Period><Period id="feature" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="feature-v" bandwidth="100"><SegmentList><Initialization sourceURL="feature-init.mp4"/><SegmentURL media="feature.m4s"/></SegmentList></Representation></AdaptationSet></Period></MPD>`))
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectRepresentations(mpd)
	if err != nil {
		t.Fatalf("selectRepresentations() error = %v", err)
	}
	if len(selected) != 1 || !selected[0].Fragmented || !reflect.DeepEqual(selected[0].PeriodIDs, []string{"opening", "feature"}) || !reflect.DeepEqual(selected[0].PeriodRepresentationIDs, []string{"opening-v", "feature-v"}) {
		t.Fatalf("selected = %#v", selected)
	}
	if got := len(selected[0].PeriodSegments); got != 2 {
		t.Fatalf("period segments = %d; want 2", got)
	}
}

func TestParseMixedAddressingMultiPeriodFixtureSelectsOneSafeTrack(t *testing.T) {
	fixtureRoot := filepath.Join("..", "..", "..", "conformance", "media", "dash")
	input, err := os.ReadFile(filepath.Join(fixtureRoot, "multi_period_mixed_addressing.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	mpd, err := Parse("https://media.example.test/dash/manifest.mpd", input)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectRepresentations(mpd)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || len(selected[0].PeriodSegments) != 3 {
		t.Fatalf("selected = %#v", selected)
	}
	if !selected[0].PeriodSegments[0][0].Initialize || !selected[0].PeriodSegments[1][0].Initialize || selected[0].PeriodSegments[2][0].IndexRange == "" {
		t.Fatalf("mixed addressing segments = %#v", selected[0].PeriodSegments)
	}
}

func TestDownloadDynamicMultiPeriodAccumulatesStableSnapshots(t *testing.T) {
	var manifests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			if manifests.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, dynamicMultiPeriodMPD(true))
				return
			}
			_, _ = fmt.Fprint(writer, dynamicMultiPeriodMPD(false))
		case "/p1-init.mp4":
			_, _ = writer.Write([]byte("P1-INIT"))
		case "/p1-1.m4s":
			_, _ = writer.Write([]byte("P1-1"))
		case "/p1-2.m4s":
			_, _ = writer.Write([]byte("P1-2"))
		case "/p2-init.mp4":
			_, _ = writer.Write([]byte("P2-INIT"))
		case "/p2-1.m4s":
			_, _ = writer.Write([]byte("P2-1"))
		case "/p2-2.m4s":
			_, _ = writer.Write([]byte("P2-2"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond, FragmentConcurrency: 1}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "video.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MultiPeriod || len(result.Tracks) != 1 || len(result.Tracks[0].PeriodDownloads) != len(result.Tracks[0].Representation.PeriodSegments) {
		t.Fatalf("result = %#v", result)
	}
	seenPaths := make(map[string]struct{}, len(result.Tracks[0].PeriodDownloads))
	for _, download := range result.Tracks[0].PeriodDownloads {
		if _, exists := seenPaths[download.Path]; exists {
			t.Fatalf("duplicate period download result for %q: %#v", download.Path, result.Tracks[0].PeriodDownloads)
		}
		seenPaths[download.Path] = struct{}{}
	}
	want := []string{"P1-INITP1-1P1-2", "P2-INITP2-1P2-2"}
	for index, download := range result.Tracks[0].PeriodDownloads {
		body, readErr := os.ReadFile(download.Path)
		if readErr != nil || string(body) != want[index] {
			t.Fatalf("period %d = %q, error = %v; want %q", index, body, readErr, want[index])
		}
	}
}

func TestDownloadDynamicMultiPeriodRejectsRepresentationReplacement(t *testing.T) {
	var manifests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/manifest.mpd" {
			http.Error(writer, "unexpected media request", http.StatusInternalServerError)
			return
		}
		if manifests.Add(1) == 1 {
			_, _ = fmt.Fprint(writer, dynamicMultiPeriodMPD(true))
			return
		}
		_, _ = fmt.Fprint(writer, strings.Replace(dynamicMultiPeriodMPD(false), `id="p2-v"`, `id="replacement-v"`, 1))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "video.mp4"), false, nil)
	if !errors.Is(err, ErrUnsupportedAddressing) || !strings.Contains(err.Error(), "identity") {
		t.Fatalf("Download() error = %v", err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("output entries = %v, error = %v; want none", entries, readErr)
	}
}

func TestDownloadDynamicMultiPeriodRejectsDisjointSegmentReplacement(t *testing.T) {
	var manifests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/manifest.mpd" {
			http.Error(writer, "media must not be fetched before snapshot validation", http.StatusInternalServerError)
			return
		}
		if manifests.Add(1) == 1 {
			_, _ = fmt.Fprint(writer, dynamicMultiPeriodMPD(true))
			return
		}
		updated := strings.Replace(dynamicMultiPeriodMPD(false), `p1-1.m4s`, `p1-replacement.m4s`, 1)
		_, _ = fmt.Fprint(writer, updated)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "video.mp4")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, destination, false, nil)
	if !errors.Is(err, ErrUnsupportedAddressing) || !strings.Contains(err.Error(), "disjoint segment replacement") {
		t.Fatalf("Download() error = %v, want precise disjoint replacement error", err)
	}
	if entries, readErr := os.ReadDir(root); readErr != nil || len(entries) != 0 {
		t.Fatalf("output entries = %v, error = %v; want none", entries, readErr)
	}
}

func TestDownloadDynamicMultiPeriodSIDXFailsClosedBeforeMedia(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/manifest.mpd" {
			_, _ = fmt.Fprint(writer, `<MPD type="dynamic" mediaPresentationDuration="PT2S"><Period id="p1" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><SegmentBase indexRange="10-19"/><Representation id="v1" bandwidth="100"><BaseURL>p1.mp4</BaseURL></Representation></AdaptationSet></Period><Period id="p2" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><SegmentBase indexRange="10-19"/><Representation id="v2" bandwidth="100"><BaseURL>p2.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`)
			return
		}
		http.Error(writer, "must not fetch index or media", http.StatusInternalServerError)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", t.TempDir(), filepath.Join(t.TempDir(), "video.mp4"), false, nil)
	if !errors.Is(err, ErrUnsupportedAddressing) || !strings.Contains(err.Error(), "dynamic multi-period SegmentBase/SIDX") {
		t.Fatalf("Download() error = %v", err)
	}
}

func dynamicMultiPeriodMPD(first bool) string {
	p1, p2, typ := `<SegmentURL media="p1-1.m4s"/>`, `<SegmentURL media="p2-1.m4s"/>`, "dynamic"
	if !first {
		p1 += `<SegmentURL media="p1-2.m4s"/>`
		p2 += `<SegmentURL media="p2-2.m4s"/>`
		typ = "static"
	}
	return fmt.Sprintf(`<MPD type="%s" mediaPresentationDuration="PT2S" minimumUpdatePeriod="PT0.001S"><Period id="p1" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="p1-v" bandwidth="100"><SegmentList><Initialization sourceURL="p1-init.mp4"/>%s</SegmentList></Representation></AdaptationSet></Period><Period id="p2" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="p2-v" bandwidth="100"><SegmentList><Initialization sourceURL="p2-init.mp4"/>%s</SegmentList></Representation></AdaptationSet></Period></MPD>`, typ, p1, p2)
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

func TestDownloadMultiPeriodEnforcesAggregateSegmentLimit(t *testing.T) {
	server := multiPeriodServer(t, nil)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{MaxSegments: 3}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "limited.mp4"), false, nil)
	if !errors.Is(err, fragment.ErrTooManySegments) {
		t.Fatalf("Download() error = %v; want ErrTooManySegments", err)
	}
}

func TestDownloadMultiPeriodRejectsExistingDestinationBeforeFragments(t *testing.T) {
	var fragmentRequests atomic.Int32
	server := multiPeriodServer(t, func(_ http.ResponseWriter, request *http.Request) bool {
		if request.URL.Path != "/manifest.mpd" {
			fragmentRequests.Add(1)
		}
		return false
	})
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "existing.mp4")
	if err := os.WriteFile(destination, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/manifest.mpd", root, destination, false, nil)
	if err == nil || !strings.Contains(err.Error(), "destination exists") {
		t.Fatalf("Download() error = %v; want destination-exists failure", err)
	}
	if fragmentRequests.Load() != 0 {
		t.Fatalf("fragment requests = %d; want 0", fragmentRequests.Load())
	}
	contents, readErr := os.ReadFile(destination)
	if readErr != nil || string(contents) != "keep" {
		t.Fatalf("destination = %q, error = %v", contents, readErr)
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

func testPeriods(count int) []Period {
	periods := make([]Period, count)
	for index := range periods {
		periods[index] = Period{Start: time.Duration(index) * time.Second, Duration: time.Second, TimingKnown: true}
	}
	return periods
}

func multiPeriodServer(t *testing.T, override func(http.ResponseWriter, *http.Request) bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if override != nil && override(writer, request) {
			return
		}
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period id="p1" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1" bandwidth="100"><Representation id="v1" bandwidth="100"><SegmentList><Initialization sourceURL="p1-init.mp4"/><SegmentURL media="p1-media.m4s"/></SegmentList></Representation></AdaptationSet></Period><Period id="p2" duration="PT1S"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="avc1"><Representation id="v2" bandwidth="100"><SegmentList><Initialization sourceURL="p2-init.mp4"/><SegmentURL media="p2-media.m4s"/></SegmentList></Representation></AdaptationSet></Period></MPD>`)
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
