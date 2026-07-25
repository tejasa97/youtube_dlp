package network

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDoWithScopedAuthorizationNoRedirectKeepsOnlyExplicitAuthorization(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		http.SetCookie(writer, &http.Cookie{Name: "response", Value: "secret"})
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := New(Config{DefaultHeaders: http.Header{
		"Authorization":       {"default-secret"},
		"Cookie":              {"default-cookie=secret"},
		"Proxy-Authorization": {"proxy-secret"},
		"X-Fixture":           {"preserved"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddCookies([]*http.Cookie{{Name: "jar", Value: "secret", Domain: "127.0.0.1", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Authorization", "jwt fixture-token")
	request.Header.Set("Cookie", "explicit-cookie=secret")
	request.Header.Set("Proxy-Authorization", "explicit-proxy")
	response, err := client.DoWithScopedAuthorizationNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen.Get("Authorization") != "jwt fixture-token" {
		t.Fatalf("Authorization = %q", seen.Get("Authorization"))
	}
	if seen.Get("Cookie") != "" || seen.Get("Proxy-Authorization") != "" {
		t.Fatalf("ambient credentials leaked: %#v", seen)
	}
	if seen.Get("X-Fixture") != "preserved" {
		t.Fatalf("non-credential default missing: %#v", seen)
	}
	cookies, err := client.Cookies(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	for _, cookie := range cookies {
		if cookie.Name == "response" {
			t.Fatalf("response cookie persisted: %#v", cookies)
		}
	}
}

func TestDoWithScopedAuthorizationNoRedirectDoesNotAdoptDefaultAuthorization(t *testing.T) {
	var authorization string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{DefaultHeaders: http.Header{"Authorization": {"default-secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.DoWithScopedAuthorizationNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if authorization != "" {
		t.Fatalf("default Authorization leaked: %q", authorization)
	}
}

func TestDoWithScopedAuthorizationNoRedirectRefusesRedirects(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		http.Redirect(writer, request, "/second", http.StatusFound)
	}))
	defer server.Close()
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	request.Header.Set("Authorization", "jwt fixture-token")
	response, err := client.DoWithScopedAuthorizationNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || requests != 1 {
		t.Fatalf("status=%d requests=%d", response.StatusCode, requests)
	}
}

func TestDoWithScopedAuthorizationNoRedirectRedactsErrorsAndCancellation(t *testing.T) {
	client, err := New(Config{RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "https://api.vimeo.com/albums/7?token=secret", nil)
	request.Header.Set("Authorization", "jwt fixture-secret")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.DoWithScopedAuthorizationNoRedirect(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "fixture-secret") {
		t.Fatalf("error leaked secret: %v", err)
	}
	if _, err := client.DoWithScopedAuthorizationNoRedirect(context.Background(), nil); err == nil {
		t.Fatal("nil request accepted")
	}
}
