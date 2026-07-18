package impersonate

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/proxy"
)

func newProxyDialContext(proxyURL *url.URL, timeout time.Duration) (func(context.Context, string, string) (net.Conn, error), error) {
	switch strings.ToLower(proxyURL.Scheme) {
	case "http", "https":
		dialer := &connectProxyDialer{proxyURL: cloneURL(proxyURL), timeout: timeout}
		return dialer.DialContext, nil
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if proxyURL.User != nil {
			password, _ := proxyURL.User.Password()
			auth = &proxy.Auth{User: proxyURL.User.Username(), Password: password}
		}
		dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, &net.Dialer{Timeout: timeout})
		if err != nil {
			return nil, fmt.Errorf("create SOCKS5 proxy dialer: %w", err)
		}
		contextDialer, ok := dialer.(proxy.ContextDialer)
		if !ok {
			return nil, errors.New("SOCKS5 proxy dialer does not support cancellation")
		}
		return contextDialer.DialContext, nil
	default:
		return nil, fmt.Errorf("unsupported impersonation proxy scheme %q", proxyURL.Scheme)
	}
}

type connectProxyDialer struct {
	proxyURL *url.URL
	timeout  time.Duration
}

// DialContext retains tls-client's all-target CONNECT behavior. In particular,
// HTTP origins are tunneled instead of being sent to the proxy in absolute
// form, which is observable by authenticated and policy-enforcing proxies.
func (dialer *connectProxyDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	proxyAddress := dialer.proxyURL.Host
	if dialer.proxyURL.Port() == "" {
		port := "80"
		if strings.EqualFold(dialer.proxyURL.Scheme, "https") {
			port = "443"
		}
		proxyAddress = net.JoinHostPort(dialer.proxyURL.Hostname(), port)
	}
	connection, err := (&net.Dialer{Timeout: dialer.timeout}).DialContext(ctx, network, proxyAddress)
	if err != nil {
		return nil, err
	}
	cancelConnection := connection
	dialComplete := make(chan struct{})
	defer close(dialComplete)
	go func() {
		select {
		case <-ctx.Done():
			_ = cancelConnection.Close()
		case <-dialComplete:
		}
	}()
	fail := func(err error) (net.Conn, error) {
		_ = connection.Close()
		return nil, err
	}

	negotiatedProtocol := ""
	if strings.EqualFold(dialer.proxyURL.Scheme, "https") {
		tlsConnection := tls.Client(connection, &tls.Config{
			ServerName: dialer.proxyURL.Hostname(),
			NextProtos: []string{"h2", "http/1.1"},
		})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return fail(err)
		}
		negotiatedProtocol = tlsConnection.ConnectionState().NegotiatedProtocol
		connection = tlsConnection
	}

	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	} else if dialer.timeout > 0 {
		_ = connection.SetDeadline(time.Now().Add(dialer.timeout))
	}
	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: make(http.Header),
	}
	if dialer.proxyURL.User != nil && dialer.proxyURL.User.Username() != "" {
		password, _ := dialer.proxyURL.User.Password()
		credentials := dialer.proxyURL.User.Username() + ":" + password
		request.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(credentials)))
	}
	if negotiatedProtocol == "h2" {
		tunnel, err := connectHTTP2Proxy(ctx, connection, request)
		if err != nil {
			return fail(err)
		}
		_ = connection.SetDeadline(time.Time{})
		return tunnel, nil
	}
	if negotiatedProtocol != "" && negotiatedProtocol != "http/1.1" {
		return fail(fmt.Errorf("proxy negotiated unsupported protocol %q", negotiatedProtocol))
	}
	if err := request.Write(connection); err != nil {
		return fail(err)
	}
	reader := bufio.NewReader(connection)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		return fail(err)
	}
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
		return fail(fmt.Errorf("proxy CONNECT failed with HTTP status %d", response.StatusCode))
	}
	_ = connection.SetDeadline(time.Time{})
	if reader.Buffered() != 0 {
		return &bufferedConn{Conn: connection, reader: reader}, nil
	}
	return connection, nil
}

func connectHTTP2Proxy(ctx context.Context, connection net.Conn, request *http.Request) (net.Conn, error) {
	transport := &http2.Transport{}
	client, err := transport.NewClientConn(connection)
	if err != nil {
		return nil, err
	}
	request = request.Clone(ctx)
	request.URL = &url.URL{Host: request.Host}
	request.Proto = "HTTP/2.0"
	request.ProtoMajor = 2
	request.ProtoMinor = 0
	reader, writer := io.Pipe()
	request.Body = reader
	response, err := client.RoundTrip(request)
	if err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = reader.Close()
		_ = writer.Close()
		_ = response.Body.Close()
		return nil, fmt.Errorf("proxy CONNECT failed with HTTP status %d", response.StatusCode)
	}
	return &http2ProxyConn{Conn: connection, writer: writer, reader: response.Body}, nil
}

type http2ProxyConn struct {
	net.Conn
	writer *io.PipeWriter
	reader io.ReadCloser
}

func (connection *http2ProxyConn) Read(buffer []byte) (int, error) {
	return connection.reader.Read(buffer)
}

func (connection *http2ProxyConn) Write(buffer []byte) (int, error) {
	return connection.writer.Write(buffer)
}

func (connection *http2ProxyConn) Close() error {
	writerErr := connection.writer.Close()
	readerErr := connection.reader.Close()
	connectionErr := connection.Conn.Close()
	return errors.Join(writerErr, readerErr, connectionErr)
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (connection *bufferedConn) Read(buffer []byte) (int, error) {
	return connection.reader.Read(buffer)
}

func cloneURL(source *url.URL) *url.URL {
	cloned := *source
	if source.User != nil {
		username := source.User.Username()
		if password, ok := source.User.Password(); ok {
			cloned.User = url.UserPassword(username, password)
		} else {
			cloned.User = url.User(username)
		}
	}
	return &cloned
}
