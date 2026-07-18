package impersonate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
)

func TestProfileMetadataAndCopiesAreStable(t *testing.T) {
	profile, err := Lookup(Chrome133Name)
	if err != nil {
		t.Fatal(err)
	}
	if profile.Browser != "Chrome" || profile.BrowserVersion != "133" || profile.EngineVersion != ReqVersion {
		t.Fatalf("profile = %#v", profile)
	}
	profile.Headers.Set("User-Agent", "mutated")
	profile.HeaderOrder[0] = "mutated"
	profile.http2Settings[0].Val = 1
	profile.pseudoHeaderOrder[0] = "mutated"
	again, _ := Lookup(Chrome133Name)
	if again.Headers.Get("User-Agent") == "mutated" || again.HeaderOrder[0] == "mutated" ||
		again.http2Settings[0].Val == 1 || again.pseudoHeaderOrder[0] == "mutated" {
		t.Fatal("Lookup returned shared mutable profile state")
	}
	if _, err := Lookup("unknown"); err == nil {
		t.Fatal("unknown profile succeeded")
	}
}

func TestChrome133FingerprintConfiguration(t *testing.T) {
	profile, _ := Lookup(Chrome133Name)
	wantSettings := []uint32{65536, 0, 6291456, 262144}
	if profile.clientHelloID.Client != "Chrome" || profile.clientHelloID.Version != "133" ||
		profile.connectionFlow != 15663105 ||
		!reflect.DeepEqual(profile.pseudoHeaderOrder, []string{":method", ":authority", ":scheme", ":path"}) {
		t.Fatalf("Chrome 133 profile = %#v", profile)
	}
	if len(profile.http2Settings) != len(wantSettings) {
		t.Fatalf("HTTP/2 settings = %#v", profile.http2Settings)
	}
	for index, setting := range profile.http2Settings {
		if setting.Val != wantSettings[index] {
			t.Fatalf("HTTP/2 setting %d = %#v", index, setting)
		}
	}
}

func TestClientPreservesRequestSemanticsAndAppliesProfile(t *testing.T) {
	var received *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		received = request.Clone(request.Context())
		body, _ := io.ReadAll(request.Body)
		writer.Header().Set("X-Body", string(body))
		_, _ = io.WriteString(writer, "ok")
	}))
	defer server.Close()
	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, http.NoBody)
	request.Header.Set("Accept-Language", "fr")
	request.Header.Set("X-Custom", "present")
	request.Host = "override.example.test"
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if received.Method != http.MethodPost || received.Host != request.Host || received.Header.Get("Accept-Language") != "fr" ||
		received.Header.Get("X-Custom") != "present" || received.Header.Get("User-Agent") != profile.UserAgent {
		t.Fatalf("received request = %#v", received)
	}
	if request.Header.Get("User-Agent") != "" {
		t.Fatal("Client.Do mutated the caller's request")
	}
	if response.Request.Header.Get(req.HeaderOderKey) != "" || response.Request.Header.Get(req.PseudoHeaderOderKey) != "" {
		t.Fatalf("response request exposed engine headers: %#v", response.Request.Header)
	}
}

func TestHTTP1HeaderOrderMatchesProfile(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lines := make(chan []string, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			lines <- nil
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		var received []string
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil || line == "\r\n" {
				break
			}
			received = append(received, strings.TrimSpace(line))
		}
		lines <- received
		_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
	}()
	profile, _ := Lookup(Chrome133Name)
	client, _ := New(Config{Profile: profile})
	request, _ := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	received := <-lines
	var ordered []string
	for _, line := range received[1:] {
		key, _, found := strings.Cut(line, ":")
		if found {
			ordered = append(ordered, strings.ToLower(key))
		}
	}
	position := -1
	for _, wanted := range profile.HeaderOrder {
		if wanted == "cookie" {
			continue
		}
		found := -1
		for index := position + 1; index < len(ordered); index++ {
			if ordered[index] == wanted {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("header %q is missing or out of order in %#v", wanted, ordered)
		}
		position = found
	}
}

func TestClientUsesStandardCookieJar(t *testing.T) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if cookie, err := request.Cookie("session"); err == nil && cookie.Value == "shared" {
			_, _ = io.WriteString(writer, "cookie accepted")
			return
		}
		http.SetCookie(writer, &http.Cookie{Name: "session", Value: "shared", Path: "/"})
		_, _ = io.WriteString(writer, "cookie set")
	}))
	defer server.Close()
	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile, Jar: jar})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "cookie accepted" {
		t.Fatalf("body = %q", body)
	}
}

func TestNewRejectsMissingProfileAndInvalidProxy(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("missing profile succeeded")
	}
	profile, _ := Lookup(Chrome133Name)
	if _, err := New(Config{Profile: profile, Proxy: "://invalid"}); err == nil {
		t.Fatal("invalid proxy succeeded")
	}
}

func TestCompatibleTimeoutRetainsMillisecondBoundary(t *testing.T) {
	for _, test := range []struct {
		input time.Duration
		want  time.Duration
	}{
		{input: 0, want: 30 * time.Second},
		{input: time.Nanosecond, want: time.Millisecond},
		{input: 1500*time.Microsecond + 999*time.Nanosecond, want: time.Millisecond},
		{input: 2500 * time.Microsecond, want: 2 * time.Millisecond},
	} {
		if got := compatibleTimeout(test.input); got != test.want {
			t.Fatalf("compatibleTimeout(%s) = %s, want %s", test.input, got, test.want)
		}
	}
	if strconv.IntSize == 32 {
		input := time.Duration(math.MaxInt32+1) * time.Millisecond
		if got := compatibleTimeout(input); got != time.Duration(math.MaxInt32)*time.Millisecond {
			t.Fatalf("32-bit timeout saturation = %s", got)
		}
	}
}

func TestResponseAdapterRetainsFormerBoundaryShape(t *testing.T) {
	original, _ := http.NewRequest(http.MethodPost, "https://source.example/path", bytes.NewBufferString("payload"))
	redirected, _ := http.NewRequest(http.MethodGet, "https://target.example/final", nil)
	redirected.Header[req.HeaderOderKey] = []string{"user-agent"}
	redirected.Header[req.PseudoHeaderOderKey] = []string{":method"}
	redirected.Header.Set("X-Final", "present")
	engineResponse := &http.Response{
		Status: "200 OK", StatusCode: http.StatusOK, Proto: "HTTP/2.0", ProtoMajor: 2,
		Header: http.Header{"X-Response": {"present"}}, Body: http.NoBody,
		ContentLength: 0, Trailer: http.Header{"X-Trailer": {"complete"}},
		Request: redirected, TLS: &tls.ConnectionState{HandshakeComplete: true},
	}
	response := toStandardResponse(original, engineResponse)
	if response.TLS != nil || response.Request.Method != http.MethodGet || response.Request.URL.String() != redirected.URL.String() ||
		response.Request.Body != original.Body || response.Request.ContentLength != original.ContentLength {
		t.Fatalf("adapted response = %#v, request = %#v", response, response.Request)
	}
	if response.Request.Header.Get("X-Final") != "present" || response.Request.Header.Get(req.HeaderOderKey) != "" ||
		response.Request.Header.Get(req.PseudoHeaderOderKey) != "" {
		t.Fatalf("adapted request headers = %#v", response.Request.Header)
	}
	engineResponse.Header.Set("X-Response", "mutated")
	engineResponse.Trailer.Set("X-Trailer", "mutated")
	if response.Header.Get("X-Response") != "present" || response.Trailer.Get("X-Trailer") != "complete" {
		t.Fatal("response adapter did not defensively copy headers")
	}
}
