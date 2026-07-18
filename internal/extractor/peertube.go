package extractor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	peerTubeMaxFiles    = 4096
	peerTubeMaxCaptions = 256
	peerTubeMaxTags     = 1024
)

var (
	ErrPeerTubeNetwork = errors.New("PeerTube network request failed")

	peerTubeShortID  = regexp.MustCompile(`^[0-9A-Za-z]{22}$`)
	peerTubeUUID     = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	peerTubeDNSLabel = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	peerTubeHeight   = regexp.MustCompile(`^([0-9]{1,5})p$`)
)

// PeerTube extracts one video from a federated PeerTube instance. HTTP URLs
// are routed only for recognized PeerTube-style hosts. The explicit
// peertube:host:id form supports other instances without claiming arbitrary
// web pages in the product registry.
type PeerTube struct{}

func NewPeerTube() PeerTube   { return PeerTube{} }
func (PeerTube) Name() string { return "peertube" }

func (PeerTube) Suitable(parsed *url.URL) bool {
	_, ok := parsePeerTubeURL(parsed)
	return ok
}

func (PeerTube) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := parsePeerTubeURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}

	var video peerTubeVideo
	if err := requestPeerTubeJSON(ctx, request.Transport, target.apiURL(""), &video); err != nil {
		return Extraction{}, err
	}
	if len(video.Description) >= 250 {
		var full struct {
			Description string `json:"description"`
		}
		if err := requestPeerTubeJSON(ctx, request.Transport, target.apiURL("description"), &full); err == nil && strings.TrimSpace(full.Description) != "" {
			video.Description = full.Description
		} else if contextError(ctx) != nil {
			return Extraction{}, contextError(ctx)
		}
	}

	var captions peerTubeCaptions
	captionErr := requestPeerTubeJSON(ctx, request.Transport, target.apiURL("captions"), &captions)
	if contextError(ctx) != nil {
		return Extraction{}, contextError(ctx)
	}
	if captionErr != nil {
		captions.Data = nil // captions are optional, matching the reference behavior
	}
	return normalizePeerTube(target, video, captions)
}

type peerTubeTarget struct {
	host string
	id   string
}

func (target peerTubeTarget) apiURL(suffix string) string {
	endpoint := "https://" + target.host + "/api/v1/videos/" + url.PathEscape(target.id)
	if suffix != "" {
		endpoint += "/" + suffix
	}
	return endpoint
}

func (target peerTubeTarget) webpageURL() string {
	return "https://" + target.host + "/videos/watch/" + target.id
}

func parsePeerTubeURL(parsed *url.URL) (peerTubeTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > sharedHostingMaxURLBytes {
		return peerTubeTarget{}, false
	}
	if strings.EqualFold(parsed.Scheme, "peertube") {
		if parsed.User != nil || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
			return peerTubeTarget{}, false
		}
		parts := strings.Split(parsed.Opaque, ":")
		if len(parts) != 2 || !validPeerTubeHost(parts[0]) || !validPeerTubeID(parts[1]) {
			return peerTubeTarget{}, false
		}
		return peerTubeTarget{host: strings.ToLower(parts[0]), id: parts[1]}, true
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" || parsed.Fragment != "" {
		return peerTubeTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if !validPeerTubeHost(host) || !recognizedPeerTubeHost(host) {
		return peerTubeTarget{}, false
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	var id string
	switch {
	case len(parts) == 3 && parts[0] == "videos" && (parts[1] == "watch" || parts[1] == "embed"):
		id = parts[2]
	case len(parts) == 2 && parts[0] == "w":
		id = parts[1]
	case len(parts) == 5 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "videos":
		// An API URL may include one supported terminal resource.
		if parts[4] != "description" && parts[4] != "captions" {
			return peerTubeTarget{}, false
		}
		id = parts[3]
	case len(parts) == 4 && parts[0] == "api" && parts[1] == "v1" && parts[2] == "videos":
		id = parts[3]
	default:
		return peerTubeTarget{}, false
	}
	decodedID, err := url.PathUnescape(id)
	if err != nil || decodedID != id || !validPeerTubeID(id) {
		return peerTubeTarget{}, false
	}
	return peerTubeTarget{host: host, id: id}, true
}

func validPeerTubeID(id string) bool {
	return peerTubeShortID.MatchString(id) || peerTubeUUID.MatchString(id)
}

func validPeerTubeHost(host string) bool {
	if len(host) < 3 || len(host) > 253 || strings.HasSuffix(host, ".") {
		return false
	}
	if net.ParseIP(host) != nil {
		return false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".internal") {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !peerTubeDNSLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func recognizedPeerTubeHost(host string) bool {
	for _, label := range strings.Split(host, ".") {
		if strings.HasPrefix(label, "peertube") {
			return true
		}
	}
	switch host {
	case "framatube.org", "tilvids.com", "diode.zone", "video.blender.org", "tube.tchncs.de", "canard.tube", "toobnix.org":
		return true
	default:
		return false
	}
}

func requestPeerTubeJSON(ctx context.Context, transport Transport, endpoint string, target any) error {
	headers := make(http.Header)
	headers.Set("Accept", "application/json")
	err := hostedRequestJSON(ctx, transport, http.MethodGet, endpoint, nil, headers, target)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrAuthentication) || errors.Is(err, ErrRegionRestricted) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) {
		return err
	}
	return ErrPeerTubeNetwork
}

type peerTubeVideo struct {
	UUID               string         `json:"uuid"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	ThumbnailPath      string         `json:"thumbnailPath"`
	PublishedAt        string         `json:"publishedAt"`
	Duration           hostingNumber  `json:"duration"`
	Views              hostingNumber  `json:"views"`
	Likes              hostingNumber  `json:"likes"`
	Dislikes           hostingNumber  `json:"dislikes"`
	NSFW               *bool          `json:"nsfw"`
	IsLive             bool           `json:"isLive"`
	Tags               []string       `json:"tags"`
	Category           peerTubeLabel  `json:"category"`
	Language           peerTubeLabel  `json:"language"`
	Licence            peerTubeLabel  `json:"licence"`
	Account            peerTubeOwner  `json:"account"`
	Channel            peerTubeOwner  `json:"channel"`
	Files              []peerTubeFile `json:"files"`
	StreamingPlaylists []struct {
		PlaylistURL string         `json:"playlistUrl"`
		Files       []peerTubeFile `json:"files"`
	} `json:"streamingPlaylists"`
}

type peerTubeLabel struct {
	ID    hostingNumber `json:"id"`
	Label string        `json:"label"`
}

type peerTubeOwner struct {
	ID          hostingNumber `json:"id"`
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	URL         string        `json:"url"`
}

type peerTubeFile struct {
	FileURL    string        `json:"fileUrl"`
	Size       hostingNumber `json:"size"`
	FPS        hostingNumber `json:"fps"`
	Resolution peerTubeLabel `json:"resolution"`
}

type peerTubeCaptions struct {
	Data []struct {
		CaptionPath string `json:"captionPath"`
		Language    struct {
			ID string `json:"id"`
		} `json:"language"`
	} `json:"data"`
}

func normalizePeerTube(target peerTubeTarget, video peerTubeVideo, captions peerTubeCaptions) (Extraction, error) {
	title := strings.TrimSpace(video.Name)
	if title == "" {
		return Extraction{}, fmt.Errorf("%w: missing PeerTube title", ErrInvalidMetadata)
	}
	if len(video.Files) > peerTubeMaxFiles || len(video.StreamingPlaylists) > peerTubeMaxFiles || len(captions.Data) > peerTubeMaxCaptions || len(video.Tags) > peerTubeMaxTags {
		return Extraction{}, fmt.Errorf("%w: PeerTube response exceeds collection limits", ErrInvalidMetadata)
	}

	formats := make([]value.Value, 0, len(video.Files)+len(video.StreamingPlaylists))
	seen := make(map[string]bool)
	appendFile := func(file peerTubeFile, fallbackID string) {
		rawURL := peerTubeAssetURL(target.host, file.FileURL)
		if rawURL == "" || seen[rawURL] {
			return
		}
		formatID := strings.TrimSpace(file.Resolution.Label)
		if formatID == "" {
			formatID = fallbackID
		}
		format, ok := hostedURLFormat(formatID, rawURL)
		if !ok {
			return
		}
		seen[rawURL] = true
		hostedSetInt(format, "filesize", file.Size.int64())
		if match := peerTubeHeight.FindStringSubmatch(formatID); len(match) == 2 {
			height, _ := strconv.ParseInt(match[1], 10, 64)
			hostedSetInt(format, "height", height)
		}
		if formatID == "0p" {
			format.Set("vcodec", value.String("none"))
		} else {
			hostedSetInt(format, "fps", file.FPS.int64())
		}
		formats = append(formats, value.ObjectValue(format))
	}
	for index, file := range video.Files {
		appendFile(file, fmt.Sprintf("http-%d", index+1))
	}
	processedFiles := len(video.Files)
	for playlistIndex, playlist := range video.StreamingPlaylists {
		playlistURL := peerTubeAssetURL(target.host, playlist.PlaylistURL)
		if playlistURL != "" && !seen[playlistURL] {
			if format, ok := hostedURLFormat(fmt.Sprintf("hls-%d", playlistIndex+1), playlistURL); ok {
				seen[playlistURL] = true
				formats = append(formats, value.ObjectValue(format))
			}
		}
		if len(playlist.Files) > peerTubeMaxFiles-processedFiles {
			return Extraction{}, fmt.Errorf("%w: PeerTube response exceeds file limits", ErrInvalidMetadata)
		}
		processedFiles += len(playlist.Files)
		for fileIndex, file := range playlist.Files {
			appendFile(file, fmt.Sprintf("stream-%d-%d", playlistIndex+1, fileIndex+1))
		}
	}
	if len(formats) == 0 {
		return Extraction{}, ErrUnavailable
	}
	sort.SliceStable(formats, func(i, j int) bool {
		left, _ := formats[i].Object()
		right, _ := formats[j].Object()
		leftID, _ := left.Lookup("format_id").StringValue()
		rightID, _ := right.Lookup("format_id").StringValue()
		return leftID < rightID
	})

	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.webpageURL())},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formats...)},
		value.Field{Key: "is_live", Value: value.Bool(video.IsLive)},
	)
	hostedSetString(info, "description", video.Description)
	hostedSetString(info, "thumbnail", peerTubeAssetURL(target.host, video.ThumbnailPath))
	hostedSetInt(info, "timestamp", hostedUnixTimestamp(video.PublishedAt))
	hostedSetInt(info, "duration", video.Duration.int64())
	hostedSetInt(info, "view_count", video.Views.int64())
	hostedSetInt(info, "like_count", video.Likes.int64())
	hostedSetInt(info, "dislike_count", video.Dislikes.int64())
	setPeerTubeOwner(info, "uploader", video.Account)
	setPeerTubeOwner(info, "channel", video.Channel)
	hostedSetString(info, "language", video.Language.ID.string())
	hostedSetString(info, "license", video.Licence.Label)
	if video.NSFW != nil {
		age := int64(0)
		if *video.NSFW {
			age = 18
		}
		info.Set("age_limit", value.Int(age))
	}
	if video.IsLive {
		info.Set("live_status", value.String("is_live"))
	}
	if category := strings.TrimSpace(video.Category.Label); category != "" {
		info.Set("categories", value.List(value.String(category)))
	}
	if tags := peerTubeStrings(video.Tags); len(tags) != 0 {
		info.Set("tags", value.List(tags...))
	}
	if subtitles := peerTubeSubtitles(target.host, captions); subtitles.Len() != 0 {
		info.Set("subtitles", value.ObjectValue(subtitles))
	}
	return Media(value.NewInfo(info)), nil
}

func setPeerTubeOwner(info *value.Object, prefix string, owner peerTubeOwner) {
	name := strings.TrimSpace(owner.DisplayName)
	if name == "" {
		name = strings.TrimSpace(owner.Name)
	}
	hostedSetString(info, prefix, name)
	hostedSetString(info, prefix+"_id", owner.ID.string())
	if validHostedHTTPURL(owner.URL) {
		hostedSetString(info, prefix+"_url", owner.URL)
	}
}

func peerTubeAssetURL(host, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > sharedHostingMaxURLBytes {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Fragment != "" || parsed.User != nil {
		return ""
	}
	if parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(parsed.Path, "/") && !strings.HasPrefix(raw, "//") {
		base := &url.URL{Scheme: "https", Host: host}
		return base.ResolveReference(parsed).String()
	}
	if validHostedHTTPURL(raw) && parsed.Port() == "" && validPeerTubeHost(strings.ToLower(parsed.Hostname())) {
		return parsed.String()
	}
	return ""
}

func peerTubeSubtitles(host string, captions peerTubeCaptions) *value.Object {
	grouped := make(map[string][]value.Value)
	for _, caption := range captions.Data {
		rawURL := peerTubeAssetURL(host, caption.CaptionPath)
		if rawURL == "" {
			continue
		}
		language := strings.TrimSpace(caption.Language.ID)
		if !validPeerTubeLanguage(language) {
			language = "en"
		}
		grouped[language] = append(grouped[language], value.ObjectValue(value.NewObject(value.Field{Key: "url", Value: value.String(rawURL)})))
	}
	languages := make([]string, 0, len(grouped))
	for language := range grouped {
		languages = append(languages, language)
	}
	sort.Strings(languages)
	result := value.NewObject()
	for _, language := range languages {
		result.Set(language, value.List(grouped[language]...))
	}
	return result
}

func validPeerTubeLanguage(language string) bool {
	if len(language) < 2 || len(language) > 35 {
		return false
	}
	for _, part := range strings.Split(language, "-") {
		if len(part) < 1 || len(part) > 8 {
			return false
		}
		for _, character := range part {
			isLetter := character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z'
			isDigit := character >= '0' && character <= '9'
			if !isLetter && !isDigit {
				return false
			}
		}
	}
	return true
}

func peerTubeStrings(inputs []string) []value.Value {
	seen := make(map[string]bool)
	result := make([]value.Value, 0, len(inputs))
	for _, input := range inputs {
		input = strings.TrimSpace(input)
		if input == "" || len(input) > 1024 || seen[input] {
			continue
		}
		seen[input] = true
		result = append(result, value.String(input))
	}
	return result
}
