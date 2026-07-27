package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newUnlistedAuthenticatedFixture(t testing.TB, cookies []*http.Cookie) *vimeoAuthenticatedViewerFixtureTransport {
	t.Helper()
	transport := newVimeoAuthenticatedViewerFixtureTransport(cookies)
	transport.allowVideoAPI = true
	transport.configBody = readVimeoFixture(t, "video-config-private.json")
	return transport
}

func TestVimeoUnlistedRouteAcceptsCanonicalForm(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want bool
	}{
		{"numeric only", "https://vimeo.com/123456789", false},
		{"canonical unlisted", "https://vimeo.com/123456789/abcdef1234", true},
		{"www host", "https://www.vimeo.com/123456789/abcdef1234", true},
		{"uppercase hash rejected", "https://vimeo.com/123456789/ABCDEF1234", false},
		{"nine char hash", "https://vimeo.com/123456789/abcdef123", false},
		{"eleven char hash", "https://vimeo.com/123456789/abcdef12345", false},
		{"non-hex hash", "https://vimeo.com/123456789/zzzzzzzzzz", false},
		{"video prefix", "https://vimeo.com/video/abcdef1234", false},
		{"extra segment", "https://vimeo.com/123456789/abcdef1234/extra", false},
		{"query safely stripped", "https://vimeo.com/123456789/abcdef1234?caller_token=never-forward", true},
		{"fragment safely stripped", "https://vimeo.com/123456789/abcdef1234#player=1", true},
		{"empty query safely stripped", "https://vimeo.com/123456789/abcdef1234?", true},
		{"player host", "https://player.vimeo.com/123456789/abcdef1234", false},
		{"non-https", "http://vimeo.com/123456789/abcdef1234", false},
		{"userinfo", "https://user:vimeo.com@evil.example/123456789/abcdef1234", false},
		{"port", "https://vimeo.com:443/123456789/abcdef1234", false},
		{"alternate host", "https://vimeo.example/123456789/abcdef1234", false},
		{"non-numeric id", "https://vimeo.com/abcdef/abcdef1234", false},
		{"encoded slash", "https://vimeo.com/123456789/abcdef%2f1234", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			route, target, ok := classifyUnlistedForTest(tc.url)
			if ok != tc.want {
				t.Fatalf("ok=%v want=%v", ok, tc.want)
			}
			if ok {
				if route != vimeoRouteVideo {
					t.Fatalf("route=%v want %v", route, vimeoRouteVideo)
				}
				if target.unlistedHash != "abcdef1234" || target.id != "123456789" {
					t.Fatalf("target=%+v", target)
				}
				if target.canonical != "https://vimeo.com/123456789/abcdef1234" {
					t.Fatalf("canonical=%q", target.canonical)
				}
			}
		})
	}
}

// classifyUnlistedForTest routes the URL through the production classifier
// and returns only the unlisted branch decision. Used to keep the route
// table tests independent of the public/config dispatch.
func classifyUnlistedForTest(rawURL string) (vimeoRouteKind, vimeoPlaylistTarget, bool) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return vimeoRouteNone, vimeoPlaylistTarget{}, false
	}
	if target, ok := classifyVimeoUnlistedURL(parsed); ok {
		return vimeoRouteVideo, target, true
	}
	return vimeoRouteNone, vimeoPlaylistTarget{}, false
}

func TestVimeoUnlistedExtractionFailsWithoutVimeoCookie(t *testing.T) {
	transport := newUnlistedAuthenticatedFixture(t, nil)
	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if err == nil {
		t.Fatal("expected error without vimeo cookie")
	}
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v want ErrAuthentication", err)
	}
	if got := transport.viewerCalls; got != 0 {
		t.Fatalf("viewer calls=%d want 0", got)
	}
}

func TestVimeoUnlistedExtractionFailsWithoutScopedCapability(t *testing.T) {
	plain := newPlainTransport()
	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: plain,
	})
	if err == nil {
		t.Fatal("expected error without scoped authorization")
	}
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v want ErrAuthentication", err)
	}
}

// newPlainTransport returns a Transport that lacks Cookies, DoNoRedirect,
// and DoWithScopedAuthorizationNoRedirect so the constructor rejects it
// closed without any network attempt.
func newPlainTransport() Transport {
	return capabilityStub{}
}

type capabilityStub struct{}

func (capabilityStub) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("network forbidden in capability stub")
}
func (capabilityStub) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("network forbidden in capability stub")
}

// transportStub returns a minimal Transport that records nothing and never
// reaches the network. Used to exercise capability rejection.
func transportStub() Transport {
	return stubTransport{}
}

type stubTransport struct{}

func (stubTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("network forbidden in stub")
}
func (stubTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("network forbidden in stub")
}

func TestVimeoUnlistedExtractionSuccess(t *testing.T) {
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = readVimeoFixture(t, "video-viewer.json")
	transport.scopedBodies = [][]byte{
		readVimeoFixture(t, "video-api-private.json"),
		readVimeoFixture(t, "video-source-privacy.json"),
		readVimeoFixture(t, "video-source-download.json"),
	}
	transport.scopedStatuses = []int{http.StatusOK, http.StatusOK, http.StatusOK}

	extraction, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234?caller_token=never-forward#player=1",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if id, _ := extraction.Info.Lookup("id").StringValue(); id != "123456789" {
		t.Fatalf("id=%q want 123456789", id)
	}
	if wp, _ := extraction.Info.Lookup("webpage_url").StringValue(); wp != "https://vimeo.com/123456789/abcdef1234" {
		t.Fatalf("webpage_url=%q", wp)
	}
	for key, want := range map[string]string{
		"description": "Synthetic authenticated API description",
		"license":     "by-sa",
	} {
		if got, _ := extraction.Info.Lookup(key).StringValue(); got != want {
			t.Fatalf("%s=%q want %q", key, got, want)
		}
	}
	for key, want := range map[string]int64{
		"timestamp":         1704164645,
		"release_timestamp": 1706933106,
		"view_count":        321,
		"comment_count":     7,
		"like_count":        19,
	} {
		if got, ok := extraction.Info.Lookup(key).Int(); !ok || got != want {
			t.Fatalf("%s=%d ok=%v want %d", key, got, ok, want)
		}
	}
	formats, ok := extraction.Info.Formats()
	if !ok || len(formats) != 4 {
		t.Fatalf("formats=%d ok=%v want 4", len(formats), ok)
	}
	source, ok := formats[len(formats)-1].Object()
	if !ok {
		t.Fatal("source format is not an object")
	}
	if formatID, _ := source.Lookup("format_id").StringValue(); formatID != "Original" {
		t.Fatalf("source format_id=%q", formatID)
	}
	if extension, _ := source.Lookup("ext").StringValue(); extension != "mov" {
		t.Fatalf("source ext=%q want mov", extension)
	}
	if quality, _ := source.Lookup("quality").Int(); quality != 1 {
		t.Fatalf("source quality=%d want 1", quality)
	}
	if size, _ := source.Lookup("filesize").Int(); size != 987654321 {
		t.Fatalf("source filesize=%d", size)
	}
	if transport.viewerCalls != 1 {
		t.Fatalf("viewer calls=%d want 1", transport.viewerCalls)
	}
	if transport.scopedCalls != 3 {
		t.Fatalf("scoped API calls=%d want 3", transport.scopedCalls)
	}
	wantFields := []string{
		"config_url,uri,created_time,description,license,metadata.connections.comments.total,metadata.connections.likes.total,release_time,stats.plays",
		"privacy",
		"download",
	}
	for index, request := range transport.scopedRequests {
		if request.URL.Scheme != "https" || request.URL.Host != "api.vimeo.com" ||
			request.URL.Path != "/videos/123456789:abcdef1234" ||
			request.URL.Query().Get("fields") != wantFields[index] {
			t.Fatalf("scoped request %d=%s", index, request.URL)
		}
		for _, forbidden := range []string{"Cookie", "Proxy-Authorization"} {
			if request.Header.Get(forbidden) != "" {
				t.Fatalf("%s leaked on scoped request %d", forbidden, index)
			}
		}
	}
	if transport.credentialIsolatedCalls < 1 {
		t.Fatalf("credential-isolated calls=%d want >=1", transport.credentialIsolatedCalls)
	}
	firstIsolated := transport.isolatedRequestURLs[0]
	if !strings.HasPrefix(firstIsolated, "https://player.vimeo.com/video/123456789/config") {
		t.Fatalf("first isolated url=%q", firstIsolated)
	}
	if referer := transport.isolatedRequests[0].Header.Get("Referer"); referer != "https://vimeo.com/123456789/abcdef1234" {
		t.Fatalf("config Referer=%q", referer)
	}
}

func TestVimeoUnlistedAPI404WithErrorCode5460MapsToAuthentication(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "unlisted-5460")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{readVimeoFixture(t, "video-api-5460.json")}
	transport.scopedStatuses = []int{http.StatusNotFound}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v want ErrAuthentication", err)
	}
	if transport.viewerCalls != 1 {
		t.Fatalf("viewer calls=%d want 1", transport.viewerCalls)
	}
	if transport.credentialIsolatedCalls != 0 {
		t.Fatalf("credential-isolated calls=%d want 0", transport.credentialIsolatedCalls)
	}
}

func TestVimeoUnlistedAPI404Without5460MapsToUnavailable(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "unlisted-notfound")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{[]byte(`{"error_code":9999}`)}
	transport.scopedStatuses = []int{http.StatusNotFound}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v want ErrUnavailable", err)
	}
}

func TestVimeoUnlistedAPI410MapsToUnavailable(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "unlisted-gone")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{[]byte(`{}`)}
	transport.scopedStatuses = []int{http.StatusGone}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v want ErrUnavailable", err)
	}
}

func TestVimeoUnlistedAPI400MapsToAuthenticationWithoutPasswordPost(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "unlisted-400")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{[]byte(`{"error_code":2204}`)}
	transport.scopedStatuses = []int{http.StatusBadRequest}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:           "https://vimeo.com/123456789/abcdef1234",
		Transport:     transport,
		VideoPassword: "synthetic-password",
	})
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v want ErrAuthentication", err)
	}
	// The error string must never leak the password or the JWT signature.
	if strings.Contains(err.Error(), "synthetic-password") {
		t.Fatalf("error leaked password: %v", err)
	}
	if strings.Contains(err.Error(), "unlisted-400") {
		t.Fatalf("error leaked JWT signature: %v", err)
	}
}

func TestVimeoUnlistedAPI401RefreshesAndSucceeds(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	firstToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "first-token")
	secondToken := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "second-token")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	// Two viewer responses: first and refreshed JWT.
	transport.viewers = [][]byte{
		[]byte(`{"jwt":"` + firstToken + `"}`),
		[]byte(`{"jwt":"` + secondToken + `"}`),
	}
	// Two API responses: first 401, then 200 with config_url.
	transport.scopedBodies = [][]byte{
		[]byte(`{"error_code":401}`),
		[]byte(`{"uri":"/videos/123456789","config_url":"https://player.vimeo.com/video/123456789/config"}`),
	}
	transport.scopedStatuses = []int{http.StatusUnauthorized, http.StatusOK}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if transport.viewerCalls != 2 {
		t.Fatalf("viewer calls=%d want 2", transport.viewerCalls)
	}
	if transport.scopedCalls != 3 {
		t.Fatalf("scoped calls=%d want 3", transport.scopedCalls)
	}
	if transport.credentialIsolatedCalls < 1 {
		t.Fatalf("credential-isolated calls=%d want >=1", transport.credentialIsolatedCalls)
	}
}

func TestVimeoUnlistedPersistentAuthorizationFailureMapsToAuthentication(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			base := time.Now().Truncate(time.Second)
			transport := newUnlistedAuthenticatedFixture(t, []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}})
			transport.viewers = [][]byte{
				[]byte(`{"jwt":"` + vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "persistent-first") + `"}`),
				[]byte(`{"jwt":"` + vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "persistent-second") + `"}`),
			}
			transport.scopedBodies = [][]byte{[]byte(`{}`), []byte(`{}`)}
			transport.scopedStatuses = []int{status, status}

			_, err := Vimeo{}.Extract(context.Background(), Request{
				URL:       "https://vimeo.com/123456789/abcdef1234",
				Transport: transport,
			})
			if !errors.Is(err, ErrAuthentication) {
				t.Fatalf("err=%v want ErrAuthentication", err)
			}
			if transport.viewerCalls != 2 || transport.scopedCalls != 2 {
				t.Fatalf("viewer calls=%d scoped calls=%d want 2 each", transport.viewerCalls, transport.scopedCalls)
			}
			if transport.credentialIsolatedCalls != 0 {
				t.Fatalf("config fetched after persistent %d", status)
			}
		})
	}
}

func TestVimeoUnlistedRejectsAPIAndConfigIdentityMismatchBeforeMedia(t *testing.T) {
	t.Run("API URI", func(t *testing.T) {
		transport := newUnlistedAuthenticatedFixture(t, []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}})
		transport.viewerPayload = readVimeoFixture(t, "video-viewer.json")
		transport.scopedBodies = [][]byte{[]byte(`{"uri":"/videos/987654321","config_url":"https://player.vimeo.com/video/123456789/config"}`)}

		extraction, err := Vimeo{}.Extract(context.Background(), Request{
			URL:       "https://vimeo.com/123456789/abcdef1234",
			Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v want ErrInvalidMetadata", err)
		}
		if !extraction.Info.Lookup("formats").IsMissing() || transport.credentialIsolatedCalls != 0 {
			t.Fatalf("media/config emitted after API identity mismatch")
		}
	})

	t.Run("player config video.id", func(t *testing.T) {
		transport := newUnlistedAuthenticatedFixture(t, []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}})
		transport.viewerPayload = readVimeoFixture(t, "video-viewer.json")
		transport.scopedBodies = [][]byte{readVimeoFixture(t, "video-api-private.json")}
		transport.configBody = bytes.Replace(
			readVimeoFixture(t, "video-config-private.json"),
			[]byte(`"id": 123456789`), []byte(`"id": 987654321`), 1)

		extraction, err := Vimeo{}.Extract(context.Background(), Request{
			URL:       "https://vimeo.com/123456789/abcdef1234",
			Transport: transport,
		})
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("err=%v want ErrInvalidMetadata", err)
		}
		if !extraction.Info.Lookup("formats").IsMissing() || transport.scopedCalls != 1 || transport.credentialIsolatedCalls != 1 {
			t.Fatalf("media/source emitted after config identity mismatch: scoped=%d config=%d",
				transport.scopedCalls, transport.credentialIsolatedCalls)
		}
	})
}

func TestVimeoUnlistedMissingConfigURLMapsToUnavailable(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), "no-config")
	cookies := []*http.Cookie{{Name: "vimeo", Value: "synthetic-cookie"}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{[]byte(`{"uri":"/videos/123456789"}`)}
	transport.scopedStatuses = []int{http.StatusOK}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err=%v want ErrUnavailable", err)
	}
	if transport.credentialIsolatedCalls != 0 {
		t.Fatalf("credential-isolated calls=%d want 0", transport.credentialIsolatedCalls)
	}
}

func TestVimeoUnlistedErrorsNeverLeakSecretMarkers(t *testing.T) {
	base := time.Now().Truncate(time.Second)
	token := vimeoAuthenticatedViewerTestJWT(base.Add(time.Hour).Unix(), vimeoViewerFixtureSecret)
	cookies := []*http.Cookie{{Name: "vimeo", Value: vimeoViewerFixtureSecret}}
	transport := newUnlistedAuthenticatedFixture(t, cookies)
	transport.viewerPayload = []byte(`{"jwt":"` + token + `"}`)
	transport.scopedBodies = [][]byte{[]byte(`{"error_code":5460,"error":"` + vimeoViewerFixtureSecret + `"}`)}
	transport.scopedStatuses = []int{http.StatusNotFound}

	_, err := Vimeo{}.Extract(context.Background(), Request{
		URL:       "https://vimeo.com/123456789/abcdef1234",
		Transport: transport,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), vimeoViewerFixtureSecret) {
		t.Fatalf("error leaked fixture secret: %v", err)
	}
}

func TestMatchesVimeoUnlistedErrorCodeRequiresStrictIntegerAndJSONEOF(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want bool
	}{
		{name: "exact", body: []byte(`{"error_code":5460}`), want: true},
		{name: "extra object field", body: []byte(`{"error_code":5460,"error":"synthetic"}`), want: true},
		{name: "trailing whitespace", body: []byte("{\"error_code\":5460}\n\t "), want: true},
		{name: "missing", body: []byte(`{}`)},
		{name: "quoted", body: []byte(`{"error_code":"5460"}`)},
		{name: "floating", body: []byte(`{"error_code":5460.0}`)},
		{name: "exponential", body: []byte(`{"error_code":5460e0}`)},
		{name: "trailing object", body: []byte(`{"error_code":5460}{}`)},
		{name: "trailing scalar", body: []byte(`{"error_code":5460} 0`)},
		{name: "array", body: []byte(`[{"error_code":5460}]`)},
		{name: "malformed", body: []byte(`{"error_code":5460`)},
		{name: "wrong integer", body: []byte(`{"error_code":5459}`)},
		{name: "oversized", body: append([]byte(`{"error_code":5460,"padding":"`), append(bytes.Repeat([]byte("x"), int(vimeoUnlistedAPIStatusReadBytes)), []byte(`"}`)...)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := matchesVimeoUnlistedErrorCode(test.body, 5460); got != test.want {
				t.Fatalf("match=%v want %v", got, test.want)
			}
			if got := strictVimeoErrorCodeReference(test.body, 5460); got != test.want {
				t.Fatalf("reference=%v want %v", got, test.want)
			}
		})
	}
}

func strictVimeoErrorCodeReference(body []byte, want int64) bool {
	if len(body) == 0 || int64(len(body)) > vimeoUnlistedAPIStatusReadBytes {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return false
	}
	raw, ok := object["error_code"]
	return ok && string(bytes.TrimSpace(raw)) == strconv.FormatInt(want, 10)
}

// FuzzMatchesVimeoUnlistedErrorCode asserts equivalence with a separate raw
// JSON-token reference and therefore covers strict integer syntax, object
// shape, EOF, and the response-size bound in addition to panic freedom.
func FuzzMatchesVimeoUnlistedErrorCode(f *testing.F) {
	seeds := []string{
		``,
		`{}`,
		`{"error_code":5460}`,
		`{"error_code":"5460"}`,
		`{"error_code":5460.0}`,
		`{"error_code":5460e0}`,
		`[]`,
		`not-json`,
		strings.Repeat("a", 9000),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		got := matchesVimeoUnlistedErrorCode([]byte(body), 5460)
		want := strictVimeoErrorCodeReference([]byte(body), 5460)
		if got != want {
			t.Fatalf("match=%v reference=%v body=%q", got, want, body)
		}
		if !matchesVimeoUnlistedErrorCode([]byte(`{"error_code":5460}`), 5460) {
			t.Fatal("canonical integer stopped matching")
		}
	})
}

func FuzzClassifyVimeoUnlistedURL(f *testing.F) {
	for _, seed := range []string{
		"https://vimeo.com/123456789/abcdef1234",
		"https://vimeo.com/123456789/abcdef1234?token=strip#fragment",
		"https://vimeo.com/123456789/ABCDEF1234",
		"https://evil.example/123456789/abcdef1234",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		parsed, err := url.Parse(rawURL)
		if err != nil {
			return
		}
		target, ok := classifyVimeoUnlistedURL(parsed)
		if !ok {
			return
		}
		if target.kind != vimeoRouteVideo || !validVimeoNumericVideoID(target.id) ||
			len(target.unlistedHash) != vimeoUnlistedHashLen ||
			!vimeoUnlistedHashPattern.MatchString(target.unlistedHash) {
			t.Fatalf("invalid accepted target: %+v", target)
		}
		wantCanonical := "https://vimeo.com/" + target.id + "/" + target.unlistedHash
		if target.canonical != wantCanonical || strings.ContainsAny(target.canonical, "?#") {
			t.Fatalf("unsafe canonical=%q want %q", target.canonical, wantCanonical)
		}
		reparsed, err := url.Parse(target.canonical)
		if err != nil {
			t.Fatalf("canonical parse: %v", err)
		}
		roundTrip, roundTripOK := classifyVimeoUnlistedURL(reparsed)
		if !roundTripOK || roundTrip != target {
			t.Fatalf("round trip=%+v ok=%v target=%+v", roundTrip, roundTripOK, target)
		}
	})
}
