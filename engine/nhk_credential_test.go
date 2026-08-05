package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	mediaformat "github.com/tejasa97/youtube_dlp/internal/format"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/value"
)

func TestNHKCredentialIsolatedMediaDownloadStripsAmbientCredentials(t *testing.T) {
	var mu sync.Mutex
	var captured http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		captured = request.Header.Clone()
		mu.Unlock()
		_, _ = writer.Write([]byte("audio-bytes"))
	}))
	defer server.Close()

	transport, err := network.New(network.Config{
		DefaultHeaders: http.Header{
			"Cookie":              {"ambient-session=secret"},
			"Authorization":       {"Bearer ambient-token"},
			"Proxy-Authorization": {"Basic proxy-secret"},
			"Referer":             {"https://www.nhk.or.jp/radio/player/ondemand.html"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("nhk-test")},
		value.Field{Key: "title", Value: value.String("NHK Test")},
		value.Field{Key: "ext", Value: value.String("m4a")},
	))
	info.Set("formats", value.List(value.ObjectValue(value.NewObject(
		value.Field{Key: "format_id", Value: value.String("direct")},
		value.Field{Key: "url", Value: value.String(server.URL + "/media.bin")},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "protocol", Value: value.String("https")},
		value.Field{Key: "_credential_isolated", Value: value.Bool(true)},
	))))

	operation := &operation{
		client:    newBroadTestClient(),
		request:   Request{OutputDir: root},
		transport: transport,
	}
	selected, err := operation.selectFormats(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || !selected[0].CredentialIsolated {
		t.Fatalf("selection=%#v", selected)
	}
	_, _, err = operation.downloadSelection(context.Background(), selected[0], root, root+"/out.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if v := captured.Get(key); v != "" {
			t.Fatalf("isolated media download leaked %s: %s", key, v)
		}
	}
}

type nhkSchoolProductRoundTripper struct {
	mu          sync.Mutex
	redirect    string
	requests    map[string][]http.Header
	targetCalls int
}

func (transport *nhkSchoolProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	if transport.requests == nil {
		transport.requests = make(map[string][]http.Header)
	}
	key := request.URL.Host + request.URL.Path
	transport.requests[key] = append(transport.requests[key], request.Header.Clone())
	transport.mu.Unlock()

	status := http.StatusOK
	header := make(http.Header)
	body := ""
	switch key {
	case "www2.nhk.or.jp/school/movie/bangumi.cgi":
		body = `var r_version = "00003";
var r_duration = "1";
programObj.name = "School isolation fixture";`
	case "nhks-vh.akamaihd.net/i/das/D0005150/D0005150191_00003_V_000.f4v/master.m3u8":
		if transport.redirect == "manifest" {
			status = http.StatusFound
			header.Set("Location", "https://nhks-vh.akamaihd.net/redirected.m3u8")
		} else {
			body = "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nsegment.ts\n#EXT-X-ENDLIST\n"
		}
	case "nhks-vh.akamaihd.net/i/das/D0005150/D0005150191_00003_V_000.f4v/segment.ts":
		if transport.redirect == "segment" {
			status = http.StatusTemporaryRedirect
			header.Set("Location", "https://nhks-vh.akamaihd.net/final.ts")
		} else {
			body = "school-segment"
		}
	case "nhks-vh.akamaihd.net/redirected.m3u8", "nhks-vh.akamaihd.net/final.ts":
		transport.mu.Lock()
		transport.targetCalls++
		transport.mu.Unlock()
		body = "must-not-fetch"
	default:
		return nil, errors.New("unexpected NHK School request: " + key)
	}
	return &http.Response{
		StatusCode: status,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}, nil
}

func TestNHKSchoolProductCredentialIsolation(t *testing.T) {
	for _, test := range []struct {
		name         string
		redirect     string
		wantCategory ErrorCategory
	}{
		{name: "success"},
		{name: "manifest-redirect", redirect: "manifest", wantCategory: ErrorInternal},
		{name: "segment-redirect", redirect: "segment", wantCategory: ErrorNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTripper := &nhkSchoolProductRoundTripper{redirect: test.redirect}
			transport, err := network.New(network.Config{
				RoundTripper: roundTripper,
				DefaultHeaders: http.Header{
					"Authorization":       {"Bearer ambient-secret"},
					"Cookie":              {"session=ambient-secret"},
					"Proxy-Authorization": {"Basic ambient-secret"},
					"Referer":             {"https://www2.nhk.or.jp/school/ambient"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer transport.CloseIdleConnections()
			root := t.TempDir()
			request := Request{
				URL:            "https://www2.nhk.or.jp/school/movie/bangumi.cgi?das_id=D0005150191_00000",
				OutputDir:      root,
				OutputTemplate: "%(id)s.%(ext)s",
				Overwrite:      true,
			}
			compatibility, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			rootExtractor := ""
			capabilities := mediaformat.PlannerCapabilities{CanMergeFormats: true}
			operation := &operation{
				client: newBroadTestClient(), request: request, transport: transport,
				registry: productRuntime(), compatibility: compatibility,
				rootExtractor: &rootExtractor, plannerCapabilities: &capabilities,
			}
			result, runErr := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if test.redirect == "" {
				if runErr != nil {
					t.Fatal(runErr)
				}
				if !result.Downloaded || result.Filename == "" {
					t.Fatalf("result = %+v", result)
				}
				if data, err := os.ReadFile(result.Filename); err != nil || string(data) != "school-segment" {
					t.Fatalf("download = %q, %v", data, err)
				}
			} else {
				if runErr == nil || !IsCategory(runErr, test.wantCategory) {
					t.Fatalf("error = %v, want category %s", runErr, test.wantCategory)
				}
				entries, err := os.ReadDir(root)
				if err != nil {
					t.Fatal(err)
				}
				if len(entries) != 0 {
					t.Fatalf("artifacts remain after redirect rejection: %v", entries)
				}
			}

			roundTripper.mu.Lock()
			defer roundTripper.mu.Unlock()
			if roundTripper.targetCalls != 0 {
				t.Fatalf("redirect targets fetched = %d", roundTripper.targetCalls)
			}
			for _, key := range []string{
				"nhks-vh.akamaihd.net/i/das/D0005150/D0005150191_00003_V_000.f4v/master.m3u8",
				"nhks-vh.akamaihd.net/i/das/D0005150/D0005150191_00003_V_000.f4v/segment.ts",
			} {
				headers := roundTripper.requests[key]
				if key != "nhks-vh.akamaihd.net/i/das/D0005150/D0005150191_00003_V_000.f4v/master.m3u8" && test.redirect == "manifest" {
					continue
				}
				if len(headers) == 0 {
					t.Fatalf("missing request %s", key)
				}
				for _, header := range headers {
					for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
						if got := header.Get(name); got != "" {
							t.Fatalf("%s leaked on %s: %q", name, key, got)
						}
					}
				}
			}
		})
	}
}
