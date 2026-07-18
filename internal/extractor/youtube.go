package extractor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

const youtubePlayerMarker = "ytInitialPlayerResponse"

var youtubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

type YouTube struct{}

func NewYouTube() YouTube { return YouTube{} }

func (YouTube) Name() string { return "youtube" }

func (YouTube) Suitable(parsed *url.URL) bool {
	host := strings.ToLower(parsed.Hostname())
	return host == "youtube.com" || strings.HasSuffix(host, ".youtube.com") || host == "youtu.be"
}

func (YouTube) Extract(ctx context.Context, request Request) (Extraction, error) {
	videoID, err := youtubeVideoID(request.URL)
	if err != nil {
		return Extraction{}, err
	}
	webpageURL := "https://www.youtube.com/watch?v=" + videoID
	page, _, err := request.Transport.ReadPage(ctx, webpageURL)
	if err != nil {
		return Extraction{}, err
	}
	rawPlayer, err := extractJSONObject(page, youtubePlayerMarker)
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: player response: %v", ErrInvalidMetadata, err)
	}
	var player youtubePlayerResponse
	if err := json.Unmarshal(rawPlayer, &player); err != nil {
		return Extraction{}, fmt.Errorf("%w: decode player response: %v", ErrInvalidMetadata, err)
	}
	if player.VideoDetails.VideoID != "" && player.VideoDetails.VideoID != videoID {
		return Extraction{}, fmt.Errorf("%w: response video id mismatch", ErrInvalidMetadata)
	}
	if err := checkYouTubeAvailability(player.PlayabilityStatus); err != nil {
		return Extraction{}, err
	}

	formats := append(append([]youtubeFormat(nil), player.StreamingData.Formats...), player.StreamingData.AdaptiveFormats...)
	resolved, err := resolveYouTubeURLs(ctx, request, webpageURL, videoID, player.Assets.JS, formats)
	if err != nil {
		return Extraction{}, err
	}
	formatValues := make([]value.Value, 0, len(resolved)+2)
	for _, format := range resolved {
		if normalized, ok := normalizeYouTubeFormat(format); ok {
			formatValues = append(formatValues, value.ObjectValue(normalized))
		}
	}
	if player.StreamingData.HLSManifestURL != "" {
		formatValues = append(formatValues, value.ObjectValue(manifestFormat("hls", player.StreamingData.HLSManifestURL, "m3u8_native")))
	}
	if player.StreamingData.DASHManifestURL != "" {
		formatValues = append(formatValues, value.ObjectValue(manifestFormat("dash", player.StreamingData.DASHManifestURL, "http_dash_segments")))
	}
	if len(formatValues) == 0 {
		return Extraction{}, fmt.Errorf("%w: no downloadable formats", ErrInvalidMetadata)
	}

	details := player.VideoDetails
	if details.Title == "" {
		return Extraction{}, fmt.Errorf("%w: missing title", ErrInvalidMetadata)
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(videoID)},
		value.Field{Key: "title", Value: value.String(details.Title)},
		value.Field{Key: "description", Value: value.String(details.ShortDescription)},
		value.Field{Key: "uploader", Value: value.String(details.Author)},
		value.Field{Key: "channel_id", Value: value.String(details.ChannelID)},
		value.Field{Key: "webpage_url", Value: value.String(webpageURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(formatValues...)},
	)
	if duration, err := strconv.ParseInt(details.LengthSeconds, 10, 64); err == nil {
		info.Set("duration", value.Int(duration))
	}
	if views, err := strconv.ParseInt(details.ViewCount, 10, 64); err == nil {
		info.Set("view_count", value.Int(views))
	}
	if len(details.Thumbnail.Thumbnails) > 0 {
		thumbnail := details.Thumbnail.Thumbnails[len(details.Thumbnail.Thumbnails)-1]
		info.Set("thumbnail", value.String(thumbnail.URL))
	}
	if details.IsLiveContent {
		info.Set("live_status", value.String("is_live"))
	} else {
		info.Set("live_status", value.String("not_live"))
	}
	return Media(value.NewInfo(info)), nil
}

type youtubePlayerResponse struct {
	PlayabilityStatus youtubePlayabilityStatus `json:"playabilityStatus"`
	VideoDetails      struct {
		VideoID          string `json:"videoId"`
		Title            string `json:"title"`
		LengthSeconds    string `json:"lengthSeconds"`
		Author           string `json:"author"`
		ChannelID        string `json:"channelId"`
		ShortDescription string `json:"shortDescription"`
		ViewCount        string `json:"viewCount"`
		IsLiveContent    bool   `json:"isLiveContent"`
		Thumbnail        struct {
			Thumbnails []struct {
				URL string `json:"url"`
			} `json:"thumbnails"`
		} `json:"thumbnail"`
	} `json:"videoDetails"`
	StreamingData struct {
		Formats         []youtubeFormat `json:"formats"`
		AdaptiveFormats []youtubeFormat `json:"adaptiveFormats"`
		HLSManifestURL  string          `json:"hlsManifestUrl"`
		DASHManifestURL string          `json:"dashManifestUrl"`
	} `json:"streamingData"`
	Assets struct {
		JS string `json:"js"`
	} `json:"assets"`
}

type youtubePlayabilityStatus struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type youtubeFormat struct {
	Itag            int    `json:"itag"`
	URL             string `json:"url"`
	SignatureCipher string `json:"signatureCipher"`
	MimeType        string `json:"mimeType"`
	Bitrate         int64  `json:"bitrate"`
	ContentLength   string `json:"contentLength"`
	Width           int64  `json:"width"`
	Height          int64  `json:"height"`
	FPS             int64  `json:"fps"`
}

type pendingYouTubeFormat struct {
	format youtubeFormat
	rawURL string
	sig    string
	sp     string
	n      string
}

func resolveYouTubeURLs(ctx context.Context, request Request, webpageURL, videoID, playerPath string, formats []youtubeFormat) ([]youtubeFormat, error) {
	pending := make([]pendingYouTubeFormat, 0, len(formats))
	var nChallenges, sigChallenges []string
	for _, format := range formats {
		item := pendingYouTubeFormat{format: format, rawURL: format.URL}
		if item.rawURL == "" && format.SignatureCipher != "" {
			cipher, err := url.ParseQuery(format.SignatureCipher)
			if err != nil || cipher.Get("url") == "" {
				continue
			}
			item.rawURL, item.sig, item.sp = cipher.Get("url"), cipher.Get("s"), cipher.Get("sp")
			if item.sp == "" {
				item.sp = "signature"
			}
		}
		parsed, err := url.Parse(item.rawURL)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			continue
		}
		item.n = parsed.Query().Get("n")
		if item.n != "" {
			nChallenges = appendUnique(nChallenges, item.n)
		}
		if item.sig != "" {
			sigChallenges = appendUnique(sigChallenges, item.sig)
		}
		pending = append(pending, item)
	}
	if len(nChallenges) == 0 && len(sigChallenges) == 0 {
		resolved := make([]youtubeFormat, len(pending))
		for index := range pending {
			pending[index].format.URL = pending[index].rawURL
			resolved[index] = pending[index].format
		}
		return resolved, nil
	}
	if request.ChallengeSolver == nil {
		return nil, ErrChallengeSolver
	}
	if playerPath == "" {
		return nil, fmt.Errorf("%w: missing player JavaScript URL", ErrInvalidMetadata)
	}
	playerURL, err := resolveYouTubePlayerURL(webpageURL, playerPath)
	if err != nil {
		return nil, err
	}
	playerSource, _, err := request.Transport.ReadPage(ctx, playerURL)
	if err != nil {
		return nil, err
	}
	challengeRequests := make([]ejs.ChallengeRequest, 0, 2)
	if len(nChallenges) > 0 {
		challengeRequests = append(challengeRequests, ejs.ChallengeRequest{Type: ejs.ChallengeN, Challenges: nChallenges})
	}
	if len(sigChallenges) > 0 {
		challengeRequests = append(challengeRequests, ejs.ChallengeRequest{Type: ejs.ChallengeSig, Challenges: sigChallenges})
	}
	solved, err := request.ChallengeSolver.SolvePlayer(ctx, "youtube-"+videoID, string(playerSource), challengeRequests, false)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrChallengeSolver, err)
	}
	results := make(map[ejs.ChallengeType]map[string]string, len(solved.Responses))
	for _, response := range solved.Responses {
		if response.Error != "" {
			return nil, fmt.Errorf("%w: %s", ErrChallengeSolver, response.Error)
		}
		results[response.Type] = response.Data
	}
	resolved := make([]youtubeFormat, 0, len(pending))
	for _, item := range pending {
		parsed, _ := url.Parse(item.rawURL)
		query := parsed.Query()
		if item.n != "" {
			answer, ok := results[ejs.ChallengeN][item.n]
			if !ok {
				return nil, fmt.Errorf("%w: missing n result", ErrChallengeSolver)
			}
			query.Set("n", answer)
		}
		if item.sig != "" {
			answer, ok := results[ejs.ChallengeSig][item.sig]
			if !ok {
				return nil, fmt.Errorf("%w: missing signature result", ErrChallengeSolver)
			}
			query.Set(item.sp, answer)
		}
		parsed.RawQuery = query.Encode()
		item.format.URL = parsed.String()
		resolved = append(resolved, item.format)
	}
	return resolved, nil
}

func normalizeYouTubeFormat(format youtubeFormat) (*value.Object, bool) {
	if format.URL == "" {
		return nil, false
	}
	mediaType, parameters, _ := mime.ParseMediaType(format.MimeType)
	extension := youtubeExtension(mediaType, format.URL)
	object := value.NewObject(
		value.Field{Key: "format_id", Value: value.String(strconv.Itoa(format.Itag))},
		value.Field{Key: "url", Value: value.String(format.URL)},
		value.Field{Key: "ext", Value: value.String(extension)},
	)
	codecs := strings.Split(parameters["codecs"], ",")
	for index := range codecs {
		codecs[index] = strings.TrimSpace(codecs[index])
	}
	if strings.HasPrefix(mediaType, "audio/") {
		object.Set("vcodec", value.String("none"))
		if len(codecs) > 0 && codecs[0] != "" {
			object.Set("acodec", value.String(codecs[0]))
		}
	} else if strings.HasPrefix(mediaType, "video/") {
		if len(codecs) > 0 && codecs[0] != "" {
			object.Set("vcodec", value.String(codecs[0]))
		}
		if len(codecs) > 1 {
			object.Set("acodec", value.String(codecs[1]))
		} else {
			object.Set("acodec", value.String("none"))
		}
	}
	if format.Bitrate > 0 {
		object.Set("tbr", value.Float(float64(format.Bitrate)/1000))
	}
	if size, err := strconv.ParseInt(format.ContentLength, 10, 64); err == nil {
		object.Set("filesize", value.Int(size))
	}
	if format.Width > 0 {
		object.Set("width", value.Int(format.Width))
	}
	if format.Height > 0 {
		object.Set("height", value.Int(format.Height))
	}
	if format.FPS > 0 {
		object.Set("fps", value.Int(format.FPS))
	}
	return object, true
}

func manifestFormat(id, rawURL, protocolName string) *value.Object {
	return value.NewObject(
		value.Field{Key: "format_id", Value: value.String(id)},
		value.Field{Key: "url", Value: value.String(rawURL)},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "protocol", Value: value.String(protocolName)},
	)
}

func youtubeVideoID(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid YouTube URL", ErrUnsupported)
	}
	var id string
	if strings.EqualFold(parsed.Hostname(), "youtu.be") {
		id = strings.TrimSpace(strings.Trim(parsed.Path, "/"))
	} else if parsed.Path == "/watch" {
		id = parsed.Query().Get("v")
	} else {
		parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(parts) == 2 && (parts[0] == "shorts" || parts[0] == "embed" || parts[0] == "live") {
			id = parts[1]
		}
	}
	if !youtubeIDPattern.MatchString(id) {
		return "", fmt.Errorf("%w: invalid YouTube video id", ErrUnsupported)
	}
	return id, nil
}

func resolveYouTubePlayerURL(webpageURL, playerPath string) (string, error) {
	base, err := url.Parse(webpageURL)
	if err != nil {
		return "", fmt.Errorf("%w: invalid webpage URL", ErrInvalidMetadata)
	}
	reference, err := url.Parse(playerPath)
	if err != nil {
		return "", fmt.Errorf("%w: invalid player URL", ErrInvalidMetadata)
	}
	return base.ResolveReference(reference).String(), nil
}

func checkYouTubeAvailability(status youtubePlayabilityStatus) error {
	switch status.Status {
	case "OK":
		return nil
	case "LOGIN_REQUIRED":
		return fmt.Errorf("%w: %s", ErrAuthentication, status.Reason)
	default:
		return fmt.Errorf("%w: %s", ErrUnavailable, status.Reason)
	}
}

func extractJSONObject(page []byte, marker string) ([]byte, error) {
	pageText := string(page)
	markerIndex := strings.Index(pageText, marker)
	if markerIndex < 0 {
		return nil, fmt.Errorf("marker %q not found", marker)
	}
	startOffset := strings.IndexByte(pageText[markerIndex+len(marker):], '{')
	if startOffset < 0 {
		return nil, errors.New("JSON object start not found")
	}
	start := markerIndex + len(marker) + startOffset
	depth := 0
	inString, escaped := false, false
	for index := start; index < len(page); index++ {
		character := page[index]
		if inString {
			if escaped {
				escaped = false
			} else if character == '\\' {
				escaped = true
			} else if character == '"' {
				inString = false
			}
			continue
		}
		switch character {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return page[start : index+1], nil
			}
		}
	}
	return nil, errors.New("JSON object is not closed")
}

func appendUnique(items []string, item string) []string {
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	return append(items, item)
}

func youtubeExtension(mediaType, rawURL string) string {
	switch mediaType {
	case "audio/mp4":
		return "m4a"
	case "video/mp4":
		return "mp4"
	case "audio/webm", "video/webm":
		return "webm"
	}
	parsed, _ := url.Parse(rawURL)
	if extension := strings.TrimPrefix(path.Ext(parsed.Path), "."); extension != "" {
		return extension
	}
	return "bin"
}
