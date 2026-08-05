package dash

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

const (
	testDynamicSIDXInitSize     = 100
	testDynamicSIDXIndexStart   = 100
	testDynamicSIDXIndexSlotLen = 100
	testDynamicSIDXMediaStart   = testDynamicSIDXIndexStart + testDynamicSIDXIndexSlotLen
)

func buildTestSIDXBox(version byte, referenceID, timescale uint32, ept, firstOffset uint64, refs []SIDXReference) []byte {
	var body []byte
	body = append(body, version, 0, 0, 0)
	body = appendTestUint32(body, referenceID)
	body = appendTestUint32(body, timescale)
	if version == 0 {
		body = appendTestUint32(body, uint32(ept))
		body = appendTestUint32(body, uint32(firstOffset))
	} else {
		body = appendTestUint64(body, ept)
		body = appendTestUint64(body, firstOffset)
	}
	body = append(body, 0, 0)
	body = appendTestUint16(body, uint16(len(refs)))
	for _, ref := range refs {
		rawSize := ref.ReferencedSize
		if ref.IsIndex {
			rawSize |= 0x80000000
		}
		body = appendTestUint32(body, rawSize)
		body = appendTestUint32(body, ref.SubsegmentDuration)
		var sap uint32
		if ref.StartsWithSAP {
			sap = 0x80000000 | uint32(ref.SAPType)<<28 | ref.SAPDeltaTime
		}
		body = appendTestUint32(body, sap)
	}
	boxSize := uint32(8 + len(body))
	var box []byte
	box = appendTestUint32(box, boxSize)
	box = append(box, 's', 'i', 'd', 'x')
	box = append(box, body...)
	return box
}

func appendTestUint16(buf []byte, value uint16) []byte {
	return append(buf, byte(value>>8), byte(value))
}

func appendTestUint32(buf []byte, value uint32) []byte {
	return append(buf, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

func appendTestUint64(buf []byte, value uint64) []byte {
	return append(buf,
		byte(value>>56), byte(value>>48), byte(value>>40), byte(value>>32),
		byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

func TestDownloadDynamicSIDXPrefixDropRejected(t *testing.T) {
	leaf1 := []byte("PREFIX_DROP_LEAF_ONE__")
	leaf2 := []byte("PREFIX_DROP_LEAF_TWO__")
	fixture1 := buildDynamicSIDXResource(leaf1, leaf2)
	fixture2 := buildDynamicSIDXResource(leaf1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXPrefixReorderRejected(t *testing.T) {
	leaf1 := []byte("PREFIX_REORDER_LEAF_ONE_")
	leaf2 := []byte("PREFIX_REORDER_LEAF_TWO__")
	fixture1 := buildDynamicSIDXResource(leaf1, leaf2)
	fixture2 := buildDynamicSIDXResource(leaf2, leaf1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXPrefixInsertionRejected(t *testing.T) {
	leaf1 := []byte("PREFIX_INSERT_LEAF_ONE__")
	leaf2 := []byte("PREFIX_INSERT_LEAF_TWO___")
	fixture1 := buildDynamicSIDXResource(leaf1)
	fixture2 := buildDynamicSIDXResource(leaf2, leaf1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXPrefixShrinkRejected(t *testing.T) {
	leaf1 := []byte("PREFIX_SHRINK_LEAF_ONE__")
	leaf2 := []byte("PREFIX_SHRINK_LEAF_TWO__")
	fixture1 := buildDynamicSIDXResource(leaf1, leaf2)
	fixture2 := buildDynamicSIDXResource(leaf1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func assertDynamicSIDXEvolutionRejected(t *testing.T, first, second dynamicSIDXFixture, want string) {
	t.Helper()
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, first.indexRange, first.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, second.indexRange, second.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := first.resource
			if polls.Load() > 1 {
				resource = second.resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want %q", err, want)
	}
}

func TestDownloadDynamicSIDXUnchangedSnapshotsWithinMaxSegments(t *testing.T) {
	leaf1 := []byte("UNCHANGED_LEAF_ONE_____")
	leaf2 := []byte("UNCHANGED_LEAF_TWO_____")
	fixture := buildDynamicSIDXResource(leaf1, leaf2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 5, PollInterval: time.Millisecond, MaxSegments: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := dynamicSIDXExpectedOutput(leaf1, leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXAppendBeyondMaxSegmentsRejected(t *testing.T) {
	leaves := [][]byte{
		[]byte("MAXSEG_LEAF_ONE_______"),
		[]byte("MAXSEG_LEAF_TWO_______"),
		[]byte("MAXSEG_LEAF_THREE_____"),
	}
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := int(polls.Add(1))
			fixture := buildDynamicSIDXResource(leaves[:poll]...)
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			poll := int(polls.Load())
			if poll == 0 {
				poll = 1
			}
			serveRange(w, r, buildDynamicSIDXResource(leaves[:poll]...).resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 3, PollInterval: time.Millisecond, MaxSegments: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXMixedAddressingRejected(t *testing.T) {
	vFixture := buildDynamicSIDXResource([]byte("MIXED_VIDEO_LEAF_____"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="a" bandwidth="128"><SegmentTemplate media="a-$Number$.m4s" initialization="a-init"><SegmentTimeline><S t="0" d="1"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet>
</Period></MPD>`, vFixture.indexRange)
		case "/video.mp4":
			serveRange(w, r, vFixture.resource)
		case "/a-init", "/a-1.m4s":
			_, _ = w.Write([]byte("audio"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 1}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "mixed dynamic SegmentBase/SIDX") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXDuplicateRepresentationKeyRejected(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("DUPKEY_LEAF__________"))
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4">
<Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation>
<Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation>
</AdaptationSet></Period></MPD>`, fixture.indexRange, fixture.indexRange)
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "ambiguous representation match") {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateRepresentationMarkers(t *testing.T) {
	marker := Segment{URL: "http://example/video.mp4", IndexRange: "100-199", InitRange: "0-99"}
	if err := validateRepresentationMarkers([]Segment{marker}); err != nil {
		t.Fatal(err)
	}
	if err := validateRepresentationMarkers([]Segment{
		{URL: "http://example/a.mp4", IndexRange: "100-199"},
		{URL: "http://example/b.mp4", IndexRange: "200-299"},
	}); err == nil || !strings.Contains(err.Error(), "multiple SegmentBase media URLs") {
		t.Fatalf("multiple URLs err = %v", err)
	}
	if err := validateRepresentationMarkers([]Segment{
		{URL: "http://example/video.mp4", IndexRange: "100-199", Initialize: true},
	}); err == nil || !strings.Contains(err.Error(), "initialization cannot share") {
		t.Fatalf("init marker err = %v", err)
	}
}

type dynamicSIDXFixture struct {
	resource   []byte
	indexRange string
	initRange  string
	indexSlot  []byte
}

func (fixture dynamicSIDXFixture) resourceWithSIDX() []byte {
	if len(fixture.indexSlot) == 0 {
		return fixture.resource
	}
	resource := append([]byte(nil), fixture.resource[:testDynamicSIDXIndexStart]...)
	resource = append(resource, fixture.indexSlot...)
	resource = append(resource, fixture.resource[testDynamicSIDXMediaStart:]...)
	return resource
}

func buildDynamicSIDXResource(leaves ...[]byte) dynamicSIDXFixture {
	return buildDynamicSIDXWindow(leaves, 0, len(leaves))
}

// buildDynamicSIDXWindow builds a SegmentBase resource whose media payload
// contains allLeaves in order at stable absolute offsets, while the SIDX
// references only allLeaves[windowStart:windowStart+windowCount]. Historical
// leaf bytes remain present so post-poll downloads can still fetch ranges that
// have already rolled out of the live window.
func buildDynamicSIDXWindow(allLeaves [][]byte, windowStart, windowCount int) dynamicSIDXFixture {
	if windowStart < 0 || windowCount < 0 || windowStart+windowCount > len(allLeaves) {
		panic("invalid rolling SIDX window")
	}
	init := make([]byte, testDynamicSIDXInitSize)
	for i := range init {
		init[i] = 'I'
	}
	window := allLeaves[windowStart : windowStart+windowCount]
	refs := make([]SIDXReference, len(window))
	for i, leaf := range window {
		refs[i] = SIDXReference{
			ReferencedSize: uint32(len(leaf)), SubsegmentDuration: 48000,
			StartsWithSAP: true, SAPType: 1,
		}
	}
	probe := buildTestSIDXBox(0, 1, 48000, 0, 0, refs)
	absStart := testDynamicSIDXMediaStart
	for i := 0; i < windowStart; i++ {
		absStart += len(allLeaves[i])
	}
	firstOffset := uint64(absStart - (testDynamicSIDXIndexStart + len(probe)))
	sidxBox := buildTestSIDXBox(0, 1, 48000, 0, firstOffset, refs)
	if len(sidxBox) > testDynamicSIDXIndexSlotLen {
		panic("dynamic SIDX fixture exceeds fixed index slot")
	}
	indexSlot := make([]byte, testDynamicSIDXIndexSlotLen)
	copy(indexSlot, sidxBox)

	resource := append([]byte(nil), init...)
	resource = append(resource, indexSlot...)
	for _, leaf := range allLeaves {
		resource = append(resource, leaf...)
	}

	indexRange := fmt.Sprintf("%d-%d", testDynamicSIDXIndexStart, testDynamicSIDXIndexStart+testDynamicSIDXIndexSlotLen-1)
	return dynamicSIDXFixture{resource: resource, indexRange: indexRange, initRange: "0-99", indexSlot: indexSlot}
}

func dynamicSIDXExpectedOutput(leaves ...[]byte) string {
	var builder strings.Builder
	builder.WriteString(strings.Repeat("I", testDynamicSIDXInitSize))
	for _, leaf := range leaves {
		builder.Write(leaf)
	}
	return builder.String()
}

func dynamicSIDXMPD(dynamic bool, indexRange, initRange string, extraAttrs string) string {
	mpdType := "static"
	if dynamic {
		mpdType = "dynamic"
	}
	return fmt.Sprintf(`<MPD type="%s" %s><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="%s"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, mpdType, extraAttrs, indexRange, initRange)
}

func TestDownloadDynamicSIDXSecondSnapshotAddsLeaves(t *testing.T) {
	leaf1 := []byte("LEAF_ONE______________")
	leaf2 := []byte("LEAF_TWO______________")
	fixture := buildDynamicSIDXResource(leaf1)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			updated := buildDynamicSIDXResource(leaf1, leaf2)
			fmt.Fprint(w, dynamicSIDXMPD(true, updated.indexRange, updated.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := fixture.resource
			if polls.Load() > 1 {
				resource = buildDynamicSIDXResource(leaf1, leaf2).resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := strings.Repeat("I", 100) + string(leaf1) + string(leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

// TestDownloadDynamicSIDXStableIndexRangeAppendPreservesLeafRanges verifies that
// append-only evolution can succeed when the manifest indexRange stays stable and
// prior media leaf URL/range metadata are preserved. It does not detect remote byte
// mutation behind an unchanged URL/range between polling and download.
func TestDownloadDynamicSIDXStableIndexRangeAppendPreservesLeafRanges(t *testing.T) {
	leaf1 := []byte("EVOLVE_LEAF_ONE_______")
	leaf2 := []byte("EVOLVE_LEAF_TWO_______")
	fixture1 := buildDynamicSIDXResource(leaf1)
	fixture2 := buildDynamicSIDXResource(leaf1, leaf2)
	if fixture1.indexRange != fixture2.indexRange {
		t.Fatalf("index ranges differ: %s vs %s", fixture1.indexRange, fixture2.indexRange)
	}
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture1.indexRange, fixture1.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture2.indexRange, fixture2.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := fixture1.resource
			if polls.Load() > 1 {
				resource = fixture2.resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := strings.Repeat("I", 100) + string(leaf1) + string(leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXDynamicToStaticTransition(t *testing.T) {
	leaf1 := []byte("STATIC_LEAF_ONE_______")
	leaf2 := []byte("STATIC_LEAF_TWO_______")
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, buildDynamicSIDXResource(leaf1).indexRange, "0-99", `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(false, buildDynamicSIDXResource(leaf1, leaf2).indexRange, "0-99", ""))
		case "/video.mp4":
			resource := buildDynamicSIDXResource(leaf1).resource
			if polls.Load() > 1 {
				resource = buildDynamicSIDXResource(leaf1, leaf2).resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 5, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls = %d, want 2", polls.Load())
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := strings.Repeat("I", 100) + string(leaf1) + string(leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXUsesMinimumUpdatePeriod(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("WAIT_LEAF_____________"))
	var polls atomic.Int32
	start := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			polls.Add(1)
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.05S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls = %d", polls.Load())
	}
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Fatalf("elapsed = %v, want at least minimumUpdatePeriod", elapsed)
	}
}

func TestDownloadDynamicSIDXUsesConfiguredPollInterval(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("FAST_POLL_LEAF________"))
	var polls atomic.Int32
	start := time.Now()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			polls.Add(1)
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT5S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
		t.Fatalf("elapsed = %v, configured PollInterval should override minimumUpdatePeriod", elapsed)
	}
}

func TestDownloadDynamicSIDXVideoAudioIndependentEvolution(t *testing.T) {
	vLeaf1 := []byte("VIDEO_LEAF_ONE________")
	vLeaf2 := []byte("VIDEO_LEAF_TWO________")
	aLeaf1 := []byte("AUDIO_LEAF_ONE________")
	aLeaf2 := []byte("AUDIO_LEAF_TWO________")
	vFixture1 := buildDynamicSIDXResource(vLeaf1)
	aFixture1 := buildDynamicSIDXResource(aLeaf1)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			vRange, aRange := vFixture1.indexRange, aFixture1.indexRange
			if poll > 1 {
				vRange = buildDynamicSIDXResource(vLeaf1, vLeaf2).indexRange
				aRange = buildDynamicSIDXResource(aLeaf1, aLeaf2).indexRange
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="a" bandwidth="128"><BaseURL>audio.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet>
</Period></MPD>`, vRange, aRange)
		case "/video.mp4":
			resource := vFixture1.resource
			if polls.Load() > 1 {
				resource = buildDynamicSIDXResource(vLeaf1, vLeaf2).resource
			}
			serveRange(w, r, resource)
		case "/audio.mp4":
			resource := aFixture1.resource
			if polls.Load() > 1 {
				resource = buildDynamicSIDXResource(aLeaf1, aLeaf2).resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "dash.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired || len(result.Tracks) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, track := range result.Tracks {
		contents, _ := os.ReadFile(track.Download.Path)
		prefix := strings.Repeat("I", 100)
		switch track.Representation.ContentType {
		case "video":
			if string(contents) != prefix+string(vLeaf1)+string(vLeaf2) {
				t.Fatalf("video contents = %q", contents)
			}
		case "audio":
			if string(contents) != prefix+string(aLeaf1)+string(aLeaf2) {
				t.Fatalf("audio contents = %q", contents)
			}
		default:
			t.Fatalf("unexpected track %s", track.Representation.ContentType)
		}
	}
}

func TestDownloadDynamicSIDXInitDeduplicated(t *testing.T) {
	leaf1 := []byte("INIT_DEDUPE_LEAF_____")
	leaf2 := []byte("INIT_DEDUPE_LEAF2____")
	fixture := buildDynamicSIDXResource(leaf1)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			polls.Add(1)
			updated := buildDynamicSIDXResource(leaf1, leaf2)
			fmt.Fprint(w, dynamicSIDXMPD(true, updated.indexRange, updated.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := fixture.resource
			if polls.Load() > 1 {
				resource = buildDynamicSIDXResource(leaf1, leaf2).resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := dynamicSIDXExpectedOutput(leaf1, leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXChangedInitRejected(t *testing.T) {
	leaf := []byte("CHANGED_INIT_LEAF_____")
	fixture := buildDynamicSIDXResource(leaf)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			initRange := fixture.initRange
			if poll > 1 {
				initRange = "0-98"
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "initialization identity changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXIgnoresHigherBandwidthSibling(t *testing.T) {
	leaf1 := []byte("SIBLING_LEAF_ONE_______")
	leaf2 := []byte("SIBLING_LEAF_TWO_______")
	fixture := buildDynamicSIDXResource(leaf1)
	updated := buildDynamicSIDXResource(leaf1, leaf2)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation><Representation id="v-hd" bandwidth="5000"><BaseURL>video-hd.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, updated.indexRange, updated.indexRange)
		case "/video.mp4":
			resource := fixture.resource
			if polls.Load() > 1 {
				resource = updated.resource
			}
			serveRange(w, r, resource)
		case "/video-hd.mp4":
			serveRange(w, r, buildDynamicSIDXResource([]byte("IGNORED_HD_LEAF______")).resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := dynamicSIDXExpectedOutput(leaf1, leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXRepresentationDisappeared(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("DISAPPEAR_LEAF_______"))
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="missing" bandwidth="1"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="100-199"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`)
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "disappeared") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXTrackPropertyMutationRejected(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("MUTATION_LEAF________"))
	for _, test := range []struct {
		name string
		mpd  func(poll int, fixture dynamicSIDXFixture) string
	}{
		{
			name: "bandwidth",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				bw := 1000
				if poll > 1 {
					bw = 2000
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="%d"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, bw, fixture.indexRange)
			},
		},
		{
			name: "width",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				width := 1280
				if poll > 1 {
					width = 1920
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000" width="%d"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, width, fixture.indexRange)
			},
		},
		{
			name: "height",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				height := 720
				if poll > 1 {
					height = 1080
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000" height="%d"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, height, fixture.indexRange)
			},
		},
		{
			name: "frameRate",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				rate := "30"
				if poll > 1 {
					rate = "60"
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000" frameRate="%s"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, rate, fixture.indexRange)
			},
		},
		{
			name: "mimeType",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				mime := "video/mp4"
				if poll > 1 {
					mime = "video/webm"
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="%s"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, mime, fixture.indexRange)
			},
		},
		{
			name: "language",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				lang := "en"
				if poll > 1 {
					lang = "fr"
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4" lang="%s"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, lang, fixture.indexRange)
			},
		},
		{
			name: "codecs",
			mpd: func(poll int, fixture dynamicSIDXFixture) string {
				codecs := "avc1.42E01E"
				if poll > 1 {
					codecs = "hev1.1.6.L93.B0"
				}
				return fmt.Sprintf(`<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4" codecs="%s"><Representation id="v" bandwidth="1000" codecs="%s"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, codecs, codecs, fixture.indexRange)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var polls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/live.mpd":
					poll := polls.Add(1)
					fmt.Fprint(w, test.mpd(int(poll), fixture))
				case "/video.mp4":
					serveRange(w, r, fixture.resource)
				}
			}))
			defer server.Close()
			transport, _ := network.New(network.Config{})
			root := t.TempDir()
			_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
			if err == nil || !strings.Contains(err.Error(), "track properties changed") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestDownloadDynamicSIDXAudioRateMutationRejected(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("AUDIO_RATE_LEAF______"))
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			rate := ` audioSamplingRate="44100"`
			if poll > 1 {
				rate = ` audioSamplingRate="48000"`
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="audio" mimeType="audio/mp4"%s><Representation id="a" bandwidth="128"><BaseURL>audio.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, rate, fixture.indexRange)
		case "/audio.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "track properties changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXMediaURLMutationRejected(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("URL_CHANGE_LEAF______"))
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			baseURL := "video.mp4"
			if poll > 1 {
				baseURL = "video2.mp4"
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>%s</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, baseURL, fixture.indexRange)
		case "/video.mp4", "/video2.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "media URL changed") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXOverlappingEvolutionRejected(t *testing.T) {
	leaf1 := []byte("OVERLAP_LEAF_ONE_____")
	leaf2 := []byte("OVERLAP_LEAF_TWO_____")
	full := buildDynamicSIDXResource(leaf1, leaf2)
	overlapRef := []SIDXReference{{
		ReferencedSize: uint32(len(leaf2)), SubsegmentDuration: 48000,
		StartsWithSAP: true, SAPType: 1,
	}}
	probe := buildTestSIDXBox(0, 1, 48000, 0, 0, overlapRef)
	overlapOffset := uint64(testDynamicSIDXMediaStart - (testDynamicSIDXIndexStart + len(probe)) + 5)
	overlapSIDX := buildTestSIDXBox(0, 1, 48000, 0, overlapOffset, overlapRef)
	indexSlot := make([]byte, testDynamicSIDXIndexSlotLen)
	copy(indexSlot, overlapSIDX)
	overlapResource := append([]byte(nil), full.resource[:testDynamicSIDXIndexStart]...)
	overlapResource = append(overlapResource, indexSlot...)
	overlapResource = append(overlapResource, full.resource[testDynamicSIDXMediaStart:]...)
	fixture1 := buildDynamicSIDXResource(leaf1)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture1.indexRange, fixture1.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, full.indexRange, full.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := fixture1.resource
			if polls.Load() > 1 {
				resource = overlapResource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || (!strings.Contains(err.Error(), "overlapping byte-range evolution") && !strings.Contains(err.Error(), "unanchored live-window evolution")) {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXCumulativeRootBoxBudgetAcrossPolls(t *testing.T) {
	leaf := []byte("ROOT_BOX_BUDGET_LEAF____")
	fixture := buildDynamicSIDXResource(leaf)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			polls.Add(1)
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{
		DynamicPolls: maxSIDXBoxesPerRepresentation + 1,
		PollInterval: time.Microsecond,
	}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "parsed SIDX box count") {
		t.Fatalf("err = %v", err)
	}
	if polls.Load() != maxSIDXBoxesPerRepresentation+1 {
		t.Fatalf("polls = %d, want %d", polls.Load(), maxSIDXBoxesPerRepresentation+1)
	}
}

func TestDownloadDynamicSIDXCumulativeLeafBudgetAcrossPolls(t *testing.T) {
	leaves := [][]byte{
		[]byte("BUDGET_LEAF_ONE_______"),
		[]byte("BUDGET_LEAF_TWO_______"),
		[]byte("BUDGET_LEAF_THREE_____"),
	}
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := int(polls.Add(1))
			fixture := buildDynamicSIDXResource(leaves[:poll]...)
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			poll := int(polls.Load())
			if poll == 0 {
				poll = 1
			}
			serveRange(w, r, buildDynamicSIDXResource(leaves[:poll]...).resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 3, PollInterval: time.Millisecond, MaxSegments: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXCancellationDuringWait(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("CANCEL_WAIT_LEAF_____"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT5S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	dest := filepath.Join(root, "cancel.bin")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2}).Download(ctx, server.URL+"/live.mpd", root, dest, false, nil)
	if err != context.DeadlineExceeded {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadDynamicSIDXCancellationDuringRootFetch(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("CANCEL_ROOT_LEAF_____"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+fixture.indexRange {
				time.Sleep(200 * time.Millisecond)
			}
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(ctx, server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestDownloadDynamicSIDXCancellationDuringMediaFetch(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("CANCEL_MEDIA_LEAF____"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, ""))
		case "/video.mp4":
			if r.Header.Get("Range") != "bytes="+fixture.indexRange {
				time.Sleep(200 * time.Millisecond)
			}
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{DynamicPolls: 1}).Download(ctx, server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestDownloadDynamicSIDXNoDestinationOnFailure(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("FAIL_NO_DEST_LEAF____"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+fixture.indexRange {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	dest := filepath.Join(root, "out.mp4")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should not exist: %v", statErr)
	}
}

func TestDownloadDynamicSIDXRangeHeaderPropagation(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("RANGE_HEADER_LEAF____"))
	var gotRange atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, ""))
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+fixture.indexRange {
				gotRange.Store(r.Header.Get("Range"))
			}
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 1}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gotRange.Load().(string); got != "bytes="+fixture.indexRange {
		t.Fatalf("Range = %q", got)
	}
}

func TestDownloadDynamicSIDX200FallbackBudget(t *testing.T) {
	leaf := []byte("FALLBACK_BUDGET_LEAF_")
	fixture := buildDynamicSIDXResource(leaf)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, ""))
		case "/video.mp4":
			if r.Header.Get("Range") == "bytes="+fixture.indexRange {
				w.WriteHeader(http.StatusOK)
				w.Write(fixture.resource)
				return
			}
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 1}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := dynamicSIDXExpectedOutput(leaf)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXDeterministicRaceOutput(t *testing.T) {
	leaf1 := []byte("RACE_LEAF_ONE_________")
	leaf2 := []byte("RACE_LEAF_TWO_________")
	fixture1 := buildDynamicSIDXResource(leaf1)
	fixture2 := buildDynamicSIDXResource(leaf1, leaf2)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture1.indexRange, fixture1.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture2.indexRange, fixture2.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			resource := fixture1.resource
			if polls.Load() > 1 {
				resource = fixture2.resource
			}
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	want := strings.Repeat("I", 100) + string(leaf1) + string(leaf2)
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			transport, err := network.New(network.Config{})
			if err != nil {
				errs <- err
				return
			}
			root := t.TempDir()
			result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
			if err != nil {
				errs <- err
				return
			}
			contents, err := os.ReadFile(result.Tracks[0].Download.Path)
			if err != nil {
				errs <- err
				return
			}
			if string(contents) != want {
				errs <- fmt.Errorf("contents = %q", contents)
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestEffectiveDynamicSIDXMediaLeafLimit(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		initCount  int
		want       int
	}{
		{name: "normalized default with init", configured: defaultMaxDownloadSegments, initCount: 1, want: fragmentHardSegmentCap - 1},
		{name: "configured below fragment cap", configured: 5000, initCount: 1, want: 5000},
		{name: "configured above fragment cap", configured: fragmentHardSegmentCap, initCount: 1, want: fragmentHardSegmentCap - 1},
		{name: "no init", configured: fragmentHardSegmentCap, initCount: 0, want: fragmentHardSegmentCap},
		{name: "init consumes cap", configured: fragmentHardSegmentCap, initCount: 2, want: fragmentHardSegmentCap - 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := effectiveDynamicSIDXMediaLeafLimit(test.configured, test.initCount)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("effectiveDynamicSIDXMediaLeafLimit(%d, %d) = %d, want %d", test.configured, test.initCount, got, test.want)
			}
		})
	}
}

func TestEffectiveDynamicSIDXMediaLeafLimitRejectsNegative(t *testing.T) {
	_, err := effectiveDynamicSIDXMediaLeafLimit(-1, 1)
	if err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
	if err := validateDynamicSIDXMaxSegmentsConfigured(-1); err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("configured err = %v", err)
	}
}

func TestDownloadDynamicSIDXNegativeMaxSegmentsRejected(t *testing.T) {
	var videoRequests atomic.Int32
	fixture := buildDynamicSIDXResource([]byte("NEG_MAXSEG_LEAF________"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, ""))
		case "/video.mp4":
			videoRequests.Add(1)
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 1, MaxSegments: -1}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
	if videoRequests.Load() != 0 {
		t.Fatalf("video requests = %d, want 0", videoRequests.Load())
	}
}

func TestSegmentAccumulatorRejectsMultipleInitSegments(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	err := accumulator.merge([]Segment{
		{URL: "http://example/media", Initialize: true, RangeStart: 0, RangeLength: 100},
		{URL: "http://example/media", Initialize: true, RangeStart: 0, RangeLength: 200},
	})
	if err == nil || !strings.Contains(err.Error(), "multiple initialization segments") {
		t.Fatalf("err = %v", err)
	}
}

func TestSegmentAccumulatorRespectsFragmentEngineMediaCap(t *testing.T) {
	got, err := effectiveDynamicSIDXMediaLeafLimit(fragmentHardSegmentCap, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got != fragmentHardSegmentCap-1 {
		t.Fatalf("limit = %d, want %d", got, fragmentHardSegmentCap-1)
	}
	accumulator := newSegmentAccumulator(2)
	leaves := []Segment{
		{URL: "http://example/media", RangeStart: 100, RangeLength: 5},
		{URL: "http://example/media", RangeStart: 200, RangeLength: 5},
		{URL: "http://example/media", RangeStart: 300, RangeLength: 5},
	}
	if err := accumulator.merge(leaves[:1]); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge(leaves[:2]); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge(leaves); err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("overflow err = %v", err)
	}
}

func TestValidateDynamicSIDXOutputBudgetRejectsFragmentCapOverflow(t *testing.T) {
	segments := make([]Segment, 0, fragmentHardSegmentCap)
	segments = append(segments, Segment{URL: "http://example/media", Initialize: true, RangeStart: 0, RangeLength: 100})
	for index := 0; index < fragmentHardSegmentCap-1; index++ {
		segments = append(segments, Segment{URL: "http://example/media", RangeStart: int64(200 + index), RangeLength: 10})
	}
	if err := validateDynamicSIDXOutputBudget(segments, fragmentHardSegmentCap); err != nil {
		t.Fatalf("valid plan err = %v", err)
	}
	segments = append(segments, Segment{URL: "http://example/media", RangeStart: int64(200 + fragmentHardSegmentCap), RangeLength: 10})
	if err := validateDynamicSIDXOutputBudget(segments, fragmentHardSegmentCap); err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("overflow err = %v", err)
	}
}

func TestSegmentAccumulatorRejectsBeyondMax(t *testing.T) {
	accumulator := newSegmentAccumulator(2)
	leaves := []Segment{
		{URL: "http://example/media", RangeStart: 100, RangeLength: 10},
		{URL: "http://example/media", RangeStart: 200, RangeLength: 10},
		{URL: "http://example/media", RangeStart: 300, RangeLength: 10},
	}
	if err := accumulator.merge(leaves[:1]); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge(leaves[:2]); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge(leaves); err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestSegmentAccumulatorMergeFuzzIdentity(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	segment := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	if err := accumulator.merge([]Segment{segment}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge([]Segment{segment}); err != nil {
		t.Fatal(err)
	}
	if len(accumulator.segments) != 1 {
		t.Fatalf("segments = %d", len(accumulator.segments))
	}
	if accumulator.uniqueLeafCount != 1 {
		t.Fatalf("uniqueLeafCount = %d", accumulator.uniqueLeafCount)
	}
}

func FuzzDynamicSIDXAccumulatorMerge(f *testing.F) {
	f.Add(uint8(0), int64(0), int64(1))
	f.Add(uint8(1), int64(100), int64(5))
	f.Fuzz(func(t *testing.T, seed uint8, start, length int64) {
		if length <= 0 || start < 0 || length > 1<<20 {
			return
		}
		accumulator := newSegmentAccumulator(100)
		segment := Segment{URL: "http://example/media", RangeStart: start, RangeLength: length}
		if err := accumulator.merge([]Segment{segment}); err != nil {
			return
		}
		dup := Segment{URL: "http://example/media", RangeStart: start + int64(seed%3), RangeLength: length}
		_ = accumulator.merge([]Segment{dup})
	})
}
