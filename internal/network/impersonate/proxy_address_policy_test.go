package impersonate

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHTTPConnectProxyHonorsIPv4SourceAddress(t *testing.T) {
	remote := make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			t.Errorf("proxy method = %s, want CONNECT", request.Method)
		}
		remote <- request.RemoteAddr
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("HTTP proxy does not support hijacking")
			return
		}
		connection, buffered, err := hijacker.Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		_, _ = io.Copy(io.Discard, connection)
	}))
	defer proxyServer.Close()

	proxyURL, _ := url.Parse(proxyServer.URL)
	dial, err := newProxyDialContext(proxyURL, time.Second, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp6", "media.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if got := <-remote; !strings.HasPrefix(got, "127.0.0.1:") {
		t.Fatalf("proxy peer address = %q, want 127.0.0.1", got)
	}
}

func TestSOCKS5ProxyHonorsIPv4SourceAddress(t *testing.T) {
	proxyURL, observations := startSOCKSProxy(t, "tcp4")
	dial, err := newProxyDialContext(proxyURL, time.Second, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp6", "127.0.0.1:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	observation := <-observations
	if !strings.HasPrefix(observation.remote, "127.0.0.1:") || observation.atyp != 1 || observation.host != "127.0.0.1" {
		t.Fatalf("SOCKS5 observation = %#v", observation)
	}
}

func TestSOCKS5HProxyRetainsRemoteDNSAndHonorsIPv6SourceAddress(t *testing.T) {
	proxyURL, observations := startSOCKSProxy(t, "tcp6")
	proxyURL.Scheme = "socks5h"
	dial, err := newProxyDialContext(proxyURL, time.Second, "::1")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dial(context.Background(), "tcp4", "media.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	observation := <-observations
	if !strings.HasPrefix(observation.remote, "[::1]:") || observation.atyp != 3 || observation.host != "media.invalid" {
		t.Fatalf("SOCKS5H observation = %#v", observation)
	}
}

type socksObservation struct {
	remote string
	atyp   byte
	host   string
}

func startSOCKSProxy(t *testing.T, network string) (*url.URL, <-chan socksObservation) {
	listener, err := net.Listen(network, map[string]string{"tcp4": "127.0.0.1:0", "tcp6": "[::1]:0"}[network])
	if err != nil {
		if network == "tcp6" {
			t.Skipf("IPv6 loopback unavailable: %v", err)
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	observations := make(chan socksObservation, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		defer connection.Close()
		observation := socksObservation{remote: connection.RemoteAddr().String()}
		greeting := make([]byte, 2)
		if _, err := io.ReadFull(connection, greeting); err != nil {
			return
		}
		methods := make([]byte, int(greeting[1]))
		if _, err := io.ReadFull(connection, methods); err != nil {
			return
		}
		_, _ = connection.Write([]byte{5, 0})
		header := make([]byte, 4)
		if _, err := io.ReadFull(connection, header); err != nil {
			return
		}
		observation.atyp = header[3]
		switch observation.atyp {
		case 1:
			address := make([]byte, 4)
			if _, err := io.ReadFull(connection, address); err != nil {
				return
			}
			observation.host = net.IP(address).String()
		case 3:
			length := make([]byte, 1)
			if _, err := io.ReadFull(connection, length); err != nil {
				return
			}
			address := make([]byte, int(length[0]))
			if _, err := io.ReadFull(connection, address); err != nil {
				return
			}
			observation.host = string(address)
		case 4:
			address := make([]byte, 16)
			if _, err := io.ReadFull(connection, address); err != nil {
				return
			}
			observation.host = net.IP(address).String()
		default:
			return
		}
		port := make([]byte, 2)
		if _, err := io.ReadFull(connection, port); err != nil {
			return
		}
		_ = binary.BigEndian.Uint16(port)
		observations <- observation
		_, _ = connection.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		_, _ = io.Copy(io.Discard, connection)
	}()
	return &url.URL{Scheme: "socks5", Host: listener.Addr().String()}, observations
}
