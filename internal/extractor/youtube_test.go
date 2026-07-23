package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

const (
	youtubeFixtureURL = "https://www.youtube.com/watch?v=fixture0001"
	youtubePlayerURL  = "https://www.youtube.com/s/player/fixture/base.js"
)

type memoryTransport struct {
	pages map[string][]byte
	reads []string
}

type youtubeFallbackTransport struct {
	*memoryTransport
	responses map[string][]byte
	requests  []*http.Request
	bodies    [][]byte
}

func (transport *youtubeFallbackTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected cookie-bearing YouTube fallback request")
}

func (transport *youtubeFallbackTransport) DoWithoutCookies(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Header.Get("Cookie") != "" {
		return nil, errors.New("isolated YouTube fallback request contains cookies")
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.requests = append(transport.requests, request)
	transport.bodies = append(transport.bodies, body)
	response, ok := transport.responses[request.Header.Get("X-Youtube-Client-Name")]
	if !ok {
		return nil, fmt.Errorf("unexpected YouTube client %q", request.Header.Get("X-Youtube-Client-Name"))
	}
	return &http.Response{
		StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(response)),
		Header: make(http.Header), Request: request,
	}, nil
}

func (transport *memoryTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected Do call")
}

func (transport *memoryTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.reads = append(transport.reads, rawURL)
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), page...), make(http.Header), nil
}

func TestYouTubeSuitableAndVideoID(t *testing.T) {
	extractor := NewYouTube()
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=fixture0001",
		"https://youtu.be/fixture0001",
		"https://m.youtube.com/shorts/fixture0001",
		"https://youtube.com/embed/fixture0001",
		"https://youtube.com/live/fixture0001",
		"https://www.youtube-nocookie.com/embed/fixture0001",
		"https://youtube-nocookie.com/embed/fixture0001",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil || !extractor.Suitable(parsed) {
			t.Fatalf("Suitable(%q) = false, %v", rawURL, err)
		}
		if id, err := youtubeVideoID(rawURL); err != nil || id != "fixture0001" {
			t.Fatalf("youtubeVideoID(%q) = %q, %v", rawURL, id, err)
		}
	}
	if id, ok := youtubePlaylistID("https://www.youtube.com/playlist?list=PL_fixture"); !ok || id != "PL_fixture" {
		t.Fatalf("youtubePlaylistID() = %q, %v", id, ok)
	}
	for _, rawURL := range []string{
		"https://example.com/watch?v=fixture0001",
		"https://www.youtube-nocookie.com.evil.example/embed/fixture0001",
		"https://evil-youtube-nocookie.com/embed/fixture0001",
		"https://attacker.youtube-nocookie.com/embed/fixture0001",
	} {
		parsed, _ := url.Parse(rawURL)
		if extractor.Suitable(parsed) {
			t.Fatalf("Suitable(%q) = true, want false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://www.youtube.com/watch?v=short",
		"https://www.youtube.com/playlist?list=fixture0001",
		"https://youtu.be/fixture0001/extra",
		"https://www.youtube-nocookie.com/embed/short",
	} {
		if _, err := youtubeVideoID(rawURL); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("youtubeVideoID(%q) error = %v", rawURL, err)
		}
	}
}

func TestYouTubeNoCookieSuitable(t *testing.T) {
	extractor := NewYouTube()
	for _, rawURL := range []string{
		"https://www.youtube-nocookie.com/embed/fixture0001",
		"https://youtube-nocookie.com/embed/fixture0001",
		"http://www.youtube-nocookie.com/embed/fixture0001",
		"http://youtube-nocookie.com/embed/fixture0001",
		"//www.youtube-nocookie.com/embed/fixture0001",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse(%q): %v", rawURL, err)
		}
		if !extractor.Suitable(parsed) {
			t.Errorf("Suitable(%q) = false; want true", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://attacker.youtube-nocookie.com/embed/fixture0001",
		"https://youtube-nocookie.com.evil.example/embed/fixture0001",
		"https://evil-youtube-nocookie.com/embed/fixture0001",
		"https://notyoutube-nocookie.com/embed/fixture0001",
		"https://youtube-nocookie.com.attacker.com/embed/fixture0001",
	} {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("parse(%q): %v", rawURL, err)
		}
		if extractor.Suitable(parsed) {
			t.Errorf("Suitable(%q) = true; want false (lookalike)", rawURL)
		}
	}
}

func TestYouTubeNoCookieParseTarget(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		for _, test := range []struct {
			url              string
			start, end       float64
			hasStart, hasEnd bool
		}{
			{"https://www.youtube-nocookie.com/embed/fixture0001", 0, 0, false, false},
			{"https://youtube-nocookie.com/embed/fixture0001", 0, 0, false, false},
			{"http://www.youtube-nocookie.com/embed/fixture0001", 0, 0, false, false},
			{"http://youtube-nocookie.com/embed/fixture0001", 0, 0, false, false},
			{"//www.youtube-nocookie.com/embed/fixture0001", 0, 0, false, false},
			{"https://www.youtube-nocookie.com/embed/fixture0001?t=10&end=20", 10, 20, true, true},
			{"https://www.youtube-nocookie.com/embed/fixture0001#t=1h2m&end=2h", 3720, 7200, true, true},
		} {
			target, err := parseYouTubeTarget(test.url)
			if err != nil {
				t.Fatalf("parseYouTubeTarget(%q): %v", test.url, err)
			}
			if target.videoID != "fixture0001" {
				t.Fatalf("parseYouTubeTarget(%q).videoID = %q", test.url, target.videoID)
			}
			if (target.startTime != nil) != test.hasStart || (target.endTime != nil) != test.hasEnd {
				t.Fatalf("parseYouTubeTarget(%q) start/end presence mismatch: %#v", test.url, target)
			}
			if target.startTime != nil && *target.startTime != test.start {
				t.Fatalf("parseYouTubeTarget(%q).startTime = %v, want %v", test.url, *target.startTime, test.start)
			}
			if target.endTime != nil && *target.endTime != test.end {
				t.Fatalf("parseYouTubeTarget(%q).endTime = %v, want %v", test.url, *target.endTime, test.end)
			}
		}
	})

	t.Run("unsupported-routes", func(t *testing.T) {
		for _, rawURL := range []string{
			"https://www.youtube-nocookie.com/watch?v=fixture0001",
			"https://www.youtube-nocookie.com/shorts/fixture0001",
			"https://www.youtube-nocookie.com/live/fixture0001",
			"https://www.youtube-nocookie.com/channel/UCfixture_channel_00001",
			"https://www.youtube-nocookie.com/@fixture/live",
			"https://www.youtube-nocookie.com/playlist?list=PL_fixture",
			"https://www.youtube-nocookie.com/c/fixture-name/live",
			"https://www.youtube-nocookie.com/user/fixture.name/live",
		} {
			if _, err := parseYouTubeTarget(rawURL); !errors.Is(err, ErrUnsupported) {
				t.Errorf("parseYouTubeTarget(%q): err = %v; want ErrUnsupported", rawURL, err)
			}
		}
	})

	t.Run("path-shape-rejected", func(t *testing.T) {
		for _, rawURL := range []string{
			"https://www.youtube-nocookie.com/embed/fixture0001/",
			"https://www.youtube-nocookie.com//embed/fixture0001",
			"https://www.youtube-nocookie.com/embed//fixture0001",
			"https://www.youtube-nocookie.com/embed/fixture0001/extra",
		} {
			if _, err := parseYouTubeTarget(rawURL); !errors.Is(err, ErrUnsupported) {
				t.Errorf("parseYouTubeTarget(%q): err = %v; want ErrUnsupported", rawURL, err)
			}
		}
	})

	t.Run("host-confusion-rejected", func(t *testing.T) {
		for _, rawURL := range []string{
			"https://evil-youtube-nocookie.com/embed/fixture0001",
			"https://youtube-nocookie.com.evil.example/embed/fixture0001",
			"https://attacker.youtube-nocookie.com/embed/fixture0001",
			"https://example.com/embed/fixture0001",
			"https://example.com/watch?v=fixture0001",
		} {
			if _, err := parseYouTubeTarget(rawURL); !errors.Is(err, ErrUnsupported) {
				t.Errorf("parseYouTubeTarget(%q): err = %v; want ErrUnsupported", rawURL, err)
			}
		}
	})

	t.Run("hostile-forms-rejected", func(t *testing.T) {
		for _, rawURL := range []string{
			"https://user@www.youtube-nocookie.com/embed/fixture0001",
			"https://user:pass@www.youtube-nocookie.com/embed/fixture0001",
			"https://www.youtube-nocookie.com:443/embed/fixture0001",
			"https://www.youtube-nocookie.com:8080/embed/fixture0001",
			"https://www.youtube-nocookie.com:/embed/fixture0001",
			"ftp://www.youtube-nocookie.com/embed/fixture0001",
			"file:///etc/passwd",
			"https://www.youtube-nocookie.com/embed/fixture0001%2Fextra",
			"https://www.youtube-nocookie.com/embed/fixture0001%5cextra",
			"https://www.youtube-nocookie.com/embed/fix%00ture0001",
			"https://www.youtube-nocookie.com/embed/short",
		} {
			if _, err := parseYouTubeTarget(rawURL); !errors.Is(err, ErrUnsupported) {
				t.Errorf("parseYouTubeTarget(%q): err = %v; want ErrUnsupported", rawURL, err)
			}
		}
	})
}

func TestYouTubeNoCookieDeterministicExtraction(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	const embedURL = "https://www.youtube-nocookie.com/embed/fixture0001"
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
		// Register the embed URL so any accidental fetch is visible.
		embedURL: watch,
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: embedURL, Transport: transport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "fixture0001" {
		t.Fatalf("id = %q", id)
	}
	if rawURL, _ := result.Info.Lookup("webpage_url").StringValue(); rawURL != youtubeFixtureURL {
		t.Fatalf("webpage_url = %q; want %q", rawURL, youtubeFixtureURL)
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("formats = 0")
	}
	// The nocookie URL must never be fetched; only canonical watch + player JS.
	wantReads := []string{youtubeFixtureURL, youtubePlayerURL}
	if !reflect.DeepEqual(transport.reads, wantReads) {
		t.Fatalf("reads = %v; want %v", transport.reads, wantReads)
	}
}

func TestYouTubeNoCookieContextCancellation(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	_, err = NewYouTube().Extract(ctx, Request{
		URL: "https://www.youtube-nocookie.com/embed/fixture0001", Transport: transport, ChallengeSolver: solver,
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v; want errors.Is(err, context.Canceled)", err)
	}
	// Cancellation must not cause additional transport work beyond what was
	// already in-flight. The first ReadPage should fail immediately.
	if len(transport.reads) > 1 {
		t.Fatalf("transport.reads = %v; expected at most 1 read before cancellation", transport.reads)
	}
}

func TestYouTubeExtractRejectsHostilePlaylistURLs(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{}}
	for _, rawURL := range []string{
		"ftp://www.youtube.com/playlist?list=PL_fixture",
		"https://user@www.youtube.com/playlist?list=PL_fixture",
		"https://user:pass@www.youtube.com/playlist?list=PL_fixture",
		"https://www.youtube.com:443/playlist?list=PL_fixture",
		"https://www.youtube.com:8080/playlist?list=PL_fixture",
		"https://www.youtube.com:/playlist?list=PL_fixture",
		"https://example.com/playlist?list=PL_fixture",
		"https://evil-youtube.com/playlist?list=PL_fixture",
		"https://www.youtube.com/playlist%2F?list=PL_fixture",
		"https://www.youtube.com/playlist%5c?list=PL_fixture",
		"https://www.youtube.com/play%00list?list=PL_fixture",
	} {
		_, err := NewYouTube().Extract(context.Background(), Request{
			URL: rawURL, Transport: transport,
		})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) error = %v; want ErrUnsupported", rawURL, err)
		}
	}
	// Verify no transport requests were made for any hostile URL.
	if len(transport.reads) != 0 {
		t.Fatalf("transport.reads = %v; want empty (no requests for hostile URLs)", transport.reads)
	}
}

func TestYouTubeExtractRejectsHostileLiveAliasURLs(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{}}
	for _, rawURL := range []string{
		"ftp://www.youtube.com/@fixture/live",
		"https://user@www.youtube.com/@fixture/live",
		"https://www.youtube.com:443/@fixture/live",
		"https://www.youtube.com:/@fixture/live",
		"https://example.com/@fixture/live",
		"https://www.youtube.com/@fix%2fture/live",
		"https://www.youtube.com/@fix%00ture/live",
	} {
		_, err := NewYouTube().Extract(context.Background(), Request{
			URL: rawURL, Transport: transport,
		})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) error = %v; want ErrUnsupported", rawURL, err)
		}
	}
	if len(transport.reads) != 0 {
		t.Fatalf("transport.reads = %v; want empty (no requests for hostile URLs)", transport.reads)
	}
}

func TestYouTubeExtractRejectsEmptyPortAllRoutes(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{}}
	for _, rawURL := range []string{
		// Standard video routes.
		"https://www.youtube.com:/watch?v=fixture0001",
		"https://youtube.com:/embed/fixture0001",
		"https://youtu.be:/fixture0001",
		// Nocookie route.
		"https://www.youtube-nocookie.com:/embed/fixture0001",
	} {
		_, err := NewYouTube().Extract(context.Background(), Request{
			URL: rawURL, Transport: transport,
		})
		if !errors.Is(err, ErrUnsupported) {
			t.Errorf("Extract(%q) error = %v; want ErrUnsupported", rawURL, err)
		}
	}
	if len(transport.reads) != 0 {
		t.Fatalf("transport.reads = %v; want empty", transport.reads)
	}
}

// stubChallengeSolver returns a fixed error from SolvePlayer, allowing tests
// to exercise the cancellation guard in resolveYouTubeURLs without running the
// real JavaScript engine.
type stubChallengeSolver struct {
	err error
}

func (s stubChallengeSolver) SolvePlayer(context.Context, string, string, []ejs.ChallengeRequest, bool) (ejs.Result, error) {
	return ejs.Result{}, s.err
}

func TestYouTubeSolverCancellationPropagation(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")

	for _, test := range []struct {
		name    string
		solver  stubChallengeSolver
		wantIs  error
		wantNot error
	}{
		{
			name:    "context.Canceled",
			solver:  stubChallengeSolver{err: context.Canceled},
			wantIs:  context.Canceled,
			wantNot: ErrChallengeSolver,
		},
		{
			name:    "context.DeadlineExceeded",
			solver:  stubChallengeSolver{err: context.DeadlineExceeded},
			wantIs:  context.DeadlineExceeded,
			wantNot: ErrChallengeSolver,
		},
		{
			name:    "wrapped cancellation",
			solver:  stubChallengeSolver{err: fmt.Errorf("solver timeout: %w", context.Canceled)},
			wantIs:  context.Canceled,
			wantNot: ErrChallengeSolver,
		},
		{
			name:   "normal solver failure remains ErrChallengeSolver",
			solver: stubChallengeSolver{err: errors.New("n-param transform failed")},
			wantIs: ErrChallengeSolver,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := &memoryTransport{pages: map[string][]byte{
				youtubeFixtureURL: watch,
				youtubePlayerURL:  player,
			}}
			_, err := NewYouTube().Extract(context.Background(), Request{
				URL:             "https://www.youtube-nocookie.com/embed/fixture0001",
				Transport:       transport,
				ChallengeSolver: test.solver,
			})
			if err == nil {
				t.Fatal("expected error from stub solver")
			}
			if !errors.Is(err, test.wantIs) {
				t.Fatalf("error = %v; want errors.Is(err, %v)", err, test.wantIs)
			}
			if test.wantNot != nil && errors.Is(err, test.wantNot) {
				t.Fatalf("error = %v; must NOT satisfy errors.Is(err, %v)", err, test.wantNot)
			}
		})
	}
}

func TestYouTubeChannelLiveAliasMatching(t *testing.T) {
	for _, rawURL := range []string{
		"https://www.youtube.com/@fixture/live",
		"https://youtube.com/channel/UCfixture_channel_00001/live",
		"https://m.youtube.com/user/fixture.name/live/",
		"https://www.youtube.com/c/fixture-name/live",
		"https://www.youtube.com/c/ИгорьКлейнер/live?feature=share#ignored",
		"http://youtube.com/TheYoungTurks/live?feature=share",
	} {
		if !youtubeChannelLiveAlias(rawURL) {
			t.Errorf("youtubeChannelLiveAlias(%q) = false", rawURL)
		}
	}
	for _, rawURL := range []string{
		"https://www.youtube.com/@fixture/videos",
		"https://example.com/@fixture/live",
		"https://www.youtube.com/@fixture%2Flive",
		"https://www.youtube.com/watch/live",
		"https://www.youtube.com/feed/live",
		"https://www.youtube.com/signin/live",
		"https://www.youtube.com/s/live",
	} {
		if youtubeChannelLiveAlias(rawURL) {
			t.Errorf("youtubeChannelLiveAlias(%q) = true", rawURL)
		}
	}
	canonical, ok := youtubeChannelLiveAliasURL("http://m.youtube.com/@fixture/live?feature=share#ignored")
	if !ok || canonical != "https://www.youtube.com/@fixture/live" {
		t.Fatalf("canonical alias = %q, %v", canonical, ok)
	}
}

func TestYouTubeChannelLiveAliasResolvesThroughVideoExtractor(t *testing.T) {
	const alias = "https://www.youtube.com/@fixture/live"
	watch := readYouTubeFixture(t, "live-watch.html")
	transport := &memoryTransport{pages: map[string][]byte{
		alias: watch, "https://www.youtube.com/watch?v=livefix0001": watch,
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: alias, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "livefix0001" {
		t.Fatalf("id = %q", id)
	}
	if status, _ := result.Info.Lookup("live_status").StringValue(); status != "is_live" {
		t.Fatalf("live_status = %q", status)
	}
	if !reflect.DeepEqual(transport.reads, []string{alias, "https://www.youtube.com/watch?v=livefix0001"}) {
		t.Fatalf("reads = %v", transport.reads)
	}
}

func TestYouTubeChannelLiveAliasOfflineAndMalformed(t *testing.T) {
	const alias = "https://www.youtube.com/@fixture/live"
	transport := &memoryTransport{pages: map[string][]byte{alias: []byte(`ytInitialData={"contents":{}};`)}}
	if _, err := NewYouTube().Extract(context.Background(), Request{URL: alias, Transport: transport}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("offline error = %v", err)
	}

	badPlayer := []byte(`ytInitialPlayerResponse={"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"bad"}};`)
	transport = &memoryTransport{pages: map[string][]byte{alias: badPlayer}}
	if _, err := NewYouTube().Extract(context.Background(), Request{URL: alias, Transport: transport}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("malformed error = %v", err)
	}
}

func TestParseYouTubeTargetOffsets(t *testing.T) {
	for _, test := range []struct {
		url              string
		start, end       float64
		hasStart, hasEnd bool
	}{
		{"https://www.youtube.com/watch?v=fixture0001&t=1s&end=9", 1, 9, true, true},
		{"https://www.youtube.com/watch?v=fixture0001#t=1h2m3.5s&end=4000", 3723.5, 4000, true, true},
		{"https://www.youtube.com/watch?v=fixture0001#t=2m&t=3m", 120, 0, true, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=bad&start=7", 0, 0, false, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=-1&end=huge", 0, 0, false, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=1:02", 62, 0, true, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=1:02:03.5", 3723.5, 0, true, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=P1DT2H3M4S", 93784, 0, true, false},
		{"https://www.youtube.com/watch?v=fixture0001&t=1.5hours", 5400, 0, true, false},
	} {
		target, err := parseYouTubeTarget(test.url)
		if err != nil {
			t.Fatalf("parseYouTubeTarget(%q): %v", test.url, err)
		}
		if target.videoID != "fixture0001" || (target.startTime != nil) != test.hasStart || (target.endTime != nil) != test.hasEnd {
			t.Fatalf("parseYouTubeTarget(%q) = %#v", test.url, target)
		}
		if target.startTime != nil && *target.startTime != test.start {
			t.Fatalf("start(%q) = %v", test.url, *target.startTime)
		}
		if target.endTime != nil && *target.endTime != test.end {
			t.Fatalf("end(%q) = %v", test.url, *target.endTime)
		}
	}
}

func TestParseYouTubeOffsetReferenceCases(t *testing.T) {
	// Derived from yt-dlp test/test_utils.py::test_parse_duration at
	// aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8.
	for _, test := range []struct {
		input string
		want  float64
	}{
		{"1337:12", 80232},
		{"3 hours, 11 mins, 53 secs", 11513},
		{"01:02:03.05", 3723.05},
		{"T30M38S", 1838},
		{"1 hour 3 minutes", 3780},
		{"87 Min.", 5220},
		{"PT1H0.040S", 3600.04},
		{"PT00H03M30SZ", 210},
		{"P0Y0M0DT0H4M20.880S", 260.88},
		{"01:02:03:050", 3723.05},
		{"103:050", 103.05},
		{"1HR 3MIN", 3780},
		{"2hrs 3mins", 7380},
	} {
		got, ok := parseYouTubeOffset(test.input)
		if !ok || math.Abs(got-test.want) > 1e-9 {
			t.Errorf("parseYouTubeOffset(%q) = (%v, %v), want (%v, true)", test.input, got, ok, test.want)
		}
	}
}

func TestYouTubeExtractionPreservesURLOffsets(t *testing.T) {
	watch := readYouTubeFixture(t, "live-watch.html")
	transport := &memoryTransport{pages: map[string][]byte{"https://www.youtube.com/watch?v=livefix0001": watch}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/watch?v=livefix0001&t=1s&end=9", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	start, _ := result.Info.Lookup("start_time").Int()
	end, _ := result.Info.Lookup("end_time").Int()
	if start != 1 || end != 9 {
		t.Fatalf("offsets = %d, %d", start, end)
	}
}

func TestYouTubeExtractsPinnedVideoAndSolvesChallenges(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	player := readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js")
	expected := readYouTubeFixture(t, "expected.json")
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  player,
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatal(err)
	}
	var actual bytes.Buffer
	encoder := json.NewEncoder(&actual)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result.Info.Fields()); err != nil {
		t.Fatal(err)
	}
	var expectedDocument, actualDocument any
	if err := json.Unmarshal(expected, &expectedDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(actual.Bytes(), &actualDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("metadata mismatch\nactual:   %s\nexpected: %s", actual.Bytes(), expected)
	}
	if len(transport.reads) != 2 || transport.reads[0] != youtubeFixtureURL || transport.reads[1] != youtubePlayerURL {
		t.Fatalf("reads = %#v", transport.reads)
	}
}

func TestYouTubeDiscoversPlayerJavaScriptFromPageConfig(t *testing.T) {
	watch := bytes.Replace(
		readYouTubeFixture(t, "watch.html"),
		[]byte(`"assets": {"js": "/s/player/fixture/base.js"}`),
		[]byte(`"assets": {}`),
		1,
	)
	watch = bytes.Replace(watch, []byte("<body>"), []byte(`<body><script>
      var unrelated = {"jsUrl":"https://attacker.example/s/player/bad/base.js"};
      ytcfg.set({"WEB_PLAYER_CONTEXT_CONFIGS":{"WEB_PLAYER_CONTEXT_CONFIG_ID_KEVLAR_WATCH":{"jsUrl":"\/s\/player\/fixture\/base.js"}}});
    </script>`), 1)
	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: watch,
		youtubePlayerURL:  readYouTubeFixture(t, "../../javascript/ejs-0.8.0/synthetic-player.js"),
	}}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: youtubeFixtureURL, Transport: transport, ChallengeSolver: solver,
	})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 4 || len(transport.reads) != 2 || transport.reads[1] != youtubePlayerURL {
		t.Fatalf("formats=%d reads=%v", len(formats), transport.reads)
	}
}

func TestYouTubePlayerURLValidation(t *testing.T) {
	for _, playerPath := range []string{
		"/s/player/fixture/base.js",
		"https://www.youtube.com/s/player/fixture/base.js?cache=1",
		"https://www.youtube-nocookie.com/s/player/fixture/base.js",
	} {
		if _, err := resolveYouTubePlayerURL(youtubeFixtureURL, playerPath); err != nil {
			t.Fatalf("resolveYouTubePlayerURL(%q) error = %v", playerPath, err)
		}
	}
	for _, playerPath := range []string{
		"http://www.youtube.com/s/player/fixture/base.js",
		"https://attacker.example/s/player/fixture/base.js",
		"https://localhost/s/player/fixture/base.js",
		"https://user@www.youtube.com/s/player/fixture/base.js",
		"https://www.youtube.com:444/s/player/fixture/base.js",
		"https://www.youtube.com/api/internal.js",
		"https://www.youtube.com/s/player/../private.js",
		"https://www.youtube.com/s/player/%2e%2e/private.js",
		"https://www.youtube.com/s/player/fixture/base.js#fragment",
	} {
		if _, err := resolveYouTubePlayerURL(youtubeFixtureURL, playerPath); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("resolveYouTubePlayerURL(%q) error = %v", playerPath, err)
		}
	}
}

func TestYouTubePageConfigParsingIsStructuredAndBounded(t *testing.T) {
	var page strings.Builder
	page.WriteString(`var unrelated={"PLAYER_JS_URL":"https://attacker.example/s/player/bad/base.js","VISITOR_DATA":"bad"};`)
	for index := 0; index <= youtubeMaxPageConfigs; index++ {
		fmt.Fprintf(&page, `ytcfg.set({"VISITOR_DATA":"visitor-%d"});`, index)
	}
	config := discoverYouTubePageConfig([]byte(page.String()))
	if config.PlayerJSURL != "" || config.VisitorData != "visitor-7" {
		t.Fatalf("config = %#v", config)
	}
}

func TestYouTubeRecoversURLBearingFormatsFromNativeClient(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{
			"3":  readYouTubeFixture(t, "android-player.json"),
			"28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if title, _ := result.Info.Lookup("title").StringValue(); title != "Synthetic SABR YouTube Video" {
		t.Fatalf("title = %q", title)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats = %#v", formats)
	}
	format, _ := formats[0].Object()
	if rawURL, _ := format.Lookup("url").StringValue(); rawURL != "https://media.example/android-video.mp4" {
		t.Fatalf("format = %#v", format)
	}
	if len(transport.requests) != 2 {
		t.Fatalf("requests = %d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Method != http.MethodPost || request.URL.String() != youtubePlayerAPIURL ||
		request.Header.Get("X-Youtube-Client-Name") != "3" ||
		request.Header.Get("X-Youtube-Client-Version") != "21.26.364" ||
		request.Header.Get("X-Goog-Visitor-Id") != "fixture-visitor" ||
		request.Header.Get("User-Agent") == "" {
		t.Fatalf("request = %s %s headers=%v", request.Method, request.URL, request.Header)
	}
	var body struct {
		VideoID      string `json:"videoId"`
		ContentCheck bool   `json:"contentCheckOk"`
		RacyCheck    bool   `json:"racyCheckOk"`
		Context      struct {
			Client struct {
				Name    string `json:"clientName"`
				Version string `json:"clientVersion"`
				Visitor string `json:"visitorData"`
			} `json:"client"`
		} `json:"context"`
		PlaybackContext struct {
			Content struct {
				Preference string `json:"html5Preference"`
			} `json:"contentPlaybackContext"`
		} `json:"playbackContext"`
	}
	if err := json.Unmarshal(transport.bodies[0], &body); err != nil || body.VideoID != "fixture0001" ||
		!body.ContentCheck || !body.RacyCheck || body.Context.Client.Name != "ANDROID" ||
		body.Context.Client.Version != "21.26.364" || body.Context.Client.Visitor != "fixture-visitor" ||
		body.PlaybackContext.Content.Preference != "HTML5_PREF_WANTS" {
		t.Fatalf("body = %#v, error=%v", body, err)
	}
}

func TestYouTubeAppliesPlayerAndGVSTokensToIsolatedRecovery(t *testing.T) {
	director, err := youtubepot.New(youtubepot.Config{
		Policy: youtubepot.FetchAlways,
		Providers: []youtubepot.Provider{youtubepot.ProviderFunc{ProviderName: "fixture", Function: func(_ context.Context, request youtubepot.Request) (youtubepot.Response, error) {
			switch request.Context {
			case youtubepot.ContextPlayer:
				return youtubepot.Response{Token: "cGxheWVy"}, nil
			case youtubepot.ContextGVS:
				return youtubepot.Response{Token: "Z3Zz"}, nil
			default:
				return youtubepot.Response{}, youtubepot.ErrRejected
			}
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{
			"3": readYouTubeFixture(t, "android-player.json"), "28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport, YouTubePOT: director})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.bodies) != 2 {
		t.Fatalf("request bodies = %d", len(transport.bodies))
	}
	for _, body := range transport.bodies {
		if !bytes.Contains(body, []byte(`"serviceIntegrityDimensions":{"poToken":"cGxheWVy"}`)) {
			t.Fatalf("player token missing from request: %s", body)
		}
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("no tokenized formats")
	}
	for _, item := range formats {
		format, _ := item.Object()
		rawURL, _ := format.Lookup("url").StringValue()
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Query().Get("pot") != "Z3Zz" {
			t.Fatalf("format URL is not tokenized: %q", rawURL)
		}
	}
}

func TestYouTubeGVSTokenPlacement(t *testing.T) {
	player := youtubePlayerResponse{}
	player.StreamingData.Formats = []youtubeFormat{{URL: "https://media.example/video?x=1"}}
	player.StreamingData.AdaptiveFormats = []youtubeFormat{{SignatureCipher: "url=https%3A%2F%2Fmedia.example%2Faudio&sp=sig&s=fixture"}}
	player.StreamingData.HLSManifestURL = "https://media.example/live/master.m3u8?keep=1"
	player.StreamingData.DASHManifestURL = "https://media.example/dash/manifest.mpd"
	applyYouTubeGVSToken(&player, "Z3Zz")

	if parsed, _ := url.Parse(player.StreamingData.Formats[0].URL); parsed.Query().Get("pot") != "Z3Zz" || parsed.Query().Get("x") != "1" {
		t.Fatalf("direct URL = %q", player.StreamingData.Formats[0].URL)
	}
	cipher, err := url.ParseQuery(player.StreamingData.AdaptiveFormats[0].SignatureCipher)
	if err != nil {
		t.Fatal(err)
	}
	if parsed, _ := url.Parse(cipher.Get("url")); parsed.Query().Get("pot") != "Z3Zz" {
		t.Fatalf("cipher URL = %q", cipher.Get("url"))
	}
	for _, manifest := range []string{player.StreamingData.HLSManifestURL, player.StreamingData.DASHManifestURL} {
		parsed, err := url.Parse(manifest)
		if err != nil || !strings.HasSuffix(parsed.Path, "/pot/Z3Zz") {
			t.Fatalf("manifest URL = %q, error=%v", manifest, err)
		}
	}
}

func TestYouTubeRecoveryFailsClosedWithoutCookieIsolation(t *testing.T) {
	transport := &memoryTransport{pages: map[string][]byte{
		youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
	}}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("error = %v", err)
	}
}

func TestYouTubeAuthenticatedPageDoesNotUseAnonymousRecovery(t *testing.T) {
	page := bytes.Replace(readYouTubeFixture(t, "sabr-watch.html"), []byte(`"LOGGED_IN":false`), []byte(`"LOGGED_IN":true`), 1)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}},
		responses: map[string][]byte{
			"3": readYouTubeFixture(t, "android-player.json"), "28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrAuthentication) || len(transport.requests) != 0 {
		t.Fatalf("error=%v requests=%d", err, len(transport.requests))
	}
}

func TestYouTubeRecoveryContinuesAfterOneClientFails(t *testing.T) {
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{
			"3": []byte(`{"playabilityStatus":`), "28": readYouTubeFixture(t, "android-vr-player.json"),
		},
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) != 2 || len(transport.requests) != 2 {
		t.Fatalf("formats=%d requests=%d", len(formats), len(transport.requests))
	}
}

func TestYouTubeSABRFallbackFailureIsCategorizedAndCancelable(t *testing.T) {
	unavailable := []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED","reason":"fixture"}}`)
	transport := &youtubeFallbackTransport{
		memoryTransport: &memoryTransport{pages: map[string][]byte{
			youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
		}},
		responses: map[string][]byte{"3": unavailable, "28": unavailable},
	}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrUnavailable) || len(transport.requests) != 2 {
		t.Fatalf("error=%v requests=%d", err, len(transport.requests))
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport.requests = nil
	_, err = NewYouTube().Extract(ctx, Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, context.Canceled) || len(transport.requests) != 0 {
		t.Fatalf("cancellation error=%v requests=%d", err, len(transport.requests))
	}
}

func TestYouTubeRejectsMalformedNativeClientResponses(t *testing.T) {
	for name, response := range map[string][]byte{
		"invalid JSON": []byte(`{"playabilityStatus":`),
		"wrong video":  []byte(`{"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":"different01"},"streamingData":{"formats":[{"itag":18,"url":"https://media.example/video.mp4"}]}}`),
	} {
		t.Run(name, func(t *testing.T) {
			transport := &youtubeFallbackTransport{
				memoryTransport: &memoryTransport{pages: map[string][]byte{
					youtubeFixtureURL: readYouTubeFixture(t, "sabr-watch.html"),
				}},
				responses: map[string][]byte{
					"3":  response,
					"28": []byte(`{"playabilityStatus":{"status":"LOGIN_REQUIRED"}}`),
				},
			}
			_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
			if !errors.Is(err, ErrInvalidMetadata) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestYouTubeChallengeAndAvailabilityFailuresAreCategorized(t *testing.T) {
	watch := readYouTubeFixture(t, "watch.html")
	transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: watch}}
	_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
	if !errors.Is(err, ErrChallengeSolver) {
		t.Fatalf("missing challenge solver error = %v", err)
	}

	for _, test := range []struct {
		status string
		want   error
	}{
		{"LOGIN_REQUIRED", ErrAuthentication},
		{"ERROR", ErrUnavailable},
	} {
		page := []byte(`ytInitialPlayerResponse = {"playabilityStatus":{"status":"` + test.status + `","reason":"fixture reason"}};`)
		transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
		_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
		if !errors.Is(err, test.want) {
			t.Fatalf("status %s error = %v", test.status, err)
		}
	}
}

func TestYouTubeCanonicalizesShortURLsBeforeFetching(t *testing.T) {
	page := []byte(`ytInitialPlayerResponse = {"playabilityStatus":{"status":"ERROR","reason":"fixture reason"}};`)
	transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
	_, err := NewYouTube().Extract(context.Background(), Request{
		URL: "https://youtu.be/fixture0001", Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if len(transport.reads) != 1 || transport.reads[0] != youtubeFixtureURL {
		t.Fatalf("reads = %#v", transport.reads)
	}
}

func TestYouTubeRejectsMalformedPlayerResponse(t *testing.T) {
	for _, page := range [][]byte{
		[]byte("no player marker"),
		[]byte("ytInitialPlayerResponse = {\"open\": true"),
		[]byte("ytInitialPlayerResponse = {not-json};"),
	} {
		transport := &memoryTransport{pages: map[string][]byte{youtubeFixtureURL: page}}
		_, err := NewYouTube().Extract(context.Background(), Request{URL: youtubeFixtureURL, Transport: transport})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("page %q error = %v", page, err)
		}
	}
}

type youtubePlaylistTransport struct {
	page         []byte
	continuation []byte
	status       int
	reads        []string
	requests     int
}

func (transport *youtubePlaylistTransport) ReadPage(_ context.Context, rawURL string) ([]byte, http.Header, error) {
	transport.reads = append(transport.reads, rawURL)
	if rawURL != "https://www.youtube.com/playlist?list=PL_fixture" {
		return nil, nil, fmt.Errorf("unexpected URL %q", rawURL)
	}
	return append([]byte(nil), transport.page...), make(http.Header), nil
}

func (transport *youtubePlaylistTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.requests++
	if request.Method != http.MethodPost || request.URL.Path != "/youtubei/v1/browse" ||
		request.URL.Query().Get("key") != "fixture-key" || request.URL.Query().Get("prettyPrint") != "false" ||
		request.Header.Get("X-Youtube-Client-Version") != youtubeDefaultClientVersion {
		return nil, fmt.Errorf("unexpected continuation request: %s %s headers=%v", request.Method, request.URL, request.Header)
	}
	body, err := io.ReadAll(request.Body)
	if err != nil || !strings.Contains(string(body), `"continuation":"fixture-token-2"`) || !strings.Contains(string(body), `"visitorData":"fixture-visitor"`) {
		return nil, fmt.Errorf("unexpected continuation body: %s: %v", body, err)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status, Body: io.NopCloser(bytes.NewReader(transport.continuation)),
		Header: make(http.Header), Request: request,
	}, nil
}

func TestYouTubePlaylistIsLazyPagedAndMatchesPinnedShape(t *testing.T) {
	transport := &youtubePlaylistTransport{
		page: readYouTubeFixture(t, "playlist.html"), continuation: readYouTubeFixture(t, "playlist-continuation.json"),
	}
	result, err := NewYouTube().Extract(context.Background(), Request{
		URL: "https://www.youtube.com/playlist?feature=share&list=PL_fixture", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() || transport.requests != 0 || len(transport.reads) != 1 {
		t.Fatalf("result=%#v reads=%v requests=%d", result, transport.reads, transport.requests)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 10)
	if err != nil || transport.requests != 1 {
		t.Fatalf("entries=%#v error=%v requests=%d", entries, err, transport.requests)
	}
	info := value.NewInfo(result.Info.Fields().Clone())
	entryValues := make([]value.Value, len(entries))
	for index, entry := range entries {
		entryValues[index] = value.ObjectValue(entry.Object())
	}
	info.Set("entries", value.List(entryValues...))
	actual, err := json.Marshal(info.Fields())
	if err != nil {
		t.Fatal(err)
	}
	var actualDocument, expectedDocument any
	if err := json.Unmarshal(actual, &actualDocument); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readYouTubeFixture(t, "playlist-expected.json"), &expectedDocument); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actualDocument, expectedDocument) {
		t.Fatalf("playlist mismatch\nactual: %s\nexpected: %#v", actual, expectedDocument)
	}
}

func TestYouTubePlaylistParsesModernLockupAndContinuationViewModels(t *testing.T) {
	page := readYouTubeFixture(t, "playlist-modern.html")
	raw, err := extractJSONObject(page, youtubeInitialDataMarker)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseYouTubePlaylistData(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.title != "Modern fixture playlist" || parsed.continuation != "modern-token-2" || len(parsed.entries) != 2 {
		t.Fatalf("parsed = %#v", parsed)
	}
	entry := parsed.entries[0]
	if entry.ID != "modern00001" || entry.Title != "Modern fixture video" || entry.URL != "https://www.youtube.com/watch?v=modern00001" || entry.ExtractorKey != "youtube" {
		t.Fatalf("entry = %#v", entry)
	}
	if parsed.entries[1].ID != "modern00001" || parsed.entries[1].Title != "Repeated fixture video" {
		t.Fatalf("repeated entry = %#v", parsed.entries[1])
	}

	continued, err := parseYouTubePlaylistData(readYouTubeFixture(t, "playlist-modern-continuation.json"))
	if err != nil || len(continued.entries) != 1 || continued.entries[0].ID != "modern00002" {
		t.Fatalf("continued = %#v, %v", continued, err)
	}
}

func TestYouTubePlaylistScopesSelectedTabAndFirstContinuationAction(t *testing.T) {
	initial := []byte(`{
		"contents":{"twoColumnBrowseResultsRenderer":{"tabs":[
			{"tabRenderer":{"selected":false,"content":{"playlistVideoRenderer":{"videoId":"decoy000001","title":{"simpleText":"decoy"}}}}},
			{"tabRenderer":{"selected":true,"content":{"playlistVideoRenderer":{"videoId":"chosen00001","title":{"simpleText":"chosen"}}}}}
		]}}
	}`)
	parsed, err := parseYouTubePlaylistData(initial)
	if err != nil || len(parsed.entries) != 1 || parsed.entries[0].ID != "chosen00001" {
		t.Fatalf("initial parsed=%#v err=%v", parsed, err)
	}

	continuation := []byte(`{
		"onResponseReceivedActions":[
			{"appendContinuationItemsAction":{"continuationItems":[{"playlistVideoRenderer":{"videoId":"first000001","title":{"simpleText":"first"}}}]}},
			{"appendContinuationItemsAction":{"continuationItems":[{"playlistVideoRenderer":{"videoId":"decoy000002","title":{"simpleText":"decoy"}}}]}}
		],
		"unrelated":{"playlistVideoRenderer":{"videoId":"decoy000003","title":{"simpleText":"decoy"}}}
	}`)
	parsed, err = parseYouTubePlaylistData(continuation)
	if err != nil || len(parsed.entries) != 1 || parsed.entries[0].ID != "first000001" {
		t.Fatalf("continuation parsed=%#v err=%v", parsed, err)
	}
}

func TestYouTubePlaylistContinuationRefreshesVisitorData(t *testing.T) {
	parsed, err := parseYouTubePlaylistData(readYouTubeFixture(t, "playlist-continuation.json"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.visitorData != "fixture-visitor-rotated" {
		t.Fatalf("visitorData=%q", parsed.visitorData)
	}
}

func TestYouTubeContinuationViewModelBounds(t *testing.T) {
	tooMany := make([]value.Value, youtubeMaxContinuationCommands+1)
	for index := range tooMany {
		tooMany[index] = value.ObjectValue(value.NewObject())
	}
	viewModel := value.NewObject(value.Field{Key: "continuationCommand", Value: value.ObjectValue(value.NewObject(
		value.Field{Key: "innertubeCommand", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "commandExecutorCommand", Value: value.ObjectValue(value.NewObject(
				value.Field{Key: "commands", Value: value.List(tooMany...)},
			))},
		))},
	))})
	if token := youtubeContinuationViewModelToken(viewModel); token != "" {
		t.Fatalf("oversized executor token = %q", token)
	}
	if token := validYouTubeContinuationToken(strings.Repeat("x", youtubeMaxContinuationBytes+1)); token != "" {
		t.Fatalf("oversized token accepted")
	}
}

func TestYouTubePlaylistLockupRejectsNonVideoAndInvalidID(t *testing.T) {
	for _, object := range []*value.Object{
		value.NewObject(value.Field{Key: "contentId", Value: value.String("modern00001")}, value.Field{Key: "contentType", Value: value.String("LOCKUP_CONTENT_TYPE_PLAYLIST")}),
		value.NewObject(value.Field{Key: "contentId", Value: value.String("too-short")}, value.Field{Key: "contentType", Value: value.String("LOCKUP_CONTENT_TYPE_VIDEO")}),
	} {
		if entry, ok := youtubePlaylistLockupEntry(object); ok {
			t.Fatalf("accepted lockup %#v", entry)
		}
	}
}

func TestYouTubePlaylistFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		name  string
		alert string
		want  error
	}{
		{"private", "This playlist is private. Sign in to continue.", ErrAuthentication},
		{"unavailable", "The playlist does not exist.", ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := []byte(`ytInitialData={"metadata":{"playlistMetadataRenderer":{"title":"Fixture"}},"alerts":[{"alertRenderer":{"text":{"simpleText":` + strconv.Quote(test.alert) + `}}}]};`)
			transport := &youtubePlaylistTransport{page: page}
			_, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport})
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	transport := &youtubePlaylistTransport{page: []byte(`ytInitialData={"contents":{}};`)}
	if _, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error = %v", err)
	}
	transport = &youtubePlaylistTransport{
		page: readYouTubeFixture(t, "playlist.html"), continuation: []byte(`{}`), status: http.StatusForbidden,
	}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: "https://www.youtube.com/playlist?list=PL_fixture", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, 10); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("continuation auth error = %v", err)
	}
}

func TestYouTubePlaylistTraversalDepthIsBounded(t *testing.T) {
	data := strings.Repeat(`{"x":`, youtubeMaxJSONDepth+2) + `{}` + strings.Repeat(`}`, youtubeMaxJSONDepth+2)
	if _, err := parseYouTubePlaylistData([]byte(data)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("depth error = %v", err)
	}
}

func TestYouTubeExtractsLiveHLSAndClassifiesLiveStates(t *testing.T) {
	liveURL := "https://www.youtube.com/watch?v=livefix0001"
	transport := &memoryTransport{pages: map[string][]byte{liveURL: readYouTubeFixture(t, "live-watch.html")}}
	result, err := NewYouTube().Extract(context.Background(), Request{URL: liveURL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if status, _ := result.Info.Lookup("live_status").StringValue(); status != "is_live" {
		t.Fatalf("live_status = %q", status)
	}
	formats, _ := result.Info.Formats()
	format, _ := formats[0].Object()
	if protocol, _ := format.Lookup("protocol").StringValue(); protocol != "m3u8_native" {
		t.Fatalf("live format = %#v", format)
	}
	trueValue, falseValue := true, false
	for _, test := range []struct {
		details youtubeVideoDetails
		want    string
	}{
		{youtubeVideoDetails{IsPostLiveDVR: true}, "post_live"},
		{youtubeVideoDetails{IsUpcoming: true}, "is_upcoming"},
		{youtubeVideoDetails{IsLiveContent: &trueValue}, "was_live"},
		{youtubeVideoDetails{IsLive: &falseValue}, "not_live"},
		{youtubeVideoDetails{}, ""},
	} {
		if got := youtubeLiveStatus(test.details); got != test.want {
			t.Fatalf("youtubeLiveStatus(%#v) = %q, want %q", test.details, got, test.want)
		}
	}
}

func FuzzParseYouTubePlaylistData(f *testing.F) {
	page := readYouTubeFixture(f, "playlist.html")
	if initial, err := extractJSONObject(page, youtubeInitialDataMarker); err == nil {
		f.Add(initial)
	}
	f.Add(readYouTubeFixture(f, "playlist-continuation.json"))
	if modern, err := extractJSONObject(readYouTubeFixture(f, "playlist-modern.html"), youtubeInitialDataMarker); err == nil {
		f.Add(modern)
	}
	f.Add(readYouTubeFixture(f, "playlist-modern-continuation.json"))
	f.Add([]byte(`{"metadata":{"playlistMetadataRenderer":{"title":"x"}}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _ = parseYouTubePlaylistData(data)
	})
}

func FuzzDiscoverYouTubePageConfig(f *testing.F) {
	f.Add([]byte(`ytcfg.set({"PLAYER_JS_URL":"\/s\/player\/fixture\/base.js"})`))
	f.Add([]byte(`ytcfg.data_ = {"WEB_PLAYER_CONTEXT_CONFIGS":{"watch":{"jsUrl":"https://www.youtube.com/s/player/fixture/base.js"}}}`))
	f.Add([]byte(`ytcfg.set({"VISITOR_DATA":"fixture-visitor","LOGGED_IN":false})`))
	f.Add([]byte(`ytcfg.set({"PLAYER_JS_URL":"unterminated}`))
	f.Fuzz(func(t *testing.T, page []byte) {
		if len(page) > 1<<20 {
			t.Skip()
		}
		config := discoverYouTubePageConfig(page)
		_ = config.playerPath("")
		_ = config.visitorData("")
	})
}

func FuzzParseYouTubeTarget(f *testing.F) {
	f.Add("https://www.youtube.com/watch?v=fixture0001&t=1s&end=9")
	f.Add("https://youtu.be/fixture0001#t=1h2m3s")
	// Privacy-enhanced embed seeds.
	f.Add("https://www.youtube-nocookie.com/embed/fixture0001")
	f.Add("https://youtube-nocookie.com/embed/fixture0001")
	f.Add("//www.youtube-nocookie.com/embed/fixture0001")
	f.Add("https://www.youtube-nocookie.com/embed/fixture0001?t=10&end=20")
	f.Add("https://www.youtube-nocookie.com/embed/fixture0001#t=1h2m&end=2h")
	// Hostile and negative seeds.
	f.Add("https://www.youtube-nocookie.com/embed/short")
	f.Add("https://www.youtube-nocookie.com/watch?v=fixture0001")
	f.Add("https://www.youtube-nocookie.com/embed/fixture0001/extra")
	f.Add("https://user:pass@www.youtube-nocookie.com/embed/fixture0001")
	f.Add("https://www.youtube-nocookie.com:443/embed/fixture0001")
	f.Add("https://www.youtube-nocookie.com:/embed/fixture0001")
	f.Add("ftp://www.youtube-nocookie.com/embed/fixture0001")
	f.Add("https://www.youtube-nocookie.com/embed%2Ffixture0001")
	f.Add("https://evil-youtube-nocookie.com/embed/fixture0001")
	f.Add("https://example.com/embed/fixture0001")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		target, err := parseYouTubeTarget(rawURL)
		if err == nil {
			if !youtubeIDPattern.MatchString(target.videoID) {
				t.Fatalf("parseYouTubeTarget(%q) returned invalid ID %q", rawURL, target.videoID)
			}
		} else {
			if !errors.Is(err, ErrUnsupported) {
				t.Fatalf("parseYouTubeTarget(%q) error = %v, want ErrUnsupported", rawURL, err)
			}
		}
	})
}

func FuzzYouTubeChannelLiveAlias(f *testing.F) {
	f.Add("https://www.youtube.com/@fixture/live")
	f.Add("https://youtube.com/channel/UCfixture_channel_00001/live")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 4096 {
			t.Skip()
		}
		_ = youtubeChannelLiveAlias(rawURL)
	})
}

type youtubeTestHelper interface {
	Helper()
	Fatal(...any)
}

func readYouTubeFixture(t youtubeTestHelper, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../conformance/extractors/youtube/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
