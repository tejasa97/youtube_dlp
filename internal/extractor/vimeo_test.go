package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"testing"
)

type vimeoFixtureTransport struct {
	page       []byte
	config     []byte
	status     int
	profile    string
	pageReads  int
	configGets int
}

func (*vimeoFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}

func (transport *vimeoFixtureTransport) ReadPageProfile(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	transport.profile, transport.pageReads = profile, transport.pageReads+1
	if rawURL != "https://vimeo.com/123456789" {
		return nil, nil, fmt.Errorf("unexpected webpage URL %q", rawURL)
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *vimeoFixtureTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.configGets++
	if request.Method != http.MethodGet || request.URL.String() != "https://player.vimeo.example/video/123456789/config?token=fixture&ref=offline" || request.Header.Get("Referer") == "" {
		return nil, fmt.Errorf("unexpected config request: %s %s %v", request.Method, request.URL, request.Header)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(bytes.NewReader(transport.config)), Header: make(http.Header), Request: request}, nil
}

func (transport *vimeoFixtureTransport) DoProfile(ctx context.Context, request *http.Request, profile string) (*http.Response, error) {
	transport.profile = profile
	return transport.Do(ctx, request)
}

func TestVimeoExtractsProgressiveHLSAndDASHWithProfile(t *testing.T) {
	transport := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), config: readVimeoFixture(t, "config.json")}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if transport.profile != vimeoImpersonationProfile || transport.pageReads != 1 || transport.configGets != 1 {
		t.Fatalf("profile=%q pageReads=%d configGets=%d", transport.profile, transport.pageReads, transport.configGets)
	}
	actual, err := json.Marshal(result.Info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if json.Unmarshal(actual, &actualDocument) != nil || json.Unmarshal(readVimeoFixture(t, "expected.json"), &expectedDocument) != nil {
		t.Fatal("decode comparison documents")
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual: %s\nexpected: %#v", actual, expectedDocument)
	}
}

func TestVimeoSuitableAndPlayerConfig(t *testing.T) {
	for _, rawURL := range []string{"https://vimeo.com/123456789", "https://player.vimeo.com/video/123456789"} {
		parsed, _ := url.Parse(rawURL)
		if !NewVimeo().Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false", rawURL)
		}
	}
	parsed, _ := url.Parse("https://vimeo.com/channels/fixture")
	if NewVimeo().Suitable(parsed) {
		t.Fatal("unsupported channel URL is suitable")
	}
	page := append([]byte("window.playerConfig = "), readVimeoFixture(t, "config.json")...)
	page = append(page, ';')
	transport := &vimeoFixtureTransport{page: page}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport})
	if err != nil || transport.configGets != 0 {
		t.Fatalf("player config result=%#v err=%v gets=%d", result, err, transport.configGets)
	}
}

func TestVimeoFailuresAreCategorized(t *testing.T) {
	base := vimeoConfig{}
	base.Video.Title = "Fixture"
	base.Video.Files.Progressive = append(base.Video.Files.Progressive, struct {
		URL     string `json:"url"`
		Quality string `json:"quality"`
		Width   int64  `json:"width"`
		Height  int64  `json:"height"`
		FPS     int64  `json:"fps"`
		Bitrate int64  `json:"bitrate"`
	}{URL: "https://media.example/video.mp4", Quality: "source"})
	auth := base
	auth.View = 4
	if _, err := parseVimeoConfig(auth, "1", "https://vimeo.com/1"); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("auth error = %v", err)
	}
	upcoming := vimeoConfig{}
	upcoming.Video.Title = "Upcoming"
	upcoming.Video.LiveEvent.Status = "pending"
	if _, err := parseVimeoConfig(upcoming, "1", "https://vimeo.com/1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("upcoming error = %v", err)
	}
	if _, err := parseVimeoConfig(vimeoConfig{}, "1", "https://vimeo.com/1"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error = %v", err)
	}
	transport := &vimeoFixtureTransport{page: readVimeoFixture(t, "page.html"), status: http.StatusForbidden}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: transport}); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("HTTP auth error = %v", err)
	}
	withoutProfile := &memoryTransport{pages: map[string][]byte{"https://vimeo.com/123456789": readVimeoFixture(t, "page.html")}}
	if _, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123456789", Transport: withoutProfile}); !errors.Is(err, ErrTransportProfile) {
		t.Fatalf("profile error = %v", err)
	}
}

func FuzzParseVimeoConfig(f *testing.F) {
	f.Add(readVimeoFixture(f, "config.json"))
	f.Add([]byte(`{"view":4}`))
	f.Add([]byte(`{"video":{"title":"x"}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		var config vimeoConfig
		if json.Unmarshal(data, &config) == nil {
			_, _ = parseVimeoConfig(config, "1", "https://vimeo.com/1")
		}
	})
}

type vimeoTestHelper interface {
	Helper()
	Fatal(...any)
}

func readVimeoFixture(t vimeoTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/vimeo/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
