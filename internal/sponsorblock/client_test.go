package sponsorblock

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestFetchDisabledProducesNoRequests(t *testing.T) {
	transport := &fakeResponseTransport{}
	result, err := Fetch(context.Background(), transport, Options{Enabled: false}, "YouTube", "abc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chapters) != 0 {
		t.Fatalf("got %d chapters, want 0", len(result.Chapters))
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.calls.Load())
	}
}

func TestFetchCanonicalRequestAndMatchingGroup(t *testing.T) {
	const videoID = "dQw4w9WgXcQ"
	sum := sha256.Sum256([]byte(videoID))
	prefix := hex.EncodeToString(sum[:])[:4]
	body := fmt.Sprintf(`[{"videoID":"other","segments":[{"segment":[0,5],"category":"sponsor","actionType":"skip","videoDuration":60}]},{"videoID":%q,"segments":[{"segment":[1,5],"category":"sponsor","actionType":"skip","videoDuration":60},{"segment":[10,20],"category":"intro","actionType":"skip","videoDuration":60}]}]`, videoID)
	transport := &fakeResponseTransport{body: strings.NewReader(body), fakeTransport: fakeTransport{status: 200, contentType: "application/json"}}
	result, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor", "intro"}}, "YouTube", videoID, 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chapters) != 2 {
		t.Fatalf("got %d chapters, want 2", len(result.Chapters))
	}
	if transport.method != http.MethodGet {
		t.Fatalf("method = %q, want GET", transport.method)
	}
	if !strings.Contains(transport.url, "/api/skipSegments/"+prefix) {
		t.Fatalf("url = %q, want contains %s", transport.url, prefix)
	}
	if !strings.Contains(transport.url, "service=YouTube") {
		t.Fatalf("url = %q, want contains service=YouTube", transport.url)
	}
	if !transport.noCookies {
		t.Fatal("Fetch did not use DoWithoutCredentials")
	}
	if transport.cookieValue != "" {
		t.Fatalf("Cookie header leaked: %q", transport.cookieValue)
	}
}

func TestFetchCookieIsolationRequired(t *testing.T) {
	// Transport without DoWithoutCredentials must fail closed.
	transport := &noIsolationTransport{}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if !errors.Is(err, ErrIsolation) {
		t.Fatalf("err = %v, want ErrIsolation", err)
	}
}

// noIsolationTransport intentionally lacks DoWithoutCredentials so the
// fail-closed branch in fetchBody triggers. The struct satisfies
// the Transport interface only at the language level; the runtime
// type assertion in fetchBody must reject it.
type noIsolationTransport struct{}

func (noIsolationTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func TestFetchNotFoundYieldsEmptyResult(t *testing.T) {
	transport := &fakeResponseTransport{body: strings.NewReader(""), fakeTransport: fakeTransport{status: 404}}
	result, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chapters) != 0 {
		t.Fatalf("got %d chapters, want 0", len(result.Chapters))
	}
}

func TestFetchRateLimit(t *testing.T) {
	transport := &fakeResponseTransport{body: strings.NewReader(""), fakeTransport: fakeTransport{status: 429}}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if !errors.Is(err, ErrNetwork) {
		t.Fatalf("err = %v, want ErrNetwork", err)
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %q, want contains rate limited", err.Error())
	}
}

func TestFetchServerError(t *testing.T) {
	for _, status := range []int{500, 502, 503} {
		transport := &fakeResponseTransport{body: strings.NewReader(""), fakeTransport: fakeTransport{status: status}}
		_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
		if !errors.Is(err, ErrNetwork) {
			t.Fatalf("status %d: err = %v, want ErrNetwork", status, err)
		}
	}
}

func TestFetchUnauthorized(t *testing.T) {
	transport := &fakeResponseTransport{body: strings.NewReader(""), fakeTransport: fakeTransport{status: 401}}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err = %v, want ErrAuthentication", err)
	}
}
