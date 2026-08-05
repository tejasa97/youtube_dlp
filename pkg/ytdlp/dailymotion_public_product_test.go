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
	"sync"
	"testing"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/network"
)

type dailymotionRunRoundTripper struct {
	mu             sync.Mutex
	requests       []*http.Request
	urls           []string
	metadata       []byte
	media          []byte
	master         []byte
	directBody     []byte
	variant        []byte
	segment0       []byte
	segment1       []byte
	subtitle       []byte
	metadataStatus int
	graphqlBodies  [][]byte
	tokenCalls     int
	failMedia      bool
	blockMedia     bool
	entered        chan struct{}
	once           sync.Once
}

func newDailymotionRunRoundTripper(t *testing.T) *dailymotionRunRoundTripper {
	t.Helper()
	return &dailymotionRunRoundTripper{
		metadata:   readProductConformanceFixture(t, "public", "dailymotion", "success.json"),
		media:      readProductConformanceFixture(t, "dailymotion", "media.json"),
		master:     readProductConformanceFixture(t, "dailymotion", "master.m3u8"),
		directBody: []byte("DM-DIRECT-BYTES"),
		variant:    readProductConformanceFixture(t, "dailymotion", "720.m3u8"),
		segment0:   []byte("DM-HLS-SEGMENT-0"),
		segment1:   []byte("DM-HLS-SEGMENT-1"),
		subtitle:   readProductConformanceFixture(t, "dailymotion", "en.vtt"),
	}
}

func dailymotionRunResponse(request *http.Request, status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}
}

func (transport *dailymotionRunRoundTripper) record(request *http.Request) []byte {
	var body []byte
	if request.Body != nil {
		body, _ = io.ReadAll(request.Body)
		request.Body = io.NopCloser(bytes.NewReader(body))
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(context.Background()))
	transport.urls = append(transport.urls, request.URL.String())
	transport.mu.Unlock()
	return body
}

func (transport *dailymotionRunRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	body := transport.record(request)
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	for _, key := range []string{"Cookie", "Proxy-Authorization", "Referer"} {
		if request.Header.Get(key) != "" {
			return nil, fmt.Errorf("ambient %s leaked to %s", key, request.URL.Redacted())
		}
	}
	switch {
	case request.URL.String() == "https://graphql.api.dailymotion.com/oauth/token":
		transport.mu.Lock()
		transport.tokenCalls++
		transport.mu.Unlock()
		if request.Header.Get("Authorization") != "" {
			return nil, errors.New("ambient Authorization leaked to token endpoint")
		}
		if request.Header.Get("Origin") != "https://www.dailymotion.com" || request.Header.Get("Referer") != "" {
			return nil, errors.New("token Origin/Referer policy violated")
		}
		return dailymotionRunResponse(request, http.StatusOK, []byte(`{"access_token":"fixture-dailymotion-token"}`)), nil
	case request.URL.String() == "https://graphql.api.dailymotion.com/":
		if request.Header.Get("Authorization") != "Bearer fixture-dailymotion-token" {
			return nil, errors.New("scoped GraphQL Authorization missing")
		}
		if request.Header.Get("Origin") != "https://www.dailymotion.com" || request.Header.Get("Referer") != "" {
			return nil, errors.New("GraphQL Origin/Referer policy violated")
		}
		transport.mu.Lock()
		transport.graphqlBodies = append(transport.graphqlBodies, append([]byte(nil), body...))
		transport.mu.Unlock()
		switch {
		case bytes.Contains(body, []byte(`"operationName":"SEARCH_QUERY"`)):
			return dailymotionRunResponse(request, http.StatusOK, []byte(`{"data":{"search":{"videos":{"edges":[{"node":{"xid":"xfixture"}}]}}}}`)), nil
		case bytes.Contains(body, []byte("collection(xid:")):
			return dailymotionRunResponse(request, http.StatusOK, []byte(`{"data":{"collection":{"videos":{"edges":[{"node":{"xid":"xfixture","url":"https://www.dailymotion.com/video/xfixture"}}]}}}}`)), nil
		case bytes.Contains(body, []byte("channel(xid:")):
			return dailymotionRunResponse(request, http.StatusOK, []byte(`{"data":{"channel":{"videos":{"edges":[{"node":{"xid":"xfixture","url":"https://www.dailymotion.com/video/xfixture"}}]}}}}`)), nil
		case bytes.Contains(body, []byte("media(xid:")):
			return dailymotionRunResponse(request, http.StatusOK, transport.media), nil
		default:
			return dailymotionRunResponse(request, http.StatusBadRequest, nil), nil
		}
	case strings.HasPrefix(request.URL.Path, "/player/metadata/video/"):
		if request.URL.Query().Get("app") != "com.dailymotion.neon" {
			return dailymotionRunResponse(request, http.StatusBadRequest, nil), nil
		}
		if transport.metadataStatus != 0 {
			return dailymotionRunResponse(request, transport.metadataStatus, nil), nil
		}
		return dailymotionRunResponse(request, http.StatusOK, transport.metadata), nil
	case request.URL.String() == "https://stream-01.dmcdn.net/video/master.m3u8?auth=master%2Bsig&token=two":
		return dailymotionRunResponse(request, http.StatusOK, transport.master), nil
	case request.URL.String() == "https://stream-01.dmcdn.net/video/720.m3u8?auth=variant%2Bsig&token=four":
		return dailymotionRunResponse(request, http.StatusOK, transport.variant), nil
	case strings.HasPrefix(request.URL.Path, "/video/H264-"):
		if transport.failMedia {
			return dailymotionRunResponse(request, http.StatusServiceUnavailable, nil), nil
		}
		if transport.blockMedia {
			transport.once.Do(func() { close(transport.entered) })
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: &dailymotionBlockingBody{ctx: request.Context()}, Request: request}, nil
		}
		return dailymotionRunResponse(request, http.StatusOK, transport.directBody), nil
	case strings.HasSuffix(request.URL.Path, "/segment-0.ts"):
		return dailymotionRunResponse(request, http.StatusOK, transport.segment0), nil
	case strings.HasSuffix(request.URL.Path, "/segment-1.ts"):
		return dailymotionRunResponse(request, http.StatusOK, transport.segment1), nil
	case request.URL.Hostname() == "s1.dmcdn.net" && strings.HasSuffix(request.URL.Path, "/en.vtt"):
		return dailymotionRunResponse(request, http.StatusOK, transport.subtitle), nil
	default:
		return dailymotionRunResponse(request, http.StatusNotFound, nil), nil
	}
}

type dailymotionBlockingBody struct{ ctx context.Context }

func (body *dailymotionBlockingBody) Read([]byte) (int, error) {
	<-body.ctx.Done()
	return 0, body.ctx.Err()
}

func (body *dailymotionBlockingBody) Close() error { return nil }

func newDailymotionRunClient(transport *dailymotionRunRoundTripper) *Client {
	return NewClient(withTransportFactory(func(config network.Config) (*network.Client, error) {
		config.RoundTripper = transport
		config.DefaultHeaders = credentialIsolationHeaders()
		return network.New(config)
	}))
}

func assertDailymotionNoArtifacts(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for index := range entries {
			names[index] = entries[index].Name()
		}
		t.Fatalf("unexpected artifacts: %v", names)
	}
}

func assertDailymotionCredentialIsolation(t *testing.T, transport *dailymotionRunRoundTripper) {
	t.Helper()
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		for _, key := range []string{"Cookie", "Proxy-Authorization", "Referer"} {
			if got := request.Header.Get(key); got != "" {
				t.Fatalf("%s leaked on %s: %q", key, request.URL.Redacted(), got)
			}
		}
		if request.URL.Hostname() == "graphql.api.dailymotion.com" && request.URL.Path == "/" {
			if got := request.Header.Get("Authorization"); got != "Bearer fixture-dailymotion-token" {
				t.Fatalf("GraphQL Authorization=%q", got)
			}
		} else if got := request.Header.Get("Authorization"); got != "" {
			t.Fatalf("Authorization leaked on %s: %q", request.URL.Redacted(), got)
		}
	}
}

func TestProductDailymotionClientRunDirectAndHLSIsolation(t *testing.T) {
	for _, test := range []struct {
		name   string
		format string
		want   []byte
	}{
		{name: "direct", format: "http-720@60", want: []byte("DM-DIRECT-BYTES")},
		{name: "hls", format: "hls-auto", want: []byte("DM-HLS-SEGMENT-0DM-HLS-SEGMENT-1")},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newDailymotionRunRoundTripper(t)
			root := t.TempDir()
			result, err := newDailymotionRunClient(transport).Run(context.Background(), Request{
				URL: testURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: test.format,
				Overwrite: true, Subtitles: SubtitleOptions{WriteManual: true},
				RelatedFiles: RelatedFileOptions{WriteInfoJSON: true},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Filename == "" {
				t.Fatal("missing media filename")
			}
			got, err := os.ReadFile(result.Filename)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, test.want) {
				t.Fatalf("media=%q want %q", got, test.want)
			}
			if len(result.Artifacts) < 3 {
				t.Fatalf("artifacts=%#v", result.Artifacts)
			}
			assertDailymotionCredentialIsolation(t, transport)
		})
	}
}

const testURL = "https://www.dailymotion.com/video/xfixture"

func TestProductDailymotionNoPlaylistChoice(t *testing.T) {
	rawURL := testURL + "?playlist=xfixture&sig=a%2Bb&token=keep"
	for _, test := range []struct {
		name              string
		noPlaylist        bool
		wantRootPlaylist  bool
		wantRootExtractor string
		wantCollectionAPI bool
		wantArchive       string
	}{
		{name: "default-prefers-playlist", wantRootPlaylist: true, wantRootExtractor: "dailymotion_playlist", wantCollectionAPI: true, wantArchive: "dailymotion xfixture\n"},
		{name: "no-playlist-prefers-video", noPlaylist: true, wantRootExtractor: "dailymotion", wantCollectionAPI: false, wantArchive: "dailymotion xfixture\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newDailymotionRunRoundTripper(t)
			root := t.TempDir()
			archivePath := filepath.Join(root, "archive.txt")
			result, err := newDailymotionRunClient(transport).Run(context.Background(), Request{
				URL: rawURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "http-720@60",
				Overwrite: true, DownloadArchive: archivePath, Playlist: PlaylistOptions{Disabled: test.noPlaylist, Items: "1"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Extractor != test.wantRootExtractor {
				t.Fatalf("root extractor=%q", result.Extractor)
			}
			if test.wantRootPlaylist {
				if len(result.Entries) != 1 || !result.Entries[0].Downloaded {
					t.Fatalf("playlist result=%#v", result)
				}
			} else if len(result.Entries) != 0 || !result.Downloaded {
				t.Fatalf("video result=%#v", result)
			}
			transport.mu.Lock()
			collectionRequests := 0
			for _, body := range transport.graphqlBodies {
				if bytes.Contains(body, []byte("collection(xid:")) {
					collectionRequests++
				}
			}
			transport.mu.Unlock()
			if (collectionRequests > 0) != test.wantCollectionAPI {
				t.Fatalf("collection requests=%d want present=%t", collectionRequests, test.wantCollectionAPI)
			}
			archive, err := os.ReadFile(archivePath)
			if err != nil || string(archive) != test.wantArchive {
				t.Fatalf("archive=%q err=%v want=%q", archive, err, test.wantArchive)
			}
		})
	}
}

func TestProductDailymotionClientRunRepeatedPublicChildren(t *testing.T) {
	for _, test := range []struct {
		name      string
		url       string
		extractor string
		format    string
		want      []byte
		match     func([]byte) bool
	}{
		{name: "playlist-direct", url: "https://www.dailymotion.com/playlist/xfixture", extractor: "dailymotion_playlist", format: "http-720@60", want: []byte("DM-DIRECT-BYTES"), match: func(body []byte) bool { return bytes.Contains(body, []byte("collection(xid:")) }},
		{name: "search-hls", url: "https://www.dailymotion.com/search/fixture/videos", extractor: "dailymotion_search", format: "hls-auto", want: []byte("DM-HLS-SEGMENT-0DM-HLS-SEGMENT-1"), match: func(body []byte) bool { return bytes.Contains(body, []byte(`"operationName":"SEARCH_QUERY"`)) }},
		{name: "user-direct", url: "https://www.dailymotion.com/user/fixture", extractor: "dailymotion_user", format: "http-720@60", want: []byte("DM-DIRECT-BYTES"), match: func(body []byte) bool { return bytes.Contains(body, []byte("channel(xid:")) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newDailymotionRunRoundTripper(t)
			client := newDailymotionRunClient(transport)
			root := t.TempDir()
			var firstRootInfo, firstChildInfo []byte
			for iteration := 0; iteration < 2; iteration++ {
				result, err := client.Run(context.Background(), Request{
					URL: test.url, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: test.format,
					Overwrite: true, Playlist: PlaylistOptions{Items: "1"},
				})
				if err != nil {
					t.Fatal(err)
				}
				if result.Extractor != test.extractor {
					t.Fatalf("iteration=%d root extractor=%q", iteration, result.Extractor)
				}
				if !result.Downloaded || len(result.Entries) != 1 || result.Entries[0].Extractor != "dailymotion" || !result.Entries[0].Downloaded {
					t.Fatalf("iteration=%d result=%#v", iteration, result)
				}
				child := result.Entries[0]
				if child.Filename == "" {
					t.Fatalf("iteration=%d child filename missing", iteration)
				}
				got, err := os.ReadFile(child.Filename)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, test.want) {
					t.Fatalf("iteration=%d child bytes=%q want %q", iteration, got, test.want)
				}
				if iteration == 0 {
					firstRootInfo = append([]byte(nil), result.InfoJSON...)
					firstChildInfo = append([]byte(nil), child.InfoJSON...)
				} else if !bytes.Equal(result.InfoJSON, firstRootInfo) || !bytes.Equal(child.InfoJSON, firstChildInfo) {
					t.Fatalf("iteration=%d metadata is not deterministic", iteration)
				}
			}
			transport.mu.Lock()
			var pageBodies [][]byte
			for _, body := range transport.graphqlBodies {
				if test.match(body) {
					pageBodies = append(pageBodies, body)
				}
			}
			transport.mu.Unlock()
			if len(pageBodies) != 2 {
				t.Fatalf("listing page requests=%d want 2", len(pageBodies))
			}
			for index, body := range pageBodies {
				pageOne := bytes.Contains(body, []byte(`"page":1`)) || bytes.Contains(body, []byte("page: 1)"))
				if !pageOne {
					var payload struct {
						Variables struct {
							Page int `json:"page"`
						} `json:"variables"`
					}
					if err := json.Unmarshal(body, &payload); err == nil && payload.Variables.Page != 0 {
						t.Fatalf("listing request %d page=%d want fresh page 1", index, payload.Variables.Page)
					}
					t.Fatalf("listing request %d was not a fresh page 1 request: %s", index, body)
				}
			}
			transport.mu.Lock()
			tokenCalls := transport.tokenCalls
			transport.mu.Unlock()
			if tokenCalls != 4 {
				t.Fatalf("token requests=%d want one fresh root and child token request per run", tokenCalls)
			}
			assertDailymotionCredentialIsolation(t, transport)
		})
	}
}

func TestProductDailymotionClientRunStatusesAreCategorizedAndSecretSafe(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		category ErrorCategory
	}{
		{name: "redirect", status: http.StatusFound, category: ErrorNetwork},
		{name: "four-hundred", status: http.StatusBadRequest, category: ErrorNetwork},
		{name: "unauthorized", status: http.StatusUnauthorized, category: ErrorAuthentication},
		{name: "forbidden", status: http.StatusForbidden, category: ErrorAuthentication},
		{name: "not-found", status: http.StatusNotFound, category: ErrorUnsupported},
		{name: "rate-limited", status: http.StatusTooManyRequests, category: ErrorNetwork},
		{name: "legal-restriction", status: http.StatusUnavailableForLegalReasons, category: ErrorUnsupported},
		{name: "server", status: http.StatusServiceUnavailable, category: ErrorNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newDailymotionRunRoundTripper(t)
			transport.metadataStatus = test.status
			root := t.TempDir()
			_, err := newDailymotionRunClient(transport).Run(context.Background(), Request{
				URL: testURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "http-720@60", Overwrite: true,
			})
			if err == nil || !IsCategory(err, test.category) {
				t.Fatalf("status=%d error=%v category=%q", test.status, err, test.category)
			}
			for _, secret := range []string{"fixture-dailymotion-token", "fixture-secret", "session=fixture-secret", "Basic fixture-secret", "https://page.example/fixture"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("status=%d error leaked %q: %v", test.status, secret, err)
				}
			}
			assertDailymotionNoArtifacts(t, root)
			assertDailymotionCredentialIsolation(t, transport)
		})
	}
}

func TestProductDailymotionClientRunFailureCancellationAndZeroArtifacts(t *testing.T) {
	t.Run("failure", func(t *testing.T) {
		transport := newDailymotionRunRoundTripper(t)
		transport.failMedia = true
		root := t.TempDir()
		_, err := newDailymotionRunClient(transport).Run(context.Background(), Request{
			URL: testURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "http-720@60", Overwrite: true,
		})
		if err == nil || !IsCategory(err, ErrorNetwork) {
			t.Fatalf("failure err=%v", err)
		}
		assertDailymotionNoArtifacts(t, root)
		assertDailymotionCredentialIsolation(t, transport)
	})

	t.Run("entered cancellation", func(t *testing.T) {
		transport := newDailymotionRunRoundTripper(t)
		transport.blockMedia = true
		transport.entered = make(chan struct{})
		root := t.TempDir()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := newDailymotionRunClient(transport).Run(ctx, Request{
				URL: testURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "http-720@60", Overwrite: true,
			})
			done <- err
		}()
		select {
		case <-transport.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("media request was not entered")
		}
		cancel()
		select {
		case err := <-done:
			if err == nil || !IsCategory(err, ErrorCancelled) {
				t.Fatalf("cancellation err=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("cancellation did not finish")
		}
		assertDailymotionNoArtifacts(t, root)
		assertDailymotionCredentialIsolation(t, transport)
	})
}

func TestProductDailymotionRunRoundTripperUsesOnlyPinnedURLs(t *testing.T) {
	transport := newDailymotionRunRoundTripper(t)
	root := t.TempDir()
	_, err := newDailymotionRunClient(transport).Run(context.Background(), Request{
		URL: testURL, OutputDir: root, OutputTemplate: "%(id)s.%(ext)s", Format: "hls-auto", Overwrite: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, want := range []string{
		"https://stream-01.dmcdn.net/video/master.m3u8?auth=master%2Bsig&token=two",
		"https://stream-01.dmcdn.net/video/720.m3u8?auth=variant%2Bsig&token=four",
		"https://proxy-01.dailymotion.com/video/segment-0.ts?auth=segment%2Bsig&token=five",
		"https://proxy-02.dailymotion.com/video/segment-1.ts?auth=segment%2Bsig&token=six",
	} {
		found := false
		for _, got := range transport.urls {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing exact URL %q in %v", want, transport.urls)
		}
	}
}
