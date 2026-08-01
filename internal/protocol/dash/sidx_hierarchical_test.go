package dash

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"math"
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

// hierarchicalTestMedia builds a synthetic media resource with a root SIDX
// containing one index reference pointing to a nested SIDX with two leaf refs.
// Layout: [init: 0-99] [rootSIDX] [nestedSIDX] [media1] [media2]
func hierarchicalTestMedia() ([]byte, string) {
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("LEAF_MEDIA_ONE__")
	media2 := []byte("LEAF_MEDIA_TWO__")

	// Nested SIDX: two leaf references.
	nestedRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	nestedBox := buildSIDX(0, 1, 48000, 0, 0, nestedRefs)

	// Root SIDX: one index reference pointing to the nested SIDX.
	rootRefs := []SIDXReference{
		{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 96000, IsIndex: true, StartsWithSAP: true, SAPType: 1},
	}
	rootBox := buildSIDX(0, 1, 48000, 0, 0, rootRefs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, rootBox...)
	resource = append(resource, nestedBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	return resource, indexRange
}

// twoLevelHierarchicalTestMedia builds a resource with root -> mid -> leaf.
// Layout: [init: 0-99] [rootSIDX] [midSIDX] [leafSIDX] [media1] [media2]
func twoLevelHierarchicalTestMedia() ([]byte, string) {
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("DEEP_LEAF_ONE___")
	media2 := []byte("DEEP_LEAF_TWO___")

	// Leaf SIDX: two leaf references.
	leafRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	leafBox := buildSIDX(0, 1, 48000, 0, 0, leafRefs)

	// Mid SIDX: one index reference pointing to the leaf SIDX.
	midRefs := []SIDXReference{
		{ReferencedSize: uint32(len(leafBox)), SubsegmentDuration: 96000, IsIndex: true},
	}
	midBox := buildSIDX(0, 1, 48000, 0, 0, midRefs)

	// Root SIDX: one index reference pointing to the mid SIDX.
	rootRefs := []SIDXReference{
		{ReferencedSize: uint32(len(midBox)), SubsegmentDuration: 96000, IsIndex: true},
	}
	rootBox := buildSIDX(0, 1, 48000, 0, 0, rootRefs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, rootBox...)
	resource = append(resource, midBox...)
	resource = append(resource, leafBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	return resource, indexRange
}

// mixedHierarchicalTestMedia builds a resource with leaf/index/leaf ordering.
// Layout: [init: 0-99] [rootSIDX] [media1] [nestedSIDX] [media3] [media2]
// The nested SIDX uses first_offset to skip past media3 so its leaf (media2)
// does not overlap with the root's leaf (media3).
func mixedHierarchicalTestMedia() ([]byte, string) {
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("FIRST_LEAF______")
	media2 := []byte("NESTED_LEAF_ONE_")
	media3 := []byte("LAST_LEAF_______")

	// Nested SIDX: one leaf reference (media2), with first_offset = len(media3)
	// so its leaf starts after media3 in the resource.
	nestedRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	nestedBox := buildSIDX(0, 1, 48000, 0, uint64(len(media3)), nestedRefs)

	// Root SIDX: leaf(media1), index(nested), leaf(media3).
	rootRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 48000, IsIndex: true},
		{ReferencedSize: uint32(len(media3)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	rootBox := buildSIDX(0, 1, 48000, 0, 0, rootRefs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, rootBox...)
	resource = append(resource, media1...)
	resource = append(resource, nestedBox...)
	resource = append(resource, media3...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	return resource, indexRange
}

func TestDownloadHierarchicalSIDXOneLevel(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
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
	// Expected: init(100) + media1(16) + media2(16) = 132
	if len(contents) != 132 {
		t.Fatalf("contents length = %d, want 132", len(contents))
	}
	if string(contents[100:116]) != "LEAF_MEDIA_ONE__" {
		t.Fatalf("media1 = %q", contents[100:116])
	}
	if string(contents[116:132]) != "LEAF_MEDIA_TWO__" {
		t.Fatalf("media2 = %q", contents[116:132])
	}
}

func TestDownloadHierarchicalSIDXTwoLevels(t *testing.T) {
	resource, indexRange := twoLevelHierarchicalTestMedia()
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
	// Expected: init(100) + media1(16) + media2(16) = 132
	if len(contents) != 132 {
		t.Fatalf("contents length = %d, want 132", len(contents))
	}
	if string(contents[100:116]) != "DEEP_LEAF_ONE___" {
		t.Fatalf("media1 = %q", contents[100:116])
	}
	if string(contents[116:132]) != "DEEP_LEAF_TWO___" {
		t.Fatalf("media2 = %q", contents[116:132])
	}
}

func TestDownloadHierarchicalSIDXMixedOrdering(t *testing.T) {
	resource, indexRange := mixedHierarchicalTestMedia()
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
	// Expected: init(100) + media1(16) + media2(16) + media3(16) = 148
	if len(contents) != 148 {
		t.Fatalf("contents length = %d, want 148", len(contents))
	}
	if string(contents[100:116]) != "FIRST_LEAF______" {
		t.Fatalf("media1 = %q", contents[100:116])
	}
	if string(contents[116:132]) != "NESTED_LEAF_ONE_" {
		t.Fatalf("media2 = %q", contents[116:132])
	}
	if string(contents[132:148]) != "LAST_LEAF_______" {
		t.Fatalf("media3 = %q", contents[132:148])
	}
}

func TestDownloadHierarchicalSIDXExactNestedRangeHeader(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var nestedRange atomic.Value
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n == 2 {
				// Second range request is the nested SIDX fetch.
				nestedRange.Store(r.Header.Get("Range"))
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
	// The nested SIDX starts right after the root SIDX.
	// rootSIDX starts at 100, nested starts at 100+len(rootBox).
	rootRefs := []SIDXReference{{ReferencedSize: 1, IsIndex: true}}
	rootBoxLen := len(buildSIDX(0, 1, 48000, 0, 0, rootRefs))
	// Recalculate with actual sizes from the resource.
	nestedStart := 100 + rootBoxLen
	got := nestedRange.Load()
	if got == nil {
		t.Fatal("nested range request was not captured")
	}
	gotStr := got.(string)
	if !strings.HasPrefix(gotStr, fmt.Sprintf("bytes=%d-", nestedStart)) {
		t.Fatalf("nested Range = %q, want prefix bytes=%d-", gotStr, nestedStart)
	}
}

func TestDownloadHierarchicalSIDXHeadersPropagated(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var nestedAuth atomic.Value
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n == 2 {
				nestedAuth.Store(r.Header.Get("X-Custom-Auth"))
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
	if v := nestedAuth.Load(); v != "secret-token" {
		t.Fatalf("nested auth = %v, want secret-token", v)
	}
}

func TestDownloadHierarchicalSIDX200Fallback(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n <= 2 {
				// First two requests (root + nested SIDX) get 200 fallback.
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
	if len(contents) != 132 {
		t.Fatalf("contents length = %d, want 132", len(contents))
	}
}

func TestDownloadHierarchicalSIDXNoSIDXBytesInOutput(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
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
	// The output should only contain init + leaf media, no SIDX bytes.
	// Verify no 'sidx' box type marker in the media portion.
	mediaPortion := contents[100:]
	if strings.Contains(string(mediaPortion), "sidx") {
		t.Fatal("output contains SIDX bytes in media portion")
	}
}

func TestDownloadHierarchicalSIDXExcessiveDepth(t *testing.T) {
	// Build a chain of SIDX boxes deeper than maxSIDXDepth.
	// Each level has one index reference pointing to the next.
	boxes := make([][]byte, maxSIDXDepth+2) // one more than allowed
	leafRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	boxes[len(boxes)-1] = buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	for i := len(boxes) - 2; i >= 0; i-- {
		refs := []SIDXReference{{ReferencedSize: uint32(len(boxes[i+1])), SubsegmentDuration: 1000, IsIndex: true}}
		boxes[i] = buildSIDX(0, 1, 1000, 0, 0, refs)
	}
	var resource []byte
	for _, box := range boxes {
		resource = append(resource, box...)
	}
	resource = append(resource, make([]byte, 10)...)
	indexRange := fmt.Sprintf("0-%d", len(boxes[0])-1)

	var mediaRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"/></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			mediaRequests.Add(1)
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("err = %v, want depth limit error", err)
	}
	if got, want := mediaRequests.Load(), int32(maxSIDXDepth+1); got != want {
		t.Fatalf("media requests = %d, want %d; over-limit child must not be fetched", got, want)
	}
}

func TestDownloadHierarchicalSIDXExcessiveBoxCount(t *testing.T) {
	// A wide root reaches the box-count limit without first hitting the
	// independent depth limit.
	child := buildSIDX(0, 1, 1000, 0, 0, nil)
	rootRefs := make([]SIDXReference, maxSIDXBoxesPerRepresentation)
	for i := range rootRefs {
		rootRefs[i] = SIDXReference{
			ReferencedSize:     uint32(len(child)),
			SubsegmentDuration: 1000,
			IsIndex:            true,
		}
	}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, rootRefs)
	resource := append([]byte(nil), rootBox...)
	for range rootRefs {
		resource = append(resource, child...)
	}
	indexRange := fmt.Sprintf("0-%d", len(rootBox)-1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"/></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "box count") {
		t.Fatalf("err = %v, want box count limit error", err)
	}
}

func TestDownloadHierarchicalSIDXRepeatedRangeDetection(t *testing.T) {
	const (
		mediaURL = "https://media.example.test/video.mp4"
		start    = int64(100)
		length   = int64(32)
	)
	sidx := &SIDX{
		Timescale: 1,
		References: []SIDXReference{{
			ReferencedSize:     uint32(length),
			SubsegmentDuration: 1,
			IsIndex:            true,
		}},
	}
	state := &sidxExpansionState{
		mediaURL: mediaURL,
		visited: map[string]struct{}{
			rangeKey(mediaURL, start, length): {},
		},
		maxLeafCount: 1,
	}
	_, err := NewDownloader(nil, Config{}).expandSIDXReferences(
		context.Background(), sidx, start, 0, state)
	if err == nil || !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("err = %v, want repeated-range rejection", err)
	}
}

func TestDownloadHierarchicalSIDXTruncatedNested(t *testing.T) {
	// Root SIDX references a nested range that contains truncated data.
	rootRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000, IsIndex: true}}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, rootRefs)
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, []byte{0, 0, 0, 10, 's', 'i', 'd', 'x', 0, 0}...) // truncated
	indexRange := fmt.Sprintf("0-%d", len(rootBox)-1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"/></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "nested SIDX") {
		t.Fatalf("err = %v, want nested SIDX parse error", err)
	}
}

func TestDownloadHierarchicalSIDXLeafCountLimit(t *testing.T) {
	// Build a root SIDX with more leaf references than the configured limit.
	refs := make([]SIDXReference, 20)
	for i := range refs {
		refs[i] = SIDXReference{ReferencedSize: 10, SubsegmentDuration: 1000}
	}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, refs)
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, make([]byte, 200)...)
	indexRange := fmt.Sprintf("0-%d", len(rootBox)-1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"/></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	// Set MaxSegments to 10, but we have 20 leaf refs.
	_, err := NewDownloader(transport, Config{MaxSegments: 10}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil || !strings.Contains(err.Error(), "leaf segment count") {
		t.Fatalf("err = %v, want leaf count limit error", err)
	}
}

func TestDownloadHierarchicalSIDXNestedTransportFailure(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n == 2 {
				// Fail the nested SIDX fetch.
				w.WriteHeader(http.StatusForbidden)
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
		t.Fatal("expected error for nested transport failure")
	}
}

func TestDownloadHierarchicalSIDXCancellationDuringNestedFetch(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n == 2 {
				// Delay the nested SIDX fetch to allow cancellation.
				time.Sleep(200 * time.Millisecond)
			}
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

func TestDownloadHierarchicalSIDXNoOutputOnFailure(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			n := requestCount.Add(1)
			if n == 2 {
				// Fail nested fetch with invalid Content-Range.
				w.Header().Set("Content-Range", "bytes 0-0/999")
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte{0})
				return
			}
			serveRange(w, r, resource)
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

func TestDownloadHierarchicalSIDXMultiPeriod(t *testing.T) {
	// Two periods, each with a hierarchical SIDX representation.
	resource1, indexRange1 := hierarchicalTestMedia()
	resource2, indexRange2 := hierarchicalTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD mediaPresentationDuration="PT4S"><Period duration="PT2S"><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video1.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period><Period duration="PT2S"><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video2.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange1, indexRange2)
		case "/video1.mp4":
			serveRange(w, r, resource1)
		case "/video2.mp4":
			serveRange(w, r, resource2)
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
	if !result.MultiPeriod {
		t.Fatal("expected MultiPeriod=true")
	}
	if len(result.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(result.Tracks))
	}
	track := result.Tracks[0]
	if len(track.PeriodDownloads) != 2 {
		t.Fatalf("period downloads = %d", len(track.PeriodDownloads))
	}
	for i, pd := range track.PeriodDownloads {
		contents, _ := os.ReadFile(pd.Path)
		if len(contents) != 132 {
			t.Fatalf("period %d contents length = %d, want 132", i, len(contents))
		}
	}
}

// audioVideoHierarchicalMedia builds a resource with one video and one audio
// representation, each with a one-level hierarchical SIDX.
func audioVideoHierarchicalMedia() (video []byte, vIndexRange string, audio []byte, aIndexRange string) {
	mkRes := func(media []byte) ([]byte, string) {
		init := make([]byte, 100)
		for i := range init {
			init[i] = 'I'
		}
		leafRefs := []SIDXReference{
			{ReferencedSize: uint32(len(media)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		}
		leafBox := buildSIDX(0, 1, 48000, 0, 0, leafRefs)
		rootRefs := []SIDXReference{
			{ReferencedSize: uint32(len(leafBox)), SubsegmentDuration: 96000, IsIndex: true, StartsWithSAP: true, SAPType: 1},
		}
		rootBox := buildSIDX(0, 1, 48000, 0, 0, rootRefs)
		var res []byte
		res = append(res, init...)
		res = append(res, rootBox...)
		res = append(res, leafBox...)
		res = append(res, media...)
		return res, fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	}
	video, vIndexRange = mkRes([]byte("VIDEO_LEAF_BYTES___"))
	audio, aIndexRange = mkRes([]byte("AUDIO_LEAF_BYTES___"))
	return video, vIndexRange, audio, aIndexRange
}

func TestDownloadHierarchicalSIDXAudioVideo(t *testing.T) {
	video, vIndex, audio, aIndex := audioVideoHierarchicalMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="2000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet><AdaptationSet mimeType="audio/mp4"><Representation id="a" bandwidth="1000"><BaseURL>audio.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, vIndex, aIndex)
		case "/video.mp4":
			serveRange(w, r, video)
		case "/audio.mp4":
			serveRange(w, r, audio)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired {
		t.Fatal("expected MergeRequired=true for separate audio/video")
	}
	if len(result.Tracks) != 2 {
		t.Fatalf("tracks = %d, want 2", len(result.Tracks))
	}
	for _, track := range result.Tracks {
		contents, _ := os.ReadFile(track.Download.Path)
		// Each file = init(100) + media(19) = 119
		if len(contents) != 119 {
			t.Fatalf("%s contents length = %d, want 119", track.Representation.ID, len(contents))
		}
	}
}

// version1HierarchicalTestMedia builds a resource where the nested SIDX
// uses version 1 (64-bit EPT and first_offset).
func version1HierarchicalTestMedia() ([]byte, string) {
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("V1_NESTED_LEAF_ONE_")
	media2 := []byte("V1_NESTED_LEAF_TWO_")

	// Nested SIDX v1: two leaf references.
	nestedRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 90000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 90000, StartsWithSAP: true, SAPType: 2},
	}
	nestedBox := buildSIDX(1, 1, 90000, 1<<40, 0, nestedRefs)

	// Root SIDX: one index reference pointing to the nested SIDX.
	rootRefs := []SIDXReference{
		{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 90000, IsIndex: true},
	}
	rootBox := buildSIDX(0, 1, 90000, 0, 0, rootRefs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, rootBox...)
	resource = append(resource, nestedBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	return resource, indexRange
}

func TestDownloadHierarchicalSIDXVersion1Child(t *testing.T) {
	resource, indexRange := version1HierarchicalTestMedia()
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
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	// init(100) + media1(19) + media2(19) = 138
	if len(contents) != 138 {
		t.Fatalf("contents length = %d, want 138", len(contents))
	}
	if string(contents[100:119]) != "V1_NESTED_LEAF_ONE_" {
		t.Fatalf("media1 = %q", contents[100:119])
	}
	if string(contents[119:138]) != "V1_NESTED_LEAF_TWO_" {
		t.Fatalf("media2 = %q", contents[119:138])
	}
}

// buildSIDXExtendedSizeBox returns a SIDX with size_type=1 (64-bit extended size).
// The box has a 16-byte header instead of 8.
func buildSIDXExtendedSizeBox(version byte, referenceID, timescale uint32, ept, firstOffset uint64, refs []SIDXReference) []byte {
	body := buildSIDXBody(version, referenceID, timescale, ept, firstOffset, refs)
	total := uint64(16 + len(body))
	header := make([]byte, 16)
	binary.BigEndian.PutUint32(header[0:4], 1) // size_type=1
	copy(header[4:8], []byte("sidx"))
	binary.BigEndian.PutUint64(header[8:16], total)
	return append(header, body...)
}

// extendedSizeChildHierarchicalMedia uses a v1 nested SIDX with extended box size.
func extendedSizeChildHierarchicalMedia() ([]byte, string) {
	init := make([]byte, 100)
	for i := range init {
		init[i] = 'I'
	}
	media1 := []byte("EXT_LEAF_ONE_______")
	media2 := []byte("EXT_LEAF_TWO_______")

	nestedRefs := []SIDXReference{
		{ReferencedSize: uint32(len(media1)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
		{ReferencedSize: uint32(len(media2)), SubsegmentDuration: 48000, StartsWithSAP: true, SAPType: 1},
	}
	nestedBox := buildSIDXExtendedSizeBox(1, 1, 48000, 0, 0, nestedRefs)
	rootRefs := []SIDXReference{
		{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 96000, IsIndex: true},
	}
	rootBox := buildSIDX(0, 1, 48000, 0, 0, rootRefs)

	var resource []byte
	resource = append(resource, init...)
	resource = append(resource, rootBox...)
	resource = append(resource, nestedBox...)
	resource = append(resource, media1...)
	resource = append(resource, media2...)

	indexRange := fmt.Sprintf("100-%d", 100+len(rootBox)-1)
	return resource, indexRange
}

func TestDownloadHierarchicalSIDXExtendedSizeChild(t *testing.T) {
	resource, indexRange := extendedSizeChildHierarchicalMedia()
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
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	// init(100) + media1(19) + media2(19) = 138
	if len(contents) != 138 {
		t.Fatalf("contents length = %d, want 138", len(contents))
	}
	if string(contents[100:119]) != "EXT_LEAF_ONE_______" {
		t.Fatalf("media1 = %q", contents[100:119])
	}
	if string(contents[119:138]) != "EXT_LEAF_TWO_______" {
		t.Fatalf("media2 = %q", contents[119:138])
	}
}

func TestDownloadHierarchicalSIDXCumulativeByteBudget(t *testing.T) {
	const nestedLength = int64(5)
	resource := make([]byte, 200)
	transport := &memoryRangeTransport{data: resource}
	sidx := &SIDX{
		Timescale: 1,
		References: []SIDXReference{{
			ReferencedSize:     uint32(nestedLength),
			SubsegmentDuration: 1,
			IsIndex:            true,
		}},
	}
	state := &sidxExpansionState{
		mediaURL:     "https://media.example.test/video.mp4",
		visited:      make(map[string]struct{}),
		boxesParsed:  1,
		indexBytes:   maxCumulativeIndexBytes - (nestedLength - 1),
		maxLeafCount: 1,
	}
	_, err := NewDownloader(transport, Config{}).expandSIDXReferences(
		context.Background(), sidx, 100, 0, state)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("err = %v, want cumulative index transfer budget error", err)
	}
}

func TestDownloadHierarchicalSIDXTruncatedChildResponse(t *testing.T) {
	resource, indexRange := hierarchicalTestMedia()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			rangeHdr := r.Header.Get("Range")
			if strings.Contains(rangeHdr, "bytes=200-") {
				// Truncate the nested SIDX response to fewer bytes than the
				// valid request length. The Content-Range advertises the
				// requested range but the body is shorter.
				w.Header().Set("Content-Range", "bytes 200-220/"+fmt.Sprint(len(resource)))
				w.WriteHeader(http.StatusPartialContent)
				w.Write([]byte{0, 0, 0, 32, 's', 'i', 'd', 'x'})
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
		t.Fatal("expected error for truncated child response")
	}
}

// overflowFirstOffsetSIDX returns a v1 SIDX whose first_offset is the
// maximum uint64 value. When the absolute offset is computed as
// rangeStart + boxSize + first_offset, the addition overflows int64 and
// must be rejected. Version 0 truncates first_offset to uint32, so v1 is
// required to exercise the full uint64 overflow.
func overflowFirstOffsetSIDX() []byte {
	refs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	return buildSIDX(1, 1, 1000, 0, math.MaxUint64, refs)
}

func TestDownloadHierarchicalSIDXOffsetOverflow(t *testing.T) {
	// Build a single SIDX box whose first_offset is MaxUint64. The box sits
	// at offset 0 of a tiny resource. The fetch succeeds, the parser
	// succeeds, but the absolute first byte = 0 + boxSize + MaxUint64
	// overflows int64 and must be rejected.
	rootBox := overflowFirstOffsetSIDX()
	indexRange := fmt.Sprintf("0-%d", len(rootBox)-1)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/manifest.mpd":
			// No Initialization range: the entire resource is the index. The
			// overflow occurs when computing the absolute first byte of the
			// (sole) leaf reference; the test is satisfied if any error fires.
			fmt.Fprintf(w, `<MPD><Period><AdaptationSet mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"/></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			serveRange(w, r, rootBox)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("err = %v, want overflow error", err)
	}
}

func TestDownloadHierarchicalSIDXRoundTripV0Hex(t *testing.T) {
	// Read the on-disk sidx_hierarchical_v0.hex fixture, parse it as a SIDX
	// box, and confirm the round-trip interpretation matches expected structure.
	data, err := os.ReadFile("../../../conformance/media/dash/sidx_hierarchical_v0.hex")
	if err != nil {
		// When the test runs from a different working directory, fall back to
		// the relative path that matches the worktree layout.
		data, err = os.ReadFile("conformance/media/dash/sidx_hierarchical_v0.hex")
		if err != nil {
			t.Skipf("conformance fixture not available: %v", err)
		}
	}
	// Strip whitespace; the fixture is a hex-encoded big-endian binary.
	hexStr := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, string(data))
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex decode: %v", err)
	}
	sidx, offset, err := ParseSIDX(raw)
	if err != nil {
		t.Fatalf("ParseSIDX: %v", err)
	}
	if offset != 0 {
		t.Fatalf("offset = %d, want 0", offset)
	}
	if sidx.Timescale != 0xbb80 {
		t.Fatalf("timescale = %d, want 48000", sidx.Timescale)
	}
	if sidx.ReferenceID != 1 {
		t.Fatalf("referenceID = %d, want 1", sidx.ReferenceID)
	}
	if len(sidx.References) != 1 {
		t.Fatalf("references = %d, want 1", len(sidx.References))
	}
	if !sidx.References[0].IsIndex {
		t.Fatal("reference[0].IsIndex = false, want true")
	}
}

// static200Transport simulates a server that ignores Range and returns the
// complete resource with status 200.
type static200Transport struct {
	resource      []byte
	contentLength int64
}

func (transport *static200Transport) Do(_ context.Context, _ *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode:    http.StatusOK,
		Body:          io.NopCloser(bytes.NewReader(transport.resource)),
		ContentLength: transport.contentLength,
		Header:        http.Header{},
	}, nil
}

func (transport *static200Transport) ReadPage(_ context.Context, _ string) ([]byte, http.Header, error) {
	return transport.resource, http.Header{}, nil
}

func TestFetchIndexRange200ExceedsBudget(t *testing.T) {
	const budget = 64
	transport := &static200Transport{
		resource:      make([]byte, budget+1),
		contentLength: -1,
	}
	result, err := NewDownloader(transport, Config{}).fetchIndexRange(
		context.Background(), "https://media.example.test/video.mp4", 0, 8, budget)
	if err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("err = %v, want cumulative budget error", err)
	}
	if result.TransferredBytes != budget+1 {
		t.Fatalf("transferred bytes = %d, want %d", result.TransferredBytes, budget+1)
	}
}

func TestFetchIndexRangeExactBudgetBoundary(t *testing.T) {
	const budget = 64
	resource := bytes.Repeat([]byte{0x5a}, budget)
	transport := &static200Transport{
		resource:      resource,
		contentLength: budget,
	}
	result, err := NewDownloader(transport, Config{}).fetchIndexRange(
		context.Background(), "https://media.example.test/video.mp4", 8, 16, budget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.TransferredBytes != budget {
		t.Fatalf("transferred bytes = %d, want %d", result.TransferredBytes, budget)
	}
	if !bytes.Equal(result.Data, resource[8:24]) {
		t.Fatalf("data = %x, want requested slice", result.Data)
	}
}

func TestFetchIndexRange206ShortResponseChargesTransferredBytes(t *testing.T) {
	transport := &fixedIndexResponseTransport{
		status: http.StatusPartialContent, body: []byte("short"), contentRange: "bytes 0-7/8",
	}
	result, err := NewDownloader(transport, Config{Attempts: 1}).fetchIndexRange(
		context.Background(), "https://media.example.test/video.mp4", 0, 8, 64)
	if err == nil || !strings.Contains(err.Error(), "response length") {
		t.Fatalf("err = %v, want short 206 length error", err)
	}
	if result.TransferredBytes != 5 {
		t.Fatalf("transferred bytes = %d, want 5", result.TransferredBytes)
	}
}

func TestFetchIndexRangeMismatchedContentRangeChargesNoBytes(t *testing.T) {
	transport := &fixedIndexResponseTransport{
		status: http.StatusPartialContent, body: []byte("ignored"), contentRange: "bytes 1-8/9",
	}
	result, err := NewDownloader(transport, Config{Attempts: 1}).fetchIndexRange(
		context.Background(), "https://media.example.test/video.mp4", 0, 8, 64)
	if err == nil || !strings.Contains(err.Error(), "Content-Range mismatch") {
		t.Fatalf("err = %v, want Content-Range mismatch", err)
	}
	if result.TransferredBytes != 0 {
		t.Fatalf("transferred bytes = %d, want 0 before body read", result.TransferredBytes)
	}
}

func TestFetchIndexRange200ExtractionFailureChargesTransferredBytes(t *testing.T) {
	transport := &fixedIndexResponseTransport{status: http.StatusOK, body: []byte("short"), contentLength: 5}
	result, err := NewDownloader(transport, Config{Attempts: 1}).fetchIndexRange(
		context.Background(), "https://media.example.test/video.mp4", 4, 8, 64)
	if err == nil || !strings.Contains(err.Error(), "too short") {
		t.Fatalf("err = %v, want 200 extraction length error", err)
	}
	if result.TransferredBytes != 5 {
		t.Fatalf("transferred bytes = %d, want 5", result.TransferredBytes)
	}
}

func TestExpandSIDXParseFailureChargesTransferredBytes(t *testing.T) {
	const body = "not-sidx"
	transport := &fixedIndexResponseTransport{
		status: http.StatusOK, body: []byte(body), contentLength: int64(len(body)),
	}
	session := &sidxSessionState{}
	_, err := NewDownloader(transport, Config{Attempts: 1}).expandOneSIDX(
		context.Background(), Segment{URL: "https://media.example.test/video.mp4", IndexRange: "0-7"}, session)
	if err == nil || !strings.Contains(err.Error(), "SIDX") {
		t.Fatalf("err = %v, want SIDX parse error", err)
	}
	if session.indexBytes != int64(len(body)) {
		t.Fatalf("session index bytes = %d, want %d", session.indexBytes, len(body))
	}
}

type fixedIndexResponseTransport struct {
	status        int
	body          []byte
	contentLength int64
	contentRange  string
}

func (transport *fixedIndexResponseTransport) Do(_ context.Context, _ *http.Request) (*http.Response, error) {
	header := http.Header{}
	if transport.contentRange != "" {
		header.Set("Content-Range", transport.contentRange)
	}
	return &http.Response{
		StatusCode:    transport.status,
		Body:          io.NopCloser(bytes.NewReader(transport.body)),
		ContentLength: transport.contentLength,
		Header:        header,
	}, nil
}

func (transport *fixedIndexResponseTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, fmt.Errorf("unexpected MPD request")
}

func TestDownloadHierarchicalSIDXNoPartialPlanAfterNestedFetchFailure(t *testing.T) {
	// Build a hierarchical resource where the nested fetch will fail.
	nestedRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	nestedBox := buildSIDX(0, 1, 1000, 0, 0, nestedRefs)
	rootRefs := []SIDXReference{{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 1000, IsIndex: true}}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, rootRefs)
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, nestedBox...)
	resource = append(resource, make([]byte, 10)...)

	// Transport that fails the second request (nested fetch).
	transport := &failingAfterNTransport{data: resource, failAfter: 1}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("0-%d", len(rootBox)-1),
	}
	segments, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err == nil {
		t.Fatal("expected error for nested transport failure")
	}
	if len(segments) != 0 {
		t.Fatalf("segments = %d, want no partial plan after failure", len(segments))
	}
}

// failingAfterNTransport fails after N successful requests.
type failingAfterNTransport struct {
	data      []byte
	failAfter int
	count     int
}

func (f *failingAfterNTransport) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	f.count++
	if f.count > f.failAfter {
		return nil, fmt.Errorf("simulated transport failure")
	}
	rangeHeader := req.Header.Get("Range")
	if rangeHeader == "" {
		return &http.Response{
			StatusCode:    http.StatusOK,
			Body:          io.NopCloser(bytes.NewReader(f.data)),
			ContentLength: int64(len(f.data)),
			Header:        http.Header{},
		}, nil
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	}
	if start >= int64(len(f.data)) || end >= int64(len(f.data)) || start > end {
		return &http.Response{StatusCode: http.StatusRequestedRangeNotSatisfiable, Body: io.NopCloser(bytes.NewReader(nil)), Header: http.Header{}}, nil
	}
	header := http.Header{}
	header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(f.data)))
	return &http.Response{
		StatusCode:    http.StatusPartialContent,
		Body:          io.NopCloser(bytes.NewReader(f.data[start : end+1])),
		ContentLength: end - start + 1,
		Header:        header,
	}, nil
}

func (f *failingAfterNTransport) ReadPage(_ context.Context, _ string) ([]byte, http.Header, error) {
	return f.data, http.Header{}, nil
}

func TestDownloadHierarchicalSIDXInitOverlapsRootIndex(t *testing.T) {
	// Build a resource where init range overlaps the root index range but not
	// the media. Place the SIDX at offset 100, media after it.
	leafRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	leafBox := buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	var resource []byte
	resource = append(resource, make([]byte, 100)...) // padding before index
	resource = append(resource, leafBox...)
	resource = append(resource, make([]byte, 10)...)

	transport := &memoryRangeTransport{data: resource}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	// The initialization ends halfway through the SIDX, before media starts.
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("100-%d", 100+len(leafBox)-1),
		InitRange:  fmt.Sprintf("0-%d", 100+len(leafBox)/2),
	}
	_, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err == nil {
		t.Fatal("expected error for init/index overlap")
	}
	if !strings.Contains(err.Error(), "initialization range overlaps index interval") {
		t.Fatalf("err = %v, want init/index overlap error", err)
	}
}

func TestDownloadHierarchicalSIDXInitOverlapsNestedIndex(t *testing.T) {
	// Leave a gap between root and nested indexes so the initialization range
	// can overlap only the nested interval.
	const nestedGap = 64
	nestedRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	nestedBox := buildSIDX(0, 1, 1000, 0, 0, nestedRefs)
	rootRefs := []SIDXReference{{ReferencedSize: uint32(len(nestedBox)), SubsegmentDuration: 1000, IsIndex: true}}
	rootBox := buildSIDX(0, 1, 1000, 0, nestedGap, rootRefs)

	// Layout: [root SIDX][gap][nested SIDX][media].
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, make([]byte, nestedGap)...)
	nestedStart := len(rootBox) + nestedGap
	resource = append(resource, nestedBox...)
	resource = append(resource, make([]byte, 10)...)

	transport := &memoryRangeTransport{data: resource}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("0-%d", len(rootBox)-1),
		InitRange:  fmt.Sprintf("%d-%d", nestedStart, nestedStart+len(nestedBox)-1),
	}
	_, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err == nil {
		t.Fatal("expected error for init/nested-index overlap")
	}
	if !strings.Contains(err.Error(), "initialization range overlaps index interval 1") {
		t.Fatalf("err = %v, want nested-index overlap error", err)
	}
}

func TestDownloadHierarchicalSIDXLeafOverlapsRootIndex(t *testing.T) {
	// A hostile indexRange includes trailing bytes beyond the SIDX box, while
	// the first leaf begins immediately after the actual box.
	leafRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	leafBox := buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	var resource []byte
	resource = append(resource, leafBox...)
	resource = append(resource, make([]byte, 10)...)

	transport := &memoryRangeTransport{data: resource}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("0-%d", len(leafBox)+4),
	}
	_, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err == nil || !strings.Contains(err.Error(), "leaf media range 0 overlaps index interval 0") {
		t.Fatalf("err = %v, want leaf/root-index overlap error", err)
	}
}

func TestDownloadHierarchicalSIDXAdjacentRangesSucceed(t *testing.T) {
	// Build a resource where index and leaf ranges are exactly adjacent
	// (no gap, no overlap). This should succeed.
	leafRefs := []SIDXReference{{ReferencedSize: 16, SubsegmentDuration: 1000}}
	leafBox := buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	media := []byte("ADJACENT_MEDIA__")
	var resource []byte
	resource = append(resource, leafBox...)
	resource = append(resource, media...)

	transport := &memoryRangeTransport{data: resource}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("0-%d", len(leafBox)-1),
	}
	segments, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(segments))
	}
	// Leaf should start exactly at len(leafBox).
	if segments[0].RangeStart != int64(len(leafBox)) {
		t.Fatalf("leaf start = %d, want %d", segments[0].RangeStart, len(leafBox))
	}
}

func TestDownloadHierarchicalSIDXNearMaxInt64Interval(t *testing.T) {
	// Test that near-MaxInt64 offsets don't cause overflow or panic.
	// Use a version 1 SIDX with a huge first_offset that will overflow
	// when added to the box size.
	refs := []SIDXReference{{ReferencedSize: 100, SubsegmentDuration: 1000}}
	// first_offset = MaxInt64 will overflow when the box size is added.
	data := buildSIDX(1, 1, 1000, 0, uint64(math.MaxInt64), refs)

	transport := &memoryRangeTransport{data: data}
	downloader := NewDownloader(transport, Config{MaxSegments: 100})
	marker := Segment{
		URL:        "https://media.example.test/video.mp4",
		IndexRange: fmt.Sprintf("0-%d", len(data)-1),
	}
	// This should fail with an overflow error, not panic.
	_, err := downloader.expandOneSIDX(context.Background(), marker, nil)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("err = %v, want overflow error", err)
	}
}
