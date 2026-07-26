package hls

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/network"
)

const (
	maxSubtitleRenditions       = 128
	maxSubtitlePlaylistSegments = 10_000
	maxSubtitleSegmentBytes     = 1 << 20
	maxAssembledSubtitleBytes   = 16 << 20
)

// SubtitleRendition is one subtitle media declaration from an HLS master playlist.
type SubtitleRendition struct {
	URL      string
	Language string
	Name     string
}

// CredentialIsolatedSubtitleTransport fetches subtitle playlists and segments
// without ambient credentials, cookies, or redirects.
type CredentialIsolatedSubtitleTransport interface {
	DoWithoutCredentialsNoRedirect(context.Context, *http.Request) (*http.Response, error)
}

// ParseMasterSubtitles extracts TYPE=SUBTITLES EXT-X-MEDIA renditions from a
// bounded master playlist. Media playlists and unrelated tags are ignored.
func ParseMasterSubtitles(rawURL string, input []byte) ([]SubtitleRendition, error) {
	if len(input) > maxPlaylistBytes {
		return nil, fmt.Errorf("%w: playlist exceeds %d bytes", ErrInvalidPlaylist, maxPlaylistBytes)
	}
	base, err := parsePlaylistBase(rawURL)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(input)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	seenHeader := false
	renditions := make([]SubtitleRendition, 0, 4)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if !seenHeader {
			if line != "#EXTM3U" {
				return nil, fmt.Errorf("%w: line 1 must be #EXTM3U", ErrInvalidPlaylist)
			}
			seenHeader = true
			continue
		}
		if !strings.HasPrefix(line, "#EXT-X-MEDIA:") {
			continue
		}
		attributes, err := parseAttributes(strings.TrimPrefix(line, "#EXT-X-MEDIA:"))
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPlaylist, err)
		}
		mediaType := strings.ToUpper(strings.TrimSpace(attributes["TYPE"]))
		if mediaType != "SUBTITLES" {
			continue
		}
		rawURI := strings.TrimSpace(attributes["URI"])
		if rawURI == "" {
			continue
		}
		resolved, err := resolveURL(base, rawURI)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidPlaylist, err)
		}
		if len(renditions) >= maxSubtitleRenditions {
			return nil, fmt.Errorf("%w: subtitle rendition count exceeds %d", ErrInvalidPlaylist, maxSubtitleRenditions)
		}
		renditions = append(renditions, SubtitleRendition{
			URL:      resolved,
			Language: strings.TrimSpace(attributes["LANGUAGE"]),
			Name:     strings.TrimSpace(attributes["NAME"]),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPlaylist, err)
	}
	return renditions, nil
}

// AssembleWebVTT downloads a bounded VOD HLS subtitle media playlist through a
// credential-isolated transport and concatenates its WebVTT segments into one
// document. Unsupported encryption, byte ranges, and initialization maps fail
// closed instead of producing corrupt output.
func AssembleWebVTT(ctx context.Context, transport CredentialIsolatedSubtitleTransport, manifestURL string, maxBytes int64) ([]byte, error) {
	if transport == nil {
		return nil, fmt.Errorf("%w: missing subtitle transport", ErrInvalidPlaylist)
	}
	if maxBytes <= 0 || maxBytes > maxAssembledSubtitleBytes {
		maxBytes = maxAssembledSubtitleBytes
	}
	body, err := readSubtitlePage(ctx, transport, manifestURL, maxPlaylistBytes)
	if err != nil {
		return nil, err
	}
	playlist, err := Parse(manifestURL, body)
	if err != nil {
		return nil, err
	}
	media := playlist.Media
	if media == nil {
		if len(playlist.Variants) == 0 {
			return nil, fmt.Errorf("%w: subtitle playlist has no media", ErrInvalidPlaylist)
		}
		body, err = readSubtitlePage(ctx, transport, playlist.Variants[0].URL, maxPlaylistBytes)
		if err != nil {
			return nil, err
		}
		playlist, err = Parse(playlist.Variants[0].URL, body)
		if err != nil || playlist.Media == nil {
			return nil, fmt.Errorf("%w: subtitle variant playlist invalid", ErrInvalidPlaylist)
		}
		media = playlist.Media
	}
	if err := validateSubtitleMediaPlaylist(media); err != nil {
		return nil, err
	}
	var assembled bytes.Buffer
	wroteSegment := false
	for _, segment := range media.Segments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if segment.Advertisement || segment.URL == "" {
			continue
		}
		if err := validateSubtitleSegment(segment); err != nil {
			return nil, err
		}
		payload, err := readSubtitlePage(ctx, transport, segment.URL, maxSubtitleSegmentBytes)
		if err != nil {
			return nil, err
		}
		if !wroteSegment {
			if _, err := assembled.Write(payload); err != nil {
				return nil, err
			}
			if int64(assembled.Len()) > maxBytes {
				return nil, fmt.Errorf("%w: assembled subtitle exceeds %d bytes", ErrInvalidPlaylist, maxBytes)
			}
			wroteSegment = true
			continue
		}
		fragment := stripWebVTTHeader(payload)
		if len(fragment) == 0 {
			continue
		}
		if assembled.Len() > 0 && !bytes.HasSuffix(assembled.Bytes(), []byte("\n")) {
			assembled.WriteByte('\n')
		}
		if _, err := assembled.Write(fragment); err != nil {
			return nil, err
		}
		if int64(assembled.Len()) > maxBytes {
			return nil, fmt.Errorf("%w: assembled subtitle exceeds %d bytes", ErrInvalidPlaylist, maxBytes)
		}
	}
	if assembled.Len() == 0 {
		return nil, fmt.Errorf("%w: assembled subtitle is empty", ErrInvalidPlaylist)
	}
	return assembled.Bytes(), nil
}

func validateSubtitleMediaPlaylist(media *MediaPlaylist) error {
	if media == nil {
		return fmt.Errorf("%w: subtitle playlist has no media", ErrInvalidPlaylist)
	}
	if !media.EndList {
		return fmt.Errorf("%w: live subtitle playlists are unsupported", ErrInvalidPlaylist)
	}
	if len(media.Segments) == 0 {
		return fmt.Errorf("%w: subtitle playlist has no segments", ErrInvalidPlaylist)
	}
	if len(media.Segments) > maxSubtitlePlaylistSegments {
		return fmt.Errorf("%w: subtitle segment count exceeds %d", ErrInvalidPlaylist, maxSubtitlePlaylistSegments)
	}
	for _, segment := range media.Segments {
		if segment.Advertisement || segment.URL == "" {
			continue
		}
		if err := validateSubtitleSegment(segment); err != nil {
			return err
		}
	}
	return nil
}

func validateSubtitleSegment(segment Segment) error {
	if segment.Key != nil {
		return fmt.Errorf("%w: encrypted subtitle segments are unsupported", ErrUnsupportedEncryption)
	}
	if segment.Map != nil {
		return fmt.Errorf("%w: subtitle initialization maps are unsupported", ErrInvalidPlaylist)
	}
	if segment.RangeStart != 0 || segment.RangeLength != 0 {
		return fmt.Errorf("%w: byte-range subtitle segments are unsupported", ErrInvalidPlaylist)
	}
	return nil
}

func readSubtitlePage(ctx context.Context, transport CredentialIsolatedSubtitleTransport, rawURL string, limit int64) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPlaylist, err)
	}
	response, err := transport.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &network.StatusError{Code: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("%w: response exceeds %d bytes", ErrInvalidPlaylist, limit)
	}
	return body, nil
}

func stripWebVTTHeader(payload []byte) []byte {
	text := string(payload)
	text = strings.TrimPrefix(text, "\uFEFF")
	if !strings.HasPrefix(text, "WEBVTT") {
		return bytes.TrimSpace(payload)
	}
	lines := strings.Split(text, "\n")
	start := 1
	for start < len(lines) {
		line := strings.TrimSpace(lines[start])
		if line == "" {
			start++
			break
		}
		if strings.Contains(line, "-->") {
			break
		}
		start++
	}
	if start >= len(lines) {
		return nil
	}
	return bytes.TrimSpace([]byte(strings.Join(lines[start:], "\n")))
}

func parsePlaylistBase(rawURL string) (*url.URL, error) {
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: base URL: %v", ErrInvalidPlaylist, err)
	}
	return base, nil
}
