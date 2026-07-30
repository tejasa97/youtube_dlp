package extractor

import (
	"bytes"
	"context"
	"encoding/base64"
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
	if _, err := verifyVimeoPlayerPassword(context.Background(), transport, "https://player.vimeo.com/video/123?token=ignored", password); err != nil {
		t.Fatal(err)
	}
	if transport.post.URL.String() != "https://player.vimeo.com/video/123/check-password" || transport.post.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
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
	}{{http.StatusOK, "false", ErrWrongPassword}, {http.StatusTeapot, "", ErrWrongPassword}, {http.StatusOK, "{", ErrInvalidMetadata}} {
		_, err := verifyVimeoPlayerPassword(context.Background(), &vimeoPasswordTransport{status: response.status, body: response.body}, "https://player.vimeo.com/video/123", password)
		if !errors.Is(err, response.want) {
			t.Fatalf("response %d/%q = %v", response.status, response.body, err)
		}
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
