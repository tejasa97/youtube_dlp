package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDoWithScopedAuthenticationNoRedirectUsesExplicitShowScope(t *testing.T) {
	var seen http.Header
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests++
		seen = request.Header.Clone()
		http.SetCookie(writer, &http.Cookie{Name: "response", Value: "secret"})
		http.Redirect(writer, request, "/redirected", http.StatusFound)
	}))
	defer server.Close()

	client, err := New(Config{DefaultHeaders: http.Header{
		"Authentication":      {"default-authentication"},
		"Authorization":       {"default-authorization"},
		"Cookie":              {"default-cookie=secret"},
		"Proxy-Authorization": {"default-proxy"},
		"Referer":             {"https://ambient.example/watch"},
		"X-Fixture":           {"preserved"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.AddCookies([]*http.Cookie{{Name: "jar", Value: "secret", Domain: "127.0.0.1", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authentication", "Bearer show-token")
	request.Header.Set("Authorization", "explicit-authorization")
	request.Header.Set("Cookie", "explicit-cookie=secret")
	request.Header.Set("Proxy-Authorization", "explicit-proxy")
	request.Header.Set("Referer", "https://www.discoveryplus.in/")
	response, err := client.DoWithScopedAuthenticationNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusFound || requests != 1 {
		t.Fatalf("status=%d requests=%d", response.StatusCode, requests)
	}
	if seen.Get("Authentication") != "Bearer show-token" || seen.Get("Referer") != "https://www.discoveryplus.in/" {
		t.Fatalf("show scope=%#v", seen)
	}
	for _, key := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if seen.Get(key) != "" {
			t.Fatalf("%s leaked: %#v", key, seen)
		}
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

func TestDoWithScopedAuthenticationNoRedirectRejectsAmbientScope(t *testing.T) {
	var seen http.Header
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		seen = request.Header.Clone()
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	client, err := New(Config{DefaultHeaders: http.Header{
		"Authentication": {"default-authentication"},
		"Referer":        {"https://ambient.example/watch"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.DoWithScopedAuthenticationNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen.Get("Authentication") != "" || seen.Get("Referer") != "" {
		t.Fatalf("ambient show scope leaked: %#v", seen)
	}
	if _, err := client.DoWithScopedAuthenticationNoRedirect(context.Background(), nil); err == nil {
		t.Fatal("nil request accepted")
	}
}
