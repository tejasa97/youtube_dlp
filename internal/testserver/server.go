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
	mux.HandleFunc("/headers", server.handleHeaders)
	mux.HandleFunc("/large", server.handleLarge)
	mux.HandleFunc("/subs/en.srt", fixedBody("application/x-subrip", []byte("1\n00:00:00,000 --> 00:00:01,000\nmanual english\n")))
	mux.HandleFunc("/subs/en.vtt", server.handleHeaderProtectedSubtitle)
	mux.HandleFunc("/subs/es.vtt", fixedBody("text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nmanual spanish\n")))
	mux.HandleFunc("/subs/fr.vtt", fixedBody("text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nmanual french\n")))
	mux.HandleFunc("/subs/auto-es.vtt", fixedBody("text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nautomatic spanish\n")))
	mux.HandleFunc("/subs/auto-pt.vtt", fixedBody("text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nautomatic portuguese\n")))
	mux.HandleFunc("/hls/master.m3u8", server.handleHLSMaster)
	mux.HandleFunc("/hls/high.m3u8", server.handleHLSHigh)
	mux.HandleFunc("/hls/low.m3u8", server.handleHLSLow)
	mux.HandleFunc("/hls/init.bin", fixedBody("application/octet-stream", []byte("hls-init-")))
	mux.HandleFunc("/hls/one.bin", fixedBody("application/octet-stream", []byte("hls-one-")))
	mux.HandleFunc("/hls/two.bin", fixedBody("application/octet-stream", []byte("hls-two")))
	mux.HandleFunc("/dash/manifest.mpd", server.handleDASHManifest)
	mux.HandleFunc("/dash/init.bin", fixedBody("application/octet-stream", []byte("dash-init-")))
	mux.HandleFunc("/dash/1.bin", fixedBody("application/octet-stream", []byte("dash-one-")))
	mux.HandleFunc("/dash/2.bin", fixedBody("application/octet-stream", []byte("dash-two")))
	return mux
}

func (server *Server) HLSMedia() []byte  { return []byte("hls-init-hls-one-hls-two") }
func (server *Server) DASHMedia() []byte { return []byte("dash-init-dash-one-dash-two") }

func (server *Server) handleHLSMaster(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = writer.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=100\nlow.m3u8\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nhigh.m3u8\n"))
}

func (server *Server) handleHLSHigh(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = writer.Write([]byte("#EXTM3U\n#EXT-X-MAP:URI=init.bin\n#EXTINF:1,\none.bin\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n"))
}

func (server *Server) handleHLSLow(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = writer.Write([]byte("#EXTM3U\n#EXTINF:1,\none.bin\n#EXT-X-ENDLIST\n"))
}

func (server *Server) handleDASHManifest(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/dash+xml")
	_, _ = writer.Write([]byte(`<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="fixture" bandwidth="1000"><SegmentTemplate duration="1" initialization="init.bin" media="$Number$.bin"/></Representation></AdaptationSet></Period></MPD>`))
}

func fixedBody(contentType string, body []byte) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", contentType)
		writer.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if request.Method != http.MethodHead {
			_, _ = writer.Write(body)
		}
	}
}

func (server *Server) handleHeaders(writer http.ResponseWriter, request *http.Request) {
	writer.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(writer, `{"user_agent":%q,"x_fixture":%q}`+"\n", request.UserAgent(), request.Header.Get("X-Fixture"))
}

func (server *Server) handleLarge(writer http.ResponseWriter, request *http.Request) {
	size, _ := strconv.Atoi(request.URL.Query().Get("size"))
	if size < 0 || size > 1<<20 {
		size = 0
	}
	writer.Header().Set("Content-Length", strconv.Itoa(size))
	_, _ = writer.Write(make([]byte, size))
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
	subtitle := func(path, extension string) value.Value {
		return value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(origin(request) + path)},
			value.Field{Key: "ext", Value: value.String(extension)},
		))
	}
	englishVTT := subtitle("/subs/en.vtt", "vtt")
	englishVTTObject, _ := englishVTT.Object()
	englishVTTObject.Set("http_headers", value.ObjectValue(value.NewObject(
		value.Field{Key: "X-Subtitle", Value: value.String("subtitle")},
	)))
	manualSubtitles := value.NewObject(
		value.Field{Key: "en", Value: value.List(subtitle("/subs/en.srt", "srt"), englishVTT)},
		value.Field{Key: "es", Value: value.List(subtitle("/subs/es.vtt", "vtt"))},
		value.Field{Key: "fr", Value: value.List(subtitle("/subs/fr.vtt", "vtt"))},
	)
	automaticCaptions := value.NewObject(
		value.Field{Key: "es", Value: value.List(subtitle("/subs/auto-es.vtt", "vtt"))},
		value.Field{Key: "pt", Value: value.List(subtitle("/subs/auto-pt.vtt", "vtt"))},
	)
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture-direct")},
		value.Field{Key: "title", Value: value.String("Deterministic Fixture")},
		value.Field{Key: "webpage_url", Value: value.String(origin(request) + request.URL.Path)},
		value.Field{Key: "ext", Value: value.String("bin")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(format))},
		value.Field{Key: "http_headers", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "X-Global", Value: value.String("global")},
			value.Field{Key: "X-Subtitle", Value: value.String("global-value-must-be-overridden")},
		))},
		value.Field{Key: "subtitles", Value: value.ObjectValue(manualSubtitles)},
		value.Field{Key: "automatic_captions", Value: value.ObjectValue(automaticCaptions)},
	)

	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(writer).Encode(value.ObjectValue(info)); err != nil {
		panic(err)
	}
}

func (server *Server) handleHeaderProtectedSubtitle(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("X-Global") != "global" || request.Header.Get("X-Subtitle") != "subtitle" {
		http.Error(writer, "missing subtitle headers", http.StatusForbidden)
		return
	}
	fixedBody("text/vtt", []byte("WEBVTT\n\n00:00.000 --> 00:01.000\nmanual english\n"))(writer, request)
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
