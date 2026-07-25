package extractor

// Panopto is a self-hosted-per-tenant video platform: every customer gets its
// own subdomain of panopto.com/panopto.eu, so exact-host routing here means
// "any non-empty subdomain of the two documented apex domains" rather than a
// single fixed hostname. Only the documented Viewer/Embed.aspx page shape with
// a playlist id (`pid`) or delivery id (`id`) query parameter is supported.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	panoptoMaxEntries = 256
	panoptoMaxStreams = 64
)

var panoptoPageAspx = regexp.MustCompile(`(?i)^/Panopto/Pages/(?:Viewer|Embed)\.aspx$`)

// panoptoBaseHost accepts only "<subdomain>.panopto.com" / "<subdomain>.panopto.eu"
// hosts, rejecting the bare apex (no tenant) and any hostile suffix-confusable
// host such as "evilpanopto.com".
func panoptoBaseHost(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	for _, suffix := range [...]string{".panopto.com", ".panopto.eu"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return host, true
		}
	}
	return "", false
}

func panoptoPagePath(parsed *url.URL) bool {
	return panoptoPageAspx.MatchString(parsed.EscapedPath())
}

// panoptoUniqueQuery returns a query value only when the key is present and
// every repeated value is identical. Conflicting duplicates (id=a&id=b) are
// rejected at the routing boundary.
func panoptoUniqueQuery(query url.Values, key string) (string, bool) {
	vals := query[key]
	if len(vals) == 0 {
		return "", false
	}
	first := strings.TrimSpace(vals[0])
	for _, v := range vals[1:] {
		if strings.TrimSpace(v) != first {
			return "", false
		}
	}
	return first, true
}

func parsePanoptoPlaylistURL(parsed *url.URL) (host, pid string, ok bool) {
	host, hostOK := panoptoBaseHost(parsed)
	if !hostOK || !panoptoPagePath(parsed) {
		return "", "", false
	}
	pid, pidOK := panoptoUniqueQuery(parsed.Query(), "pid")
	if !pidOK || !podcastUUID.MatchString(pid) {
		return "", "", false
	}
	return host, strings.ToLower(pid), true
}

// parsePanoptoVideoURL yields to PanoptoPlaylist whenever a valid pid is also
// present, mirroring PanoptoIE.suitable's explicit deference in the reference.
func parsePanoptoVideoURL(parsed *url.URL) (host, id string, ok bool) {
	host, hostOK := panoptoBaseHost(parsed)
	if !hostOK || !panoptoPagePath(parsed) {
		return "", "", false
	}
	query := parsed.Query()
	if pid, pidOK := panoptoUniqueQuery(query, "pid"); pidOK && podcastUUID.MatchString(pid) {
		return "", "", false
	}
	// Conflicting pid values still fail closed even when no valid playlist
	// pid is present (do not fall through to video routing).
	if vals := query["pid"]; len(vals) > 1 {
		first := strings.TrimSpace(vals[0])
		for _, v := range vals[1:] {
			if strings.TrimSpace(v) != first {
				return "", "", false
			}
		}
	}
	id, idOK := panoptoUniqueQuery(query, "id")
	if !idOK || !podcastUUID.MatchString(id) {
		return "", "", false
	}
	return host, strings.ToLower(id), true
}

// panoptoBoundViewerEntry rebuilds a same-tenant Viewer URL. When ViewerUri is
// present it must already target the original exact tenant host, a Viewer/Embed
// path, and the matching session id (no cross-tenant or id-mismatch handoff).
func panoptoBoundViewerEntry(tenantHost, itemID, viewerURI, title string) (Entry, bool) {
	id := strings.ToLower(strings.TrimSpace(itemID))
	if !podcastUUID.MatchString(id) || tenantHost == "" {
		return Entry{}, false
	}
	if strings.TrimSpace(viewerURI) != "" {
		viewerURL, err := url.Parse(viewerURI)
		if err != nil {
			return Entry{}, false
		}
		viewerHost, viewerHostOK := panoptoBaseHost(viewerURL)
		if !viewerHostOK || viewerHost != tenantHost || !panoptoPagePath(viewerURL) {
			return Entry{}, false
		}
		viewerID, viewerIDOK := panoptoUniqueQuery(viewerURL.Query(), "id")
		if !viewerIDOK || strings.ToLower(viewerID) != id {
			return Entry{}, false
		}
	}
	return Entry{
		URL:          "https://" + tenantHost + "/Panopto/Pages/Viewer.aspx?id=" + id,
		ExtractorKey: "panopto",
		ID:           id,
		Title:        title,
	}, true
}

func panoptoAPIError(code *int) error {
	if code == nil {
		return nil
	}
	if *code == 2 {
		return ErrAuthentication
	}
	// Deliberately do not echo the API's ErrorMessage text; it is untrusted
	// backend content and must never reach a diagnostic error string.
	return fmt.Errorf("%w: Panopto API error", ErrUnavailable)
}

type panoptoStreamJSON struct {
	StreamHttpUrl           string `json:"StreamHttpUrl"`
	StreamUrl               string `json:"StreamUrl"`
	ViewerMediaFileTypeName string `json:"ViewerMediaFileTypeName"`
}

func panoptoStreamFormats(prefix string, streams []panoptoStreamJSON, formats []value.Value, seen map[string]bool) []value.Value {
	for index, stream := range streams {
		rawURL := firstNonEmpty(stream.StreamHttpUrl, stream.StreamUrl)
		if rawURL == "" || seen[rawURL] {
			continue
		}
		format, ok := hostedURLFormat(fmt.Sprintf("%s-%d", prefix, index), rawURL)
		if !ok {
			continue
		}
		seen[rawURL] = true
		formats = append(formats, value.ObjectValue(format))
	}
	return formats
}

// Panopto is a minimal re-entry target for PanoptoPlaylist entries: it
// resolves a single delivery id into direct HTTP(S)/HLS formats. It
// intentionally does not replicate chapters, subtitles, or watched-marking
// from the reference PanoptoIE.
type Panopto struct{}

func NewPanopto() Panopto    { return Panopto{} }
func (Panopto) Name() string { return "panopto" }

func (Panopto) Suitable(parsed *url.URL) bool {
	_, _, ok := parsePanoptoVideoURL(parsed)
	return ok
}

func (Panopto) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	host, id, ok := parsePanoptoVideoURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	baseURL := "https://" + host + "/Panopto"
	endpoint := baseURL + "/Pages/Viewer/DeliveryInfo.aspx?deliveryId=" + id + "&responseType=json"
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	var payload struct {
		Delivery *struct {
			SessionName    string              `json:"SessionName"`
			Duration       float64             `json:"Duration"`
			PodcastStreams []panoptoStreamJSON `json:"PodcastStreams"`
			Streams        []panoptoStreamJSON `json:"Streams"`
		} `json:"Delivery"`
		ErrorCode *int `json:"ErrorCode"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, headers, &payload); err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if err := panoptoAPIError(payload.ErrorCode); err != nil {
		return Extraction{}, err
	}
	if payload.Delivery == nil {
		return Extraction{}, fmt.Errorf("%w: missing Panopto delivery", ErrInvalidMetadata)
	}
	if len(payload.Delivery.PodcastStreams)+len(payload.Delivery.Streams) > panoptoMaxStreams {
		return Extraction{}, fmt.Errorf("%w: Panopto stream overflow", ErrInvalidMetadata)
	}
	seen := make(map[string]bool)
	// Podcast (combined) streams are preferred first, matching reference order.
	formats := panoptoStreamFormats("podcast", payload.Delivery.PodcastStreams, nil, seen)
	formats = panoptoStreamFormats("stream", payload.Delivery.Streams, formats, seen)
	if len(formats) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing Panopto formats", ErrInvalidMetadata)
	}
	title := strings.TrimSpace(payload.Delivery.SessionName)
	if title == "" {
		title = id
	}
	fields := []value.Field{
		{Key: "id", Value: value.String(id)},
		{Key: "title", Value: value.String(title)},
		{Key: "webpage_url", Value: value.String(baseURL + "/Pages/Viewer.aspx?id=" + id)},
		{Key: "formats", Value: value.List(formats...)},
	}
	if payload.Delivery.Duration > 0 {
		fields = append(fields, value.Field{Key: "duration", Value: value.Float(payload.Delivery.Duration)})
	}
	return Media(value.NewInfo(value.NewObject(fields...))), nil
}

// PanoptoPlaylist lazily enumerates a Panopto playlist's session list via the
// documented two-call API sequence: Api/Playlists/{id} resolves the
// SessionListId, then Api/SessionLists/{sessionListId} returns the items.
// Both calls are deferred to the first Entries.Iterator().Next().
type PanoptoPlaylist struct{}

func NewPanoptoPlaylist() PanoptoPlaylist { return PanoptoPlaylist{} }
func (PanoptoPlaylist) Name() string      { return "panopto_playlist" }

func (PanoptoPlaylist) Suitable(parsed *url.URL) bool {
	_, _, ok := parsePanoptoPlaylistURL(parsed)
	return ok
}

func (PanoptoPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	host, pid, ok := parsePanoptoPlaylistURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	baseURL := "https://" + host + "/Panopto"
	canonical := baseURL + "/Pages/Viewer.aspx?pid=" + pid
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(pid)},
		value.Field{Key: "title", Value: value.String(pid)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(panoptoMaxEntries, func(ctx context.Context) ([]Entry, error) {
		headers := make(http.Header)
		headers.Set("Accept", "application/json")
		var playlist struct {
			SessionListId string `json:"SessionListId"`
			Name          string `json:"Name"`
			ErrorCode     *int   `json:"ErrorCode"`
		}
		playlistEndpoint := baseURL + "/Api/Playlists/" + pid
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, playlistEndpoint, nil, headers, &playlist); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := panoptoAPIError(playlist.ErrorCode); err != nil {
			return nil, err
		}
		sessionListID := strings.ToLower(strings.TrimSpace(playlist.SessionListId))
		if !podcastUUID.MatchString(sessionListID) {
			return nil, fmt.Errorf("%w: missing Panopto session list id", ErrInvalidMetadata)
		}
		itemsEndpoint := baseURL + "/Api/SessionLists/" + sessionListID + "?collections[0].maxCount=500&collections[0].name=items"
		var sessionList struct {
			Items []struct {
				TypeName  string `json:"TypeName"`
				Id        string `json:"Id"`
				ViewerUri string `json:"ViewerUri"`
				Name      string `json:"Name"`
			} `json:"Items"`
			ErrorCode *int `json:"ErrorCode"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, itemsEndpoint, nil, headers, &sessionList); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := panoptoAPIError(sessionList.ErrorCode); err != nil {
			return nil, err
		}
		if len(sessionList.Items) > panoptoMaxEntries {
			return nil, fmt.Errorf("%w: Panopto session list overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(sessionList.Items))
		seen := make(map[string]bool, len(sessionList.Items))
		for _, item := range sessionList.Items {
			if item.TypeName != "Session" {
				continue
			}
			id := strings.ToLower(strings.TrimSpace(item.Id))
			if seen[id] {
				continue
			}
			entry, ok := panoptoBoundViewerEntry(host, id, item.ViewerUri, item.Name)
			if !ok {
				continue
			}
			seen[id] = true
			entries = append(entries, entry)
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Panopto playlist", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}
