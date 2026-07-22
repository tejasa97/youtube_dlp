package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
)

func TestClientDASHMultiPeriodDispatchAndFixup(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	periodSegments := make([][]string, 2)
	for period := range periodSegments {
		prefix := fmt.Sprintf("p%d", period+1)
		manifestPath := filepath.Join(root, prefix+"-generated.mpd")
		command := exec.Command(ffmpegPath,
			"-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.25",
			"-an", "-c:v", "mpeg4", "-f", "dash", "-seg_duration", "0.1",
			"-init_seg_name", prefix+"-init.mp4", "-media_seg_name", prefix+"-$Number$.m4s", manifestPath)
		command.Dir = root
		if output, commandErr := command.CombinedOutput(); commandErr != nil {
			t.Fatalf("generate period %d: %v: %s", period, commandErr, output)
		}
		matches, globErr := filepath.Glob(filepath.Join(root, prefix+"-*.m4s"))
		if globErr != nil || len(matches) == 0 {
			t.Fatalf("period %d fragments = %v, error = %v", period, matches, globErr)
		}
		sort.Strings(matches)
		for _, match := range matches {
			periodSegments[period] = append(periodSegments[period], filepath.Base(match))
		}
	}

	var manifest strings.Builder
	manifest.WriteString(`<MPD type="static">`)
	for period, segments := range periodSegments {
		prefix := fmt.Sprintf("p%d", period+1)
		fmt.Fprintf(&manifest, `<Period id="%s"><AdaptationSet contentType="video" mimeType="video/mp4" codecs="mp4v.20.9"><Representation id="%s-video" bandwidth="200000" width="16" height="16"><SegmentList><Initialization sourceURL="%s-init.mp4"/>`, prefix, prefix, prefix)
		for _, segment := range segments {
			fmt.Fprintf(&manifest, `<SegmentURL media="%s"/>`, segment)
		}
		manifest.WriteString(`</SegmentList></Representation></AdaptationSet></Period>`)
	}
	manifest.WriteString(`</MPD>`)

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/manifest.mpd" {
			writer.Header().Set("Content-Type", "application/dash+xml")
			_, _ = writer.Write([]byte(manifest.String()))
			return
		}
		http.ServeFile(writer, request, filepath.Join(root, filepath.Base(request.URL.Path)))
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := NewClient().Run(ctx, Request{URL: server.URL + "/manifest.mpd", OutputDir: root, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{FFmpegPath: ffmpegPath})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, result.Filename)
	if err != nil || len(probe.Streams) != 1 || probe.Streams[0].CodecType != "video" {
		t.Fatalf("probe = %#v, error = %v", probe, err)
	}
	duration, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || duration < 0.4 {
		t.Fatalf("multi-period duration = %q, error = %v; want at least 0.4 seconds", probe.Format.Duration, err)
	}
	if matches, _ := filepath.Glob(result.Filename + ".*"); len(matches) != 0 {
		t.Fatalf("temporary multi-period tracks remain: %v", matches)
	}
}
