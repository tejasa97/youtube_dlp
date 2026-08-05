package extractor

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/tejasa97/youtube_dlp/internal/value"
)

type soundCloudArtworkVariant struct {
	id   string
	size int64
}

var (
	soundCloudArtworkSuffix   = regexp.MustCompile(`-[0-9a-z]+\.(jpg|png)$`)
	soundCloudArtworkVariants = []soundCloudArtworkVariant{
		{id: "mini", size: 16},
		{id: "tiny", size: 20},
		{id: "small", size: 32},
		{id: "badge", size: 47},
		{id: "t67x67", size: 67},
		{id: "large", size: 100},
		{id: "t300x300", size: 300},
		{id: "crop", size: 400},
		{id: "t500x500", size: 500},
		{id: "original"},
	}
)

func addSoundCloudThumbnails(
	ctx context.Context,
	transport Transport,
	info *value.Object,
	id, artworkURL, avatarURL string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	rawURL := artworkURL
	artwork := validSoundCloudThumbnailSource(rawURL)
	if !artwork {
		rawURL = avatarURL
	}
	if !validSoundCloudThumbnailSource(rawURL) {
		return nil
	}
	thumbnails, err := soundCloudThumbnails(ctx, transport, id, rawURL, artwork)
	if err != nil {
		return err
	}
	if len(thumbnails) != 0 {
		preferred, _ := thumbnails[len(thumbnails)-1].Object()
		preferredURL, _ := preferred.Lookup("url").StringValue()
		info.Set("thumbnail", value.String(preferredURL))
		info.Set("thumbnails", value.List(thumbnails...))
	}
	return nil
}

func soundCloudThumbnails(
	ctx context.Context,
	transport Transport,
	_ string,
	rawURL string,
	artwork bool,
) ([]value.Value, error) {
	parsed, extension, expandable := parseSoundCloudArtworkURL(rawURL)
	if !expandable {
		return []value.Value{value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(rawURL)},
		))}, nil
	}
	thumbnails := make([]value.Value, 0, len(soundCloudArtworkVariants))
	for _, variant := range soundCloudArtworkVariants {
		variantExtension := "jpg"
		if variant.id == "original" {
			variantExtension = extension
		}
		variantURL := soundCloudArtworkVariantURL(parsed, variant.id, variantExtension)
		if variant.id == "original" {
			exists, err := soundCloudThumbnailExists(ctx, transport, variantURL)
			if err != nil {
				return nil, err
			}
			if !exists {
				if variantExtension == "png" {
					variantExtension = "jpg"
				} else {
					variantExtension = "png"
				}
				variantURL = soundCloudArtworkVariantURL(parsed, variant.id, variantExtension)
			}
		}
		thumbnail := value.NewObject(
			value.Field{Key: "id", Value: value.String(variant.id)},
			value.Field{Key: "url", Value: value.String(variantURL)},
		)
		size := variant.size
		if variant.id == "tiny" && !artwork {
			size = 18
		}
		if size > 0 {
			thumbnail.Set("width", value.Int(size))
			thumbnail.Set("height", value.Int(size))
		}
		if variant.id == "original" {
			thumbnail.Set("preference", value.Int(10))
		}
		thumbnails = append(thumbnails, value.ObjectValue(thumbnail))
	}
	return thumbnails, nil
}

func parseSoundCloudArtworkURL(rawURL string) (*url.URL, string, bool) {
	if !validSoundCloudThumbnailSource(rawURL) {
		return nil, "", false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, "", false
	}
	match := soundCloudArtworkSuffix.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return nil, "", false
	}
	for _, variant := range soundCloudArtworkVariants {
		if len(soundCloudArtworkVariantURL(parsed, variant.id, "jpg")) > soundCloudMaxURLBytes {
			return nil, "", false
		}
	}
	return parsed, match[1], true
}

func validSoundCloudThumbnailSource(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > soundCloudMaxURLBytes || strings.ContainsRune(rawURL, 0) {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Fragment != "" || soundCloudEncodedSeparators(parsed) {
		return false
	}
	switch strings.ToLower(parsed.Hostname()) {
	case "i1.sndcdn.com", "a1.sndcdn.com":
		return true
	default:
		return false
	}
}

func soundCloudArtworkVariantURL(parsed *url.URL, id, extension string) string {
	copyURL := *parsed
	copyURL.Path = soundCloudArtworkSuffix.ReplaceAllString(copyURL.Path, "-"+id+"."+extension)
	copyURL.RawPath = ""
	return copyURL.String()
}

func soundCloudThumbnailExists(
	ctx context.Context,
	transport Transport,
	rawURL string,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return false, nil
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		// Keep the extension supplied by metadata when the optional probe cannot
		// be performed without ambient credentials or redirects.
		return true, nil
	}
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		return false, ctxErr
	}
	if response == nil {
		return false, nil
	}
	if response.Body != nil {
		defer response.Body.Close()
	}
	return response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices, nil
}
