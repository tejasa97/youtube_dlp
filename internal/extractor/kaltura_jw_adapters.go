package extractor

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

const (
	azMedienPartnerID   = "1719221"
	incDefaultPartner   = "1034971"
	heiseKalturaPartner = "2238431"
)

var (
	unWebTVPath   = regexp.MustCompile(`(?i)^/(ar|zh|en|fr|ru|es)/asset/[A-Za-z0-9_-]+/([A-Za-z0-9_-]{1,64})/?$`)
	unPartnerID   = regexp.MustCompile(`(?i)partnerId:\s*([0-9]{1,16})`)
	unEntryID     = regexp.MustCompile(`(?i)const\s+kentryID\s*=\s*["']([A-Za-z0-9_-]{1,64})["']`)
	azMedienHosts = map[string]bool{
		"telezueri.ch": true, "tv.telezueri.ch": true, "www.telezueri.ch": true,
		"telebaern.tv": true, "www.telebaern.tv": true, "tv.telebaern.tv": true,
		"telem1.ch": true, "www.telem1.ch": true, "tv.telem1.ch": true,
		"tvo-online.ch": true, "www.tvo-online.ch": true, "tv.tvo-online.ch": true,
	}
	azMedienPath    = regexp.MustCompile(`(?i)^/[^/?#]+/([^/?#]+-\d{1,16})/?$`)
	azMedienEntry   = regexp.MustCompile(`(?i)^video=([0-9A-Za-z_]{1,64})$`)
	azApolloID      = regexp.MustCompile(`(?i)"__typename"\s*:\s*"KalturaData"[^}]{0,256}"kalturaId"\s*:\s*"([0-9A-Za-z_]{1,64})"`)
	azApolloIDAlt   = regexp.MustCompile(`(?i)"kalturaId"\s*:\s*"([0-9A-Za-z_]{1,64})"[^}]{0,256}"__typename"\s*:\s*"KalturaData"`)
	incPath         = regexp.MustCompile(`(?i)^/(?:[^/]+/)+([^.]+)\.html$`)
	incPartner      = regexp.MustCompile(`(?i)var\s+_?bizo_data_partner_id\s*=\s*["']([0-9]{1,16})["']`)
	incPlayerID     = regexp.MustCompile(`(?i)id=["']kaltura_player_([0-9A-Za-z_]{1,64})["']`)
	heisePath       = regexp.MustCompile(`(?i)^/(?:[^/]+/)+[^/]+-([0-9]{1,16})\.html$`)
	heiseEntryID    = regexp.MustCompile(`(?i)entry-id=["']([0-9A-Za-z_]{1,64})["']`)
	heiseKalturaURL = regexp.MustCompile(`(?i)https?://(?:[^/"']+\.)?kaltura\.com/[^"']+entry_id/([0-9A-Za-z_]{1,64})`)
	spiegelPath     = regexp.MustCompile(`(?i)^/(?:[^/]+/)+[^/]*-([0-9]{1,16}|[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:-embed|-iframe)?(?:\.html)?/?$`)
	spiegelMedia    = regexp.MustCompile(`(?i)(?:&#34;|["'])mediaId(?:&#34;|["'])\s*:\s*(?:&#34;|["'])([A-Za-z0-9]{8})(?:&#34;|["'])`)
	oneFootballPath = regexp.MustCompile(`(?i)^/[a-z]{2}/video/[^/&?#]+-([0-9]{1,16})/?$`)
	oneFootballJW   = regexp.MustCompile(`(?i)https://cdn\.jwplayer\.com/manifests/([A-Za-z0-9]{8})\.m3u8`)
)

func kalturaURLResult(partnerID, entryID string) (Extraction, error) {
	if !kalturaPartnerPattern.MatchString(partnerID) || !kalturaEntryPattern.MatchString(entryID) {
		return Extraction{}, fmt.Errorf("%w: invalid Kaltura handoff", ErrInvalidMetadata)
	}
	return URLResult(Entry{
		URL:          "kaltura:" + partnerID + ":" + entryID,
		ExtractorKey: "kaltura",
		ID:           entryID,
		Transparent:  true,
	})
}

// jwPlatformURLResult is the shared validated transparent handoff for any
// JW-backed site adapter. Adapters pass an Entry whose optional fields are
// preserved transparently across product recursion; missing IDs fall back to
// the validated media id, while URL and ExtractorKey are always rewritten.
func jwPlatformURLResult(mediaID, displayID string) (Extraction, error) {
	entry, err := jwPlatformEntry(mediaID, Entry{ID: displayID})
	if err != nil {
		return Extraction{}, err
	}
	return URLResult(entry)
}

// jwPlatformURLEntry builds the same handoff when the caller already has an
// Entry with producer metadata they want preserved across product recursion.
// The transparent flag is always set so the next extractor inherits the
// supplied metadata; routing fields (URL, ExtractorKey) are not honored.
func jwPlatformURLEntry(mediaID string, entry Entry) (Extraction, error) {
	built, err := jwPlatformEntry(mediaID, entry)
	if err != nil {
		return Extraction{}, err
	}
	return URLResult(built)
}

// jwPlatformEntry is the shared validated JW-backed Entry constructor used
// by URLResult handoffs and playlist entry construction. It validates the
// media id, rewrites URL and ExtractorKey, falls back to the media id when
// the supplied Entry has none, and marks the Entry as transparent so
// product recursion inherits producer metadata.
func jwPlatformEntry(mediaID string, entry Entry) (Entry, error) {
	if !jwPlatformID.MatchString(mediaID) {
		return Entry{}, fmt.Errorf("%w: invalid JW Platform handoff", ErrInvalidMetadata)
	}
	entry.URL = "jwplatform:" + mediaID
	entry.ExtractorKey = "jwplatform"
	if entry.ID == "" {
		entry.ID = mediaID
	}
	entry.Transparent = true
	return entry, nil
}

// UnitedNationsWebTV routes webtv.un.org assets to Kaltura.
type UnitedNationsWebTV struct{}

func NewUnitedNationsWebTV() UnitedNationsWebTV { return UnitedNationsWebTV{} }
func (UnitedNationsWebTV) Name() string         { return "unitednationswebtv" }

func (UnitedNationsWebTV) Suitable(parsed *url.URL) bool {
	_, ok := parseUNWebTVURL(parsed)
	return ok
}

func (UnitedNationsWebTV) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseUNWebTVURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://webtv.un.org" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: UN WebTV page too large", ErrInvalidMetadata)
	}
	partner := unPartnerID.FindSubmatch(page)
	entry := unEntryID.FindSubmatch(page)
	if len(partner) != 2 || len(entry) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "UN WebTV Kaltura ids")
	}
	return kalturaURLResult(string(partner[1]), string(entry[1]))
}

func parseUNWebTVURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "webtv.un.org" {
		return "", false
	}
	match := unWebTVPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", false
	}
	return match[2], true
}

// AZMedien routes Swiss regional TV pages to a fixed Kaltura partner.
type AZMedien struct{}

func NewAZMedien() AZMedien   { return AZMedien{} }
func (AZMedien) Name() string { return "azmedien" }

func (AZMedien) Suitable(parsed *url.URL) bool {
	_, _, ok := parseAZMedienURL(parsed)
	return ok
}

func (AZMedien) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, entryID, ok := parseAZMedienURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if entryID != "" {
		return kalturaURLResult(azMedienPartnerID, entryID)
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	host := strings.ToLower(parsed.Hostname())
	canonical := "https://" + host + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: AZ Medien page too large", ErrInvalidMetadata)
	}
	match := azApolloID.FindSubmatch(page)
	if len(match) != 2 {
		match = azApolloIDAlt.FindSubmatch(page)
	}
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "AZ Medien Kaltura id")
	}
	_ = displayID
	return kalturaURLResult(azMedienPartnerID, string(match[1]))
}

func parseAZMedienURL(parsed *url.URL) (displayID, entryID string, ok bool) {
	if parsed == nil || len(parsed.String()) > sharedHostingMaxURLBytes {
		return "", "", false
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || parsed.Port() != "" {
		return "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if !azMedienHosts[host] {
		return "", "", false
	}
	if strictPathUnsafe(parsed.EscapedPath()) {
		return "", "", false
	}
	match := azMedienPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", "", false
	}
	displayID = match[1]
	if parsed.Fragment != "" {
		frag := azMedienEntry.FindStringSubmatch(parsed.Fragment)
		if len(frag) != 2 {
			return "", "", false
		}
		entryID = frag[1]
	}
	return displayID, entryID, true
}

// Inc routes inc.com articles to Kaltura player embeds.
type Inc struct{}

func NewInc() Inc        { return Inc{} }
func (Inc) Name() string { return "inc" }

func (Inc) Suitable(parsed *url.URL) bool {
	_, ok := parseIncURL(parsed)
	return ok
}

func (Inc) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseIncURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.inc.com" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Inc page too large", ErrInvalidMetadata)
	}
	partner := incDefaultPartner
	if match := incPartner.FindSubmatch(page); len(match) == 2 {
		partner = string(match[1])
	}
	match := incPlayerID.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Inc Kaltura player id")
	}
	return kalturaURLResult(partner, string(match[1]))
}

func parseIncURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "inc.com" && host != "www.inc.com" {
		return "", false
	}
	match := incPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Heise routes heise.de Kaltura embeds (YouTube-only pages are unsupported here).
type Heise struct{}

func NewHeise() Heise      { return Heise{} }
func (Heise) Name() string { return "heise" }

func (Heise) Suitable(parsed *url.URL) bool {
	_, ok := parseHeiseURL(parsed)
	return ok
}

func (Heise) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseHeiseURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.heise.de" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Heise page too large", ErrInvalidMetadata)
	}
	if match := heiseKalturaURL.FindSubmatch(page); len(match) == 2 {
		return kalturaURLResult(heiseKalturaPartner, string(match[1]))
	}
	if match := heiseEntryID.FindSubmatch(page); len(match) == 2 {
		return kalturaURLResult(heiseKalturaPartner, string(match[1]))
	}
	return Extraction{}, classifyMissingMediaPage(page, "Heise Kaltura embed")
}

func parseHeiseURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "heise.de" && host != "www.heise.de" {
		return "", false
	}
	match := heisePath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Spiegel routes spiegel.de / manager-magazin.de pages to JW Platform.
type Spiegel struct{}

func NewSpiegel() Spiegel    { return Spiegel{} }
func (Spiegel) Name() string { return "spiegel" }

func (Spiegel) Suitable(parsed *url.URL) bool {
	_, ok := parseSpiegelURL(parsed)
	return ok
}

func (Spiegel) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseSpiegelURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	canonical := "https://www." + host + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Spiegel page too large", ErrInvalidMetadata)
	}
	match := spiegelMedia.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Spiegel JW mediaId")
	}
	return jwPlatformURLResult(string(match[1]), displayID)
}

func parseSpiegelURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	if host != "spiegel.de" && host != "manager-magazin.de" {
		return "", false
	}
	match := spiegelPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// OneFootball routes onefootball.com videos to JW Platform manifests.
type OneFootball struct{}

func NewOneFootball() OneFootball { return OneFootball{} }
func (OneFootball) Name() string  { return "onefootball" }

func (OneFootball) Suitable(parsed *url.URL) bool {
	_, ok := parseOneFootballURL(parsed)
	return ok
}

func (OneFootball) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseOneFootballURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://onefootball.com" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: OneFootball page too large", ErrInvalidMetadata)
	}
	match := oneFootballJW.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "OneFootball JW manifest")
	}
	return jwPlatformURLResult(string(match[1]), displayID)
}

func parseOneFootballURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "onefootball.com" && host != "www.onefootball.com" {
		return "", false
	}
	match := oneFootballPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}
