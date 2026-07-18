package impersonate

import (
	"context"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func TestExplicitHTTPProxyRetainsConnectTunnelAndAuthentication(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/media" {
			http.NotFound(writer, request)
			return
		}
		_, _ = io.WriteString(writer, "tunneled")
	}))
	defer origin.Close()

	var method, target, authorization string
	proxyServer := httptest.NewServer(connectProxyHandler(func(request *http.Request) {
		method, target = request.Method, request.Host
		authorization = request.Header.Get("Proxy-Authorization")
	}))
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	proxyURL.User = url.UserPassword("proxy-user", "proxy-pass")
	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile, Proxy: proxyURL.String(), Timeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequest(http.MethodGet, origin.URL+"/media", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	client.CloseIdleConnections()
	if string(body) != "tunneled" || method != http.MethodConnect {
		t.Fatalf("body = %q, proxy method = %q", body, method)
	}
	originURL, _ := url.Parse(origin.URL)
	if target != originURL.Host || authorization != "Basic cHJveHktdXNlcjpwcm94eS1wYXNz" {
		t.Fatalf("CONNECT target = %q, authorization = %q", target, authorization)
	}
}

func TestExplicitHTTPProxyTunnelsImpersonatedTLS(t *testing.T) {
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "secure tunnel")
	}))
	defer origin.Close()
	proxyServer := httptest.NewServer(connectProxyHandler(nil))
	defer proxyServer.Close()
	pool := x509.NewCertPool()
	pool.AddCert(origin.Certificate())
	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile, Proxy: proxyServer.URL, RootCAs: pool, Timeout: 2 * time.Second})
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
	if string(body) != "secure tunnel" {
		t.Fatalf("body = %q, protocol = %q", body, response.Proto)
	}
}

func TestClientDoesNotInheritEnvironmentProxy(t *testing.T) {
	var proxyHits atomic.Int32
	proxyServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyHits.Add(1)
	}))
	defer proxyServer.Close()
	t.Setenv("HTTP_PROXY", proxyServer.URL)
	t.Setenv("HTTPS_PROXY", proxyServer.URL)
	t.Setenv("ALL_PROXY", proxyServer.URL)
	t.Setenv("NO_PROXY", "")

	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile, Timeout: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://environment-proxy-check.invalid/", nil)
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("unresolvable direct request unexpectedly succeeded")
	}
	if proxyHits.Load() != 0 {
		t.Fatalf("environment proxy received %d requests", proxyHits.Load())
	}
}

func TestProxyConnectHonorsCancellation(t *testing.T) {
	proxyServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer proxyServer.Close()
	profile, _ := Lookup(Chrome133Name)
	client, err := New(Config{Profile: profile, Proxy: proxyServer.URL, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://media.invalid/", nil)
	started := time.Now()
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if err == nil || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("proxy cancellation error = %v after %s", err, time.Since(started))
	}
}

func connectProxyHandler(observe func(*http.Request)) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if observe != nil {
			observe(request)
		}
		upstream, err := net.DialTimeout("tcp", request.Host, time.Second)
		if err != nil {
			http.Error(writer, "dial failed", http.StatusBadGateway)
			return
		}
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			upstream.Close()
			http.Error(writer, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			upstream.Close()
			return
		}
		defer client.Close()
		defer upstream.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		done := make(chan struct{}, 2)
		go func() {
			_, _ = io.Copy(upstream, client)
			done <- struct{}{}
		}()
		go func() {
			_, _ = io.Copy(client, upstream)
			done <- struct{}{}
		}()
		<-done
		_ = client.Close()
		_ = upstream.Close()
	}
}
