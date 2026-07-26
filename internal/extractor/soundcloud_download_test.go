package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
)

type soundCloudDownloadFixtureTransport struct {
	*soundCloudFixtureTransport
	mu       sync.Mutex
	headURLs []string
	head     func(*http.Request) (int, http.Header, error)
	ordinary func(context.Context, *http.Request) (*http.Response, error)
}

func (transport *soundCloudDownloadFixtureTransport) Do(
	ctx context.Context,
	request *http.Request,
) (*http.Response, error) {
	if transport.ordinary != nil {
		return transport.ordinary(ctx, request)
	}
	return transport.soundCloudFixtureTransport.Do(ctx, request)
}

func newSoundCloudDownloadFixtureTransport(t *testing.T) *soundCloudDownloadFixtureTransport {
	base := newSoundCloudFixtureTransport(t)
	base.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/tracks/4242/download" {
			return http.StatusOK, []byte(`{"redirectUri":"https://downloads.sndcdn.com/original/source?signature=secret"}`), true
		}
		return 0, nil, false
	}
	return &soundCloudDownloadFixtureTransport{
		soundCloudFixtureTransport: base,
		head: func(request *http.Request) (int, http.Header, error) {
			return http.StatusOK, http.Header{
				"Content-Disposition": {`attachment; filename="source.wav"`},
				"Content-Length":      {"12345"},
				"Content-Type":        {"audio/mpeg"},
			}, nil
		},
	}
}

func (transport *soundCloudDownloadFixtureTransport) DoWithoutCredentialsNoRedirect(
	ctx context.Context,
	request *http.Request,
) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(key) != "" {
			transport.testingT.Fatalf("credential header %s forwarded", key)
		}
	}
	if request.Method == http.MethodHead &&
		(request.URL.Hostname() == "i1.sndcdn.com" || request.URL.Hostname() == "a1.sndcdn.com") {
		return soundCloudResponse(http.StatusOK, nil), nil
	}
	transport.mu.Lock()
	transport.headURLs = append(transport.headURLs, request.URL.String())
	transport.mu.Unlock()
	status, headers, err := transport.head(request)
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Request:    request,
	}, nil
}

func (transport *soundCloudDownloadFixtureTransport) ordinaryURLs() []string {
	transport.soundCloudFixtureTransport.mu.Lock()
	defer transport.soundCloudFixtureTransport.mu.Unlock()
	return append([]string(nil), transport.soundCloudFixtureTransport.requests...)
}

func TestSoundCloudOriginalDownloadIsFirstAndBounded(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudDownloadFixtureTransport(t)
	var track soundCloudTrack
	if err := json.Unmarshal(transport.fixture["track.json"], &track); err != nil {
		t.Fatal(err)
	}
	track.Downloadable = true
	track.HasDownloadsLeft = true
	extraction, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
		context.Background(), transport, track, "s-private", SoundCloudCommentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	formats, ok := extraction.Info.Formats()
	if !ok || len(formats) != 3 {
		t.Fatalf("formats = %d, %v", len(formats), ok)
	}
	original, _ := formats[0].Object()
	for key, want := range map[string]string{
		"format_id": "download", "ext": "wav", "format_note": "Original",
		"vcodec": "none", "protocol": "http",
	} {
		if got, _ := original.Lookup(key).StringValue(); got != want {
			t.Fatalf("%s = %q; want %q", key, got, want)
		}
	}
	if quality, ok := original.Lookup("quality").Int(); !ok || quality != 10 {
		t.Fatalf("quality = %d, %v", quality, ok)
	}
	if size, ok := original.Lookup("filesize").Int(); !ok || size != 12345 {
		t.Fatalf("filesize = %d, %v", size, ok)
	}
	requests := transport.ordinaryURLs()
	var downloadURL string
	for _, raw := range requests {
		if strings.Contains(raw, "/tracks/4242/download") {
			downloadURL = raw
		}
	}
	parsed, err := url.Parse(downloadURL)
	if err != nil || parsed.Query().Get("secret_token") != "s-private" ||
		parsed.Query().Get("client_id") != soundCloudFixtureClientID {
		t.Fatalf("download URL = %q", downloadURL)
	}
}

func TestSoundCloudOriginalDownloadFlagsAndDeduplication(t *testing.T) {
	t.Parallel()
	for _, flags := range [][2]bool{{false, false}, {true, false}, {false, true}} {
		transport := newSoundCloudDownloadFixtureTransport(t)
		var track soundCloudTrack
		if err := json.Unmarshal(transport.fixture["track.json"], &track); err != nil {
			t.Fatal(err)
		}
		track.Downloadable, track.HasDownloadsLeft = flags[0], flags[1]
		if _, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
			context.Background(), transport, track, "", SoundCloudCommentOptions{}); err != nil {
			t.Fatal(err)
		}
		for _, raw := range transport.ordinaryURLs() {
			if strings.Contains(raw, "/download") {
				t.Fatalf("flags=%v requested download: %s", flags, raw)
			}
		}
	}

	transport := newSoundCloudDownloadFixtureTransport(t)
	transport.soundCloudFixtureTransport.override = func(request *http.Request) (int, []byte, bool) {
		switch request.URL.Path {
		case "/tracks/4242/download":
			return http.StatusOK, []byte(`{"redirectUri":"https://cdn.sndcdn.com/same.mp3"}`), true
		case "/media/4242/progressive":
			return http.StatusOK, []byte(`{"url":"https://cdn.sndcdn.com/same.mp3"}`), true
		}
		return 0, nil, false
	}
	var track soundCloudTrack
	if err := json.Unmarshal(transport.fixture["track.json"], &track); err != nil {
		t.Fatal(err)
	}
	track.Downloadable, track.HasDownloadsLeft = true, true
	extraction, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
		context.Background(), transport, track, "", SoundCloudCommentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := extraction.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("deduplicated formats = %d", len(formats))
	}
}

func TestSoundCloudOriginalDownloadAPIFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, nil, nil},
		{"forbidden", http.StatusForbidden, nil, nil},
		{"rate limited", http.StatusTooManyRequests, nil, ErrSoundCloudOriginalRateLimited},
		{"malformed", http.StatusOK, []byte(`{"redirectUri":`), nil},
		{"oversized", http.StatusOK, bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1), nil},
		{"server", http.StatusInternalServerError, nil, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudDownloadFixtureTransport(t)
			transport.soundCloudFixtureTransport.override = func(request *http.Request) (int, []byte, bool) {
				if request.URL.Path == "/tracks/4242/download" {
					return test.status, test.body, true
				}
				return 0, nil, false
			}
			format, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).resolveOriginalDownload(
				context.Background(), transport, "4242", "s-secret")
			if !errors.Is(err, test.want) || format != nil || (err != nil && strings.Contains(err.Error(), "s-secret")) {
				t.Fatalf("format=%#v error=%v; want %v", format, err, test.want)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := newSoundCloudDownloadFixtureTransport(t)
	if _, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).resolveOriginalDownload(ctx, transport, "4242", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	transport = newSoundCloudDownloadFixtureTransport(t)
	transport.ordinary = func(ctx context.Context, _ *http.Request) (*http.Response, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).resolveOriginalDownload(ctx, transport, "4242", "")
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight API cancellation = %v", err)
	}
}

func TestSoundCloudOriginalRedirectSecurityAndFailures(t *testing.T) {
	t.Parallel()
	if _, _, err := resolveSoundCloudOriginalRedirect(context.Background(), newSoundCloudFixtureTransport(t),
		"https://downloads.sndcdn.com/file"); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("isolation = %v", err)
	}
	for _, raw := range []string{
		"http://downloads.sndcdn.com/file",
		"https://user@downloads.sndcdn.com/file",
		"https://127.0.0.1/file",
		"https://downloads.sndcdn.com:443/file",
		"https://downloads.sndcdn.com/a%2fb",
	} {
		transport := newSoundCloudDownloadFixtureTransport(t)
		if _, _, err := resolveSoundCloudOriginalRedirect(context.Background(), transport, raw); !errors.Is(err, ErrInvalidMetadata) ||
			len(transport.headURLs) != 0 {
			t.Fatalf("URL=%q calls=%d error=%v", raw, len(transport.headURLs), err)
		}
	}
	transport := newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(request *http.Request) (int, http.Header, error) {
		return http.StatusFound, http.Header{"Location": {request.URL.String()}}, nil
	}
	if _, _, err := resolveSoundCloudOriginalRedirect(context.Background(), transport,
		"https://downloads.sndcdn.com/file"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("loop error = %v", err)
	}
	transport = newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(*http.Request) (int, http.Header, error) {
		return http.StatusFound, http.Header{"Location": {"http://evil.invalid/file"}}, nil
	}
	if _, _, err := resolveSoundCloudOriginalRedirect(context.Background(), transport,
		"https://downloads.sndcdn.com/file"); !errors.Is(err, ErrInvalidMetadata) || len(transport.headURLs) != 1 {
		t.Fatalf("unsafe redirect calls=%d error=%v", len(transport.headURLs), err)
	}
	transport = newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(request *http.Request) (int, http.Header, error) {
		next := request.URL.Query().Get("hop") + "x"
		return http.StatusFound, http.Header{"Location": {"https://downloads.sndcdn.com/file?hop=" + next}}, nil
	}
	if _, _, err := resolveSoundCloudOriginalRedirect(context.Background(), transport,
		"https://downloads.sndcdn.com/file?hop=x"); !errors.Is(err, ErrInvalidMetadata) ||
		len(transport.headURLs) != soundCloudOriginalMaxRedirects+1 {
		t.Fatalf("hop limit calls=%d error=%v", len(transport.headURLs), err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	transport = newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(request *http.Request) (int, http.Header, error) {
		close(started)
		<-request.Context().Done()
		return 0, nil, request.Context().Err()
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := resolveSoundCloudOriginalRedirect(ctx, transport, "https://downloads.sndcdn.com/file")
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("in-flight cancellation = %v", err)
	}
}

func TestSoundCloudOriginalExtensionPrecedence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		headers http.Header
		want    string
	}{
		{http.Header{"Content-Disposition": {`attachment; filename="track.FLAC"`}, "Content-Type": {"audio/mpeg"}}, "flac"},
		{http.Header{"X-Amz-Meta-Name": {"track.wav"}, "Content-Type": {"audio/mpeg"}}, "wav"},
		{http.Header{"X-Amz-Meta-File-Type": {"aiff"}, "Content-Type": {"audio/mpeg"}}, "aiff"},
		{http.Header{"Content-Type": {"audio/mp4; charset=binary"}}, "m4a"},
		{http.Header{"Content-Type": {"audio/aac"}}, "aac"},
		{http.Header{"Content-Type": {"audio/webm"}}, "webm"},
		{http.Header{"Content-Type": {"audio/midi"}}, "mid"},
		{http.Header{"Content-Type": {"audio/x-realaudio"}}, "ra"},
		{http.Header{"Content-Type": {"application/octet-stream"}}, ""},
	} {
		if got := soundCloudOriginalExtension(test.headers); got != test.want {
			t.Errorf("extension(%v) = %q; want %q", test.headers, got, test.want)
		}
	}
}

func TestSoundCloudOriginalOptionalHeadFailuresKeepStreamingFormats(t *testing.T) {
	t.Parallel()
	for _, setup := range []func(*soundCloudDownloadFixtureTransport){
		func(transport *soundCloudDownloadFixtureTransport) {
			transport.head = func(*http.Request) (int, http.Header, error) {
				return http.StatusFound, http.Header{"Location": {"http://unsafe.invalid/file"}}, nil
			}
		},
		func(transport *soundCloudDownloadFixtureTransport) {
			transport.head = func(request *http.Request) (int, http.Header, error) {
				return http.StatusFound, http.Header{"Location": {request.URL.String()}}, nil
			}
		},
		func(transport *soundCloudDownloadFixtureTransport) {
			transport.head = func(*http.Request) (int, http.Header, error) {
				return http.StatusInternalServerError, make(http.Header), nil
			}
		},
	} {
		transport := newSoundCloudDownloadFixtureTransport(t)
		setup(transport)
		var track soundCloudTrack
		if err := json.Unmarshal(transport.fixture["track.json"], &track); err != nil {
			t.Fatal(err)
		}
		track.Downloadable, track.HasDownloadsLeft = true, true
		extraction, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
			context.Background(), transport, track, "", SoundCloudCommentOptions{})
		if err != nil {
			t.Fatal(err)
		}
		formats, _ := extraction.Info.Formats()
		if len(formats) != 2 {
			t.Fatalf("streaming formats = %d", len(formats))
		}
	}
	base := newSoundCloudFixtureTransport(t)
	base.override = func(request *http.Request) (int, []byte, bool) {
		if request.URL.Path == "/tracks/4242/download" {
			return http.StatusOK, []byte(`{"redirectUri":"https://downloads.sndcdn.com/file.wav"}`), true
		}
		return 0, nil, false
	}
	var track soundCloudTrack
	if err := json.Unmarshal(base.fixture["track.json"], &track); err != nil {
		t.Fatal(err)
	}
	track.Downloadable, track.HasDownloadsLeft = true, true
	extraction, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
		context.Background(), base, track, "", SoundCloudCommentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := extraction.Info.Formats()
	if len(formats) != 2 {
		t.Fatalf("isolation fallback streaming formats = %d", len(formats))
	}
}

func TestSoundCloudOriginalUnknownExtensionIsRetained(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(*http.Request) (int, http.Header, error) {
		return http.StatusOK, http.Header{"Content-Type": {"application/octet-stream"}}, nil
	}
	var track soundCloudTrack
	if err := json.Unmarshal(transport.fixture["track.json"], &track); err != nil {
		t.Fatal(err)
	}
	track.Downloadable, track.HasDownloadsLeft = true, true
	extraction, err := (&SoundCloud{clientID: soundCloudFixtureClientID}).normalizeTrack(
		context.Background(), transport, track, "", SoundCloudCommentOptions{})
	if err != nil {
		t.Fatal(err)
	}
	formats, _ := extraction.Info.Formats()
	if len(formats) != 3 {
		t.Fatalf("formats = %d", len(formats))
	}
	original, _ := formats[0].Object()
	if _, present := original.Lookup("ext").StringValue(); present {
		t.Fatal("unknown extension was fabricated")
	}
	if extension, _ := extraction.Info.Extension(); extension == "" {
		t.Fatal("top-level extension did not fall through to streaming format")
	}
}

func TestSoundCloudOriginalRelativeMultiHopRedirect(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudDownloadFixtureTransport(t)
	transport.head = func(request *http.Request) (int, http.Header, error) {
		switch request.URL.Path {
		case "/start":
			return http.StatusFound, http.Header{"Location": {"/middle?token=x"}}, nil
		case "/middle":
			return http.StatusTemporaryRedirect, http.Header{"Location": {"final.wav?token=y"}}, nil
		default:
			return http.StatusOK, http.Header{"Content-Type": {"audio/wav"}}, nil
		}
	}
	finalURL, headers, err := resolveSoundCloudOriginalRedirect(
		context.Background(), transport, "https://downloads.sndcdn.com/start")
	if err != nil || finalURL != "https://downloads.sndcdn.com/final.wav?token=y" ||
		headers.Get("Content-Type") != "audio/wav" || len(transport.headURLs) != 3 {
		t.Fatalf("final=%q headers=%v calls=%v error=%v", finalURL, headers, transport.headURLs, err)
	}
}

func TestSoundCloudOriginalPreservesSignedPathEncoding(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudDownloadFixtureTransport(t)
	rawURL := "https://downloads.sndcdn.com/a%2Bb/file%7E.wav?signature=opaque"
	finalURL, _, err := resolveSoundCloudOriginalRedirect(context.Background(), transport, rawURL)
	if err != nil || finalURL != rawURL || len(transport.headURLs) != 1 || transport.headURLs[0] != rawURL {
		t.Fatalf("final=%q calls=%v error=%v", finalURL, transport.headURLs, err)
	}
}

func FuzzSoundCloudOriginalURL(f *testing.F) {
	f.Add("https://downloads.sndcdn.com/original/file.wav?signature=x")
	f.Add("http://127.0.0.1/file")
	f.Fuzz(func(t *testing.T, raw string) {
		parsed, err := validateSoundCloudOriginalURL(raw)
		if err != nil {
			return
		}
		if parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
			parsed.Hostname() != strings.ToLower(parsed.Hostname()) || !strictValidHostedHTTPURL(parsed.String()) {
			t.Fatalf("unsafe URL accepted: %q", parsed)
		}
	})
}

func FuzzSoundCloudOriginalExtension(f *testing.F) {
	f.Add(`attachment; filename="track.flac"`, "track.wav", "aiff", "audio/mpeg")
	f.Fuzz(func(t *testing.T, disposition, name, fileType, contentType string) {
		extension := soundCloudOriginalExtension(http.Header{
			"Content-Disposition":  {disposition},
			"X-Amz-Meta-Name":      {name},
			"X-Amz-Meta-File-Type": {fileType},
			"Content-Type":         {contentType},
		})
		if len(extension) > 16 || strings.ContainsAny(extension, "/\\.\x00") {
			t.Fatalf("unsafe extension %q", extension)
		}
	})
}
