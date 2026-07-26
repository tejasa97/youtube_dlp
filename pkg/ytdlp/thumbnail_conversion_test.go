package ytdlp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestThumbnailConversionMapping(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input, source, target string
		convert               bool
	}{
		{"none", "webp", "", false},
		{" JPG ", "webp", "jpg", true},
		{"webp>png/jpg", "webp", "png", true},
		{"webp>png/jpg", "avif", "jpg", true},
		{"webp>png", "jpg", "", false},
		{"jpeg>png/jpg", "jpeg", "", false},
		{"jpg>png", "jpeg", "png", true},
	}
	for _, test := range tests {
		mapping, err := parseThumbnailConversionMapping(test.input)
		if err != nil {
			t.Fatalf("parse %q: %v", test.input, err)
		}
		target, convert := mapping.resolve(test.source)
		if target != test.target || convert != test.convert {
			t.Fatalf("%q for %q = (%q, %t), want (%q, %t)",
				test.input, test.source, target, convert, test.target, test.convert)
		}
	}
	for _, input := range []string{
		"gif", ">jpg", "web-p>jpg", "webp>>jpg", "webp>gif", "webp>", "jpg\x00",
		strings.Repeat("a", maxThumbnailMapping+1),
		strings.Repeat("jpg/", maxThumbnailRules) + "jpg",
	} {
		if _, err := parseThumbnailConversionMapping(input); err == nil {
			t.Fatalf("invalid mapping %q accepted", input)
		}
	}
}

func TestWriteThumbnailsConvertsAllAndUpdatesMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.TrimPrefix(request.URL.Path, "/")))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()

	root := t.TempDir()
	info := thumbnailInfo(server.URL)
	var conversions []string
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: root,
			Thumbnails: ThumbnailOptions{
				WriteAll: true, ConvertFormat: "webp>png/jpg",
			},
			OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "images/%(id)s.%(ext)s"},
		},
		thumbnailConvert: func(
			_ context.Context, source, destination, format string, overwrite bool, _ events.Sink,
		) error {
			conversions = append(conversions, filepath.Base(source)+">"+filepath.Base(destination)+":"+format)
			input, readErr := os.ReadFile(source)
			if readErr != nil {
				return readErr
			}
			return os.WriteFile(destination, append([]byte("converted:"), input...), 0o600)
		},
	}
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(conversions, ","); got != "item.middle.png>item.middle.jpg:jpg,item.small.webp>item.small.png:png" {
		t.Fatalf("conversions = %q", got)
	}
	wantPaths := []string{"item.best.jpg", "item.middle.jpg", "item.small.png"}
	var wantBytes int64
	for index, want := range wantPaths {
		if filepath.Base(artifacts[index].Path) != want {
			t.Fatalf("artifact %d = %#v, want %q", index, artifacts[index], want)
		}
		stat, statErr := os.Stat(artifacts[index].Path)
		if statErr != nil {
			t.Fatal(statErr)
		}
		wantBytes += stat.Size()
	}
	if bytes != wantBytes {
		t.Fatalf("bytes = %d, want %d", bytes, wantBytes)
	}
	for _, removed := range []string{"images/item.middle.png", "images/item.small.webp"} {
		if _, statErr := os.Stat(filepath.Join(root, filepath.FromSlash(removed))); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("source %q remains: %v", removed, statErr)
		}
	}
	thumbnails, _ := info.Lookup("thumbnails").ListValue()
	for index, want := range []string{"png", "jpg", "jpg"} {
		object, _ := thumbnails[index].Object()
		if extension, _ := object.Lookup("ext").StringValue(); extension != want {
			t.Fatalf("metadata %d extension = %q, want %q", index, extension, want)
		}
		if path, _ := object.Lookup("filepath").StringValue(); path == "" {
			t.Fatalf("metadata %d has no filepath", index)
		}
	}
}

func TestThumbnailConversionFailureAndCleanupSafety(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("source"))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	makeInfo := func() value.Info {
		return value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("item")},
			value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.webp")},
		))
	}

	root := t.TempDir()
	info := makeInfo()
	conversionError := errors.New("injected conversion failure")
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: root, Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "png"},
			OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
		},
		thumbnailConvert: func(
			context.Context, string, string, string, bool, events.Sink,
		) error {
			return conversionError
		},
	}
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); !errors.Is(err, conversionError) {
		t.Fatalf("conversion failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "item.webp")); err != nil {
		t.Fatalf("source was not preserved: %v", err)
	}

	var warnings int
	root = t.TempDir()
	info = makeInfo()
	operation.client = NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warnings++
			return errors.New("observer failure")
		}
		return nil
	}))
	operation.request.OutputDir = root
	operation.thumbnailConvert = func(
		_ context.Context, source, destination, _ string, _ bool, _ events.Sink,
	) error {
		input, readErr := os.ReadFile(source)
		if readErr != nil {
			return readErr
		}
		return os.WriteFile(destination, input, 0o600)
	}
	operation.removeFile = func(string) error { return errors.New("cleanup failure") }
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err != nil {
		t.Fatal(err)
	}
	if warnings != 1 || len(artifacts) != 2 ||
		filepath.Ext(artifacts[0].Path) != ".png" || filepath.Ext(artifacts[1].Path) != ".webp" ||
		bytes != int64(2*len("source")) {
		t.Fatalf("warnings=%d artifacts=%#v bytes=%d", warnings, artifacts, bytes)
	}
}

func TestThumbnailConversionCorrectsMislabeledWebP(t *testing.T) {
	webp := []byte("RIFF\x08\x00\x00\x00WEBPpayload")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(webp)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	makeInfo := func() value.Info {
		return value.NewInfo(value.NewObject(
			value.Field{Key: "id", Value: value.String("item")},
			value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.jpg")},
		))
	}

	t.Run("conditional conversion uses corrected extension", func(t *testing.T) {
		root := t.TempDir()
		info := makeInfo()
		var sourceExtension, target string
		operation := operation{
			client: NewClient(), transport: transport,
			request: Request{
				OutputDir: root,
				Thumbnails: ThumbnailOptions{
					Write: true, ConvertFormat: "webp>png/jpg",
				},
				OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
			},
			thumbnailConvert: func(
				_ context.Context, source, destination, format string, _ bool, _ events.Sink,
			) error {
				sourceExtension, target = filepath.Ext(source), format
				return os.WriteFile(destination, []byte("png"), 0o600)
			},
		}
		artifacts, _, err := operation.writeThumbnails(context.Background(), &info, false)
		if err != nil {
			t.Fatal(err)
		}
		if sourceExtension != ".webp" || target != "png" || len(artifacts) != 1 ||
			filepath.Ext(artifacts[0].Path) != ".png" {
			t.Fatalf("source=%q target=%q artifacts=%#v", sourceExtension, target, artifacts)
		}
		for _, old := range []string{"item.jpg", "item.webp"} {
			if _, err := os.Stat(filepath.Join(root, old)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("%s remains: %v", old, err)
			}
		}
	})

	t.Run("same target only corrects extension", func(t *testing.T) {
		root := t.TempDir()
		info := makeInfo()
		operation := operation{
			client: NewClient(), transport: transport,
			request: Request{
				OutputDir: root, Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "webp"},
				OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
			},
			thumbnailConvert: func(
				context.Context, string, string, string, bool, events.Sink,
			) error {
				t.Fatal("same-target correction invoked ffmpeg")
				return nil
			},
		}
		artifacts, _, err := operation.writeThumbnails(context.Background(), &info, false)
		if err != nil || len(artifacts) != 1 || filepath.Ext(artifacts[0].Path) != ".webp" {
			t.Fatalf("artifacts=%#v error=%v", artifacts, err)
		}
		if content, err := os.ReadFile(artifacts[0].Path); err != nil || string(content) != string(webp) {
			t.Fatalf("content=%q error=%v", content, err)
		}
	})

	t.Run("disabled conversion does not run fixup", func(t *testing.T) {
		root := t.TempDir()
		info := makeInfo()
		operation := operation{
			client: NewClient(), transport: transport,
			request: Request{
				OutputDir: root, Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "none"},
				OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
			},
		}
		artifacts, _, err := operation.writeThumbnails(context.Background(), &info, false)
		if err != nil || len(artifacts) != 1 || filepath.Ext(artifacts[0].Path) != ".jpg" {
			t.Fatalf("artifacts=%#v error=%v", artifacts, err)
		}
	})

	t.Run("correction collision preserves source", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "item.webp"), []byte("existing"), 0o600); err != nil {
			t.Fatal(err)
		}
		info := makeInfo()
		operation := operation{
			client: NewClient(), transport: transport,
			request: Request{
				OutputDir: root, Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "webp"},
				OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
			},
		}
		if _, _, err := operation.writeThumbnails(context.Background(), &info, false); err == nil {
			t.Fatal("WebP correction collision was accepted")
		}
		if content, err := os.ReadFile(filepath.Join(root, "item.jpg")); err != nil || string(content) != string(webp) {
			t.Fatalf("source content=%q error=%v", content, err)
		}
	})
}

func TestThumbnailConversionCancellationPreservesSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("source"))
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
		value.Field{Key: "thumbnail", Value: value.String(server.URL + "/cover.webp")},
	))
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: root, Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "png"},
			OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
		},
		thumbnailConvert: func(
			context.Context, string, string, string, bool, events.Sink,
		) error {
			return context.Canceled
		},
	}
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); !errors.Is(err, context.Canceled) ||
		!IsCategory(categorized("write thumbnails", err), ErrorCancelled) {
		t.Fatalf("cancellation = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "item.webp")); err != nil {
		t.Fatalf("source was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "item.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination exists: %v", err)
	}
	thumbnails, _ := info.Lookup("thumbnails").ListValue()
	metadata, _ := thumbnails[0].Object()
	if extension, _ := metadata.Lookup("ext").StringValue(); extension != "webp" {
		t.Fatalf("metadata extension mutated to %q", extension)
	}
}

func TestThumbnailConversionValidationPrecedesNetwork(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	_, err := NewClient().Run(context.Background(), Request{
		URL: server.URL, SkipDownload: true,
		Thumbnails: ThumbnailOptions{Write: true, ConvertFormat: "gif"},
	})
	if !IsCategory(err, ErrorInvalidInput) || requests != 0 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
}

func TestThumbnailConversionCollisionPrecedesDownload(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnails", Value: value.List(
			thumbnailValue("same", server.URL+"/cover.webp", "webp", 1, 100, 100),
			thumbnailValue("same", server.URL+"/cover.jpg", "jpg", 2, 200, 200),
		)},
	))
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: t.TempDir(),
			Thumbnails: ThumbnailOptions{
				WriteAll: true, ConvertFormat: "jpg",
			},
		},
	}
	if _, _, err := operation.writeThumbnails(context.Background(), &info, false); err == nil || requests != 0 {
		t.Fatalf("error=%v requests=%d", err, requests)
	}
}

func TestThumbnailConversionRejectsContentInducedCollision(t *testing.T) {
	webp := []byte("RIFF\x08\x00\x00\x00WEBPpayload")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".jpg") {
			_, _ = writer.Write(webp)
			return
		}
		_, _ = writer.Write([]byte("png"))
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnails", Value: value.List(
			thumbnailValue("same", server.URL+"/cover.jpg", "jpg", 1, 100, 100),
			thumbnailValue("same", server.URL+"/cover.png", "png", 2, 200, 200),
		)},
	))
	root := t.TempDir()
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: root, Overwrite: true,
			Thumbnails: ThumbnailOptions{
				WriteAll: true, ConvertFormat: "webp>png",
			},
			OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
		},
		thumbnailConvert: func(
			_ context.Context, source, destination, _ string, _ bool, _ events.Sink,
		) error {
			input, readErr := os.ReadFile(source)
			if readErr != nil {
				return readErr
			}
			return os.WriteFile(destination, input, 0o600)
		},
	}
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err == nil || len(artifacts) != 1 || bytes != int64(len("png")) {
		t.Fatalf("artifacts=%#v bytes=%d error=%v", artifacts, bytes, err)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, "item.same.png")); readErr != nil || string(content) != "png" {
		t.Fatalf("committed destination=%q error=%v", content, readErr)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, "item.same.jpg")); readErr != nil || string(content) != string(webp) {
		t.Fatalf("colliding source=%q error=%v", content, readErr)
	}
}

func TestThumbnailConversionRejectsDownloadOverCommittedCorrectedPath(t *testing.T) {
	webp := []byte("RIFF\x08\x00\x00\x00WEBPpayload")
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = writer.Write(webp)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("item")},
		value.Field{Key: "thumbnails", Value: value.List(
			thumbnailValue("same", server.URL+"/second.webp", "webp", 1, 100, 100),
			thumbnailValue("same", server.URL+"/first.jpg", "jpg", 2, 200, 200),
		)},
	))
	root := t.TempDir()
	operation := operation{
		client: NewClient(), transport: transport,
		request: Request{
			OutputDir: root, Overwrite: true,
			Thumbnails: ThumbnailOptions{
				WriteAll: true, ConvertFormat: "jpg>png/webp",
			},
			OutputTemplates: OutputTemplates{OutputTemplateThumbnail: "%(id)s.%(ext)s"},
		},
	}
	artifacts, bytes, err := operation.writeThumbnails(context.Background(), &info, false)
	if err == nil || requests != 1 || len(artifacts) != 1 || bytes != int64(len(webp)) {
		t.Fatalf("requests=%d artifacts=%#v bytes=%d error=%v", requests, artifacts, bytes, err)
	}
	if content, readErr := os.ReadFile(filepath.Join(root, "item.same.webp")); readErr != nil || string(content) != string(webp) {
		t.Fatalf("committed corrected path=%q error=%v", content, readErr)
	}
}

func FuzzThumbnailConversionMapping(f *testing.F) {
	f.Add("webp>png/jpg", "webp")
	f.Add("none", "jpeg")
	f.Fuzz(func(t *testing.T, input, source string) {
		mapping, err := parseThumbnailConversionMapping(input)
		if err != nil {
			return
		}
		target, convert := mapping.resolve(source)
		if convert && target != "jpg" && target != "png" && target != "webp" {
			t.Fatalf("unsafe target %q", target)
		}
		root := t.TempDir()
		sourcePath := filepath.Join(root, "image.jpg")
		destination, pathErr := thumbnailConversionPath(root, sourcePath, source, mapping)
		if pathErr != nil {
			return
		}
		relative, relativeErr := filepath.Rel(root, destination)
		if relativeErr != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("destination escaped root: %q", destination)
		}
	})
}
