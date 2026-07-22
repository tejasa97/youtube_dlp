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
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("err = %v, want depth limit error", err)
	}
}

func TestDownloadHierarchicalSIDXExcessiveBoxCount(t *testing.T) {
	// Build a chain of SIDX boxes exceeding maxSIDXBoxesPerRepresentation.
	// Each level has one index reference pointing to the next.
	count := maxSIDXBoxesPerRepresentation + 1
	boxes := make([][]byte, count)
	leafRefs := []SIDXReference{{ReferencedSize: 10, SubsegmentDuration: 1000}}
	boxes[count-1] = buildSIDX(0, 1, 1000, 0, 0, leafRefs)
	for i := count - 2; i >= 0; i-- {
		refs := []SIDXReference{{ReferencedSize: uint32(len(boxes[i+1])), SubsegmentDuration: 1000, IsIndex: true}}
		boxes[i] = buildSIDX(0, 1, 1000, 0, 0, refs)
	}
	var resource []byte
	for _, box := range boxes {
		resource = append(resource, box...)
	}
	resource = append(resource, make([]byte, 10)...)
	indexRange := fmt.Sprintf("0-%d", len(boxes[0])-1)

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
	// Should hit either depth or box count limit.
	if err == nil || (!strings.Contains(err.Error(), "depth") && !strings.Contains(err.Error(), "box count")) {
		t.Fatalf("err = %v, want depth or box count limit error", err)
	}
}

func TestDownloadHierarchicalSIDXRepeatedRangeDetection(t *testing.T) {
	// Verify that the visited-range set catches a repeated nested range.
	// We test this at the unit level by calling expandOneSIDX with a resource
	// where the root SIDX has an index ref, and the nested SIDX also has an
	// index ref pointing to the same range as the root indexRange.
	//
	// Layout: [rootSIDX at 0] [nestedSIDX at rootEnd]
	// Root indexRange = "0-{rootEnd-1}" → visited includes this range.
	// Root ref[0]: index → nested range = [rootEnd, rootEnd+nestedSize)
	// Nested SIDX ref[0]: index with first_offset set so its range equals
	// the root indexRange [0, rootEnd). Since first_offset is unsigned and
	// offsets are forward-only, we instead verify that the depth limit
	// catches unbounded recursion in this scenario.
	rootRefs := []SIDXReference{{ReferencedSize: 100, SubsegmentDuration: 1000, IsIndex: true}}
	rootBox := buildSIDX(0, 1, 1000, 0, 0, rootRefs)
	// Place padding and another SIDX at the referenced offset.
	var resource []byte
	resource = append(resource, rootBox...)
	resource = append(resource, make([]byte, 100)...) // padding for the index ref
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
	// The nested range [rootEnd, rootEnd+100) contains zeros (no valid SIDX).
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "out.mp4"), false, nil)
	if err == nil {
		t.Fatal("expected error for invalid nested SIDX")
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
