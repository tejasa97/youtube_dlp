package extractor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type namedExtractor struct {
	name     string
	suitable bool
}

func TestFixtureRejectsMalformedMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"title":"missing id and formats"}`))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	_, err := NewFixture().Extract(context.Background(), Request{URL: server.URL + "/page", Transport: transport})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract() error = %v", err)
	}
}

func (extractor namedExtractor) Name() string           { return extractor.name }
func (extractor namedExtractor) Suitable(*url.URL) bool { return extractor.suitable }
func (extractor namedExtractor) Extract(context.Context, Request) (value.Info, error) {
	return value.NewInfo(value.NewObject()), nil
}

func TestRegistrySelectionIsDeterministic(t *testing.T) {
	registry := NewRegistry(namedExtractor{"first", true}, namedExtractor{"second", true})
	selected, err := registry.Select("https://example.invalid/video")
	if err != nil {
		t.Fatal(err)
	}
	if selected.Name() != "first" {
		t.Fatalf("selected = %q", selected.Name())
	}
}

func TestRegistryRejectsMalformedURL(t *testing.T) {
	registry := NewRegistry(NewGeneric())
	if _, err := registry.Select("not a URL"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("Select() error = %v", err)
	}
}

func TestFixtureExtraction(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	registry := NewRegistry(NewFixture(), NewGeneric())
	info, name, err := registry.Extract(context.Background(), Request{URL: server.URL + "/page", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if name != "fixture" {
		t.Fatalf("extractor = %q", name)
	}
	if title, _ := info.Title(); title != "Deterministic Fixture" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats = %d", len(formats))
	}
}

func TestGenericDirectMediaExtraction(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	registry := NewRegistry(NewFixture(), NewGeneric())
	info, name, err := registry.Extract(context.Background(), Request{URL: server.URL + "/media", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if name != "generic" {
		t.Fatalf("extractor = %q", name)
	}
	if extension, _ := info.Extension(); extension != "bin" {
		t.Fatalf("extension = %q", extension)
	}
}

func TestGenericManifestExtraction(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	registry := NewRegistry(NewFixture(), NewGeneric())
	for _, test := range []struct {
		path     string
		protocol string
	}{
		{"/hls/master.m3u8", "m3u8_native"},
		{"/dash/manifest.mpd", "http_dash_segments"},
	} {
		info, _, err := registry.Extract(context.Background(), Request{URL: server.URL + test.path, Transport: transport})
		if err != nil {
			t.Fatalf("extract %s: %v", test.path, err)
		}
		formats, _ := info.Formats()
		format, _ := formats[0].Object()
		if got, _ := format.Lookup("protocol").StringValue(); got != test.protocol {
			t.Fatalf("protocol for %s = %q", test.path, got)
		}
		if ext, _ := info.Extension(); ext != "mp4" {
			t.Fatalf("extension for %s = %q", test.path, ext)
		}
	}
}
