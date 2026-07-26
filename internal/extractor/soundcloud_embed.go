package extractor

import (
	"context"
	"net/url"
	"strings"
)

const soundCloudEmbedMaxQueryParams = 16

// SoundCloudEmbed resolves SoundCloud's public player wrapper without
// networking. The resulting URL is re-entered through the native SoundCloud
// extractor.
type SoundCloudEmbed struct{}

func NewSoundCloudEmbed() SoundCloudEmbed { return SoundCloudEmbed{} }
func (SoundCloudEmbed) Name() string      { return "soundcloud_embed" }

func (SoundCloudEmbed) Suitable(parsed *url.URL) bool {
	_, _, ok := parseSoundCloudEmbedURL(parsed)
	return ok
}

func (SoundCloudEmbed) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	canonical, target, ok := parseSoundCloudEmbedURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{
		URL:          canonical,
		ExtractorKey: "soundcloud",
		ID:           target.id,
		Transparent:  true,
	})
}

func parseSoundCloudEmbedURL(parsed *url.URL) (string, soundCloudTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > soundCloudMaxURLBytes ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.Port() != "" || parsed.Fragment != "" || parsed.RawFragment != "" ||
		soundCloudEncodedSeparators(parsed) {
		return "", soundCloudTarget{}, false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "soundcloud.com", "w.soundcloud.com", "player.soundcloud.com", "p.soundcloud.com":
	default:
		return "", soundCloudTarget{}, false
	}
	if parsed.EscapedPath() != "/player" && parsed.EscapedPath() != "/player/" {
		return "", soundCloudTarget{}, false
	}
	query, err := url.ParseQuery(parsed.RawQuery)
	if err != nil || len(query) == 0 || len(query) > soundCloudEmbedMaxQueryParams {
		return "", soundCloudTarget{}, false
	}
	queryParams := 0
	for key, values := range query {
		queryParams += len(values)
		if len(key) == 0 || len(key) > soundCloudMaxQueryValue || len(values) != 1 ||
			len(values[0]) > soundCloudMaxURLBytes {
			return "", soundCloudTarget{}, false
		}
	}
	if queryParams > soundCloudEmbedMaxQueryParams {
		return "", soundCloudTarget{}, false
	}
	innerValues, ok := query["url"]
	if !ok || len(innerValues) != 1 || strings.TrimSpace(innerValues[0]) == "" {
		return "", soundCloudTarget{}, false
	}
	token := ""
	if values, present := query["secret_token"]; present {
		if len(values) != 1 || !soundCloudTokenPattern.MatchString(values[0]) || len(values[0]) > 256 {
			return "", soundCloudTarget{}, false
		}
		token = values[0]
	}
	inner, err := url.Parse(innerValues[0])
	if err != nil || inner.Fragment != "" || inner.RawFragment != "" || inner.User != nil || inner.Port() != "" ||
		len(inner.String()) > soundCloudMaxURLBytes {
		return "", soundCloudTarget{}, false
	}
	innerQuery, err := url.ParseQuery(inner.RawQuery)
	if err != nil || len(innerQuery) > soundCloudMaxQueryParams {
		return "", soundCloudTarget{}, false
	}
	innerParams := 0
	for key, values := range innerQuery {
		innerParams += len(values)
		if len(key) > soundCloudMaxQueryValue || len(values) > 1 {
			return "", soundCloudTarget{}, false
		}
		for _, value := range values {
			if len(value) > soundCloudMaxQueryValue {
				return "", soundCloudTarget{}, false
			}
		}
	}
	if innerParams > soundCloudMaxQueryParams {
		return "", soundCloudTarget{}, false
	}
	target, ok := classifySoundCloudURL(inner)
	if !ok {
		return "", soundCloudTarget{}, false
	}
	if token != "" {
		target.secretToken = token
	}
	canonical, ok := canonicalSoundCloudEmbedTarget(target)
	if !ok {
		return "", soundCloudTarget{}, false
	}
	roundTrip, err := url.Parse(canonical)
	if err != nil {
		return "", soundCloudTarget{}, false
	}
	verified, ok := classifySoundCloudURL(roundTrip)
	if !ok || verified.kind != target.kind || verified.id != target.id ||
		verified.secretToken != target.secretToken || verified.relation != target.relation {
		return "", soundCloudTarget{}, false
	}
	return canonical, target, true
}

func canonicalSoundCloudEmbedTarget(target soundCloudTarget) (string, bool) {
	canonical := target.canonical
	switch target.kind {
	case soundCloudTrackTarget:
		if canonical == "" && target.id != "" {
			canonical = "https://api.soundcloud.com/tracks/" + target.id
		}
	case soundCloudAPIPlaylistTarget:
		if target.id != "" {
			canonical = "https://api.soundcloud.com/playlists/" + target.id
		}
	case soundCloudAPIUserTarget, soundCloudSetTarget, soundCloudUserTabTarget,
		soundCloudStationTarget, soundCloudRelatedTarget:
	default:
		return "", false
	}
	if canonical == "" {
		return "", false
	}
	parsed, err := url.Parse(canonical)
	if err != nil {
		return "", false
	}
	if target.secretToken != "" {
		segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
		if len(segments) > 0 && soundCloudTokenPattern.MatchString(segments[len(segments)-1]) &&
			(target.kind == soundCloudTrackTarget || target.kind == soundCloudSetTarget) {
			segments = segments[:len(segments)-1]
			parsed.Path = "/" + strings.Join(segments, "/")
			parsed.RawPath = ""
		}
		query := parsed.Query()
		query.Set("secret_token", target.secretToken)
		parsed.RawQuery = query.Encode()
	}
	if len(parsed.String()) > soundCloudMaxURLBytes {
		return "", false
	}
	return parsed.String(), true
}
