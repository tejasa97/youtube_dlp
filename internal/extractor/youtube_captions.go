package extractor

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
	"github.com/ytdlp-go/ytdlp/internal/youtubepot"
)

const (
	youtubeMaxCaptionPlayers          = 8
	youtubeMaxCaptionTracks           = 128
	youtubeMaxTranslationLanguages    = 256
	youtubeMaxCaptionRuns             = 16
	youtubeMaxCaptionTextBytes        = 512
	youtubeMaxCaptionBaseURLBytes     = 8192
	youtubeMaxCaptionQueryFields      = 64
	youtubeMaxCaptionQueryValues      = 128
	youtubeMaxCaptionQueryBytes       = 2048
	youtubeMaxCaptionOutputBytes      = youtubeMaxCaptionBaseURLBytes + youtubepot.MaxTokenBytes + 1024
	youtubeMaxCaptionOutputTotalBytes = 16 << 20
)

var youtubeCaptionLanguagePattern = regexp.MustCompile(`^[A-Za-z0-9]{1,16}(?:-[A-Za-z0-9]{1,16}){0,4}$`)

var youtubeSubtitleFormats = [...]string{"json3", "srv1", "srv2", "srv3", "ttml", "srt", "vtt"}

type youtubeCaptionTracklist struct {
	CaptionTracks        []youtubeCaptionTrack    `json:"captionTracks"`
	TranslationLanguages []youtubeCaptionLanguage `json:"translationLanguages"`
}

type youtubeCaptionTrack struct {
	BaseURL        string      `json:"baseUrl"`
	VssID          string      `json:"vssId"`
	LanguageCode   string      `json:"languageCode"`
	Name           youtubeText `json:"name"`
	Kind           string      `json:"kind"`
	IsTranslatable bool        `json:"isTranslatable"`
}

type youtubeCaptionLanguage struct {
	LanguageCode string      `json:"languageCode"`
	LanguageName youtubeText `json:"languageName"`
}

type youtubeText struct {
	SimpleText string `json:"simpleText"`
	Runs       []struct {
		Text string `json:"text"`
	} `json:"runs"`
}

type youtubeCaptionCandidate struct {
	base          *url.URL
	language      string
	name          string
	automatic     bool
	translatable  bool
	clientName    string
	visitorData   string
	playerURL     string
	subsPolicy    youtubePOTPolicy
	playerToken   bool
	requiresToken bool
}

type youtubeCaptionTokenState struct {
	token string
	ok    bool
	skip  bool
}

type youtubeCaptionResult struct {
	subtitles         *value.Object
	automaticCaptions *value.Object
	audioLanguage     string
}

func normalizeYouTubeCaptions(ctx context.Context, players []youtubePlayerResponse, videoID string, tokens *youtubepot.Director, translatedManual bool) (youtubeCaptionResult, error) {
	result := youtubeCaptionResult{subtitles: value.NewObject(), automaticCaptions: value.NewObject()}
	if ctx == nil || len(players) > youtubeMaxCaptionPlayers {
		return result, fmt.Errorf("%w: YouTube caption player limit", ErrInvalidMetadata)
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	translations, candidates, err := collectYouTubeCaptionCandidates(players)
	if err != nil {
		return result, err
	}
	states, err := resolveYouTubeCaptionTokens(ctx, candidates, videoID, tokens)
	if err != nil {
		return result, err
	}
	seen := make(map[string]bool)
	outputBytes := 0
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		state := states[candidate.clientName]
		if state.skip {
			continue
		}
		if candidate.automatic {
			language := strings.TrimPrefix(candidate.language, "a-")
			if !validYouTubeCaptionLanguage(language) {
				continue
			}
			name := strings.TrimSuffix(candidate.name, " (auto-generated)")
			if err := addYouTubeCaptionFormats(result.automaticCaptions, seen, language, name, candidate, state, "", &outputBytes); err != nil {
				return result, err
			}
			if !candidate.translatable {
				continue
			}
			if result.audioLanguage == "" {
				result.audioLanguage = language
			}
			if err := addYouTubeCaptionFormats(result.automaticCaptions, seen, language+"-orig", name+" (Original)", candidate, state, "", &outputBytes); err != nil {
				return result, err
			}
			for _, translation := range translations {
				if translation.code == language {
					continue
				}
				if err := addYouTubeCaptionFormats(result.automaticCaptions, seen, translation.code, translation.name, candidate, state, translation.code, &outputBytes); err != nil {
					return result, err
				}
			}
			continue
		}

		if err := addYouTubeCaptionFormats(result.subtitles, seen, candidate.language, candidate.name, candidate, state, "", &outputBytes); err != nil {
			return result, err
		}
		if !translatedManual || !candidate.translatable {
			continue
		}
		for _, translation := range translations {
			if translation.code == candidate.language {
				continue
			}
			language := translation.code
			if language != "und" {
				language += "-" + candidate.language
			}
			if err := addYouTubeCaptionFormats(result.automaticCaptions, seen, language, translation.name+" from "+candidate.name, candidate, state, translation.code, &outputBytes); err != nil {
				return result, err
			}
		}
	}
	return result, nil
}

type youtubeTranslation struct{ code, name string }

func collectYouTubeCaptionCandidates(players []youtubePlayerResponse) ([]youtubeTranslation, []youtubeCaptionCandidate, error) {
	translations := make([]youtubeTranslation, 0)
	translationSeen := make(map[string]bool)
	candidates := make([]youtubeCaptionCandidate, 0)
	for _, player := range players {
		tracklist := player.Captions.Tracklist
		if len(tracklist.CaptionTracks) > youtubeMaxCaptionTracks || len(tracklist.TranslationLanguages) > youtubeMaxTranslationLanguages ||
			len(candidates)+len(tracklist.CaptionTracks) > youtubeMaxCaptionTracks {
			return nil, nil, fmt.Errorf("%w: YouTube caption resource limit", ErrInvalidMetadata)
		}
		for _, language := range tracklist.TranslationLanguages {
			code := normalizeYouTubeCaptionLanguage(language.LanguageCode)
			name, err := boundedYouTubeText(language.LanguageName)
			if err != nil {
				return nil, nil, err
			}
			if !validYouTubeCaptionLanguage(code) || name == "" || translationSeen[code] {
				continue
			}
			translationSeen[code] = true
			translations = append(translations, youtubeTranslation{code: code, name: name})
			if len(translations) > youtubeMaxTranslationLanguages {
				return nil, nil, fmt.Errorf("%w: YouTube translation language limit", ErrInvalidMetadata)
			}
		}
		clientName := player.clientName
		if clientName == "" {
			clientName = "WEB"
		}
		for _, track := range tracklist.CaptionTracks {
			base, err := parseYouTubeCaptionURL(track.BaseURL)
			if err != nil {
				continue
			}
			language := normalizeYouTubeCaptionLanguage(strings.TrimPrefix(track.VssID, "."))
			if language == "" {
				language = normalizeYouTubeCaptionLanguage(track.LanguageCode)
			}
			name, textErr := boundedYouTubeText(track.Name)
			if textErr != nil {
				return nil, nil, textErr
			}
			if !validYouTubeCaptionLanguage(language) || name == "" {
				continue
			}
			candidate := youtubeCaptionCandidate{
				base: base, language: language, name: name, automatic: track.Kind == "asr",
				translatable: track.IsTranslatable, clientName: clientName,
				visitorData: player.visitorData, playerURL: player.playerURL,
				subsPolicy: player.subsPolicy, playerToken: player.playerTokenProvided,
			}
			candidate.requiresToken = youtubeCaptionURLRequiresToken(base) || candidate.subsPolicy.required(candidate.playerToken)
			candidates = append(candidates, candidate)
		}
	}
	return translations, candidates, nil
}

func resolveYouTubeCaptionTokens(ctx context.Context, candidates []youtubeCaptionCandidate, videoID string, tokens *youtubepot.Director) (map[string]youtubeCaptionTokenState, error) {
	type clientRequest struct {
		candidate   youtubeCaptionCandidate
		required    bool
		recommended bool
	}
	requests := make(map[string]clientRequest)
	order := make([]string, 0)
	for _, candidate := range candidates {
		request, exists := requests[candidate.clientName]
		if !exists {
			request.candidate = candidate
			order = append(order, candidate.clientName)
		}
		request.required = request.required || candidate.requiresToken
		request.recommended = request.recommended || candidate.subsPolicy.Recommended
		requests[candidate.clientName] = request
	}
	states := make(map[string]youtubeCaptionTokenState, len(requests))
	for _, clientName := range order {
		request := requests[clientName]
		if tokens == nil {
			states[clientName] = youtubeCaptionTokenState{skip: request.required}
			continue
		}
		token, ok, err := tokens.ResolvePolicy(ctx, youtubepot.Request{
			Context: youtubepot.ContextSubs, Client: clientName,
			VisitorData: request.candidate.visitorData, VideoID: videoID,
			PlayerURL: request.candidate.playerURL,
		}, request.required, request.recommended)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			states[clientName] = youtubeCaptionTokenState{skip: request.required}
			continue
		}
		states[clientName] = youtubeCaptionTokenState{token: token, ok: ok, skip: request.required && !ok}
	}
	return states, nil
}

func addYouTubeCaptionFormats(container *value.Object, seen map[string]bool, language, name string, candidate youtubeCaptionCandidate, token youtubeCaptionTokenState, translatedLanguage string, outputBytes *int) error {
	if !validYouTubeCaptionLanguage(language) {
		return nil
	}
	for _, extension := range youtubeSubtitleFormats {
		captionURL := *candidate.base
		query := captionURL.Query()
		query.Set("fmt", extension)
		query.Del("xosf")
		if translatedLanguage != "" {
			query.Set("tlang", translatedLanguage)
		} else {
			query.Del("tlang")
		}
		if token.ok {
			query.Set("pot", token.token)
			query.Set("potc", "1")
			query.Set("c", candidate.clientName)
		}
		captionURL.RawQuery = query.Encode()
		rawURL := captionURL.String()
		if len(rawURL) > youtubeMaxCaptionOutputBytes {
			continue
		}
		key := language + "\x00" + rawURL
		if seen[key] {
			continue
		}
		addedBytes := len(rawURL) + len(language) + len(name) + len(extension)
		if outputBytes == nil || *outputBytes > youtubeMaxCaptionOutputTotalBytes-addedBytes {
			return fmt.Errorf("%w: YouTube caption output limit", ErrInvalidMetadata)
		}
		*outputBytes += addedBytes
		seen[key] = true
		entry := value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(rawURL)},
			value.Field{Key: "ext", Value: value.String(extension)},
			value.Field{Key: "name", Value: value.String(name)},
		))
		entries, _ := container.Lookup(language).ListValue()
		entries = append(entries, entry)
		container.Set(language, value.List(entries...))
	}
	return nil
}

func parseYouTubeCaptionURL(rawURL string) (*url.URL, error) {
	if rawURL == "" || len(rawURL) > youtubeMaxCaptionBaseURLBytes {
		return nil, fmt.Errorf("%w: invalid YouTube caption URL", ErrInvalidMetadata)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid YouTube caption URL", ErrInvalidMetadata)
	}
	if !parsed.IsAbs() {
		base, _ := url.Parse("https://www.youtube.com")
		parsed = base.ResolveReference(parsed)
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	allowedHost := host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")
	if parsed.Scheme != "https" || !allowedHost || parsed.Port() != "" || parsed.User != nil || parsed.Fragment != "" ||
		parsed.RawPath != "" || parsed.Path != "/api/timedtext" || path.Clean(parsed.Path) != parsed.Path {
		return nil, fmt.Errorf("%w: untrusted YouTube caption URL", ErrInvalidMetadata)
	}
	query := parsed.Query()
	if len(query) > youtubeMaxCaptionQueryFields {
		return nil, fmt.Errorf("%w: YouTube caption query limit", ErrInvalidMetadata)
	}
	values := 0
	for key, items := range query {
		values += len(items)
		if key == "" || len(key) > youtubeMaxCaptionQueryBytes {
			return nil, fmt.Errorf("%w: YouTube caption query limit", ErrInvalidMetadata)
		}
		for _, item := range items {
			if len(item) > youtubeMaxCaptionQueryBytes {
				return nil, fmt.Errorf("%w: YouTube caption query limit", ErrInvalidMetadata)
			}
		}
	}
	if values > youtubeMaxCaptionQueryValues {
		return nil, fmt.Errorf("%w: YouTube caption query limit", ErrInvalidMetadata)
	}
	return parsed, nil
}

func youtubeCaptionURLRequiresToken(parsed *url.URL) bool {
	for _, experiment := range parsed.Query()["exp"] {
		if experiment == "xpe" || experiment == "xpv" {
			return true
		}
	}
	return false
}

func boundedYouTubeText(text youtubeText) (string, error) {
	if len(text.Runs) > youtubeMaxCaptionRuns {
		return "", fmt.Errorf("%w: YouTube caption text limit", ErrInvalidMetadata)
	}
	result := text.SimpleText
	if result == "" && len(text.Runs) > 0 {
		result = text.Runs[0].Text
	}
	result = strings.TrimSpace(result)
	if len(result) > youtubeMaxCaptionTextBytes {
		return "", fmt.Errorf("%w: YouTube caption text limit", ErrInvalidMetadata)
	}
	if strings.ContainsAny(result, "\x00\r\n") {
		return "", nil
	}
	return result, nil
}

func normalizeYouTubeCaptionLanguage(language string) string {
	return strings.ReplaceAll(strings.TrimSpace(language), ".", "-")
}

func validYouTubeCaptionLanguage(language string) bool {
	return len(language) <= 64 && youtubeCaptionLanguagePattern.MatchString(language)
}

func applyYouTubeAudioLanguage(formats []youtubeFormat, language string) {
	if language == "" {
		return
	}
	for index := range formats {
		if formats[index].Language != "" {
			continue
		}
		mediaType, parameters, err := mime.ParseMediaType(formats[index].MimeType)
		if err != nil {
			continue
		}
		codecs := strings.Split(parameters["codecs"], ",")
		if strings.HasPrefix(mediaType, "audio/") || strings.HasPrefix(mediaType, "video/") && len(codecs) > 1 {
			formats[index].Language = language
		}
	}
}
