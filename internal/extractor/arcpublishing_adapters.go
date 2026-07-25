package extractor

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

var washingtonPostUUIDPath = regexp.MustCompile(`(?i)/(?:video|posttv)/(?:[^/]+/)*([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`)

// WashingtonPost is a thin Arc Publishing adapter for documented WaPo video UUID
// paths. Article playlist discovery remains out of this wave.
type WashingtonPost struct{}

func NewWashingtonPost() WashingtonPost { return WashingtonPost{} }
func (WashingtonPost) Name() string     { return "washingtonpost" }

func (WashingtonPost) Suitable(parsed *url.URL) bool {
	_, ok := parseWashingtonPostURL(parsed)
	return ok
}

func (WashingtonPost) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseWashingtonPostURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return URLResult(Entry{
		URL:          "arcpublishing:wapo:" + videoID,
		ExtractorKey: "arcpublishing",
		ID:           videoID,
	})
}

func parseWashingtonPostURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "washingtonpost.com" && host != "www.washingtonpost.com" {
		return "", false
	}
	match := washingtonPostUUIDPath.FindStringSubmatch(parsed.Path)
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

type arcPowaSite struct {
	key     string
	org     string
	host    string
	anyPath bool
}

func (site arcPowaSite) Name() string { return site.key }

func (site arcPowaSite) Suitable(parsed *url.URL) bool {
	if !arcSiteHostOK(parsed, site.host) {
		return false
	}
	if site.anyPath {
		return arcSiteAnyPathOK(parsed)
	}
	return arcSitePathOK(parsed)
}

func (site arcPowaSite) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil || !arcSiteHostOK(parsed, site.host) {
		return Extraction{}, ErrUnsupported
	}
	if site.anyPath {
		if !arcSiteAnyPathOK(parsed) {
			return Extraction{}, ErrUnsupported
		}
	} else if !arcSitePathOK(parsed) {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://" + site.host + parsed.EscapedPath()
	if parsed.RawQuery != "" {
		canonical += "?" + parsed.RawQuery
	}
	if len(canonical) > sharedHostingMaxURLBytes {
		return Extraction{}, fmt.Errorf("%w: Arc site URL too long", ErrInvalidMetadata)
	}
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	entries, err := extractArcPowaEntries(page, site.org)
	if err != nil {
		return Extraction{}, err
	}
	if len(entries) == 0 {
		lower := strings.ToLower(string(page))
		if strings.Contains(lower, "sign in") || strings.Contains(lower, "log in") {
			return Extraction{}, ErrAuthentication
		}
		if strings.Contains(lower, "not found") {
			return Extraction{}, ErrUnavailable
		}
		return Extraction{}, fmt.Errorf("%w: missing Arc POWA embeds", ErrInvalidMetadata)
	}
	if len(entries) == 1 {
		return URLResult(entries[0])
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(site.key + "-" + entries[0].ID)},
		value.Field{Key: "title", Value: value.String(site.key + " playlist")},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, StaticEntries(entries...))
}

func arcSiteHostOK(parsed *url.URL, host string) bool {
	if hostedRejectUnsafeURL(parsed) {
		return false
	}
	got := strings.ToLower(parsed.Hostname())
	return got == host || got == "www."+host
}

func arcSitePathOK(parsed *url.URL) bool {
	path := strings.ToLower(parsed.EscapedPath())
	if path == "" || path == "/" {
		return false
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		base := strings.TrimSuffix(segment, ".html")
		if segment == "video" || segment == "videos" || strings.HasPrefix(segment, "video-") ||
			strings.Contains(segment, "-video-") || strings.HasSuffix(segment, "-video") ||
			arcUUIDPattern.MatchString(base) {
			return true
		}
	}
	return false
}

func arcSiteAnyPathOK(parsed *url.URL) bool {
	path := strings.ToLower(parsed.EscapedPath())
	if path == "" || path == "/" {
		return false
	}
	segments := strings.Split(strings.Trim(path, "/"), "/")
	return len(segments) >= 2
}

// ADN extracts Arc POWA embeds from adn.com article/video pages.
type ADN struct{ arcPowaSite }

func NewADN() ADN {
	return ADN{arcPowaSite{key: "adn", org: "adn", host: "adn.com"}}
}

// BostonGlobe extracts Arc POWA embeds from bostonglobe.com pages.
type BostonGlobe struct{ arcPowaSite }

func NewBostonGlobe() BostonGlobe {
	return BostonGlobe{arcPowaSite{key: "bostonglobe", org: "bostonglobe", host: "bostonglobe.com"}}
}

// Gray extracts Arc POWA embeds from wabi.tv (Gray Digital) pages.
type Gray struct{ arcPowaSite }

func NewGray() Gray {
	return Gray{arcPowaSite{key: "gray", org: "gray", host: "wabi.tv"}}
}

// ClickOnDetroit extracts Arc POWA embeds from clickondetroit.com pages.
type ClickOnDetroit struct{ arcPowaSite }

func NewClickOnDetroit() ClickOnDetroit {
	return ClickOnDetroit{arcPowaSite{key: "clickondetroit", org: "gmg", host: "clickondetroit.com"}}
}

// ActionNewsJax extracts Arc POWA embeds from actionnewsjax.com (CMG).
type ActionNewsJax struct{ arcPowaSite }

func NewActionNewsJax() ActionNewsJax {
	return ActionNewsJax{arcPowaSite{key: "actionnewsjax", org: "cmg", host: "actionnewsjax.com"}}
}

// ElComercio extracts Arc POWA embeds from elcomercio.pe.
type ElComercio struct{ arcPowaSite }

func NewElComercio() ElComercio {
	return ElComercio{arcPowaSite{key: "elcomercio", org: "elcomercio", host: "elcomercio.pe"}}
}

// Lateja extracts Arc POWA embeds from lateja.cr.
type Lateja struct{ arcPowaSite }

func NewLateja() Lateja {
	return Lateja{arcPowaSite{key: "lateja", org: "gruponacion", host: "lateja.cr"}}
}

// FifthDomain extracts Arc POWA embeds from fifthdomain.com.
type FifthDomain struct{ arcPowaSite }

func NewFifthDomain() FifthDomain {
	return FifthDomain{arcPowaSite{key: "fifthdomain", org: "mco", host: "fifthdomain.com"}}
}

// VLNO extracts Arc POWA embeds from vl.no.
type VLNO struct{ arcPowaSite }

func NewVLNO() VLNO {
	return VLNO{arcPowaSite{key: "vlno", org: "mentormedier", host: "vl.no", anyPath: true}}
}

// FourteenNews extracts Arc POWA embeds from 14news.com.
type FourteenNews struct{ arcPowaSite }

func NewFourteenNews() FourteenNews {
	return FourteenNews{arcPowaSite{key: "fourteennews", org: "raycom", host: "14news.com", anyPath: true}}
}

// GlobeAndMail extracts Arc POWA embeds from theglobeandmail.com.
type GlobeAndMail struct{ arcPowaSite }

func NewGlobeAndMail() GlobeAndMail {
	return GlobeAndMail{arcPowaSite{key: "globeandmail", org: "tgam", host: "theglobeandmail.com"}}
}

// PilotOnline extracts Arc POWA embeds from pilotonline.com.
type PilotOnline struct{ arcPowaSite }

func NewPilotOnline() PilotOnline {
	return PilotOnline{arcPowaSite{key: "pilotonline", org: "tronc", host: "pilotonline.com", anyPath: true}}
}

// UpperMichiganSource extracts Arc POWA embeds from uppermichigansource.com.
type UpperMichiganSource struct{ arcPowaSite }

func NewUpperMichiganSource() UpperMichiganSource {
	return UpperMichiganSource{arcPowaSite{key: "uppermichigansource", org: "gray", host: "uppermichigansource.com", anyPath: true}}
}
