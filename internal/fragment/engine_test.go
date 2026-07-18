package fragment

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/events"
	"github.com/ytdlp-go/ytdlp/internal/network"
)

func TestEngineDownloadsRangesDecryptsAndAssemblesInOrder(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("abcdef0123456789")
	encrypted := encrypt(t, []byte("third-encrypted"), key, iv)
	combined := []byte("skip-second-tail")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/first":
			_, _ = writer.Write([]byte("first-"))
		case "/range":
			writer.Header().Set("Content-Range", fmt.Sprintf("bytes 5-10/%d", len(combined)))
			writer.WriteHeader(http.StatusPartialContent)
			_, _ = writer.Write(combined[5:11])
		case "/encrypted":
			_, _ = writer.Write(encrypted)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "assembled.bin")
	result, err := New(transport).Download(context.Background(), Job{
		OutputRoot: root, Destination: destination, Concurrency: 3,
		Segments: []Segment{
			{URL: server.URL + "/first"},
			{URL: server.URL + "/range", RangeStart: 5, RangeLength: 6},
			{URL: server.URL + "/encrypted", AES128: &AES128{Key: key, IV: iv}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if got, want := string(contents), "first-secondthird-encrypted"; got != want {
		t.Fatalf("assembled = %q, want %q", got, want)
	}
	if result.Downloaded != 3 || result.Bytes != int64(len(contents)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestEngineRevalidatesLegacyFragmentsWithoutDigests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = writer.Write([]byte("network"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "resume.bin")
	segments := []Segment{{URL: server.URL + "/one"}, {URL: server.URL + "/two"}}
	hash, _ := planHash(segments)
	workDir := destination + ".fragments"
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state, _ := json.Marshal(planState{Hash: hash})
	_ = os.WriteFile(filepath.Join(workDir, "state.json"), state, 0o600)
	_ = os.WriteFile(fragmentPath(workDir, 0), []byte("reused-"), 0o644)
	result, err := New(transport).Download(context.Background(), Job{OutputRoot: root, Destination: destination, Segments: segments}, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "networknetwork" || result.Reused != 0 || requests.Load() != 2 {
		t.Fatalf("contents = %q, result = %#v, requests = %d", contents, result, requests.Load())
	}
}

func TestFragmentEventsRedactSignedURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("segment"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	var captured []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error {
		captured = append(captured, event)
		return nil
	})
	root := t.TempDir()
	_, err := New(transport).Download(context.Background(), Job{
		OutputRoot: root, Destination: filepath.Join(root, "media.bin"),
		Segments: []Segment{{URL: server.URL + "/segment.ts?token=playback-secret&visible=yes"}},
	}, sink)
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("events = %#v", captured)
	}
	for _, event := range captured {
		if strings.Contains(event.URL, "secret") || !strings.Contains(event.URL, "visible=yes") {
			t.Fatalf("event URL was not safely redacted: %#v", event)
		}
	}
}

func TestEngineRejectsOversizedAndUnsafePlans(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 100)))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := New(transport).Download(context.Background(), Job{
		OutputRoot: root, Destination: filepath.Join(root, "large"), MaxSegmentSize: 10, Attempts: 1,
		Segments: []Segment{{URL: server.URL}},
	}, nil)
	if !errors.Is(err, ErrSegmentTooLarge) {
		t.Fatalf("large segment error = %v", err)
	}
	_, err = New(transport).Download(context.Background(), Job{
		OutputRoot: root, Destination: filepath.Join(root, "..", "escape"), Segments: []Segment{{URL: server.URL}},
	}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("unsafe destination error = %v", err)
	}
}

func TestEngineLimitsConcurrentRequestsPerHost(t *testing.T) {
	var active, peak atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		current := active.Add(1)
		for {
			previous := peak.Load()
			if current <= previous || peak.CompareAndSwap(previous, current) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		active.Add(-1)
		_, _ = writer.Write([]byte("x"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := New(transport).Download(context.Background(), Job{OutputRoot: root, Destination: filepath.Join(root, "out"), Concurrency: 4, PerHostConcurrency: 1, Segments: []Segment{{URL: server.URL + "/1"}, {URL: server.URL + "/2"}, {URL: server.URL + "/3"}}}, nil)
	if err != nil || peak.Load() != 1 {
		t.Fatalf("err=%v peak=%d", err, peak.Load())
	}
}

func TestEngineDoesNotRetryPermanentStatus(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(writer, "no", http.StatusForbidden)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := New(transport).Download(context.Background(), Job{OutputRoot: root, Destination: filepath.Join(root, "out"), Attempts: 3, Segments: []Segment{{URL: server.URL}}}, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestFragmentRetryTelemetryIsRedactedAndDeterministic(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			http.Error(writer, "retry", http.StatusServiceUnavailable)
			return
		}
		_, _ = writer.Write([]byte("ok"))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	var captured []events.Event
	sink := events.SinkFunc(func(_ context.Context, event events.Event) error { captured = append(captured, event); return nil })
	_, err := New(transport).Download(context.Background(), Job{OutputRoot: root, Destination: filepath.Join(root, "out"), Attempts: 2, Segments: []Segment{{URL: server.URL + "/segment?token=secret"}}}, sink)
	if err != nil {
		t.Fatal(err)
	}
	var retry events.Event
	for _, event := range captured {
		if event.Kind == events.KindRetry {
			retry = event
		}
	}
	if retry.Attempt != 2 || retry.Fragment != 1 || retry.Fragments != 1 || retry.Message != "transient fragment transport failure" || strings.Contains(retry.URL, "secret") || strings.Contains(retry.Message, "secret") {
		t.Fatalf("retry=%#v", retry)
	}
}

func TestEngineRejectsSymlinkedState(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "out")
	workDir := destination + ".fragments"
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), filepath.Join(workDir, "state.json")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	_, err := New(nil).Download(context.Background(), Job{OutputRoot: root, Destination: destination, Segments: []Segment{{URL: "https://example.test/a"}}}, nil)
	if !errors.Is(err, ErrUnsafeDestination) {
		t.Fatalf("error=%v", err)
	}
}
func TestManifestDoesNotReuseSymlinkArtifact(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "artifact")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manifest := &artifactManifest{state: manifestState{Artifacts: map[int]artifact{0: {Bytes: 1, SHA256: strings.Repeat("0", 64)}}}}
	if manifest.Valid(0, link) {
		t.Fatal("symlink artifact was trusted")
	}
}

func FuzzPlanHash(f *testing.F) {
	f.Add("https://example.test/a", int64(0), int64(1))
	f.Fuzz(func(t *testing.T, raw string, start, length int64) {
		_, _ = planHash([]Segment{{URL: raw, RangeStart: start, RangeLength: length}})
	})
}

func TestEngineRejectsExcessiveResourceConfiguration(t *testing.T) {
	root := t.TempDir()
	job := Job{OutputRoot: root, Destination: filepath.Join(root, "a"), Segments: []Segment{{URL: "https://example.test/a"}}, Concurrency: 129}
	if _, err := New(nil).Download(context.Background(), job, nil); !errors.Is(err, ErrTooMuchConcurrency) {
		t.Fatalf("concurrency error=%v", err)
	}
	job.Concurrency, job.Attempts = 1, 101
	if _, err := New(nil).Download(context.Background(), job, nil); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("attempt error=%v", err)
	}
}

func encrypt(t *testing.T, plaintext, key, iv []byte) []byte {
	t.Helper()
	padding := aes.BlockSize - len(plaintext)%aes.BlockSize
	input := append(append([]byte(nil), plaintext...), bytesOf(byte(padding), padding)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	output := make([]byte, len(input))
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(output, input)
	return output
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}
