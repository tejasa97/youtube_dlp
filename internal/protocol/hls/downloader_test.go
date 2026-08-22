package hls

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tejasa97/ytdlp-go/internal/events"
	"github.com/tejasa97/ytdlp-go/internal/fragment"
	"github.com/tejasa97/ytdlp-go/internal/network"
)

func FuzzVODCanonicalizationNeverPersistsURI(f *testing.F) {
	f.Add("https://cdn.example.invalid/path.ts?token=bearer-secret")
	f.Fuzz(func(t *testing.T, raw string) {
		identity := FragmentIdentity{
			RepresentationIdentity: "provider:track:v1", PlaylistVersion: 7, PlaylistMediaSequence: 1,
			PlaylistDiscontinuitySequence: 0, SelectedDiscontinuityGroup: -1, CanonicalComplete: true,
			SelectedCodecs: raw, SelectedResolution: raw, SelectedAudioGroup: raw, SelectedAudioLanguage: raw, Encryption: raw,
		}
		scale := NewDownloader(nil, Config{}).vodScale(identity)
		encoded, err := json.Marshal(scale)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "https://") || strings.Contains(string(encoded), "token=") || (len(raw) > 8 && strings.Contains(string(encoded), raw)) {
			t.Fatalf("canonical scale leaked fuzz input: %q", encoded)
		}
		if len(scale.Key) > 256 || len(scale.Scope) > 256 {
			t.Fatalf("unbounded scale: %#v", scale)
		}
	})
}

func TestVODCheckpointProofControlsSignedURLReuseAndStaysSecretFree(t *testing.T) {
	var generation atomic.Int32
	var firstSegmentCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/vod.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:7\n#EXTINF:1,\nfirst.ts?token=token-%d\n#EXTINF:1,\nsecond.ts?token=token-%d\n#EXT-X-ENDLIST\n", generation.Load(), generation.Load())
		case "/first.ts":
			firstSegmentCalls.Add(1)
			_, _ = writer.Write([]byte("first"))
		case "/second.ts":
			_, _ = writer.Write([]byte("second"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "hls:vod:fixture"}
	proof := func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: strings.Repeat("a", 64), Scope: strings.Repeat("b", 64)}, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(ctx, server.URL+"/vod.m3u8?token=manifest-one", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	ledger, readErr := os.ReadFile(filepath.Join(checkpoint.Directory, "state.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{server.URL, "token-0", "manifest-one", "first.ts", "second.ts"} {
		if strings.Contains(string(ledger), secret) {
			t.Fatalf("checkpoint leaked %q: %s", secret, ledger)
		}
	}
	generation.Store(1)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(context.Background(), server.URL+"/vod.m3u8?token=manifest-two", root, destination, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 1 || firstSegmentCalls.Load() != 1 {
		t.Fatalf("result=%#v first segment calls=%d, want signed-URL reuse", result, firstSegmentCalls.Load())
	}
	body, readErr := os.ReadFile(destination)
	if readErr != nil || string(body) != "firstsecond" {
		t.Fatalf("output=%q err=%v", body, readErr)
	}
}

func TestVODCheckpointEncryptedFragmentsDoNotClaimCrossRefreshProof(t *testing.T) {
	download := NewDownloader(nil, Config{EquivalenceProof: func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "provider-immutable", Value: "immutable:fixture", Scope: "hls:fixture"}, true
	}})
	scale := download.vodScale(FragmentIdentity{Encrypted: true, DiscontinuitySequence: 1, MediaSequence: 2})
	if scale.Kind != "" || scale.Value != "" {
		t.Fatalf("encrypted scale=%#v, want no cross-refresh proof without key identity", scale)
	}
}

func TestVODScaleRejectsOpaqueCallbackProof(t *testing.T) {
	download := NewDownloader(nil, Config{EquivalenceProof: func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJ0b2tlbiJ9.signature", Scope: strings.Repeat("b", 64)}, true
	}})
	scale := download.vodScale(FragmentIdentity{CanonicalComplete: true, RepresentationIdentity: "provider:track:v1", PlaylistVersion: 1, PlaylistMediaSequence: 1, PlaylistDiscontinuitySequence: 0, SelectedDiscontinuityGroup: -1, Encryption: "none"})
	if scale.Kind != "" || scale.Value != "" || len(scale.Key) != 64 || len(scale.Scope) != 64 {
		t.Fatalf("opaque callback proof was retained: %#v", scale)
	}
}

func TestVODCanonicalStructuralKeyChangesForEveryResumeFact(t *testing.T) {
	download := NewDownloader(nil, Config{RepresentationIdentity: "provider:track:v1"})
	base := FragmentIdentity{
		Map: true, DiscontinuitySequence: 9, MediaSequence: 17, PartIndex: 2, Partial: true,
		RangeStart: 3, RangeLength: 7, DurationNanos: 1_000_000_000, MapOrdinal: 1,
		Encrypted: false, Encryption: "none", RepresentationIdentity: "provider:track:v1",
		PlaylistVersion: 7, PlaylistMediaSequence: 17, PlaylistDiscontinuitySequence: 9,
		SelectedBandwidth: 2_000_000, SelectedCodecs: "avc1.4d401f,mp4a.40.2", SelectedResolution: "1920x1080",
		SelectedAudioGroup: "audio-main", SelectedAudioLanguage: "en", SelectedDiscontinuityGroup: 9,
		CanonicalComplete: true,
	}
	want := download.vodScale(base).Key
	mutations := []struct {
		name string
		edit func(*FragmentIdentity)
	}{
		{"provider representation identity", func(v *FragmentIdentity) { v.RepresentationIdentity = "provider:track:v2" }},
		{"playlist version", func(v *FragmentIdentity) { v.PlaylistVersion++ }},
		{"playlist media sequence", func(v *FragmentIdentity) { v.PlaylistMediaSequence++ }},
		{"playlist discontinuity sequence", func(v *FragmentIdentity) { v.PlaylistDiscontinuitySequence++ }},
		{"selected bandwidth", func(v *FragmentIdentity) { v.SelectedBandwidth++ }},
		{"selected codecs", func(v *FragmentIdentity) { v.SelectedCodecs = "hev1" }},
		{"selected resolution", func(v *FragmentIdentity) { v.SelectedResolution = "1280x720" }},
		{"selected audio group", func(v *FragmentIdentity) { v.SelectedAudioGroup = "audio-alt" }},
		{"selected audio language", func(v *FragmentIdentity) { v.SelectedAudioLanguage = "fr" }},
		{"selected discontinuity group", func(v *FragmentIdentity) { v.SelectedDiscontinuityGroup++ }},
		{"map role", func(v *FragmentIdentity) { v.Map = false }},
		{"segment discontinuity", func(v *FragmentIdentity) { v.DiscontinuitySequence++ }},
		{"segment media sequence", func(v *FragmentIdentity) { v.MediaSequence++ }},
		{"part index", func(v *FragmentIdentity) { v.PartIndex++ }},
		{"partial", func(v *FragmentIdentity) { v.Partial = false }},
		{"range start", func(v *FragmentIdentity) { v.RangeStart++ }},
		{"range length", func(v *FragmentIdentity) { v.RangeLength++ }},
		{"duration", func(v *FragmentIdentity) { v.DurationNanos++ }},
		{"map ordinal", func(v *FragmentIdentity) { v.MapOrdinal++ }},
		{"key declaration", func(v *FragmentIdentity) { v.KeyDeclaration++ }},
		{"encryption declaration", func(v *FragmentIdentity) { v.Encryption = "aes-128:identity:00" }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := base
			mutation.edit(&candidate)
			if got := download.vodScale(candidate).Key; got == want {
				t.Fatalf("mutation %q did not change structural key", mutation.name)
			}
		})
	}
}

func TestVODCheckpointChangedProofRestartsWholeScope(t *testing.T) {
	var generation atomic.Int32
	var firstCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/vod.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nfirst.ts?token=%d\n#EXTINF:1,\nsecond.ts?token=%d\n#EXT-X-ENDLIST\n", generation.Load(), generation.Load())
		case "/first.ts":
			firstCalls.Add(1)
			if generation.Load() == 0 {
				_, _ = writer.Write([]byte("old"))
			} else {
				_, _ = writer.Write([]byte("new"))
			}
		case "/second.ts":
			_, _ = writer.Write([]byte("two"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "hls:proof:restart"}
	proofValue := strings.Repeat("a", 64)
	proof := func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: proofValue, Scope: strings.Repeat("b", 64)}, true
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(ctx, server.URL+"/vod.m3u8?token=one", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	generation.Store(1)
	proofValue = strings.Repeat("c", 64)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof}).Download(context.Background(), server.URL+"/vod.m3u8?token=two", root, destination, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 0 || firstCalls.Load() != 2 {
		t.Fatalf("result=%#v first calls=%d, want complete restart", result, firstCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "newtwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestVODCheckpointIncompleteCanonicalMetadataDownloadsAndRestarts(t *testing.T) {
	var generation atomic.Int32
	var firstCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1,CODECS=\"unsafe/path\"\nvod.m3u8\n")
		case "/vod.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nfirst.ts\n#EXTINF:1,\nsecond.ts\n#EXT-X-ENDLIST\n")
		case "/first.ts":
			firstCalls.Add(1)
			if generation.Load() == 0 {
				_, _ = writer.Write([]byte("old"))
			} else {
				_, _ = writer.Write([]byte("new"))
			}
		case "/second.ts":
			_, _ = writer.Write([]byte("two"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root, destination := t.TempDir(), ""
	destination = filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "hls:incomplete:restart"}
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1}).Download(ctx, server.URL+"/master.m3u8", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	generation.Store(1)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1}).Download(context.Background(), server.URL+"/master.m3u8", root, destination, true, nil)
	if err != nil {
		t.Fatalf("incomplete canonical VOD must download: %v", err)
	}
	if result.Reused != 0 || firstCalls.Load() != 2 {
		t.Fatalf("result=%#v first calls=%d, want full restart", result, firstCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "newtwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestVODCheckpointStableKeyIdentityReusesAcrossSignedKeyURLRotation(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("abcdef0123456789")
	var generation atomic.Int32
	var firstCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/vod.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin?token=%d\",IV=0x%x\n#EXTINF:1,\nfirst.ts?token=%d\n#EXTINF:1,\nsecond.ts?token=%d\n#EXT-X-ENDLIST\n", generation.Load(), iv, generation.Load(), generation.Load())
		case "/key.bin":
			_, _ = writer.Write(key)
		case "/first.ts":
			firstCalls.Add(1)
			_, _ = writer.Write(encryptSegment(t, []byte("one"), key, iv))
		case "/second.ts":
			_, _ = writer.Write(encryptSegment(t, []byte("two"), key, iv))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "payload")
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "hls:key:rotation"}
	proof := func(FragmentIdentity) (fragment.Scale, bool) {
		return fragment.Scale{Kind: "content-identity", Value: strings.Repeat("a", 64), Scope: strings.Repeat("b", 64)}, true
	}
	keyIdentity := func(FragmentIdentity) (string, bool) { return "provider:key:epoch-1", true }
	ctx, cancel := context.WithCancel(context.Background())
	_, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof, StableKeyIdentity: keyIdentity}).Download(ctx, server.URL+"/vod.m3u8?token=one", root, destination, true, events.SinkFunc(func(_ context.Context, event events.Event) error {
		if event.Kind == events.KindFragmentCompleted && event.Fragment == 1 {
			cancel()
		}
		return nil
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first run error=%v, want cancellation", err)
	}
	ledger, readErr := os.ReadFile(filepath.Join(checkpoint.Directory, "state.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, secret := range []string{"key.bin", "token=", string(key), string(iv)} {
		if strings.Contains(string(ledger), secret) {
			t.Fatalf("ledger leaked %q: %s", secret, ledger)
		}
	}
	generation.Store(1)
	result, err := NewDownloader(transport, Config{Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:v1", FragmentConcurrency: 1, EquivalenceProof: proof, StableKeyIdentity: keyIdentity}).Download(context.Background(), server.URL+"/vod.m3u8?token=two", root, destination, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Reused != 1 || firstCalls.Load() != 1 {
		t.Fatalf("result=%#v first calls=%d, want signed key URL reuse", result, firstCalls.Load())
	}
	if body, err := os.ReadFile(destination); err != nil || string(body) != "onetwo" {
		t.Fatalf("output=%q err=%v", body, err)
	}
}

func TestCheckpointRejectsLivePlaylistBeforeFragmentArtifacts(t *testing.T) {
	root := t.TempDir()
	checkpoint := &fragment.Checkpoint{Directory: filepath.Join(root, "checkpoint"), ResumeIdentity: "hls:live:fixture"}
	_, err := NewDownloader(nil, Config{
		Checkpoint: checkpoint, RequireVODCheckpoint: true, RepresentationIdentity: "provider:track:live",
		InitialPlaylist: &InitialPlaylist{URL: "https://example.test/live.m3u8", Body: []byte("#EXTM3U\n#EXTINF:1,\nsegment.ts\n")},
	}).Download(context.Background(), "https://example.test/live.m3u8", root, filepath.Join(root, "payload"), true, nil)
	if !errors.Is(err, ErrVODCheckpointRequired) {
		t.Fatalf("error=%v, want VOD checkpoint rejection", err)
	}
	if _, statErr := os.Lstat(checkpoint.Directory); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("checkpoint directory created for live playlist: %v", statErr)
	}
}

type hlsRoundTripper func(*http.Request) (*http.Response, error)

func (roundTripper hlsRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTripper(request)
}

func TestDownloadMasterByteRangeMapAndAES128(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := []byte("abcdef0123456789")
	encrypted := encryptSegment(t, []byte("secret-"), key, iv)
	blob := []byte("skip-range-tail")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/master.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nhigh.m3u8\n")
		case "/low.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nlow.bin\n#EXT-X-ENDLIST\n")
		case "/high.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MAP:URI=init.bin\n#EXTINF:1,\nplain.bin\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%s\n#EXTINF:1,\nsecret.bin\n#EXT-X-KEY:METHOD=NONE\n#EXT-X-BYTERANGE:5@5\n#EXTINF:1,\nblob.bin\n#EXT-X-ENDLIST\n", fmt.Sprintf("%x", iv))
		case "/init.bin":
			_, _ = writer.Write([]byte("init-"))
		case "/plain.bin":
			_, _ = writer.Write([]byte("plain-"))
		case "/key.bin":
			_, _ = writer.Write(key)
		case "/secret.bin":
			_, _ = writer.Write(encrypted)
		case "/blob.bin":
			writer.WriteHeader(http.StatusPartialContent)
			_, _ = writer.Write(blob[5:10])
		case "/low.bin":
			_, _ = writer.Write([]byte("wrong"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "hls.bin")
	result, err := NewDownloader(transport, Config{PollInterval: time.Millisecond}).Download(context.Background(), server.URL+"/master.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if got, want := string(contents), "init-plain-secret-range"; got != want {
		t.Fatalf("HLS contents = %q, want %q", got, want)
	}
	if result.Bytes != int64(len(contents)) {
		t.Fatalf("result = %#v", result)
	}
}

func TestDownloadAllowedHostsCoversMasterVariantsSegmentsKeysAndMaps(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, aes.BlockSize)
	encrypted := encryptSegment(t, []byte("segment"), key, iv)
	requests := make([]string, 0)
	var requestsMu sync.Mutex
	transport, err := network.New(network.Config{RoundTripper: hlsRoundTripper(func(request *http.Request) (*http.Response, error) {
		requestsMu.Lock()
		requests = append(requests, request.URL.String())
		requestsMu.Unlock()
		body := ""
		switch request.URL.String() {
		case "https://usher.ttvnw.net/master.m3u8?sig=master":
			body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://edge.ttvnw.net/media.m3u8?sig=variant\n"
		case "https://edge.ttvnw.net/media.m3u8?sig=variant":
			body = "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4?sig=init\"\n#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin?sig=key\"\n#EXTINF:1,\nsegment.ts?sig=segment\n#EXT-X-ENDLIST\n"
		case "https://edge.ttvnw.net/init.mp4?sig=init":
			body = "init"
		case "https://edge.ttvnw.net/key.bin?sig=key":
			body = "0123456789abcdef"
		case "https://edge.ttvnw.net/segment.ts?sig=segment":
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(encrypted))), Request: request}, nil
		default:
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	root := t.TempDir()
	destination := filepath.Join(root, "allowed.bin")
	_, err = NewDownloader(transport, Config{AllowedHosts: []string{"ttvnw.net"}}).Download(
		context.Background(), "https://usher.ttvnw.net/master.m3u8?sig=master", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if data, readErr := os.ReadFile(destination); readErr != nil || string(data) != "initsegment" {
		t.Fatalf("data=%q err=%v", data, readErr)
	}
	requestsMu.Lock()
	requestedURLs := append([]string(nil), requests...)
	requestsMu.Unlock()
	for _, rawURL := range requestedURLs {
		if !strings.Contains(rawURL, "sig=") {
			t.Fatalf("signed query was lost: %q", rawURL)
		}
	}
}

func TestDownloadAllowedHostsRejectsHostileResolvedURI(t *testing.T) {
	transport, err := network.New(network.Config{RoundTripper: hlsRoundTripper(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() == "https://usher.ttvnw.net/master.m3u8" {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\nhttps://evil.invalid/media.m3u8\n")), Request: request}, nil
		}
		return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	defer transport.CloseIdleConnections()
	_, err = NewDownloader(transport, Config{AllowedHosts: []string{"ttvnw.net"}}).Download(
		context.Background(), "https://usher.ttvnw.net/master.m3u8", t.TempDir(), filepath.Join(t.TempDir(), "hostile.bin"), false, nil)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("error=%v", err)
	}
}

func TestDownloadAllowedHostsRejectsMalformedPolicy(t *testing.T) {
	for _, rawHost := range []string{
		"", "ttvnw.net/", "ttvnw..net", " ttvnw.net", "ttvnw.net ",
		"ttvnw.net:443", "127.0.0.1", "[::1]", "edge_ttvnw.net", "-ttvnw.net", "ttvnw-.net",
	} {
		t.Run(rawHost, func(t *testing.T) {
			destination := filepath.Join(t.TempDir(), "malformed.bin")
			_, err := NewDownloader(nil, Config{AllowedHosts: []string{rawHost}}).Download(
				context.Background(), "https://usher.ttvnw.net/master.m3u8", t.TempDir(), destination, false, nil)
			if !errors.Is(err, ErrInvalidPlaylist) {
				t.Fatalf("host %q error=%v", rawHost, err)
			}
		})
	}
}

func TestDownloadDecryptsAES128MapWithDeclarationKey(t *testing.T) {
	mapKey := []byte("map-key-16-bytes")
	mediaKey := []byte("media-key-16byte")
	mapIV := []byte("map-iv-16-bytes!")
	mediaIV := []byte("media-iv-16-byte")
	encryptedMap := encryptSegment(t, []byte("init-"), mapKey, mapIV)
	encryptedMedia := encryptSegment(t, []byte("media"), mediaKey, mediaIV)
	var mapHits, mapKeyHits, mediaKeyHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprintf(writer, `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="map.key",IV=0x%x
#EXT-X-MAP:URI="init.mp4"
#EXT-X-KEY:METHOD=AES-128,URI="media.key",IV=0x%x
#EXTINF:1,
one.mp4
#EXTINF:1,
two.mp4
#EXT-X-ENDLIST
`, mapIV, mediaIV)
		case "/map.key":
			mapKeyHits.Add(1)
			_, _ = writer.Write(mapKey)
		case "/media.key":
			mediaKeyHits.Add(1)
			_, _ = writer.Write(mediaKey)
		case "/init.mp4":
			mapHits.Add(1)
			_, _ = writer.Write(encryptedMap)
		case "/one.mp4", "/two.mp4":
			_, _ = writer.Write(encryptedMedia)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "encrypted-map.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "init-mediamedia" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if mapHits.Load() != 1 || mapKeyHits.Load() != 1 || mediaKeyHits.Load() != 1 {
		t.Fatalf("map=%d map key=%d media key=%d", mapHits.Load(), mapKeyHits.Load(), mediaKeyHits.Load())
	}
}

func TestDownloadReemitsInitializationMapAfterABARotation(t *testing.T) {
	var mapAHits, mapBHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MAP:URI="a.init"
#EXTINF:1,
one.m4s
#EXT-X-MAP:URI="b.init"
#EXTINF:1,
two.m4s
#EXT-X-MAP:URI="a.init"
#EXTINF:1,
three.m4s
#EXT-X-ENDLIST
`)
		case "/a.init":
			mapAHits.Add(1)
			_, _ = writer.Write([]byte("A-"))
		case "/b.init":
			mapBHits.Add(1)
			_, _ = writer.Write([]byte("B-"))
		case "/one.m4s":
			_, _ = writer.Write([]byte("one-"))
		case "/two.m4s":
			_, _ = writer.Write([]byte("two-"))
		case "/three.m4s":
			_, _ = writer.Write([]byte("three"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "map-rotation.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "A-one-B-two-A-three" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if mapAHits.Load() != 2 || mapBHits.Load() != 1 {
		t.Fatalf("map A=%d map B=%d", mapAHits.Load(), mapBHits.Load())
	}
}

func TestDownloadReemitsInitializationMapAfterDiscontinuity(t *testing.T) {
	var mapHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MAP:URI="init"
#EXTINF:1,
one
#EXT-X-DISCONTINUITY
#EXT-X-MAP:URI="init"
#EXTINF:1,
two
#EXT-X-ENDLIST
`)
		case "/init":
			mapHits.Add(1)
			_, _ = writer.Write([]byte("I-"))
		case "/one":
			_, _ = writer.Write([]byte("one-"))
		case "/two":
			_, _ = writer.Write([]byte("two"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "map-discontinuity.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "I-one-I-two" || mapHits.Load() != 2 {
		t.Fatalf("contents=%q map hits=%d err=%v", contents, mapHits.Load(), err)
	}
}

func TestDownloadSelectedDiscontinuityGroupFiltersMapsKeysAndParts(t *testing.T) {
	selectedKey := []byte("0123456789abcdef")
	selectedIV := []byte("selected-iv-1234")
	encryptedMap := encryptSegment(t, []byte("selected-init"), selectedKey, selectedIV)
	encryptedMedia := encryptSegment(t, []byte("selected"), selectedKey, selectedIV)
	var unselectedHits, selectedMapHits, selectedKeyHits, selectedPartHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprintf(writer, `#EXTM3U
#EXT-X-KEY:METHOD=AES-128,URI="unselected.key",IV=0x%s
#EXT-X-MAP:URI="unselected.init"
#EXTINF:1,
unselected.bin
#EXT-X-DISCONTINUITY
#EXT-X-KEY:METHOD=AES-128,URI="selected.key",IV=0x%s
#EXT-X-MAP:URI="selected.init"
#EXT-X-PART:DURATION=0.5,URI="selected-part.bin"
#EXTINF:1,
selected.bin
#EXT-X-ENDLIST
`, fmt.Sprintf("%x", selectedIV), fmt.Sprintf("%x", selectedIV))
		case "/selected.key":
			selectedKeyHits.Add(1)
			_, _ = writer.Write(selectedKey)
		case "/selected.init":
			selectedMapHits.Add(1)
			_, _ = writer.Write(encryptedMap)
		case "/selected.bin":
			_, _ = writer.Write(encryptedMedia)
		case "/selected-part.bin":
			selectedPartHits.Add(1)
			_, _ = writer.Write([]byte("must-not-download"))
		case "/unselected.key", "/unselected.init", "/unselected.bin":
			unselectedHits.Add(1)
			http.Error(writer, "unselected group fetched", http.StatusInternalServerError)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "selected.bin")
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 1}
	_, err := NewDownloader(transport, Config{SelectedDiscontinuityGroup: &selectedGroup}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "selected-initselected" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if unselectedHits.Load() != 0 || selectedMapHits.Load() != 1 || selectedKeyHits.Load() != 1 || selectedPartHits.Load() != 0 {
		t.Fatalf("unselected=%d selected map=%d key=%d part=%d", unselectedHits.Load(), selectedMapHits.Load(), selectedKeyHits.Load(), selectedPartHits.Load())
	}
}

func TestDownloadInitialPlaylistSnapshotReusesInitialLoadAndReloadsLive(t *testing.T) {
	var reloads, segmentHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.m3u8" {
			if request.Header.Get("X-Snapshot-Test") != "reloaded" {
				t.Errorf("reload headers=%q, want selected headers", request.Header.Get("X-Snapshot-Test"))
			}
			if reloads.Add(1) != 1 {
				t.Errorf("unexpected additional playlist reload")
			}
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\none.bin\n#EXT-X-ENDLIST\n")
			return
		}
		if request.URL.Path == "/one.bin" {
			segmentHits.Add(1)
			_, _ = writer.Write([]byte("one"))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "snapshot.bin")
	initial := []byte("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\none.bin\n")
	_, err := NewDownloader(transport, Config{
		Headers: http.Header{"X-Snapshot-Test": {"reloaded"}}, PollInterval: time.Millisecond,
		InitialPlaylist: &InitialPlaylist{URL: server.URL + "/live.m3u8", Body: initial},
	}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "one" || reloads.Load() != 1 || segmentHits.Load() != 1 {
		t.Fatalf("contents=%q err=%v reloads=%d segments=%d, want cached initial plus one network reload", contents, err, reloads.Load(), segmentHits.Load())
	}
}

func TestDownloadNilDiscontinuitySelectionPreservesAllGroups(t *testing.T) {
	var segmentHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXTINF:1,
one.bin
#EXT-X-DISCONTINUITY
#EXTINF:1,
two.bin
#EXT-X-ENDLIST
`)
		case "/one.bin":
			segmentHits.Add(1)
			_, _ = writer.Write([]byte("one-"))
		case "/two.bin":
			segmentHits.Add(1)
			_, _ = writer.Write([]byte("two"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "all-groups.bin")
	_, err := NewDownloader(transport, Config{SelectedDiscontinuityGroup: nil}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "one-two" || segmentHits.Load() != 2 {
		t.Fatalf("contents=%q hits=%d err=%v", contents, segmentHits.Load(), err)
	}
}

func TestDownloadSelectedDiscontinuityGroupAbsentVODReturnsNoSegments(t *testing.T) {
	var unselectedHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nother.bin\n#EXT-X-ENDLIST\n")
		case "/other.bin":
			unselectedHits.Add(1)
			_, _ = writer.Write([]byte("other"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "absent.bin")
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 1}
	_, err := NewDownloader(transport, Config{SelectedDiscontinuityGroup: &selectedGroup}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) || unselectedHits.Load() != 0 {
		t.Fatalf("error=%v unselected hits=%d", err, unselectedHits.Load())
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error=%v", statErr)
	}
}

func TestDownloadSelectedDiscontinuityGroupAbsentLiveIsBounded(t *testing.T) {
	var polls, unselectedHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			polls.Add(1)
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nother.bin\n")
		case "/other.bin":
			unselectedHits.Add(1)
			_, _ = writer.Write([]byte("other"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "absent-live.bin")
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 1}
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3, SelectedDiscontinuityGroup: &selectedGroup}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if !errors.Is(err, ErrLivePollLimit) || polls.Load() != 3 || unselectedHits.Load() != 0 {
		t.Fatalf("error=%v polls=%d unselected hits=%d", err, polls.Load(), unselectedHits.Load())
	}
	if _, statErr := os.Stat(destination); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error=%v", statErr)
	}
}

func TestDownloadSelectedDiscontinuityGroupDoesNotSwitchWhenAbsent(t *testing.T) {
	var polls, selectedHits, unselectedHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\nselected.bin\n")
			case 2:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:2\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\nunselected.bin\n")
			default:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:2\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\nunselected.bin\n#EXT-X-ENDLIST\n")
			}
		case "/selected.bin":
			selectedHits.Add(1)
			_, _ = writer.Write([]byte("selected"))
		case "/unselected.bin":
			unselectedHits.Add(1)
			_, _ = writer.Write([]byte("unselected"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "stable-selected.bin")
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 0}
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4, SelectedDiscontinuityGroup: &selectedGroup}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "selected" || polls.Load() != 3 || selectedHits.Load() != 1 || unselectedHits.Load() != 0 {
		t.Fatalf("contents=%q err=%v polls=%d selected=%d unselected=%d", contents, err, polls.Load(), selectedHits.Load(), unselectedHits.Load())
	}
}

func TestDownloadSelectedDiscontinuityGroupHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.m3u8" {
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nother.bin\n")
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 1}
	_, err := NewDownloader(transport, Config{PollInterval: time.Second, SelectedDiscontinuityGroup: &selectedGroup}).Download(
		ctx, server.URL+"/live.m3u8", root, filepath.Join(root, "cancel-selected.bin"), false, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Download() error=%v context=%v", err, ctx.Err())
	}
}

func TestDownloadSelectedDiscontinuityGroupRejectsInconsistentBoundaryState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/media.m3u8" {
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:1
#EXTINF:1,
one.bin
#EXT-X-DISCONTINUITY
#EXTINF:1,
two.bin
#EXT-X-DISCONTINUITY-SEQUENCE:0
#EXTINF:1,
three.bin
#EXT-X-ENDLIST
`)
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	selectedGroup := DiscontinuityGroupID{DiscontinuitySequence: 0}
	_, err := NewDownloader(transport, Config{SelectedDiscontinuityGroup: &selectedGroup}).Download(
		context.Background(), server.URL+"/media.m3u8", root, filepath.Join(root, "malformed.bin"), false, nil)
	if !errors.Is(err, ErrInvalidDiscontinuityGroups) {
		t.Fatalf("error=%v", err)
	}
}

func TestDownloadLivePreservesInitializationMapRotations(t *testing.T) {
	var polls, mapAHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-MAP:URI=\"a.init\"\n#EXTINF:1,\n10.m4s\n")
			case 2:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:11\n#EXTINF:1,\n11.m4s\n")
			default:
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:12\n#EXT-X-MAP:URI=\"a.init\"\n#EXTINF:1,\n12.m4s\n#EXT-X-ENDLIST\n")
			}
		case "/a.init":
			mapAHits.Add(1)
			_, _ = writer.Write([]byte("A-"))
		case "/10.m4s":
			_, _ = writer.Write([]byte("ten-"))
		case "/11.m4s":
			_, _ = writer.Write([]byte("eleven-"))
		case "/12.m4s":
			_, _ = writer.Write([]byte("twelve"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live-map-rotation.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "A-ten-eleven-A-twelve" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if polls.Load() != 3 || mapAHits.Load() != 2 {
		t.Fatalf("polls=%d map A=%d", polls.Load(), mapAHits.Load())
	}
}

func TestDownloadLivePollDeduplicatesSegments(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n")
			} else {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n#EXTINF:1,\n11.bin\n#EXT-X-ENDLIST\n")
			}
		case "/10.bin":
			_, _ = writer.Write([]byte("ten-"))
		case "/11.bin":
			_, _ = writer.Write([]byte("eleven"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, _ := os.ReadFile(destination)
	if string(contents) != "ten-eleven" || polls.Load() != 2 {
		t.Fatalf("contents = %q, polls = %d", contents, polls.Load())
	}
}

func TestDownloadLowLatencyPartsAreReplacedByCompletedSegment(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXT-X-PART:DURATION=0.5,URI=10.0.bin\n#EXT-X-PART:DURATION=0.5,URI=10.1.bin\n")
			} else {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:10\n#EXTINF:1,\n10.bin\n#EXT-X-PART:DURATION=0.5,URI=11.0.bin\n#EXT-X-PART:DURATION=0.5,URI=11.1.bin\n#EXT-X-ENDLIST\n")
			}
		case "/10.bin":
			_, _ = writer.Write([]byte("complete-ten-"))
		case "/10.0.bin", "/10.1.bin":
			_, _ = writer.Write([]byte("duplicate"))
		case "/11.0.bin":
			_, _ = writer.Write([]byte("eleven-part-a-"))
		case "/11.1.bin":
			_, _ = writer.Write([]byte("eleven-part-b"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "low-latency.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "complete-ten-eleven-part-a-eleven-part-b" || polls.Load() != 2 {
		t.Fatalf("contents=%q polls=%d error=%v", contents, polls.Load(), err)
	}
}

func TestDownloadLiveHonorsCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, ".m3u8") {
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nseg.bin\n")
		} else {
			_, _ = writer.Write([]byte("segment"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{PollInterval: time.Second}).Download(ctx, server.URL+"/live.m3u8", root, filepath.Join(root, "cancel.bin"), false, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Download() error = %v, context = %v", err, ctx.Err())
	}
}

func TestDownloadPropagatesSelectedFormatHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Referer") != "https://origin.example/watch" || request.Header.Get("X-Media-Token") != "fixture" {
			http.Error(writer, "missing format headers", http.StatusForbidden)
			return
		}
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXTINF:1,\nsegment.bin\n#EXT-X-ENDLIST\n")
		case "/segment.bin":
			_, _ = writer.Write([]byte("protected"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "protected.bin")
	_, err := NewDownloader(transport, Config{Headers: http.Header{
		"Referer":       []string{"https://origin.example/watch"},
		"X-Media-Token": []string{"fixture"},
	}}).Download(context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "protected" {
		t.Fatalf("contents = %q, error = %v", contents, err)
	}
}

// Regression derived from yt-dlp aefce1eea: an empty test fragment list must
// remain empty and fail explicitly rather than manufacturing a nil fragment.
func TestDownloadEmptyPlaylistReturnsNoSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-ENDLIST\n")
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/empty.m3u8", root, filepath.Join(root, "empty.bin"), false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("empty playlist error = %v", err)
	}
}

func TestDownloadSuppressesAttributedVODAdvertisements(t *testing.T) {
	fixture, err := os.ReadFile("../../../conformance/media/hls_ads/mixed-vod.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mixed-vod.m3u8":
			_, _ = writer.Write(fixture)
		case "/media-40.bin":
			_, _ = writer.Write([]byte("forty-"))
		case "/media-42.bin":
			_, _ = writer.Write([]byte("forty-two-"))
		case "/media-44.bin":
			_, _ = writer.Write([]byte("forty-four"))
		case "/anvato-ad-41.bin", "/uplynk-ad-43.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "suppressed.bin")
	result, err := NewDownloader(transport, Config{MaxSegments: 3}).Download(
		context.Background(), server.URL+"/mixed-vod.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "forty-forty-two-forty-four" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(contents)), "235f2f70c5d54c69777f1f36a19a0f23929a545457420b651de1958b3bfea86e"; got != want {
		t.Fatalf("output SHA-256=%s want %s", got, want)
	}
	if advertisementHits.Load() != 0 || result.Downloaded != 3 {
		t.Fatalf("ad hits=%d result=%#v", advertisementHits.Load(), result)
	}
}

func TestDownloadLiveAdvertisementReclassificationAndCompleteReplacement(t *testing.T) {
	var polls atomic.Int32
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#ANVATO-SEGMENT-INFO:type=ad
#EXTINF:1,
ad-10.bin
#EXT-X-PART:DURATION=0.5,URI="ad-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-11.1.bin"
`)
			case 2:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:1,
media-10.bin
#UPLYNK-SEGMENT,ad
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.1.bin"
`)
			default:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:1,
media-10.bin
#EXTINF:1,
media-11.bin
#EXT-X-ENDLIST
`)
			}
		case "/media-10.bin":
			_, _ = writer.Write([]byte("ten-"))
		case "/media-11.bin":
			_, _ = writer.Write([]byte("eleven"))
		case "/ad-10.bin", "/ad-11.0.bin", "/ad-11.1.bin", "/ad-new-11.0.bin", "/ad-new-11.1.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live-suppressed.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "ten-eleven" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if polls.Load() != 3 || advertisementHits.Load() != 0 {
		t.Fatalf("polls=%d ad hits=%d", polls.Load(), advertisementHits.Load())
	}
}

func TestDownloadAdvertisementKeysMapsAndPhysicalAESIV(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, aes.BlockSize)
	iv[len(iv)-1] = 6 // Ad is physical sequence 5; retained media is sequence 6.
	encrypted := encryptSegment(t, []byte("media-secret"), key, iv)
	var adResourceHits atomic.Int32
	var mediaKeyHits atomic.Int32
	var mediaMapHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:5
#ANVATO-SEGMENT-INFO:type=ad
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXTINF:1,
ad-5.bin
#ANVATO-SEGMENT-INFO:type=master
#EXT-X-KEY:METHOD=NONE
#EXT-X-MAP:URI="media-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="media-key.bin"
#EXTINF:1,
media-6.bin
#EXT-X-ENDLIST
`)
		case "/ad-init.bin", "/ad-key.bin", "/ad-5.bin":
			adResourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		case "/media-init.bin":
			mediaMapHits.Add(1)
			_, _ = writer.Write([]byte("init-"))
		case "/media-key.bin":
			mediaKeyHits.Add(1)
			_, _ = writer.Write(key)
		case "/media-6.bin":
			_, _ = writer.Write(encrypted)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "encrypted.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "init-media-secret" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if adResourceHits.Load() != 0 || mediaMapHits.Load() != 1 || mediaKeyHits.Load() != 1 {
		t.Fatalf("ad=%d map=%d key=%d", adResourceHits.Load(), mediaMapHits.Load(), mediaKeyHits.Load())
	}
}

func TestDownloadAllAdvertisementsReturnsNoSegmentsWithoutScratch(t *testing.T) {
	var resourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ads.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#UPLYNK-SEGMENT,ad
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin"
#EXTINF:1,
ad.bin
#EXT-X-ENDLIST
`)
		default:
			resourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "ads.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/ads.m3u8", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("error=%v", err)
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", resourceHits.Load())
	}
	for _, path := range []string{destination, destination + ".part", destination + ".fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scratch path %q exists or returned unexpected error: %v", path, statErr)
		}
	}
}

func TestDownloadSuppressesCueVODAdvertisements(t *testing.T) {
	fixture, err := os.ReadFile("../../../conformance/media/hls_ads/mixed-cue-vod.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mixed-cue-vod.m3u8":
			_, _ = writer.Write(fixture)
		case "/media-50.bin":
			_, _ = writer.Write([]byte("fifty-"))
		case "/media-53.bin":
			_, _ = writer.Write([]byte("fifty-three"))
		case "/cue-ad-51.bin", "/cue-ad-52.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cue-suppressed.bin")
	result, err := NewDownloader(transport, Config{MaxSegments: 2}).Download(
		context.Background(), server.URL+"/mixed-cue-vod.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "fifty-fifty-three" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if advertisementHits.Load() != 0 || result.Downloaded != 2 {
		t.Fatalf("ad hits=%d result=%#v", advertisementHits.Load(), result)
	}
}

func TestDownloadCueLiveOUTCONTReclassificationAndCompleteReplacement(t *testing.T) {
	var polls atomic.Int32
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-CUE-OUT:DURATION=4
#EXTINF:1,
ad-10.bin
#EXT-X-PART:DURATION=0.5,URI="ad-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-11.1.bin"
`)
			case 2:
				// Sliding snapshot starts mid-break; OUT-CONT re-establishes ads.
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-SKIP:SKIPPED-SEGMENTS=1
#EXT-X-CUE-OUT-CONT:ElapsedTime=1.0,Duration=4
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.1.bin"
`)
			default:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-SKIP:SKIPPED-SEGMENTS=1
#EXT-X-CUE-IN
#EXTINF:1,
media-11.bin
#EXT-X-ENDLIST
`)
			}
		case "/media-11.bin":
			_, _ = writer.Write([]byte("eleven"))
		case "/ad-10.bin", "/ad-11.0.bin", "/ad-11.1.bin", "/ad-new-11.0.bin", "/ad-new-11.1.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cue-live.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "eleven" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if polls.Load() != 3 || advertisementHits.Load() != 0 {
		t.Fatalf("polls=%d ad hits=%d", polls.Load(), advertisementHits.Load())
	}
}

func TestDownloadCueAdvertisementKeysMapsAndPhysicalAESIV(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, aes.BlockSize)
	iv[len(iv)-1] = 8 // Ad occupies physical sequences 7; retained media is 8.
	encrypted := encryptSegment(t, []byte("cue-secret"), key, iv)
	var adResourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:7
#EXT-X-CUE-OUT
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXTINF:1,
ad-7.bin
#EXT-X-CUE-IN
#EXT-X-KEY:METHOD=NONE
#EXT-X-MAP:URI="media-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="media-key.bin"
#EXTINF:1,
media-8.bin
#EXT-X-ENDLIST
`)
		case "/ad-init.bin", "/ad-key.bin", "/ad-7.bin":
			adResourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		case "/media-init.bin":
			_, _ = writer.Write([]byte("init-"))
		case "/media-key.bin":
			_, _ = writer.Write(key)
		case "/media-8.bin":
			_, _ = writer.Write(encrypted)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cue-encrypted.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "init-cue-secret" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if adResourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", adResourceHits.Load())
	}
}

func TestDownloadSuppressesDaterangeVODAdvertisements(t *testing.T) {
	fixture, err := os.ReadFile("../../../conformance/media/hls_ads/mixed-daterange-vod.m3u8")
	if err != nil {
		t.Fatal(err)
	}
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/mixed-daterange-vod.m3u8":
			_, _ = writer.Write(fixture)
		case "/media-60.bin":
			_, _ = writer.Write([]byte("sixty-"))
		case "/media-63.bin":
			_, _ = writer.Write([]byte("sixty-three"))
		case "/daterange-ad-61.bin", "/daterange-ad-62.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "daterange-suppressed.bin")
	result, err := NewDownloader(transport, Config{MaxSegments: 2}).Download(
		context.Background(), server.URL+"/mixed-daterange-vod.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "sixty-sixty-three" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if advertisementHits.Load() != 0 || result.Downloaded != 2 {
		t.Fatalf("ad hits=%d result=%#v", advertisementHits.Load(), result)
	}
}

func TestDownloadDaterangeLiveReclassificationAndDelta(t *testing.T) {
	var polls atomic.Int32
	var advertisementHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			switch polls.Add(1) {
			case 1:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-DATERANGE:ID="live-break",SCTE35-OUT=0xFC301B007E000000000000000A050000000100DF0000000000001AA436BF
#EXTINF:1,
ad-10.bin
#EXT-X-PART:DURATION=0.5,URI="ad-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-11.1.bin"
`)
			case 2:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-SKIP:SKIPPED-SEGMENTS=1
#EXT-X-DATERANGE:ID="live-break",SCTE35-OUT=0xFC301B007E000000000000000A050000000100DF0000000000001AA436BF
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.0.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-new-11.1.bin"
`)
			default:
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-SKIP:SKIPPED-SEGMENTS=1
#EXT-X-DATERANGE:ID="live-break",SCTE35-IN=0xFC301B007E000000000000000A0500000001005F0000000000003774D8DA
#EXTINF:1,
media-11.bin
#EXT-X-ENDLIST
`)
			}
		case "/media-11.bin":
			_, _ = writer.Write([]byte("eleven"))
		case "/ad-10.bin", "/ad-11.0.bin", "/ad-11.1.bin", "/ad-new-11.0.bin", "/ad-new-11.1.bin":
			advertisementHits.Add(1)
			_, _ = writer.Write([]byte("ADVERTISEMENT"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "daterange-live.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 4}).Download(
		context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "eleven" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if polls.Load() != 3 || advertisementHits.Load() != 0 {
		t.Fatalf("polls=%d ad hits=%d", polls.Load(), advertisementHits.Load())
	}
}

func TestDownloadDaterangeAdvertisementKeysMapsAndPhysicalAESIV(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, aes.BlockSize)
	iv[len(iv)-1] = 9
	encrypted := encryptSegment(t, []byte("daterange-secret"), key, iv)
	var adResourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:8
#EXT-X-DATERANGE:ID="enc-break",SCTE35-OUT=0xFC301B007E000000000000000A050000000100DF0000000000001AA436BF
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXTINF:1,
ad-8.bin
#EXT-X-DATERANGE:ID="enc-break",SCTE35-IN=0xFC301B007E000000000000000A0500000001005F0000000000003774D8DA
#EXT-X-KEY:METHOD=NONE
#EXT-X-MAP:URI="media-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="media-key.bin"
#EXTINF:1,
media-9.bin
#EXT-X-ENDLIST
`)
		case "/ad-init.bin", "/ad-key.bin", "/ad-8.bin":
			adResourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		case "/media-init.bin":
			_, _ = writer.Write([]byte("init-"))
		case "/media-key.bin":
			_, _ = writer.Write(key)
		case "/media-9.bin":
			_, _ = writer.Write(encrypted)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "daterange-encrypted.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "init-daterange-secret" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
	if adResourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", adResourceHits.Load())
	}
}

func TestDownloadDaterangeAllAdvertisementsAndCancellation(t *testing.T) {
	var resourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ads.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-DATERANGE:ID="only-ads",SCTE35-OUT=0xFC301B007E000000000000000A050000000100DF0000000000001AA436BF
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin"
#EXTINF:1,
ad.bin
#EXT-X-ENDLIST
`)
		case "/live.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-DATERANGE:ID="live-ad",SCTE35-OUT=0xFC301B007E000000000000000A050000000100DF0000000000001AA436BF
#EXTINF:1,
ad-live.bin
`)
		default:
			resourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "daterange-ads.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/ads.m3u8", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("ad-only error=%v", err)
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", resourceHits.Load())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err = NewDownloader(transport, Config{PollInterval: time.Second}).Download(
		ctx, server.URL+"/live.m3u8", root, filepath.Join(root, "daterange-cancel.bin"), false, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Download() error = %v, context = %v", err, ctx.Err())
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("cancelled daterange live fetched ad media: hits=%d", resourceHits.Load())
	}
}

func TestDownloadCueAllAdvertisementsAndCancellation(t *testing.T) {
	var resourceHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/ads.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-CUE-OUT:DURATION=3
#EXT-X-MAP:URI="ad-init.bin"
#EXT-X-KEY:METHOD=AES-128,URI="ad-key.bin"
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin"
#EXTINF:1,
ad.bin
#EXT-X-ENDLIST
`)
		case "/live.m3u8":
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-CUE-OUT
#EXTINF:1,
ad-live.bin
`)
		default:
			resourceHits.Add(1)
			_, _ = writer.Write([]byte("must-not-be-requested"))
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "cue-ads.bin")
	_, err := NewDownloader(transport, Config{}).Download(
		context.Background(), server.URL+"/ads.m3u8", root, destination, false, nil)
	if !errors.Is(err, fragment.ErrNoSegments) {
		t.Fatalf("ad-only error=%v", err)
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("ad resource hits=%d", resourceHits.Load())
	}
	for _, path := range []string{destination, destination + ".part", destination + ".fragments"} {
		if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("scratch path %q exists or returned unexpected error: %v", path, statErr)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	_, err = NewDownloader(transport, Config{PollInterval: time.Second}).Download(
		ctx, server.URL+"/live.m3u8", root, filepath.Join(root, "cue-cancel.bin"), false, nil)
	if err == nil || ctx.Err() == nil {
		t.Fatalf("Download() error = %v, context = %v", err, ctx.Err())
	}
	if resourceHits.Load() != 0 {
		t.Fatalf("cancelled cue live fetched ad media: hits=%d", resourceHits.Load())
	}
}

func TestDownloadLiveUsesBlockingReloadWithoutPrefetchingHint(t *testing.T) {
	var polls atomic.Int32
	var preloadHits atomic.Int32
	var sawDirective atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES
#EXT-X-PART:DURATION=0.5,URI="10.0.bin"
#EXT-X-PRELOAD-HINT:TYPE=PART,URI="10.1.bin"
`)
				return
			}
			if request.URL.Query().Get("_HLS_msn") != "10" || request.URL.Query().Get("_HLS_part") != "1" {
				http.Error(writer, "missing delivery directive", http.StatusBadRequest)
				return
			}
			sawDirective.Store(true)
			_, _ = fmt.Fprint(writer, `#EXTM3U
#EXT-X-MEDIA-SEQUENCE:10
#EXTINF:1,
10.bin
#EXT-X-ENDLIST
`)
		case "/10.bin":
			_, _ = writer.Write([]byte("complete"))
		case "/10.0.bin", "/10.1.bin":
			preloadHits.Add(1)
			_, _ = writer.Write([]byte("must-not-fetch"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "blocking.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8?token=secret", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "complete" || !sawDirective.Load() || preloadHits.Load() != 0 {
		t.Fatalf("contents=%q directive=%t preloadHits=%d err=%v", contents, sawDirective.Load(), preloadHits.Load(), err)
	}
}

func TestDownloadLiveBlockingReloadFallsBackOnce(t *testing.T) {
	var polls atomic.Int32
	var directiveAttempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/live.m3u8" {
			call := polls.Add(1)
			if call == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-SERVER-CONTROL:CAN-BLOCK-RELOAD=YES\n#EXTINF:1,\n1.bin\n")
				return
			}
			if request.URL.Query().Get("_HLS_msn") != "" {
				directiveAttempts.Add(1)
				http.Error(writer, "directives unsupported", http.StatusNotImplemented)
				return
			}
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXTINF:1,\n1.bin\n#EXTINF:1,\n2.bin\n#EXT-X-ENDLIST\n")
			return
		}
		if request.URL.Path == "/1.bin" {
			_, _ = writer.Write([]byte("one-"))
			return
		}
		if request.URL.Path == "/2.bin" {
			_, _ = writer.Write([]byte("two"))
			return
		}
		http.NotFound(writer, request)
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "fallback.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "one-two" || directiveAttempts.Load() != 1 {
		t.Fatalf("contents=%q attempts=%d err=%v", contents, directiveAttempts.Load(), err)
	}
}

func TestDownloadLiveSequenceResetUsesNewPhysicalEpoch(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:5\n#EXTINF:1,\nold.bin\n")
				return
			}
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\nnew.bin\n#EXT-X-ENDLIST\n")
		case "/old.bin":
			_, _ = writer.Write([]byte("old-"))
		case "/new.bin":
			_, _ = writer.Write([]byte("new"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "reset.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "old-new" {
		t.Fatalf("contents=%q err=%v", contents, err)
	}
}

func TestDownloadLiveOverlappingSequenceChangedURLStartsNewEpoch(t *testing.T) {
	var polls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\nold-0.bin\n")
				return
			}
			_, _ = fmt.Fprint(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:0\n#EXTINF:1,\nnew-0.bin\n#EXT-X-ENDLIST\n")
		case "/old-0.bin":
			_, _ = writer.Write([]byte("old-"))
		case "/new-0.bin":
			_, _ = writer.Write([]byte("new"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "overlap-reset.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "old-new" || polls.Load() != 2 {
		t.Fatalf("contents=%q polls=%d err=%v", contents, polls.Load(), err)
	}
}

func TestDownloadRefetchesKeyAfterSameURIKeyRotation(t *testing.T) {
	keyA := []byte("0123456789abcdef")
	keyB := []byte("abcdef0123456789")
	ivA := []byte("aaaaaaaaaaaaaaaa")
	ivB := []byte("bbbbbbbbbbbbbbbb")
	var keyHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%x\n#EXTINF:1,\none.bin\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%x\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n", ivA, ivB)
		case "/key.bin":
			if keyHits.Add(1) == 1 {
				_, _ = writer.Write(keyA)
			} else {
				_, _ = writer.Write(keyB)
			}
		case "/one.bin":
			_, _ = writer.Write(encryptSegment(t, []byte("one-"), keyA, ivA))
		case "/two.bin":
			_, _ = writer.Write(encryptSegment(t, []byte("two"), keyB, ivB))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "rotated-key.bin")
	_, err := NewDownloader(transport, Config{}).Download(context.Background(), server.URL+"/media.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "one-two" || keyHits.Load() != 2 {
		t.Fatalf("contents=%q keyHits=%d err=%v", contents, keyHits.Load(), err)
	}
}

func TestDownloadLiveRefetchesSameURIKeyAcrossSnapshots(t *testing.T) {
	keyA := []byte("0123456789abcdef")
	keyB := []byte("abcdef0123456789")
	ivA := []byte("aaaaaaaaaaaaaaaa")
	ivB := []byte("bbbbbbbbbbbbbbbb")
	var polls atomic.Int32
	var playlistEpoch atomic.Int32
	var keyHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/live.m3u8":
			if polls.Add(1) == 1 {
				playlistEpoch.Store(1)
				_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%x\n#EXTINF:1,\none.bin\n", ivA)
				return
			}
			playlistEpoch.Store(2)
			_, _ = fmt.Fprintf(writer, "#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%x\n#EXTINF:1,\none.bin\n#EXT-X-KEY:METHOD=AES-128,URI=key.bin,IV=0x%x\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n", ivA, ivB)
		case "/key.bin":
			keyHits.Add(1)
			if playlistEpoch.Load() == 1 {
				_, _ = writer.Write(keyA)
			} else {
				_, _ = writer.Write(keyB)
			}
		case "/one.bin":
			_, _ = writer.Write(encryptSegment(t, []byte("one-"), keyA, ivA))
		case "/two.bin":
			_, _ = writer.Write(encryptSegment(t, []byte("two"), keyB, ivB))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	destination := filepath.Join(root, "live-rotated-key.bin")
	_, err := NewDownloader(transport, Config{PollInterval: time.Millisecond, MaxPolls: 3}).Download(context.Background(), server.URL+"/live.m3u8", root, destination, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(destination)
	if err != nil || string(contents) != "one-two" || keyHits.Load() != 3 {
		t.Fatalf("contents=%q keyHits=%d err=%v", contents, keyHits.Load(), err)
	}
}

func encryptSegment(t *testing.T, plaintext, key, iv []byte) []byte {
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
