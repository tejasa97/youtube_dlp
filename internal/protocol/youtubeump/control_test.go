package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestPolicyCookieAppearsInNextRequest(t *testing.T) {
	cookie := validTestCookie()
	var calls atomic.Int32
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), 0, cookie)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if call == 1 {
			return umpResponse(roundOne, request), nil
		}
		streamer, ok, err := streamerContextBytes(body)
		if err != nil || !ok {
			t.Fatalf("streamer=%v ok=%v err=%v", streamer, ok, err)
		}
		got, found, err := playbackCookieFromStreamer(streamer)
		if err != nil || !found {
			t.Fatalf("cookie err=%v found=%v", err, found)
		}
		if !bytes.Equal(got, cookie) {
			t.Fatalf("cookie mismatch")
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
}

func TestPolicyCookieReplacementAndPreservation(t *testing.T) {
	firstCookie := validTestCookie()
	secondCookie := encodePlaybackCookie(appendProtobufVarint(nil, fPlaybackCookieField1, 99))
	var calls atomic.Int32
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), 0, firstCookie)
	roundTwo := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
	), 0, secondCookie)
	roundThree := buildTestUMP(
		137,
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		streamer, _, _ := streamerContextBytes(body)
		got, found, _ := playbackCookieFromStreamer(streamer)
		switch call {
		case 1:
			if found {
				t.Fatal("initial request must omit playback cookie")
			}
			return umpResponse(roundOne, request), nil
		case 2:
			if !bytes.Equal(got, firstCookie) {
				t.Fatal("expected first cookie")
			}
			return umpResponse(roundTwo, request), nil
		case 3:
			if !bytes.Equal(got, secondCookie) {
				t.Fatal("expected replaced cookie")
			}
			return umpResponse(roundThree, request), nil
		default:
			t.Fatalf("unexpected call %d", call)
			return nil, nil
		}
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
}

func TestPolicyWithoutCookiePreservesPriorCookie(t *testing.T) {
	cookie := validTestCookie()
	var calls atomic.Int32
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), 0, cookie)
	roundTwo := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
	), 0, nil)
	roundThree := buildTestUMP(
		137,
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		body, _ := io.ReadAll(request.Body)
		streamer, _, _ := streamerContextBytes(body)
		got, found, _ := playbackCookieFromStreamer(streamer)
		if call >= 2 {
			if !found || !bytes.Equal(got, cookie) {
				t.Fatalf("call=%d cookie not preserved", call)
			}
		}
		switch call {
		case 1:
			return umpResponse(roundOne, request), nil
		case 2:
			return umpResponse(roundTwo, request), nil
		case 3:
			return umpResponse(roundThree, request), nil
		default:
			t.Fatalf("unexpected call %d", call)
			return nil, nil
		}
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetryReusesRequestBodyAndDoesNotCommitFailedPolicy(t *testing.T) {
	cookie := validTestCookie()
	var attempts atomic.Int32
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), 0, cookie)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	var firstBody []byte
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		body, err := io.ReadAll(request.Body)
		if err != nil {
			return nil, err
		}
		if attempt == 1 {
			firstBody = bytes.Clone(body)
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       http.NoBody,
				Request:    request,
			}, nil
		}
		if attempt == 2 {
			if !bytes.Equal(body, firstBody) {
				t.Fatal("retry must reuse prior request body")
			}
			return umpResponse(roundOne, request), nil
		}
		streamer, _, _ := streamerContextBytes(body)
		got, found, _ := playbackCookieFromStreamer(streamer)
		if !found || !bytes.Equal(got, cookie) {
			t.Fatalf("expected committed cookie on round two")
		}
		return umpResponse(roundTwo, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
		func(config *Config) { config.Attempts = 3 },
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("attempts=%d", attempts.Load())
	}
}

func TestFailedPolicyDoesNotCommitCookie(t *testing.T) {
	secret := appendProtobufVarint(validTestCookie(), fPlaybackCookieField1, 0xDEAD)
	bad := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("x")},
	), 0, secret)
	bad = appendPolicyPart(bad, 0, secret)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(bad, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected failure")
	}
	if strings.Contains(err.Error(), string(secret)) {
		t.Fatal("cookie bytes leaked in error")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestCancellationDuringPolicyBackoffCleansUp(t *testing.T) {
	cookie := validTestCookie()
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), MaxPolicyBackoffMs, cookie)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(roundOne, request), nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	downloader := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	))
	downloader.policyBackoffWait = func(waitCtx context.Context, delay time.Duration) error {
		cancel()
		return sleep(waitCtx, delay)
	}
	root, destination := testRoot(t, "out.bin")
	_, err := downloader.Download(ctx, root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected cancellation")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestPolicyBackoffZeroAndMaximumAccepted(t *testing.T) {
	cookie := validTestCookie()
	var waits []time.Duration
	roundOne := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte("seg0")},
	), 0, cookie)
	roundTwo := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("seg1")},
	), MaxPolicyBackoffMs, cookie)
	roundThree := buildTestUMP(
		137,
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("seg2")},
	)
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return umpResponse(roundOne, request), nil
		case 2:
			return umpResponse(roundTwo, request), nil
		case 3:
			return umpResponse(roundThree, request), nil
		default:
			t.Fatalf("unexpected call")
			return nil, nil
		}
	})
	downloader := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	))
	downloader.policyBackoffWait = func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return sleep(ctx, 0)
	}
	root, destination := testRoot(t, "out.bin")
	_, err := downloader.Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if len(waits) != 1 || waits[0] != MaxPolicyBackoffMs*time.Millisecond {
		t.Fatalf("waits=%v", waits)
	}
}

func TestPolicyValidationFailures(t *testing.T) {
	cookie := validTestCookie()
	parseTests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"empty_cookie", appendProtobufBytes(nil, fPolicyPlaybackCookie, nil), ErrInvalidMediaState},
		{"oversized_cookie", encodeNextRequestPolicy(0, bytes.Repeat([]byte{0x0A, 0x01, 0x01}, 2048)), ErrInvalidMediaState},
		{"malformed_cookie", encodeNextRequestPolicy(0, []byte{0xFF}), ErrTruncatedStream},
		{"duplicate_backoff", append(encodeNextRequestPolicy(1, cookie), appendProtobufVarint(nil, fPolicyBackoffTimeMs, 2)...), ErrInvalidProtobuf},
		{"duplicate_cookie", append(encodeNextRequestPolicy(0, cookie), appendProtobufBytes(nil, fPolicyPlaybackCookie, cookie)...), ErrInvalidProtobuf},
		{"wrong_wire_backoff", appendProtobufBytes(nil, fPolicyBackoffTimeMs, []byte("x")), ErrInvalidProtobuf},
		{"overflow_backoff", appendProtobufVarint(nil, fPolicyBackoffTimeMs, uint64(1<<31)), ErrVarintOverflow},
		{"excessive_backoff", encodeNextRequestPolicy(MaxPolicyBackoffMs+1, cookie), ErrExcessivePolicyBackoff},
	}
	for _, test := range parseTests {
		t.Run(test.name, func(t *testing.T) {
			_, _, _, err := parseNextRequestPolicy(test.payload)
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
	t.Run("duplicate_policy_part", func(t *testing.T) {
		body := appendPolicyPart(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("x")},
		), 0, cookie)
		body = appendPolicyPart(body, 0, cookie)
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
	})
}

func TestEndOfTrackFailures(t *testing.T) {
	base := func() []byte {
		return buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
		)
	}
	tests := []struct {
		name string
		body []byte
	}{
		{"premature", encodePart(PartEndOfTrack, nil)},
		{"non_empty", appendEndOfTrackPart(append(base(), encodePart(PartEndOfTrack, []byte{1})...))},
		{"duplicate", appendEndOfTrackPart(appendEndOfTrackPart(base()))},
		{"data_after", append(appendEndOfTrackPart(base()), encodePart(PartNextRequestPolicy, encodeNextRequestPolicy(0, validTestCookie()))...)},
		{"active_header", bytes.Join([][]byte{
			encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(137)),
			encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 1, Itag: 137, IsInitSeg: true, ContentLength: 4})),
			encodePart(PartMedia, append(mustTestVarint(1), []byte("INIT")...)),
			encodePart(PartMediaEnd, mustTestVarint(1)),
			encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 2, Itag: 137, SequenceNumber: 0, DurationMs: 1000, ContentLength: 3})),
			encodePart(PartEndOfTrack, nil),
		}, nil)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return umpResponse(test.body, request), nil
			})
			root, destination := testRoot(t, "out.bin")
			_, err := NewDownloader(transport, testConfig(
				"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
			)).Download(context.Background(), root, destination, true, events.Nop())
			if err == nil {
				t.Fatal("expected failure")
			}
			assertNoPublishedArtifact(t, root, destination)
		})
	}
}

func TestUnsupportedDirectivesRemainUnsupported(t *testing.T) {
	base := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("only")},
	)
	for _, partType := range []struct {
		name string
		id   int
	}{
		{"error", PartSABRError},
		{"reload", PartReloadPlayerResponse},
		{"live", PartLiveMetadata},
		{"protection", PartStreamProtectionStatus},
	} {
		t.Run(partType.name, func(t *testing.T) {
			body := append(bytes.Clone(base), encodePart(partType.id, []byte{0x0A, 0x01, 'x'})...)
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				return umpResponse(body, request), nil
			})
			root, destination := testRoot(t, "out.bin")
			_, err := NewDownloader(transport, testConfig(
				"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
			)).Download(context.Background(), root, destination, true, events.Nop())
			if !errors.Is(err, ErrUnsupportedDirective) {
				t.Fatalf("part=%d err=%v", partType.id, err)
			}
			assertNoPublishedArtifact(t, root, destination)
		})
	}
}

func TestCookieBytesAbsentFromErrorsAndEvents(t *testing.T) {
	cookie := validTestCookie()
	body := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("x")},
	), 0, cookie)
	body = append(body, encodePart(PartNextRequestPolicy, encodeNextRequestPolicy(0, cookie))...)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return umpResponse(body, request), nil
	})
	var messages []string
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		messages = append(messages, event.Message)
		return nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, sink)
	if err == nil {
		t.Fatal("expected failure")
	}
	needle := string(cookie)
	for _, message := range append(messages, err.Error()) {
		if strings.Contains(message, needle) {
			t.Fatalf("cookie leaked in message: %q", message)
		}
	}
}

func consumeUMPBody(t *testing.T, body []byte, assembler *trackAssembler) (roundControl, error) {
	t.Helper()
	consumer := newStreamConsumer(assembler)
	reader := NewReader(newByteReader(body), int64(len(body)))
	for {
		part, ok, err := reader.ReadPart()
		if err != nil {
			return roundControl{}, err
		}
		if !ok {
			return consumer.finish()
		}
		if err := consumer.consumePart(part); err != nil {
			return roundControl{}, err
		}
	}
}

func TestConsumeStreamMixedControlAndMedia(t *testing.T) {
	cookie := validTestCookie()
	body := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
	), 5, cookie)
	file, err := os.CreateTemp(t.TempDir(), "ctrl-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	ctrl, err := consumeUMPBody(t, body, assembler)
	if err != nil {
		t.Fatal(err)
	}
	if ctrl.backoff != 5*time.Millisecond || !ctrl.updateCookie || !bytes.Equal(ctrl.cookie, cookie) {
		t.Fatalf("ctrl=%+v", ctrl)
	}
}

func TestDownloaderPolicyBackoffWaitIsolated(t *testing.T) {
	cookie := validTestCookie()
	makeRound := func(seg string) []byte {
		return appendPolicyPart(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 4000, payload: []byte(seg)},
		), 1000, cookie)
	}
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 3, sequence: 1, duration: 3000, payload: []byte("tail")},
		testSegment{headerID: 4, sequence: 2, duration: 3000, payload: []byte("done")},
	)
	var (
		waitsA []time.Duration
		waitsB []time.Duration
		mu     sync.Mutex
		callsA atomic.Int32
		callsB atomic.Int32
	)
	transportA := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if callsA.Add(1) == 1 {
			return umpResponse(makeRound("a0"), request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	transportB := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if callsB.Add(1) == 1 {
			return umpResponse(makeRound("b0"), request), nil
		}
		return umpResponse(roundTwo, request), nil
	})
	downloaderA := NewDownloader(transportA, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=a",
	))
	downloaderA.policyBackoffWait = func(_ context.Context, delay time.Duration) error {
		mu.Lock()
		waitsA = append(waitsA, delay)
		mu.Unlock()
		return nil
	}
	downloaderB := NewDownloader(transportB, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=b",
	))
	downloaderB.policyBackoffWait = func(_ context.Context, delay time.Duration) error {
		mu.Lock()
		waitsB = append(waitsB, delay*2)
		mu.Unlock()
		return nil
	}
	rootA, destA := testRoot(t, "a.bin")
	rootB, destB := testRoot(t, "b.bin")
	errCh := make(chan error, 2)
	go func() {
		_, err := downloaderA.Download(context.Background(), rootA, destA, true, events.Nop())
		errCh <- err
	}()
	go func() {
		_, err := downloaderB.Download(context.Background(), rootB, destB, true, events.Nop())
		errCh <- err
	}()
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if len(waitsA) != 1 || waitsA[0] != time.Second {
		t.Fatalf("waitsA=%v", waitsA)
	}
	if len(waitsB) != 1 || waitsB[0] != 2*time.Second {
		t.Fatalf("waitsB=%v", waitsB)
	}
}

func TestPlaybackCookieEmbeddedFormatIDValidation(t *testing.T) {
	valid := appendProtobufBytes(nil, fPlaybackCookieFormatID, FormatID{Itag: 137}.marshal())
	if err := validatePlaybackCookie(valid); err != nil {
		t.Fatalf("valid cookie err=%v", err)
	}
	wrongWire := appendProtobufVarint(nil, fPlaybackCookieFormatID, 137)
	if err := validatePlaybackCookie(wrongWire); !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("wrong wire err=%v", err)
	}
	duplicate := append(
		appendProtobufBytes(nil, fPlaybackCookieFormatID, FormatID{Itag: 137}.marshal()),
		appendProtobufBytes(nil, fPlaybackCookieFormatID, FormatID{Itag: 140}.marshal())...,
	)
	if err := validatePlaybackCookie(duplicate); !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("duplicate err=%v", err)
	}
	nestedWrongWire := appendProtobufBytes(nil, fPlaybackCookieFormatID, appendProtobufBytes(nil, fFormatItag, []byte("x")))
	if err := validatePlaybackCookie(nestedWrongWire); !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("nested wrong wire err=%v", err)
	}
	nestedDuplicate := appendProtobufBytes(nil, fPlaybackCookieFormatID, append(
		appendProtobufVarint(nil, fFormatItag, 137),
		appendProtobufVarint(nil, fFormatItag, 140)...,
	))
	if err := validatePlaybackCookie(nestedDuplicate); !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("nested duplicate err=%v", err)
	}
}

func TestConsumeStreamDoesNotCommitControlOnLateFailure(t *testing.T) {
	cookie := validTestCookie()
	body := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
	), 5000, cookie)
	body = append(body, encodePart(PartSABRError, []byte{0x0A, 0x01, 'x'})...)
	file, err := os.CreateTemp(t.TempDir(), "txn-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	ctrl, err := consumeStream(context.Background(), bytes.NewReader(body), assembler)
	if !errors.Is(err, ErrUnsupportedDirective) {
		t.Fatalf("err=%v", err)
	}
	if !roundControlIsZero(ctrl) {
		t.Fatalf("committed control on failure: %+v", ctrl)
	}
	if !assembler.initWritten {
		t.Fatal("expected media assembler to retain progress before failure")
	}
	if assembler.endOfTrackDone {
		t.Fatal("end marker must not be committed on failure")
	}
}

func TestConsumeStreamDoesNotCommitControlOnTruncatedStream(t *testing.T) {
	cookie := validTestCookie()
	body := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
	), 2500, cookie)
	body = append(body,
		encodePart(PartMediaHeader, marshalMediaHeader(MediaHeader{HeaderID: 2, Itag: 137, SequenceNumber: 0, DurationMs: 1000, ContentLength: 3}))...)
	file, err := os.CreateTemp(t.TempDir(), "txn-trunc-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	ctrl, err := consumeStream(context.Background(), bytes.NewReader(body), assembler)
	if !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("err=%v", err)
	}
	if !roundControlIsZero(ctrl) {
		t.Fatalf("committed control on truncated stream: %+v", ctrl)
	}
}

func roundControlIsZero(ctrl roundControl) bool {
	return ctrl.backoff == 0 && !ctrl.updateCookie && len(ctrl.cookie) == 0 &&
		!ctrl.hasRedirect && ctrl.redirectURL == "" &&
		(ctrl.contexts == nil || (len(ctrl.contexts.entries) == 0 && len(ctrl.contexts.active) == 0))
}
