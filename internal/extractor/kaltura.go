package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	kalturaDefaultService = "https://cdnapi.kaltura.com"
	kalturaServicePath    = "/api_v3/service/multirequest"
	kalturaMaxPlaylist    = 10_000
)

var (
	kalturaPartnerPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,128}$`)
	kalturaEntryPattern   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)
	// The page response is bounded by the operation transport.  This recognises
	// only package data that is a JSON object, then uses the bounded JSON parser.
	kalturaPackageData = regexp.MustCompile(`(?s)window\.kalturaIframePackageData\s*=\s*`)
)

// Kaltura implements documented multirequest metadata/flavor endpoints for
// normal HTML5 and kwidget links.  It also accepts the kaltura: URL used by
// embed producers; the product registry currently requires HTTP(S) roots, so
// callers resolving a lazy kaltura entry should use SelectFor/Extract directly.
type Kaltura struct{}

func NewKaltura() Kaltura    { return Kaltura{} }
func (Kaltura) Name() string { return "kaltura" }

func (Kaltura) Suitable(parsed *url.URL) bool {
	_, ok := parseKalturaURL(parsed)
	return ok
}

func (Kaltura) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parseKalturaURL(parsed)
	if !ok || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if target.playlistID != "" {
		return extractKalturaPlaylist(ctx, request.Transport, target)
	}
	info, flavors, captions, err := requestKalturaMedia(ctx, request.Transport, target)
	if err != nil {
		return Extraction{}, err
	}
	return normalizeKalturaMedia(info, flavors, captions, target)
}

type kalturaTarget struct {
	partnerID  string
	entryID    string
	playerType string
	serviceURL string
	ks         string
	playlistID string
	canonical  string
	pageURL    string
}

func parseKalturaURL(parsed *url.URL) (kalturaTarget, bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return kalturaTarget{}, false
	}
	if parsed.Scheme == "kaltura" {
		parts := strings.Split(parsed.Opaque, ":")
		if len(parts) < 2 || len(parts) > 3 || !kalturaPartnerPattern.MatchString(parts[0]) || !kalturaEntryPattern.MatchString(parts[1]) {
			return kalturaTarget{}, false
		}
		player := "html5"
		if len(parts) == 3 && parts[2] != "" {
			if parts[2] != "html5" && parts[2] != "kwidget" {
				return kalturaTarget{}, false
			}
			player = parts[2]
		}
		return kalturaTarget{partnerID: parts[0], entryID: parts[1], playerType: player, serviceURL: kalturaDefaultService, canonical: "kaltura:" + strings.Join(parts, ":")}, true
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return kalturaTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "kaltura.com" && host != "www.kaltura.com" && host != "cdnapi.kaltura.com" && host != "cdnapisec.kaltura.com" {
		return kalturaTarget{}, false
	}
	query := parsed.Query()
	params := make(map[string]string)
	for key, values := range query {
		if len(values) > 0 {
			params[key] = values[0]
		}
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(segments); index += 2 {
		if _, exists := params[segments[index]]; !exists {
			params[segments[index]] = segments[index+1]
		}
	}
	partner := strings.TrimPrefix(firstNonEmpty(params["wid"], params["p"], params["partner_id"]), "_")
	entry := firstNonEmpty(params["entry_id"], params["entryId"])
	playlist := params["flashvars[playlistAPI.kpl0Id]"]
	if !kalturaPartnerPattern.MatchString(partner) || (entry == "" && playlist == "") || (entry != "" && !kalturaEntryPattern.MatchString(entry)) || (playlist != "" && !kalturaEntryPattern.MatchString(playlist)) {
		return kalturaTarget{}, false
	}
	player := "html5"
	if strings.Contains(parsed.Path, "html5lib/v2") {
		player = "kwidget"
	}
	serviceURL := kalturaDefaultService
	if host == "cdnapi.kaltura.com" || host == "cdnapisec.kaltura.com" {
		serviceURL = "https://" + host
	}
	canonical := "kaltura:" + partner + ":" + firstNonEmpty(entry, playlist) + ":" + player
	return kalturaTarget{partnerID: partner, entryID: entry, playerType: player, serviceURL: serviceURL, ks: params["flashvars[ks]"], playlistID: playlist, canonical: canonical, pageURL: parsed.String()}, true
}

func firstNonEmpty(inputs ...string) string {
	for _, input := range inputs {
		if input != "" {
			return input
		}
	}
	return ""
}

func requestKalturaMedia(ctx context.Context, transport Transport, target kalturaTarget) (kalturaEntry, []kalturaFlavor, []kalturaCaption, error) {
	actions := kalturaActions(target)
	body, err := json.Marshal(actions)
	if err != nil {
		return kalturaEntry{}, nil, nil, fmt.Errorf("%w: encode Kaltura request", ErrInvalidMetadata)
	}
	var response []json.RawMessage
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	if err := hostedRequestJSON(ctx, transport, http.MethodPost, strings.TrimRight(target.serviceURL, "/")+kalturaServicePath, body, headers, &response); err != nil {
		return kalturaEntry{}, nil, nil, err
	}
	if len(response) == 0 || len(response) > 16 {
		return kalturaEntry{}, nil, nil, fmt.Errorf("%w: malformed Kaltura multirequest", ErrInvalidMetadata)
	}
	if err := kalturaResponseError(response); err != nil {
		return kalturaEntry{}, nil, nil, err
	}
	var info kalturaObjectList[kalturaEntry]
	var flavors kalturaObjectList[kalturaFlavor]
	var captions kalturaObjectList[kalturaCaption]
	for _, item := range response {
		var envelope struct {
			Objects json.RawMessage `json:"objects"`
		}
		if json.Unmarshal(item, &envelope) != nil || len(envelope.Objects) == 0 {
			continue
		}
		if len(info.Objects) == 0 {
			_ = json.Unmarshal(item, &info)
			if len(info.Objects) > 0 && info.Objects[0].ID != "" {
				continue
			}
			info.Objects = nil
		}
		if len(flavors.Objects) == 0 {
			_ = json.Unmarshal(item, &flavors)
			if len(flavors.Objects) > 0 && flavors.Objects[0].ID != "" {
				continue
			}
			flavors.Objects = nil
		}
		if len(captions.Objects) == 0 {
			_ = json.Unmarshal(item, &captions)
		}
	}
	if len(info.Objects) == 0 || info.Objects[0].ID == "" {
		return kalturaEntry{}, nil, nil, fmt.Errorf("%w: missing Kaltura entry", ErrInvalidMetadata)
	}
	// Flavor and caption action positions are stable in the API request we send.
	if len(response) >= 4 {
		_ = json.Unmarshal(response[len(response)-2], &flavors)
		_ = json.Unmarshal(response[len(response)-1], &captions)
	}
	return info.Objects[0], flavors.Objects, captions.Objects, nil
}

func kalturaActions(target kalturaTarget) []map[string]any {
	widgetID := target.partnerID
	if !strings.HasPrefix(widgetID, "_") {
		widgetID = "_" + widgetID
	}
	return []map[string]any{
		{"apiVersion": "3.3.0", "clientTag": "html5:ytdlp-go", "format": 1, "ks": "", "partnerId": target.partnerID},
		{"expiry": 86400, "service": "session", "action": "startWidgetSession", "widgetId": widgetID},
		{"action": "list", "service": "baseentry", "ks": "{1:result:ks}", "filter": map[string]string{"redirectFromEntryId": target.entryID}, "responseProfile": map[string]any{"type": 1, "fields": "createdAt,dataUrl,description,duration,name,plays,thumbnailUrl,userId"}},
		{"action": "getbyentryid", "service": "flavorAsset", "entryId": target.entryID, "ks": "{1:result:ks}"},
		{"action": "list", "service": "caption_captionasset", "filter:entryIdEqual": target.entryID, "ks": "{1:result:ks}"},
	}
}

type kalturaObjectList[T any] struct {
	Objects []T `json:"objects"`
}
type kalturaEntry struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description"`
	DataURL      string        `json:"dataUrl"`
	Duration     hostingNumber `json:"duration"`
	CreatedAt    hostingNumber `json:"createdAt"`
	ThumbnailURL string        `json:"thumbnailUrl"`
	UserID       string        `json:"userId"`
	Plays        hostingNumber `json:"plays"`
}
type kalturaFlavor struct {
	ID              string        `json:"id"`
	Status          hostingNumber `json:"status"`
	FileExt         string        `json:"fileExt"`
	ContainerFormat string        `json:"containerFormat"`
	Bitrate         hostingNumber `json:"bitrate"`
	FrameRate       hostingNumber `json:"frameRate"`
	Size            hostingNumber `json:"size"`
	Width           hostingNumber `json:"width"`
	Height          hostingNumber `json:"height"`
	VideoCodecID    string        `json:"videoCodecId"`
	IsOriginal      bool          `json:"isOriginal"`
}
type kalturaCaption struct {
	ID           string        `json:"id"`
	Status       hostingNumber `json:"status"`
	LanguageCode string        `json:"languageCode"`
	Language     string        `json:"language"`
	FileExt      string        `json:"fileExt"`
	Format       hostingNumber `json:"format"`
}

func kalturaResponseError(response []json.RawMessage) error {
	for _, raw := range response {
		var apiError struct {
			ObjectType string `json:"objectType"`
			Code       string `json:"code"`
			Message    string `json:"message"`
		}
		if json.Unmarshal(raw, &apiError) != nil || apiError.ObjectType != "KalturaAPIException" {
			continue
		}
		code := strings.ToUpper(apiError.Code)
		message := strings.ToUpper(apiError.Message)
		switch {
		case strings.Contains(code, "GEO") || strings.Contains(message, "GEO"):
			return ErrRegionRestricted
		case strings.Contains(code, "AUTH"), strings.Contains(code, "FORBIDDEN"), strings.Contains(code, "SESSION"):
			return ErrAuthentication
		case strings.Contains(code, "NOT_FOUND"), strings.Contains(code, "ENTRY_ID"):
			return ErrUnavailable
		default:
			return ErrUnavailable
		}
	}
	return nil
}

func normalizeKalturaMedia(info kalturaEntry, flavors []kalturaFlavor, captions []kalturaCaption, target kalturaTarget) (Extraction, error) {
	if info.ID == "" || info.ID != target.entryID || strings.TrimSpace(info.Name) == "" || !validHostedHTTPURL(info.DataURL) {
		return Extraction{}, fmt.Errorf("%w: malformed Kaltura media", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, len(flavors)+1)
	for _, flavor := range flavors {
		if flavor.ID == "" || flavor.Status.int64() != 2 || strings.EqualFold(flavor.FileExt, "chun") || strings.EqualFold(flavor.FileExt, "wvm") {
			continue
		}
		formatURL := strings.TrimRight(info.DataURL, "/") + "/flavorId/" + url.PathEscape(flavor.ID)
		format, ok := hostedURLFormat(""+kalturaFlavorID(flavor), kalturaSignedURL(formatURL, target.ks))
		if !ok {
			continue
		}
		extension := strings.ToLower(flavor.FileExt)
		if extension == "" {
			extension = "mp4"
			if strings.EqualFold(flavor.ContainerFormat, "qt") {
				extension = "mov"
			}
		}
		format.Set("ext", value.String(extension))
		hostedSetInt(format, "tbr", flavor.Bitrate.int64())
		hostedSetInt(format, "width", flavor.Width.int64())
		hostedSetInt(format, "height", flavor.Height.int64())
		hostedSetInt(format, "fps", flavor.FrameRate.int64())
		hostedSetInt(format, "filesize_approx", flavor.Size.int64()*1024)
		hostedSetString(format, "container", flavor.ContainerFormat)
		if flavor.VideoCodecID != "" {
			format.Set("vcodec", value.String(flavor.VideoCodecID))
		} else if flavor.FrameRate.int64() == 0 {
			format.Set("vcodec", value.String("none"))
		}
		formats = append(formats, value.ObjectValue(format))
	}
	if strings.Contains(info.DataURL, "/playManifest/") {
		hlsURL := strings.Replace(info.DataURL, "format/url", "format/applehttp", 1)
		hlsURL = kalturaSignedURL(hlsURL, target.ks)
		if validHostedHTTPURL(hlsURL) {
			formats = append(formats, value.ObjectValue(manifestFormat("hls", hlsURL, "m3u8_native")))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	infoObject := value.NewObject(
		value.Field{Key: "id", Value: value.String(info.ID)},
		value.Field{Key: "title", Value: value.String(info.Name)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
	)
	hostedSetString(infoObject, "description", info.Description)
	hostedSetString(infoObject, "thumbnail", firstHostedURL(info.ThumbnailURL))
	hostedSetString(infoObject, "uploader_id", info.UserID)
	hostedSetFloat(infoObject, "duration", info.Duration.float64())
	hostedSetInt(infoObject, "timestamp", info.CreatedAt.int64())
	hostedSetInt(infoObject, "view_count", info.Plays.int64())
	if subtitles := kalturaSubtitles(captions, target.serviceURL); !subtitles.IsMissing() {
		infoObject.Set("subtitles", subtitles)
	}
	return Media(value.NewInfo(infoObject)), nil
}

func kalturaFlavorID(flavor kalturaFlavor) string {
	extension := strings.ToLower(flavor.FileExt)
	if extension == "" {
		extension = "mp4"
	}
	if bitrate := flavor.Bitrate.int64(); bitrate > 0 {
		return fmt.Sprintf("%s-%d", extension, bitrate)
	}
	return extension + "-source"
}

func kalturaSignedURL(rawURL, ks string) string {
	if ks == "" || len(ks) > 2048 || strings.ContainsAny(ks, "\r\n") {
		return rawURL
	}
	return strings.TrimRight(rawURL, "/") + "/ks/" + url.PathEscape(ks)
}

func kalturaSubtitles(captions []kalturaCaption, serviceURL string) value.Value {
	byLanguage := make(map[string][]value.Value)
	for _, caption := range captions {
		if caption.ID == "" || caption.Status.int64() != 2 {
			continue
		}
		language := firstNonEmpty(caption.LanguageCode, caption.Language)
		if language == "" {
			language = "und"
		}
		extension := strings.ToLower(caption.FileExt)
		if extension == "" {
			extension = map[int64]string{1: "srt", 2: "ttml", 3: "vtt"}[caption.Format.int64()]
		}
		if extension == "" {
			extension = "ttml"
		}
		captionURL := strings.TrimRight(serviceURL, "/") + "/api_v3/service/caption_captionasset/action/serve/captionAssetId/" + url.PathEscape(caption.ID)
		byLanguage[language] = append(byLanguage[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(captionURL)}, value.Field{Key: "ext", Value: value.String(extension)})))
	}
	if len(byLanguage) == 0 {
		return value.Missing()
	}
	languages := make([]string, 0, len(byLanguage))
	for language := range byLanguage {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	output := value.NewObject()
	for _, language := range languages {
		output.Set(language, value.List(byLanguage[language]...))
	}
	return value.ObjectValue(output)
}

func extractKalturaPlaylist(ctx context.Context, transport Transport, target kalturaTarget) (Extraction, error) {
	// The Playlist API is rendered into the iframe package data. Fetching it is
	// required only for playlist routes; normal videos use no HTML parsing.
	pageURL := target.pageURL
	if pageURL == "" {
		pageURL = "https://www.kaltura.com/index.php/kwidget/wid/_" + target.partnerID + "/uiconf_id/1?flashvars%5BplaylistAPI.kpl0Id%5D=" + url.QueryEscape(target.playlistID)
	}
	page, _, err := transport.ReadPage(ctx, pageURL)
	if err != nil {
		return Extraction{}, err
	}
	raw, err := extractJSONObjectAfter(page, kalturaPackageData)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: Kaltura playlist package", ErrInvalidMetadata)
	}
	var payload struct {
		PlaylistResult map[string]struct {
			Name  string `json:"name"`
			Items []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"items"`
		} `json:"playlistResult"`
	}
	if json.Unmarshal(raw, &payload) != nil {
		return Extraction{}, fmt.Errorf("%w: malformed Kaltura playlist", ErrInvalidMetadata)
	}
	playlist := payload.PlaylistResult[target.playlistID]
	if len(playlist.Items) == 0 {
		return Extraction{}, ErrUnavailable
	}
	if len(playlist.Items) > kalturaMaxPlaylist {
		return Extraction{}, ErrPlaylistLimit
	}
	entries := make([]Entry, 0, len(playlist.Items))
	for _, item := range playlist.Items {
		if kalturaEntryPattern.MatchString(item.ID) {
			entries = append(entries, Entry{URL: "kaltura:" + target.partnerID + ":" + item.ID + ":" + target.playerType, ExtractorKey: "kaltura", ID: item.ID, Title: item.Name})
		}
	}
	if len(entries) == 0 {
		return Extraction{}, ErrUnavailable
	}
	title := playlist.Name
	if title == "" {
		title = "Kaltura playlist " + target.playlistID
	}
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(target.playlistID)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "webpage_url", Value: value.String(target.canonical)})), StaticEntries(entries...))
}

func extractJSONObjectAfter(page []byte, marker *regexp.Regexp) ([]byte, error) {
	location := marker.FindIndex(page)
	if location == nil {
		return nil, fmt.Errorf("marker absent")
	}
	start := location[1]
	for start < len(page) && (page[start] == ' ' || page[start] == '\t' || page[start] == '\n' || page[start] == '\r') {
		start++
	}
	if start >= len(page) || page[start] != '{' {
		return nil, fmt.Errorf("object absent")
	}
	depth, quoted, escaped := 0, false, false
	for index := start; index < len(page) && int64(index-start) <= maxExtractorJSONBytes; index++ {
		character := page[index]
		if quoted {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				quoted = false
			}
			continue
		}
		if character == '"' {
			quoted = true
			continue
		}
		if character == '{' {
			depth++
		}
		if character == '}' {
			depth--
			if depth == 0 {
				return append([]byte(nil), page[start:index+1]...), nil
			}
		}
	}
	return nil, fmt.Errorf("unterminated object")
}

var _ Extractor = Kaltura{}
