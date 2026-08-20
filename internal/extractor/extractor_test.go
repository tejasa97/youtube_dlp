package extractor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/network"
	"github.com/tejasa97/ytdlp-go/internal/testserver"
	"github.com/tejasa97/ytdlp-go/internal/value"
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
func (extractor namedExtractor) Extract(context.Context, Request) (Extraction, error) {
	return Media(value.NewInfo(value.NewObject())), nil
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

func TestRegistryHonorsExplicitExtractorKey(t *testing.T) {
	registry := NewRegistry(namedExtractor{"first", false}, namedExtractor{"second", false})
	selected, err := registry.SelectFor("https://example.invalid/video", "Second")
	if err != nil || selected.Name() != "second" {
		t.Fatalf("SelectFor() = %v, %v", selected, err)
	}
	if _, err := registry.SelectFor("https://example.invalid/video", "missing"); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("unknown key error = %v", err)
	}
}

func TestRegistryRoutesSupportedOpaqueURLsOnly(t *testing.T) {
	registry := NewRegistry(NewKaltura(), NewWistia(), NewGeneric())
	for _, test := range []struct {
		rawURL, want string
	}{
		{"kaltura:123:1_abcd1234", "kaltura"},
		{"wistia:a1b2c3d4e5", "wistia"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v", test.rawURL, selected, err)
		}
		selected, err = registry.SelectFor(test.rawURL, test.want)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("SelectFor(%q) = %v, %v", test.rawURL, selected, err)
		}
	}
	for _, rawURL := range []string{"javascript:alert(1)", "generic:payload", "wistia:not-valid"} {
		if _, err := registry.Select(rawURL); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("Select(%q) error = %v", rawURL, err)
		}
	}
}

func TestProfileTransportCapabilityIsExplicit(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{"https://example.test": []byte("native")}}
	if _, _, err := ReadPageWithProfile(context.Background(), transport, "https://example.test", "chrome-133"); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("ReadPageWithProfile() error = %v", err)
	}

	profiled := &recordingProfileTransport{memoryTransport: transport}
	body, _, err := ReadPageWithProfile(context.Background(), profiled, "https://example.test", "chrome-133")
	if err != nil || string(body) != "native" || profiled.profile != "chrome-133" {
		t.Fatalf("body = %q, profile = %q, error = %v", body, profiled.profile, err)
	}
}

type recordingProfileTransport struct {
	*memoryTransport
	profile string
}

func (transport *recordingProfileTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.profile = profile
	return transport.Do(ctx, request)
}

func (transport *recordingProfileTransport) ReadPageProfile(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile = profile
	return transport.ReadPage(ctx, rawURL)
}

func TestFixtureExtraction(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	registry := NewRegistry(NewFixture(), NewGeneric())
	result, name, err := registry.Extract(context.Background(), Request{URL: server.URL + "/page", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if name != "fixture" {
		t.Fatalf("extractor = %q", name)
	}
	if title, _ := result.Info.Title(); title != "Deterministic Fixture" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 1 {
		t.Fatalf("formats = %d", len(formats))
	}
}

func TestGenericDirectMediaExtraction(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	transport, _ := network.New(network.Config{})
	registry := NewRegistry(NewFixture(), NewGeneric())
	result, name, err := registry.Extract(context.Background(), Request{URL: server.URL + "/media", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if name != "generic" {
		t.Fatalf("extractor = %q", name)
	}
	if extension, _ := result.Info.Extension(); extension != "bin" {
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
		result, _, err := registry.Extract(context.Background(), Request{URL: server.URL + test.path, Transport: transport})
		if err != nil {
			t.Fatalf("extract %s: %v", test.path, err)
		}
		formats, _ := result.Info.Formats()
		format, _ := formats[0].Object()
		if got, _ := format.Lookup("protocol").StringValue(); got != test.protocol {
			t.Fatalf("protocol for %s = %q", test.path, got)
		}
		if ext, _ := result.Info.Extension(); ext != "mp4" {
			t.Fatalf("extension for %s = %q", test.path, ext)
		}
	}
}

func TestGenericRecognizesSmoothStreamingManifest(t *testing.T) {
	if got := protocolForMediaType("application/vnd.ms-sstr+xml"); got != "ism" {
		t.Fatalf("protocol = %q, want ism", got)
	}
}

// TestRequestStringerRedactsAllSensitiveFields proves that the internal
// extractor.Request formatting surface cannot reveal the video password,
// URL credentials, transport, or credential provider under %v, %+v, or %#v.
func TestRequestStringerRedactsAllSensitiveFields(t *testing.T) {
	const secret = "video-password-secret-AAAAA-12345"
	transport := &recordingProfileTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{"https://example.test": []byte("native")}},
	}
	request := Request{
		URL:           "https://user:hidden@host.invalid/path",
		VideoPassword: secret,
		Transport:     transport,
		Credentials:   staticCredentialProvider{username: "alice", password: "bob-secret"},
	}
	surfaces := map[string]string{
		"%v":  fmt.Sprintf("%v", request),
		"%+v": fmt.Sprintf("%+v", request),
		"%#v": fmt.Sprintf("%#v", request),
	}
	// Pointer form must also redact; the formatter must work for both value
	// and pointer receivers.
	surfaces["%v*"] = fmt.Sprintf("%v", &request)
	surfaces["%+v*"] = fmt.Sprintf("%+v", &request)
	surfaces["%#v*"] = fmt.Sprintf("%#v", &request)
	// And it must hold when the Request is wrapped in another struct, which
	// is how it would surface in operation errors.
	type wrap struct{ Inner Request }
	surfaces["wrapped%v"] = fmt.Sprintf("%v", wrap{Inner: request})
	surfaces["wrapped%+v"] = fmt.Sprintf("%+v", wrap{Inner: request})

	for label, got := range surfaces {
		if strings.Contains(got, secret) {
			t.Fatalf("%s leaked video password: %q", label, got)
		}
		if strings.Contains(got, "hidden") {
			t.Fatalf("%s leaked URL userinfo: %q", label, got)
		}
		if strings.Contains(got, "bob-secret") {
			t.Fatalf("%s leaked credential password: %q", label, got)
		}
		if strings.Contains(got, "memoryTransport") || strings.Contains(got, "recordingProfileTransport") {
			t.Fatalf("%s leaked transport type: %q", label, got)
		}
	}
}

func TestErrWrongPasswordIsDistinctAndUnwrappable(t *testing.T) {
	if errors.Is(ErrAuthentication, ErrWrongPassword) {
		t.Fatal("ErrWrongPassword must not satisfy errors.Is(ErrAuthentication)")
	}
	if errors.Is(ErrWrongPassword, ErrAuthentication) {
		t.Fatal("ErrAuthentication must not satisfy errors.Is(ErrWrongPassword)")
	}
	wrapped := fmt.Errorf("wrap: %w", ErrWrongPassword)
	if !errors.Is(wrapped, ErrWrongPassword) {
		t.Fatal("wrapped ErrWrongPassword does not unwrap via errors.Is")
	}
	if errors.Is(wrapped, ErrAuthentication) {
		t.Fatal("wrapped ErrWrongPassword must not match ErrAuthentication")
	}
}

type staticCredentialProvider struct {
	username, password string
}

func (provider staticCredentialProvider) Lookup(_ context.Context, _ string) (Credential, bool, error) {
	return Credential{Username: provider.username, Password: provider.password}, true, nil
}
