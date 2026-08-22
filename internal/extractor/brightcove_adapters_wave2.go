package extractor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

const (
	formula1BrightcoveAccount  = "6057949432001"
	formula1BrightcovePlayer   = "S1WMrhjlh"
	europeanTourDefaultAccount = "5136026580001"
	maoriTVBrightcoveAccount   = "1614493167001"
	maoriTVBrightcovePlayer    = "HJlhIQhQf"
	theStarBrightcoveAccount   = "794267642001"
	theSunDefaultAccount       = "5067014667001"
	wimbledonBrightcoveAccount = "3506358525001"
	usatodayDefaultAccount     = "29906170001"
	skyNewsAUAPIKey            = "6krsj3w249nk779d8fukqx9f"
	wave2MaxTitleBytes         = 512
	wave2MaxStringBytes        = 4096
)

var (
	formula1Path           = regexp.MustCompile(`(?i)^/en/latest/video\.[^.]+\.([0-9]{1,32})\.html$`)
	europeanTourPath       = regexp.MustCompile(`(?i)^/dpworld-tour/news/video/([^/&?#$]{1,256})/?$`)
	europeanTourBrightcove = regexp.MustCompile(`(?is)brightcove-player\s?video-id="([0-9]{1,32})".*?"ACCOUNT_ID":"([0-9]{1,32})"`)
	maoriTVPath            = regexp.MustCompile(`(?i)^/shows/(?:[^/]+/)+([^/?&#]{1,256})/?$`)
	maoriTVVideoID         = regexp.MustCompile(`(?i)data-main-video-id=["']([0-9]{1,32})`)
	theStarPath            = regexp.MustCompile(`(?i)^/(?:[^/]+/)*(.{1,512})\.html$`)
	theStarBrightcoveID    = regexp.MustCompile(`(?i)mainartBrightcoveVideoId["']?\s*:\s*["']?([0-9]{1,32})`)
	theSunPath             = regexp.MustCompile(`(?i)^/[^/]+/([0-9]{1,32})(?:/.*)?/?$`)
	theSunVideoTag         = regexp.MustCompile(`(?is)<video[^>]+data-video-id-pending=[^>]+>`)
	wimbledonPath          = regexp.MustCompile(`(?i)^/[A-Za-z0-9_]+/video/media/([0-9]{1,32})\.html$`)
	usatodayPath           = regexp.MustCompile(`(?i)^/(?:[^/]+/)*([^?/#]{1,512})/?$`)
	skyNewsAUPath          = regexp.MustCompile(`(?i)^/[^/]+/[^/]+/[^/]+/video/([a-z0-9]{1,128})/?$`)
	skyNewsAUEmbedCode     = regexp.MustCompile(`(?i)embedcode\s?=\s?"([0-9]{1,32}-[0-9]{1,32})"`)
	wave2OGTitle           = regexp.MustCompile(`(?is)<meta\b[^>]*(?:property|name)=["']og:title["'][^>]*content=["']([^"']{1,512})["']`)
	wave2OGTitleAlt        = regexp.MustCompile(`(?is)<meta\b[^>]*content=["']([^"']{1,512})["'][^>]*(?:property|name)=["']og:title["']`)
	wave2HTMLAttr          = regexp.MustCompile(`(?i)([a-z0-9:_-]{1,64})\s*=\s*["']([^"']{0,512})["']`)
)

// Formula1 routes formula1.com/en/latest/video.*.<id>.html to Brightcove without a page fetch.
type Formula1 struct{}

func NewFormula1() Formula1   { return Formula1{} }
func (Formula1) Name() string { return "formula1" }

func (Formula1) Suitable(parsed *url.URL) bool {
	_, ok := parseFormula1URL(parsed)
	return ok
}

func (Formula1) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseFormula1URL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	return brightcoveURLResult(formula1BrightcoveAccount, formula1BrightcovePlayer, videoID)
}

func parseFormula1URL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "formula1.com" && host != "www.formula1.com" {
		return "", false
	}
	match := formula1Path.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// EuropeanTour routes DP World Tour video pages to Brightcove.
type EuropeanTour struct{}

func NewEuropeanTour() EuropeanTour { return EuropeanTour{} }
func (EuropeanTour) Name() string   { return "europeantour" }

func (EuropeanTour) Suitable(parsed *url.URL) bool {
	_, ok := parseEuropeanTourURL(parsed)
	return ok
}

func (EuropeanTour) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	slug, ok := parseEuropeanTourURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	_ = slug
	canonical := wave2HTTPSCanonical(parsed)
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: European Tour page too large", ErrInvalidMetadata)
	}
	match := europeanTourBrightcove.FindSubmatch(page)
	if len(match) != 3 {
		return Extraction{}, classifyMissingMediaPage(page, "European Tour Brightcove player")
	}
	account := string(match[2])
	if account == "" {
		account = europeanTourDefaultAccount
	}
	if !brightcoveAccountID.MatchString(account) {
		return Extraction{}, fmt.Errorf("%w: invalid European Tour Brightcove account", ErrInvalidMetadata)
	}
	return brightcoveURLResult(account, "default", string(match[1]))
}

func parseEuropeanTourURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "europeantour.com" && host != "www.europeantour.com" {
		return "", false
	}
	match := europeanTourPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// MaoriTV routes Maori Television show pages to Brightcove.
type MaoriTV struct{}

func NewMaoriTV() MaoriTV    { return MaoriTV{} }
func (MaoriTV) Name() string { return "maoritv" }

func (MaoriTV) Suitable(parsed *url.URL) bool {
	_, ok := parseMaoriTVURL(parsed)
	return ok
}

func (MaoriTV) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseMaoriTVURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := wave2HTTPSCanonical(parsed)
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: MaoriTV page too large", ErrInvalidMetadata)
	}
	match := maoriTVVideoID.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "MaoriTV main video id")
	}
	_ = displayID
	return brightcoveURLResult(maoriTVBrightcoveAccount, maoriTVBrightcovePlayer, string(match[1]))
}

func parseMaoriTVURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "maoritelevision.com" && host != "www.maoritelevision.com" {
		return "", false
	}
	match := maoriTVPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// TheStar routes thestar.com article pages to Brightcove.
type TheStar struct{}

func NewTheStar() TheStar    { return TheStar{} }
func (TheStar) Name() string { return "thestar" }

func (TheStar) Suitable(parsed *url.URL) bool {
	_, ok := parseTheStarURL(parsed)
	return ok
}

func (TheStar) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseTheStarURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := wave2HTTPSCanonical(parsed)
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: The Star page too large", ErrInvalidMetadata)
	}
	match := theStarBrightcoveID.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "The Star Brightcove id")
	}
	_ = displayID
	return brightcoveURLResult(theStarBrightcoveAccount, "default", string(match[1]))
}

func parseTheStarURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "thestar.com" && host != "www.thestar.com" {
		return "", false
	}
	match := theStarPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || match[1] == "" {
		return "", false
	}
	return match[1], true
}

// TheSun collects ordered Brightcove embeds from Sun article pages.
type TheSun struct{}

func NewTheSun() TheSun     { return TheSun{} }
func (TheSun) Name() string { return "thesun" }

func (TheSun) Suitable(parsed *url.URL) bool {
	_, ok := parseTheSunURL(parsed)
	return ok
}

func (TheSun) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	articleID, ok := parseTheSunURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := theSunCanonicalURL(parsed)
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: The Sun page too large", ErrInvalidMetadata)
	}
	tags := theSunVideoTag.FindAllString(string(page), brightcoveAdapterMaxEntries+1)
	if len(tags) == 0 {
		return Extraction{}, classifyMissingMediaPage(page, "The Sun video embeds")
	}
	if len(tags) > brightcoveAdapterMaxEntries {
		return Extraction{}, fmt.Errorf("%w: The Sun playlist overflow", ErrInvalidMetadata)
	}
	entries := make([]Entry, 0, len(tags))
	for _, tag := range tags {
		attrs := wave2HTMLAttributes(tag)
		videoID := attrs["data-video-id-pending"]
		if !brightcoveDigitsID.MatchString(videoID) {
			return Extraction{}, fmt.Errorf("%w: invalid The Sun Brightcove video id", ErrInvalidMetadata)
		}
		account := attrs["data-account"]
		if account == "" {
			account = theSunDefaultAccount
		}
		if !brightcoveAccountID.MatchString(account) {
			return Extraction{}, fmt.Errorf("%w: invalid The Sun Brightcove account", ErrInvalidMetadata)
		}
		handoff, err := brightcoveURLResult(account, "default", videoID)
		if err != nil {
			return Extraction{}, err
		}
		entries = append(entries, *handoff.Redirect)
	}
	title := wave2PageTitle(page)
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(articleID)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(canonical)},
	))
	return Playlist(info, StaticEntries(entries...))
}

func parseTheSunURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	if !theSunHostAllowed(parsed.Hostname()) {
		return "", false
	}
	match := theSunPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func theSunHostAllowed(host string) bool {
	host = strings.ToLower(host)
	switch host {
	case "thesun.co.uk", "www.thesun.co.uk", "the-sun.co.uk", "www.the-sun.co.uk",
		"thesun.com", "www.thesun.com", "the-sun.com", "www.the-sun.com":
		return true
	default:
		return false
	}
}

func theSunCanonicalURL(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if host == "thesun.co.uk" || host == "the-sun.co.uk" || host == "thesun.com" || host == "the-sun.com" {
		host = "www." + host
	}
	return "https://" + host + parsed.EscapedPath()
}

// Wimbledon routes locale video pages through the related-content API to Brightcove.
type Wimbledon struct{}

func NewWimbledon() Wimbledon  { return Wimbledon{} }
func (Wimbledon) Name() string { return "wimbledon" }

func (Wimbledon) Suitable(parsed *url.URL) bool {
	_, ok := parseWimbledonURL(parsed)
	return ok
}

func (Wimbledon) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	videoID, ok := parseWimbledonURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	endpoint := "https://www.wimbledon.com/relatedcontent/rest/v2/wim_v1/en/content/wim_v1_" + videoID + "_en"
	var metadata struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		Metadata    struct {
			Duration hostingNumber `json:"duration"`
		} `json:"metadata"`
	}
	if err := hostedRequestJSONWithoutCredentialsNoRedirect(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &metadata); err != nil {
		return Extraction{}, err
	}
	entry := Entry{
		URL:          brightcovePlayerURL(wimbledonBrightcoveAccount, "default", videoID),
		ExtractorKey: "brightcove",
		ID:           videoID,
		Transparent:  true,
	}
	if title := wave2BoundString(metadata.Title, wave2MaxTitleBytes); title != "" {
		entry.Title = title
	}
	if duration, ok := wave2ParseDuration(metadata.Metadata.Duration.string()); ok {
		entry.Duration = duration
		entry.HasDuration = true
	}
	return URLResult(entry)
}

func parseWimbledonURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "wimbledon.com" && host != "www.wimbledon.com" {
		return "", false
	}
	match := wimbledonPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

// USAToday routes article pages with ui-video-data JSON to Brightcove.
type USAToday struct{}

func NewUSAToday() USAToday   { return USAToday{} }
func (USAToday) Name() string { return "usatoday" }

func (USAToday) Suitable(parsed *url.URL) bool {
	_, ok := parseUSATodayURL(parsed)
	return ok
}

func (USAToday) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	displayID, ok := parseUSATodayURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	ajaxURL := wave2USATodayAjaxURL(parsed)
	page, _, err := request.Transport.ReadPage(ctx, ajaxURL)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: USA Today page too large", ErrInvalidMetadata)
	}
	raw, err := extractJSONObject(page, "ui-video-data")
	if err != nil {
		return Extraction{}, classifyMissingMediaPage(page, "USA Today ui-video-data")
	}
	var videoData struct {
		ID            json.Number `json:"id"`
		BrightcoveID  string      `json:"brightcove_id"`
		Title         string      `json:"title"`
		Thumbnail     string      `json:"thumbnail"`
		Description   string      `json:"description"`
		Length        string      `json:"length"`
		AssetMetadata struct {
			Items struct {
				BrightcoveAccount string `json:"brightcoveaccount"`
				BrightcoveID      string `json:"brightcoveid"`
			} `json:"items"`
		} `json:"asset_metadata"`
	}
	if err := json.Unmarshal(raw, &videoData); err != nil {
		return Extraction{}, fmt.Errorf("%w: invalid USA Today ui-video-data", ErrInvalidMetadata)
	}
	account := videoData.AssetMetadata.Items.BrightcoveAccount
	if account == "" {
		account = usatodayDefaultAccount
	}
	videoID := videoData.AssetMetadata.Items.BrightcoveID
	if videoID == "" {
		videoID = videoData.BrightcoveID
	}
	if !brightcoveAccountID.MatchString(account) || !brightcoveDigitsID.MatchString(videoID) {
		return Extraction{}, fmt.Errorf("%w: invalid USA Today Brightcove handoff", ErrInvalidMetadata)
	}
	entry := Entry{
		URL:          brightcovePlayerURL(account, "default", videoID),
		ExtractorKey: "brightcove",
		ID:           videoData.ID.String(),
		Transparent:  true,
	}
	if title := wave2BoundString(videoData.Title, wave2MaxTitleBytes); title != "" {
		entry.Title = title
	}
	if thumb := wave2BoundString(videoData.Thumbnail, wave2MaxStringBytes); thumb != "" && strictValidHostedHTTPURL(thumb) {
		entry.Thumbnail = thumb
	}
	if duration, ok := wave2ParseDuration(videoData.Length); ok {
		entry.Duration = duration
		entry.HasDuration = true
	}
	_ = displayID
	return URLResult(entry)
}

func parseUSATodayURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "usatoday.com" && host != "www.usatoday.com" {
		return "", false
	}
	match := usatodayPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 || match[1] == "" {
		return "", false
	}
	return match[1], true
}

func wave2USATodayAjaxURL(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if host == "usatoday.com" {
		host = "www.usatoday.com"
	}
	clone := *parsed
	clone.Scheme = "https"
	clone.Host = host
	clone.Fragment = ""
	clone.RawFragment = ""
	query := clone.Query()
	query.Set("ajax", "true")
	clone.RawQuery = query.Encode()
	return clone.String()
}

// SkyNewsAU routes skynews.com.au video pages through the News content API to Brightcove.
type SkyNewsAU struct{}

func NewSkyNewsAU() SkyNewsAU  { return SkyNewsAU{} }
func (SkyNewsAU) Name() string { return "skynewsau" }

func (SkyNewsAU) Suitable(parsed *url.URL) bool {
	_, ok := parseSkyNewsAUURL(parsed)
	return ok
}

func (SkyNewsAU) Extract(ctx context.Context, request Request) (Extraction, error) {
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	parsed, err := url.Parse(request.URL)
	if err != nil || request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	pageID, ok := parseSkyNewsAUURL(parsed)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	canonical := "https://www.skynews.com.au" + parsed.EscapedPath()
	page, _, err := request.Transport.ReadPage(ctx, canonical)
	if err != nil {
		return Extraction{}, err
	}
	if err := ctx.Err(); err != nil {
		return Extraction{}, err
	}
	if int64(len(page)) > maxExtractorJSONBytes {
		return Extraction{}, fmt.Errorf("%w: Sky News AU page too large", ErrInvalidMetadata)
	}
	match := skyNewsAUEmbedCode.FindSubmatch(page)
	if len(match) != 2 {
		return Extraction{}, classifyMissingMediaPage(page, "Sky News AU embedcode")
	}
	account, videoID, ok := wave2SplitBrightcoveEmbedCode(string(match[1]))
	if !ok {
		return Extraction{}, fmt.Errorf("%w: invalid Sky News AU embedcode", ErrInvalidMetadata)
	}
	endpoint := "https://content.api.news/v3/videos/brightcove/" + url.PathEscape(string(match[1])) + "?api_key=" + url.QueryEscape(skyNewsAUAPIKey)
	var payload struct {
		Content struct {
			Caption string `json:"caption"`
			Date    struct {
				Created string `json:"created"`
			} `json:"date"`
		} `json:"content"`
	}
	if err := hostedRequestJSONWithoutCredentialsNoRedirect(ctx, request.Transport, http.MethodGet, endpoint, nil, make(http.Header), &payload); err != nil {
		return Extraction{}, err
	}
	entry := Entry{
		URL:          brightcovePlayerURL(account, "default", videoID),
		ExtractorKey: "brightcove",
		ID:           pageID,
		Transparent:  true,
	}
	if title := wave2BoundString(payload.Content.Caption, wave2MaxTitleBytes); title != "" {
		entry.Title = title
	}
	if timestamp := wave2ParseUploadTimestamp(payload.Content.Date.Created); timestamp > 0 {
		entry.Timestamp = timestamp
		entry.HasTimestamp = true
	}
	return URLResult(entry)
}

func parseSkyNewsAUURL(parsed *url.URL) (string, bool) {
	if hostedRejectUnsafeURL(parsed) {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "skynews.com.au" && host != "www.skynews.com.au" {
		return "", false
	}
	match := skyNewsAUPath.FindStringSubmatch(parsed.EscapedPath())
	if len(match) != 2 {
		return "", false
	}
	return match[1], true
}

func wave2SplitBrightcoveEmbedCode(embedCode string) (account, videoID string, ok bool) {
	parts := strings.SplitN(embedCode, "-", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	if !brightcoveAccountID.MatchString(parts[0]) || !brightcoveDigitsID.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func wave2HTMLAttributes(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, match := range wave2HTMLAttr.FindAllStringSubmatch(tag, -1) {
		if len(match) != 3 {
			continue
		}
		attrs[strings.ToLower(match[1])] = htmlUnescapeAttr(match[2])
	}
	return attrs
}

func wave2PageTitle(page []byte) string {
	if match := wave2OGTitle.FindSubmatch(page); len(match) == 2 {
		return wave2BoundString(htmlUnescapeAttr(string(match[1])), wave2MaxTitleBytes)
	}
	if match := wave2OGTitleAlt.FindSubmatch(page); len(match) == 2 {
		return wave2BoundString(htmlUnescapeAttr(string(match[1])), wave2MaxTitleBytes)
	}
	return ""
}

func wave2BoundString(input string, maxBytes int) string {
	input = strings.TrimSpace(input)
	if input == "" || maxBytes <= 0 {
		return ""
	}
	if !utf8.ValidString(input) {
		return ""
	}
	if len(input) <= maxBytes {
		return input
	}
	for maxBytes > 0 && !utf8.RuneStart(input[maxBytes]) {
		maxBytes--
	}
	if maxBytes == 0 {
		return ""
	}
	return input[:maxBytes]
}

func wave2ParseDuration(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if strings.Contains(raw, ":") {
		parts := strings.Split(raw, ":")
		if len(parts) < 2 || len(parts) > 3 {
			return 0, false
		}
		multipliers := []float64{3600, 60, 1}
		start := 3 - len(parts)
		var total float64
		for index, part := range parts {
			value, err := parseWave2Float(part)
			if err != nil {
				return 0, false
			}
			total += value * multipliers[start+index]
		}
		if total <= 0 {
			return 0, false
		}
		return total, true
	}
	value, err := parseWave2Float(raw)
	if err != nil || value <= 0 {
		return 0, false
	}
	if value > 3600*100 {
		return value / 1000, true
	}
	return value, true
}

func parseWave2Float(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("empty")
	}
	var value float64
	_, err := fmt.Sscanf(raw, "%f", &value)
	return value, err
}

func wave2HTTPSCanonical(parsed *url.URL) string {
	host := strings.ToLower(parsed.Hostname())
	if host == "europeantour.com" || host == "maoritelevision.com" || host == "thestar.com" ||
		host == "skynews.com.au" || host == "usatoday.com" || host == "wimbledon.com" {
		host = "www." + host
	}
	return "https://" + host + parsed.EscapedPath()
}

func wave2ParseUploadTimestamp(raw string) int64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if unix := hostedUnixTimestamp(raw); unix > 0 {
		return unix
	}
	layouts := []string{time.RFC3339, "2006-01-02", "2006-01-02T15:04:05"}
	for _, layout := range layouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}
