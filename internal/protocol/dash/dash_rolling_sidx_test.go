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

	"github.com/tejasa97/ytdlp-go/internal/network"
)

func TestAlignLiveWindowPrefixEvictionAndAppend(t *testing.T) {
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 110, RangeLength: 10}
	c := Segment{URL: "http://example/media", RangeStart: 120, RangeLength: 10}
	d := Segment{URL: "http://example/media", RangeStart: 130, RangeLength: 10}

	drop, err := alignLiveWindow([]Segment{a, b, c}, []Segment{b, c, d})
	if err != nil {
		t.Fatal(err)
	}
	if drop != 1 {
		t.Fatalf("drop = %d, want 1", drop)
	}

	drop, err = alignLiveWindow([]Segment{a, b, c}, []Segment{b, c})
	if err != nil {
		t.Fatal(err)
	}
	if drop != 1 {
		t.Fatalf("pure eviction drop = %d, want 1", drop)
	}

	drop, err = alignLiveWindow([]Segment{a, b}, []Segment{a, b, c})
	if err != nil {
		t.Fatal(err)
	}
	if drop != 0 {
		t.Fatalf("append drop = %d, want 0", drop)
	}
}

func TestAlignLiveWindowRejectsRewindMutationAndReorder(t *testing.T) {
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 110, RangeLength: 10}
	c := Segment{URL: "http://example/media", RangeStart: 120, RangeLength: 10}
	bMut := Segment{URL: "http://example/media", RangeStart: 110, RangeLength: 11}

	if _, err := alignLiveWindow([]Segment{a, b, c}, []Segment{a, b}); err == nil || !strings.Contains(err.Error(), "unanchored live-window evolution") {
		t.Fatalf("rewind err = %v", err)
	}
	if _, err := alignLiveWindow([]Segment{a, b, c}, []Segment{a, bMut, c}); err == nil || !strings.Contains(err.Error(), "unanchored live-window evolution") {
		t.Fatalf("mutation err = %v", err)
	}
	drop, err := alignLiveWindow([]Segment{a, b}, []Segment{b, a})
	if err != nil {
		t.Fatal(err)
	}
	if drop != 1 {
		t.Fatalf("reorder drop = %d, want 1", drop)
	}
}

func TestAlignLiveWindowRejectsDisjointNonEmptyWindows(t *testing.T) {
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 200, RangeLength: 10}
	c := Segment{URL: "http://example/media", RangeStart: 300, RangeLength: 10}

	_, err := alignLiveWindow([]Segment{a, b}, []Segment{c})
	if err == nil || !strings.Contains(err.Error(), "unanchored live-window evolution") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, ErrUnsupportedAddressing) {
		t.Fatalf("err = %v, want ErrUnsupportedAddressing", err)
	}

	_, err = alignLiveWindow([]Segment{a}, []Segment{b, c})
	if err == nil || !errors.Is(err, ErrUnsupportedAddressing) {
		t.Fatalf("single-to-novel err = %v", err)
	}

	_, err = alignLiveWindow([]Segment{a, b}, nil)
	if err == nil || !errors.Is(err, ErrUnsupportedAddressing) {
		t.Fatalf("empty updated err = %v", err)
	}
}

func TestSegmentAccumulatorRollingWindowAppendsOnce(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 110, RangeLength: 10}
	c := Segment{URL: "http://example/media", RangeStart: 120, RangeLength: 10}
	d := Segment{URL: "http://example/media", RangeStart: 130, RangeLength: 10}

	if err := accumulator.merge([]Segment{a, b, c}); err != nil {
		t.Fatal(err)
	}
	if err := accumulator.merge([]Segment{b, c, d}); err != nil {
		t.Fatal(err)
	}
	if len(accumulator.mediaLeaves) != 4 || accumulator.uniqueLeafCount != 4 {
		t.Fatalf("leaves = %d unique = %d", len(accumulator.mediaLeaves), accumulator.uniqueLeafCount)
	}
	if len(accumulator.window) != 3 {
		t.Fatalf("window = %d, want 3", len(accumulator.window))
	}
	if segmentKey(accumulator.mediaLeaves[0]) != segmentKey(a) || segmentKey(accumulator.mediaLeaves[3]) != segmentKey(d) {
		t.Fatalf("accumulated order = %+v", accumulator.mediaLeaves)
	}
}

func TestSegmentAccumulatorRejectsReplayAfterEviction(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 110, RangeLength: 10}
	c := Segment{URL: "http://example/media", RangeStart: 120, RangeLength: 10}
	if err := accumulator.merge([]Segment{a, b}); err != nil {
		t.Fatal(err)
	}
	// Reorder aligns as eviction of a then append of a → replay.
	err := accumulator.merge([]Segment{b, a, c})
	if err == nil || !strings.Contains(err.Error(), "replayed media leaf") {
		t.Fatalf("err = %v", err)
	}
}

func TestSegmentAccumulatorRejectsDuplicateWindowIdentity(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	leaf := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	err := accumulator.merge([]Segment{leaf, leaf})
	if err == nil || !strings.Contains(err.Error(), "duplicate media leaf identity") {
		t.Fatalf("err = %v", err)
	}
}

func TestSegmentAccumulatorRejectsUnanchoredFullWindowReplacement(t *testing.T) {
	accumulator := newSegmentAccumulator(10)
	a := Segment{URL: "http://example/media", RangeStart: 100, RangeLength: 10}
	b := Segment{URL: "http://example/media", RangeStart: 200, RangeLength: 10}
	if err := accumulator.merge([]Segment{a}); err != nil {
		t.Fatal(err)
	}
	err := accumulator.merge([]Segment{b})
	if err == nil || !strings.Contains(err.Error(), "unanchored live-window evolution") {
		t.Fatalf("err = %v", err)
	}
	if !errors.Is(err, ErrUnsupportedAddressing) {
		t.Fatalf("err = %v, want ErrUnsupportedAddressing", err)
	}
	if accumulator.uniqueLeafCount != 1 {
		t.Fatalf("uniqueLeafCount = %d, want 1 after rejected replacement", accumulator.uniqueLeafCount)
	}
}

func TestDownloadDynamicSIDXRollingWindowEvictAndAppend(t *testing.T) {
	leaves := [][]byte{
		[]byte("ROLL_LEAF_ONE_________"),
		[]byte("ROLL_LEAF_TWO_________"),
		[]byte("ROLL_LEAF_THREE_______"),
		[]byte("ROLL_LEAF_FOUR________"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves[:3], 0, 3)
	fixture2 := buildDynamicSIDXWindow(leaves, 1, 3)
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
	want := dynamicSIDXExpectedOutput(leaves...)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXRollingWindowPureEvictionKeepsHistory(t *testing.T) {
	leaves := [][]byte{
		[]byte("PURE_EVICT_LEAF_ONE___"),
		[]byte("PURE_EVICT_LEAF_TWO___"),
		[]byte("PURE_EVICT_LEAF_THREE_"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves, 0, 3)
	fixture2 := buildDynamicSIDXWindow(leaves, 1, 2)
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
			// Serve the full historical payload so evicted leaf one remains fetchable.
			serveRange(w, r, fixture1.resource)
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
	want := dynamicSIDXExpectedOutput(leaves...)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXRollingWindowToStaticTransition(t *testing.T) {
	leaves := [][]byte{
		[]byte("ROLL_STATIC_LEAF_ONE__"),
		[]byte("ROLL_STATIC_LEAF_TWO__"),
		[]byte("ROLL_STATIC_LEAF_THREE"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves[:2], 0, 2)
	fixture2 := buildDynamicSIDXWindow(leaves, 1, 2)
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			if poll == 1 {
				fmt.Fprint(w, dynamicSIDXMPD(true, fixture1.indexRange, fixture1.initRange, `minimumUpdatePeriod="PT0.001S"`))
				return
			}
			fmt.Fprint(w, dynamicSIDXMPD(false, fixture2.indexRange, fixture2.initRange, ""))
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
	result, err := NewDownloader(transport, Config{DynamicPolls: 5, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls = %d, want 2", polls.Load())
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	want := dynamicSIDXExpectedOutput(leaves...)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}

func TestDownloadDynamicSIDXRollingWindowRelocationRejected(t *testing.T) {
	// Unequal sizes make rebuilt absolute ranges diverge from the stable-offset
	// live window, so relocation is visible to URL/range identity.
	leaf1 := []byte("RELOC_ONE_")
	leaf2 := []byte("RELOC_LEAF_TWO_LONGER_")
	leaf3 := []byte("RELOC_THREE")
	fixture1 := buildDynamicSIDXResource(leaf1, leaf2)
	fixture2 := buildDynamicSIDXResource(leaf2, leaf3)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXRollingWindowRewindRejected(t *testing.T) {
	leaves := [][]byte{
		[]byte("REWIND_LEAF_ONE_______"),
		[]byte("REWIND_LEAF_TWO_______"),
		[]byte("REWIND_LEAF_THREE_____"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves, 0, 3)
	fixture2 := buildDynamicSIDXWindow(leaves[:2], 0, 2)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXRollingWindowCompleteDiscontinuityRejected(t *testing.T) {
	leafA := []byte("DISCONTIG_LEAF_A______")
	leafB := []byte("DISCONTIG_LEAF_B______")
	leafC := []byte("DISCONTIG_LEAF_C______")
	// Snapshot 1 window [A,B]; snapshot 2 jumps to a disjoint novel window [C]
	// with no retained suffix/prefix anchor.
	fixture1 := buildDynamicSIDXResource(leafA, leafB)
	all := [][]byte{leafA, leafB, leafC}
	fixture2 := buildDynamicSIDXWindow(all, 2, 1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXRollingWindowRetainedMutationRejected(t *testing.T) {
	leaf1 := []byte("MUT_ROLL_LEAF_ONE_____")
	leaf2 := []byte("MUT_ROLL_LEAF_TWO_____")
	leaf2Mut := []byte("MUT_ROLL_LEAF_TWO_EXTRA")
	fixture1 := buildDynamicSIDXWindow([][]byte{leaf1, leaf2}, 0, 2)
	// Evict leaf1 and publish a length-mutated leaf at leaf2's absolute start
	// with no retained identity anchor.
	fixture2 := buildDynamicSIDXWindow([][]byte{leaf1, leaf2Mut}, 1, 1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXRollingWindowReplayRejected(t *testing.T) {
	leaf1 := []byte("REPLAY_ONE")
	leaf2 := []byte("REPLAY_LEAF_TWO_LONG__")
	fixture1 := buildDynamicSIDXResource(leaf1, leaf2)
	// Rebuild in reverse order with unequal sizes so ranges cannot silently
	// alias the prior window identities.
	fixture2 := buildDynamicSIDXResource(leaf2, leaf1)
	assertDynamicSIDXEvolutionRejected(t, fixture1, fixture2, "unanchored live-window evolution")
}

func TestDownloadDynamicSIDXRollingWindowExceedsMaxSegmentsRejected(t *testing.T) {
	leaves := [][]byte{
		[]byte("ROLL_MAX_LEAF_ONE_____"),
		[]byte("ROLL_MAX_LEAF_TWO_____"),
		[]byte("ROLL_MAX_LEAF_THREE___"),
	}
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := int(polls.Add(1))
			var fixture dynamicSIDXFixture
			switch poll {
			case 1:
				fixture = buildDynamicSIDXWindow(leaves[:2], 0, 2)
			default:
				fixture = buildDynamicSIDXWindow(leaves, 1, 2)
			}
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT0.001S"`))
		case "/video.mp4":
			poll := int(polls.Load())
			if poll <= 1 {
				serveRange(w, r, buildDynamicSIDXWindow(leaves[:2], 0, 2).resource)
				return
			}
			serveRange(w, r, buildDynamicSIDXWindow(leaves, 0, len(leaves)).resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond, MaxSegments: 2}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "fragment plan exceeds segment limit") {
		t.Fatalf("err = %v", err)
	}
}

func TestDownloadDynamicSIDXRollingWindowNoDestinationOnFailure(t *testing.T) {
	leaves := [][]byte{
		[]byte("ROLL_FAIL_LEAF_ONE____"),
		[]byte("ROLL_FAIL_LEAF_TWO____"),
		[]byte("ROLL_FAIL_LEAF_THREE__"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves, 0, 3)
	fixture2 := buildDynamicSIDXWindow(leaves[:2], 0, 2)
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
	dest := filepath.Join(root, "out.mp4")
	_, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, dest, false, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("destination should be absent on failure, stat err = %v", statErr)
	}
}

func TestDownloadDynamicSIDXRollingWindowDeterministicRaceOutput(t *testing.T) {
	leaves := [][]byte{
		[]byte("ROLL_RACE_LEAF_ONE____"),
		[]byte("ROLL_RACE_LEAF_TWO____"),
		[]byte("ROLL_RACE_LEAF_THREE__"),
	}
	fixture1 := buildDynamicSIDXWindow(leaves[:2], 0, 2)
	fixture2 := buildDynamicSIDXWindow(leaves, 1, 2)
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
	want := dynamicSIDXExpectedOutput(leaves...)
	for i := 0; i < 3; i++ {
		polls.Store(0)
		root := t.TempDir()
		result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond, FragmentConcurrency: 4}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
		if err != nil {
			t.Fatal(err)
		}
		contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
		if string(contents) != want {
			t.Fatalf("iteration %d contents = %q, want %q", i, contents, want)
		}
	}
}

func TestDownloadDynamicSIDXRollingWindowCancellationDuringWait(t *testing.T) {
	fixture := buildDynamicSIDXResource([]byte("ROLL_CANCEL_WAIT_LEAF_"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			fmt.Fprint(w, dynamicSIDXMPD(true, fixture.indexRange, fixture.initRange, `minimumUpdatePeriod="PT2S"`))
		case "/video.mp4":
			serveRange(w, r, fixture.resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewDownloader(transport, Config{DynamicPolls: 3}).Download(ctx, server.URL+"/live.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func FuzzDynamicSIDXRollingWindowMerge(f *testing.F) {
	f.Add(uint8(1), uint8(1), int64(100), int64(10), int64(10), int64(10))
	f.Add(uint8(2), uint8(0), int64(50), int64(5), int64(7), int64(9))
	f.Add(uint8(3), uint8(1), int64(10), int64(3), int64(4), int64(5))
	f.Fuzz(func(t *testing.T, dropSeed, appendSeed uint8, start, lenA, lenB, lenC int64) {
		if start < 0 || lenA <= 0 || lenB <= 0 || lenC <= 0 {
			return
		}
		if lenA > 1<<20 || lenB > 1<<20 || lenC > 1<<20 {
			return
		}
		a := Segment{URL: "http://example/media", RangeStart: start, RangeLength: lenA}
		b := Segment{URL: "http://example/media", RangeStart: start + lenA, RangeLength: lenB}
		c := Segment{URL: "http://example/media", RangeStart: start + lenA + lenB, RangeLength: lenC}
		accumulator := newSegmentAccumulator(100)
		if err := accumulator.merge([]Segment{a, b}); err != nil {
			return
		}
		// Anchored evolutions: prefix eviction and optional append of c.
		updated := []Segment{a, b}
		drop := int(dropSeed % 2) // 0 or 1 retains at least one leaf from [a,b]
		updated = updated[drop:]
		if appendSeed%2 == 1 {
			updated = append(updated, c)
		}
		if err := accumulator.merge(updated); err != nil {
			t.Fatalf("anchored merge: %v", err)
		}
		// Completely disjoint non-empty evolution must fail closed.
		novel := Segment{URL: "http://example/media", RangeStart: start + lenA + lenB + lenC + 1, RangeLength: 1}
		err := accumulator.merge([]Segment{novel})
		if err == nil || !errors.Is(err, ErrUnsupportedAddressing) {
			t.Fatalf("disjoint merge err = %v, want ErrUnsupportedAddressing", err)
		}
	})
}
