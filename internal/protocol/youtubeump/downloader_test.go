package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	_ = ctx
	return fn(request)
}

func testConfig(serverURL string, extra ...func(*Config)) Config {
	config := Config{
		ServerURL:       serverURL,
		UstreamerConfig: []byte("fixture-ustreamer"),
		Format:          FormatID{Itag: 137},
		TrackKind:       TrackVideo,
		ClientInfo:      ClientInfo{ClientName: 1, ClientVersion: "fixture"},
		UserAgent:       "fixture-agent",
		DurationSec:     10,
	}
	for _, apply := range extra {
		apply(&config)
	}
	return config
}

func TestSABRRequestPreservesSignedQueryBytes(t *testing.T) {
	var seen string
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INITSEG")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fixture-bytes")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seen = request.URL.String()
		return umpResponse(body, request), nil
	})
	serverURL := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?expire=9999999999&sig=fixture%2Btoken"
	root, destination := testRoot(t, "out.bin")
	result, err := NewDownloader(transport, testConfig(serverURL)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen, "sig=fixture%2Btoken") || !strings.HasSuffix(seen, "rn=0") {
		t.Fatalf("url=%q", seen)
	}
	want := "INITSEGfixture-bytes"
	if result.Bytes != int64(len(want)) {
		t.Fatalf("bytes=%d", result.Bytes)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("payload=%q", got)
	}
}

func testRoot(t *testing.T, name string) (string, string) {
	t.Helper()
	root := t.TempDir()
	return root, filepath.Join(root, name)
}

func TestSABRRejectsExternalRedirect(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusFound,
			Header:     http.Header{"Location": {"https://evil.example/steal"}},
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil || !errors.Is(err, ErrRedirect) {
		t.Fatalf("err=%v", err)
	}
}

func TestSABRRequestHasNoCookieLeakage(t *testing.T) {
	TestSABRCredentialHeadersStripped(t)
}

func TestSABRCredentialHeadersStripped(t *testing.T) {
	var seen http.Header
	body := buildTestUMP(
		140,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("audio")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seen = request.Header.Clone()
		return umpResponse(body, request), nil
	})
	headers := make(http.Header)
	headers.Set("Cookie", "secret=must-not-send")
	headers.Set("Authorization", "secret-auth")
	headers.Set("Proxy-Authorization", "secret-proxy")
	root, destination := testRoot(t, "audio.m4a")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) {
			config.Headers = headers
			config.Format = FormatID{Itag: 140}
			config.TrackKind = TrackAudio
		},
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
		if seen.Get(key) != "" {
			t.Fatalf("%s leaked: %q", key, seen.Get(key))
		}
	}
}

func TestSABRCancellationBeforeRead(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(ctx, root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected cancellation")
	}
}

func TestDownloaderCleansUpOnFailure(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
			Body:       io.NopCloser(bytes.NewReader(encodePart(PartSABRRedirect, []byte{0x0A, 0x01, 'x'}))),
			Request:    request,
		}, nil
	})
	root, destination := testRoot(t, "final.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected failure")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestValidateSABRURLRejectsUntrustedHost(t *testing.T) {
	if _, err := ValidateSABRURL("https://sabr.example/stream"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestMultiSegmentSingleResponseExactBytes(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 3000, payload: []byte("seg0")},
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
		testSegment{headerID: 4, sequence: 2, duration: 4000, payload: []byte("seg2")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "INITseg0seg1seg2" {
		t.Fatalf("got=%q", got)
	}
}

func TestMultiRoundProgressionAndBufferedRanges(t *testing.T) {
	var calls atomic.Int32
	roundOne := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 2, sequence: 1, duration: 3000, payload: []byte("seg1")},
		testSegment{headerID: 3, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if call == 1 {
			if !strings.HasSuffix(request.URL.String(), "rn=0") {
				t.Fatalf("first url=%s", request.URL)
			}
			return umpResponse(roundOne, request), nil
		}
		if !strings.HasSuffix(request.URL.String(), "rn=1") {
			t.Fatalf("second url=%s", request.URL)
		}
		playerTime, bufferedCount, selected, err := decodePlaybackRequestBody(body)
		if err != nil {
			t.Fatal(err)
		}
		if playerTime != 4000 || bufferedCount != 1 || !selected {
			t.Fatalf("playerTime=%d buffered=%d selected=%v", playerTime, bufferedCount, selected)
		}
		return umpResponse(roundTwo, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITseg0seg1seg2" {
		t.Fatalf("got=%q", got)
	}
}

func TestReplayDedupAcceptsIdenticalSegment(t *testing.T) {
	var calls atomic.Int32
	segment := testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("same")}
	segmentTwo := testSegment{headerID: 3, sequence: 1, duration: 5000, payload: []byte("tail")}
	roundOne := buildTestUMP(137, testSegment{headerID: 1, init: true, payload: []byte("INIT")}, segment)
	roundTwo := buildTestUMP(137, segment, segmentTwo)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			return umpResponse(roundOne, request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITsametail" {
		t.Fatalf("got=%q", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestReplayHeaderAcceptedBeforeSequenceRejection(t *testing.T) {
	file, err := os.CreateTemp(t.TempDir(), "replay-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	initHeader := marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})
	segHeader := marshalMediaHeader(MediaHeader{HeaderID: 2, Itag: 137, SequenceNumber: 0, DurationMs: 5000, ContentLength: 4})
	segTwoHeader := marshalMediaHeader(MediaHeader{HeaderID: 3, Itag: 137, SequenceNumber: 1, DurationMs: 5000, ContentLength: 4})
	for _, part := range []struct {
		partType int
		payload  []byte
	}{
		{PartFormatInitializationMetadata, encodeFormatInitialization(137)},
		{PartMediaHeader, initHeader},
		{PartMedia, append(mustTestVarint(1), []byte("INIT")...)},
		{PartMediaEnd, mustTestVarint(1)},
		{PartMediaHeader, segHeader},
		{PartMedia, append(mustTestVarint(2), []byte("seg0")...)},
		{PartMediaEnd, mustTestVarint(2)},
		{PartMediaHeader, segTwoHeader},
		{PartMedia, append(mustTestVarint(3), []byte("seg1")...)},
		{PartMediaEnd, mustTestVarint(3)},
		{PartMediaHeader, segHeader},
		{PartMedia, append(mustTestVarint(2), []byte("seg0")...)},
		{PartMediaEnd, mustTestVarint(2)},
	} {
		if err := assembler.consumePart(Part{Type: part.partType, Payload: part.payload}); err != nil {
			t.Fatalf("part %d: %v", part.partType, err)
		}
	}
	if assembler.nextSequence != 2 {
		t.Fatalf("nextSequence=%d", assembler.nextSequence)
	}
}

func TestOutOfOrderSequenceRejected(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 1, duration: 10000, payload: []byte("late")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected out-of-order rejection")
	}
}

func TestPrematureMediaEndDoesNotComplete(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("short")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.MaxRounds = 1 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrRoundsExhausted) {
		t.Fatalf("err=%v", err)
	}
}

func TestEndOfTrackRejected(t *testing.T) {
	body := encodePart(PartEndOfTrack, nil)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestEndOfTrackCompletesBelowDuration(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("short")},
	)
	body = appendEndOfTrackPart(body)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITshort" {
		t.Fatalf("got=%q", got)
	}
}

func TestMaxRoundsExhaustedWithoutPublish(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("short")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.MaxRounds = 1 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrRoundsExhausted) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestDestinationSymlinkRejected(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.bin")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.bin")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, link, true, events.Nop())
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("err=%v", err)
	}
}

func TestDestinationExistsWithoutOverwrite(t *testing.T) {
	root, destination := testRoot(t, "out.bin")
	if err := os.WriteFile(destination, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		t.Fatal("unexpected request")
		return nil, nil
	})
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, false, events.Nop())
	if !errors.Is(err, ErrDestinationExists) {
		t.Fatalf("err=%v", err)
	}
}

func TestErrorRedactsSignedURL(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=topsecret",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "topsecret") {
		t.Fatalf("leaked secret: %v", err)
	}
}

func umpResponse(body []byte, request *http.Request) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}
}

func assertNoPublishedArtifact(t *testing.T, root, destination string) {
	t.Helper()
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination published: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(root, ".*.tmp-*"))
	if len(matches) != 0 {
		t.Fatalf("temp files remain: %v", matches)
	}
}

func TestInterleavedMultiplexSelectsVideoTrack(t *testing.T) {
	body := buildMultiplexUMP(137,
		multiplexSegment{itag: 140, testSegment: testSegment{headerID: 10, init: true, payload: []byte("AUDI")}},
		multiplexSegment{itag: 137, testSegment: testSegment{headerID: 1, init: true, payload: []byte("INIT")}},
		multiplexSegment{itag: 140, testSegment: testSegment{headerID: 11, sequence: 0, duration: 5000, payload: []byte("aaaaa")}},
		multiplexSegment{itag: 137, testSegment: testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("vvvvv")}},
		multiplexSegment{itag: 140, testSegment: testSegment{headerID: 12, sequence: 1, duration: 5000, payload: []byte("bbbbb")}},
		multiplexSegment{itag: 137, testSegment: testSegment{headerID: 3, sequence: 1, duration: 5000, payload: []byte("wwwww")}},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "INITvvvvvwwwww" {
		t.Fatalf("got=%q", got)
	}
}

func TestInterleavedMultiplexSelectsAudioTrack(t *testing.T) {
	body := buildMultiplexUMP(140,
		multiplexSegment{itag: 137, testSegment: testSegment{headerID: 1, init: true, payload: []byte("INIT")}},
		multiplexSegment{itag: 140, testSegment: testSegment{headerID: 10, init: true, payload: []byte("AUDI")}},
		multiplexSegment{itag: 137, testSegment: testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("video-bytes")}},
		multiplexSegment{itag: 140, testSegment: testSegment{headerID: 11, sequence: 0, duration: 10000, payload: []byte("audio-bytes")}},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.m4a")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.Format = FormatID{Itag: 140}; config.TrackKind = TrackAudio },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(destination)
	if string(got) != "AUDIaudio-bytes" {
		t.Fatalf("got=%q", got)
	}
}

func TestIgnoredTrackOversizedPayloadRejected(t *testing.T) {
	header := marshalMediaHeader(MediaHeader{
		HeaderID: 10, Itag: 140, SequenceNumber: 0, DurationMs: 1000, ContentLength: 3,
	})
	body := bytes.Join([][]byte{
		encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(137)),
		encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(140)),
		encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})),
		encodePart(PartMedia, append(mustTestVarint(1), []byte("INIT")...)),
		encodePart(PartMediaEnd, mustTestVarint(1)),
		encodePart(PartMediaHeader, header),
		encodePart(PartMedia, append(mustTestVarint(10), []byte("toobig")...)),
		encodePart(PartMediaEnd, mustTestVarint(10)),
	}, nil)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestReplayRejectsDifferentBytesSameSequence(t *testing.T) {
	segment := testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("same")}
	roundOne := buildTestUMP(137, testSegment{headerID: 1, init: true, payload: []byte("INIT")}, segment)
	roundTwo := buildTestUMP(137, testSegment{headerID: 2, sequence: 0, duration: 5000, payload: []byte("diff")})
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Query().Get("rn") == "0" {
			return umpResponse(roundOne, request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.MaxRounds = 4 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrInvalidMediaState) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestDurationCompletionRequiresExactDeclaredDuration(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 9999, payload: []byte("short")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.MaxRounds = 1 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrRoundsExhausted) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestDurationCompletionAcceptsExactBoundary(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("exact")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedResponseWithoutMediaEndFails(t *testing.T) {
	body := bytes.Join([][]byte{
		encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(137)),
		encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})),
		encodePart(PartMedia, append(mustTestVarint(1), []byte("INIT")...)),
	}, nil)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestCompletedEventFailureAfterPublishStillSucceeds(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("fixture")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, completedFailingEventSink{})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != "INITfixture" {
		t.Fatalf("payload=%q err=%v", got, err)
	}
}

type completedFailingEventSink struct{}

func (completedFailingEventSink) Emit(_ context.Context, event events.Event) error {
	if event.Kind == events.KindCompleted {
		return errors.New("sink failed")
	}
	return nil
}
