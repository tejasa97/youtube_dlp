package extractor

// Rai is deliberately implemented as one family backend.  The upstream
// extractors share a relinker protocol but their page JSON is not identical;
// route classification is therefore kept separate from metadata decoding.

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

const (
	raiMaxURLLength = 4096
	raiMaxJSON      = 16 << 20
	raiMaxXML       = 2 << 20
	raiMaxFormats   = 64
	raiMaxSubs      = 32
	raiMaxThumbs    = 32
	raiMaxEntries   = 10000

	// raiMaxMP4Probe mirrors the bounded HEAD probe used to gate the pinned
	// _create_http_urls MP4 synthesis path.  It must stay tiny because the
	// response body of a HEAD probe is expected to be empty; any payload above
	// this is rejected as a contract violation.
	raiMaxMP4Probe int64 = 1 << 14
	// raiMaxMP4Qualities caps the parsed manifest quality list so a hostile
	// or pathological relinker cannot drive unbounded synthesis.
	raiMaxMP4Qualities = 32
)

var (
	raiUUID            = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	raiUUIDPath        = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	raiNewsCulturaPath = regexp.MustCompile(`(?i)^/.*?-([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:-[^/]+)?\.html$`)

	// raiMP4Manifest extracts the bounded quality list from a Rai manifest
	// URL path.  It mirrors the pinned `_MANIFEST_REG` in
	// yt_dlp/extractor/rai.py (aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8).
	raiMP4Manifest = regexp.MustCompile(`/(?P<id>\w+)(?:_(?P<quality>[\d,]+))?(?:\.mp4)?(?:\.csmil)?/playlist\.m3u8$`)
	// raiMP4QualitySelector only matches a single digit-and-comma quality
	// token; the manifest quality list is comma separated and bounded to
	// values in `raiMP4QualityTable`.
	raiMP4QualitySelector = regexp.MustCompile(`^[\d,]+$`)
)

// raiMP4QualityTable mirrors the pinned `_QUALITY` mapping in
// yt_dlp/extractor/rai.py (aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8).  It
// records (width, height) for each enumerated bitrate token.  Unknown
// qualities are rejected by the synthesis path; they never synthesize
// unsupported dimensions.
var raiMP4QualityTable = map[int][2]int{
	250:   {352, 198},
	400:   {512, 288},
	600:   {512, 288},
	700:   {512, 288},
	800:   {700, 394},
	1200:  {736, 414},
	1500:  {920, 518},
	1800:  {1024, 576},
	2400:  {1280, 720},
	3200:  {1440, 810},
	3600:  {1440, 810},
	5000:  {1920, 1080},
	10000: {1920, 1080},
}

// Concrete types are intentionally tiny.  Their keys match yt-dlp's public
// classes while all extraction is owned by raiAdapter.
type RaiPlay struct{}

func NewRaiPlay() RaiPlay                { return RaiPlay{} }
func (RaiPlay) Name() string             { return "raiplay" }
func (RaiPlay) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiPlayVOD }
func (RaiPlay) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiPlayVOD)
}

type RaiPlayLive struct{}

func NewRaiPlayLive() RaiPlayLive            { return RaiPlayLive{} }
func (RaiPlayLive) Name() string             { return "raiplay_live" }
func (RaiPlayLive) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiPlayLive }
func (RaiPlayLive) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiPlayLive)
}

type RaiPlayPlaylist struct{}

func NewRaiPlayPlaylist() RaiPlayPlaylist        { return RaiPlayPlaylist{} }
func (RaiPlayPlaylist) Name() string             { return "raiplay_playlist" }
func (RaiPlayPlaylist) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiPlayPlaylist }
func (RaiPlayPlaylist) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiPlayPlaylist)
}

type RaiPlaySound struct{}

func NewRaiPlaySound() RaiPlaySound           { return RaiPlaySound{} }
func (RaiPlaySound) Name() string             { return "raiplaysound" }
func (RaiPlaySound) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiSoundVOD }
func (RaiPlaySound) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiSoundVOD)
}

type RaiPlaySoundLive struct{}

func NewRaiPlaySoundLive() RaiPlaySoundLive       { return RaiPlaySoundLive{} }
func (RaiPlaySoundLive) Name() string             { return "raiplaysound_live" }
func (RaiPlaySoundLive) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiSoundLive }
func (RaiPlaySoundLive) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiSoundLive)
}

type RaiPlaySoundPlaylist struct{}

func NewRaiPlaySoundPlaylist() RaiPlaySoundPlaylist   { return RaiPlaySoundPlaylist{} }
func (RaiPlaySoundPlaylist) Name() string             { return "raiplaysound_playlist" }
func (RaiPlaySoundPlaylist) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiSoundPlaylist }
func (RaiPlaySoundPlaylist) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiSoundPlaylist)
}

type Rai struct{}

func NewRai() Rai                    { return Rai{} }
func (Rai) Name() string             { return "rai" }
func (Rai) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiLegacy }
func (Rai) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiLegacy)
}

type RaiNews struct{}

func NewRaiNews() RaiNews                { return RaiNews{} }
func (RaiNews) Name() string             { return "rainews" }
func (RaiNews) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiNews }
func (RaiNews) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiNews)
}

type RaiCultura struct{}

func NewRaiCultura() RaiCultura             { return RaiCultura{} }
func (RaiCultura) Name() string             { return "raicultura" }
func (RaiCultura) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiCultura }
func (RaiCultura) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiCultura)
}

type RaiSudtirol struct{}

func NewRaiSudtirol() RaiSudtirol            { return RaiSudtirol{} }
func (RaiSudtirol) Name() string             { return "raisudtirol" }
func (RaiSudtirol) Suitable(u *url.URL) bool { return raiClassify(u).kind == raiSudtirol }
func (RaiSudtirol) Extract(c context.Context, r Request) (Extraction, error) {
	return raiExtract(c, r, raiSudtirol)
}

type raiKind uint8

const (
	raiNone raiKind = iota
	raiPlayVOD
	raiPlayLive
	raiPlayPlaylist
	raiSoundVOD
	raiSoundLive
	raiSoundPlaylist
	raiLegacy
	raiNews
	raiCultura
	raiSudtirol
)

type raiTarget struct {
	kind            raiKind
	id, base, extra string
}

func raiClassify(u *url.URL) raiTarget {
	if !raiSafePageURL(u) {
		return raiTarget{}
	}
	host := strings.ToLower(u.Hostname())
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	lastUUID := strings.ToLower(raiUUIDPath.FindString(u.Path))
	base := u.Scheme + "://" + u.Host + strings.TrimSuffix(u.Path, ".html")
	switch host {
	case "raiplay.it", "www.raiplay.it":
		if len(parts) >= 2 && parts[0] == "dirette" && raiSafeID(parts[1]) {
			return raiTarget{raiPlayLive, parts[1], base, ""}
		}
		if len(parts) >= 2 && parts[0] == "programmi" && raiSafeID(parts[1]) {
			return raiTarget{raiPlayPlaylist, parts[1], u.Scheme + "://" + u.Host + "/programmi/" + parts[1], strings.Join(parts[2:], "/")}
		}
		if lastUUID != "" && (strings.HasSuffix(u.Path, ".html") || strings.HasSuffix(u.Path, ".json")) {
			return raiTarget{raiPlayVOD, lastUUID, strings.TrimSuffix(strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, ".html"), ".json"), ""}
		}
	case "raiplaysound.it", "www.raiplaysound.it":
		if len(parts) >= 2 && (parts[0] == "programmi" || parts[0] == "playlist" || parts[0] == "audiolibri") && raiSafeID(parts[1]) {
			return raiTarget{raiSoundPlaylist, parts[1], u.Scheme + "://" + u.Host + "/" + parts[0] + "/" + parts[1], strings.Join(parts[2:], "/")}
		}
		if lastUUID != "" && (strings.HasSuffix(u.Path, ".html") || strings.HasSuffix(u.Path, ".json")) {
			return raiTarget{raiSoundVOD, lastUUID, strings.TrimSuffix(strings.TrimSuffix(u.Scheme+"://"+u.Host+u.Path, ".html"), ".json"), ""}
		}
		if len(parts) == 1 && raiSafeID(parts[0]) {
			return raiTarget{raiSoundLive, parts[0], base, ""}
		}
	case "rainews.it", "www.rainews.it":
		if id, ok := raiNewsCulturaID(u.Path); ok {
			return raiTarget{raiNews, id, u.String(), ""}
		}
	case "raicultura.it", "www.raicultura.it":
		if id, ok := raiNewsCulturaID(u.Path); ok {
			return raiTarget{raiCultura, id, u.String(), ""}
		}
	case "raibz.rai.it", "raisudtirol.rai.it":
		for key := range u.Query() {
			if key != "media" && key != "lang" {
				return raiTarget{}
			}
		}
		id := u.Query().Get("media")
		if strings.EqualFold(path.Ext(id), ".smil") {
			id = id[:len(id)-len(path.Ext(id))]
		}
		if raiSafeID(id) {
			return raiTarget{raiSudtirol, id, u.String(), ""}
		}
	default:
		if strings.HasSuffix(host, ".rai.it") || strings.HasSuffix(host, ".rai.tv") {
			if lastUUID != "" && strings.HasSuffix(u.Path, ".html") {
				return raiTarget{raiLegacy, lastUUID, u.String(), ""}
			}
		}
	}
	return raiTarget{}
}

func raiSafePageURL(u *url.URL) bool {
	if u == nil || len(u.String()) > raiMaxURLLength || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Port() != "" || u.Fragment != "" || u.ForceQuery || u.RawPath != "" || u.Path == "" {
		return false
	}
	if strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") || strings.Contains(strings.ToLower(u.EscapedPath()), "%00") || strings.Contains(u.Path, "..") {
		return false
	}
	for key, vals := range u.Query() {
		if len(vals) != 1 || len(key) > 64 || len(vals[0]) > 512 {
			return false
		}
	}
	return true
}
func raiSafeID(id string) bool {
	return len(id) > 0 && len(id) <= 256 && regexp.MustCompile(`^[A-Za-z0-9_.-]+$`).MatchString(id)
}

func raiExtract(ctx context.Context, request Request, want raiKind) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	u, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target := raiClassify(u)
	if target.kind != want {
		return Extraction{}, ErrUnsupported
	}
	switch want {
	case raiPlayPlaylist, raiSoundPlaylist:
		return raiPlaylist(ctx, request, target)
	case raiLegacy:
		return raiLegacyExtract(ctx, request, target)
	case raiNews, raiCultura:
		return raiNewsExtract(ctx, request, target)
	case raiSudtirol:
		return raiSudtirolExtract(ctx, request, target)
	default:
		return raiJSONItem(ctx, request, target)
	}
}

func raiJSON(ctx context.Context, t Transport, endpoint string, out *map[string]any) error {
	if !raiEndpoint(endpoint) {
		return fmt.Errorf("%w: unsafe Rai endpoint", ErrInvalidMetadata)
	}
	if err := requestRiskJSON(ctx, t, http.MethodGet, endpoint, nil, make(http.Header), "", out); err != nil {
		return raiCategorize(err)
	}
	return nil
}
func raiEndpoint(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || !raiSafePageURL(u) {
		return false
	}
	h := strings.ToLower(u.Hostname())
	return h == "raiplay.it" || h == "www.raiplay.it" || h == "raiplaysound.it" || h == "www.raiplaysound.it" || strings.HasSuffix(h, ".rai.it") || strings.HasSuffix(h, ".rai.tv")
}
func raiCategorize(err error) error {
	switch riskHTTPStatus(err) {
	case 401, 403:
		return ErrAuthentication
	case 404, 410:
		return ErrUnavailable
	case 451:
		return ErrRegionRestricted
	}
	return err
}

func raiJSONItem(ctx context.Context, request Request, target raiTarget) (Extraction, error) {
	endpoint := target.base + ".json"
	if target.kind == raiPlayLive || target.kind == raiSoundLive {
		endpoint = target.base + ".json"
	}
	var media map[string]any
	if err := raiJSON(ctx, request.Transport, endpoint, &media); err != nil {
		return Extraction{}, err
	}
	if raiBoolPath(media, "rights_management", "rights", "drm") {
		return Extraction{}, ErrUnavailable
	}
	video := raiMap(media["video"])
	if video == nil {
		video = media
	}
	relinkers := []string{raiString(video["content_url"])}
	if target.kind == raiSoundVOD || target.kind == raiSoundLive {
		relinkers = []string{
			raiString(media["downloadable_audio"]),
			raiStringPath(media, "downloadable_audio", "url"),
			raiStringPath(media, "audio", "url"),
			raiStringPath(media, "live", "url"),
			raiStringPath(media, "live", "cards", "0", "audio", "url"),
		}
		if r := raiString(video["content_url"]); r != "" {
			relinkers = append(relinkers, r)
		}
	}
	formats := []value.Value{}
	seenRelinkers := make(map[string]bool)
	seenFormats := make(map[string]bool)
	live := false
	duration := raiDuration(video["duration"])
	for _, relinker := range relinkers {
		if relinker == "" || seenRelinkers[relinker] {
			continue
		}
		seenRelinkers[relinker] = true
		info, err := raiRelinker(ctx, request.Transport, relinker, target.id, target.kind == raiSoundVOD || target.kind == raiSoundLive)
		if err != nil {
			return Extraction{}, err
		}
		for _, format := range info.formats {
			formatObject, ok := format.Object()
			if !ok {
				continue
			}
			formatURL, ok := formatObject.Lookup("url").StringValue()
			if !ok || seenFormats[formatURL] {
				continue
			}
			seenFormats[formatURL] = true
			formats = append(formats, format)
		}
		live = live || info.live
		if duration == 0 {
			duration = info.duration
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	id, present := raiContentIdentity(media, target)
	if !present {
		id = target.id
	} else if !raiValidContentIdentity(id, target) {
		return Extraction{}, fmt.Errorf("%w: Rai content identity mismatch", ErrInvalidMetadata)
	}
	title := raiFirst(raiString(media["name"]), raiString(media["title"]), raiString(media["episode_title"]))
	if title == "" {
		title = target.id
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "display_id", Value: value.String(target.id)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)}, value.Field{Key: "formats", Value: value.List(formats...)})
	raiSetString(info, "description", raiString(media["description"]))
	raiSetString(info, "alt_title", raiJoin(raiString(media["subtitle"]), raiString(media["toptitle"])))
	raiSetString(info, "uploader", raiFirst(raiStringPath(media, "program_info", "channel"), raiString(media["channel"]), raiStringPath(media, "track_info", "channel")))
	raiSetString(info, "creator", raiFirst(raiStringPath(media, "program_info", "editor"), raiString(media["editor"]), raiStringPath(media, "track_info", "editor")))
	if duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	if live {
		info.Set("is_live", value.Bool(true))
		info.Set("live_status", value.String("is_live"))
	}
	if target.kind == raiSoundVOD || target.kind == raiSoundLive {
		podcast := raiSoundPodcastInfo(media)
		raiSetString(info, "series", raiString(podcast["title"]))
		raiAddImages(info, request.URL, raiMap(podcast["images"]))
	} else {
		raiSetString(info, "series", raiStringPath(media, "program_info", "name"))
		raiAddImages(info, request.URL, raiMap(media["images"]))
	}
	raiSetString(info, "episode", raiString(media["episode_title"]))
	raiSetInt(info, "season_number", raiInt(media["season"]))
	raiSetInt(info, "episode_number", raiInt(media["episode"]))
	dateTime := raiPublicationDateTime(media, target)
	if ts := raiTimestamp(dateTime); ts > 0 {
		info.Set("timestamp", value.Int(ts))
		info.Set("upload_date", value.String(time.Unix(ts, 0).UTC().Format("20060102")))
	}
	if subs := raiSubtitles(request.URL, video); subs.Len() > 0 {
		info.Set("subtitles", value.ObjectValue(subs))
	}
	return Media(value.NewInfo(info)), nil
}

type raiRelinkerInfo struct {
	formats  []value.Value
	duration float64
	live     bool
}

func raiRelinker(ctx context.Context, transport Transport, raw, id string, audioOnly bool) (raiRelinkerInfo, error) {
	if err := ctx.Err(); err != nil {
		return raiRelinkerInfo{}, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return raiRelinkerInfo{}, fmt.Errorf("%w: invalid relinker", ErrInvalidMetadata)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return raiRelinkerInfo{}, fmt.Errorf("%w: unsupported Rai relinker protocol", ErrUnavailable)
	}
	if !raiEndpoint(raw) {
		return raiRelinkerInfo{}, fmt.Errorf("%w: unsafe Rai relinker", ErrInvalidMetadata)
	}
	q := u.Query()
	q.Set("output", "64")
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return raiRelinkerInfo{}, fmt.Errorf("%w: invalid Rai relinker", ErrInvalidMetadata)
	}
	req.Header.Set("User-Agent", "Rai")
	var response *http.Response
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return raiRelinkerInfo{}, ErrTransportIsolation
	}
	response, err = isolated.DoWithoutCredentialsNoRedirect(ctx, req)
	if err != nil {
		return raiRelinkerInfo{}, raiCategorize(err)
	}
	if response == nil || response.Body == nil {
		return raiRelinkerInfo{}, fmt.Errorf("%w: empty Rai relinker response", ErrInvalidMetadata)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return raiRelinkerInfo{}, raiCategorize(&HTTPStatusError{response.StatusCode})
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, raiMaxXML+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return raiRelinkerInfo{}, ctxErr
		}
		return raiRelinkerInfo{}, errors.New("read Rai relinker response failed")
	}
	if len(data) > raiMaxXML {
		return raiRelinkerInfo{}, fmt.Errorf("%w: Rai relinker XML response too large", ErrInvalidMetadata)
	}
	fields, err := raiXML(data)
	if err != nil {
		return raiRelinkerInfo{}, err
	}
	if l := strings.TrimSpace(fields["license_url"]); l != "" && l != "{}" {
		return raiRelinkerInfo{}, ErrUnavailable
	}
	media := fields["content_url"]
	if media == "" {
		media = fields["url"]
	}
	if media == "" {
		return raiRelinkerInfo{}, fmt.Errorf("%w: Rai relinker has no media URL", ErrUnavailable)
	}
	if strings.Contains(media, "/video_no_available.mp4") {
		return raiRelinkerInfo{}, ErrRegionRestricted
	}
	formats, err := raiFormats(media, fields, audioOnly)
	if err != nil {
		return raiRelinkerInfo{}, err
	}
	if len(formats) == 0 && strings.EqualFold(fields["geoprotection"], "Y") {
		return raiRelinkerInfo{}, ErrRegionRestricted
	}
	// Bounded `_create_http_urls` synthesis: append a credential-isolated
	// availability probe and synthetic MP4 HTTP format entries when the
	// pinned preconditions hold (m3u8, !live, !audioOnly, manifest matched).
	// The original relinker URL is preserved verbatim so the relinker's
	// signature survives the synthesis round-trip.
	//
	// Pinned `_create_http_urls` swallows every non-context error from URL
	// preparation or the availability probe and degrades to no synthetic
	// formats; only context cancellation propagates.  We mirror that
	// contract so a hostile or transient MP4 availability failure cannot
	// drop a valid base HLS extraction.
	live := strings.EqualFold(fields["is_live"], "Y")
	synthetic, err := raiMP4Synthesize(ctx, transport, raw, media, formats, live, audioOnly)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return raiRelinkerInfo{}, err
		}
		synthetic = nil
	}
	if len(synthetic) > 0 {
		formats = append(formats, synthetic...)
	}
	return raiRelinkerInfo{formats: formats, duration: raiDuration(fields["duration"]), live: strings.EqualFold(fields["is_live"], "Y")}, nil
}
func raiXML(data []byte) (map[string]string, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	out := map[string]string{}
	depth, count := 0, 0
	var current string
	var text strings.Builder
	for {
		token, err := d.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: malformed Rai relinker XML", ErrInvalidMetadata)
		}
		switch node := token.(type) {
		case xml.StartElement:
			depth++
			count++
			if depth > 32 || count > 128 {
				return nil, fmt.Errorf("%w: Rai relinker XML limits", ErrInvalidMetadata)
			}
			if depth == 2 {
				current, text = strings.ToLower(node.Name.Local), strings.Builder{}
				if current == "url" {
					for _, attribute := range node.Attr {
						if attribute.Name.Local == "type" && attribute.Value == "content" {
							current = "content_url"
						}
					}
				}
			}
		case xml.CharData:
			if current != "" && depth == 2 {
				if text.Len()+len(node) > raiMaxURLLength {
					return nil, fmt.Errorf("%w: Rai XML field limit", ErrInvalidMetadata)
				}
				text.Write([]byte(node))
			}
		case xml.EndElement:
			if depth == 2 && current != "" {
				if _, exists := out[current]; exists {
					return nil, fmt.Errorf("%w: contradictory Rai XML", ErrInvalidMetadata)
				}
				out[current] = strings.TrimSpace(text.String())
				current = ""
			}
			depth--
		}
	}
	if depth != 0 {
		return nil, fmt.Errorf("%w: malformed Rai relinker XML", ErrInvalidMetadata)
	}
	return out, nil
}
func raiFormats(raw string, f map[string]string, audioOnly bool) ([]value.Value, error) {
	if manifestURL, recognized, err := raiF4MManifestURL(raw); recognized {
		if err != nil {
			return nil, err
		}
		format := manifestFormat("hds", manifestURL, "f4m_native")
		format.Set("ext", value.String("flv"))
		return []value.Value{value.ObjectValue(format)}, nil
	}
	if !raiPublicURL(raw) {
		return nil, fmt.Errorf("%w: unsafe Rai media URL", ErrInvalidMetadata)
	}
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(strings.Split(raw, "?")[0]), "."))
	if ext == "" && strings.Contains(strings.ToLower(raw), "format=m3u8") {
		ext = "m3u8"
	}
	var format *value.Object
	switch ext {
	case "m3u8":
		format = manifestFormat("hls", raw, "m3u8_native")
		format.Set("ext", value.String("mp4"))
		if audioOnly || strings.Contains(raw, "_ao.") || strings.Contains(raw, "_ao_") {
			format.Set("vcodec", value.String("none"))
			format.Set("acodec", value.String("mp4a"))
		} else if strings.Contains(raw, "_vo.") || strings.Contains(raw, "_vo_") {
			format.Set("acodec", value.String("none"))
			format.Set("vcodec", value.String("avc1"))
		} else {
			format.Set("acodec", value.String("mp4a"))
			format.Set("vcodec", value.String("avc1"))
		}
	case "mp3":
		format, _ = riskFormat(raw, "https-mp3")
		format.Set("vcodec", value.String("none"))
		format.Set("acodec", value.String("mp3"))
	case "mp4":
		format, _ = riskFormat(raw, "https-"+strings.TrimSpace(f["bitrate"]))
		format.Set("vcodec", value.String("avc1"))
		format.Set("acodec", value.String("mp4a"))
		raiSetInt(format, "tbr", raiInt(f["bitrate"]))
	default:
		return nil, fmt.Errorf("%w: unsupported Rai media extension", ErrUnavailable)
	}
	return []value.Value{value.ObjectValue(format)}, nil
}

// raiF4MManifestURL mirrors the pinned Rai F4M branch without downloading or
// parsing the manifest. The legacy `manifest#live_hds.f4m` spelling is first
// normalized with a byte-preserving replacement; the original raw query is
// then retained verbatim and the Adobe compatibility parameters are appended.
// A recognized but unsafe F4M URL returns an error rather than falling back to
// another media type.
func raiF4MManifestURL(raw string) (string, bool, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", true, fmt.Errorf("%w: invalid Rai F4M URL", ErrInvalidMetadata)
	}
	normalized := raw
	// The pinned legacy spelling is a path named exactly "manifest" followed
	// by the literal fragment "#live_hds.f4m". A query-like suffix after that
	// fragment is retained as query text because the pinned replacement turns
	// it into `manifest.f4m?<suffix>`; no replacement is attempted elsewhere.
	if parsed.RawQuery == "" && path.Base(parsed.Path) == "manifest" {
		const legacyMarker = "#live_hds.f4m"
		if marker := strings.Index(raw, legacyMarker); marker >= 0 {
			suffix := raw[marker+len(legacyMarker):]
			if suffix == "" || strings.HasPrefix(suffix, "?") {
				normalized = raw[:marker] + ".f4m" + suffix
			}
		}
	}
	normalizedParsed, err := url.Parse(normalized)
	if err != nil {
		return "", true, fmt.Errorf("%w: invalid Rai F4M URL", ErrInvalidMetadata)
	}
	if !strings.EqualFold(path.Ext(normalizedParsed.Path), ".f4m") {
		return "", false, nil
	}
	if normalizedParsed.Scheme != "http" && normalizedParsed.Scheme != "https" || normalizedParsed.User != nil || normalizedParsed.Fragment != "" || normalizedParsed.Hostname() == "" || !raiPublicURL(normalized) {
		return "", true, fmt.Errorf("%w: unsafe Rai F4M URL", ErrInvalidMetadata)
	}
	query, queryErr := url.ParseQuery(normalizedParsed.RawQuery)
	if queryErr != nil {
		return "", true, fmt.Errorf("%w: malformed Rai F4M query", ErrInvalidMetadata)
	}
	controls := map[string]string{
		"hdcore": "3.7.0",
		"plugin": "aasp-3.7.0.39.44",
	}
	missing := make([]string, 0, len(controls))
	for key, want := range controls {
		values, present := query[key]
		if !present {
			missing = append(missing, key+"="+want)
			continue
		}
		if len(values) != 1 || values[0] != want {
			return "", true, fmt.Errorf("%w: conflicting Rai F4M %s query", ErrInvalidMetadata, key)
		}
	}
	// Keep output deterministic while leaving every existing byte untouched.
	sort.Strings(missing)
	if len(missing) == 0 {
		return normalized, true, nil
	}
	separator := "?"
	if normalizedParsed.RawQuery != "" {
		separator = "&"
	}
	return normalized + separator + strings.Join(missing, "&"), true, nil
}

// raiMP4URL applies the pinned MP4 URL template
// (`overrideUserAgentRule=mp4-<quality>`) to a relinker URL while preserving
// the original RawQuery byte order and any signature-bearing query
// parameters (the Rai relinker signs its base query string; rewriting it via
// `url.Values.Encode` invalidates the signature).  The override parameter is
// appended verbatim after a `&` separator; a pre-existing
// `overrideUserAgentRule` key in the relinker URL is rejected so a malicious
// or stale signature cannot leak through.  Fragments, userinfo, and empty
// RawQuery are rejected so a relinker URL cannot leak secrets via the
// synthesized URL.  Quality must be either `*` or digit/comma.
func raiMP4URL(relinkerURL, quality string) (string, error) {
	if quality != "*" && !raiMP4QualitySelector.MatchString(quality) {
		return "", fmt.Errorf("%w: invalid Rai MP4 quality", ErrInvalidMetadata)
	}
	if quality != "*" && strings.Contains(quality, ",") {
		return "", fmt.Errorf("%w: Rai MP4 quality must be a single token", ErrInvalidMetadata)
	}
	parsed, err := url.Parse(relinkerURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid Rai relinker URL", ErrInvalidMetadata)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("%w: unsupported Rai relinker protocol", ErrUnavailable)
	}
	if parsed.User != nil || parsed.Fragment != "" || parsed.ForceQuery || parsed.RawQuery == "" {
		return "", fmt.Errorf("%w: unsafe Rai relinker URL", ErrInvalidMetadata)
	}
	if _, exists := parsed.Query()["overrideUserAgentRule"]; exists {
		return "", fmt.Errorf("%w: Rai relinker URL already overrides User-Agent", ErrInvalidMetadata)
	}
	candidate := parsed.String() + "&overrideUserAgentRule=mp4-" + quality
	if !raiPublicURL(candidate) {
		return "", fmt.Errorf("%w: unsafe Rai MP4 URL", ErrInvalidMetadata)
	}
	return candidate, nil
}

// raiMP4ManifestQualities returns the bounded list of MP4 quality tokens
// declared by a manifest URL path.  The three-state return distinguishes:
//   - (nil, false): the manifest URL did not match the pinned `_MANIFEST_REG`
//     at all; synthesis is suppressed entirely so we do not invent MP4
//     formats for arbitrary Rai m3u8 URLs.
//   - (nil, true):  the manifest URL matched but exposes no quality list;
//     synthesis proceeds with the wildcard token `*`.
//   - (qualities, true): the manifest URL matched and declared an explicit
//     list; each element is a bounded digit/comma token.  Unknown or
//     malformed quality tokens, or lists that exceed
//     `raiMaxMP4Qualities` entries, are treated as `valid=false` so the
//     caller suppresses synthesis instead of falling back to the wildcard.
func raiMP4ManifestQualities(manifestURL string) ([]string, bool) {
	match := raiMP4Manifest.FindStringSubmatch(strings.SplitN(manifestURL, "?", 2)[0])
	if len(match) == 0 {
		return nil, false
	}
	quality := match[2]
	if quality == "" {
		return nil, true
	}
	parts := strings.Split(quality, ",")
	out := make([]string, 0, len(parts))
	seen := make(map[string]bool, len(parts))
	for _, part := range parts {
		if !raiMP4QualitySelector.MatchString(part) {
			return nil, false
		}
		if seen[part] {
			continue
		}
		seen[part] = true
		out = append(out, part)
		if len(out) > raiMaxMP4Qualities {
			return nil, false
		}
	}
	return out, true
}

// raiMP4Probe executes a credential-isolated no-redirect HEAD probe against
// the availability probe URL (`overrideUserAgentRule=mp4-*`).  It mirrors
// the pinned availability gate at the top of `_create_http_urls`.  The probe
// is only considered successful when the response status is in the 2xx
// range and the (bounded) body is empty.  Network errors are treated as
// "probe failed" while context cancellation and deadline errors are
// propagated verbatim so callers can distinguish them from a "service
// unavailable" path.  Nil responses or nil bodies are rejected without
// panic.
func raiMP4Probe(ctx context.Context, transport Transport, probeURL string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	probe, err := url.Parse(probeURL)
	if err != nil || !raiPublicURL(probeURL) {
		return false, fmt.Errorf("%w: unsafe Rai MP4 probe URL", ErrInvalidMetadata)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, probe.String(), nil)
	if err != nil {
		return false, fmt.Errorf("%w: invalid Rai MP4 probe", ErrInvalidMetadata)
	}
	request.Header.Set("User-Agent", "Rai")
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return false, ErrTransportIsolation
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, nil
	}
	if response == nil {
		return false, nil
	}
	if response.Body == nil {
		return false, nil
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, nil
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, raiMaxMP4Probe+1))
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, nil
	}
	if int64(len(data)) > raiMaxMP4Probe {
		return false, nil
	}
	return true, nil
}

// raiMP4QualityBounds mirrors the percentage-and-roof helper used in the
// pinned `_create_http_urls.get_format_info`.  It returns true when `target`
// is within `min(20%, 125 kbps)` of `number`.  In the pinned call site
// `number` is the desired tbr and `target` is the candidate base-format tbr.
// The check is implemented as the strict, overflow-safe conditions
// `diff < 125 && diff*5 < number` (both integers).  Non-positive inputs
// disable the match.
func raiMP4QualityBounds(number, target int64) bool {
	if number <= 0 || target <= 0 {
		return false
	}
	diff := target - number
	if diff < 0 {
		diff = -diff
	}
	if diff >= 125 {
		return false
	}
	return diff*5 < number
}

// raiMP4Synthesize implements the bounded `_create_http_urls` synthesis path
// from yt_dlp/extractor/rai.py
// (aefce1eea4d0b6bab1ec2bd3beff09bff91a39c8).  It is only invoked when the
// pinned preconditions hold: the manifest URL matched the pinned
// `_MANIFEST_REG`, the relinker response is not flagged as live, and the
// caller has not requested audio-only synthesis.  Audio/video single-stream
// HLS base formats are filtered out of the metadata-copy candidates but do
// not abort synthesis - the pinned default table is still emitted when
// `!audioOnly && !live`.  Synthesis is bounded by `raiMaxFormats` overall
// (base + synthetic), the pinned quality table, and the parsed manifest
// quality list.  When the manifest exposes no explicit quality list the
// wildcard token `*` is used; invalid or oversized quality lists suppress
// synthesis entirely.
//
// The credential-isolated no-redirect HEAD availability probe is the first
// network step, mirroring the pinned `_request_webpage(HEADRequest(...))`
// call site at the top of `_create_http_urls`.  Only a successful probe
// is followed by the manifest-regex quality extraction; a probe failure
// degrades to no synthetic formats without surfacing an error.
func raiMP4Synthesize(ctx context.Context, transport Transport, relinkerURL, manifestURL string, baseFormats []value.Value, live, audioOnly bool) ([]value.Value, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if live || audioOnly {
		return nil, nil
	}
	probeURL, err := raiMP4URL(relinkerURL, "*")
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, nil
	}
	available, err := raiMP4Probe(ctx, transport, probeURL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, nil
	}
	if !available {
		return nil, nil
	}
	qualities, matched := raiMP4ManifestQualities(manifestURL)
	if !matched {
		return nil, nil
	}
	// Filter combined A/V base formats for metadata-copy selection.  The
	// pinned code uses `len(filtered)==1` as the trigger for the 250 kbps
	// fallback path; audio-only or video-only base formats never qualify.
	combined := make([]value.Value, 0, len(baseFormats))
	singleTBR := int64(0)
	for _, candidate := range baseFormats {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		vcodec, _ := object.Lookup("vcodec").StringValue()
		acodec, _ := object.Lookup("acodec").StringValue()
		if vcodec == "none" || acodec == "none" {
			continue
		}
		combined = append(combined, candidate)
		if tbr, ok := raiNumericValue(object, "tbr"); ok && tbr > 0 {
			singleTBR = tbr
		}
	}
	if len(qualities) == 0 {
		qualities = []string{"*"}
	}
	remaining := raiMaxFormats - len(baseFormats)
	if remaining <= 0 {
		return nil, nil
	}
	out := make([]value.Value, 0, min(len(qualities), remaining))
	seen := make(map[string]bool, len(qualities))
	for _, quality := range qualities {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if quality != "*" {
			tbr, parseErr := strconv.Atoi(quality)
			if parseErr != nil || tbr <= 0 {
				continue
			}
			if _, known := raiMP4QualityTable[tbr]; !known {
				// Pinned `_create_http_urls` still emits an explicit
				// quality even when the table lacks it, as long as
				// `raiMP4Format` can either derive a metadata copy or
				// fall back to the table default.  We only skip here
				// when neither path can produce a valid synthetic entry.
				if !raiMP4FormatResolves(int64(tbr), combined) {
					continue
				}
			}
		}
		formatURL, err := raiMP4URL(relinkerURL, quality)
		if err != nil {
			continue
		}
		if seen[formatURL] {
			continue
		}
		seen[formatURL] = true
		out = append(out, value.ObjectValue(raiMP4Format(formatURL, quality, combined, singleTBR, len(combined))))
		if len(out) >= remaining {
			break
		}
	}
	return out, nil
}

// raiNumericValue reads an integer-typed or float-typed value from an Object,
// matching the existing `value.Value` int/float conversion conventions.
func raiNumericValue(object *value.Object, key string) (int64, bool) {
	v := object.Lookup(key)
	if n, ok := v.Int(); ok {
		return n, true
	}
	if f, ok := v.Float(); ok {
		return int64(f), true
	}
	return 0, false
}

// raiMP4FormatResolves reports whether `raiMP4Format` can produce a valid
// synthetic entry for an explicit tbr token that is not in the pinned
// `_QUALITY` table.  It returns true when at least one combined base
// format has a bitrate within the percentage bounds of the desired tbr
// or matches its resolution table entry.  Used by `raiMP4Synthesize` to
// decide whether to skip a non-table quality token without ever calling
// `raiMP4Format`.
func raiMP4FormatResolves(desired int64, combined []value.Value) bool {
	for _, candidate := range combined {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		baseTBR, ok := raiNumericValue(object, "tbr")
		if !ok {
			continue
		}
		if raiMP4QualityBounds(desired, baseTBR) {
			return true
		}
		baseW, hasW := object.Lookup("width").Int()
		baseH, hasH := object.Lookup("height").Int()
		if dims, known := raiMP4QualityTable[int(desired)]; known && hasW && hasH && baseW == int64(dims[0]) && baseH == int64(dims[1]) {
			return true
		}
	}
	return false
}

// raiMP4Format builds one synthetic MP4 HTTP format entry.  Pinned
// `_create_http_urls.get_format_info` is invoked once per synthesized
// quality token (including explicit ones), not only on the wildcard.  The
// metadata-copy selection prefers bitrate matches over resolution matches
// and always picks the LAST matching candidate in iteration order,
// mirroring the pinned Python loop.  When no combined A/V base format copy
// is selected, width/height remain absent so the caller can distinguish
// them from explicit table defaults; codecs/fps likewise only inherit
// from the chosen copy.  The 250/352x198 table default is applied only as
// a fallback when no copy was selected and the desired tbr is not in the
// pinned table.
func raiMP4Format(rawURL, quality string, combined []value.Value, singleTBR int64, combinedCount int) *value.Object {
	width, height := 0, 0
	var vcodec, acodec string
	fps := 25
	desired := int64(250)
	if quality == "*" {
		if combinedCount == 1 && singleTBR > 0 {
			derived := (singleTBR / 100) * 100
			if derived <= 0 || derived < 250 {
				derived = 250
			}
			// Pinned semantics: the original code uses `br > 300` (strict)
			// to enable the derived fallback.  When the rounded value is
			// 300 it falls through to the 250 default; otherwise the
			// rounded value is the desired tbr.
			if derived > 300 || (derived == 300 && singleTBR > 300) {
				desired = derived
			}
		}
	} else {
		if parsedTBR, parseErr := strconv.ParseInt(quality, 10, 64); parseErr == nil && parsedTBR > 0 {
			desired = parsedTBR
		}
	}
	var bitrateMatch *value.Object
	var resolutionMatch *value.Object
	for _, candidate := range combined {
		object, ok := candidate.Object()
		if !ok {
			continue
		}
		baseTBR, hasTBR := raiNumericValue(object, "tbr")
		baseW, hasW := object.Lookup("width").Int()
		baseH, hasH := object.Lookup("height").Int()
		if hasTBR && raiMP4QualityBounds(desired, baseTBR) {
			// Deep clone so the resolution-match branch (which mutates
			// tbr) cannot corrupt the bitrate-match copy via the shared
			// backing slice.  Shallow `*object` copies leak mutations
			// because Object.fields shares its backing array.
			bitrateMatch = object.Clone()
		}
		if dims, known := raiMP4QualityTable[int(desired)]; known && hasW && hasH && baseW == int64(dims[0]) && baseH == int64(dims[1]) {
			resolutionMatch = object.Clone()
			resolutionMatch.Set("tbr", value.Int(desired))
		}
	}
	chosen := bitrateMatch
	if chosen == nil {
		chosen = resolutionMatch
	}
	if chosen != nil {
		if w, ok := chosen.Lookup("width").Int(); ok && w > 0 {
			width = int(w)
		}
		if h, ok := chosen.Lookup("height").Int(); ok && h > 0 {
			height = int(h)
		}
		if v, ok := chosen.Lookup("vcodec").StringValue(); ok && v != "" && v != "none" {
			vcodec = v
		}
		if a, ok := chosen.Lookup("acodec").StringValue(); ok && a != "" && a != "none" {
			acodec = a
		}
		if f, ok := chosen.Lookup("fps").Int(); ok && f > 0 {
			fps = int(f)
		}
	}
	if vcodec == "" {
		vcodec = "avc1"
	}
	if acodec == "" {
		acodec = "mp4a"
	}
	tbr := int(desired)
	if chosen != nil {
		// Pinned `format_copy.get('tbr') or desired`: when the chosen copy
		// exposes its own tbr, that value wins over the desired/rounded
		// tbr.  format_id remains the desired tbr per the pinned contract.
		if chosenTBR, ok := raiNumericValue(chosen, "tbr"); ok && chosenTBR > 0 {
			tbr = int(chosenTBR)
		}
	}
	formatID := "https-" + strconv.FormatInt(desired, 10)
	fields := []value.Field{
		{Key: "format_id", Value: value.String(formatID)},
		{Key: "url", Value: value.String(rawURL)},
		{Key: "protocol", Value: value.String("https")},
		{Key: "ext", Value: value.String("mp4")},
		{Key: "vcodec", Value: value.String(vcodec)},
		{Key: "acodec", Value: value.String(acodec)},
		{Key: "tbr", Value: value.Int(int64(tbr))},
	}
	// width/height are emitted only when the chosen base format copy
	// exposed them.  When no copy was selected we apply the table default
	// for the *desired* tbr; if neither path produces a dimension we omit
	// the field entirely (no synthetic 1280x720 fallback).
	if width > 0 && height > 0 {
		fields = append(fields,
			value.Field{Key: "width", Value: value.Int(int64(width))},
			value.Field{Key: "height", Value: value.Int(int64(height))},
		)
	} else if chosen == nil {
		if dims, known := raiMP4QualityTable[tbr]; known {
			fields = append(fields,
				value.Field{Key: "width", Value: value.Int(int64(dims[0]))},
				value.Field{Key: "height", Value: value.Int(int64(dims[1]))},
			)
		}
	}
	fields = append(fields, value.Field{Key: "fps", Value: value.Int(int64(fps))})
	return value.NewObject(fields...)
}

func raiPlaylist(ctx context.Context, request Request, target raiTarget) (Extraction, error) {
	var p map[string]any
	if err := raiJSON(ctx, request.Transport, target.base+".json", &p); err != nil {
		return Extraction{}, err
	}
	title := raiFirst(raiString(p["name"]), raiString(p["title"]), target.id)
	playlistID := target.id
	if target.kind == raiSoundPlaylist && target.extra != "" {
		pathID := ""
		for _, filter := range raiSlice(p["filters"]) {
			filter := raiMap(filter)
			if strings.Contains(strings.Trim(raiString(filter["weblink"]), "/"), strings.Trim(target.extra, "/")) {
				pathID = raiString(filter["path_id"])
				break
			}
		}
		if pathID == "" {
			return Extraction{}, fmt.Errorf("%w: Rai Sound playlist selector not found", ErrInvalidPlaylist)
		}
		selected, ok := raiRelativeEndpoint(target.base, pathID)
		if !ok {
			return Extraction{}, fmt.Errorf("%w: unsafe Rai Sound selector", ErrInvalidMetadata)
		}
		if err := raiJSON(ctx, request.Transport, selected, &p); err != nil {
			return Extraction{}, err
		}
		playlistID += "_" + strings.ReplaceAll(strings.Trim(target.extra, "/"), "/", "_")
		title = raiFirst(raiString(p["title"]), title)
	}
	seq, title, err := raiPlaylistEntries(request.Transport, target, p, title)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(playlistID)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(request.URL)})
	if target.kind == raiSoundPlaylist {
		raiSetString(info, "description", raiStringPath(p, "podcast_info", "description"))
	} else {
		raiSetString(info, "description", raiStringPath(p, "program_info", "description"))
	}
	return Playlist(value.NewInfo(info), seq)
}

type raiEntries struct {
	transport Transport
	target    raiTarget
	sets      []string
	cards     []string
}

func raiPlaylistEntries(t Transport, target raiTarget, p map[string]any, title string) (EntrySequence, string, error) {
	r := raiEntries{transport: t, target: target}
	if target.kind == raiPlayPlaylist {
		selector := strings.ToUpper(strings.Trim(target.extra, "/"))
		for _, block := range raiSlice(p["blocks"]) {
			blockMap := raiMap(block)
			for _, set := range raiSlice(raiMap(block)["sets"]) {
				setMap := raiMap(set)
				setPath := strings.ToUpper(strings.ReplaceAll(raiJoinPath(raiString(blockMap["name"]), raiString(setMap["name"])), " ", "-"))
				if selector != "" && setPath != selector {
					continue
				}
				if selector != "" {
					title = raiJoin(title, raiString(setMap["name"]))
				}
				if id := raiString(setMap["id"]); raiSafeID(id) {
					r.sets = append(r.sets, id)
				}
			}
		}
	} else {
		cards := append(raiSlice(p["cards"]), raiSlice(raiMap(p["block"])["cards"])...)
		for _, card := range cards {
			if item := raiString(raiMap(card)["path_id"]); item != "" {
				r.cards = append(r.cards, item)
			}
		}
	}
	if len(r.sets) > raiMaxEntries || len(r.cards) > raiMaxEntries {
		return nil, "", fmt.Errorf("%w: Rai playlist overflow", ErrInvalidPlaylist)
	}
	return r, title, nil
}
func (r raiEntries) Iterator() EntryIterator { return &raiEntriesIterator{source: r} }

type raiEntriesIterator struct {
	source     raiEntries
	set, index int
	seen       map[string]bool
	pending    []Entry
	emitted    int
}

func (it *raiEntriesIterator) Next(ctx context.Context) (Entry, bool, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, false, err
	}
	if it.seen == nil {
		it.seen = map[string]bool{}
	}
	for {
		if len(it.pending) > 0 {
			e := it.pending[0]
			it.pending = it.pending[1:]
			if !it.seen[e.URL] {
				if it.emitted >= raiMaxEntries {
					return Entry{}, false, fmt.Errorf("%w: Rai playlist entry overflow", ErrInvalidPlaylist)
				}
				it.seen[e.URL] = true
				it.emitted++
				return e, true, nil
			}
			continue
		}
		if it.source.target.kind == raiSoundPlaylist {
			if it.index >= len(it.source.cards) {
				return Entry{}, false, nil
			}
			p := it.source.cards[it.index]
			it.index++
			e, ok := raiEntry(it.source.target.base, p, "raiplaysound")
			if ok {
				it.pending = []Entry{e}
			}
			continue
		}
		if it.set >= len(it.source.sets) {
			return Entry{}, false, nil
		}
		sid := it.source.sets[it.set]
		it.set++
		var page map[string]any
		if err := raiJSON(ctx, it.source.transport, it.source.target.base+"/"+url.PathEscape(sid)+".json", &page); err != nil {
			if ctx.Err() != nil {
				return Entry{}, false, ctx.Err()
			}
			if raiSkippableSetError(err) {
				continue
			}
			return Entry{}, false, err
		}
		items := raiSlice(page["items"])
		if len(items) > raiMaxEntries-it.emitted-len(it.pending) {
			return Entry{}, false, fmt.Errorf("%w: Rai playlist entry overflow", ErrInvalidPlaylist)
		}
		for _, m := range items {
			if e, ok := raiEntry(it.source.target.base, raiString(raiMap(m)["path_id"]), "raiplay"); ok {
				it.pending = append(it.pending, e)
			}
		}
	}
}

func raiJoinPath(parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.Trim(part, "/"); part != "" {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, "/")
}

func raiRelativeEndpoint(base, raw string) (string, bool) {
	relative, err := url.Parse(raw)
	if err != nil || relative.IsAbs() || relative.Host != "" || relative.User != nil || relative.Fragment != "" {
		return "", false
	}
	origin, err := url.Parse(base)
	if err != nil {
		return "", false
	}
	endpoint := origin.ResolveReference(relative).String()
	return endpoint, raiEndpoint(endpoint)
}
func raiEntry(base, p, key string) (Entry, bool) {
	u, err := url.Parse(p)
	if err != nil {
		return Entry{}, false
	}
	if !u.IsAbs() {
		b, _ := url.Parse(base)
		u = b.ResolveReference(u)
	}
	if !raiEndpoint(u.String()) {
		return Entry{}, false
	}
	return Entry{URL: u.String(), ExtractorKey: key}, true
}

func raiLegacyExtract(ctx context.Context, r Request, t raiTarget) (Extraction, error) {
	endpoint := "https://www.rai.tv/dl/RaiTV/programmi/media/ContentItem-" + t.id + ".html?json"
	var m map[string]any
	if err := raiJSON(ctx, r.Transport, endpoint, &m); err != nil {
		return Extraction{}, err
	}
	return raiLegacyMedia(ctx, r, t, m)
}
func raiLegacyMedia(ctx context.Context, r Request, t raiTarget, m map[string]any) (Extraction, error) {
	if strings.Contains(strings.ToLower(raiString(m["type"])), "audio") {
		f, ok := riskFormat(raiString(m["audioUrl"]), "https-"+raiString(m["formatoAudio"]))
		if !ok {
			return Extraction{}, ErrUnavailable
		}
		f.Set("vcodec", value.String("none"))
		return raiResult(t.id, t.id, raiFirst(raiString(m["name"]), raiString(m["title"])), r.URL, []value.Value{value.ObjectValue(f)}, false, 0), nil
	}
	ri, err := raiRelinker(ctx, r.Transport, raiString(m["mediaUri"]), t.id, false)
	if err != nil {
		return Extraction{}, err
	}
	return raiResult(t.id, t.id, raiFirst(raiString(m["name"]), raiString(m["title"])), r.URL, ri.formats, ri.live, ri.duration), nil
}
func raiNewsExtract(ctx context.Context, r Request, t raiTarget) (Extraction, error) {
	page, _, err := r.Transport.ReadPage(ctx, r.URL)
	if err != nil {
		return Extraction{}, raiCategorize(err)
	}
	if len(page) > raiMaxJSON {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	re := regexp.MustCompile(`(?is)<rai(?:news|cultura)-player[^>]+data=['\"]([^'\"]+)`)
	match := re.FindSubmatch(page)
	if len(match) < 2 {
		return raiLegacyExtract(ctx, r, raiTarget{kind: raiLegacy, id: t.id})
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &data); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid Rai player data", ErrInvalidMetadata)
	}
	link := raiFirst(
		raiString(data["content_url"]),
		raiString(data["mediapolis"]),
		raiStringPath(data, "mediapolis", "content_url"),
	)
	if link == "" {
		return raiLegacyExtract(ctx, r, raiTarget{kind: raiLegacy, id: t.id})
	}
	b, _ := url.Parse(r.URL)
	rel, _ := url.Parse(link)
	link = b.ResolveReference(rel).String()
	ri, err := raiRelinker(ctx, r.Transport, link, t.id, false)
	if err != nil {
		return Extraction{}, err
	}
	return raiResult(t.id, t.id, raiFirst(raiString(data["title"]), raiStringPath(data, "track_info", "title")), r.URL, ri.formats, ri.live, ri.duration), nil
}
func raiSudtirolExtract(ctx context.Context, r Request, t raiTarget) (Extraction, error) {
	page, _, err := r.Transport.ReadPage(ctx, r.URL)
	if err != nil {
		return Extraction{}, err
	}
	if len(page) > raiMaxJSON {
		return Extraction{}, ErrJSONResponseTooLarge
	}
	re := regexp.MustCompile(`(?is)(?:file:\s*["']|<source\s+src=["'])([^"']+)`)
	m := re.FindSubmatch(page)
	if len(m) < 2 {
		return Extraction{}, ErrUnavailable
	}
	formats, err := raiFormats(string(m[1]), map[string]string{}, false)
	if err != nil {
		return Extraction{}, err
	}
	title := t.id
	if x := regexp.MustCompile(`(?is)<span class=["']med_title["']>([^<]+)`).FindSubmatch(page); len(x) == 2 {
		title = strings.TrimSpace(string(x[1]))
	}
	return raiResult(t.id, t.id, title, r.URL, formats, false, 0), nil
}
func raiResult(id, display, title, page string, formats []value.Value, live bool, duration float64) Extraction {
	if title == "" {
		title = display
	}
	o := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "display_id", Value: value.String(display)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(page)}, value.Field{Key: "formats", Value: value.List(formats...)})
	if duration > 0 {
		o.Set("duration", value.Float(duration))
	}
	if live {
		o.Set("is_live", value.Bool(true))
		o.Set("live_status", value.String("is_live"))
	}
	return Media(value.NewInfo(o))
}

func raiPublicURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if looksLikeLocalOrInternalHost(h) || looksLikeIPLiteralHost(u.Host) || raiNumericIPLiteralVariant(h) {
		return false
	}
	if ip := net.ParseIP(h); ip != nil {
		return false
	}
	return !strings.Contains(strings.ToLower(u.EscapedPath()), "%00")
}

func raiNumericIPLiteralVariant(host string) bool {
	if host == "" {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" {
			return false
		}
		if strings.HasPrefix(strings.ToLower(label), "0x") {
			if len(label) == 2 {
				return false
			}
			for _, character := range label[2:] {
				if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
					return false
				}
			}
			continue
		}
		for _, character := range label {
			if character < '0' || character > '9' {
				return false
			}
		}
	}
	return true
}

func raiContentIdentity(media map[string]any, target raiTarget) (string, bool) {
	field := "id"
	if target.kind == raiSoundVOD || target.kind == raiSoundLive {
		field = "uniquename"
	}
	raw, present := media[field]
	if !present || raw == nil {
		return "", false
	}
	identity, ok := raw.(string)
	if !ok {
		return "", true
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return "", false
	}
	identity = raiTrimContentPrefix(identity)
	if identity == "" {
		return "", false
	}
	return identity, true
}

func raiNewsCulturaID(path string) (string, bool) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) > 0 && strings.HasPrefix(strings.ToLower(parts[0]), "articoli") {
		return "", false
	}
	match := raiNewsCulturaPath.FindStringSubmatch(path)
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

func raiSoundPodcastInfo(media map[string]any) map[string]any {
	if podcast := raiMap(media["podcast_info"]); len(podcast) > 0 {
		return podcast
	}
	live := raiMap(media["live"])
	cards := raiSlice(live["cards"])
	if len(cards) == 0 {
		return nil
	}
	return raiMap(cards[0])
}

func raiTrimContentPrefix(identity string) string {
	identity = strings.TrimPrefix(strings.TrimPrefix(identity, "ContentItem-"), "Page-")
	return identity
}

func raiValidContentIdentity(identity string, target raiTarget) bool {
	if !raiUUID.MatchString(identity) {
		return false
	}
	switch target.kind {
	case raiPlayVOD, raiSoundVOD:
		return strings.EqualFold(identity, target.id)
	case raiPlayLive, raiSoundLive:
		return true
	default:
		return strings.EqualFold(identity, target.id)
	}
}

func raiPublicationDateTime(media map[string]any, target raiTarget) string {
	if target.kind != raiSoundVOD && target.kind != raiSoundLive {
		return raiJoinSpace(raiString(media["date_published"]), raiString(media["time_published"]))
	}
	if createDate := raiString(media["create_date"]); createDate != "" {
		return raiJoinSpace(createDate, raiString(media["create_time"]))
	}
	return raiStringPath(media, "live", "create_date")
}

func raiSkippableSetError(err error) bool {
	return errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge)
}
func raiMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func raiSlice(v any) []any        { x, _ := v.([]any); return x }
func raiString(v any) string {
	s, _ := v.(string)
	if len(s) > raiMaxURLLength {
		return ""
	}
	return strings.TrimSpace(s)
}
func raiStringPath(m map[string]any, keys ...string) string {
	var v any = m
	for _, k := range keys {
		if i, e := strconv.Atoi(k); e == nil {
			a := raiSlice(v)
			if i < 0 || i >= len(a) {
				return ""
			}
			v = a[i]
		} else {
			v = raiMap(v)[k]
		}
	}
	return raiString(v)
}
func raiBoolPath(m map[string]any, keys ...string) bool {
	var valueAt any = m
	for _, key := range keys {
		valueAt = raiMap(valueAt)[key]
	}
	switch value := valueAt.(type) {
	case bool:
		return value
	case string:
		return strings.EqualFold(value, "true") || value == "1" || strings.EqualFold(value, "Y")
	}
	return false
}
func raiFirst(a ...string) string {
	for _, s := range a {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}
func raiJoin(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + " - " + b
}
func raiJoinSpace(a ...string) string { return strings.TrimSpace(strings.Join(a, " ")) }
func raiInt(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}
func raiDuration(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		parts := strings.Split(x, ":")
		n := 0.0
		for _, p := range parts {
			q, e := strconv.ParseFloat(strings.TrimSpace(p), 64)
			if e != nil {
				return 0
			}
			n = n*60 + q
		}
		return n
	}
	return float64(raiInt(v))
}
func raiTimestamp(s string) int64 {
	for _, l := range []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, e := time.Parse(l, strings.TrimSpace(s)); e == nil {
			return t.Unix()
		}
	}
	return 0
}
func raiSetString(o *value.Object, k, s string) {
	if s != "" {
		o.Set(k, value.String(s))
	}
}
func raiSetInt(o *value.Object, k string, n int64) {
	if n > 0 {
		o.Set(k, value.Int(n))
	}
}
func raiAddImages(o *value.Object, base string, images map[string]any) {
	keys := make([]string, 0, len(images))
	for key := range images {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	vals := make([]value.Value, 0, min(len(keys), raiMaxThumbs))
	seen := make(map[string]bool)
	for _, key := range keys {
		if len(vals) >= raiMaxThumbs {
			break
		}
		v := images[key]
		u, err := url.Parse(raiString(v))
		if err == nil && !u.IsAbs() {
			b, _ := url.Parse(base)
			u = b.ResolveReference(u)
		}
		if u != nil && raiPublicURL(u.String()) && !seen[u.String()] {
			seen[u.String()] = true
			vals = append(vals, value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(u.String())})))
		}
	}
	if len(vals) > 0 {
		o.Set("thumbnails", value.List(vals...))
		first, _ := vals[0].Object()
		if x, ok := first.Lookup("url").StringValue(); ok {
			o.Set("thumbnail", value.String(x))
		}
	}
}
func raiSubtitles(base string, v map[string]any) *value.Object {
	o := value.NewObject()
	seen := map[string]bool{}
	all := append(raiSlice(v["subtitlesArray"]), raiSlice(v["subtitleList"])...)
	for _, k := range []string{"subtitles", "subtitlesUrl"} {
		if s := raiString(v[k]); s != "" {
			all = append(all, map[string]any{"url": s})
		}
	}
	for _, x := range all {
		m := raiMap(x)
		raw := raiString(m["url"])
		u, e := url.Parse(raw)
		if e != nil {
			continue
		}
		if !u.IsAbs() {
			b, _ := url.Parse(base)
			u = b.ResolveReference(u)
		}
		if !raiPublicURL(u.String()) || seen[u.String()] || raiSubtitleCount(o) >= raiMaxSubs {
			continue
		}
		seen[u.String()] = true
		lang := raiFirst(raiString(m["language"]), "it")
		ext := strings.TrimPrefix(path.Ext(u.Path), ".")
		if ext == "" {
			ext = "srt"
		}
		raiAppendSubtitle(o, lang, u.String(), ext)
		if ext == "stl" {
			srt := strings.TrimSuffix(u.String(), ".stl") + ".srt"
			if !seen[srt] && raiPublicURL(srt) && raiSubtitleCount(o) < raiMaxSubs {
				seen[srt] = true
				raiAppendSubtitle(o, lang, srt, "srt")
			}
		}
	}
	return o
}

func raiAppendSubtitle(subtitles *value.Object, language, rawURL, ext string) {
	entry := value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(rawURL)}, value.Field{Key: "ext", Value: value.String(ext)}))
	if existing, ok := subtitles.Lookup(language).ListValue(); ok {
		subtitles.Set(language, value.List(append(existing, entry)...))
		return
	}
	subtitles.Set(language, value.List(entry))
}

func raiSubtitleCount(subtitles *value.Object) int {
	count := 0
	for _, field := range subtitles.Fields() {
		if entries, ok := field.Value.ListValue(); ok {
			count += len(entries)
		}
	}
	return count
}
