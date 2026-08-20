package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	ardAudiothekGraphQLURL          = "https://api.ardaudiothek.de/graphql"
	ardAudiothekMaxPlaylistEntries  = 10_000
	ardAudiothekMaxSlugBytes        = 256
	ardAudiothekMaxURNBytes         = 64
	ardAudiothekMaxTitleBytes       = 2048
	ardAudiothekMaxDescriptionBytes = 8192
	ardAudiothekMaxFormatIDBytes    = 64
	ardAudiothekMaxExtensionBytes   = 16
)

var (
	ardAudiothekEpisodeURNPattern = regexp.MustCompile(`^urn:ard:(?:episode|section|extra):[a-f0-9]{16}$`)
	ardAudiothekShowURNPattern    = regexp.MustCompile(`^urn:ard:show:[a-f0-9]{16}$`)
	ardAudiothekSlugPattern       = regexp.MustCompile(`^[\w-]{1,256}$`)
	ardAudiothekFormatIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	ardAudiothekExtensionPattern  = regexp.MustCompile(`^[a-z0-9]{1,16}$`)
)

const (
	ardAudiothekItemQuery = `query($id: ID!) {
 item(id: $id) {
 audioList {
 href
 distributionType
 audioBitrate
 audioCodec
 }
 show {
 title
 }
 image {
 url1X1
 }
 programSet {
 publicationService {
 organizationName
 }
 }
 description
 title
 duration
 startDate
 episodeNumber
 }
 }`

	ardAudiothekPlaylistQuery = `query($id: ID!) {
 show(id: $id) {
 title
 description
 items(filter: { isPublished: { equalTo: true } }) {
 nodes {
 url
 }
 }
 }
 }`
)

type ardAudiothekTarget struct {
	urn        string
	displayID  string
	webpageURL string
	playlist   bool
}

// ARDAudiothek extracts public ARD Audiothek and ARD Sounds episode pages.
type ARDAudiothek struct{}

func NewARDAudiothek() ARDAudiothek { return ARDAudiothek{} }

func (ARDAudiothek) Name() string { return "ard_audiothek" }

func (ARDAudiothek) Suitable(parsed *url.URL) bool {
	target, ok := classifyARDAudiothekURL(parsed)
	return ok && !target.playlist
}

func (ARDAudiothek) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyARDAudiothekURL(parsed)
	if !ok || target.playlist {
		return Extraction{}, ErrUnsupported
	}
	return extractARDAudiothekItem(ctx, request.Transport, target)
}

// ARDAudiothekPlaylist extracts bounded public ARD Audiothek show playlists.
type ARDAudiothekPlaylist struct{}

func NewARDAudiothekPlaylist() ARDAudiothekPlaylist { return ARDAudiothekPlaylist{} }

func (ARDAudiothekPlaylist) Name() string { return "ard_audiothek_playlist" }

func (ARDAudiothekPlaylist) Suitable(parsed *url.URL) bool {
	target, ok := classifyARDAudiothekURL(parsed)
	return ok && target.playlist
}

func (ARDAudiothekPlaylist) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyARDAudiothekURL(parsed)
	if !ok || !target.playlist {
		return Extraction{}, ErrUnsupported
	}
	return extractARDAudiothekPlaylist(ctx, request.Transport, target)
}

func classifyARDAudiothekURL(parsed *url.URL) (ardAudiothekTarget, bool) {
	if parsed == nil || parsed.User != nil || parsed.Port() != "" {
		return ardAudiothekTarget{}, false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return ardAudiothekTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	switch host {
	case "ardaudiothek.de", "www.ardaudiothek.de", "ardsounds.de", "www.ardsounds.de":
	default:
		return ardAudiothekTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) < 2 {
		return ardAudiothekTarget{}, false
	}
	switch parts[0] {
	case "episode":
		if len(parts) != 2 {
			return ardAudiothekTarget{}, false
		}
		urn := strings.TrimSuffix(parts[1], "/")
		if !ardAudiothekValidURN(urn, ardAudiothekEpisodeURNPattern) {
			return ardAudiothekTarget{}, false
		}
		return ardAudiothekTarget{urn: urn, displayID: urn, webpageURL: parsed.Scheme + "://" + host + "/episode/" + urn + "/"}, true
	case "sendung":
		if len(parts) != 3 {
			return ardAudiothekTarget{}, false
		}
		slug, urn := parts[1], parts[2]
		if !ardAudiothekSlugPattern.MatchString(slug) || !ardAudiothekValidURN(urn, ardAudiothekShowURNPattern) {
			return ardAudiothekTarget{}, false
		}
		return ardAudiothekTarget{
			urn:        urn,
			displayID:  slug,
			webpageURL: parsed.Scheme + "://" + host + "/sendung/" + slug + "/" + urn + "/",
			playlist:   true,
		}, true
	default:
		return ardAudiothekTarget{}, false
	}
}

func ardAudiothekValidURN(urn string, pattern *regexp.Regexp) bool {
	return len(urn) <= ardAudiothekMaxURNBytes && utf8.ValidString(urn) && pattern.MatchString(urn)
}

type ardAudiothekGraphQLEnvelope struct {
	Data   json.RawMessage   `json:"data"`
	Errors []json.RawMessage `json:"errors"`
}

type ardAudiothekItemPayload struct {
	Item *ardAudiothekItem `json:"item"`
}

type ardAudiothekShowPayload struct {
	Show *ardAudiothekShow `json:"show"`
}

type ardAudiothekItem struct {
	AudioList []struct {
		Href             string `json:"href"`
		DistributionType string `json:"distributionType"`
		AudioBitrate     int64  `json:"audioBitrate"`
		AudioCodec       string `json:"audioCodec"`
	} `json:"audioList"`
	Show struct {
		Title string `json:"title"`
	} `json:"show"`
	Image struct {
		URL1X1 string `json:"url1X1"`
	} `json:"image"`
	ProgramSet struct {
		PublicationService struct {
			OrganizationName string `json:"organizationName"`
		} `json:"publicationService"`
	} `json:"programSet"`
	Description   string `json:"description"`
	Title         string `json:"title"`
	Duration      int64  `json:"duration"`
	StartDate     string `json:"startDate"`
	EpisodeNumber int64  `json:"episodeNumber"`
}

type ardAudiothekShow struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Items       struct {
		Nodes []struct {
			URL string `json:"url"`
		} `json:"nodes"`
	} `json:"items"`
}

func extractARDAudiothekItem(ctx context.Context, transport Transport, target ardAudiothekTarget) (Extraction, error) {
	data, err := requestARDAudiothekGraphQL(ctx, transport, target.urn, ardAudiothekItemQuery)
	if err != nil {
		return Extraction{}, categorizeARDAudiothekError(err)
	}
	var payload ardAudiothekItemPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid ARD Audiothek item response", ErrInvalidMetadata)
	}
	item := payload.Item
	if item == nil {
		return Extraction{}, ErrUnavailable
	}
	formats := ardAudiothekFormats(item.AudioList)
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	title := ardAudiothekBoundedText(item.Title, ardAudiothekMaxTitleBytes)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing ARD Audiothek title", ErrInvalidMetadata)
	}
	ext, _ := formats[0].Object()
	extension, _ := ext.Lookup("ext").StringValue()
	if extension == "" {
		extension = "mp3"
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.urn)},
		value.Field{Key: "display_id", Value: value.String(target.displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
		value.Field{Key: "ext", Value: value.String(extension)},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	riskString(info, "description", ardAudiothekBoundedText(item.Description, ardAudiothekMaxDescriptionBytes))
	riskString(info, "series", ardAudiothekBoundedText(item.Show.Title, ardAudiothekMaxTitleBytes))
	riskString(info, "channel", ardAudiothekBoundedText(item.ProgramSet.PublicationService.OrganizationName, ardAudiothekMaxTitleBytes))
	riskPositiveInt(info, "duration", item.Duration)
	riskPositiveInt(info, "timestamp", riskTimestamp(item.StartDate))
	riskPositiveInt(info, "episode_number", item.EpisodeNumber)
	if thumbnail := ardAudiothekThumbnailURL(item.Image.URL1X1); thumbnail != "" {
		info.Set("thumbnail", value.String(thumbnail))
	}
	return Media(value.NewInfo(info)), nil
}

func extractARDAudiothekPlaylist(ctx context.Context, transport Transport, target ardAudiothekTarget) (Extraction, error) {
	data, err := requestARDAudiothekGraphQL(ctx, transport, target.urn, ardAudiothekPlaylistQuery)
	if err != nil {
		return Extraction{}, categorizeARDAudiothekError(err)
	}
	var payload ardAudiothekShowPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid ARD Audiothek playlist response", ErrInvalidMetadata)
	}
	show := payload.Show
	if show == nil {
		return Extraction{}, ErrUnavailable
	}
	entries, err := ardAudiothekPlaylistEntries(show)
	if err != nil {
		return Extraction{}, err
	}
	title := ardAudiothekBoundedText(show.Title, ardAudiothekMaxTitleBytes)
	if title == "" {
		title = target.displayID
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.urn)},
		value.Field{Key: "display_id", Value: value.String(target.displayID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL)},
	)
	riskString(info, "description", ardAudiothekBoundedText(show.Description, ardAudiothekMaxDescriptionBytes))
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func ardAudiothekPlaylistEntries(show *ardAudiothekShow) ([]Entry, error) {
	nodes := show.Items.Nodes
	entries := make([]Entry, 0, len(nodes))
	seen := make(map[string]bool, len(nodes))
	for _, node := range nodes {
		rawURL := strings.TrimSpace(node.URL)
		if rawURL == "" || !validHTTPURL(rawURL) {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil {
			continue
		}
		target, ok := classifyARDAudiothekURL(parsed)
		if !ok || target.playlist {
			continue
		}
		if seen[target.webpageURL] {
			continue
		}
		if len(entries) >= ardAudiothekMaxPlaylistEntries {
			return nil, fmt.Errorf("%w: ARD Audiothek playlist entry overflow", ErrInvalidPlaylist)
		}
		seen[target.webpageURL] = true
		entries = append(entries, Entry{
			URL:          target.webpageURL,
			ExtractorKey: "ard_audiothek",
			ID:           target.urn,
			Transparent:  true,
		})
	}
	return entries, nil
}

func ardAudiothekFormats(audioList []struct {
	Href             string `json:"href"`
	DistributionType string `json:"distributionType"`
	AudioBitrate     int64  `json:"audioBitrate"`
	AudioCodec       string `json:"audioCodec"`
}) []value.Value {
	formats := make([]value.Value, 0, len(audioList))
	seen := make(map[string]bool, len(audioList))
	for _, source := range audioList {
		if seen[source.Href] {
			continue
		}
		formatID, ok := ardAudiothekValidFormatID(source.DistributionType)
		if !ok {
			continue
		}
		format, ok := ardAudiothekAudioFormat(source.Href, formatID)
		if !ok {
			continue
		}
		seen[source.Href] = true
		riskPositiveInt(format, "abr", source.AudioBitrate)
		riskString(format, "acodec", source.AudioCodec)
		format.Set("vcodec", value.String("none"))
		format.Set("_credential_isolated", value.Bool(true))
		formats = append(formats, value.ObjectValue(format))
	}
	return formats
}

func ardAudiothekAudioFormat(rawURL, formatID string) (*value.Object, bool) {
	if !strictValidHostedHTTPURL(rawURL) {
		return nil, false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, false
	}
	ext, ok := ardAudiothekValidExtension(rawURL)
	if !ok {
		return nil, false
	}
	if formatID == "" {
		formatID = "http"
	}
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(formatID)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String(ext)},
		value.Field{Key: "protocol", Value: value.String(strings.ToLower(parsed.Scheme))},
	), true
}

func ardAudiothekValidFormatID(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "http", true
	}
	if len(raw) > ardAudiothekMaxFormatIDBytes || !utf8.ValidString(raw) || !ardAudiothekFormatIDPattern.MatchString(raw) {
		return "", false
	}
	return raw, true
}

func ardAudiothekValidExtension(rawURL string) (string, bool) {
	ext := strings.ToLower(strings.TrimPrefix(pathExt(rawURL), "."))
	if ext == "" {
		return "mp3", true
	}
	if len(ext) > ardAudiothekMaxExtensionBytes || !utf8.ValidString(ext) || !ardAudiothekExtensionPattern.MatchString(ext) {
		return "", false
	}
	return ext, true
}

func ardAudiothekThumbnailURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !validHTTPURL(rawURL) {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func ardAudiothekBoundedText(text string, maxBytes int) string {
	if text == "" || !utf8.ValidString(text) {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	for maxBytes > 0 && !utf8.ValidString(text[:maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}

func requestARDAudiothekGraphQL(ctx context.Context, transport Transport, urn, query string) (json.RawMessage, error) {
	if transport == nil || urn == "" || query == "" {
		return nil, fmt.Errorf("%w: invalid ARD Audiothek GraphQL request", ErrInvalidMetadata)
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	payload, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]string{"id": urn},
	})
	if err != nil {
		return nil, fmt.Errorf("%w: invalid ARD Audiothek GraphQL request", ErrInvalidMetadata)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	var envelope ardAudiothekGraphQLEnvelope
	if err := requestJSON(ctx, isolated.DoWithoutCredentialsNoRedirect, http.MethodPost, ardAudiothekGraphQLURL, payload, headers, &envelope); err != nil {
		return nil, err
	}
	if len(envelope.Errors) > 0 {
		return nil, fmt.Errorf("%w: ARD Audiothek GraphQL errors", ErrInvalidMetadata)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return nil, fmt.Errorf("%w: missing ARD Audiothek GraphQL data", ErrInvalidMetadata)
	}
	return envelope.Data, nil
}

func categorizeARDAudiothekError(err error) error {
	switch riskHTTPStatus(err) {
	case http.StatusUnauthorized:
		return ErrAuthentication
	case http.StatusForbidden, http.StatusUnavailableForLegalReasons:
		return ErrRegionRestricted
	case http.StatusNotFound, http.StatusGone:
		return ErrUnavailable
	default:
		return err
	}
}
