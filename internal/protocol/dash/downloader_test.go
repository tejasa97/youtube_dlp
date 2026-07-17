package dash

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestDownloadSeparateAudioVideoTracks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			_, _ = fmt.Fprint(writer, `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><SegmentTemplate duration="1" initialization="v-init" media="v-$Number$"/><Representation id="v" bandwidth="1000"/></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><SegmentTemplate duration="1" initialization="a-init" media="a-$Number$"/><Representation id="a" bandwidth="128"/></AdaptationSet>
</Period></MPD>`)
		case "/v-init":
			_, _ = writer.Write([]byte("VI"))
		case "/v-1", "/v-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		case "/a-init":
			_, _ = writer.Write([]byte("AI"))
		case "/a-1", "/a-2":
			_, _ = writer.Write([]byte(request.URL.Path[3:]))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/manifest.mpd", root, filepath.Join(root, "dash.mp4"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MergeRequired || len(result.Tracks) != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, track := range result.Tracks {
		contents, _ := os.ReadFile(track.Download.Path)
		if len(contents) != 4 {
			t.Fatalf("track %s contents = %q", track.Representation.ID, contents)
		}
	}
}

func TestDownloadDynamicMPDPollsAndDeduplicates(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.mpd" {
			repeat := 0
			if polls.Add(1) > 1 {
				repeat = 1
			}
			_, _ = fmt.Fprintf(writer, `<MPD type="dynamic" minimumUpdatePeriod="PT0.001S"><Period><AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><SegmentTemplate media="$Time$.m4s"><SegmentTimeline><S t="0" d="1" r="%d"/></SegmentTimeline></SegmentTemplate></Representation></AdaptationSet></Period></MPD>`, repeat)
			return
		}
		_, _ = writer.Write([]byte(request.URL.Path))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	result, err := NewDownloader(transport, Config{DynamicPolls: 2, PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/live.mpd", root, filepath.Join(root, "live.bin"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(result.Tracks[0].Download.Path)
	if string(contents) != "/0.m4s/1.m4s" || polls.Load() != 2 {
		t.Fatalf("contents = %q, polls = %d", contents, polls.Load())
	}
}
