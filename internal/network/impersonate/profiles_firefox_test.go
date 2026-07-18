package impersonate

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/imroc/req/v3"
	utls "github.com/refraction-networking/utls"
)

type profileCapture struct {
	Schema         string              `json:"schema"`
	Name           string              `json:"name"`
	Browser        string              `json:"browser"`
	BrowserVersion string              `json:"browser_version"`
	Engine         string              `json:"engine"`
	EngineVersion  string              `json:"engine_version"`
	Fingerprint    FingerprintMetadata `json:"fingerprint"`
}

func TestFirefox120ConformanceCapture(t *testing.T) {
	profile, err := Lookup(Firefox120Name)
	if err != nil {
		t.Fatal(err)
	}
	actual := profileCapture{
		Schema: "ytdlp-go-impersonation-profile-v1", Name: profile.Name,
		Browser: profile.Browser, BrowserVersion: profile.BrowserVersion,
		Engine: profile.Engine, EngineVersion: profile.EngineVersion,
		Fingerprint: profile.Fingerprint(),
	}
	fixture, err := os.Open(filepath.Join("..", "..", "..", "conformance", "network", "impersonation-profiles", "firefox-120.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer fixture.Close()
	decoder := json.NewDecoder(fixture)
	decoder.DisallowUnknownFields()
	var expected profileCapture
	if err := decoder.Decode(&expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("Firefox capture drift:\nactual=%#v\nexpected=%#v", actual, expected)
	}
	encoded, err := json.Marshal(actual)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	if got := hex.EncodeToString(digest[:]); got != "ed4990d2cc9637fecc2db79b7923412d22bbbf6b086f0e26fa53e6fc7494ef5c" {
		t.Fatalf("canonical Firefox capture hash = %s", got)
	}
}

func TestFirefox120UsesExactSupportedClientHello(t *testing.T) {
	profile, _ := Lookup(Firefox120Name)
	if profile.clientHelloID != utls.HelloFirefox_120 || profile.clientHelloID.Client != "Firefox" || profile.clientHelloID.Version != "120" {
		t.Fatalf("ClientHello ID = %#v", profile.clientHelloID)
	}
	spec, err := utls.UTLSIdToSpec(profile.clientHelloID)
	if err != nil || len(spec.CipherSuites) == 0 || len(spec.Extensions) == 0 {
		t.Fatalf("uTLS Firefox 120 spec unavailable: ciphers=%d extensions=%d err=%v", len(spec.CipherSuites), len(spec.Extensions), err)
	}
	client := req.C().SetLogger(nil)
	if profile.apply(client) != client {
		t.Fatal("profile apply did not preserve client identity")
	}
}

func TestFirefoxLookupAndFingerprintAreDefensive(t *testing.T) {
	profiles := Supported()
	if len(profiles) != 2 || profiles[0].Name != Chrome133Name || profiles[1].Name != Firefox120Name {
		t.Fatalf("Supported() = %#v", profiles)
	}
	profile := profiles[1]
	fingerprint := profile.Fingerprint()
	profile.Headers.Set("Accept", "mutated")
	profile.HeaderOrder[0] = "mutated"
	profile.priorityFrames[0].StreamID = 99
	fingerprint.Headers.Set("Accept", "fingerprint-mutated")
	fingerprint.HTTP2Settings[0].Value = 1
	fingerprint.PriorityFrames[0].StreamID = 99
	fingerprint.HeaderPriority.Weight = 1
	again, err := Lookup(Firefox120Name)
	if err != nil {
		t.Fatal(err)
	}
	againFingerprint := again.Fingerprint()
	if again.Headers.Get("Accept") == "mutated" || again.HeaderOrder[0] == "mutated" || again.priorityFrames[0].StreamID == 99 ||
		againFingerprint.Headers.Get("Accept") == "fingerprint-mutated" || againFingerprint.HTTP2Settings[0].Value == 1 ||
		againFingerprint.PriorityFrames[0].StreamID == 99 || againFingerprint.HeaderPriority.Weight == 1 {
		t.Fatal("profile or fingerprint exposed shared mutable state")
	}
	if _, err := Lookup("firefox-latest"); err == nil {
		t.Fatal("floating Firefox profile unexpectedly succeeded")
	}
}

func TestFirefoxHTTP1HeaderOrderMatchesLocalCapture(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	lines := make(chan []string, 1)
	go captureHTTP1Headers(listener, lines)
	profile, _ := Lookup(Firefox120Name)
	client, err := New(Config{Profile: profile})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, "http://"+listener.Addr().String()+"/", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	assertHeaderSubsequence(t, <-lines, profile.HeaderOrder)
}

func TestFirefoxProfileRetainsProxyPathAndCancellation(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "proxied Firefox")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(connectProxyHandler(nil))
	defer proxy.Close()
	profile, _ := Lookup(Firefox120Name)
	client, err := New(Config{Profile: profile, Proxy: proxy.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, origin.URL, nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if string(body) != "proxied Firefox" {
		t.Fatalf("proxy body = %q", body)
	}

	started := make(chan struct{})
	blocking := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
	}))
	defer blocking.Close()
	direct, _ := New(Config{Profile: profile, Timeout: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancelResult := make(chan error, 1)
	go func() {
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, blocking.URL, nil)
		response, err := direct.Do(request)
		if response != nil {
			response.Body.Close()
		}
		cancelResult <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancellation server did not receive request")
	}
	cancel()
	select {
	case err := <-cancelResult:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Firefox cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Firefox request ignored cancellation")
	}
}

func FuzzProfileLookup(f *testing.F) {
	f.Add(Chrome133Name)
	f.Add(Firefox120Name)
	f.Add("firefox-latest")
	f.Fuzz(func(t *testing.T, name string) {
		profile, err := Lookup(name)
		if err != nil {
			return
		}
		if profile.Name != name || profile.Name == "" || profile.clientHelloID.Client == "" {
			t.Fatalf("Lookup(%q) = %#v", name, profile)
		}
		first := profile.Fingerprint()
		second := profile.Fingerprint()
		if !reflect.DeepEqual(first, second) {
			t.Fatal("fingerprint snapshot is not deterministic")
		}
	})
}

func captureHTTP1Headers(listener net.Listener, output chan<- []string) {
	connection, err := listener.Accept()
	if err != nil {
		output <- nil
		return
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	var lines []string
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil || line == "\r\n" {
			break
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	output <- lines
	_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
}

func assertHeaderSubsequence(t *testing.T, lines, wanted []string) {
	t.Helper()
	var observed []string
	for _, line := range lines[1:] {
		key, _, found := strings.Cut(line, ":")
		if found {
			observed = append(observed, strings.ToLower(key))
		}
	}
	position := -1
	for _, key := range wanted {
		if key == "referer" || key == "cookie" || key == "te" {
			continue
		}
		found := -1
		for index := position + 1; index < len(observed); index++ {
			if observed[index] == key {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("header %q missing/out of order in %#v", key, observed)
		}
		position = found
	}
}
