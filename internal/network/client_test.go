package network

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/testserver"
)

func TestClientHeadersCookiesRedirectsAndCompression(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client, err := New(Config{DefaultHeaders: http.Header{"X-Fixture": []string{"present"}}})
	if err != nil {
		t.Fatal(err)
	}

	body, _, err := client.ReadPage(context.Background(), server.URL+"/headers")
	if err != nil {
		t.Fatalf("headers: %v", err)
	}
	if !strings.Contains(string(body), `"x_fixture":"present"`) || !strings.Contains(string(body), `"user_agent":"ytdlp-go/`) {
		t.Fatalf("header response = %s", body)
	}
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/cookies/set"); err != nil {
		t.Fatalf("cookie set: %v", err)
	}
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/cookies/check"); err != nil {
		t.Fatalf("cookie check: %v", err)
	}
	body, _, err = client.ReadPage(context.Background(), server.URL+"/redirect")
	if err != nil || !strings.Contains(string(body), `"fixture-direct"`) {
		t.Fatalf("redirect body = %s, error = %v", body, err)
	}
	body, _, err = client.ReadPage(context.Background(), server.URL+"/gzip")
	if err != nil || string(body) != "deterministic gzip response\n" {
		t.Fatalf("gzip body = %q, error = %v", body, err)
	}
}

func TestReadPageLimitAndCancellation(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client, _ := New(Config{MaxPageSize: 32})
	if _, _, err := client.ReadPage(context.Background(), server.URL+"/large?size=64"); !errors.Is(err, ErrPageTooLarge) {
		t.Fatalf("large page error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, _, err := client.ReadPage(ctx, server.URL+"/slow?delay=1s"); err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v", err)
	}
}

func TestClientUsesConfiguredProxy(t *testing.T) {
	var received string
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.URL.String()
		_, _ = writer.Write([]byte("proxied"))
	}))
	defer proxy.Close()
	client, err := New(Config{Proxy: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err := client.ReadPage(context.Background(), "http://media.invalid/page")
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "proxied" || received != "http://media.invalid/page" {
		t.Fatalf("body = %q, URL = %q", body, received)
	}
}

func TestDoLeavesBodyOwnershipToCaller(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "body")
	}))
	defer server.Close()
	client, _ := New(Config{})
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.Do(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Body == nil {
		t.Fatal("response body is nil")
	}
	response.Body.Close()
}

func TestRedaction(t *testing.T) {
	parsed, _ := url.Parse("https://user:secret@example.invalid/v?token=secret&visible=yes")
	redacted := RedactURL(parsed)
	if strings.Contains(redacted, "secret") || !strings.Contains(redacted, "visible=yes") {
		t.Fatalf("RedactURL() = %q", redacted)
	}
	headers := RedactHeaders(http.Header{"Authorization": []string{"secret"}, "X-Safe": []string{"yes"}})
	if headers.Get("Authorization") != "REDACTED" || headers.Get("X-Safe") != "yes" {
		t.Fatalf("RedactHeaders() = %v", headers)
	}
}
