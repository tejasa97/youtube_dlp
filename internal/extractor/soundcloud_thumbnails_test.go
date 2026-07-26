package extractor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestSoundCloudArtworkThumbnailMatrix(t *testing.T) {
	transport := newSoundCloudThumbnailTransport(t)
	info := value.NewObject()
	if err := addSoundCloudThumbnails(
		context.Background(), transport, info, "4242",
		"https://i1.sndcdn.com/artworks-fixture-large.png?source=test", "https://i1.sndcdn.com/avatars-fixture-large.jpg",
	); err != nil {
		t.Fatal(err)
	}
	if thumbnail, _ := info.Lookup("thumbnail").StringValue(); thumbnail !=
		"https://i1.sndcdn.com/artworks-fixture-original.png?source=test" {
		t.Fatalf("thumbnail=%q", thumbnail)
	}
	assertSoundCloudThumbnailMatrix(t, info, true, "png")
	requests := transport.probeRequests()
	if len(requests) != 1 || requests[0].Method != http.MethodHead ||
		requests[0].URL.String() != "https://i1.sndcdn.com/artworks-fixture-original.png?source=test" {
		t.Fatalf("probe requests=%v", requestURLs(requests))
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if requests[0].Header.Get(header) != "" {
			t.Fatalf("probe leaked %s", header)
		}
	}
}

func TestSoundCloudArtworkOriginalFallbackAndAvatarDimensions(t *testing.T) {
	t.Run("flip original extension", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		transport.status = http.StatusNotFound
		info := value.NewObject()
		if err := addSoundCloudThumbnails(
			context.Background(), transport, info, "1",
			"https://a1.sndcdn.com/artworks-fixture-large.jpg", "",
		); err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, info, true, "png")
	})
	t.Run("avatar fallback", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		info := value.NewObject()
		if err := addSoundCloudThumbnails(
			context.Background(), transport, info, "1",
			"https://evil.example/artworks-fixture-large.jpg",
			"https://i1.sndcdn.com/avatars-fixture-large.jpg",
		); err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, info, false, "jpg")
	})
	t.Run("unavailable isolation keeps metadata extension", func(t *testing.T) {
		info := value.NewObject()
		if err := addSoundCloudThumbnails(
			context.Background(), newSoundCloudFixtureTransport(t), info, "1",
			"https://i1.sndcdn.com/artworks-fixture-large.png", "",
		); err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, info, true, "png")
	})
	t.Run("unavailable isolation preserves cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := addSoundCloudThumbnails(
			ctx, newSoundCloudFixtureTransport(t), value.NewObject(), "1",
			"https://i1.sndcdn.com/artworks-fixture-large.png", "",
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error=%v", err)
		}
	})
	t.Run("ordinary probe failure is nonfatal", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		transport.err = errors.New("offline token=must-not-leak")
		info := value.NewObject()
		if err := addSoundCloudThumbnails(
			context.Background(), transport, info, "1",
			"https://i1.sndcdn.com/artworks-fixture-large.jpg", "",
		); err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, info, true, "png")
	})
}

func TestSoundCloudArtworkNonmatchingAndUnsafeSources(t *testing.T) {
	t.Run("nonmatching CDN URL remains singleton", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		info := value.NewObject()
		rawURL := "https://i1.sndcdn.com/images/fixture.jpg"
		if err := addSoundCloudThumbnails(
			context.Background(), transport, info, "1", rawURL, "",
		); err != nil {
			t.Fatal(err)
		}
		thumbnails, _ := info.Lookup("thumbnails").ListValue()
		if len(thumbnails) != 1 || len(transport.probeRequests()) != 0 {
			t.Fatalf("thumbnails=%#v probes=%d", thumbnails, len(transport.probeRequests()))
		}
		object, _ := thumbnails[0].Object()
		if got, _ := object.Lookup("url").StringValue(); got != rawURL {
			t.Fatalf("singleton URL=%q", got)
		}
	})
	for _, rawURL := range []string{
		"http://i1.sndcdn.com/artworks-x-large.jpg",
		"https://i1.sndcdn.com.evil.test/artworks-x-large.jpg",
		"https://user@i1.sndcdn.com/artworks-x-large.jpg",
		"https://i1.sndcdn.com:443/artworks-x-large.jpg",
		"https://i1.sndcdn.com/artworks%2fx-large.jpg",
		"https://i1.sndcdn.com/artworks-x-large.jpg#fragment",
		"https://evil.test/artworks-x-large.jpg",
	} {
		t.Run(rawURL, func(t *testing.T) {
			transport := newSoundCloudThumbnailTransport(t)
			info := value.NewObject()
			if err := addSoundCloudThumbnails(
				context.Background(), transport, info, "1", rawURL, "",
			); err != nil {
				t.Fatal(err)
			}
			if _, ok := info.Lookup("thumbnail").StringValue(); ok {
				t.Fatalf("unsafe thumbnail retained: %q", rawURL)
			}
			if len(transport.probeRequests()) != 0 {
				t.Fatalf("unsafe thumbnail probed: %q", rawURL)
			}
		})
	}
}

func TestSoundCloudArtworkCancellation(t *testing.T) {
	transport := newSoundCloudThumbnailTransport(t)
	transport.block = true
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- addSoundCloudThumbnails(
			ctx, transport, value.NewObject(), "1",
			"https://i1.sndcdn.com/artworks-fixture-large.jpg", "",
		)
	}()
	<-transport.started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error=%v", err)
	}
}

func TestSoundCloudArtworkTrackAndPlaylistIntegration(t *testing.T) {
	t.Run("track", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://soundcloud.com/fixture-artist/synthetic-signal", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, result.Info.Fields(), true, "jpg")
		if len(transport.probeRequests()) != 1 {
			t.Fatalf("track probes=%d", len(transport.probeRequests()))
		}
	})
	t.Run("playlist", func(t *testing.T) {
		transport := newSoundCloudThumbnailTransport(t)
		transport.override = func(request *http.Request) (int, []byte, bool) {
			if request.URL.Path == "/playlists/55" {
				return http.StatusOK, []byte(`{
					"id":55,"title":"Synthetic Set",
					"artwork_url":"https://i1.sndcdn.com/artworks-set-large.png",
					"user":{"id":7,"username":"Fixture Artist","avatar_url":"https://i1.sndcdn.com/avatars-fixture-large.jpg"},
					"tracks":[{"id":100,"permalink_url":"https://soundcloud.com/fixture-artist/first"}]
				}`), true
			}
			return 0, nil, false
		}
		result, err := NewSoundCloud().Extract(context.Background(), Request{
			URL: "https://api.soundcloud.com/playlists/55", Transport: transport,
		})
		if err != nil {
			t.Fatal(err)
		}
		assertSoundCloudThumbnailMatrix(t, result.Info.Fields(), true, "png")
		if len(transport.probeRequests()) != 1 {
			t.Fatalf("playlist probes=%d", len(transport.probeRequests()))
		}
	})
}

func FuzzSoundCloudArtworkPlan(f *testing.F) {
	f.Add("https://i1.sndcdn.com/artworks-fixture-large.jpg")
	f.Add("https://a1.sndcdn.com/avatars-fixture-t500x500.png?source=test")
	f.Add("https://evil.test/artworks-fixture-large.jpg")
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > soundCloudMaxURLBytes+1 {
			t.Skip()
		}
		parsed, _, ok := parseSoundCloudArtworkURL(rawURL)
		if !ok {
			return
		}
		for _, variant := range soundCloudArtworkVariants {
			output := soundCloudArtworkVariantURL(parsed, variant.id, "jpg")
			outputURL, err := url.Parse(output)
			host := strings.ToLower(outputURL.Hostname())
			if err != nil || outputURL.Scheme != "https" ||
				(host != "i1.sndcdn.com" && host != "a1.sndcdn.com") ||
				outputURL.User != nil || outputURL.Port() != "" || outputURL.Fragment != "" ||
				len(output) > soundCloudMaxURLBytes {
				t.Fatalf("unsafe output=%q error=%v", output, err)
			}
			if !strings.HasSuffix(outputURL.Path, "-"+variant.id+".jpg") {
				t.Fatalf("wrong variant output=%q", output)
			}
		}
	})
}

func assertSoundCloudThumbnailMatrix(
	t *testing.T,
	info *value.Object,
	artwork bool,
	originalExtension string,
) {
	t.Helper()
	thumbnails, ok := info.Lookup("thumbnails").ListValue()
	if !ok || len(thumbnails) != len(soundCloudArtworkVariants) {
		t.Fatalf("thumbnails=%#v", thumbnails)
	}
	gotIDs := make([]string, 0, len(thumbnails))
	for index, item := range thumbnails {
		object, ok := item.Object()
		if !ok {
			t.Fatalf("thumbnail[%d]=%#v", index, item)
		}
		id, _ := object.Lookup("id").StringValue()
		gotIDs = append(gotIDs, id)
		rawURL, _ := object.Lookup("url").StringValue()
		extension := "jpg"
		if id == "original" {
			extension = originalExtension
		}
		if !strings.HasSuffix(strings.Split(rawURL, "?")[0], "-"+id+"."+extension) {
			t.Fatalf("thumbnail[%d] URL=%q", index, rawURL)
		}
		size := soundCloudArtworkVariants[index].size
		if id == "tiny" && !artwork {
			size = 18
		}
		width, widthOK := object.Lookup("width").Int()
		height, heightOK := object.Lookup("height").Int()
		if size == 0 {
			if widthOK || heightOK {
				t.Fatalf("original dimensions=%d,%d", width, height)
			}
			preference, ok := object.Lookup("preference").Int()
			if !ok || preference != 10 {
				t.Fatalf("original preference=%d ok=%v", preference, ok)
			}
		} else if !widthOK || !heightOK || width != size || height != size {
			t.Fatalf("thumbnail[%d] dimensions=%d,%d want=%d", index, width, height, size)
		}
	}
	wantIDs := []string{"mini", "tiny", "small", "badge", "t67x67", "large", "t300x300", "crop", "t500x500", "original"}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("IDs=%#v", gotIDs)
	}
	preferred, _ := thumbnails[len(thumbnails)-1].Object()
	preferredURL, _ := preferred.Lookup("url").StringValue()
	if singular, _ := info.Lookup("thumbnail").StringValue(); singular != preferredURL {
		t.Fatalf("singular thumbnail=%q preferred=%q", singular, preferredURL)
	}
}

type soundCloudThumbnailTransport struct {
	*soundCloudFixtureTransport

	mu       sync.Mutex
	requests []*http.Request
	status   int
	err      error
	block    bool
	started  chan struct{}
	once     sync.Once
}

func newSoundCloudThumbnailTransport(t *testing.T) *soundCloudThumbnailTransport {
	return &soundCloudThumbnailTransport{
		soundCloudFixtureTransport: newSoundCloudFixtureTransport(t),
		started:                    make(chan struct{}),
	}
}

func (transport *soundCloudThumbnailTransport) DoWithoutCredentialsNoRedirect(
	ctx context.Context,
	request *http.Request,
) (*http.Response, error) {
	if request.Method != http.MethodHead ||
		(request.URL.Hostname() != "i1.sndcdn.com" && request.URL.Hostname() != "a1.sndcdn.com") {
		return transport.soundCloudFixtureTransport.Do(ctx, request)
	}
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(header) != "" {
			transport.testingT.Fatalf("probe leaked %s", header)
		}
	}
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(ctx))
	transport.mu.Unlock()
	if transport.block {
		transport.once.Do(func() { close(transport.started) })
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(nil)),
		Request:    request,
	}, nil
}

func (transport *soundCloudThumbnailTransport) probeRequests() []*http.Request {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return append([]*http.Request(nil), transport.requests...)
}

func requestURLs(requests []*http.Request) []string {
	output := make([]string, len(requests))
	for index := range requests {
		output[index] = requests[index].URL.String()
	}
	return output
}
