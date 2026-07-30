package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type vimeoPasswordTransport struct {
	status int
	body   string
	post   *http.Request
	data   []byte
	pages  int
}

func (*vimeoPasswordTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected request")
}
func (*vimeoPasswordTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page request")
}
func (t *vimeoPasswordTransport) ReadPageProfile(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	if rawURL != "https://vimeo.com/123" || profile != vimeoImpersonationProfile {
		return nil, nil, errors.New("unexpected Vimeo page")
	}
	t.pages++
	if t.pages == 1 {
		return []byte("password"), make(http.Header), nil
	}
	return []byte(`window.playerConfig = {"video":{"id":123,"title":"synthetic","files":{"progressive":[{"url":"https://cdn.example/video.mp4","quality":"sd"}]}}};`), make(http.Header), nil
}
func (t *vimeoPasswordTransport) DoProfile(ctx context.Context, request *http.Request, _ string) (*http.Response, error) {
	return t.Do(ctx, request)
}
func (*vimeoPasswordTransport) ReadPageProfileWithoutCredentialsNoRedirect(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	if rawURL != "https://vimeo.com/_next/viewer" || profile != vimeoImpersonationProfile {
		return nil, nil, errors.New("unexpected viewer request")
	}
	return []byte(`{"xsrft":"fixture-xsrft"}`), make(http.Header), nil
}
func (t *vimeoPasswordTransport) DoProfiledNoRedirect(_ context.Context, request *http.Request, profile string) (*http.Response, error) {
	if profile != vimeoImpersonationProfile {
		return nil, errors.New("unexpected profile")
	}
	t.post = request.Clone(request.Context())
	t.data, _ = io.ReadAll(request.Body)
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(t.body)), Header: make(http.Header), Request: request}, nil
}

func TestVerifyVimeoVideoPassword(t *testing.T) {
	const password = "synthetic-password"
	transport := &vimeoPasswordTransport{}
	if err := verifyVimeoVideoPassword(context.Background(), transport, "https://vimeo.com/channels/demo/123", "123", password); err != nil {
		t.Fatal(err)
	}
	if transport.post.URL.String() != "https://vimeo.com/channels/demo/123/password" || transport.post.Header.Get("Referer") != "https://vimeo.com/channels/demo/123" || transport.post.Header.Get("Content-Type") != "application/json" || transport.post.Header.Get("Accept") != "*/*" {
		t.Fatalf("unexpected password request: %s %v", transport.post.URL, transport.post.Header)
	}
	if !bytes.Equal(transport.data, []byte(`{"password":"synthetic-password","token":"fixture-xsrft"}`)) {
		t.Fatal("password request did not use compact expected JSON")
	}
	for _, status := range []int{http.StatusTeapot, http.StatusFound} {
		transport := &vimeoPasswordTransport{status: status}
		err := verifyVimeoVideoPassword(context.Background(), transport, "https://vimeo.com/123", "123", password)
		if status == http.StatusTeapot && !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("418 error = %v", err)
		}
		if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "fixture-xsrft") {
			t.Fatalf("secret leaked in error: %v", err)
		}
	}
	if err := verifyVimeoVideoPassword(context.Background(), transport, "https://vimeo.com/123", "123", ""); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("missing password = %v", err)
	}
}

func TestVimeoExtractsAfterVideoPasswordVerification(t *testing.T) {
	transport := &vimeoPasswordTransport{}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: "https://vimeo.com/123", VideoPassword: "synthetic-password", Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "123" || transport.pages != 2 || transport.post == nil {
		t.Fatalf("id=%q pages=%d posted=%t", id, transport.pages, transport.post != nil)
	}
}

func TestVerifyVimeoPlayerPassword(t *testing.T) {
	const password = "synthetic-password"
	transport := &vimeoPasswordTransport{body: `{}`}
	if _, err := verifyVimeoPlayerPassword(context.Background(), transport, "https://player.vimeo.com/video/123?token=ignored", "https://publisher.example/embed", password); err != nil {
		t.Fatal(err)
	}
	if transport.post.URL.String() != "https://player.vimeo.com/video/123/check-password" || transport.post.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || transport.post.Header.Get("Referer") != "https://publisher.example/embed" {
		t.Fatalf("unexpected player request: %s %v", transport.post.URL, transport.post.Header)
	}
	form, err := url.ParseQuery(string(transport.data))
	if err != nil || form.Get("password") != base64.StdEncoding.EncodeToString([]byte(password)) {
		t.Fatalf("player form = %q, want encoded password", transport.data)
	}
	for _, response := range []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusOK, "false", ErrWrongPassword},
		{http.StatusTeapot, "", ErrWrongPassword},
		{http.StatusUnauthorized, "", ErrAuthentication},
		{http.StatusForbidden, "", ErrAuthentication},
		{http.StatusNotFound, "", ErrUnavailable},
		{http.StatusGone, "", ErrUnavailable},
		{http.StatusFound, "", ErrVimeoPlaylistNetwork},
		{http.StatusInternalServerError, "", ErrVimeoPlaylistNetwork},
		{http.StatusOK, "{", ErrInvalidMetadata},
	} {
		_, err := verifyVimeoPlayerPassword(context.Background(), &vimeoPasswordTransport{status: response.status, body: response.body}, "https://player.vimeo.com/video/123", "https://player.vimeo.com/video/123", password)
		if !errors.Is(err, response.want) {
			t.Fatalf("response %d/%q = %v", response.status, response.body, err)
		}
	}
}

type vimeoPlayerPasswordE2ETransport struct {
	status       int
	pageURL      string
	postURL      string
	postReferer  string
	postBody     []byte
	pageCalls    int
	postCalls    int
	ambientCalls int
}

func (t *vimeoPlayerPasswordE2ETransport) Do(context.Context, *http.Request) (*http.Response, error) {
	t.ambientCalls++
	return nil, errors.New("unexpected ambient request")
}
func (*vimeoPlayerPasswordE2ETransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected native page request")
}
func (t *vimeoPlayerPasswordE2ETransport) ReadPageProfile(_ context.Context, rawURL, profile string) ([]byte, http.Header, error) {
	t.pageCalls++
	t.pageURL = rawURL
	if profile != vimeoImpersonationProfile {
		return nil, nil, errors.New("unexpected profile")
	}
	return []byte(`window.playerConfig = {"view":4,"video":{"id":123}};`), make(http.Header), nil
}
func (t *vimeoPlayerPasswordE2ETransport) DoProfile(ctx context.Context, request *http.Request, _ string) (*http.Response, error) {
	return t.Do(ctx, request)
}
func (t *vimeoPlayerPasswordE2ETransport) DoProfiledNoRedirect(_ context.Context, request *http.Request, profile string) (*http.Response, error) {
	if profile != vimeoImpersonationProfile {
		return nil, errors.New("unexpected profile")
	}
	t.postCalls++
	t.postURL = request.URL.String()
	t.postReferer = request.Header.Get("Referer")
	t.postBody, _ = io.ReadAll(request.Body)
	status := t.status
	if status == 0 {
		status = http.StatusOK
	}
	body := `{"video":{"id":123,"title":"synthetic player","files":{"progressive":[{"url":"https://media.example.test/video.mp4","quality":"sd"}]}}}`
	header := make(http.Header)
	if status >= 300 && status < 400 {
		header.Set("Location", "https://evil.example/collect")
		body = ""
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: header, Request: request}, nil
}

func TestVimeoPlayerRouteExtractsAfterPasswordVerification(t *testing.T) {
	const (
		playerURL = "https://player.vimeo.com/video/123?context=synthetic"
		referer   = "https://publisher.example/embed"
		password  = "synthetic-password"
	)
	transport := &vimeoPlayerPasswordE2ETransport{}
	result, err := NewVimeo().Extract(context.Background(), Request{URL: playerURL, Referer: referer, VideoPassword: password, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := result.Info.ID(); id != "123" || transport.pageURL != playerURL || transport.postURL != "https://player.vimeo.com/video/123/check-password" || transport.postReferer != referer {
		t.Fatalf("id=%q page=%q post=%q referer=%q", id, transport.pageURL, transport.postURL, transport.postReferer)
	}
	if transport.pageCalls != 1 || transport.postCalls != 1 || transport.ambientCalls != 0 {
		t.Fatalf("page=%d post=%d ambient=%d", transport.pageCalls, transport.postCalls, transport.ambientCalls)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	form, err := url.ParseQuery(string(transport.postBody))
	if err != nil || form.Get("password") != encoded {
		t.Fatal("player password form mismatch")
	}
	metadata, _ := json.Marshal(result.Info.Fields())
	for _, secret := range []string{password, encoded} {
		if strings.Contains(string(metadata), secret) || strings.Contains(transport.pageURL, secret) || strings.Contains(transport.postURL, secret) || strings.Contains(transport.postReferer, secret) {
			t.Fatal("player password escaped its bounded POST body")
		}
	}
}

func TestVimeoPlayerRoutePasswordRedirectDoesNotCrossOrigin(t *testing.T) {
	const password = "synthetic-password"
	transport := &vimeoPlayerPasswordE2ETransport{status: http.StatusFound}
	_, err := NewVimeo().Extract(context.Background(), Request{
		URL: "https://player.vimeo.com/video/123", VideoPassword: password, Transport: transport,
	})
	if !errors.Is(err, ErrVimeoPlaylistNetwork) || transport.postCalls != 1 || transport.ambientCalls != 0 {
		t.Fatalf("error=%v post=%d ambient=%d", err, transport.postCalls, transport.ambientCalls)
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), base64.StdEncoding.EncodeToString([]byte(password))) {
		t.Fatal("player password leaked through redirect error")
	}
}

func TestVimeoPasswordEndpointsRejectCrossOriginAndCancellation(t *testing.T) {
	if _, _, ok := vimeoVideoPasswordEndpoint("https://evil.example/123", "123"); ok {
		t.Fatal("accepted cross-origin video endpoint")
	}
	if _, ok := vimeoPlayerPasswordEndpoint("https://evil.example/video/123"); ok {
		t.Fatal("accepted cross-origin player endpoint")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyVimeoVideoPassword(ctx, &vimeoPasswordTransport{}, "https://vimeo.com/123", "123", "synthetic-password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
}

func FuzzVimeoConfigPasswordRequired(f *testing.F) {
	f.Add([]byte(`{"invalid_parameters":[{"field":"password"}]}`))
	f.Add([]byte(`{`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = vimeoConfigPasswordRequired(data)
	})
}
