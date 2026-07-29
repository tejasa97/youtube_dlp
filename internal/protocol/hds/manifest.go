package hds

import (
	"bytes"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// maxManifestBytes caps the F4M XML payload. A reasonable bound for the pinned
// Rai/Akamai corpus; anything larger indicates a malformed or hostile source.
const maxManifestBytes = 4 << 20

// maxMediaElements caps the per-manifest <media> entries we will retain after
// filtering. The pinned corpus never advertises more than a handful.
const maxMediaElements = 32

// maxDecodedBootstrapBytes is a stricter cap applied to the decoded base64
// bootstrap payload so a 4 MiB manifest cannot trigger a 4 MiB allocation.
const maxDecodedBootstrapBytes = 2 << 20

// maxDecodedMetadataBytes bounds the optional per-media FLV metadata tag.
const maxDecodedMetadataBytes = 1 << 20

// F4M namespace URIs (v1.0 and v2.0 of the Adobe spec).
const (
	f4mNamespaceV1 = "http://ns.adobe.com/f4m/1.0"
	f4mNamespaceV2 = "http://ns.adobe.com/f4m/2.0"
)

var f4mNamespaces = map[string]struct{}{
	f4mNamespaceV1: {},
	f4mNamespaceV2: {},
}

// Manifest is the decoded, DRM-filtered subset of an F4M XML document.
type Manifest struct {
	BaseURL     string
	PV2Query    string
	Media       []Media
	Bootstrap   BootstrapSource
	RawDocument string
}

// Media is one decoded <media> child of the manifest root.
type Media struct {
	URL      string
	Bitrate  int64
	Width    int
	Height   int
	Metadata []byte
}

// BootstrapSource resolves to either an external HTTP bootstrap URL or an
// inline base64 bootstrap payload. Only one is populated after parsing.
type BootstrapSource struct {
	URL    string // "" when only inline bootstrap is present.
	Inline []byte // Decoded bytes; nil when only an external URL is present.
}

// xmlManifest mirrors the subset of the F4M schema we recognize.
type xmlManifest struct {
	XMLName       xml.Name     `xml:"manifest"`
	BaseURL       string       `xml:"baseURL"`
	PV2           string       `xml:"pv-2.0"`
	Media         []xmlMedia   `xml:"media"`
	Bootstrap     xmlBootstrap `xml:"bootstrapInfo"`
	DRMHeaders    []xmlDRM     `xml:"drmAdditionalHeader"`
	DRMHeaderSets []xmlDRM     `xml:"drmAdditionalHeaderSet"`
}

type xmlMedia struct {
	URL            string `xml:"url,attr"`
	Bitrate        string `xml:"bitrate,attr"`
	Width          string `xml:"width,attr"`
	Height         string `xml:"height,attr"`
	DRMHeaderID    string `xml:"drmAdditionalHeaderId,attr,omitempty"`
	DRMHeaderSetID string `xml:"drmAdditionalHeaderSetId,attr,omitempty"`
	Metadata       string `xml:"metadata"`
}

type xmlBootstrap struct {
	URL  string `xml:"url,attr"`
	Body string `xml:",chardata"`
}

type xmlDRM struct {
	ID string `xml:"id,attr"`
}

// Parse reads an F4M XML document and returns a normalized Manifest.
//
// DRM-attached media is filtered per f4m.py semantics: a <drmAdditionalHeader>
// or <drmAdditionalHeaderSet> element without an id attribute applies to every
// <media> entry that does not pin a specific id, so we reject the whole
// manifest with ErrUnsupportedDRM. Per-media id matches are tolerated because
// we simply discard the offending <media>; if every media is removed we return
// ErrUnsupportedDRM.
func Parse(manifestURL string, body []byte) (Manifest, error) {
	parsed, err := url.Parse(manifestURL)
	if err != nil || !isAllowedURL(parsed) {
		return Manifest{}, fmt.Errorf("%w: manifest URL", ErrInvalidManifest)
	}
	if len(body) == 0 || len(body) > maxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: manifest size %d", ErrInvalidManifest, len(body))
	}
	normalized := fixBareAmpersands(body)
	var doc xmlManifest
	dec := xml.NewDecoder(bytes.NewReader(normalized))
	dec.Strict = false
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, fmt.Errorf("%w: empty document", ErrInvalidManifest)
		}
		return Manifest{}, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	// Allow trailing whitespace and XML comments after the root but reject any
	// additional elements or text content that would indicate a malformed or
	// concatenated document.
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: trailing data: %v", ErrInvalidManifest, err)
		}
		switch tok := token.(type) {
		case xml.Comment, xml.ProcInst, xml.Directive:
			continue
		case xml.CharData:
			trimmed := strings.TrimSpace(string(tok))
			if trimmed == "" {
				continue
			}
			return Manifest{}, fmt.Errorf("%w: trailing non-whitespace data %q", ErrInvalidManifest, trimmed)
		default:
			return Manifest{}, fmt.Errorf("%w: trailing token after root: %T %v", ErrInvalidManifest, tok, tok)
		}
	}
	if _, ok := f4mNamespaces[doc.XMLName.Space]; !ok {
		return Manifest{}, fmt.Errorf("%w: root namespace %q is not F4M v1/v2", ErrInvalidManifest, doc.XMLName.Space)
	}
	if len(doc.Media) == 0 {
		return Manifest{}, fmt.Errorf("%w: no media elements", ErrInvalidManifest)
	}
	if len(doc.Media) > maxMediaElements {
		return Manifest{}, fmt.Errorf("%w: too many media elements", ErrInvalidManifest)
	}
	for _, header := range doc.DRMHeaders {
		if header.ID == "" {
			return Manifest{}, fmt.Errorf("%w: unscoped drmAdditionalHeader", ErrUnsupportedDRM)
		}
	}
	for _, header := range doc.DRMHeaderSets {
		if header.ID == "" {
			return Manifest{}, fmt.Errorf("%w: unscoped drmAdditionalHeaderSet", ErrUnsupportedDRM)
		}
	}
	base := strings.TrimSpace(doc.BaseURL)
	if base == "" {
		base = manifestURL
	}
	baseURL, err := url.Parse(base)
	if err != nil {
		return Manifest{}, fmt.Errorf("%w: baseURL parse: %v", ErrInvalidManifest, err)
	}
	if !baseURL.IsAbs() {
		// A relative <baseURL> must be resolved against the manifest URL.
		manifestParsed, parseErr := url.Parse(manifestURL)
		if parseErr != nil {
			return Manifest{}, fmt.Errorf("%w: manifest URL", ErrInvalidManifest)
		}
		baseURL = manifestParsed.ResolveReference(baseURL)
	}
	if !isAllowedURL(baseURL) {
		return Manifest{}, fmt.Errorf("%w: baseURL rejected (scheme=%q host=%q)", ErrInvalidManifest, baseURL.Scheme, baseURL.Host)
	}
	media := make([]Media, 0, len(doc.Media))
	for _, raw := range doc.Media {
		if raw.DRMHeaderID != "" || raw.DRMHeaderSetID != "" {
			continue
		}
		mediaURL, err := safeResolve(baseURL, raw.URL)
		if err != nil || mediaURL == "" {
			return Manifest{}, fmt.Errorf("%w: media url", ErrInvalidManifest)
		}
		bitrate, err := parseOptionalInt(raw.Bitrate)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: bitrate", ErrInvalidManifest)
		}
		width, err := parseOptionalInt(raw.Width)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: width", ErrInvalidManifest)
		}
		height, err := parseOptionalInt(raw.Height)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: height", ErrInvalidManifest)
		}
		var metadata []byte
		if trimmed := strings.TrimSpace(raw.Metadata); trimmed != "" {
			decoded, err := base64.StdEncoding.DecodeString(trimmed)
			if err != nil {
				return Manifest{}, fmt.Errorf("%w: metadata base64", ErrInvalidManifest)
			}
			if len(decoded) > maxDecodedMetadataBytes {
				return Manifest{}, fmt.Errorf("%w: metadata exceeds %d bytes", ErrInvalidManifest, maxDecodedMetadataBytes)
			}
			metadata = decoded
		}
		media = append(media, Media{
			URL:      mediaURL,
			Bitrate:  bitrate,
			Width:    int(width),
			Height:   int(height),
			Metadata: metadata,
		})
	}
	if len(media) == 0 {
		return Manifest{}, fmt.Errorf("%w: %v", ErrUnsupportedDRM, ErrUnsupportedEmpty)
	}
	source := BootstrapSource{}
	if external := strings.TrimSpace(doc.Bootstrap.URL); external != "" {
		resolved, err := safeResolve(baseURL, external)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: bootstrap url", ErrInvalidManifest)
		}
		source.URL = resolved
	} else {
		inline := strings.TrimSpace(doc.Bootstrap.Body)
		if inline == "" {
			return Manifest{}, fmt.Errorf("%w: missing bootstrap info", ErrInvalidManifest)
		}
		decoded, err := base64.StdEncoding.DecodeString(inline)
		if err != nil {
			return Manifest{}, fmt.Errorf("%w: inline bootstrap base64", ErrInvalidManifest)
		}
		if len(decoded) == 0 || len(decoded) > maxDecodedBootstrapBytes {
			return Manifest{}, fmt.Errorf("%w: inline bootstrap size %d", ErrInvalidBootstrap, len(decoded))
		}
		source.Inline = decoded
	}
	pv2 := strings.TrimSpace(doc.PV2)
	return Manifest{
		BaseURL:     baseURL.String(),
		PV2Query:    pv2,
		Media:       media,
		Bootstrap:   source,
		RawDocument: string(normalized),
	}, nil
}

// SelectMedia returns the requested Media or, when tbr<=0, the highest-bitrate
// unencrypted entry deterministically sorted by (bitrate asc, url asc).
func (m Manifest) SelectMedia(tbr int64) (Media, error) {
	if len(m.Media) == 0 {
		return Media{}, fmt.Errorf("%w: empty media list", ErrInvalidManifest)
	}
	if tbr > 0 {
		for _, entry := range m.Media {
			if entry.Bitrate == tbr {
				return entry, nil
			}
		}
		return Media{}, fmt.Errorf("%w: requested bitrate %d not found", ErrInvalidManifest, tbr)
	}
	sorted := make([]Media, len(m.Media))
	copy(sorted, m.Media)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Bitrate != sorted[j].Bitrate {
			return sorted[i].Bitrate < sorted[j].Bitrate
		}
		return sorted[i].URL < sorted[j].URL
	})
	return sorted[len(sorted)-1], nil
}

// fixBareAmpersands rewrites bare `&` characters that are not part of a valid
// XML entity reference, mirroring yt-dlp's fix_xml_ampersands. Valid entities
// (`&amp;`, `&lt;`, `&gt;`, `&quot;`, `&apos;`, `&#NN;`, `&#xNN;`) are
// preserved verbatim. Bare `&` characters are escaped to `&amp;` so the
// downstream xml.Decoder does not reject bare query ampersands or signed
// URLs that happen to contain them.
func fixBareAmpersands(body []byte) []byte {
	out := make([]byte, 0, len(body))
	i := 0
	for i < len(body) {
		ch := body[i]
		if ch != '&' {
			out = append(out, ch)
			i++
			continue
		}
		// Look ahead to see whether the ampersand begins a valid entity.
		j := i + 1
		if j < len(body) && body[j] == '#' {
			j++
			hex := false
			if j < len(body) && (body[j] == 'x' || body[j] == 'X') {
				hex = true
				j++
			}
			start := j
			for j < len(body) && isHexOrDigit(body[j], hex) {
				j++
			}
			if j > start && j < len(body) && body[j] == ';' {
				out = append(out, body[i:j+1]...)
				i = j + 1
				continue
			}
		} else {
			start := j
			for j < len(body) && (isAlpha(body[j]) || isDigit(body[j])) {
				j++
			}
			if j > start && j < len(body) && body[j] == ';' {
				if isPredefinedEntity(string(body[start:j])) {
					out = append(out, body[i:j+1]...)
					i = j + 1
					continue
				}
			}
		}
		out = append(out, '&', 'a', 'm', 'p', ';')
		i++
	}
	return out
}

func isPredefinedEntity(name string) bool {
	switch name {
	case "amp", "lt", "gt", "quot", "apos":
		return true
	}
	return false
}

func isAlpha(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }
func isDigit(b byte) bool { return b >= '0' && b <= '9' }
func isHexOrDigit(b byte, hex bool) bool {
	if isDigit(b) {
		return true
	}
	if !hex {
		return false
	}
	return (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// isAllowedURL enforces http(s) scheme with non-empty host and no embedded
// credentials. F4M bootstrap URLs and media URLs must always be http(s).
func isAllowedURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	if u.Host == "" {
		return false
	}
	if u.User != nil {
		return false
	}
	return true
}

// safeResolve joins ref against base, then validates the result with
// isAllowedURL. Scheme-relative refs (//cdn/foo) inherit the base scheme.
func safeResolve(base *url.URL, ref string) (string, error) {
	if ref == "" {
		return "", nil
	}
	parsed, err := url.Parse(ref)
	if err != nil {
		return "", err
	}
	resolved := base.ResolveReference(parsed)
	if !isAllowedURL(resolved) {
		return "", fmt.Errorf("disallowed url: scheme=%q host=%q user=%q", resolved.Scheme, resolved.Host, resolved.User)
	}
	return resolved.String(), nil
}

func parseOptionalInt(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, err
	}
	if value < 0 {
		return 0, fmt.Errorf("negative value")
	}
	return value, nil
}
