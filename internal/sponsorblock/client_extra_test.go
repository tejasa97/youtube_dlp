package sponsorblock

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchMalformedBodyCategorized(t *testing.T) {
	for _, body := range []string{`{not json`, `not-an-array`, `["garbage"]`} {
		transport := &fakeResponseTransport{body: strings.NewReader(body), fakeTransport: fakeTransport{status: 200, contentType: "application/json"}}
		_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 60)
		if !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("body %q: err = %v, want ErrInvalidMetadata", body, err)
		}
	}
}

func TestFetchUnknownCategoryDropsSegment(t *testing.T) {
	// Unknown categories are dropped silently. The pinned reference
	// only emits the eleven canonical categories; an envelope with
	// an unknown entry is well-formed but the offending segment is
	// dropped, leaving the call successful.
	body := `[{"videoID":"abc","segments":[{"segment":[0,1],"category":"unknown","actionType":"skip","videoDuration":60},{"segment":[5,10],"category":"sponsor","actionType":"skip","videoDuration":60}]}]`
	transport := &fakeResponseTransport{body: strings.NewReader(body), fakeTransport: fakeTransport{status: 200, contentType: "application/json"}}
	result, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chapters) != 1 {
		t.Fatalf("got %d chapters, want 1", len(result.Chapters))
	}
}

func TestFetchUnsupportedService(t *testing.T) {
	transport := &fakeResponseTransport{}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "Vimeo", "abc", 0)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v, want ErrUnsupported", err)
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", transport.calls.Load())
	}
}

func TestFetchCancellationRespected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Fetch(ctx, &serverTransport{server: server}, Options{Enabled: true, Categories: []string{"sponsor"}, APIBase: server.URL}, "YouTube", "abc", 0)
	if err == nil {
		t.Fatal("Fetch succeeded with cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context cancellation", err)
	}
}

func TestFetchOversizedResponse(t *testing.T) {
	// Create a body larger than MaxResponseBytes by emitting many
	// empty groups. The decoder caps the group count to 64 so the
	// payload must exceed the byte budget; the body reader triggers
	// the cap before the decoder runs.
	builder := strings.Builder{}
	builder.WriteByte('[')
	for builder.Len() <= MaxResponseBytes+8 {
		builder.WriteString(`{"videoID":"a","segments":[]},`)
	}
	builder.WriteString(`{"videoID":"a","segments":[]}]`)
	transport := &fakeResponseTransport{body: strings.NewReader(builder.String()), fakeTransport: fakeTransport{status: 200, contentType: "application/json"}}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err = %v, want ErrInvalidMetadata", err)
	}
}

func TestFetchSecretSafeErrorMessages(t *testing.T) {
	transport := &fakeResponseTransport{body: strings.NewReader("server leaked: token=abc123"), fakeTransport: fakeTransport{status: 500}}
	_, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "abc123") {
		t.Fatalf("error leaked body: %q", err.Error())
	}
}

func TestFetchNoMatchReturnsEmptyChapters(t *testing.T) {
	body := `[{"videoID":"other","segments":[]}]`
	transport := &fakeResponseTransport{body: strings.NewReader(body), fakeTransport: fakeTransport{status: 200, contentType: "application/json"}}
	result, err := Fetch(context.Background(), transport, Options{Enabled: true, Categories: []string{"sponsor"}}, "YouTube", "abc", 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Chapters) != 0 {
		t.Fatalf("got %d, want 0", len(result.Chapters))
	}
}

func TestDecodeResponseRejectsStructuralAndResourceBounds(t *testing.T) {
	for _, body := range []string{
		`[{"videoID":"abc"}]`,
		`[{"videoID":"abc","segments":null}]`,
		`[{"videoID":"abc","segments":{}}]`,
		`[{"segments":[]}]`,
		`[{"videoID":"` + strings.Repeat("x", MaxStringBytes+1) + `","segments":[]}]`,
	} {
		if _, err := decodeResponse([]byte(body), "abc"); !errors.Is(err, ErrInvalidMetadata) {
			t.Fatalf("decode(%d bytes) = %v, want ErrInvalidMetadata", len(body), err)
		}
	}
	var groups strings.Builder
	groups.WriteByte('[')
	for index := 0; index < 65; index++ {
		if index > 0 {
			groups.WriteByte(',')
		}
		groups.WriteString(`{"videoID":"abc","segments":[]}`)
	}
	groups.WriteByte(']')
	if _, err := decodeResponse([]byte(groups.String()), "abc"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("group bound = %v", err)
	}
	var segments strings.Builder
	segments.WriteString(`[{"videoID":"abc","segments":[`)
	for index := 0; index <= MaxSegmentCount; index++ {
		if index > 0 {
			segments.WriteByte(',')
		}
		segments.WriteString(`{"segment":[1,2],"category":"sponsor","actionType":"skip"}`)
	}
	segments.WriteString(`]}]`)
	if _, err := decodeResponse([]byte(segments.String()), "abc"); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("segment bound = %v", err)
	}
}

// serverTransport wraps an httptest.Server so the test cancellation
// path uses the same credential-isolated surface.
type serverTransport struct {
	server *httptest.Server
}

func (transport *serverTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

func (transport *serverTransport) DoWithoutCredentials(ctx context.Context, request *http.Request) (*http.Response, error) {
	request = request.Clone(ctx)
	request.Header.Del("Cookie")
	request.Header.Del("Authorization")
	return http.DefaultClient.Do(request)
}
