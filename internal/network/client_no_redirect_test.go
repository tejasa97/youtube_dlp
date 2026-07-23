package network

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestDoNoRedirectReturnsSameOriginRedirectAndPreservesRequest(t *testing.T) {
	var targetHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/set":
			http.SetCookie(writer, &http.Cookie{Name: "session", Value: "jar-value", Path: "/"})
		case "/redirect":
			if got := request.Header.Get("Cookie"); got != "session=jar-value" {
				http.Error(writer, "expected only operation jar cookie", http.StatusForbidden)
				return
			}
			if got := request.Header.Get("X-Default"); got != "present" {
				http.Error(writer, "missing default header", http.StatusForbidden)
				return
			}
			if got := request.Header.Get("User-Agent"); got == "" {
				http.Error(writer, "missing default user agent", http.StatusForbidden)
				return
			}
			http.Redirect(writer, request, "/target", http.StatusFound)
		case "/target":
			targetHits.Add(1)
			_, _ = io.WriteString(writer, "must not be reached")
		}
	}))
	defer server.Close()

	client, err := New(Config{DefaultHeaders: http.Header{
		"Cookie":    {"default=must-not-send"},
		"X-Default": {"present"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/set"); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, server.URL+"/redirect?token=caller-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Caller", "preserve-me")
	request.Header.Set("Cookie", "caller=must-not-send")
	response, err := client.DoNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound || response.Header.Get("Location") != "/target" {
		t.Fatalf("response = %d location %q", response.StatusCode, response.Header.Get("Location"))
	}
	if targetHits.Load() != 0 {
		t.Fatalf("same-origin redirect was followed %d times", targetHits.Load())
	}
	if request.Header.Get("X-Default") != "" || request.Header.Get("User-Agent") != "" || request.Header.Get("Cookie") != "caller=must-not-send" {
		t.Fatalf("caller request mutated: %#v", request.Header)
	}
	if request.Header.Get("X-Caller") != "preserve-me" || request.URL.Query().Get("token") != "caller-secret" {
		t.Fatalf("caller request was not preserved: %#v %s", request.Header, request.URL)
	}
}

func TestDoNoRedirectDoesNotFollowCrossOriginRedirect(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetHits.Add(1)
		_, _ = io.WriteString(writer, "must not be reached")
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL+"/target", http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, source.URL+"/redirect", strings.NewReader("body"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer must-not-leave-source")
	response, err := client.DoNoRedirect(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if targetHits.Load() != 0 {
		t.Fatalf("cross-origin redirect was followed %d times", targetHits.Load())
	}
}

func TestDoNoRedirectCancellationAndRedactsTransportFailures(t *testing.T) {
	client, err := New(Config{RoundTripper: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()
		return nil, request.Context().Err()
	})})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid/slow?token=secret", nil)
	request.Header.Set("Authorization", "Bearer secret-header")
	_, err = client.DoNoRedirect(ctx, request)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation error = %v", err)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("error leaked sensitive request data: %v", err)
	}

	client, err = New(Config{RoundTripper: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failed with token=secret and authorization=secret-header")
	})})
	if err != nil {
		t.Fatal(err)
	}
	request, _ = http.NewRequest(http.MethodGet, "https://example.invalid/fail?token=secret", nil)
	request.Header.Set("Cookie", "session=secret-cookie")
	_, err = client.DoNoRedirect(context.Background(), request)
	var requestError *RequestError
	if !errors.As(err, &requestError) {
		t.Fatalf("error type = %T, want RequestError", err)
	}
	if strings.Contains(err.Error(), "secret") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("error leaked sensitive request data: %v", err)
	}
}

func TestDoNoRedirectNilRequest(t *testing.T) {
	client, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoNoRedirect(context.Background(), nil)
	if err == nil || err.Error() != "HTTP request must not be nil" {
		t.Fatalf("nil request error = %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
