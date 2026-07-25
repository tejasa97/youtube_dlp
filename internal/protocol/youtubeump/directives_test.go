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
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/events"
)

func TestParseSabrRedirectValidation(t *testing.T) {
	valid := fixtureRedirectURL("2")
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"ok", encodeSabrRedirect(valid), nil},
		{"missing", nil, ErrUnsafeRedirect},
		{"empty", appendProtobufBytes(nil, fSabrRedirectURL, nil), ErrUnsafeRedirect},
		{"duplicate", append(encodeSabrRedirect(valid), encodeSabrRedirect(valid)...), ErrInvalidProtobuf},
		{"wrong_wire", appendProtobufVarint(nil, fSabrRedirectURL, 1), ErrInvalidProtobuf},
		{"oversize", appendProtobufBytes(nil, fSabrRedirectURL, bytes.Repeat([]byte{'a'}, MaxRedirectURLBytes+1)), ErrUnsafeRedirect},
		{"evil_host", encodeSabrRedirect("https://evil.com/videoplayback"), ErrUnsafeRedirect},
		{"lookalike", encodeSabrRedirect("https://googlevideo.com.evil.example/x"), ErrUnsafeRedirect},
		{"userinfo", encodeSabrRedirect("https://user:pass@rr1---sn-fixture.googlevideo.com/x"), ErrUnsafeRedirect},
		{"port", encodeSabrRedirect("https://rr1---sn-fixture.googlevideo.com:443/x"), ErrUnsafeRedirect},
		{"fragment", encodeSabrRedirect(valid + "#frag"), ErrUnsafeRedirect},
		{"encoded_slash", encodeSabrRedirect("https://rr1---sn-fixture.googlevideo.com/a%2fb"), ErrUnsafeRedirect},
		{"encoded_nul", encodeSabrRedirect("https://rr1---sn-fixture.googlevideo.com/a%00b"), ErrUnsafeRedirect},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseSabrRedirect(test.payload)
			if test.want == nil {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			if strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), string(test.payload)) && len(test.payload) > 64 {
				t.Fatalf("unsafe diagnostics: %v", err)
			}
		})
	}
}

func TestParseSabrContextUpdateValidation(t *testing.T) {
	ok := encodeSabrContextUpdate(1, SabrContextScopePlayback, SabrContextWriteOverwrite, []byte("val"), true)
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"ok", ok, nil},
		{"missing_type", appendProtobufBytes(nil, fSabrContextValue, []byte("v")), ErrInvalidContextState},
		{"nonpositive_type", encodeSabrContextUpdate(0, 0, 0, []byte("v"), false), ErrInvalidContextState},
		{"negative_type", append(
			[]byte{0x08, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01},
			appendProtobufBytes(nil, fSabrContextValue, []byte("v"))...,
		), ErrInvalidContextState},
		{"missing_value", appendProtobufVarint(nil, fSabrContextType, 1), ErrInvalidContextState},
		{"empty_value", encodeSabrContextUpdate(1, 0, 0, nil, false), ErrInvalidContextState},
		{"oversize_value", encodeSabrContextUpdate(1, 0, 0, bytes.Repeat([]byte{'x'}, MaxSabrContextValueBytes+1), false), ErrInvalidContextState},
		{"bad_scope", encodeSabrContextUpdate(1, 5, 0, []byte("v"), false), ErrInvalidContextState},
		{"bad_policy", encodeSabrContextUpdate(1, 0, 3, []byte("v"), false), ErrInvalidContextState},
		{"dup_type", append(ok, appendProtobufVarint(nil, fSabrContextType, 2)...), ErrInvalidProtobuf},
		{"wrong_wire_type", appendProtobufBytes(nil, fSabrContextType, []byte("x")), ErrInvalidProtobuf},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSabrContextUpdate(test.payload)
			if test.want == nil {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
				if got.Type != 1 || !bytes.Equal(got.Value, []byte("val")) || !got.SendByDefault {
					t.Fatalf("got=%+v", got)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			if strings.Contains(err.Error(), "secret-value") {
				t.Fatalf("value leaked: %v", err)
			}
		})
	}
}

func TestParseSabrContextSendingPolicyPackedAndUnpacked(t *testing.T) {
	unpacked := encodeSabrSendingPolicy([]int32{1, 2}, []int32{3}, []int32{4}, false)
	packed := encodeSabrSendingPolicy([]int32{1, 2}, []int32{3}, []int32{4}, true)
	for _, name := range []string{"unpacked", "packed"} {
		payload := unpacked
		if name == "packed" {
			payload = packed
		}
		t.Run(name, func(t *testing.T) {
			got, err := parseSabrContextSendingPolicy(payload)
			if err != nil {
				t.Fatal(err)
			}
			if !int32SliceEqual(got.Start, []int32{1, 2}) || !int32SliceEqual(got.Stop, []int32{3}) || !int32SliceEqual(got.Discard, []int32{4}) {
				t.Fatalf("got=%+v", got)
			}
		})
	}
	oversized := make([]int32, MaxSabrContextPolicyOps+1)
	for i := range oversized {
		oversized[i] = int32(i + 1)
	}
	_, err := parseSabrContextSendingPolicy(encodeSabrSendingPolicy(oversized, nil, nil, true))
	if !errors.Is(err, ErrInvalidContextState) {
		t.Fatalf("err=%v", err)
	}
	_, err = parseSabrContextSendingPolicy(encodeSabrSendingPolicy([]int32{0}, nil, nil, false))
	if !errors.Is(err, ErrInvalidContextState) {
		t.Fatalf("err=%v", err)
	}
}

func TestContextStateKeepExistingOverwriteAndPolicyOrder(t *testing.T) {
	state := newSabrContextState()
	if err := state.applyUpdate(sabrContextUpdateDirective{
		Type: 2, Scope: 1, Value: []byte("first"), WritePolicy: SabrContextWriteOverwrite, SendByDefault: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := state.applyUpdate(sabrContextUpdateDirective{
		Type: 2, Value: []byte("ignored"), WritePolicy: SabrContextWriteKeepExisting, SendByDefault: false,
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(state.entries[2].Value, []byte("first")) {
		t.Fatalf("keep-existing mutated value")
	}
	if _, ok := state.active[2]; !ok {
		t.Fatal("keep-existing must not clear prior active mark")
	}
	if err := state.applyUpdate(sabrContextUpdateDirective{
		Type: 2, Value: []byte("second"), WritePolicy: SabrContextWriteOverwrite,
	}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(state.entries[2].Value, []byte("second")) {
		t.Fatal("overwrite failed")
	}
	if err := state.applyUpdate(sabrContextUpdateDirective{
		Type: 3, Value: []byte("three"), WritePolicy: SabrContextWriteUnspecified,
	}); err != nil {
		t.Fatal(err)
	}
	// start then stop => inactive; discard then (already ordered after stop) removes storage.
	if err := state.applySendingPolicy(sabrContextSendingPolicyDirective{
		Start:   []int32{3, 9},
		Stop:    []int32{3},
		Discard: []int32{2},
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.entries[2]; ok {
		t.Fatal("discard must remove stored value")
	}
	if _, ok := state.active[3]; ok {
		t.Fatal("start then stop must leave type inactive")
	}
	if _, ok := state.active[9]; !ok {
		t.Fatal("start of unknown type still marks active")
	}
}

func TestContextStateMarshalSortedIndependentOfInsertion(t *testing.T) {
	a := newSabrContextState()
	b := newSabrContextState()
	updates := []sabrContextUpdateDirective{
		{Type: 5, Value: []byte("e"), SendByDefault: true},
		{Type: 1, Value: []byte("a")},
		{Type: 3, Value: []byte("c"), SendByDefault: true},
		{Type: 2, Value: []byte("b")},
		{Type: 4, Value: []byte("d")},
	}
	for _, update := range updates {
		if err := a.applyUpdate(update); err != nil {
			t.Fatal(err)
		}
	}
	for i := len(updates) - 1; i >= 0; i-- {
		if err := b.applyUpdate(updates[i]); err != nil {
			t.Fatal(err)
		}
	}
	left := a.appendToStreamer(nil)
	right := b.appendToStreamer(nil)
	if !bytes.Equal(left, right) {
		t.Fatalf("marshal order diverged\nleft=%x\nright=%x", left, right)
	}
	active, unsent, err := decodeStreamerContexts(left)
	if err != nil {
		t.Fatal(err)
	}
	if !int32SliceEqual(active, []int32{3, 5}) || !int32SliceEqual(unsent, []int32{1, 2, 4}) {
		t.Fatalf("active=%v unsent=%v", active, unsent)
	}
}

func TestRedirectAndContextDownloadLoop(t *testing.T) {
	initial := fixtureRedirectURL("1")
	next := fixtureRedirectURL("2")
	var calls atomic.Int32
	var seenURLs []string
	var headers []http.Header
	roundOne := appendRedirectPart(appendContextUpdatePart(nil, 7, SabrContextScopePlayback, SabrContextWriteOverwrite, []byte("ctx7"), true), next)
	roundOne = appendSendingPolicyPart(roundOne, nil, nil, nil, true)
	roundTwo := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("data")},
	)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		seenURLs = append(seenURLs, request.URL.String())
		headers = append(headers, request.Header.Clone())
		body, _ := io.ReadAll(request.Body)
		streamer, ok, err := streamerContextBytes(body)
		if err != nil || !ok {
			t.Fatalf("streamer err=%v ok=%v", err, ok)
		}
		switch call {
		case 1:
			if strings.Contains(request.URL.Host, "rr2---") {
				t.Fatal("first request must use initial host")
			}
			active, unsent, err := decodeStreamerContexts(streamer)
			if err != nil {
				t.Fatal(err)
			}
			if len(active) != 0 || len(unsent) != 0 {
				t.Fatalf("initial contexts active=%v unsent=%v", active, unsent)
			}
			return umpResponse(roundOne, request), nil
		case 2:
			if !strings.Contains(request.URL.String(), "rr2---") {
				t.Fatalf("expected redirected host: %s", request.URL)
			}
			if !strings.Contains(request.URL.RawQuery, "sig=fixture%2Btoken") {
				t.Fatalf("signed query mutated: %s", request.URL.RawQuery)
			}
			active, unsent, err := decodeStreamerContexts(streamer)
			if err != nil {
				t.Fatal(err)
			}
			if !int32SliceEqual(active, []int32{7}) || len(unsent) != 0 {
				t.Fatalf("active=%v unsent=%v", active, unsent)
			}
			return umpResponse(roundTwo, request), nil
		default:
			t.Fatalf("unexpected call %d", call)
			return nil, nil
		}
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(initial, func(config *Config) {
		config.Headers = http.Header{
			"Cookie":              {"secret=1"},
			"Authorization":       {"secret-auth"},
			"Proxy-Authorization": {"secret-proxy"},
		}
	})).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls=%d", calls.Load())
	}
	for _, header := range headers {
		for _, key := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
			if header.Get(key) != "" {
				t.Fatalf("%s leaked on %v", key, seenURLs)
			}
		}
	}
}

func TestRedirectLoopAndBudget(t *testing.T) {
	a := fixtureRedirectURL("1")
	b := fixtureRedirectURL("2")
	t.Run("loop", func(t *testing.T) {
		var calls atomic.Int32
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			call := calls.Add(1)
			if call == 1 {
				return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(b)), request), nil
			}
			return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(a)), request), nil
		})
		root, destination := testRoot(t, "out.bin")
		_, err := NewDownloader(transport, testConfig(a)).Download(context.Background(), root, destination, true, events.Nop())
		if !errors.Is(err, ErrRedirectLoop) {
			t.Fatalf("err=%v", err)
		}
		assertNoPublishedArtifact(t, root, destination)
	})
	t.Run("ninth", func(t *testing.T) {
		var calls atomic.Int32
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			n := int(calls.Add(1))
			next := fixtureRedirectURL(string(rune('a'+n)))
			// Use deterministic numeric hosts instead.
			next = "https://rr" + itoa(n+1) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
			return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(next)), request), nil
		})
		root, destination := testRoot(t, "out.bin")
		_, err := NewDownloader(transport, testConfig(
			"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
			func(config *Config) { config.MaxRounds = MaxRounds },
		)).Download(context.Background(), root, destination, true, events.Nop())
		if !errors.Is(err, ErrRedirectBudget) {
			t.Fatalf("err=%v", err)
		}
		assertNoPublishedArtifact(t, root, destination)
		if calls.Load() != MaxDirectiveRedirects+1 {
			t.Fatalf("calls=%d", calls.Load())
		}
	})
}

func TestDirectiveTransactionRollbackAndRetry(t *testing.T) {
	initial := fixtureRedirectURL("1")
	next := fixtureRedirectURL("9")
	cookie := validTestCookie()
	var attempts atomic.Int32
	var firstBody []byte
	goodMedia := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
	)
	staged := appendRedirectPart(appendContextUpdatePart(appendPolicyPart(nil, 10, cookie), 4, 1, SabrContextWriteOverwrite, []byte("staged"), true), next)
	staged = append(staged, encodePart(PartSABRError, []byte{0x08, 0x01})...)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		body, _ := io.ReadAll(request.Body)
		switch attempt {
		case 1:
			firstBody = bytes.Clone(body)
			return umpResponse(staged, request), nil
		case 2:
			t.Fatal("non-retryable directive failure must not retry")
			return nil, nil
		default:
			streamer, _, _ := streamerContextBytes(body)
			active, unsent, _ := decodeStreamerContexts(streamer)
			if len(active) != 0 || len(unsent) != 0 {
				t.Fatalf("staged contexts leaked: active=%v unsent=%v", active, unsent)
			}
			if strings.Contains(request.URL.Host, "rr9---") {
				t.Fatal("staged redirect leaked")
			}
			got, found, _ := playbackCookieFromStreamer(streamer)
			if found {
				t.Fatalf("staged cookie leaked: %v", got)
			}
			return umpResponse(goodMedia, request), nil
		}
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(initial, func(config *Config) {
		config.Attempts = 3
	})).Download(context.Background(), root, destination, true, events.Nop())
	if !errors.Is(err, ErrUnsupportedDirective) {
		t.Fatalf("err=%v", err)
	}
	assertNoPublishedArtifact(t, root, destination)
	if len(firstBody) == 0 {
		t.Fatal("expected captured failed request body")
	}

	// Retry path: HTTP failure must reuse body/endpoint/context; success commits new state.
	attempts.Store(0)
	var bodies [][]byte
	var hosts []string
	roundOK := appendRedirectPart(appendContextUpdatePart(nil, 8, 1, SabrContextWriteOverwrite, []byte("ok"), true), next)
	roundFinal := goodMedia
	transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		attempt := attempts.Add(1)
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, bytes.Clone(body))
		hosts = append(hosts, request.URL.Host)
		switch attempt {
		case 1:
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
				Body:       http.NoBody,
				Request:    request,
			}, nil
		case 2:
			if !bytes.Equal(bodies[0], body) {
				t.Fatal("retry must reuse immutable request body")
			}
			return umpResponse(roundOK, request), nil
		case 3:
			if hosts[2] == "" || !strings.Contains(hosts[2], "rr9---") {
				t.Fatalf("expected committed redirect host, got %s", hosts[2])
			}
			streamer, _, _ := streamerContextBytes(body)
			active, _, _ := decodeStreamerContexts(streamer)
			if !int32SliceEqual(active, []int32{8}) {
				t.Fatalf("active=%v", active)
			}
			return umpResponse(roundFinal, request), nil
		default:
			t.Fatalf("unexpected attempt %d", attempt)
			return nil, nil
		}
	})
	root, destination = testRoot(t, "retry.bin")
	_, err = NewDownloader(transport, testConfig(initial, func(config *Config) {
		config.Attempts = 3
	})).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
}

func TestEndOfTrackWithRedirectDoesNotPostAgain(t *testing.T) {
	var calls atomic.Int32
	body := appendEndOfTrackPart(appendRedirectPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("short")},
	), fixtureRedirectURL("2")))
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return umpResponse(body, request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(fixtureRedirectURL("1"))).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestContextOnlyAndRedirectOnlyCountTowardRounds(t *testing.T) {
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		n := calls.Add(1)
		if n == 1 {
			return umpResponse(encodePart(PartSABRContextUpdate, encodeSabrContextUpdate(1, 1, 1, []byte("c"), true)), request), nil
		}
		if n == 2 {
			return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(fixtureRedirectURL("2"))), request), nil
		}
		return umpResponse(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("x")},
		), request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(fixtureRedirectURL("1"), func(config *Config) {
		config.MaxRounds = 3
	})).Download(context.Background(), root, destination, true, events.Nop())
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

func TestCancellationDuringContextOnlyLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if calls.Add(1) >= 2 {
			cancel()
		}
		return umpResponse(encodePart(PartSABRContextUpdate, encodeSabrContextUpdate(int32(calls.Load()), 1, 1, []byte("c"), false)), request), nil
	})
	root, destination := testRoot(t, "out.bin")
	_, err := NewDownloader(transport, testConfig(fixtureRedirectURL("1"), func(config *Config) {
		config.MaxRounds = 8
	})).Download(ctx, root, destination, true, events.Nop())
	if err == nil {
		t.Fatal("expected cancellation")
	}
	assertNoPublishedArtifact(t, root, destination)
}

func TestConcurrentIndependentDownloadersDirectiveState(t *testing.T) {
	var (
		mu     sync.Mutex
		hostsA []string
		hostsB []string
	)
	makeTransport := func(label string, hosts *[]string) IsolatedTransport {
		var calls atomic.Int32
		redirectTo := fixtureRedirectURL(label)
		return roundTripFunc(func(request *http.Request) (*http.Response, error) {
			mu.Lock()
			*hosts = append(*hosts, request.URL.Host)
			mu.Unlock()
			if calls.Add(1) == 1 {
				body := appendContextUpdatePart(nil, 1, 1, 1, []byte(label), true)
				body = appendRedirectPart(body, redirectTo)
				return umpResponse(body, request), nil
			}
			return umpResponse(buildTestUMP(
				137,
				testSegment{headerID: 1, init: true, payload: []byte("INIT")},
				testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte(label)},
			), request), nil
		})
	}
	downloaderA := NewDownloader(makeTransport("3", &hostsA), testConfig(fixtureRedirectURL("1")))
	downloaderB := NewDownloader(makeTransport("4", &hostsB), testConfig(fixtureRedirectURL("2")))
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
	if len(hostsA) < 2 || !strings.Contains(hostsA[1], "rr3---") {
		t.Fatalf("hostsA=%v", hostsA)
	}
	if len(hostsB) < 2 || !strings.Contains(hostsB[1], "rr4---") {
		t.Fatalf("hostsB=%v", hostsB)
	}
}

func TestValidDirectiveThenUnsupportedCommitsNothing(t *testing.T) {
	cookie := validTestCookie()
	body := appendRedirectPart(appendContextUpdatePart(appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
	), 5, cookie), 1, 1, 1, []byte("secret-context"), true), fixtureRedirectURL("2"))
	body = append(body, encodePart(PartStreamProtectionStatus, []byte{0x08, 0x01})...)
	file, err := os.CreateTemp(t.TempDir(), "late-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	contexts := newSabrContextState()
	redirects := newRedirectTracker(fixtureRedirectURL("1"))
	ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, redirects)
	if !errors.Is(err, ErrUnsupportedDirective) {
		t.Fatalf("err=%v", err)
	}
	if !roundControlIsZero(ctrl) {
		t.Fatalf("ctrl=%+v", ctrl)
	}
	if len(contexts.entries) != 0 || redirects.count != 0 {
		t.Fatal("caller state mutated on failure")
	}
}

func decodeStreamerContexts(streamer []byte) (active []int32, unsent []int32, err error) {
	reader := fieldReader{data: streamer}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fStreamerCtxSabrContexts && wireType == wireBytes:
			nested := reader.bytes()
			typ, _, nestErr := decodeNestedSabrContext(nested)
			if nestErr != nil {
				return nil, nil, nestErr
			}
			active = append(active, typ)
		case num == fStreamerCtxUnsentSabrContexts && wireType == wireBytes:
			values, decErr := decodePackedProtoInt32(reader.bytes(), new(int))
			if decErr != nil {
				return nil, nil, decErr
			}
			unsent = append(unsent, values...)
		case num == fStreamerCtxUnsentSabrContexts && wireType == wireVarint:
			raw := reader.varint()
			value, convErr := protoInt32FromVarint(raw)
			if convErr != nil {
				return nil, nil, convErr
			}
			unsent = append(unsent, value)
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return nil, nil, reader.err
	}
	return active, unsent, nil
}

func decodeNestedSabrContext(data []byte) (typ int32, value []byte, err error) {
	reader := fieldReader{data: data}
	var seenType, seenValue bool
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fStreamerSabrContextType && wireType == wireVarint:
			raw := reader.varint()
			typ, err = protoInt32FromVarint(raw)
			if err != nil {
				return 0, nil, err
			}
			seenType = true
		case num == fStreamerSabrContextValue && wireType == wireBytes:
			value = append([]byte(nil), reader.bytes()...)
			seenValue = true
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return 0, nil, reader.err
	}
	if !seenType || !seenValue {
		return 0, nil, ErrInvalidProtobuf
	}
	return typ, value, nil
}

func int32SliceEqual(a, b []int32) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

func TestDirectiveFixturesLoad(t *testing.T) {
	root := filepath.Join("..", "..", "..", "conformance", "media", "youtube_sabr_directives")
	for _, name := range []string{
		"redirect-valid.ump.bin",
		"context-update-active.ump.bin",
		"sending-policy-packed.ump.bin",
		"mixed-media-redirect.ump.bin",
	} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(body) == 0 {
			t.Fatalf("empty fixture %s", name)
		}
		reader := NewReader(newByteReader(body), int64(len(body)))
		parts := 0
		for {
			part, ok, err := reader.ReadPart()
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if !ok {
				break
			}
			parts++
			switch part.Type {
			case PartSABRRedirect:
				if _, err := parseSabrRedirect(part.Payload); err != nil {
					t.Fatalf("%s redirect: %v", name, err)
				}
			case PartSABRContextUpdate:
				if _, err := parseSabrContextUpdate(part.Payload); err != nil {
					t.Fatalf("%s update: %v", name, err)
				}
			case PartSABRContextSendingPolicy:
				if _, err := parseSabrContextSendingPolicy(part.Payload); err != nil {
					t.Fatalf("%s policy: %v", name, err)
				}
			}
		}
		if parts == 0 {
			t.Fatalf("%s produced no parts", name)
		}
	}
}
