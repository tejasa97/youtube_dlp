// Package testserver provides deterministic offline HTTP scenarios for tests.
package testserver

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const mediaSize = 256 * 1024

// Server owns an isolated deterministic fixture HTTP server.
type Server struct {
	*httptest.Server
	media    []byte
	mediaTag string
	revision atomic.Uint64
}

// New starts a fixture server. Call Close when it is no longer needed.
func New() *Server {
	server := &Server{media: makeMedia(mediaSize)}
	digest := sha256.Sum256(server.media)
	server.mediaTag = `"sha256-` + hex.EncodeToString(digest[:]) + `"`
	server.Server = httptest.NewServer(server.handler())
	return server
}

// Media returns a copy of the deterministic direct-media body.
func (server *Server) Media() []byte {
	return append([]byte(nil), server.media...)
}

func (server *Server) MediaETag() string { return server.mediaTag }

func (server *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/page", server.handlePage)
	mux.HandleFunc("/media", server.handleMedia)
	mux.HandleFunc("/redirect", server.handleRedirect)
	mux.HandleFunc("/gzip", server.handleGzip)
	mux.HandleFunc("/cookies/set", server.handleCookieSet)
	mux.HandleFunc("/cookies/check", server.handleCookieCheck)
	mux.HandleFunc("/slow", server.handleSlow)
	mux.HandleFunc("/disconnect", server.handleDisconnect)
	mux.HandleFunc("/mutable", server.handleMutable)
	return mux
}

func (server *Server) handlePage(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}

	format := value.NewObject(
		value.Field{Key: "format_id", Value: value.String("direct-http")},
		value.Field{Key: "url", Value: value.String(origin(request) + "/media")},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "filesize", Value: value.Int(int64(len(server.media)))},
	)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture-direct")},
		value.Field{Key: "title", Value: value.String("Deterministic Fixture")},
		value.Field{Key: "webpage_url", Value: value.String(origin(request) + request.URL.Path)},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
	)

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(writer).Encode(value.ObjectValue(info)); err != nil {
		panic(err)
	}
}

func (server *Server) handleMedia(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead)
		return
	}

	writer.Header().Set("Accept-Ranges", "bytes")
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("ETag", server.mediaTag)

	start, end, ranged, err := requestedRange(request, len(server.media), server.mediaTag)
	if err != nil {
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(server.media)))
		http.Error(writer, "range not satisfiable", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	body := server.media
	if ranged {
		body = server.media[start : end+1]
		writer.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(server.media)))
	}
	writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if ranged {
		writer.WriteHeader(http.StatusPartialContent)
	}
	if request.Method == http.MethodHead {
		return
	}
	_, _ = writer.Write(body)
}

func (server *Server) handleRedirect(writer http.ResponseWriter, request *http.Request) {
	http.Redirect(writer, request, "/page", http.StatusFound)
}

func (server *Server) handleGzip(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Encoding", "gzip")
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	compressor := gzip.NewWriter(writer)
	_, _ = compressor.Write([]byte("deterministic gzip response\n"))
	_ = compressor.Close()
}

func (server *Server) handleCookieSet(writer http.ResponseWriter, request *http.Request) {
	http.SetCookie(writer, &http.Cookie{Name: "fixture_session", Value: "accepted", Path: "/", HttpOnly: true})
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleCookieCheck(writer http.ResponseWriter, request *http.Request) {
	cookie, err := request.Cookie("fixture_session")
	if err != nil || cookie.Value != "accepted" {
		http.Error(writer, "fixture cookie required", http.StatusUnauthorized)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (server *Server) handleSlow(writer http.ResponseWriter, request *http.Request) {
	delay := 200 * time.Millisecond
	if raw := request.URL.Query().Get("delay"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed >= 0 && parsed <= 2*time.Second {
			delay = parsed
		}
	}
	select {
	case <-request.Context().Done():
		return
	case <-time.After(delay):
		_, _ = writer.Write([]byte("slow response completed\n"))
	}
}

func (server *Server) handleDisconnect(writer http.ResponseWriter, _ *http.Request) {
	hijacker, ok := writer.(http.Hijacker)
	if !ok {
		http.Error(writer, "connection hijacking unavailable", http.StatusNotImplemented)
		return
	}
	connection, buffer, err := hijacker.Hijack()
	if err != nil {
		return
	}
	_, _ = buffer.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 1024\r\nContent-Type: application/octet-stream\r\n\r\npartial")
	_ = buffer.Flush()
	_ = connection.Close()
}

func (server *Server) handleMutable(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodPost:
		server.revision.Add(1)
		writer.WriteHeader(http.StatusNoContent)
	case http.MethodGet, http.MethodHead:
		revision := server.revision.Load()
		body := []byte(fmt.Sprintf("fixture revision %d\n", revision))
		writer.Header().Set("ETag", fmt.Sprintf(`"revision-%d"`, revision))
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if request.Method == http.MethodGet {
			_, _ = writer.Write(body)
		}
	default:
		methodNotAllowed(writer, http.MethodGet+", "+http.MethodHead+", "+http.MethodPost)
	}
}

func requestedRange(request *http.Request, size int, etag string) (start, end int, ranged bool, err error) {
	raw := request.Header.Get("Range")
	if raw == "" || (request.Header.Get("If-Range") != "" && request.Header.Get("If-Range") != etag) {
		return 0, size - 1, false, nil
	}
	if !strings.HasPrefix(raw, "bytes=") || strings.Contains(raw, ",") {
		return 0, 0, false, fmt.Errorf("unsupported range %q", raw)
	}
	parts := strings.Split(strings.TrimPrefix(raw, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" {
		return 0, 0, false, fmt.Errorf("unsupported range %q", raw)
	}
	start, err = strconv.Atoi(parts[0])
	if err != nil || start < 0 || start >= size {
		return 0, 0, false, fmt.Errorf("invalid range start")
	}
	end = size - 1
	if parts[1] != "" {
		end, err = strconv.Atoi(parts[1])
		if err != nil || end < start {
			return 0, 0, false, fmt.Errorf("invalid range end")
		}
		if end >= size {
			end = size - 1
		}
	}
	return start, end, true, nil
}

func origin(request *http.Request) string {
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + request.Host
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}

func makeMedia(size int) []byte {
	media := make([]byte, size)
	for index := range media {
		media[index] = byte((index*31 + index/251) % 251)
	}
	return media
}
