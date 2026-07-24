package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubeump"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestYouTubeSABRProductDownloadDispatch(t *testing.T) {
	var lastRequest *http.Request
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			lastRequest = request
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	path, bytesWritten, err := operation.downloadSelection(context.Background(), sabrSelection(t), root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytesWritten != int64(len("INITfixture")) {
		t.Fatalf("path=%q bytes=%d", path, bytesWritten)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "INITfixture" {
		t.Fatalf("payload=%q", got)
	}
	if lastRequest == nil ||
		lastRequest.Method != http.MethodPost ||
		lastRequest.Header.Get("Content-Type") != "application/x-protobuf" ||
		lastRequest.Header.Get("Accept") != "application/vnd.yt-ump" {
		t.Fatalf("request=%#v", lastRequest)
	}
	if lastRequest.Header.Get("Cookie") != "" || lastRequest.Header.Get("Authorization") != "" {
		t.Fatalf("credential headers leaked: %#v", lastRequest.Header)
	}
	if !strings.Contains(lastRequest.URL.String(), "rn=0") {
		t.Fatalf("url=%s", lastRequest.URL)
	}
}

func TestYouTubeSABRProductDownloadViaDefaultSelection(t *testing.T) {
	info := sabrSelectionInfoWithAudio(t)
	selections, err := format.Default(info, format.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(selections) != 2 {
		t.Fatalf("selections=%d", len(selections))
	}
	for _, selection := range selections {
		if selection.URL != "" {
			t.Fatalf("SABR selection must keep empty metadata URL, got %q", selection.URL)
		}
		if !selection.YouTubeSABR || selection.YouTubeSABRServerURL == "" {
			t.Fatalf("selection=%+v", selection)
		}
	}
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	path, bytesWritten, err := operation.downloadSelection(context.Background(), selections[0], root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytesWritten != int64(len("INITfixture")) {
		t.Fatalf("path=%q bytes=%d", path, bytesWritten)
	}
}

func TestYouTubeSABRRejectsExternalDownloader(t *testing.T) {
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		transport: transport,
		request: Request{
			Downloader: DownloaderOptions{External: &ExternalDownloader{Executable: "curl"}},
			Overwrite:  true,
		},
	}
	root := t.TempDir()
	_, _, err = operation.downloadSelection(context.Background(), sabrSelection(t), root, filepath.Join(root, "out.mp4"), nil)
	if err == nil || !strings.Contains(err.Error(), "external downloaders") {
		t.Fatalf("err=%v", err)
	}
	if got := categorized("download SABR", err); !IsCategory(got, ErrorUnsupported) {
		t.Fatalf("category=%v", got)
	}
}

func TestYouTubeSABRCategorizesFailures(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{youtubeump.ErrMissingConfig, ErrorInvalidInput},
		{youtubeump.ErrUnsupportedDirective, ErrorUnsupported},
		{youtubeump.ErrRedirect, ErrorNetwork},
		{youtubeump.ErrInvalidMediaState, ErrorNetwork},
		{youtubeump.ErrTruncatedStream, ErrorNetwork},
		{youtubeump.ErrRoundsExhausted, ErrorNetwork},
		{context.Canceled, ErrorCancelled},
	} {
		if got := categorized("SABR", test.err); !IsCategory(got, test.category) {
			t.Fatalf("categorized(%v)=%v want %s", test.err, got, test.category)
		}
	}
}

func TestYouTubeSABRRequestFailurePreservesCause(t *testing.T) {
	wrapped := fmt.Errorf("%w: redacted: %w", youtubeump.ErrDownloadFailed, context.Canceled)
	if got := categorized("SABR", wrapped); !IsCategory(got, ErrorCancelled) || !errors.Is(got, context.Canceled) {
		t.Fatalf("got=%v", got)
	}
}

func TestYouTubeSABRRejectedProviderDoesNotBlockDownload(t *testing.T) {
	client := NewClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{
			ProviderName: "fixture",
			Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
				return YouTubePOTResponse{}, errors.New("secret-provider-detail")
			},
		}},
	}))
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client:    client,
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	_, _, err = operation.downloadSelection(context.Background(), sabrSelection(t), root, destination, nil)
	if err != nil {
		t.Fatalf("err=%v", err)
	}
}

func TestYouTubeSABRExtractionJSONOmitsPOTToken(t *testing.T) {
	info := sabrSelectionInfo(t)
	encoded, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "pot_token") || strings.Contains(string(encoded), "Zm9v") {
		t.Fatalf("json=%s", encoded)
	}
}

func TestYouTubeSABRPOTResolvedAtDownloadWithoutMetadataLeak(t *testing.T) {
	const secret = "c2VjcmV0LXRva2Vu"
	client := NewClient(WithYouTubePOTProviders(YouTubePOTConfig{
		Policy: YouTubePOTFetchAlways,
		Providers: []YouTubePOTProvider{YouTubePOTProviderFunc{
			ProviderName: "fixture",
			Function: func(context.Context, YouTubePOTRequest) (YouTubePOTResponse, error) {
				return YouTubePOTResponse{Token: secret}, nil
			},
		}},
	}))
	body := sabrFixtureUMP(t, 137, []byte("INIT"), []byte("fixture"))
	var lastBody []byte
	transport, err := network.New(network.Config{
		RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			lastBody, _ = io.ReadAll(request.Body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       io.NopCloser(bytes.NewReader(body)),
				Request:    request,
			}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	selection := sabrSelection(t)
	selection.YouTubeSABRVideoID = "fixture0001"
	selection.YouTubeSABRClientName = "WEB"
	operation := &operation{
		client:    client,
		transport: transport,
		request:   Request{Downloader: DownloaderOptions{}, Overwrite: true},
	}
	root := t.TempDir()
	destination := filepath.Join(root, "out.mp4")
	_, _, err = operation.downloadSelection(context.Background(), selection, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(lastBody, []byte("secret-token")) {
		t.Fatalf("PO token not forwarded to request body")
	}
	if strings.Contains(string(lastBody), secret) {
		t.Fatalf("raw token leaked on wire encoding")
	}
	encoded, err := json.Marshal(sabrSelectionInfo(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), secret) || strings.Contains(string(encoded), "secret-token") {
		t.Fatalf("token leaked into metadata json")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func sabrSelection(t *testing.T) format.Selection {
	t.Helper()
	selection, err := format.Best(sabrSelectionInfo(t))
	if err != nil {
		t.Fatal(err)
	}
	selection.YouTubeSABRDurationSec = 10
	selection.YouTubeSABRVideoID = "fixture0001"
	selection.YouTubeSABRClientName = "WEB"
	return selection
}

func sabrSelectionInfo(t *testing.T) value.Info {
	t.Helper()
	return value.NewInfo(value.NewObject(
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "format_id", Value: value.String("137")},
			value.Field{Key: "ext", Value: value.String("mp4")},
			value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
			value.Field{Key: "vcodec", Value: value.String("avc1")},
			value.Field{Key: "acodec", Value: value.String("none")},
			value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
			value.Field{Key: "_youtube_sabr_track", Value: value.String("video")},
			value.Field{Key: "_youtube_sabr_itag", Value: value.Int(137)},
			value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
			value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
			value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
			value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
			value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
			value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
			value.Field{Key: "_youtube_client", Value: value.String("WEB")},
			value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
		)))},
	))
}

func sabrSelectionInfoWithAudio(t *testing.T) value.Info {
	t.Helper()
	return value.NewInfo(value.NewObject(
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("137")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
				value.Field{Key: "vcodec", Value: value.String("avc1")},
				value.Field{Key: "acodec", Value: value.String("none")},
				value.Field{Key: "height", Value: value.Int(1080)},
				value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
				value.Field{Key: "_youtube_sabr_track", Value: value.String("video")},
				value.Field{Key: "_youtube_sabr_itag", Value: value.Int(137)},
				value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
				value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
				value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
				value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
				value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
				value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
				value.Field{Key: "_youtube_client", Value: value.String("WEB")},
				value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("140")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "protocol", Value: value.String("youtube_sabr_ump")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("mp4a")},
				value.Field{Key: "_youtube_sabr", Value: value.Bool(true)},
				value.Field{Key: "_youtube_sabr_track", Value: value.String("audio")},
				value.Field{Key: "_youtube_sabr_itag", Value: value.Int(140)},
				value.Field{Key: "_youtube_sabr_server_url", Value: value.String("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken")},
				value.Field{Key: "_youtube_sabr_ustreamer_config", Value: value.String("Zml4dHVyZS11c3RyZWFtZXI=")},
				value.Field{Key: "_youtube_sabr_client_id", Value: value.Int(1)},
				value.Field{Key: "_youtube_sabr_client_version", Value: value.String("fixture")},
				value.Field{Key: "_youtube_sabr_user_agent", Value: value.String("fixture-agent")},
				value.Field{Key: "_youtube_sabr_duration_sec", Value: value.Int(10)},
				value.Field{Key: "_youtube_client", Value: value.String("WEB")},
				value.Field{Key: "_youtube_sabr_video_id", Value: value.String("fixture0001")},
			)),
		)},
	))
}

func sabrFixtureUMP(t *testing.T, itag int32, init, media []byte) []byte {
	t.Helper()
	return bytes.Join([][]byte{
		encodeSabrPart(42, encodeSabrFormatInit(itag)),
		encodeSabrPart(20, encodeSabrMediaHeader(1, itag, true, 0, 0, int64(len(init)))),
		encodeSabrPart(21, append(encodeSabrUMPVarint(1), init...)),
		encodeSabrPart(22, encodeSabrUMPVarint(1)),
		encodeSabrPart(20, encodeSabrMediaHeader(2, itag, false, 0, 10000, int64(len(media)))),
		encodeSabrPart(21, append(encodeSabrUMPVarint(2), media...)),
		encodeSabrPart(22, encodeSabrUMPVarint(2)),
	}, nil)
}

func encodeSabrFormatInit(itag int32) []byte {
	return appendSabrKeyBytes(nil, 2, encodeSabrFieldVarint(nil, 1, uint64(itag)))
}

func encodeSabrMediaHeader(id uint32, itag int32, init bool, sequence uint64, duration, length int64) []byte {
	buf := encodeSabrFieldVarint(nil, 1, uint64(id))
	buf = encodeSabrFieldVarint(buf, 3, uint64(itag))
	if init {
		buf = encodeSabrFieldVarint(buf, 8, 1)
	}
	if sequence != 0 || init {
		buf = encodeSabrFieldVarint(buf, 9, sequence)
	}
	if duration != 0 {
		buf = encodeSabrFieldVarint(buf, 12, uint64(duration))
	}
	if length != 0 {
		buf = encodeSabrFieldVarint(buf, 14, uint64(length))
	}
	return buf
}

func encodeSabrPart(partType int, payload []byte) []byte {
	return append(append(encodeSabrUMPVarint(uint64(partType)), encodeSabrUMPVarint(uint64(len(payload)))...), payload...)
}

func encodeSabrUMPVarint(value uint64) []byte {
	switch {
	case value <= 0x7F:
		return []byte{byte(value)}
	case value <= 0x3FFF:
		return []byte{byte(0x80 | (value & 0x3F)), byte(value >> 6)}
	default:
		return []byte{0xF0, byte(value), byte(value >> 8), byte(value >> 16), byte(value >> 24)}
	}
}

func encodeSabrFieldVarint(buf []byte, field uint64, value uint64) []byte {
	key := field<<3 | 0
	buf = appendSabrU64(buf, key)
	return appendSabrU64(buf, value)
}

func appendSabrKeyBytes(buf []byte, field uint64, value []byte) []byte {
	key := field<<3 | 2
	buf = appendSabrU64(buf, key)
	buf = appendSabrU64(buf, uint64(len(value)))
	return append(buf, value...)
}

func appendSabrU64(buf []byte, value uint64) []byte {
	for value >= 0x80 {
		buf = append(buf, byte(value)|0x80)
		value >>= 7
	}
	return append(buf, byte(value))
}
