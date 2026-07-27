// NHK for School extractors: bangumi/clip (NhkForSchoolBangumiIE),
// subject playlists (NhkForSchoolSubjectIE), and program lists
// (NhkForSchoolProgramListIE). The school pages are static; we parse bounded
// HTML/JS/JSON without any JavaScript runtime.
package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	nhkSchoolMaxPageBytes      = 2 << 20
	nhkSchoolMaxJSONBytes      = 4 << 20
	nhkSchoolMaxPrograms       = 4096
	nhkSchoolMaxChapters       = 1024
	nhkSchoolMaxThumbnail      = 32
	nhkSchoolProgramIDLen      = 256
	nhkSchoolBangumiIDLen      = 256
	nhkSchoolBangumiAkamaiPath = 256
)

const (
	nhkSchoolBangumiHost = "www2.nhk.or.jp"
	nhkSchoolListHost    = "www.nhk.or.jp"
)

var nhkSchoolSubjectAllowlist = map[string]bool{
	"rika":     true,
	"syakai":   true,
	"kokugo":   true,
	"sansuu":   true,
	"seikatsu": true,
	"doutoku":  true,
	"ongaku":   true,
	"taiiku":   true,
	"zukou":    true,
	"gijutsu":  true,
	"katei":    true,
	"sougou":   true,
	"eigo":     true,
	"tokkatsu": true,
	"tokushi":  true,
	"sonota":   true,
}

var nhkSchoolBangumiIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var nhkSchoolProgramIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// nhkSchoolBangumiTarget represents a School bangumi/clip URL after the
// route has accepted it.
type nhkSchoolBangumiTarget struct {
	host   string
	kind   string // "bangumi" or "clip"
	id     string
	webURL string
}

// NhkForSchoolBangumiIE extracts NHK for School bangumi and clip pages.
type NhkForSchoolBangumiIE struct{}

func NewNhkForSchoolBangumiIE() NhkForSchoolBangumiIE { return NhkForSchoolBangumiIE{} }
func (NhkForSchoolBangumiIE) Name() string            { return "nhk_for_school_bangumi" }
func (NhkForSchoolBangumiIE) Suitable(parsed *url.URL) bool {
	return nhkSchoolBangumiSuitable(parsed)
}
func (b NhkForSchoolBangumiIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkSchoolBangumi(ctx, request)
}

// NhkForSchoolSubjectIE extracts NHK for School subject playlists. The
// subject must be in the pinned allowlist.
type NhkForSchoolSubjectIE struct{}

func NewNhkForSchoolSubjectIE() NhkForSchoolSubjectIE { return NhkForSchoolSubjectIE{} }
func (NhkForSchoolSubjectIE) Name() string            { return "nhk_for_school_subject" }
func (NhkForSchoolSubjectIE) Suitable(parsed *url.URL) bool {
	return nhkSchoolSubjectSuitable(parsed)
}
func (NhkForSchoolSubjectIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkSchoolSubject(ctx, request)
}

// NhkForSchoolProgramListIE extracts NHK for School program lists. Program
// lists are bounded JSON-driven collections of bangumi IDs.
type NhkForSchoolProgramListIE struct{}

func NewNhkForSchoolProgramListIE() NhkForSchoolProgramListIE {
	return NhkForSchoolProgramListIE{}
}
func (NhkForSchoolProgramListIE) Name() string { return "nhk_for_school_program_list" }
func (NhkForSchoolProgramListIE) Suitable(parsed *url.URL) bool {
	return nhkSchoolProgramListSuitable(parsed)
}
func (NhkForSchoolProgramListIE) Extract(ctx context.Context, request Request) (Extraction, error) {
	return extractNhkSchoolProgramList(ctx, request)
}

func nhkSchoolBangumiSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if len(parsed.String()) > nhkMaxURLBytes {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkSchoolBangumiHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	id := parsed.Query().Get("das_id")
	if !nhkSchoolBangumiIDPattern.MatchString(id) || len(id) > nhkSchoolBangumiIDLen {
		return false
	}
	parts := splitPathSegments(parsed.Path)
	if len(parts) < 3 || parts[0] != "school" || parts[1] != "movie" {
		return false
	}
	if parts[2] != "bangumi.cgi" && parts[2] != "clip.cgi" {
		return false
	}
	return true
}

func extractNhkSchoolBangumi(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkSchoolBangumiSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	id := parsed.Query().Get("das_id")
	parts := splitPathSegments(parsed.Path)
	kind := strings.TrimSuffix(parts[2], ".cgi")
	target := nhkSchoolBangumiTarget{
		host:   nhkSchoolBangumiHost,
		kind:   kind,
		id:     id,
		webURL: nhkSchoolBangumiCanonical(parsed.Scheme, id, kind),
	}
	page, err := nhkSchoolFetchPage(ctx, request.Transport, target.webURL)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	return nhkSchoolParseBangumiPage(page, target)
}

func nhkSchoolBangumiCanonical(scheme, id, kind string) string {
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + nhkSchoolBangumiHost + "/school/movie/" + kind + ".cgi?das_id=" + url.QueryEscape(id)
}

func nhkSchoolFetchPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if !nhkSchoolBangumiAcceptsURL(rawURL) && !nhkSchoolListAcceptsURL(rawURL) {
		return nil, fmt.Errorf("%w: unsafe NHK School URL", ErrInvalidMetadata)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK School request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "text/html")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, nhkSchoolMaxPageBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkSchoolMaxPageBytes {
		return nil, fmt.Errorf("%w: NHK School page too large", ErrInvalidMetadata)
	}
	return data, nil
}

func nhkSchoolBangumiAcceptsURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > 4096 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	if parsed.Host != nhkSchoolBangumiHost {
		return false
	}
	return true
}

func nhkSchoolParseBangumiPage(page []byte, target nhkSchoolBangumiTarget) (Extraction, error) {
	if len(page) == 0 {
		return Extraction{}, fmt.Errorf("%w: empty NHK School page", ErrInvalidMetadata)
	}
	baseValues := nhkSchoolExtractQuotedVars(page)
	programValues := nhkSchoolExtractProgramObj(page)
	finalID := target.id
	version := baseValues["r_version"]
	if version == "" {
		version = programValues["version"]
	}
	if version != "" {
		prefix := strings.SplitN(target.id, "_", 2)[0]
		finalID = prefix + "_" + version
	}
	title := programValues["name"]
	duration := nhkSchoolParseDuration(baseValues["r_duration"])
	timestamp := nhkSchoolParseTimestamp(baseValues["r_upload"])
	chapters := nhkSchoolExtractChapters(page, duration)
	formatURL := nhkSchoolAkamaiURL(finalID)
	info := value.NewObject(value.Field{Key: "id", Value: value.String(finalID)})
	info.Set("extractor", value.String("nhk_for_school_bangumi"))
	info.Set("extractor_key", value.String("NhkForSchoolBangumiIE"))
	if title != "" {
		info.Set("title", value.String(nhkSchoolDecodeEntities(title)))
	}
	info.Set("webpage_url", value.String(target.webpageURL(target)))
	if duration > 0 {
		info.Set("duration", value.Float(duration))
	}
	if timestamp > 0 {
		info.Set("timestamp", value.Int(timestamp))
	}
	if len(chapters) > 0 {
		chapterList := make([]value.Value, 0, len(chapters))
		for _, chapter := range chapters {
			chapterList = append(chapterList, value.ObjectValue(chapter))
		}
		info.Set("chapters", value.List(chapterList...))
	}
	if formatURL == "" {
		return Extraction{}, fmt.Errorf("%w: NHK School bangumi URL could not be constructed", ErrInvalidMetadata)
	}
	info.Set("formats", value.List(value.ObjectValue(nhkSchoolFormat(formatURL))))
	return Media(value.NewInfo(info)), nil
}

func nhkSchoolExtractQuotedVars(page []byte) map[string]string {
	out := make(map[string]string)
	pattern := regexp.MustCompile(`var\s+([a-zA-Z_]+)\s*=\s*"([^"]*?)"`)
	scoped := page
	if len(scoped) > 256<<10 {
		scoped = scoped[:256<<10]
	}
	for _, match := range pattern.FindAllSubmatch(scoped, 1024) {
		out[string(match[1])] = string(match[2])
	}
	return out
}

func nhkSchoolExtractProgramObj(page []byte) map[string]string {
	out := make(map[string]string)
	// RE2 does not support backreferences; match quote characters explicitly.
	pattern := regexp.MustCompile(`(?:program|clip)Obj\.([a-zA-Z_]+)\s*=\s*(?:"([^"]*)"|'([^']*)')`)
	scoped := page
	if len(scoped) > 256<<10 {
		scoped = scoped[:256<<10]
	}
	for _, match := range pattern.FindAllSubmatch(scoped, 1024) {
		value := string(match[2])
		if value == "" {
			value = string(match[3])
		}
		out[string(match[1])] = value
	}
	return out
}

func nhkSchoolExtractChapters(page []byte, duration float64) []*value.Object {
	timePattern := regexp.MustCompile(`chapterTime\.push\('([0-9:]+?)'\)`)
	titlePattern := regexp.MustCompile(`<div class="cpTitle"><span>(scene\s*\d+)?</span>([^<]+?)</div>`)
	scoped := page
	if len(scoped) > 256<<10 {
		scoped = scoped[:256<<10]
	}
	timeMatches := timePattern.FindAllSubmatch(scoped, nhkSchoolMaxChapters+1)
	titleMatches := titlePattern.FindAllSubmatch(scoped, nhkSchoolMaxChapters+1)
	if len(timeMatches) == 0 || len(titleMatches) == 0 || len(timeMatches) != len(titleMatches) {
		return nil
	}
	if len(timeMatches) > nhkSchoolMaxChapters {
		return nil
	}
	starts := make([]float64, 0, len(timeMatches))
	titles := make([]string, 0, len(titleMatches))
	for index, match := range timeMatches {
		start := nhkSchoolParseDuration(string(match[1]))
		if index > 0 && start < starts[index-1] {
			return nil
		}
		if duration > 0 && start > duration {
			return nil
		}
		starts = append(starts, start)
		scene := string(titleMatches[index][1])
		name := nhkSchoolDecodeEntities(string(titleMatches[index][2]))
		title := strings.TrimSpace(strings.TrimSpace(scene) + " " + strings.TrimSpace(name))
		if title == "" {
			return nil
		}
		titles = append(titles, title)
	}
	objects := make([]*value.Object, 0, len(starts))
	for index, start := range starts {
		end := duration
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		object := value.NewObject(
			value.Field{Key: "title", Value: value.String(titles[index])},
			value.Field{Key: "start_time", Value: value.Float(start)},
		)
		if end > start {
			object.Set("end_time", value.Float(end))
		}
		objects = append(objects, object)
	}
	return objects
}

// nhkSchoolAkamaiURL constructs the HLS master URL for a School bangumi
// according to the pinned reference.
func nhkSchoolAkamaiURL(id string) string {
	if !nhkSchoolBangumiIDPattern.MatchString(id) {
		return ""
	}
	prefix := id
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	path := "/i/das/" + prefix + "/" + id + "_V_000.f4v/master.m3u8"
	if len(path) > nhkSchoolBangumiAkamaiPath {
		return ""
	}
	return "https://nhks-vh.akamaihd.net" + path
}

// webpageURL returns the canonical reference URL for a School bangumi.
func (t nhkSchoolBangumiTarget) webpageURL(_ nhkSchoolBangumiTarget) string {
	return nhkSchoolBangumiCanonical("https", t.id, t.kind)
}

// nhkSchoolDecodeEntities decodes a small set of HTML entities used by NHK
// School pages. It deliberately refuses to substitute any entity that would
// expand to control characters or backticks.
func nhkSchoolDecodeEntities(input string) string {
	if !strings.Contains(input, "&") {
		return input
	}
	var sb strings.Builder
	sb.Grow(len(input))
	for i := 0; i < len(input); {
		if input[i] != '&' {
			sb.WriteByte(input[i])
			i++
			continue
		}
		end := strings.IndexByte(input[i:], ';')
		if end <= 0 || end > 16 {
			sb.WriteByte('&')
			i++
			continue
		}
		entity := input[i : i+end+1]
		switch entity {
		case "&amp;":
			sb.WriteByte('&')
		case "&lt;":
			sb.WriteByte('<')
		case "&gt;":
			sb.WriteByte('>')
		case "&quot;":
			sb.WriteByte('"')
		case "&#39;":
			sb.WriteByte('\'')
		case "&apos;":
			sb.WriteByte('\'')
		case "&nbsp;":
			sb.WriteByte(' ')
		default:
			if strings.HasPrefix(entity, "&#") {
				body := entity[2 : len(entity)-1]
				if base := 10; strings.HasPrefix(entity, "&#x") || strings.HasPrefix(entity, "&#X") {
					body = entity[3 : len(entity)-1]
					if code, err := strconv.ParseInt(body, 16, 32); err == nil && code >= 32 && code < 0x10FFFF {
						sb.WriteRune(rune(code))
					} else {
						sb.WriteString(entity)
						i += end + 1
						continue
					}
				} else if code, err := strconv.ParseInt(body, base, 32); err == nil && code >= 32 && code < 0x10FFFF {
					sb.WriteRune(rune(code))
				} else {
					sb.WriteString(entity)
					i += end + 1
					continue
				}
			} else {
				sb.WriteString(entity)
				i += end + 1
				continue
			}
		}
		i += end + 1
	}
	return sb.String()
}

// nhkSchoolParseDuration parses durations in either seconds (numeric) or
// HH:MM:SS(.ms) format.
func nhkSchoolParseDuration(text string) float64 {
	if text == "" {
		return 0
	}
	if value, err := strconv.ParseFloat(text, 64); err == nil {
		return value
	}
	parts := strings.Split(text, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0
	}
	values := make([]float64, len(parts))
	for index, segment := range parts {
		parsed, err := strconv.ParseFloat(segment, 64)
		if err != nil {
			return 0
		}
		values[index] = parsed
	}
	switch len(values) {
	case 2:
		return values[0]*60 + values[1]
	case 3:
		return values[0]*3600 + values[1]*60 + values[2]
	}
	return 0
}

func nhkSchoolParseTimestamp(text string) int64 {
	if text == "" {
		return 0
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006/01/02"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.Unix()
		}
	}
	return 0
}

func nhkSchoolFormat(rawURL string) *value.Object {
	object := value.NewObject(value.Field{Key: "format_id", Value: value.String("hls")})
	object.Set("url", value.String(rawURL))
	object.Set("ext", value.String("mp4"))
	object.Set("protocol", value.String("m3u8_native"))
	return object
}

// --- Subject playlists ---

func nhkSchoolSubjectSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if len(parsed.String()) > nhkMaxURLBytes {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkSchoolListHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	parts := splitPathSegments(parsed.Path)
	if len(parts) < 2 || parts[0] != "school" {
		return false
	}
	subject := strings.ToLower(parts[1])
	if !nhkSchoolSubjectAllowlist[subject] {
		return false
	}
	for _, segment := range parts[2:] {
		if segment != "" {
			return false
		}
	}
	return true
}

// nhkSchoolSubjectNamePattern extracts the human-readable subject title from the
// bounded subjectName span. Hardcoded fallbacks are used only when parsing fails.
var nhkSchoolSubjectNamePattern = regexp.MustCompile(`(?s)<span\s+class="subjectName">\s*<img\s*[^>]+>\s*([^<]+?)</span>`)

func nhkSchoolSubjectTitleFromPage(page []byte, subject string) string {
	if match := nhkSchoolSubjectNamePattern.FindSubmatch(page); len(match) > 1 {
		title := strings.TrimSpace(nhkSchoolDecodeEntities(string(match[1])))
		if title != "" {
			return title
		}
	}
	return nhkSchoolSubjectTitleFallback(subject)
}

func nhkSchoolSubjectTitleFallback(subject string) string {
	titles := map[string]string{
		"rika":     "理科",
		"syakai":   "社会",
		"kokugo":   "国語",
		"sansuu":   "算数",
		"seikatsu": "生活",
		"doutoku":  "道徳",
		"ongaku":   "音楽",
		"taiiku":   "体育",
		"zukou":    "図工",
		"gijutsu":  "技術",
		"katei":    "家庭",
		"sougou":   "総合的な学習の時間",
		"eigo":     "英語",
		"tokkatsu": "特別活動",
		"tokushi":  "特設",
		"sonota":   "その他",
	}
	if title, ok := titles[strings.ToLower(subject)]; ok {
		return title
	}
	return ""
}

func nhkSchoolSubjectCanonical(scheme, subject string) string {
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + nhkSchoolListHost + "/school/" + subject + "/"
}

func extractNhkSchoolSubject(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkSchoolSubjectSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	parts := splitPathSegments(parsed.Path)
	subject := strings.ToLower(parts[1])
	pageURL := nhkSchoolSubjectCanonical(parsed.Scheme, subject)
	page, err := nhkSchoolFetchPage(ctx, request.Transport, pageURL)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	entries := nhkSchoolSubjectPrograms(page, parsed.Scheme, subject)
	if len(entries) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK School subject has no programs", ErrInvalidPlaylist)
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(subject)})
	info.Set("title", value.String(nhkSchoolSubjectTitleFromPage(page, subject)))
	info.Set("webpage_url", value.String(pageURL))
	info.Set("extractor", value.String("nhk_for_school_subject"))
	info.Set("extractor_key", value.String("NhkForSchoolSubjectIE"))
	sequence, err := nhkSchoolNewStaticSequence(entries)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(info), sequence)
}

// nhkSchoolSubjectPrograms scans a bounded subject page for child program
// links and returns deduplicated URL results targeting the Program List
// extractor. The page is treated as HTML; link hrefs are validated against
// the exact school host and the same subject.
func nhkSchoolSubjectPrograms(page []byte, scheme, subject string) []Entry {
	if scheme == "" {
		scheme = "https"
	}
	host := strings.ToLower(nhkSchoolListHost)
	seen := make(map[string]bool, 32)
	entries := make([]Entry, 0, 32)
	scoped := page
	if len(scoped) > 256<<10 {
		scoped = scoped[:256<<10]
	}
	linkPattern := regexp.MustCompile(`<a[^>]+href=("[^"]*"|'[^']*')`)
	for _, match := range linkPattern.FindAllSubmatch(scoped, -1) {
		if len(entries) >= nhkSchoolMaxPrograms {
			break
		}
		href := string(match[1])
		href = strings.Trim(href, `"'`)
		if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "javascript:") {
			continue
		}
		parsed, err := url.Parse(href)
		if err != nil {
			continue
		}
		if parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
			continue
		}
		if parsed.Host != "" && strings.ToLower(parsed.Host) != host {
			continue
		}
		pathParts := splitPathSegments(parsed.Path)
		if len(pathParts) < 3 || pathParts[0] != "school" || strings.ToLower(pathParts[1]) != subject {
			continue
		}
		program := pathParts[2]
		if !nhkSchoolProgramIDPattern.MatchString(program) {
			continue
		}
		base := &url.URL{Scheme: scheme, Host: host}
		resolved := base.ResolveReference(parsed).String()
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		entries = append(entries, Entry{
			URL:          resolved,
			ExtractorKey: "nhk_for_school_program_list",
			ID:           program,
			Title:        program,
		})
	}
	return entries
}

func nhkSchoolNewStaticSequence(entries []Entry) (EntrySequence, error) {
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: NHK School subject has no programs", ErrInvalidPlaylist)
	}
	return StaticEntries(entries...), nil
}

// --- Program lists ---

func nhkSchoolProgramListSuitable(parsed *url.URL) bool {
	if parsed == nil {
		return false
	}
	if len(parsed.String()) > nhkMaxURLBytes {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	if parsed.User != nil || parsed.Port() != "" {
		return false
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if strings.ToLower(parsed.Hostname()) != nhkSchoolListHost {
		return false
	}
	lowerPath := strings.ToLower(parsed.EscapedPath())
	if strings.ContainsAny(lowerPath, "\\\x00") ||
		strings.Contains(lowerPath, "%00") ||
		strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") {
		return false
	}
	parts := splitPathSegments(parsed.Path)
	if len(parts) < 3 || parts[0] != "school" {
		return false
	}
	subject := strings.ToLower(parts[1])
	if !nhkSchoolSubjectAllowlist[subject] {
		return false
	}
	if !nhkSchoolProgramIDPattern.MatchString(parts[2]) {
		return false
	}
	for _, segment := range parts[3:] {
		if segment != "" {
			return false
		}
	}
	return true
}

func nhkSchoolProgramListCanonical(scheme, subject, program string) string {
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + nhkSchoolListHost + "/school/" + subject + "/" + program + "/"
}

func nhkSchoolProgramJSONCanonical(scheme, subject, program string) string {
	if scheme == "" {
		scheme = "https"
	}
	return scheme + "://" + nhkSchoolListHost + "/school/" + subject + "/" + program + "/meta/program.json"
}

func extractNhkSchoolProgramList(ctx context.Context, request Request) (Extraction, error) {
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	if request.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return Extraction{}, ErrUnsupported
	}
	if !nhkSchoolProgramListSuitable(parsed) {
		return Extraction{}, ErrUnsupported
	}
	parts := splitPathSegments(parsed.Path)
	subject := strings.ToLower(parts[1])
	program := parts[2]
	pageURL := nhkSchoolProgramListCanonical(parsed.Scheme, subject, program)
	jsonURL := nhkSchoolProgramJSONCanonical(parsed.Scheme, subject, program)
	page, err := nhkSchoolFetchPage(ctx, request.Transport, pageURL)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	jsonBytes, err := nhkSchoolFetchJSON(ctx, request.Transport, jsonURL)
	if err != nil {
		return Extraction{}, err
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	programPayload, err := nhkSchoolParseProgramJSON(jsonBytes)
	if err != nil {
		return Extraction{}, err
	}
	title, description := nhkSchoolProgramListMetadata(page, programPayload)
	bangumiEntries := nhkSchoolProgramListBangumis(subject, program, parsed.Scheme, programPayload)
	info := value.NewObject(value.Field{Key: "id", Value: value.String(subject + "/" + program)})
	info.Set("extractor", value.String("nhk_for_school_program_list"))
	info.Set("extractor_key", value.String("NhkForSchoolProgramListIE"))
	if title != "" {
		info.Set("title", value.String(title))
	}
	if description != "" {
		info.Set("description", value.String(description))
	}
	info.Set("webpage_url", value.String(pageURL))
	if len(bangumiEntries) == 0 {
		return Extraction{}, fmt.Errorf("%w: NHK School program list has no items", ErrInvalidPlaylist)
	}
	sequence, err := nhkSchoolNewStaticSequence(bangumiEntries)
	if err != nil {
		return Extraction{}, err
	}
	return Playlist(value.NewInfo(info), sequence)
}

func nhkSchoolFetchJSON(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if !nhkSchoolBangumiAcceptsURL(rawURL) && !nhkSchoolListAcceptsURL(rawURL) {
		return nil, fmt.Errorf("%w: unsafe NHK School JSON URL", ErrInvalidMetadata)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid NHK School JSON request", ErrInvalidMetadata)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := transport.Do(ctx, req)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nhkCategorizeStatus(resp.StatusCode)
	}
	reader := io.LimitReader(resp.Body, nhkSchoolMaxJSONBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, nhkCategorizeError(err)
	}
	if int64(len(data)) > nhkSchoolMaxJSONBytes {
		return nil, fmt.Errorf("%w: NHK School JSON too large", ErrInvalidMetadata)
	}
	return data, nil
}

func nhkSchoolListAcceptsURL(rawURL string) bool {
	if len(rawURL) == 0 || len(rawURL) > 4096 {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return false
	}
	return parsed.Host == nhkSchoolListHost
}

func nhkSchoolParseProgramJSON(data []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("%w: empty NHK School program JSON", ErrInvalidMetadata)
	}
	var payload map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("%w: invalid NHK School program JSON", ErrInvalidMetadata)
	}
	if err := ensureJSONEOF(dec); err != nil {
		return nil, fmt.Errorf("%w: trailing NHK School program JSON", ErrInvalidMetadata)
	}
	return payload, nil
}

func nhkSchoolProgramListMetadata(page []byte, payload map[string]any) (title, description string) {
	title = nhkExtractHTMLMeta(page, "og:title")
	if title == "" {
		title = nhkSchoolProgramField(payload, "title")
	}
	title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(title), "| NHK for School"))
	description = nhkExtractHTMLMeta(page, "og:description")
	if description == "" {
		description = nhkSchoolProgramField(payload, "description")
	}
	return title, description
}

func nhkSchoolProgramField(payload map[string]any, key string) string {
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

// nhkExtractHTMLMeta finds the content attribute of the first <meta> tag with
// the supplied name/property. It is intentionally bounded and tolerant of
// malformed input.
func nhkExtractHTMLMeta(page []byte, target string) string {
	scoped := page
	if len(scoped) > 256<<10 {
		scoped = scoped[:256<<10]
	}
	pattern := regexp.MustCompile(`<meta[^>]+(?:name|property)=` + regexp.QuoteMeta("\""+target+"\"") + `[^>]*content=("[^"]*"|'[^']*')`)
	match := pattern.FindSubmatch(scoped)
	if match == nil {
		return ""
	}
	return strings.TrimSpace(strings.Trim(string(match[1]), `"'`))
}

func nhkSchoolProgramListBangumis(subject, program, scheme string, payload map[string]any) []Entry {
	partsRaw, ok := payload["part"].([]any)
	if !ok {
		return nil
	}
	if scheme == "" {
		scheme = "https"
	}
	seen := make(map[string]bool, len(partsRaw))
	entries := make([]Entry, 0, len(partsRaw))
	for _, raw := range partsRaw {
		if len(entries) >= nhkSchoolMaxPrograms {
			break
		}
		node, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		dasID, _ := node["part-video-dasid"].(string)
		if !nhkSchoolBangumiIDPattern.MatchString(dasID) {
			continue
		}
		title := strings.TrimSpace(fmt.Sprintf("%v", nhkSchoolProgramField(node, "part-video-title")))
		query := url.Values{}
		query.Set("das_id", dasID)
		encoded := query.Encode()
		resolved := scheme + "://" + nhkSchoolBangumiHost + "/school/movie/bangumi.cgi?" + encoded
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		entries = append(entries, Entry{
			URL:          resolved,
			ExtractorKey: "nhk_for_school_bangumi",
			ID:           dasID,
			Title:        title,
		})
	}
	return entries
}

// ensure that the canonical helpers compile in non-test builds even when
// unused elsewhere (they are exercised via fixtures and tests).
var _ = nhkSchoolSubjectTitleFallback
var _ = bytes.HasPrefix
var _ = errors.Is
