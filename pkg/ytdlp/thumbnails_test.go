package ytdlp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	outputtemplate "github.com/tejasa97/youtube_dlp/internal/compat/template"
	"github.com/tejasa97/youtube_dlp/internal/downloader"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestSelectThumbnailsNormalizesSortsAndBoundsMetadata(t *testing.T) {
	t.Parallel()
	small := thumbnailValue("small", "https://cdn.example.test/small.webp", "webp", 1, 100, 100)
	smallObject, _ := small.Object()
	smallObject.Set("http_headers", value.ObjectValue(value.NewObject(
		value.Field{Key: "X-Thumbnail", Value: value.String("local")},
	)))
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "Authorization", Value: value.String("global-secret")},
		))},
		value.Field{Key: "thumbnails", Value: value.List(
			small,
			thumbnailValue("../hostile", "https://cdn.example.test/best.jpeg", "", 5, 1000, 720),
			thumbnailValue("bad", "https://user@cdn.example.test/bad.jpg", "jpg", 9, 2000, 1000),
		)},
	))
	tracks, err := selectThumbnails(&info)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].id != "small" || tracks[1].id != "1" ||
		tracks[1].extension != "jpeg" || tracks[0].headers.Get("Authorization") != "" ||
		tracks[0].headers.Get("X-Thumbnail") != "local" {
		t.Fatalf("tracks = %#v", tracks)
	}
	normalized, _ := info.Lookup("thumbnails").ListValue()
	if len(normalized) != 2 {
		t.Fatalf("normalized thumbnails = %#v", normalized)
	}

	singular := value.NewInfo(value.NewObject(
		value.Field{Key: "thumbnail", Value: value.String("https://cdn.example.test/cover")},
	))
	tracks, err = selectThumbnails(&singular)
	if err != nil || len(tracks) != 1 || tracks[0].extension != "jpg" {
		t.Fatalf("singular tracks=%#v error=%v", tracks, err)
	}
	if _, ok := singular.Lookup("thumbnails").ListValue(); !ok {
		t.Fatal("singular thumbnail was not promoted")
	}
	emptyExtension := value.NewInfo(value.NewObject(
		value.Field{Key: "thumbnails", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String("https://cdn.example.test/cover.png")},
			value.Field{Key: "ext", Value: value.String("  ")},
		)))},
	))
	if tracks, err := selectThumbnails(&emptyExtension); err != nil || len(tracks) != 1 || tracks[0].extension != "png" {
		t.Fatalf("empty-extension tracks=%#v error=%v", tracks, err)
	}
	unsafe := value.NewInfo(value.NewObject(
		value.Field{Key: "thumbnails", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String("https://user@cdn.example.test/image.jpg")})),
		)},
	))
	if tracks, err := selectThumbnails(&unsafe); err != nil || len(tracks) != 0 {
		t.Fatalf("unsafe tracks=%#v error=%v", tracks, err)
	}
	if normalized, ok := unsafe.Lookup("thumbnails").ListValue(); !ok || len(normalized) != 0 {
		t.Fatalf("unsafe thumbnails retained: %#v", normalized)
	}
	missingIDs := value.NewInfo(value.NewObject(value.Field{Key: "thumbnails", Value: value.List(
		thumbnailValue("", "https://cdn.example.test/z.jpg", "jpg", 1, 100, 100),
		thumbnailValue("", "https://cdn.example.test/a.jpg", "jpg", 1, 100, 100),
	)}))
	tracks, err = selectThumbnails(&missingIDs)
	if err != nil || len(tracks) != 2 || tracks[0].rawURL != "https://cdn.example.test/a.jpg" ||
		tracks[0].id != "0" || tracks[1].id != "1" {
		t.Fatalf("missing-ID ordering tracks=%#v error=%v", tracks, err)
	}
	overflow := make([]value.Value, maxThumbnails+1)
	tooMany := value.NewInfo(value.NewObject(value.Field{Key: "thumbnails", Value: value.List(overflow...)}))
	if _, err := selectThumbnails(&tooMany); err == nil {
		t.Fatal("thumbnail limit was not enforced")
	}
}

func TestWriteThumbnailsBestFallbackAllPlaylistAndSafety(t *testing.T) {
	requests := make([]string, 0)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.URL.Path)
		switch request.URL.Path {
		case "/best.jpg":
			http.NotFound(writer, request)
		case "/middle.png":
			_, _ = writer.Write([]byte("middle"))
		case "/small.webp":
			_, _ = writer.Write([]byte("small"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := thumbnailInfo(server.URL)
	root := t.TempDir()
	operation := operation{client: NewClient(), transport: transport, request: Request{
		OutputDir: root, SkipDownload: true, Thumbnails: ThumbnailOptions{Write: true},
		OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "entry/%(id)s.%(ext)s"},
	}}
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err != nil || len(artifacts) != 1 || bytes != int64(len("middle")) {
		t.Fatalf("best artifacts=%#v bytes=%d error=%v", artifacts, bytes, err)
	}
	if got := filepath.ToSlash(strings.TrimPrefix(artifacts[0].Path, root+string(filepath.Separator))); got != "entry/item.png" {
		t.Fatalf("best path = %q", got)
	}
	if fmt.Sprint(requests) != "[/best.jpg /middle.png]" {
		t.Fatalf("best requests = %v", requests)
	}

	requests = nil
	allRoot := t.TempDir()
	info = thumbnailInfo(server.URL)
	operation.request.OutputDir = allRoot
	operation.request.Thumbnails = ThumbnailOptions{WriteAll: true}
	operation.request.OutputTemplates = OutputTemplates{
		OutputTemplateThumbnail:   "entry/%(id)s.%(ext)s",
		OutputTemplatePLThumbnail: "playlist/%(id)s.%(ext)s",
	}
	artifacts, _, err = operation.writeThumbnails(context.Background(), &info, true)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("all artifacts=%#v error=%v", artifacts, err)
	}
	for _, relative := range []string{"playlist/item.middle.png", "playlist/item.small.webp"} {
		if _, err := os.Stat(filepath.Join(allRoot, filepath.FromSlash(relative))); err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
	}

	info = thumbnailInfo(server.URL)
	operation.request.OutputTemplates[OutputTemplatePLThumbnail] = "../escape/%(id)s.%(ext)s"
	if _, _, err := operation.writeThumbnails(context.Background(), &info, true); err == nil {
		t.Fatal("thumbnail traversal accepted")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	operation.request.OutputTemplates[OutputTemplatePLThumbnail] = "cancelled/%(id)s.%(ext)s"
	if _, _, err := operation.writeThumbnails(cancelled, &info, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestClientWritesThumbnailWithSkipDownloadAndTypedTemplate(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			_, _ = fmt.Fprintf(writer, `<html><head>
<meta property="og:title" content="Fixture">
<meta property="og:video" content="%s/media.mp4">
<meta property="og:image" content="%s/cover.png">
</head></html>`, server.URL, server.URL)
		case "/cover.png":
			_, _ = writer.Write([]byte("image"))
		case "/media.mp4":
			_, _ = writer.Write([]byte("media"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/article", OutputDir: root, SkipDownload: true,
		Thumbnails: ThumbnailOptions{Write: true},
		OutputTemplates: OutputTemplates{
			OutputTemplateDefault:   "media/%(id)s.%(ext)s",
			OutputTemplateThumbnail: "images/%(id)s.%(ext)s",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "thumbnail" {
		t.Fatalf("artifacts = %#v", result.Artifacts)
	}
	if _, err := os.Stat(result.Artifacts[0].Path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "media")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("media output exists: %v", err)
	}
}

func TestWriteThumbnailsExistingDestination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("image"))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.jpg")},
	))
	operation := operation{client: NewClient(), transport: transport, request: Request{
		OutputDir: root, Thumbnails: ThumbnailOptions{Write: true},
		OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
	}}
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); err != nil {
		t.Fatal(err)
	}
	info = value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.jpg")},
	))
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); !errors.Is(err, downloader.ErrDestinationExists) {
		t.Fatalf("existing destination = %v", err)
	}
}

func TestThumbnailRedirectPolicy(t *testing.T) {
	var crossOriginAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		crossOriginAuthorization = request.Header.Get("Authorization")
		_, _ = writer.Write([]byte("cross"))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/safe":
			http.Redirect(writer, request, "/image.png", http.StatusFound)
		case "/unsafe":
			writer.Header().Set("Location", "https://user:secret@evil.example/image.png")
			writer.WriteHeader(http.StatusFound)
		case "/cross":
			http.Redirect(writer, request, target.URL+"/image.png", http.StatusFound)
		case "/loop":
			http.Redirect(writer, request, "/loop", http.StatusFound)
		case "/image.png":
			_, _ = writer.Write([]byte("image"))
		}
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	makeInfo := func(endpoint string) value.Info {
		return value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("item")},
			value.Field{Key: "thumbnail", Value: value.String(server.URL + endpoint)},
		))
	}
	operation := operation{client: NewClient(), transport: transport, request: Request{
		OutputDir: t.TempDir(), Thumbnails: ThumbnailOptions{Write: true},
		OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
	}}
	safe := makeInfo("/safe")
	if artifacts, _, err := operation.writeThumbnails(context.Background(), &safe, false); err != nil || len(artifacts) != 1 {
		t.Fatalf("safe redirect artifacts=%#v error=%v", artifacts, err)
	}
	operation.request.OutputDir = t.TempDir()
	unsafe := makeInfo("/unsafe")
	if _, _, err := operation.writeThumbnails(context.Background(), &unsafe, false); !errors.Is(err, errUnsafeThumbnailRedirect) {
		t.Fatalf("unsafe redirect = %v", err)
	}
	operation.request.OutputDir = t.TempDir()
	cross := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("cross")},
		value.Field{Key: "thumbnails", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(server.URL + "/cross")},
			value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "Authorization", Value: value.String("secret")},
			))},
		)))},
	))
	if artifacts, _, err := operation.writeThumbnails(context.Background(), &cross, false); err != nil || len(artifacts) != 1 {
		t.Fatalf("cross-origin redirect artifacts=%#v error=%v", artifacts, err)
	}
	if crossOriginAuthorization != "" {
		t.Fatalf("authorization crossed redirect origin: %q", crossOriginAuthorization)
	}
	operation.request.OutputDir = t.TempDir()
	loop := makeInfo("/loop")
	if _, _, err := operation.writeThumbnails(context.Background(), &loop, false); !errors.Is(err, errUnsafeThumbnailRedirect) {
		t.Fatalf("redirect loop = %v", err)
	}
}

func TestThumbnailRemoteExhaustionWarnsAndContinues(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnail", Value: value.String(server.URL + "/missing.jpg")},
	))
	operation := operation{client: NewClient(), transport: transport, request: Request{
		OutputDir: t.TempDir(), Thumbnails: ThumbnailOptions{Write: true},
	}}
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err != nil || len(artifacts) != 0 || bytes != 0 {
		t.Fatalf("artifacts=%#v bytes=%d error=%v", artifacts, bytes, err)
	}
	if thumbnails, ok := info.Lookup("thumbnails").ListValue(); !ok || len(thumbnails) != 0 {
		t.Fatalf("failed thumbnail retained: %#v", thumbnails)
	}
}

func FuzzThumbnailPathAndURL(f *testing.F) {
	f.Add("thumb", "jpg", "https://cdn.example.test/image.jpg")
	f.Add("../escape", "png", "https://user@cdn.example.test/image.png")
	f.Fuzz(func(t *testing.T, id, extension, rawURL string) {
		trackID := thumbnailOriginalID(value.String(id))
		if trackID == "" {
			trackID = "0"
		}
		track := thumbnailTrack{id: trackID, extension: thumbnailExtensions[extension]}
		if track.extension == "" {
			track.extension = "jpg"
		}
		info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("item")}))
		outputInfo := value.NewInfo(info.Fields().Clone())
		outputInfo.Set("ext", value.String(track.extension))
		destination, err := outputtemplate.Resolve(t.TempDir(), "%(id)s.%(ext)s", outputInfo)
		if err != nil {
			return
		}
		if strings.Contains(filepath.Base(destination), "..") || filepath.Ext(destination) != "."+track.extension {
			t.Fatalf("unsafe destination = %q", destination)
		}
		_ = validThumbnailURL(rawURL)
	})
}

func thumbnailInfo(baseURL string) value.Info {
	return value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "title", Value: value.String("Item")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "thumbnails", Value: value.List(
			thumbnailValue("small", baseURL+"/small.webp", "webp", 1, 100, 100),
			thumbnailValue("middle", baseURL+"/middle.png", "png", 2, 500, 500),
			thumbnailValue("best", baseURL+"/best.jpg", "jpg", 3, 1000, 1000),
		)},
	))
}

func thumbnailValue(id, rawURL, extension string, preference, width, height int64) value.Value {
	fields := []value.Field{
		{Key: "id", Value: value.String(id)},
		{Key: "url", Value: value.String(rawURL)},
		{Key: "preference", Value: value.Int(preference)},
		{Key: "width", Value: value.Int(width)},
		{Key: "height", Value: value.Int(height)},
	}
	if extension != "" {
		fields = append(fields, value.Field{Key: "ext", Value: value.String(extension)})
	}
	return value.ObjectValue(value.NewObject(fields...))
}
