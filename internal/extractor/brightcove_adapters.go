package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	brightcoveAdapterMaxEntries = 256
	pgaTourCloudcastAccount     = "6116716431001"
	pgaTourCloudcastPlayer      = "Vsd5Umu8r"
	pgaTourFeaturesAccount      = "6082840763001"
	pgaTourFeaturesPlayer       = "FWIBYMBPj"
	netAppBrightcoveAccount     = "6255154784001"
	tvaNouvellesAccount         = "1741764581"
	nineNowBrightcoveAccount    = "4460760524001"
	tvaPlusBrightcoveAccount    = "5481942443001"
	tvoBrightcoveAccount        = "18140038001"
)

var (
	brightcoveDigitsID   = regexp.MustCompile(`^[0-9]{1,32}$`)
	brightcoveRefOrDigit = regexp.MustCompile(`^(?:[0-9]{1,32}|ref:[A-Za-z0-9_.-]{1,256})$`)
	pgaTourVideoPath     = regexp.MustCompile(`(?i)^/video/[A-Za-z0-9_-]{1,128}/(T)?([0-9]{1,32})(?:/[A-Za-z0-9_-]{1,256})?/?$`)
	nineNewsPath         = regexp.MustCompile(`(?i)^/(?:[A-Za-z0-9_-]+/){2,3}([A-Za-z0-9_-]{1,256})/?$`)
	nineNowPath          = regexp.MustCompile(`(?i)^/(?:[^/]+/){2}((clip|episode)-[A-Za-z0-9_-]{1,256})/?$`)
	netAppUUIDPath       = regexp.MustCompile(`(?i)^/video-detail/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(?:/.*)?$`)
	netAppCollectionPath = regexp.MustCompile(`(?i)^/collection/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})/?$`)
	amcNetworksPath      = regexp.MustCompile(`(?i)^/((?:movies|shows(?:/[^/?#]+)+)/[^/?#&]+)$`)
	craftsyClassPath     = regexp.MustCompile(`(?i)^/class/([A-Za-z0-9_-]{1,256})/?$`)
	tvoVideoPath         = regexp.MustCompile(`(?i)^/video(?:/documentaries)?/([A-Za-z0-9_-]{1,256})/?$`)
	tvaPlusPath          = regexp.MustCompile(`(?i)^/(?:[^/]+/)*[A-Za-z0-9_-]+-([0-9]{1,32})/?$`)
	tvaNouvellesVideo    = regexp.MustCompile(`(?i)^/videos/([0-9]{1,32})/?$`)
	tvaNouvellesArticle  = regexp.MustCompile(`(?i)^/(?:[^/]+/)+([A-Za-z0-9_-]{1,256})/?$`)
	tvaNouvellesVideoID  = regexp.MustCompile(`(?i)data-video-id=["']?([0-9]{1,32})`)
	nineNewsBrightcove   = regexp.MustCompile(`(?i)"brightcoveId"\s*:\s*"([0-9]{1,32})"`)
	nineNewsAccount      = regexp.MustCompile(`(?i)"account"\s*:\s*"([0-9]{1,32})"`)
	nineNowBrightcove    = regexp.MustCompile(`(?i)"brightcoveId"\s*:\s*"([0-9]{1,32}|ref:[A-Za-z0-9_.-]{1,256})"`)
	nineNowDRM           = regexp.MustCompile(`(?i)"drm"\s*:\s*true`)
	amcInitialData       = regexp.MustCompile(`(?is)window\.initialData\s*=\s*JSON\.parse\(\s*String\.raw` + "`")
	craftsyWireSnapshot  = regexp.MustCompile(`(?is)wire:snapshot="([^"]+)"`)
	craftsyVideoJS       = regexp.MustCompile(`(?is)<video-js[^>]+data-video-id=["']([0-9]{1,32})["'][^>]*data-account=["']([0-9]{1,32})["']`)
	craftsyVideoJSAlt    = regexp.MustCompile(`(?is)<video-js[^>]+data-account=["']([0-9]{1,32})["'][^>]*data-video-id=["']([0-9]{1,32})["']`)
	tvaNextData          = regexp.MustCompile(`(?is)<script[^>]+id=["']__NEXT_DATA__["'][^>]*>`)
)

func brightcovePlayerURL(accountID, playerID, videoID string) string {
	if playerID == "" {
		playerID = "default"
	}
	return "https://players.brightcove.net/" + accountID + "/" + playerID + "_default/index.html?videoId=" + url.QueryEscape(videoID)
}

func brightcoveURLResult(accountID, playerID, videoID string) (Extraction, error) {
	if !brightcoveAccountID.MatchString(accountID) || !brightcoveRefOrDigit.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: invalid Brightcove handoff", ErrInvalidMetadata)
	}
	if playerID == "" {
		playerID = "default"
	}
	if !brightcovePlayerID.MatchString(playerID) {
		return Extraction{}, fmt.Errorf("%w: invalid Brightcove player", ErrInvalidMetadata)
	}
	return URLResult(Entry{
		URL:          brightcovePlayerURL(accountID, playerID, videoID),
		ExtractorKey: "brightcove",
		ID:           videoID,
		Transparent:  true,
	})
}

// PGATour routes pgatour.com/video pages to the documented Brightcove accounts.
type PGATour struct{}

func NewPGATour() PGATour    { return PGATour{} }
func (PGATour) Name() string { return "pgatour" }

func (PGATour) Suitable(parsed *url.URL) bool {
	_, _, _, ok := parsePGATourURL(parsed)
	return ok
}

func (PGATour) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, account, player, ok := parsePGATourURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return brightcoveURLResult(account, player, videoID)
}

func parsePGATourURL(parsed *url.URL) (videoID, account, player string, ok bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", "", "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "pgatour.com" && host != "www.pgatour.com" {
		return "", "", "", false
	}
	match := pgaTourVideoPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", "", "", false
	}
	if match[1] == "T" {
		return match[2], pgaTourCloudcastAccount, pgaTourCloudcastPlayer, true
	}
	return match[2], pgaTourFeaturesAccount, pgaTourFeaturesPlayer, true
}

// TVANouvelles routes tvanouvelles.ca/videos/<id> to Brightcove.
type TVANouvelles struct{}

func NewTVANouvelles() TVANouvelles { return TVANouvelles{} }
func (TVANouvelles) Name() string   { return "tvanouvelles" }

func (TVANouvelles) Suitable(parsed *url.URL) bool {
	_, ok := parseTVANouvellesURL(parsed)
	return ok
}

func (TVANouvelles) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseTVANouvellesURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return brightcoveURLResult(tvaNouvellesAccount, "default", videoID)
}

func parseTVANouvellesURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tvanouvelles.ca" && host != "www.tvanouvelles.ca" {
		return "", false
	}
	match := tvaNouvellesVideo.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// TVANouvellesArticle collects data-video-id embeds from article pages.
type TVANouvellesArticle struct{}

func NewTVANouvellesArticle() TVANouvellesArticle { return TVANouvellesArticle{} }
func (TVANouvellesArticle) Name() string          { return "tvanouvelles_article" }

func (TVANouvellesArticle) Suitable(parsed *url.URL) bool {
	_, ok := parseTVANouvellesArticleURL(parsed)
	return ok
}

func (TVANouvellesArticle) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseTVANouvellesArticleURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.tvanouvelles.ca" + parsed.EscapedPath()
	if len(canonical) > sharedHostingMaxURLBytes {
		return Extraction{}, fmt.Errorf("%w: TVA Nouvelles article URL too long", ErrInvalidMetadata)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "title", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(brightcoveAdapterMaxEntries, func(ctx context.Context) ([]Entry, error) {
		page, _, err := request.Transport.ReadPage(ctx, canonical)
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(page)) > maxExtractorJSONBytes {
			return nil, fmt.Errorf("%w: TVA Nouvelles article too large", ErrInvalidMetadata)
		}
		matches := tvaNouvellesVideoID.FindAllSubmatch(page, brightcoveAdapterMaxEntries+1)
		if len(matches) == 0 {
			return nil, classifyMissingMediaPage(page, "TVA Nouvelles video embeds")
		}
		if len(matches) > brightcoveAdapterMaxEntries {
			return nil, fmt.Errorf("%w: TVA Nouvelles article entry overflow", ErrInvalidMetadata)
		}
		seen := make(map[string]bool, len(matches))
		entries := make([]Entry, 0, len(matches))
		for _, match := range matches {
			if len(match) != 2 {
				continue
			}
			id := string(match[1])
			if seen[id] {
				continue
			}
			seen[id] = true
			entries = append(entries, Entry{
				URL:          "https://www.tvanouvelles.ca/videos/" + id,
				ExtractorKey: "tvanouvelles",
				ID:           id,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: missing TVA Nouvelles video embeds", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseTVANouvellesArticleURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tvanouvelles.ca" && host != "www.tvanouvelles.ca" {
		return "", false
	}
	if _, videoOK := parseTVANouvellesURL(parsed); videoOK {
		return "", false
	}
	match := tvaNouvellesArticle.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// NetApp extracts Brightcove IDs from media.netapp.com video-detail JSON.
type NetApp struct{}

func NewNetApp() NetApp     { return NetApp{} }
func (NetApp) Name() string { return "netapp" }

func (NetApp) Suitable(parsed *url.URL) bool {
	_, ok := parseNetAppURL(parsed)
	return ok
}

func (NetApp) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	uuid, ok := parseNetAppURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://api.media.netapp.com/client/detail/" + uuid
	var payload struct {
		Sections []struct {
			Type  string `json:"type"`
			Video string `json:"video"`
		} `json:"sections"`
	}
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	var videoID string
	for _, section := range payload.Sections {
		if section.Type == "Player" && brightcoveDigitsID.MatchString(section.Video) {
			videoID = section.Video
			break
		}
	}
	if videoID == "" {
		return Extraction{}, fmt.Errorf("%w: missing NetApp Brightcove id", ErrInvalidMetadata)
	}
	return brightcoveURLResult(netAppBrightcoveAccount, "default", videoID)
}

func parseNetAppURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "media.netapp.com" {
		return "", false
	}
	match := netAppUUIDPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// NetAppCollection enumerates Brightcove entries for a NetApp collection.
type NetAppCollection struct{}

func NewNetAppCollection() NetAppCollection { return NetAppCollection{} }
func (NetAppCollection) Name() string       { return "netapp_collection" }

func (NetAppCollection) Suitable(parsed *url.URL) bool {
	_, ok := parseNetAppCollectionURL(parsed)
	return ok
}

func (NetAppCollection) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	uuid, ok := parseNetAppCollectionURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://api.media.netapp.com/client/collection/" + uuid
	canonical := "https://media.netapp.com/collection/" + uuid
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(uuid)},
		value.Field{Key: "title", Value: value.String(uuid)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(brightcoveAdapterMaxEntries, func(ctx context.Context) ([]Entry, error) {
		var payload struct {
			Name  string `json:"name"`
			Items []struct {
				BrightcoveVideoID string `json:"brightcoveVideoId"`
				Name              string `json:"name"`
			} `json:"items"`
		}
		if err := hostedRequestJSON(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(payload.Items) > brightcoveAdapterMaxEntries {
			return nil, fmt.Errorf("%w: NetApp collection overflow", ErrInvalidMetadata)
		}
		entries := make([]Entry, 0, len(payload.Items))
		seen := make(map[string]bool)
		for _, item := range payload.Items {
			id := strings.TrimSpace(item.BrightcoveVideoID)
			if !brightcoveDigitsID.MatchString(id) || seen[id] {
				continue
			}
			seen[id] = true
			entries = append(entries, Entry{
				URL:          brightcovePlayerURL(netAppBrightcoveAccount, "default", id),
				ExtractorKey: "brightcove",
				ID:           id,
				Title:        item.Name,
				Transparent:  true,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty NetApp collection", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

func parseNetAppCollectionURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if strings.ToLower(parsed.Hostname()) != "media.netapp.com" {
		return "", false
	}
	match := netAppCollectionPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return strings.ToLower(match[1]), true
}

// NineNews extracts Brightcove IDs from 9news.com.au __INITIAL_STATE__.
type NineNews struct{}

func NewNineNews() NineNews   { return NineNews{} }
func (NineNews) Name() string { return "ninenews" }

func (NineNews) Suitable(parsed *url.URL) bool {
	_, ok := parseNineNewsURL(parsed)
	return ok
}

func (NineNews) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseNineNewsURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.9news.com.au" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: 9News page too large", ErrInvalidMetadata)
	}
	videoMatch := nineNewsBrightcove.FindSubmatch(page)
	accountMatch := nineNewsAccount.FindSubmatch(page)
	if len(videoMatch) != 2 || len(accountMatch) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "9News Brightcove metadata")
	}
	return brightcoveURLResult(string(accountMatch[1]), "default", string(videoMatch[1]))
}

func parseNineNewsURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "9news.com.au" && host != "www.9news.com.au" {
		return "", false
	}
	match := nineNewsPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// NineNow extracts Brightcove IDs from 9now.com.au clip/episode pages.
type NineNow struct{}

func NewNineNow() NineNow    { return NineNow{} }
func (NineNow) Name() string { return "ninenow" }

func (NineNow) Suitable(parsed *url.URL) bool {
	_, ok := parseNineNowURL(parsed)
	return ok
}

func (NineNow) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseNineNowURL(parsed); !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.9now.com.au" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: 9Now page too large", ErrInvalidMetadata)
	}
	if nineNowDRM.Match(page) {
		return Extraction{}, ErrAuthentication
	}
	match := nineNowBrightcove.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "9Now Brightcove id")
	}
	return brightcoveURLResult(nineNowBrightcoveAccount, "default", string(match[1]))
}

func parseNineNowURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "9now.com.au" && host != "www.9now.com.au" {
		return "", false
	}
	match := nineNowPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 3 {
		return "", false
	}
	return match[1], true
}

// AMCNetworks extracts Brightcove player config from AMC family pages.
type AMCNetworks struct{}

func NewAMCNetworks() AMCNetworks { return AMCNetworks{} }
func (AMCNetworks) Name() string  { return "amcnetworks" }

func (AMCNetworks) Suitable(parsed *url.URL) bool {
	_, ok := parseAMCNetworksURL(parsed)
	return ok
}

func (AMCNetworks) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	if _, ok := parseAMCNetworksURL(parsed); !ok {
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
		return Extraction{}, fmt.Errorf("%w: AMCNetworks page too large", ErrInvalidMetadata)
	}
	raw, err := extractJSONObjectAfter(page, amcInitialData)
	if err != nil {
		return Extraction{}, classifyMissingMediaPage(page, "AMCNetworks initialData")
	}
	var payload struct {
		InitialData struct {
			Properties struct {
				VideoID string `json:"videoId"`
			} `json:"properties"`
		} `json:"initialData"`
		Config struct {
			Brightcove struct {
				AccountID string `json:"accountId"`
				PlayerID  string `json:"playerId"`
			} `json:"brightcove"`
		} `json:"config"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid AMCNetworks initialData", ErrInvalidMetadata)
	}
	if payload.InitialData.Properties.VideoID == "" {
		return Extraction{}, ErrAuthentication
	}
	return brightcoveURLResult(payload.Config.Brightcove.AccountID, payload.Config.Brightcove.PlayerID, payload.InitialData.Properties.VideoID)
}

func parseAMCNetworksURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	switch host {
	case "amc.com", "bbcamerica.com", "ifc.com", "wetv.com", "sundancetv.com":
	default:
		return "", false
	}
	match := amcNetworksPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// Craftsy extracts Brightcove lesson playlists from craftsy.com/class pages.
type Craftsy struct{}

func NewCraftsy() Craftsy    { return Craftsy{} }
func (Craftsy) Name() string { return "craftsy" }

func (Craftsy) Suitable(parsed *url.URL) bool {
	_, ok := parseCraftsyURL(parsed)
	return ok
}

func (Craftsy) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseCraftsyURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.craftsy.com/class/" + slug
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(slug)},
		value.Field{Key: "title", Value: value.String(slug)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	sequence, err := LazyFirstPageEntries(brightcoveAdapterMaxEntries, func(ctx context.Context) ([]Entry, error) {
		page, _, err := request.Transport.ReadPage(ctx, canonical+"/")
		if err != nil {
			return nil, err
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if int64(len(page)) > maxExtractorJSONBytes {
			return nil, fmt.Errorf("%w: Craftsy page too large", ErrInvalidMetadata)
		}
		accountID, lessons, err := parseCraftsyLessons(page)
		if err != nil {
			return nil, err
		}
		entries := make([]Entry, 0, len(lessons))
		for _, lesson := range lessons {
			entries = append(entries, Entry{
				URL:          brightcovePlayerURL(accountID, "default", lesson.id),
				ExtractorKey: "brightcove",
				ID:           lesson.id,
				Title:        lesson.title,
				Transparent:  true,
			})
		}
		if len(entries) == 0 {
			return nil, fmt.Errorf("%w: empty Craftsy class", ErrInvalidMetadata)
		}
		return entries, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(info, sequence)
}

type craftsyLesson struct {
	id, title string
}

func parseCraftsyLessons(page []byte) (accountID string, lessons []craftsyLesson, err error) {
	if match := craftsyWireSnapshot.FindSubmatch(page); len(match) == 2 {
		if len(match[1]) > 1<<20 {
			return "", nil, fmt.Errorf("%w: Craftsy snapshot too large", ErrInvalidMetadata)
		}
		decoded := htmlUnescapeAttr(string(match[1]))
		var snapshot struct {
			Data struct {
				AccountID     string `json:"accountId"`
				UserHasAccess bool   `json:"userHasAccess"`
				Lessons       [][]struct {
					VideoID string `json:"video_id"`
					Title   string `json:"title"`
				} `json:"lessons"`
			} `json:"data"`
		}
		if jsonErr := json.Unmarshal([]byte(decoded), &snapshot); jsonErr == nil {
			accountID = snapshot.Data.AccountID
			for _, group := range snapshot.Data.Lessons {
				for _, lesson := range group {
					if brightcoveDigitsID.MatchString(lesson.VideoID) {
						lessons = append(lessons, craftsyLesson{id: lesson.VideoID, title: lesson.Title})
					}
				}
			}
			if !snapshot.Data.UserHasAccess && len(lessons) == 0 {
				return "", nil, ErrAuthentication
			}
		}
	}
	if accountID == "" || len(lessons) == 0 {
		if match := craftsyVideoJS.FindSubmatch(page); len(match) == 3 {
			lessons = append(lessons, craftsyLesson{id: string(match[1])})
			accountID = string(match[2])
		} else if match := craftsyVideoJSAlt.FindSubmatch(page); len(match) == 3 {
			accountID = string(match[1])
			lessons = append(lessons, craftsyLesson{id: string(match[2])})
		}
	}
	if accountID == "" || len(lessons) == 0 {
		return "", nil, classifyMissingMediaPage(page, "Craftsy Brightcove lessons")
	}
	if len(lessons) > brightcoveAdapterMaxEntries {
		return "", nil, fmt.Errorf("%w: Craftsy lesson overflow", ErrInvalidMetadata)
	}
	if !brightcoveAccountID.MatchString(accountID) {
		return "", nil, fmt.Errorf("%w: invalid Craftsy Brightcove account", ErrInvalidMetadata)
	}
	return accountID, lessons, nil
}

func parseCraftsyURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "craftsy.com" && host != "www.craftsy.com" {
		return "", false
	}
	match := craftsyClassPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// TVO extracts Brightcove ref IDs via the documented GraphQL endpoint.
type TVO struct{}

func NewTVO() TVO        { return TVO{} }
func (TVO) Name() string { return "tvo" }

func (TVO) Suitable(parsed *url.URL) bool {
	_, ok := parseTVOURL(parsed)
	return ok
}

func (TVO) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseTVOURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	path := strings.TrimSuffix(parsed.EscapedPath(), "/")
	body, err := json.Marshal(map[string]any{
		"operationName": "getVideo",
		"variables":     map[string]string{"slug": path},
		"query":         "query getVideo($slug: String){getTVOOrgVideo(slug:$slug){videoSource{brightcoveRefId}}}",
	})
	if err != nil {
		return Extraction{}, fmt.Errorf("%w: TVO request", ErrInvalidMetadata)
	}
	var payload struct {
		Data struct {
			GetTVOOrgVideo *struct {
				VideoSource struct {
					BrightcoveRefID string `json:"brightcoveRefId"`
				} `json:"videoSource"`
			} `json:"getTVOOrgVideo"`
		} `json:"data"`
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if err := hostedRequestJSON(ctx, request.Transport, http.MethodPost, "https://hmy0rc1bo2.execute-api.ca-central-1.amazonaws.com/graphql", body, headers, &payload); err != nil {
		return Extraction{}, err
	}
	if payload.Data.GetTVOOrgVideo == nil || payload.Data.GetTVOOrgVideo.VideoSource.BrightcoveRefID == "" {
		return Extraction{}, fmt.Errorf("%w: missing TVO Brightcove id", ErrInvalidMetadata)
	}
	ref := payload.Data.GetTVOOrgVideo.VideoSource.BrightcoveRefID
	if !strings.HasPrefix(ref, "ref:") && !brightcoveDigitsID.MatchString(ref) {
		ref = "ref:" + ref
	}
	_ = slug
	return brightcoveURLResult(tvoBrightcoveAccount, "default", ref)
}

func parseTVOURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tvo.org" && host != "www.tvo.org" {
		return "", false
	}
	match := tvoVideoPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// TVAPlus extracts Brightcove video IDs from tvaplus.ca Next.js pages.
type TVAPlus struct{}

func NewTVAPlus() TVAPlus    { return TVAPlus{} }
func (TVAPlus) Name() string { return "tva" }

func (TVAPlus) Suitable(parsed *url.URL) bool {
	_, ok := parseTVAPlusURL(parsed)
	return ok
}

func (TVAPlus) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	entityID, ok := parseTVAPlusURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.tvaplus.ca" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: TVA+ page too large", ErrInvalidMetadata)
	}
	raw, err := extractJSONObjectAfter(page, tvaNextData)
	if err != nil {
		return Extraction{}, classifyMissingMediaPage(page, "TVA+ Next data")
	}
	var payload struct {
		Props struct {
			PageProps struct {
				StaticEntity struct {
					VideoID string `json:"videoId"`
				} `json:"staticEntity"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid TVA+ Next data", ErrInvalidMetadata)
	}
	if !brightcoveDigitsID.MatchString(payload.Props.PageProps.StaticEntity.VideoID) {
		return Extraction{}, fmt.Errorf("%w: missing TVA+ Brightcove id", ErrInvalidMetadata)
	}
	_ = entityID
	return brightcoveURLResult(tvaPlusBrightcoveAccount, "default", payload.Props.PageProps.StaticEntity.VideoID)
}

func parseTVAPlusURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "tvaplus.ca" && host != "www.tvaplus.ca" {
		return "", false
	}
	match := tvaPlusPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func classifyMissingMediaPage(page []byte, what string) error {
	lower := strings.ToLower(string(page))
	if strings.Contains(lower, "sign in") || strings.Contains(lower, "log in") || strings.Contains(lower, "subscribe") {
		return ErrAuthentication
	}
	if strings.Contains(lower, "not found") || strings.Contains(lower, "404") {
		return ErrUnavailable
	}
	return fmt.Errorf("%w: missing %s", ErrInvalidMetadata, what)
}

func htmlUnescapeAttr(raw string) string {
	replacer := strings.NewReplacer(
		"&quot;", `"`,
		"&#34;", `"`,
		"&amp;", "&",
		"&#39;", "'",
		"&lt;", "<",
		"&gt;", ">",
	)
	return replacer.Replace(raw)
}
