package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
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
	origWait := policyBackoffWait
	policyBackoffWait = func(waitCtx context.Context, delay time.Duration) error {
		cancel()
		return origWait(waitCtx, delay)
	}
	t.Cleanup(func() { policyBackoffWait = origWait })
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(ctx, root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected cancellation")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestPolicyBackoffZeroAndMaximumAccepted(t *testing.T) {
	cookie := validTestCookie()
	var waits []time.Duration
	origWait := policyBackoffWait
	policyBackoffWait = func(ctx context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		return origWait(ctx, 0)
	}
	t.Cleanup(func() { policyBackoffWait = origWait })

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
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(
		"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
	)).Download(context.Background(), root, destination, true, events.Nop())
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
		{"redirect", PartSABRRedirect},
		{"error", PartSABRError},
		{"reload", PartReloadPlayerResponse},
		{"live", PartLiveMetadata},
		{"context_update", PartSABRContextUpdate},
		{"protection", PartStreamProtectionStatus},
		{"sending_policy", PartSABRContextSendingPolicy},
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
