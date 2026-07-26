package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoWithoutCredentialsNoRedirectDropsAllCredentialSources(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{
		DefaultHeaders: http.Header{
			"Authorization":       {"default-auth"},
			"Cookie":              {"default-cookie=1"},
			"Proxy-Authorization": {"proxy-auth"},
			"Referer":             {"https://default.example/watch"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "explicit-auth")
	request.Header.Set("Cookie", "explicit-cookie=1")
	request.Header.Set("Proxy-Authorization", "explicit-proxy")
	request.Header.Set("Referer", "https://explicit.example/watch")
	if err := client.AddCookies([]*http.Cookie{{Name: "jar", Value: "secret", Domain: "127.0.0.1", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	response, err := client.DoWithoutCredentialsNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if seen.Get(key) != "" {
			t.Fatalf("credential header %s leaked: %q", key, seen.Get(key))
		}
	}
}

func TestDoWithoutCredentialsNoRedirectRefusesSameOriginRedirect(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		if requests == 1 {
			http.Redirect(writer, request, "/second", http.StatusFound)
			return
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.DoWithoutCredentialsNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || requests != 1 {
		t.Fatalf("status=%d requests=%d", response.StatusCode, requests)
	}
}

func TestDoWithoutCredentialsNoRedirectRefusesCrossOriginRedirect(t *testing.T) {
	redirected := false
	first := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, "https://evil.example/steal", http.StatusFound)
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		redirected = true
	}))
	defer second.Close()

	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, first.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.DoWithoutCredentialsNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || redirected {
		t.Fatalf("status=%d redirected=%v", response.StatusCode, redirected)
	}
}

func TestDoWithoutCredentialsNoRedirectDoesNotPersistSetCookie(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: "secret"})
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.DoWithoutCredentialsNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cookies, err := client.Cookies(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(cookies) != 0 {
		t.Fatalf("cookies=%#v", cookies)
	}
}

func TestDoWithoutCredentialsNoRedirectRedactsSignedURLInErrors(t *testing.T) {
	client, err := New(Config{RoundTripper: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.Canceled
	})})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://rr1---sn-fixture.googlevideo.com/videoplayback?sig=secret&rn=0", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoWithoutCredentialsNoRedirect(context.Background(), request)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("error leaked signed URL: %v", err)
	}
}
