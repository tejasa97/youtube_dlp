package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const cloudflareStreamMaxIDBytes = 4096

var (
	cloudflareStreamHexID = regexp.MustCompile(`^[0-9a-f]{32}$`)
	cloudflareStreamJWT   = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`)
	cloudflareStreamCust  = regexp.MustCompile(`^customer-[a-z0-9-]{1,63}\.cloudflarestream\.com$`)
)

// CloudflareStream extracts public Stream watch, iframe, embed-script, and
// manifest URLs. Signed JWT delivery tokens stay in manifest/thumbnail URLs;
// only the returned metadata id is reduced to the JWT subject. DRM and account
// APIs are out of scope.
type CloudflareStream struct{}

func NewCloudflareStream() CloudflareStream { return CloudflareStream{} }
func (CloudflareStream) Name() string       { return "cloudflarestream" }

func (CloudflareStream) Suitable(parsed *url.URL) bool {
	_, ok := parseCloudflareStreamURL(parsed)
	return ok
}

func (CloudflareStream) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseCloudflareStreamURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	return normalizeCloudflareStream(target)
}

type cloudflareStreamTarget struct {
	// deliveryID is the raw hex id or signed JWT used in Stream media URLs.
	deliveryID string
	// videoID is the public metadata id (JWT sub when deliveryID is signed).
	videoID   string
	domain    string
	canonical string
}

func parseCloudflareStreamURL(parsed *url.URL) (cloudflareStreamTarget, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return cloudflareStreamTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	domain, ok := cloudflareStreamDomain(host)
	if !ok {
		return cloudflareStreamTarget{}, false
	}
	deliveryID, videoID, ok := cloudflareStreamIDs(parsed)
	if !ok {
		return cloudflareStreamTarget{}, false
	}
	canonicalDomain := domain
	if domain != "bytehighway.net" {
		canonicalDomain = "cloudflarestream.com"
	}
	return cloudflareStreamTarget{
		deliveryID: deliveryID,
		videoID:    videoID,
		domain:     canonicalDomain,
		canonical:  "https://" + canonicalDomain + "/" + deliveryID,
	}, true
}

func cloudflareStreamDomain(host string) (string, bool) {
	switch host {
	case "cloudflarestream.com", "watch.cloudflarestream.com", "iframe.cloudflarestream.com", "embed.cloudflarestream.com":
		return "cloudflarestream.com", true
	case "videodelivery.net", "embed.videodelivery.net", "watch.videodelivery.net", "iframe.videodelivery.net":
		return "videodelivery.net", true
	case "bytehighway.net", "embed.bytehighway.net", "watch.bytehighway.net", "iframe.bytehighway.net":
		return "bytehighway.net", true
	}
	if cloudflareStreamCust.MatchString(host) {
		return "cloudflarestream.com", true
	}
	return "", false
}

func cloudflareStreamIDs(parsed *url.URL) (deliveryID, videoID string, ok bool) {
	if queryID := strings.TrimSpace(parsed.Query().Get("video")); queryID != "" {
		return normalizeCloudflareStreamID(queryID)
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" || segments[0] == "embed" || segments[0] == "manifest" {
		return "", "", false
	}
	return normalizeCloudflareStreamID(segments[0])
}

func normalizeCloudflareStreamID(raw string) (deliveryID, videoID string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > cloudflareStreamMaxIDBytes || strings.ContainsAny(raw, " \x00\r\n\t/?#") {
		return "", "", false
	}
	if cloudflareStreamHexID.MatchString(raw) {
		return raw, raw, true
	}
	if !cloudflareStreamJWT.MatchString(raw) {
		return "", "", false
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return "", "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(payload) == 0 || int64(len(payload)) > maxExtractorJSONBytes {
		return "", "", false
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", "", false
	}
	sub := strings.TrimSpace(claims.Sub)
	if !cloudflareStreamHexID.MatchString(sub) {
		return "", "", false
	}
	return raw, sub, true
}

func normalizeCloudflareStream(target cloudflareStreamTarget) (Extraction, error) {
	// Match the pinned reference: media URLs keep the original delivery token;
	// only metadata id/title use the public subject id.
	base := "https://" + target.domain + "/" + target.deliveryID + "/"
	formats := make([]value.Value, 0, 2)
	if format, ok := strictHostedURLFormat("hls", base+"manifest/video.m3u8"); ok {
		formats = append(formats, value.ObjectValue(format))
	}
	if format, ok := strictHostedURLFormat("dash", base+"manifest/video.mpd"); ok {
		formats = append(formats, value.ObjectValue(format))
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.videoID)},
		value.Field{Key: "title", Value: value.String(target.videoID)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "thumbnail", Value: value.String(base + "thumbnails/thumbnail.jpg")},
	)
	return Media(value.NewInfo(info)), nil
}
