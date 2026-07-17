package testserver

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

func TestPageDescribesDirectMedia(t *testing.T) {
	server := New()
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/page")
	if err != nil {
		t.Fatalf("GET /page: %v", err)
	}
	defer response.Body.Close()

	var root value.Value
	if err := json.NewDecoder(response.Body).Decode(&root); err != nil {
		t.Fatalf("decode page: %v", err)
	}
	object, ok := root.Object()
	if !ok {
		t.Fatalf("page kind = %s", root.Kind())
	}
	keys := object.Fields()
	wantKeys := []string{"id", "title", "webpage_url", "ext", "formats"}
	for index, want := range wantKeys {
		if got := keys[index].Key; got != want {
			t.Fatalf("field[%d] = %q, want %q", index, got, want)
		}
	}
	formats, ok := object.Lookup("formats").ListValue()
	if !ok || len(formats) != 1 {
		t.Fatalf("formats = %#v, %v", formats, ok)
	}
	format, _ := formats[0].Object()
	mediaURL, _ := format.Lookup("url").StringValue()
	if mediaURL != server.URL+"/media" {
		t.Fatalf("media URL = %q", mediaURL)
	}
}

func TestMediaFullAndRangeResponses(t *testing.T) {
	server := New()
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/media")
	if err != nil {
		t.Fatalf("GET /media: %v", err)
	}
	full, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read full media: %v", err)
	}
	if string(full) != string(server.Media()) {
		t.Fatal("full media does not match fixture bytes")
	}

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/media", nil)
	request.Header.Set("Range", "bytes=100-199")
	response, err = server.Client().Do(request)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	partial, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	if response.StatusCode != http.StatusPartialContent {
		t.Fatalf("range status = %d", response.StatusCode)
	}
	if len(partial) != 100 || string(partial) != string(server.Media()[100:200]) {
		t.Fatalf("range length = %d or content mismatch", len(partial))
	}
}

func TestMediaRejectsUnsatisfiableRange(t *testing.T) {
	server := New()
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/media", nil)
	request.Header.Set("Range", "bytes=999999-")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatalf("range request: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d", response.StatusCode)
	}
}

func TestRedirectGzipAndCookies(t *testing.T) {
	server := New()
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Jar = jar

	response, err := client.Get(server.URL + "/redirect")
	if err != nil {
		t.Fatalf("redirect: %v", err)
	}
	response.Body.Close()
	if response.Request.URL.Path != "/page" {
		t.Fatalf("redirect path = %q", response.Request.URL.Path)
	}

	rawClient := *server.Client()
	rawClient.Transport = &http.Transport{DisableCompression: true}
	response, err = rawClient.Get(server.URL + "/gzip")
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	compressor, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, err := io.ReadAll(compressor)
	compressor.Close()
	response.Body.Close()
	if err != nil || string(decoded) != "deterministic gzip response\n" {
		t.Fatalf("gzip body = %q, err = %v", decoded, err)
	}

	response, err = client.Get(server.URL + "/cookies/set")
	if err != nil {
		t.Fatalf("set cookie: %v", err)
	}
	response.Body.Close()
	response, err = client.Get(server.URL + "/cookies/check")
	if err != nil {
		t.Fatalf("check cookie: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("cookie status = %d", response.StatusCode)
	}
}

func TestSlowEndpointHonorsCancellation(t *testing.T) {
	server := New()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/slow?delay=1s", nil)
	_, err := server.Client().Do(request)
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("slow request error = %v", err)
	}
}

func TestDisconnectEndpointTruncatesResponse(t *testing.T) {
	server := New()
	defer server.Close()

	response, err := server.Client().Get(server.URL + "/disconnect")
	if err != nil {
		t.Fatalf("GET /disconnect: %v", err)
	}
	_, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err == nil {
		t.Fatal("truncated response was accepted without an error")
	}
}

func TestMutableEndpointAdvancesDeterministically(t *testing.T) {
	server := New()
	defer server.Close()

	read := func() string {
		response, err := server.Client().Get(server.URL + "/mutable")
		if err != nil {
			t.Fatalf("GET /mutable: %v", err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		return string(body)
	}
	if got := read(); got != "fixture revision 0\n" {
		t.Fatalf("initial body = %q", got)
	}
	response, err := server.Client().Post(server.URL+"/mutable", "application/octet-stream", nil)
	if err != nil {
		t.Fatalf("POST /mutable: %v", err)
	}
	response.Body.Close()
	if got := read(); got != "fixture revision 1\n" {
		t.Fatalf("advanced body = %q", got)
	}
}
