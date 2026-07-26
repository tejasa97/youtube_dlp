package dash

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"strings"
)

const maxTextRepresentations = 128

// TextRepresentation is one subtitle/caption representation from a DASH MPD.
type TextRepresentation struct {
	URL      string
	Language string
	MimeType string
	Name     string
}

type textMPDXML struct {
	BaseURL string          `xml:"BaseURL"`
	Periods []textPeriodXML `xml:"Period"`
}

type textPeriodXML struct {
	BaseURL        string                 `xml:"BaseURL"`
	AdaptationSets []textAdaptationSetXML `xml:"AdaptationSet"`
}

type textAdaptationSetXML struct {
	ContentType     string                  `xml:"contentType,attr"`
	MimeType        string                  `xml:"mimeType,attr"`
	Language        string                  `xml:"lang,attr"`
	Label           string                  `xml:"label,attr"`
	BaseURL         string                  `xml:"BaseURL"`
	Representations []textRepresentationXML `xml:"Representation"`
}

type textRepresentationXML struct {
	ID       string `xml:"id,attr"`
	MimeType string `xml:"mimeType,attr"`
	Language string `xml:"lang,attr"`
	BaseURL  string `xml:"BaseURL"`
}

// ParseTextRepresentations extracts bounded text/subtitle representations from
// a DASH MPD without building media download plans.
func ParseTextRepresentations(rawURL string, input []byte) ([]TextRepresentation, error) {
	if len(input) > maxPlaylistBytes {
		return nil, fmt.Errorf("%w: MPD exceeds %d bytes", ErrInvalidMPD, maxPlaylistBytes)
	}
	base, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("%w: base URL: %v", ErrInvalidMPD, err)
	}
	var document textMPDXML
	if err := xml.Unmarshal(input, &document); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMPD, err)
	}
	mpdBase, err := resolveTextBase(base, document.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMPD, err)
	}
	representations := make([]TextRepresentation, 0, 4)
	for _, period := range document.Periods {
		periodBase, err := resolveTextBase(mpdBase, period.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidMPD, err)
		}
		for _, adaptation := range period.AdaptationSets {
			if !textAdaptationSet(adaptation) {
				continue
			}
			adaptationBase, err := resolveTextBase(periodBase, adaptation.BaseURL)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidMPD, err)
			}
			language := strings.TrimSpace(firstNonEmpty(adaptation.Language))
			name := strings.TrimSpace(adaptation.Label)
			mimeType := strings.TrimSpace(firstNonEmpty(adaptation.MimeType))
			if len(adaptation.Representations) == 0 {
				if adaptation.BaseURL == "" {
					continue
				}
				if len(representations) >= maxTextRepresentations {
					return nil, fmt.Errorf("%w: text representation count exceeds %d", ErrInvalidMPD, maxTextRepresentations)
				}
				representations = append(representations, TextRepresentation{
					URL: adaptationBase.String(), Language: language, MimeType: mimeType, Name: name,
				})
				continue
			}
			for _, representation := range adaptation.Representations {
				raw := strings.TrimSpace(firstNonEmpty(representation.BaseURL))
				if raw == "" {
					continue
				}
				representationBase, err := resolveTextBase(adaptationBase, raw)
				if err != nil {
					return nil, fmt.Errorf("%w: %v", ErrInvalidMPD, err)
				}
				if len(representations) >= maxTextRepresentations {
					return nil, fmt.Errorf("%w: text representation count exceeds %d", ErrInvalidMPD, maxTextRepresentations)
				}
				representations = append(representations, TextRepresentation{
					URL:      representationBase.String(),
					Language: strings.TrimSpace(firstNonEmpty(representation.Language, language)),
					MimeType: strings.TrimSpace(firstNonEmpty(representation.MimeType, mimeType)),
					Name:     name,
				})
			}
		}
	}
	return representations, nil
}

func textAdaptationSet(adaptation textAdaptationSetXML) bool {
	contentType := strings.ToLower(strings.TrimSpace(adaptation.ContentType))
	mimeType := strings.ToLower(strings.TrimSpace(adaptation.MimeType))
	return contentType == "text" || strings.Contains(mimeType, "vtt") || strings.Contains(mimeType, "ttml")
}

func resolveTextBase(base *url.URL, raw string) (*url.URL, error) {
	if raw == "" {
		copy := *base
		return &copy, nil
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	return base.ResolveReference(reference), nil
}

const maxPlaylistBytes = 16 << 20
