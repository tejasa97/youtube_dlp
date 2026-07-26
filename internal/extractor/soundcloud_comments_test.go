package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type soundCloudCommentFixtureTransport struct {
	*soundCloudFixtureTransport
	muComment sync.Mutex
	calls     []*http.Request
	status    int
	body      []byte
	block     <-chan struct{}
}

func newSoundCloudCommentFixtureTransport(t *testing.T) *soundCloudCommentFixtureTransport {
	return &soundCloudCommentFixtureTransport{soundCloudFixtureTransport: newSoundCloudFixtureTransport(t)}
}

func (transport *soundCloudCommentFixtureTransport) DoWithoutCredentialsNoRedirect(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	transport.muComment.Lock()
	transport.calls = append(transport.calls, request.Clone(ctx))
	transport.muComment.Unlock()
	if transport.block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-transport.block:
		}
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if request.Header.Get(key) != "" {
			transport.testingT.Fatalf("credential header %s forwarded", key)
		}
	}
	if request.URL.Hostname() != "api-v2.soundcloud.com" || !strings.HasSuffix(request.URL.Path, "/comments") {
		return transport.soundCloudFixtureTransport.Do(ctx, request)
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	body := transport.body
	if body == nil && status == http.StatusOK {
		name := "comments_page1.json"
		if request.URL.Query().Get("offset") == "20" {
			name = "comments_page2.json"
		}
		var err error
		body, err = os.ReadFile(filepath.Join("..", "..", "conformance", "extractors", "soundcloud", name))
		if err != nil {
			transport.testingT.Fatal(err)
		}
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    request,
	}, nil
}

func (transport *soundCloudCommentFixtureTransport) commentRequests() []*http.Request {
	transport.muComment.Lock()
	defer transport.muComment.Unlock()
	return append([]*http.Request(nil), transport.calls...)
}

func TestSoundCloudTrackCommentsAreDeferredOrderedAndNormalized(t *testing.T) {
	t.Parallel()
	transport := newSoundCloudCommentFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL:       "https://soundcloud.com/fixture-artist/synthetic-signal",
		Transport: transport,
		SoundCloudComments: SoundCloudCommentOptions{
			Enabled: true,
		},
	})
	if err != nil || result.Enrich == nil || len(transport.commentRequests()) != 0 {
		t.Fatalf("extraction=%#v err=%v comment requests=%d", result, err, len(transport.commentRequests()))
	}
	info := value.NewInfo(result.Info.Fields().Clone())
	if err := result.Enrich(context.Background(), &info); err != nil {
		t.Fatal(err)
	}
	requests := transport.commentRequests()
	if len(requests) != 2 {
		t.Fatalf("comment requests = %d", len(requests))
	}
	for index, request := range requests {
		if request.Method != http.MethodGet || request.URL.Hostname() != "api-v2.soundcloud.com" ||
			request.URL.Path != "/tracks/4242/comments" || request.URL.Query().Get("sort") != "newest" ||
			request.URL.Query().Get("limit") != "20" || request.URL.Query().Get("threaded") != "1" {
			t.Fatalf("request %d = %s", index, request.URL)
		}
	}
	comments, ok := info.Lookup("comments").ListValue()
	if !ok || len(comments) != 3 {
		t.Fatalf("comments = %#v", comments)
	}
	first, _ := comments[0].Object()
	if got, _ := first.Lookup("id").StringValue(); got != "9001" {
		t.Fatalf("first id = %q", got)
	}
	if got, _ := first.Lookup("author_id").StringValue(); got != "71" {
		t.Fatalf("author id = %q", got)
	}
	start, _ := first.Lookup("start_time").Float()
	end, _ := first.Lookup("end_time").Float()
	if start != 12.5 || end != start {
		t.Fatalf("comment times = %v/%v", start, end)
	}
	verified, ok := first.Lookup("author_is_verified").Bool()
	if !ok || !verified {
		t.Fatalf("verified = %v, %v", verified, ok)
	}
	third, _ := comments[2].Object()
	if _, ok := third.Lookup("author_thumbnail").StringValue(); ok {
		t.Fatal("unsafe thumbnail retained")
	}
	if count, ok := info.Lookup("comment_count").Int(); !ok || count != 3 {
		t.Fatalf("comment count = %d, %v", count, ok)
	}
}

func TestSoundCloudTrackCommentSortLimitsAndDisabled(t *testing.T) {
	t.Parallel()
	for _, sortMode := range []string{"newest", "oldest", "track-timestamp"} {
		t.Run(sortMode, func(t *testing.T) {
			transport := newSoundCloudCommentFixtureTransport(t)
			extractor := &SoundCloud{clientID: soundCloudFixtureClientID}
			comments, err := extractor.extractTrackComments(context.Background(), transport, "4242",
				normalizedSoundCloudCommentOptions{sort: sortMode, max: 1})
			if err != nil || len(comments) != 1 || len(transport.commentRequests()) != 1 {
				t.Fatalf("comments=%d requests=%d err=%v", len(comments), len(transport.commentRequests()), err)
			}
			if got := transport.commentRequests()[0].URL.Query().Get("sort"); got != sortMode {
				t.Fatalf("sort = %q", got)
			}
		})
	}
	if _, err := normalizeSoundCloudCommentOptions(SoundCloudCommentOptions{Enabled: true, Sort: "invalid"}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("invalid sort error = %v", err)
	}
	if _, err := normalizeSoundCloudCommentOptions(SoundCloudCommentOptions{Enabled: true, MaxComments: soundCloudCommentHardMax + 1}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("invalid limit error = %v", err)
	}
	transport := newSoundCloudCommentFixtureTransport(t)
	result, err := NewSoundCloud().Extract(context.Background(), Request{
		URL: "https://soundcloud.com/fixture-artist/synthetic-signal", Transport: transport,
	})
	if err != nil || result.Enrich != nil || len(transport.commentRequests()) != 0 {
		t.Fatalf("disabled extraction=%#v err=%v", result, err)
	}
}

func TestSoundCloudCommentIsolationFailuresAndCancellation(t *testing.T) {
	t.Parallel()
	extractor := &SoundCloud{clientID: soundCloudFixtureClientID}
	options := normalizedSoundCloudCommentOptions{sort: "newest", max: 10}
	if _, err := extractor.extractTrackComments(context.Background(), newSoundCloudFixtureTransport(t), "4242", options); !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("isolation error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	transport := newSoundCloudCommentFixtureTransport(t)
	if _, err := extractor.extractTrackComments(ctx, transport, "4242", options); !errors.Is(err, context.Canceled) ||
		len(transport.commentRequests()) != 0 {
		t.Fatalf("cancellation error=%v requests=%d", err, len(transport.commentRequests()))
	}
	for _, test := range []struct {
		name   string
		status int
		want   error
	}{
		{"not found", http.StatusNotFound, ErrUnavailable},
		{"gone", http.StatusGone, ErrUnavailable},
		{"rate limited", http.StatusTooManyRequests, ErrSoundCloudCommentsRateLimited},
		{"server", http.StatusInternalServerError, ErrSoundCloudCommentsNetwork},
		{"redirect", http.StatusFound, ErrSoundCloudCommentsNetwork},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudCommentFixtureTransport(t)
			transport.status = test.status
			transport.body = []byte("token=must-not-leak")
			_, err := extractor.extractTrackComments(context.Background(), transport, "4242", options)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
	t.Run("authentication retry", func(t *testing.T) {
		for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
			retrying := &SoundCloud{clientID: soundCloudFixtureClientID}
			transport := newSoundCloudCommentFixtureTransport(t)
			transport.status = status
			transport.body = []byte("token=must-not-leak")
			_, err := retrying.extractTrackComments(context.Background(), transport, "4242", options)
			if !errors.Is(err, ErrAuthentication) || len(transport.commentRequests()) != 4 ||
				strings.Contains(err.Error(), "must-not-leak") {
				t.Fatalf("status=%d retry error=%v calls=%d", status, err, len(transport.commentRequests()))
			}
			for _, request := range transport.commentRequests() {
				if request.Header.Get("Cookie") != "" || request.Header.Get("Authorization") != "" {
					t.Fatalf("status=%d refresh leaked credentials", status)
				}
			}
		}
	})
	t.Run("in-flight cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		block := make(chan struct{})
		transport := newSoundCloudCommentFixtureTransport(t)
		transport.block = block
		done := make(chan error, 1)
		go func() {
			_, err := extractor.extractTrackComments(ctx, transport, "4242", options)
			done <- err
		}()
		for len(transport.commentRequests()) == 0 {
			runtime.Gosched()
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("in-flight cancellation error = %v", err)
		}
	})
	for _, test := range []struct {
		name string
		body []byte
		want error
	}{
		{"malformed", []byte(`{"collection":`), ErrInvalidMetadata},
		{"trailing", []byte(`{"collection":[]} {}`), ErrInvalidMetadata},
		{"oversize", bytes.Repeat([]byte("x"), int(maxExtractorJSONBytes)+1), ErrJSONResponseTooLarge},
		{"repeated", []byte(`{"collection":[],"next_href":"https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=0&sort=newest&threaded=1"}`), ErrInvalidPlaylist},
		{"malformed optional field", []byte(`{"collection":[{"id":1,"body":"ok","user":{"username":42}}]}`), ErrInvalidMetadata},
	} {
		t.Run(test.name, func(t *testing.T) {
			transport := newSoundCloudCommentFixtureTransport(t)
			transport.body = test.body
			_, err := extractor.extractTrackComments(context.Background(), transport, "4242", options)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v; want %v", err, test.want)
			}
		})
	}
	t.Run("quoted ids are not integers", func(t *testing.T) {
		transport := newSoundCloudCommentFixtureTransport(t)
		transport.body = []byte(`{"collection":[
			{"id":"1","body":"skip","user":{"id":2}},
			{"id":3,"body":"keep","user":{"id":"4"}}
		]}`)
		comments, err := extractor.extractTrackComments(context.Background(), transport, "4242", options)
		if err != nil || len(comments) != 1 {
			t.Fatalf("comments=%d error=%v", len(comments), err)
		}
		comment, _ := comments[0].Object()
		if _, present := comment.Lookup("author_id").StringValue(); present {
			t.Fatal("quoted author id retained")
		}
	})
}

func TestSoundCloudCommentContinuationPolicy(t *testing.T) {
	t.Parallel()
	policy := soundCloudCommentContinuationPolicy{trackID: "4242", sort: "newest"}
	valid := "https://api-v2.soundcloud.com/tracks/4242/comments?client_id=stale&limit=20&offset=20&sort=newest&threaded=1"
	got, err := policy.validate(valid)
	if err != nil || strings.Contains(got, "client_id") {
		t.Fatalf("valid continuation = %q, %v", got, err)
	}
	mixedCase, err := policy.validate("https://Api-v2.SoundCloud.Com/tracks/4242/comments?limit=20&offset=20&sort=newest&threaded=1")
	if err != nil || mixedCase != got {
		t.Fatalf("mixed-case continuation = %q, %v; want %q", mixedCase, err, got)
	}
	for _, raw := range []string{
		"https://evil.invalid/tracks/4242/comments?limit=20&offset=20&sort=newest&threaded=1",
		"https://api-v2.soundcloud.com/tracks/9999/comments?limit=20&offset=20&sort=newest&threaded=1",
		"https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=20&sort=oldest&threaded=1",
		"https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=-1&sort=newest&threaded=1",
		"https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=020&sort=newest&threaded=1",
		"https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=20&sort=newest&threaded=1&pivot=x",
	} {
		if _, err := policy.validate(raw); !errors.Is(err, ErrInvalidPlaylist) {
			t.Errorf("validate(%q) error = %v", raw, err)
		}
	}
}

func FuzzSoundCloudCommentContinuationPolicy(f *testing.F) {
	f.Add("https://api-v2.soundcloud.com/tracks/4242/comments?limit=20&offset=20&sort=newest&threaded=1")
	f.Add("https://evil.invalid/tracks/4242/comments")
	policy := soundCloudCommentContinuationPolicy{trackID: "4242", sort: "newest"}
	f.Fuzz(func(t *testing.T, raw string) {
		canonical, err := policy.validate(raw)
		if err != nil {
			return
		}
		parsed, err := url.Parse(canonical)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Scheme != "https" || parsed.Hostname() != "api-v2.soundcloud.com" ||
			parsed.Path != "/tracks/4242/comments" || parsed.Query().Get("sort") != "newest" ||
			parsed.Query().Get("limit") != "20" || parsed.Query().Get("threaded") != "1" ||
			parsed.Query().Get("client_id") != "" {
			t.Fatalf("unsafe canonical continuation: %q", canonical)
		}
	})
}

func FuzzNormalizeSoundCloudComment(f *testing.F) {
	f.Add("1", "2", "body", "author", "https://i1.sndcdn.com/avatar.png", "https://soundcloud.com/author", "2024-01-02T03:04:05Z", "12500")
	f.Add("", "", "", "", "javascript:alert(1)", "https://evil.invalid/author", "invalid", "-1")
	f.Fuzz(func(t *testing.T, id, authorID, body, author, avatar, authorURL, createdAt, timestamp string) {
		row := soundCloudComment{
			ID:        soundCloudCommentID{value: soundCloudNumericID(id)},
			Body:      body,
			CreatedAt: createdAt,
			Timestamp: json.Number(timestamp),
		}
		row.User.ID = soundCloudCommentID{value: soundCloudNumericID(authorID)}
		row.User.Username = author
		row.User.AvatarURL = avatar
		row.User.PermalinkURL = authorURL
		normalized, ok, err := normalizeSoundCloudComment(row)
		if err != nil || !ok {
			return
		}
		gotID, present := normalized.Lookup("id").StringValue()
		if !present || soundCloudNumericID(gotID) == "" {
			t.Fatalf("unsafe normalized id %q", gotID)
		}
		if thumbnail, present := normalized.Lookup("author_thumbnail").StringValue(); present && !strictValidHostedHTTPURL(thumbnail) {
			t.Fatalf("unsafe thumbnail %q", thumbnail)
		}
		if profile, present := normalized.Lookup("author_url").StringValue(); present && !strictValidHostedHTTPURL(profile) {
			t.Fatalf("unsafe profile %q", profile)
		}
	})
}
