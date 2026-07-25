package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestParseSabrErrorValidation(t *testing.T) {
	ok := encodeSabrError("sabr.no_audio_selected", 2)
	tests := []struct {
		name    string
		payload []byte
		want    error
		code    int32
		typ     string
	}{
		{"ok", ok, nil, 2, "sabr.no_audio_selected"},
		{"missing_type", appendProtobufVarint(nil, fSabrErrorCode, 2), ErrInvalidProtobuf, 0, ""},
		{"missing_code", appendProtobufBytes(nil, fSabrErrorType, []byte("sabr.x")), ErrInvalidProtobuf, 0, ""},
		{"empty_type", append(appendProtobufBytes(nil, fSabrErrorType, nil), appendProtobufVarint(nil, fSabrErrorCode, 1)...), ErrInvalidProtobuf, 0, ""},
		{"duplicate_type", append(ok, appendProtobufBytes(nil, fSabrErrorType, []byte("other"))...), ErrInvalidProtobuf, 0, ""},
		{"duplicate_code", append(ok, appendProtobufVarint(nil, fSabrErrorCode, 3)...), ErrInvalidProtobuf, 0, ""},
		{"wrong_wire_type", appendProtobufVarint(nil, fSabrErrorType, 1), ErrInvalidProtobuf, 0, ""},
		{"wrong_wire_code", appendProtobufBytes(nil, fSabrErrorCode, []byte("x")), ErrInvalidProtobuf, 0, ""},
		{"oversize_type", append(
			appendProtobufBytes(nil, fSabrErrorType, bytes.Repeat([]byte{'a'}, MaxSabrErrorTypeBytes+1)),
			appendProtobufVarint(nil, fSabrErrorCode, 1)...,
		), ErrInvalidProtobuf, 0, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseSabrError(test.payload)
			if test.want == nil {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
				if got.Type != test.typ || got.Code != test.code {
					t.Fatalf("got=%+v", got)
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
		})
	}
}

func TestParseReloadPlayerResponseValidation(t *testing.T) {
	const token = "reload-token-fixture"
	ok := encodeReloadPlayerResponse(token)
	invalidUTF8 := []byte{0xff, 0xfe, 't', 'o', 'k', 'e', 'n'}
	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"ok", ok, nil},
		{"missing", nil, ErrInvalidProtobuf},
		{"empty_params", appendProtobufBytes(nil, fReloadPlaybackContextParams, nil), ErrInvalidProtobuf},
		{"empty_token", appendProtobufBytes(nil, fReloadPlaybackContextParams, appendProtobufBytes(nil, fReloadPlaybackParamsToken, nil)), ErrInvalidProtobuf},
		{"duplicate_params", append(ok, encodeReloadPlayerResponse("other")...), ErrInvalidProtobuf},
		{"duplicate_token", appendProtobufBytes(nil, fReloadPlaybackContextParams, append(
			appendProtobufBytes(nil, fReloadPlaybackParamsToken, []byte(token)),
			appendProtobufBytes(nil, fReloadPlaybackParamsToken, []byte("other"))...,
		)), ErrInvalidProtobuf},
		{"wrong_wire_params", appendProtobufVarint(nil, fReloadPlaybackContextParams, 1), ErrInvalidProtobuf},
		{"wrong_wire_token", appendProtobufBytes(nil, fReloadPlaybackContextParams, appendProtobufVarint(nil, fReloadPlaybackParamsToken, 1)), ErrInvalidProtobuf},
		{"oversize_token", encodeReloadPlayerResponse(strings.Repeat("t", MaxReloadTokenBytes+1)), ErrInvalidProtobuf},
		{"invalid_utf8_token", encodeReloadPlayerResponseBytes(invalidUTF8), ErrInvalidProtobuf},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseReloadPlayerResponse(test.payload)
			if test.want == nil {
				if err != nil {
					t.Fatalf("err=%v", err)
				}
				if got.Token != token {
					t.Fatalf("token mismatch")
				}
				return
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "reload-token") {
				t.Fatalf("reload token leaked: %v", err)
			}
			if test.name == "invalid_utf8_token" {
				if strings.Contains(err.Error(), string(invalidUTF8)) || strings.Contains(err.Error(), "\xff") {
					t.Fatalf("invalid UTF-8 token leaked: %v", err)
				}
			}
		})
	}
}

func TestSabrErrorAndReloadSignalsTransactional(t *testing.T) {
	cookie := validTestCookie()
	file, err := os.CreateTemp(t.TempDir(), "sig-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	t.Run("sabr_error", func(t *testing.T) {
		body := appendPolicyPart(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
		), 1000, cookie)
		body = appendSabrErrorPart(body, "sabr.no_audio_selected", 2)
		assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
		contexts := newSabrContextState()
		redirects := newRedirectTracker(fixtureRedirectURL("1"))
		ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, redirects)
		var signal *SabrErrorSignal
		if !errors.As(err, &signal) || signal.Type != "sabr.no_audio_selected" || signal.Code != 2 {
			t.Fatalf("err=%v", err)
		}
		if !roundControlIsZero(ctrl) {
			t.Fatalf("ctrl=%+v", ctrl)
		}
	})

	t.Run("reload", func(t *testing.T) {
		const secret = "secret-reload-token-value"
		body := appendPolicyPart(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
		), 1000, cookie)
		body = appendReloadPlayerPart(body, secret)
		assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
		contexts := newSabrContextState()
		redirects := newRedirectTracker(fixtureRedirectURL("1"))
		ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, redirects)
		var signal *ReloadPlayerSignal
		if !errors.As(err, &signal) || signal.ReloadToken() != secret {
			t.Fatalf("err=%v", err)
		}
		if !roundControlIsZero(ctrl) {
			t.Fatalf("ctrl=%+v", ctrl)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(fmt.Sprintf("%v %#v", signal, signal), secret) {
			t.Fatalf("reload token leaked through formatting")
		}
	})
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
			got, err := parseSabrContextSendingPolicy(payload, nil)
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
	_, err := parseSabrContextSendingPolicy(encodeSabrSendingPolicy(oversized, nil, nil, true), nil)
	if !errors.Is(err, ErrInvalidContextState) {
		t.Fatalf("err=%v", err)
	}
	_, err = parseSabrContextSendingPolicy(encodeSabrSendingPolicy([]int32{0}, nil, nil, false), nil)
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
	t.Run("canonical_case_and_trailing_dot_loop", func(t *testing.T) {
		initial := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken"
		alias := "https://RR1---sn-fixture.googlevideo.com./videoplayback/sabr/fixture?sig=fixture%2Btoken"
		keyInitial, err := sabrRedirectLoopKey(initial)
		if err != nil {
			t.Fatal(err)
		}
		keyAlias, err := sabrRedirectLoopKey(alias)
		if err != nil {
			t.Fatal(err)
		}
		if keyInitial != keyAlias {
			t.Fatalf("keys diverge: %q vs %q", keyInitial, keyAlias)
		}
		var seenRequestURL string
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			seenRequestURL = request.URL.String()
			return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(alias)), request), nil
		})
		root, destination := testRoot(t, "out.bin")
		_, err = NewDownloader(transport, testConfig(initial)).Download(context.Background(), root, destination, true, events.Nop())
		if !errors.Is(err, ErrRedirectLoop) {
			t.Fatalf("err=%v", err)
		}
		if !strings.HasPrefix(seenRequestURL, initial+"&rn=") && !strings.HasPrefix(seenRequestURL, initial+"?rn=") {
			// requestURL appends rn to exact server URL bytes
			if !strings.Contains(seenRequestURL, "sig=fixture%2Btoken") || strings.Contains(seenRequestURL, "RR1---") {
				t.Fatalf("request must preserve exact signed initial URL, got %q", seenRequestURL)
			}
		}
		assertNoPublishedArtifact(t, root, destination)
	})
	t.Run("distinct_signed_query_not_loop", func(t *testing.T) {
		initial := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=tokenA"
		next := "https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=tokenB"
		keyA, _ := sabrRedirectLoopKey(initial)
		keyB, _ := sabrRedirectLoopKey(next)
		if keyA == keyB {
			t.Fatal("distinct signed query bytes must remain distinct loop keys")
		}
		var calls atomic.Int32
		var secondURL string
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			call := calls.Add(1)
			if call == 1 {
				return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(next)), request), nil
			}
			secondURL = request.URL.String()
			return umpResponse(buildTestUMP(
				137,
				testSegment{headerID: 1, init: true, payload: []byte("INIT")},
				testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("x")},
			), request), nil
		})
		root, destination := testRoot(t, "out.bin")
		_, err := NewDownloader(transport, testConfig(initial)).Download(context.Background(), root, destination, true, events.Nop())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(secondURL, "sig=tokenB") {
			t.Fatalf("exact redirect URL not preserved: %q", secondURL)
		}
		if strings.Contains(secondURL, "sig=tokenA") {
			t.Fatalf("redirect mutated toward initial query: %q", secondURL)
		}
	})
	t.Run("exactly_eight_hops_then_complete", func(t *testing.T) {
		var calls atomic.Int32
		trackerSnapshot := newRedirectTracker("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture")
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			n := int(calls.Add(1))
			if n <= MaxDirectiveRedirects {
				next := "https://rr" + itoa(n+1) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
				return umpResponse(encodePart(PartSABRRedirect, encodeSabrRedirect(next)), request), nil
			}
			return umpResponse(buildTestUMP(
				137,
				testSegment{headerID: 1, init: true, payload: []byte("INIT")},
				testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("done")},
			), request), nil
		})
		root, destination := testRoot(t, "out.bin")
		_, err := NewDownloader(transport, testConfig(
			"https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture",
			func(config *Config) { config.MaxRounds = MaxRounds },
		)).Download(context.Background(), root, destination, true, events.Nop())
		if err != nil {
			t.Fatal(err)
		}
		if calls.Load() != int32(MaxDirectiveRedirects+1) {
			t.Fatalf("calls=%d want %d", calls.Load(), MaxDirectiveRedirects+1)
		}
		// Simulate expected committed hop count after success path.
		for i := 1; i <= MaxDirectiveRedirects; i++ {
			next := "https://rr" + itoa(i+1) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
			if err := trackerSnapshot.validate(next); err != nil {
				t.Fatalf("hop %d validate: %v", i, err)
			}
			trackerSnapshot.record(next)
		}
		if trackerSnapshot.count != MaxDirectiveRedirects {
			t.Fatalf("count=%d", trackerSnapshot.count)
		}
	})
	t.Run("ninth_redirect_rejected_before_commit", func(t *testing.T) {
		var calls atomic.Int32
		committed := newRedirectTracker("https://rr1---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture")
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			n := int(calls.Add(1))
			next := "https://rr" + itoa(n+1) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
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
		if calls.Load() != int32(MaxDirectiveRedirects+1) {
			t.Fatalf("calls=%d", calls.Load())
		}
		for i := 1; i <= MaxDirectiveRedirects; i++ {
			next := "https://rr" + itoa(i+1) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
			if err := committed.validate(next); err != nil {
				t.Fatalf("expected hop %d commitable: %v", i, err)
			}
			committed.record(next)
		}
		ninth := "https://rr" + itoa(MaxDirectiveRedirects+2) + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture"
		if err := committed.validate(ninth); !errors.Is(err, ErrRedirectBudget) {
			t.Fatalf("ninth validate err=%v", err)
		}
		if committed.count != MaxDirectiveRedirects {
			t.Fatalf("committed count=%d want %d", committed.count, MaxDirectiveRedirects)
		}
		if _, ok := committed.seen[mustLoopKey(t, ninth)]; ok {
			t.Fatal("ninth redirect must not be committed into tracker")
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
	if !errors.Is(err, ErrInvalidProtobuf) {
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
	if strings.Contains(err.Error(), "secret-context") || strings.Contains(err.Error(), "sig=") {
		t.Fatalf("diagnostics leaked secrets: %v", err)
	}
}

func TestContextStateBoundsExactAndPlusOne(t *testing.T) {
	t.Run("stored_entry_count", func(t *testing.T) {
		state := newSabrContextState()
		for i := 1; i <= MaxSabrContexts; i++ {
			if err := state.applyUpdate(sabrContextUpdateDirective{
				Type: int32(i), Value: []byte{byte(i)}, WritePolicy: SabrContextWriteOverwrite,
			}); err != nil {
				t.Fatalf("insert %d: %v", i, err)
			}
		}
		prior := state.clone()
		err := state.applyUpdate(sabrContextUpdateDirective{
			Type: int32(MaxSabrContexts + 1), Value: []byte("x"), WritePolicy: SabrContextWriteOverwrite,
		})
		if !errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("err=%v", err)
		}
		if len(state.entries) != MaxSabrContexts || len(prior.entries) != MaxSabrContexts {
			t.Fatalf("entries mutated on failure: %d", len(state.entries))
		}
	})
	t.Run("cumulative_bytes", func(t *testing.T) {
		state := newSabrContextState()
		chunk := MaxSabrContextValueBytes
		filled := 0
		typ := int32(1)
		for filled+chunk <= MaxSabrContextValueBytesTotal {
			if err := state.applyUpdate(sabrContextUpdateDirective{
				Type: typ, Value: bytes.Repeat([]byte{'a'}, chunk), WritePolicy: SabrContextWriteOverwrite,
			}); err != nil {
				t.Fatalf("fill typ=%d: %v", typ, err)
			}
			filled += chunk
			typ++
		}
		remain := MaxSabrContextValueBytesTotal - filled
		if remain > 0 {
			if err := state.applyUpdate(sabrContextUpdateDirective{
				Type: typ, Value: bytes.Repeat([]byte{'b'}, remain), WritePolicy: SabrContextWriteOverwrite,
			}); err != nil {
				t.Fatalf("exact fill: %v", err)
			}
			filled += remain
			typ++
		}
		if state.total != MaxSabrContextValueBytesTotal {
			t.Fatalf("total=%d want %d", state.total, MaxSabrContextValueBytesTotal)
		}
		priorTotal := state.total
		priorEntries := len(state.entries)
		err := state.applyUpdate(sabrContextUpdateDirective{
			Type: typ, Value: []byte("x"), WritePolicy: SabrContextWriteOverwrite,
		})
		if !errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("err=%v", err)
		}
		if state.total != priorTotal || len(state.entries) != priorEntries {
			t.Fatal("cumulative bound failure mutated state")
		}
	})
	t.Run("active_orphan_ids", func(t *testing.T) {
		state := newSabrContextState()
		types := make([]int32, MaxSabrContexts)
		for i := range types {
			types[i] = int32(i + 1)
		}
		if err := state.applySendingPolicy(sabrContextSendingPolicyDirective{Start: types}); err != nil {
			t.Fatal(err)
		}
		if len(state.active) != MaxSabrContexts {
			t.Fatalf("active=%d", len(state.active))
		}
		priorActive := len(state.active)
		err := state.applySendingPolicy(sabrContextSendingPolicyDirective{Start: []int32{int32(MaxSabrContexts + 1)}})
		if !errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("err=%v", err)
		}
		if len(state.active) != priorActive {
			t.Fatal("active map mutated after bound failure")
		}
		if _, ok := state.active[int32(MaxSabrContexts+1)]; ok {
			t.Fatal("orphan ID committed past bound")
		}
	})
}

func TestMultipleSendingPoliciesResponseWideBudget(t *testing.T) {
	t.Run("sequential_conflicting_ops", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "policy-")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
		contexts := newSabrContextState()
		if err := contexts.applyUpdate(sabrContextUpdateDirective{
			Type: 1, Value: []byte("one"), WritePolicy: SabrContextWriteOverwrite,
		}); err != nil {
			t.Fatal(err)
		}
		body := appendContextUpdatePart(nil, 1, 1, SabrContextWriteOverwrite, []byte("one"), false)
		body = appendSendingPolicyPart(body, []int32{1}, nil, nil, true)
		body = appendSendingPolicyPart(body, nil, []int32{1}, nil, true)
		body = appendSendingPolicyPart(body, []int32{1}, nil, nil, false)
		ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, newRedirectTracker(""))
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := ctrl.contexts.active[1]; !ok {
			t.Fatal("final start after stop must leave type active")
		}
	})
	t.Run("over_budget_second_policy_rolls_back", func(t *testing.T) {
		file, err := os.CreateTemp(t.TempDir(), "budget-")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
		contexts := newSabrContextState()
		if err := contexts.applyUpdate(sabrContextUpdateDirective{
			Type: 5, Value: []byte("keep"), SendByDefault: true, WritePolicy: SabrContextWriteOverwrite,
		}); err != nil {
			t.Fatal(err)
		}
		redirects := newRedirectTracker(fixtureRedirectURL("1"))
		first := make([]int32, MaxSabrContextPolicyOps-1)
		for i := range first {
			first[i] = 1 // idempotent starts: count ops without growing active map
		}
		body := appendSendingPolicyPart(nil, first, nil, nil, true)
		body = appendSendingPolicyPart(body, []int32{1, 2}, nil, nil, false) // response-wide ops exceed budget
		body = appendRedirectPart(body, fixtureRedirectURL("2"))
		ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, redirects)
		if !errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("err=%v", err)
		}
		if !roundControlIsZero(ctrl) {
			t.Fatalf("ctrl=%+v", ctrl)
		}
		if len(contexts.entries) != 1 || string(contexts.entries[5].Value) != "keep" {
			t.Fatal("caller contexts mutated")
		}
		if _, ok := contexts.active[5]; !ok || redirects.count != 0 {
			t.Fatal("caller active/redirect tracker mutated")
		}
		if strings.Contains(err.Error(), "keep") || strings.Contains(err.Error(), "sig=") {
			t.Fatalf("diagnostics leaked: %v", err)
		}
	})
}

func TestLateFailureAfterMultiplePoliciesAndRedirectRollsBack(t *testing.T) {
	cookie := validTestCookie()
	file, err := os.CreateTemp(t.TempDir(), "late-multi-")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	contexts := newSabrContextState()
	if err := contexts.applyUpdate(sabrContextUpdateDirective{
		Type: 9, Value: []byte("prior-secret"), SendByDefault: true, WritePolicy: SabrContextWriteOverwrite,
	}); err != nil {
		t.Fatal(err)
	}
	redirects := newRedirectTracker(fixtureRedirectURL("1"))
	body := appendPolicyPart(buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
	), 2500, cookie)
	body = appendContextUpdatePart(body, 3, 1, SabrContextWriteOverwrite, []byte("staged-secret"), true)
	body = appendSendingPolicyPart(body, []int32{3}, nil, nil, true)
	body = appendSendingPolicyPart(body, nil, []int32{9}, nil, false)
	body = appendRedirectPart(body, fixtureRedirectURL("2"))
	body = append(body, encodePart(PartSABRError, []byte{0x0A, 0x01, 'x'})...)
	ctrl, err := consumeStreamState(context.Background(), bytes.NewReader(body), assembler, contexts, redirects)
	if !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("err=%v", err)
	}
	if !roundControlIsZero(ctrl) {
		t.Fatalf("ctrl=%+v", ctrl)
	}
	if len(contexts.entries) != 1 || string(contexts.entries[9].Value) != "prior-secret" {
		t.Fatal("caller context entries changed")
	}
	if _, ok := contexts.active[9]; !ok {
		t.Fatal("caller active set changed")
	}
	if _, ok := contexts.entries[3]; ok {
		t.Fatal("staged context leaked into caller")
	}
	if redirects.count != 0 {
		t.Fatal("redirect tracker count changed")
	}
	if _, ok := redirects.seen[mustLoopKey(t, fixtureRedirectURL("2"))]; ok {
		t.Fatal("redirect key committed on failure")
	}
	if assembler.endOfTrackDone {
		t.Fatal("completion control must stay unset")
	}
	if strings.Contains(err.Error(), "prior-secret") || strings.Contains(err.Error(), "staged-secret") ||
		strings.Contains(err.Error(), "sig=") || strings.Contains(err.Error(), string(cookie)) {
		t.Fatalf("diagnostics leaked secrets: %v", err)
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

func mustLoopKey(t *testing.T, raw string) string {
	t.Helper()
	key, err := sabrRedirectLoopKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func directiveFixtureBytes() map[string][]byte {
	return map[string][]byte{
		"redirect-valid.ump.bin": encodePart(PartSABRRedirect, encodeSabrRedirect(fixtureRedirectURL("2"))),
		"context-update-active.ump.bin": encodePart(PartSABRContextUpdate, encodeSabrContextUpdate(
			7, SabrContextScopePlayback, SabrContextWriteOverwrite, []byte("fixture-ctx"), true,
		)),
		"sending-policy-packed.ump.bin": encodePart(PartSABRContextSendingPolicy, encodeSabrSendingPolicy(
			[]int32{7}, []int32{8}, []int32{9}, true,
		)),
		"mixed-media-redirect.ump.bin": appendRedirectPart(buildTestUMP(
			137,
			testSegment{headerID: 1, init: true, payload: []byte("INIT")},
			testSegment{headerID: 2, sequence: 0, duration: 1000, payload: []byte("seg")},
		), fixtureRedirectURL("2")),
	}
}

func TestDirectiveFixturesByteIdenticalAndSynthetic(t *testing.T) {
	root := filepath.Join("..", "..", "..", "conformance", "media", "youtube_sabr_directives")
	want := directiveFixtureBytes()
	secretNeedles := []string{
		"LIVE", "googlevideo.com/videoplayback?", "youtube.com", "Authorization",
		"Cookie=", "pot=", "oauth", "Bearer ",
	}
	for name, regenerated := range want {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, regenerated) {
			t.Fatalf("%s diverged from deterministic rebuild", name)
		}
		lower := strings.ToLower(string(got))
		for _, needle := range secretNeedles {
			if strings.Contains(lower, strings.ToLower(needle)) && name != "redirect-valid.ump.bin" && name != "mixed-media-redirect.ump.bin" {
				// googlevideo host appears in redirect fixtures by design; other needles must not.
				if needle != "googlevideo.com/videoplayback?" {
					t.Fatalf("%s contains unexpected needle %q", name, needle)
				}
			}
		}
		if bytes.Contains(got, []byte("secret")) || bytes.Contains(got, []byte("topsecret")) {
			t.Fatalf("%s contains secret marker", name)
		}
		reader := NewReader(newByteReader(got), int64(len(got)))
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
				if _, err := parseSabrContextSendingPolicy(part.Payload, nil); err != nil {
					t.Fatalf("%s policy: %v", name, err)
				}
			}
		}
		if parts == 0 {
			t.Fatalf("%s produced no parts", name)
		}
	}
}
