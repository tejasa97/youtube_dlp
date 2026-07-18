package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
)

type tiktokFixtureTransport struct {
	page       []byte
	profile    string
	requested  string
	nativeRead bool
	wait       bool
	started    chan struct{}
}

func (transport *tiktokFixtureTransport) ReadPage(ctx context.Context, _ string) ([]byte, http.Header, error) {
	transport.nativeRead = true
	return nil, nil, errors.New("unexpected native TikTok request")
}

func (transport *tiktokFixtureTransport) ReadPageProfile(ctx context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile, transport.requested = profile, rawURL
	if transport.wait {
		if transport.started != nil {
			close(transport.started)
		}
		<-ctx.Done()
		return nil, nil, ctx.Err()
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (*tiktokFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected TikTok API request")
}

func (*tiktokFixtureTransport) DoProfile(context.Context, *http.Request, string) (*http.Response, error) {
	return nil, errors.New("unexpected profiled TikTok API request")
}

func TestTikTokExtractsProtectedHydrationFormats(t *testing.T) {
	transport := &tiktokFixtureTransport{page: readTikTokFixture(t, "page.html")}
	result, err := NewTikTok().Extract(context.Background(), Request{
		URL:       "https://www.tiktok.com/@fixture_creator/video/7460000000000000001?lang=en&token=not-forwarded",
		Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.nativeRead || transport.profile != tiktokImpersonationProfile {
		t.Fatalf("native=%v profile=%q", transport.nativeRead, transport.profile)
	}
	if transport.requested != "https://www.tiktok.com/@fixture_creator/video/7460000000000000001" {
		t.Fatalf("profiled request URL = %q", transport.requested)
	}
	actual, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if json.Unmarshal(actual, &actualDocument) != nil || json.Unmarshal(readTikTokFixture(t, "expected.json"), &expectedDocument) != nil {
		t.Fatal("decode TikTok comparison documents")
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual: %s\nexpected: %s", actual, readTikTokFixture(t, "expected.json"))
	}
}

func TestTikTokSuitableAndEmbedCanonicalization(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.tiktok.com/@fixture.creator/video/7460000000000000001",
		"https://tiktok.com/embed/7460000000000000001",
	} {
		parsed, _ := url.Parse(rawURL)
		if !NewTikTok().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{"https://example.com/@x/video/1", "https://www.tiktok.com/@x", "https://www.tiktok.com/live/1", "ftp://www.tiktok.com/@x/video/1"} {
		parsed, _ := url.Parse(rawURL)
		if NewTikTok().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true", rawURL)
		}
	}
	transport := &tiktokFixtureTransport{page: readTikTokFixture(t, "page.html")}
	_, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/embed/7460000000000000001", Transport: transport})
	if err != nil || transport.requested != "https://www.tiktok.com/@_/video/7460000000000000001" {
		t.Fatalf("embed request=%q error=%v", transport.requested, err)
	}
}

func TestTikTokFailuresAreCategorizedAndSecretSafe(t *testing.T) {
	tests := []struct {
		name string
		page []byte
		want error
	}{
		{"private", readTikTokFixture(t, "private.html"), ErrAuthentication},
		{"private-account", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"__DEFAULT_SCOPE__\":{\"webapp.video-detail\":{\"statusCode\":10222}}}</script>"), ErrAuthentication},
		{"blocked", readTikTokFixture(t, "unavailable.html"), ErrUnavailable},
		{"expired-cookie", []byte("<html><title>Session expired - Log in</title></html>"), ErrAuthentication},
		{"challenge", []byte("<html><title>Please wait...</title><div id=\"cs\"></div></html>"), ErrChallengeSolver},
		{"malformed", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"secret\":\"private-cookie-value\"</script>"), ErrInvalidMetadata},
		{"no-formats", []byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{\"__DEFAULT_SCOPE__\":{\"webapp.video-detail\":{\"statusCode\":0,\"itemInfo\":{\"itemStruct\":{\"id\":\"7460000000000000001\",\"desc\":\"x\"}}}}}</script>"), ErrInvalidMetadata},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := &tiktokFixtureTransport{page: test.page}
			_, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if err != nil && strings.Contains(err.Error(), "private-cookie-value") {
				t.Fatalf("error leaked fixture secret: %v", err)
			}
		})
	}
	withoutProfile := &memoryTransport{pages: map[string][]byte{}}
	if _, err := NewTikTok().Extract(context.Background(), Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: withoutProfile}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("missing profile error = %v", err)
	}
}

func TestTikTokCancellationAndHydrationBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := &tiktokFixtureTransport{wait: true}
	if _, err := NewTikTok().Extract(ctx, Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancel error = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	transport = &tiktokFixtureTransport{wait: true, started: started}
	go func() { <-started; cancel() }()
	if _, err := NewTikTok().Extract(ctx, Request{URL: "https://www.tiktok.com/@fixture/video/7460000000000000001", Transport: transport}); !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancel error = %v", err)
	}
	oversized := fmt.Sprintf("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">%s</script>", strings.Repeat(" ", int(maxExtractorJSONBytes)+1))
	if _, err := parseTikTokPage([]byte(oversized), "1", "https://www.tiktok.com/@x/video/1"); !errors.Is(err, ErrJSONResponseTooLarge) {
		t.Fatalf("oversized hydration error = %v", err)
	}
}

func FuzzParseTikTokPage(f *testing.F) {
	f.Add(readTikTokFixture(f, "page.html"))
	f.Add(readTikTokFixture(f, "private.html"))
	f.Add([]byte("<script id=\"__UNIVERSAL_DATA_FOR_REHYDRATION__\">{}</script>"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseTikTokPage(data, "7460000000000000001", "https://www.tiktok.com/@fixture/video/7460000000000000001")
	})
}

type tiktokTestHelper interface {
	Helper()
	Fatal(...any)
}

func readTikTokFixture(t tiktokTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/tiktok/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
