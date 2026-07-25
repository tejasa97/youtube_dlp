package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestSabrErrorRecoveryRetriesWithinBudget(t *testing.T) {
	var posts atomic.Int32
	good := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		n := posts.Add(1)
		body, _ := io.ReadAll(request.Body)
		if n == 1 {
			return umpResponse(appendSabrErrorPart(nil, "sabr.no_audio_selected", 2), request), nil
		}
		if len(body) == 0 {
			t.Fatal("expected immutable retry body")
		}
		return umpResponse(good, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	result, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.RetryBaseDelay = 0
			config.MaxSabrErrorRecoveries = 2
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || result.Bytes == 0 {
		t.Fatalf("posts=%d bytes=%d", posts.Load(), result.Bytes)
	}
}

func TestSabrErrorRecoveryBudgetExhausted(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(appendSabrErrorPart(nil, "sabr.no_audio_selected", 2), request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.RetryBaseDelay = 0
			config.MaxSabrErrorRecoveries = 1
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrSabrRecoveryBudget) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestReloadPlayerRecoveryReplacesInventory(t *testing.T) {
	var posts atomic.Int32
	var reloadCalls atomic.Int32
	initial := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=old%2Btoken"
	refreshed := "https://rr2---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=new%2Btoken"
	good := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		n := posts.Add(1)
		if n == 1 {
			if request.URL.String() != initial+"&rn=0" && !strings.HasPrefix(request.URL.String(), strings.Split(initial, "?")[0]) {
				// preserve path; signed query must be exact initial bytes plus rn
			}
			if !strings.Contains(request.URL.RawQuery, "sig=old%2Btoken") {
				t.Fatalf("url=%q", request.URL.String())
			}
			return umpResponse(appendReloadPlayerPart(nil, "secret-reload-token"), request), nil
		}
		if !strings.Contains(request.URL.RawQuery, "sig=new%2Btoken") {
			t.Fatalf("refreshed url=%q", request.URL.String())
		}
		return umpResponse(good, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(initial, func(config *Config) {
		config.RetryBaseDelay = 0
		config.Reload = func(_ context.Context, req ReloadRequest) (RefreshMaterial, error) {
			reloadCalls.Add(1)
			if req.Token != "secret-reload-token" || req.VideoID != "fixture0001" {
				return RefreshMaterial{}, fmt.Errorf("bad reload request")
			}
			return RefreshMaterial{
				ServerURL:       refreshed,
				UstreamerConfig: []byte("fresh-ustreamer"),
				Format:          FormatID{Itag: 137},
				ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
				VideoID:         "fixture0001",
				DurationSec:     10,
			}, nil
		}
	})).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if posts.Load() != 2 || reloadCalls.Load() != 1 {
		t.Fatalf("posts=%d reloads=%d", posts.Load(), reloadCalls.Load())
	}
}

func TestReloadRejectsIncompatibleInventory(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(appendReloadPlayerPart(nil, "token"), request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.RetryBaseDelay = 0
			config.Reload = func(context.Context, ReloadRequest) (RefreshMaterial, error) {
				return RefreshMaterial{
					ServerURL:       "https://rr2---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=x",
					UstreamerConfig: []byte("x"),
					Format:          FormatID{Itag: 140},
					ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
					VideoID:         "fixture0001",
					DurationSec:     10,
				}, nil
			}
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrReloadRejected) && !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestReloadTokenNeverLeaks(t *testing.T) {
	const secret = "super-secret-reload-token"
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(appendReloadPlayerPart(nil, secret), request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.RetryBaseDelay = 0
			config.MaxReloadAttempts = 1
			config.Reload = func(context.Context, ReloadRequest) (RefreshMaterial, error) {
				return RefreshMaterial{}, errors.New("reload failed without token")
			}
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("token leaked: %v", err)
	}
}

func TestSabrErrorDoesNotCommitCookie(t *testing.T) {
	cookie := validTestCookie()
	var posts atomic.Int32
	var secondBody []byte
	good := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
	)
	staged := appendPolicyPart(nil, 10, cookie)
	staged = appendSabrErrorPart(staged, "sabr.no_audio_selected", 2)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		n := posts.Add(1)
		body, _ := io.ReadAll(request.Body)
		if n == 1 {
			return umpResponse(staged, request), nil
		}
		secondBody = bytes.Clone(body)
		return umpResponse(good, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.RetryBaseDelay = 0 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	streamer, _, _ := streamerContextBytes(secondBody)
	got, found, _ := playbackCookieFromStreamer(streamer)
	if found {
		t.Fatalf("cookie committed after failed SABR_ERROR response: %v", got)
	}
}

func TestHTTPRetryReusesImmutableBody(t *testing.T) {
	var bodies [][]byte
	good := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, bytes.Clone(body))
		if len(bodies) == 1 {
			return &http.Response{
				StatusCode: http.StatusBadGateway,
				Body:       http.NoBody,
				Request:    request,
			}, nil
		}
		return umpResponse(good, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.Attempts = 3
			config.RetryBaseDelay = 0
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("bodies not immutable: %d", len(bodies))
	}
}

func TestRefreshMaterialValidation(t *testing.T) {
	config := testConfig("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture")
	config.VisitorData = "visitor"
	ok := RefreshMaterial{
		ServerURL:       config.ServerURL,
		UstreamerConfig: []byte("ustreamer"),
		Format:          FormatID{Itag: 137},
		ClientInfo:      config.ClientInfo,
		VisitorData:     "visitor",
		VideoID:         config.VideoID,
		DurationSec:     config.DurationSec,
	}
	if err := ok.validate(config); err != nil {
		t.Fatal(err)
	}
	badHost := ok
	badHost.ServerURL = "https://evil.example/x"
	if err := badHost.validate(config); !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err=%v", err)
	}
	badID := ok
	badID.VideoID = "other000000"
	if err := badID.validate(config); !errors.Is(err, ErrRefreshRejected) {
		t.Fatalf("err=%v", err)
	}
}

func TestResumeRefreshObtainsFreshMaterial(t *testing.T) {
	var calls atomic.Int32
	var refreshed atomic.Bool
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 6000, payload: []byte("seg1!!")},
	)
	root, destination := testRoot(t, "out.bin")
	cancelCtx, cancelFirst := context.WithCancel(context.Background())
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		switch call {
		case 1:
			return umpResponse(roundOne, request), nil
		default:
			if !strings.Contains(request.URL.RawQuery, "sig=fresh%2Btoken") {
				t.Fatalf("expected fresh signed url, got %q", request.URL.String())
			}
			return umpResponse(roundTwo, request), nil
		}
	})
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindProgress && event.Bytes >= int64(len("INITseg0")) {
			cancelFirst()
		}
		return nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=old",
		func(config *Config) { config.MaxRounds = 4 },
	)).Download(cancelCtx, root, destination, true, sink)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("first err=%v", err)
	}
	_, err = NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=stale%2Btoken",
		func(config *Config) {
			config.MaxRounds = 4
			config.RetryBaseDelay = 0
			config.Refresh = func(context.Context) (RefreshMaterial, error) {
				refreshed.Store(true)
				return RefreshMaterial{
					ServerURL:       "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fresh%2Btoken",
					UstreamerConfig: []byte("fixture-ustreamer"),
					Format:          FormatID{Itag: 137},
					ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
					VideoID:         "fixture0001",
					DurationSec:     10,
				}, nil
			}
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.Load() {
		t.Fatal("expected resume refresh callback")
	}
}
