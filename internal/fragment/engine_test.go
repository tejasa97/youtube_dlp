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

func TestEngineReusesCompletedFragments(t *testing.T) {
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
	if string(contents) != "reused-network" || result.Reused != 1 || requests.Load() != 1 {
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
