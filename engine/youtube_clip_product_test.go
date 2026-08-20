package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/network"
)

// clipPageRoundTripper serves the clip page and the source watch page over
// youtube.com URLs with a custom transport; the media URL in the source watch
// page points at a real media server so the ffmpeg section consumer can reach
// it.
type clipPageRoundTripper struct {
	clip     []byte
	watch    []byte
	watchURL string
}

func (transport *clipPageRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.String() {
	case transport.watchURL:
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(bytes.NewReader(transport.watch)), Request: request}, nil
	default:
		// The clip URL is the request URL; serve its page.
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/html"}}, Body: io.NopCloser(bytes.NewReader(transport.clip)), Request: request}, nil
	}
}

// TestProductYouTubeClipSectionDownload exercises the full clip pipeline end
// to end: a synthetic /clip/<id> page resolves to a source watch page, the
// source video extraction runs, and the clip's section_start/section_end drive
// PR4's section consumer to produce a clipped artifact carrying the clip
// identity. The media is served over a real HTTP server so ffmpeg (the section
// consumer) can reach it directly.
func TestProductYouTubeClipSectionDownload(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	mediaPath := filepath.Join(t.TempDir(), "source.mp4")
	if output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i",
		"color=c=black:s=32x32:d=2", "-c:v", "mpeg4", mediaPath).CombinedOutput(); err != nil {
		t.Fatalf("generate source: %v: %s", err, output)
	}
	mediaBytes, err := os.ReadFile(mediaPath)
	if err != nil {
		t.Fatal(err)
	}

	clipID := "UgytABC-PRODUCT0ABC12345"
	watchURL := "https://www.youtube.com/watch?v=abcdefghijk"
	// The media is served over a real HTTP server so the ffmpeg section
	// consumer can reach it directly.
	mediaServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodHead {
			writer.WriteHeader(http.StatusOK)
			return
		}
		_, _ = writer.Write(mediaBytes)
	}))
	defer mediaServer.Close()
	pageTransport := &clipPageRoundTripper{
		clip:     buildClipProductPage("abcdefghijk"),
		watch:    buildClipSourceWatchPage(mediaServer.URL + "/media.mp4"),
		watchURL: watchURL,
	}
	client := newBroadTestClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = pageTransport
		return network.New(config)
	}))

	clipURL := "https://www.youtube.com/clip/" + clipID
	root := t.TempDir()
	result, err := client.Run(context.Background(), Request{
		URL: clipURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Overwrite: true,
	})
	if err != nil {
		t.Fatalf("clip run: %v", err)
	}
	if id, _ := lookupInfoString(result, "id"); id != clipID {
		t.Fatalf("id = %q; want clip id", id)
	}
	if mediaType, _ := lookupInfoString(result, "media_type"); mediaType != "clip" {
		t.Fatalf("media_type = %q", mediaType)
	}
	if !result.Downloaded {
		t.Fatal("clip not downloaded")
	}
	if filepath.Base(result.Filename) != clipID+".mp4" {
		t.Fatalf("filename = %q; want clip-id artifact", result.Filename)
	}
	if duration, err := probeDuration(result.Filename); err == nil && duration > 1.5 {
		t.Fatalf("clip artifact duration = %v; want ~1s (section)", duration)
	}
}

// TestProductYouTubeClipMissingSourceVideoID verifies a clip page without a
// source video id fails closed before producing media, and that the failure is
// reached through the real YouTube clip route (a youtube.com /clip/<id> URL),
// not the generic extractor.
func TestProductYouTubeClipMissingSourceVideoID(t *testing.T) {
	clipID := "UgytMissingPRODUCT0A12345"
	pageTransport := &clipPageRoundTripper{
		clip: buildClipProductPage(""),
	}
	client := newBroadTestClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = pageTransport
		return network.New(config)
	}))
	_, err := client.Run(context.Background(), Request{
		URL: "https://www.youtube.com/clip/" + clipID, OutputDir: t.TempDir(), OutputTemplate: "%(id)s.%(ext)s", Overwrite: true,
	})
	if err == nil {
		t.Fatal("clip with missing source id succeeded")
	}
	if !strings.Contains(err.Error(), "Unable to find video ID") {
		t.Fatalf("error = %v; want pinned Unable-to-find-video-ID message", err)
	}
}

// buildClipProductPage constructs a synthetic clip page whose source is the
// given video id.
func buildClipProductPage(sourceID string) []byte {
	// Timing is supplied as integer strings to exercise the pinned int(...)
	// coercion through the full product pipeline.
	loop := map[string]any{
		"loopCommand": map[string]any{"startTimeMs": "0", "endTimeMs": "1000"},
	}
	commands := []any{loop}
	buttonCommand := map[string]any{"commandExecutorCommand": map[string]any{"commands": commands}}
	button := map[string]any{"buttonRenderer": map[string]any{"command": buttonCommand}}
	actionButton := map[string]any{"actionButton": button}
	notification := map[string]any{"notificationActionRenderer": actionButton}
	popup := map[string]any{"popup": notification}
	openPopup := map[string]any{"openPopupAction": popup}
	onScrub := map[string]any{"commandExecutorCommand": map[string]any{"commands": []any{openPopup}}}
	attribution := map[string]any{"clipAttributionRenderer": map[string]any{"onScrubExit": onScrub}}
	contents := map[string]any{"clipSectionRenderer": map[string]any{"contents": []any{attribution}}}
	content := map[string]any{"engagementPanelSectionListRenderer": map[string]any{"content": contents}}
	binding := map[string]any{
		"currentVideoEndpoint": map[string]any{"watchEndpoint": map[string]any{"videoId": sourceID}},
		"engagementPanels":     []any{content},
	}
	raw, err := json.Marshal(binding)
	if err != nil {
		panic(fmt.Sprintf("build clip product page: %v", err))
	}
	return []byte("<html><script>var ytInitialData = " + string(raw) + ";</script></html>")
}

// buildClipSourceWatchPage constructs a source watch page with one direct
// format URL.
func buildClipSourceWatchPage(mediaURL string) []byte {
	player := fmt.Sprintf(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"abcdefghijk","title":"Clip Source","lengthSeconds":"2","author":"fixture channel","channelId":"UCfixture000000000000000"},"streamingData":{"formats":[{"format_id":"18","itag":18,"url":%q,"ext":"mp4","vcodec":"mpeg4","acodec":"none","mimeType":"video/mp4"}],"adaptiveFormats":[]}}`, mediaURL)
	return []byte("<html><script>var ytInitialPlayerResponse = " + player + ";</script></html>")
}

// lookupInfoString reads a string field from a JsonOut result.
func lookupInfoString(result Result, key string) (string, bool) {
	if len(result.InfoJSON) == 0 {
		return "", false
	}
	var fields map[string]any
	if err := json.Unmarshal(result.InfoJSON, &fields); err != nil {
		return "", false
	}
	value, ok := fields[key].(string)
	return value, ok
}
