package extractor

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	aeonCoMaxURLBytes  = 4096
	aeonCoMaxSlugBytes = 256
	aeonCoReferer      = "https://aeon.co/"
)

var aeonCoSlugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// AeonCo extracts the first supported JSON-LD VideoObject embed from a bounded
// aeon.co video page and transparently hands it to Vimeo or YouTube.
type AeonCo struct{}

func NewAeonCo() AeonCo     { return AeonCo{} }
func (AeonCo) Name() string { return "aeonco" }

func (AeonCo) Suitable(parsed *url.URL) bool {
	_, ok := classifyAeonCoURL(parsed)
	return ok
}

func (AeonCo) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	target, ok := classifyAeonCoURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	page, _, err := request.Transport.ReadPage(ctx, target.webpageURL)
	if err != nil {
		if contextErr := contextError(ctx); contextErr != nil {
			return Extraction{}, contextErr
		}
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if len(page) > maxGenericHTMLBytes {
		return Extraction{}, fmt.Errorf("%w: Aeon page exceeds %d bytes", ErrInvalidMetadata, maxGenericHTMLBytes)
	}
	document, err := parseGenericMetadataDocument(ctx, page)
	if err != nil {
		return Extraction{}, err
	}
	entry, err := aeonCoFirstEmbedEntry(ctx, parsed, document.jsonLD)
	if err != nil {
		return Extraction{}, err
	}
	return URLResult(entry)
}

type aeonCoTarget struct {
	slug       string
	webpageURL string
}

func classifyAeonCoURL(parsed *url.URL) (aeonCoTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > aeonCoMaxURLBytes ||
		parsed.Scheme != "https" || parsed.RawQuery != "" || parsed.ForceQuery || strictURLPolicyRejects(parsed) {
		return aeonCoTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "aeon.co" && host != "www.aeon.co" {
		return aeonCoTarget{}, false
	}
	const prefix = "/videos/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return aeonCoTarget{}, false
	}
	slug := strings.TrimPrefix(parsed.Path, prefix)
	if len(slug) == 0 || len(slug) > aeonCoMaxSlugBytes || strings.Contains(slug, "/") || !aeonCoSlugPattern.MatchString(slug) {
		return aeonCoTarget{}, false
	}
	return aeonCoTarget{slug: slug, webpageURL: "https://aeon.co/videos/" + slug}, true
}

func aeonCoFirstEmbedEntry(ctx context.Context, pageURL *url.URL, candidates []genericMetadataCandidate) (Entry, error) {
	for index, candidate := range candidates {
		if index%64 == 0 {
			if err := contextError(ctx); err != nil {
				return Entry{}, err
			}
		}
		// The shared parser marks AudioObject-only candidates as audio. Upstream
		// Aeon selects the first VideoObject embedUrl even when contentUrl is also
		// present; generic contentUrl precedence does not apply here.
		if candidate.kind == "audio" || candidate.embedURL == "" {
			continue
		}
		entry, ok := canonicalGenericEmbed(pageURL, candidate.embedURL)
		if !ok {
			continue
		}
		switch entry.ExtractorKey {
		case "vimeo":
			entry.Referer = aeonCoReferer
		case "youtube":
		default:
			continue
		}
		entry.Transparent = true
		return entry, nil
	}
	return Entry{}, fmt.Errorf("%w: no supported Aeon embed URL", ErrUnavailable)
}
