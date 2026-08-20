package engine

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

	mediaformat "github.com/tejasa97/ytdlp-go/internal/format"
	"github.com/tejasa97/ytdlp-go/internal/network"
)

const (
	dispatchSIDXInitSize     = 100
	dispatchSIDXIndexStart   = 100
	dispatchSIDXIndexSlotLen = 100
	dispatchSIDXMediaStart   = dispatchSIDXIndexStart + dispatchSIDXIndexSlotLen
)

func appendDispatchUint16(buf []byte, value uint16) []byte {
	return append(buf, byte(value>>8), byte(value))
}

func appendDispatchUint32(buf []byte, value uint32) []byte {
	return append(buf, byte(value>>24), byte(value>>16), byte(value>>8), byte(value))
}

func buildDispatchSIDXBoxBytes(firstOffset uint64, sizes ...uint32) []byte {
	body := []byte{0, 0, 0, 0, 0, 0, 0, 1, 0, 0, 0x75, 0x30}
	body = appendDispatchUint32(body, 0)
	body = appendDispatchUint32(body, uint32(firstOffset))
	body = append(body, 0, 0)
	body = appendDispatchUint16(body, uint16(len(sizes)))
	for _, size := range sizes {
		body = appendDispatchUint32(body, size)
		body = appendDispatchUint32(body, 48000)
		body = appendDispatchUint32(body, 0x80000000|1<<28)
	}
	boxSize := uint32(8 + len(body))
	box := appendDispatchUint32(nil, boxSize)
	box = append(box, 's', 'i', 'd', 'x')
	return append(box, body...)
}

func buildDispatchSIDXIndexSlot(firstOffset uint64, sizes ...uint32) []byte {
	box := buildDispatchSIDXBoxBytes(firstOffset, sizes...)
	indexSlot := make([]byte, dispatchSIDXIndexSlotLen)
	copy(indexSlot, box)
	return indexSlot
}

func buildDispatchDynamicSIDXResource(leaves ...[]byte) ([]byte, string) {
	init := make([]byte, dispatchSIDXInitSize)
	for i := range init {
		init[i] = 'I'
	}
	sizes := make([]uint32, len(leaves))
	for i, leaf := range leaves {
		sizes[i] = uint32(len(leaf))
	}
	probe := buildDispatchSIDXBoxBytes(0, sizes...)
	firstOffset := uint64(dispatchSIDXMediaStart - (dispatchSIDXIndexStart + len(probe)))
	resource := append([]byte(nil), init...)
	resource = append(resource, buildDispatchSIDXIndexSlot(firstOffset, sizes...)...)
	for _, leaf := range leaves {
		resource = append(resource, leaf...)
	}
	indexRange := fmt.Sprintf("%d-%d", dispatchSIDXIndexStart, dispatchSIDXIndexStart+dispatchSIDXIndexSlotLen-1)
	return resource, indexRange
}

func serveDispatchRange(w http.ResponseWriter, r *http.Request, resource []byte) {
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(resource)
		return
	}
	var start, end int64
	if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if start >= int64(len(resource)) || end >= int64(len(resource)) || start > end {
		w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		return
	}
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(resource)))
	w.WriteHeader(http.StatusPartialContent)
	_, _ = w.Write(resource[start : end+1])
}

func TestDASHDownloadLiveOptionsEnableDynamicSIDXPolling(t *testing.T) {
	leaf1 := []byte("DISPATCH_LEAF_ONE_____")
	leaf2 := []byte("DISPATCH_LEAF_TWO_____")
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/live.mpd":
			poll := polls.Add(1)
			_, indexRange := buildDispatchDynamicSIDXResource(leaf1)
			if poll > 1 {
				_, indexRange = buildDispatchDynamicSIDXResource(leaf1, leaf2)
			}
			fmt.Fprintf(w, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="v" bandwidth="1000"><BaseURL>video.mp4</BaseURL><SegmentBase indexRange="%s"><Initialization range="0-99"/></SegmentBase></Representation></AdaptationSet></Period></MPD>`, indexRange)
		case "/video.mp4":
			resource, _ := buildDispatchDynamicSIDXResource(leaf1)
			if polls.Load() > 1 {
				resource, _ = buildDispatchDynamicSIDXResource(leaf1, leaf2)
			}
			serveDispatchRange(w, r, resource)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operation := &operation{
		client: newBroadTestClient(), transport: transport,
		request: Request{
			OutputDir: root,
			Downloader: DownloaderOptions{
				LiveMaxPolls: 2, LivePollInterval: time.Millisecond,
			},
		},
	}
	destination := filepath.Join(root, "dispatch.mp4")
	path, _, err := operation.downloadSelection(context.Background(), mediaformat.Selection{
		URL: server.URL + "/live.mpd", Ext: "mp4",
		Protocol: "http_dash_segments", VCodec: "mpeg4", ACodec: "none",
	}, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if polls.Load() != 2 {
		t.Fatalf("polls = %d, want 2", polls.Load())
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Repeat("I", 100) + string(leaf1) + string(leaf2)
	if string(contents) != want {
		t.Fatalf("contents = %q, want %q", contents, want)
	}
}
