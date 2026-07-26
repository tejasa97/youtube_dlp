package extractor

// PRX is a small, self-contained CMS API backend. Optional malformed cards are
// skipped in playlists; the requested object identity and playable audio are
// required and therefore fail closed.

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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	prxAPI        = "https://cms.prx.org/api/v1/"
	prxMaxPages   = 1000
	prxMaxEntries = 10000
	prxMaxPieces  = 100
	prxMaxTags    = 100
)

var (
	prxRoute = regexp.MustCompile(`^/(stories|series|accounts)/([0-9]{1,16})/?$`)
	prxRowID = regexp.MustCompile(`^[0-9]{1,16}$`)
)

type PRXStory struct{}
type PRXSeries struct{}
type PRXAccount struct{}

func NewPRXStory() PRXStory                 { return PRXStory{} }
func NewPRXSeries() PRXSeries               { return PRXSeries{} }
func NewPRXAccount() PRXAccount             { return PRXAccount{} }
func (PRXStory) Name() string               { return "prx_story" }
func (PRXSeries) Name() string              { return "prx_series" }
func (PRXAccount) Name() string             { return "prx_account" }
func (PRXStory) Suitable(u *url.URL) bool   { k, _, ok := prxTarget(u); return ok && k == "stories" }
func (PRXSeries) Suitable(u *url.URL) bool  { k, _, ok := prxTarget(u); return ok && k == "series" }
func (PRXAccount) Suitable(u *url.URL) bool { k, _, ok := prxTarget(u); return ok && k == "accounts" }

func prxTarget(u *url.URL) (string, string, bool) {
	if u == nil || hostedRejectUnsafeURL(u) || u.Scheme != "https" || !prxPartQueryOK(u) {
		return "", "", false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if h != "prx.org" && h != "beta.prx.org" && h != "listen.prx.org" {
		return "", "", false
	}
	m := prxRoute.FindStringSubmatch(u.EscapedPath())
	if len(m) != 3 {
		return "", "", false
	}
	return m[1], m[2], true
}

// prx_part is an internal, canonical URL-result selector. It is the only
// accepted query, so public routing cannot turn this extractor into a proxy.
func prxPartQueryOK(u *url.URL) bool {
	if u.RawQuery == "" {
		return true
	}
	v, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(v) != 1 || len(v["prx_part"]) != 1 {
		return false
	}
	n, err := strconv.Atoi(v.Get("prx_part"))
	return err == nil && n > 0 && n <= 100
}
func prxPart(u *url.URL) int {
	if u == nil || u.RawQuery == "" {
		return 0
	}
	n, _ := strconv.Atoi(u.Query().Get("prx_part"))
	return n
}

type prxResource struct {
	ID          prxID       `json:"id"`
	Title       string      `json:"title"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Short       string      `json:"shortDescription"`
	Released    string      `json:"releasedAt"`
	Created     string      `json:"createdAt"`
	Updated     string      `json:"updatedAt"`
	Duration    json.Number `json:"duration"`
	Tags        []string    `json:"tags"`
	Episode     json.Number `json:"episodeIdentifier"`
	Season      json.Number `json:"seasonIdentifier"`
	Embedded    struct {
		Image   *prxImage     `json:"prx:image"`
		Account *prxResource  `json:"prx:account"`
		Series  *prxResource  `json:"prx:series"`
		Audio   *prxAudio     `json:"prx:audio"`
		Items   []prxResource `json:"prx:items"`
	} `json:"_embedded"`
	Links struct {
		Enclosure struct {
			Href string `json:"href"`
		} `json:"enclosure"`
	} `json:"_links"`
	Count json.Number `json:"count"`
	Total json.Number `json:"total"`
}
type prxImage struct {
	ID     prxID       `json:"id"`
	Size   json.Number `json:"size"`
	Width  json.Number `json:"width"`
	Height json.Number `json:"height"`
	Links  struct {
		Enclosure struct {
			Href string `json:"href"`
		} `json:"enclosure"`
	} `json:"_links"`
}
type prxAudio struct {
	Embedded struct {
		Items []prxPiece `json:"prx:items"`
	} `json:"_embedded"`
}
type prxPiece struct {
	ID          prxID       `json:"id"`
	Label       string      `json:"label"`
	Size        json.Number `json:"size"`
	Duration    json.Number `json:"duration"`
	Position    json.Number `json:"position"`
	ContentType string      `json:"contentType"`
	Frequency   json.Number `json:"frequency"`
	BitRate     json.Number `json:"bitRate"`
	Links       struct {
		Enclosure struct {
			Href string `json:"href"`
		} `json:"enclosure"`
	} `json:"_links"`
}

// prxID mirrors yt-dlp's str_or_none: both JSON strings and JSON numbers are
// accepted, while arrays, objects, booleans, and null are rejected.
type prxID string

func (id *prxID) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return fmt.Errorf("invalid PRX id")
	}
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*id = prxID(s)
		return nil
	}
	var n json.Number
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err := d.Decode(&n); err == nil {
		*id = prxID(n.String())
		return nil
	}
	return fmt.Errorf("invalid PRX id")
}

func prxGet(ctx context.Context, t Transport, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw := prxAPI + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ErrInvalidMetadata
	}
	isolate, ok := t.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return ErrTransportIsolation
	}
	r, err := isolate.DoWithoutCredentialsNoRedirect(ctx, req)
	if err != nil {
		return err
	}
	if r == nil || r.Body == nil {
		return fmt.Errorf("%w: empty PRX response", ErrInvalidMetadata)
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, maxExtractorJSONBytes+1))
	if err != nil {
		return errors.New("read PRX response failed")
	}
	if int64(len(data)) > maxExtractorJSONBytes {
		return ErrJSONResponseTooLarge
	}
	if r.StatusCode == 401 || r.StatusCode == 403 {
		return ErrAuthentication
	}
	if r.StatusCode == 404 || r.StatusCode == 410 {
		return ErrUnavailable
	}
	if r.StatusCode == 429 {
		return fmt.Errorf("PRX API rate limited")
	}
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return fmt.Errorf("PRX API unavailable")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	if err = d.Decode(out); err != nil {
		return fmt.Errorf("%w: invalid PRX response", ErrInvalidMetadata)
	}
	if err = ensureJSONEOF(d); err != nil {
		return fmt.Errorf("%w: trailing PRX response", ErrInvalidMetadata)
	}
	return ctx.Err()
}
func prxNumber(n json.Number) (int64, bool) {
	if n == "" {
		return 0, false
	}
	v, e := strconv.ParseInt(string(n), 10, 64)
	return v, e == nil && v >= 0
}
func prxTime(s string) int64 {
	for _, l := range []string{time.RFC3339, time.RFC3339Nano} {
		if v, e := time.Parse(l, s); e == nil {
			return v.Unix()
		}
	}
	return 0
}
func prxURL(s string) bool {
	u, e := url.Parse(s)
	return e == nil && u.Scheme == "https" && !hostedRejectUnsafeURL(u)
}
func prxInfo(r prxResource, kind string) (*value.Info, error) {
	if r.ID == "" {
		return nil, fmt.Errorf("%w: missing PRX id", ErrInvalidMetadata)
	}
	title := r.Title
	if kind == "accounts" && r.Name != "" {
		title = r.Name
	}
	if title == "" {
		title = string(r.ID)
	}
	o := value.NewObject(value.Field{Key: "id", Value: value.String(string(r.ID))}, value.Field{Key: "title", Value: value.String(title)})
	if r.Description != "" {
		o.Set("description", value.String(stripPRXHTML(r.Description)))
	} else if r.Short != "" {
		o.Set("description", value.String(r.Short))
	}
	for _, x := range []struct{ k, s string }{{"release_timestamp", r.Released}, {"timestamp", r.Created}, {"modified_timestamp", r.Updated}} {
		if v := prxTime(x.s); v > 0 {
			o.Set(x.k, value.Int(v))
		}
	}
	if v, ok := prxNumber(r.Duration); ok {
		o.Set("duration", value.Int(v))
	}
	if v, ok := prxNumber(r.Episode); ok {
		o.Set("episode_number", value.Int(v))
	}
	if v, ok := prxNumber(r.Season); ok {
		o.Set("season_number", value.Int(v))
	}
	if len(r.Tags) > prxMaxTags {
		return nil, fmt.Errorf("%w: too many PRX tags", ErrInvalidMetadata)
	}
	if len(r.Tags) > 0 {
		a := make([]value.Value, 0, len(r.Tags))
		for _, s := range r.Tags {
			a = append(a, value.String(s))
		}
		o.Set("tags", value.List(a...))
	}
	if r.Embedded.Image != nil {
		im := r.Embedded.Image
		if im.Links.Enclosure.Href != "" && !prxURL(im.Links.Enclosure.Href) {
			return nil, fmt.Errorf("%w: unsafe PRX image", ErrInvalidMetadata)
		}
		if im.Links.Enclosure.Href != "" {
			o.Set("thumbnail", value.String(im.Links.Enclosure.Href))
		}
	}
	if kind == "accounts" {
		o.Set("channel_id", value.String(string(r.ID)))
		o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+string(r.ID)))
		o.Set("channel", value.String(title))
	}
	if kind == "series" {
		o.Set("series_id", value.String(string(r.ID)))
		o.Set("series", value.String(title))
		if a := r.Embedded.Account; a != nil && a.ID != "" {
			n := a.Name
			if n == "" {
				n = a.Title
			}
			o.Set("channel_id", value.String(string(a.ID)))
			o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+string(a.ID)))
			o.Set("channel", value.String(n))
		}
	}
	if kind == "stories" {
		if s := r.Embedded.Series; s != nil && s.ID != "" {
			o.Set("series_id", value.String(string(s.ID)))
			o.Set("series", value.String(firstPRX(s.Title, string(s.ID))))
		}
		if a := r.Embedded.Account; a != nil && a.ID != "" {
			n := firstPRX(a.Name, a.Title, string(a.ID))
			o.Set("channel_id", value.String(string(a.ID)))
			o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+string(a.ID)))
			o.Set("channel", value.String(n))
		}
	}
	i := value.NewInfo(o)
	return &i, nil
}
func firstPRX(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

var prxHTML = regexp.MustCompile(`<[^>]*>`)

func stripPRXHTML(s string) string { return strings.TrimSpace(prxHTML.ReplaceAllString(s, "")) }

func (x PRXStory) Extract(ctx context.Context, req Request) (Extraction, error) {
	parsed := mustPRXURL(req.URL)
	k, id, ok := prxTarget(parsed)
	if !ok || k != "stories" || req.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	var r prxResource
	if e := prxGet(ctx, req.Transport, "stories/"+id, &r); e != nil {
		return Extraction{}, e
	}
	if string(r.ID) != id {
		return Extraction{}, fmt.Errorf("%w: PRX story identity mismatch", ErrInvalidMetadata)
	}
	info, e := prxInfo(r, "stories")
	if e != nil {
		return Extraction{}, e
	}
	pieces := r.Embedded.Audio
	if pieces == nil || len(pieces.Embedded.Items) == 0 {
		return Extraction{}, fmt.Errorf("%w: missing PRX audio", ErrUnavailable)
	}
	if len(pieces.Embedded.Items) > prxMaxPieces {
		return Extraction{}, fmt.Errorf("%w: too many PRX audio parts", ErrInvalidMetadata)
	}
	ps := append([]prxPiece(nil), pieces.Embedded.Items...)
	sort.SliceStable(ps, func(i, j int) bool {
		a, _ := prxNumber(ps[i].Position)
		b, _ := prxNumber(ps[j].Position)
		return a < b
	})
	formats := make([]value.Value, 0, len(ps))
	for _, p := range ps {
		ext := prxExt(p.ContentType)
		if p.ID == "" || ext == "" || !prxURL(p.Links.Enclosure.Href) {
			return Extraction{}, fmt.Errorf("%w: invalid PRX audio", ErrInvalidMetadata)
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String(string(p.ID))}, value.Field{Key: "format_note", Value: value.String(p.Label)}, value.Field{Key: "url", Value: value.String(p.Links.Enclosure.Href)}, value.Field{Key: "vcodec", Value: value.String("none")})
		if v, ok := prxNumber(p.Size); ok {
			f.Set("filesize", value.Int(v))
		}
		if v, ok := prxNumber(p.Duration); ok {
			f.Set("duration", value.Int(v))
		}
		if v, ok := prxNumber(p.BitRate); ok {
			f.Set("abr", value.Int(v))
		}
		if v, ok := prxNumber(p.Frequency); ok {
			f.Set("asr", value.Int(v/1000))
		}
		f.Set("ext", value.String(ext))
		formats = append(formats, value.ObjectValue(f))
	}
	part := prxPart(parsed)
	if part > len(formats) {
		return Extraction{}, fmt.Errorf("%w: invalid PRX part", ErrInvalidMetadata)
	}
	if len(formats) == 1 || part != 0 {
		if part != 0 {
			formats = formats[part-1 : part]
		}
		if part != 0 {
			info.Set("id", value.String(fmt.Sprintf("%s_part%d", id, part)))
		}
		info.Set("formats", value.List(formats...))
		info.Set("ext", value.String(prxExt(ps[partIndex(part)].ContentType)))
		return Media(*info), nil
	}
	entries := make([]Entry, len(ps))
	for i := range ps {
		entries[i] = Entry{URL: fmt.Sprintf("https://beta.prx.org/stories/%s?prx_part=%d", id, i+1), ExtractorKey: "prx_story", ID: fmt.Sprintf("%s_part%d", id, i+1), Title: firstPRX(r.Title, id)}
	}
	return Playlist(*info, StaticEntries(entries...))
}
func partIndex(part int) int {
	if part > 0 {
		return part - 1
	}
	return 0
}
func mustPRXURL(s string) *url.URL { u, _ := url.Parse(s); return u }
func prxExt(m string) string {
	switch strings.ToLower(strings.TrimSpace(strings.Split(m, ";")[0])) {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/aac":
		return "aac"
	case "audio/mp4", "audio/x-m4a":
		return "m4a"
	case "audio/flac", "audio/x-flac":
		return "flac"
	case "audio/ogg", "application/ogg":
		return "ogg"
	case "audio/wav", "audio/x-wav":
		return "wav"
	default:
		return ""
	}
}

func (x PRXSeries) Extract(ctx context.Context, req Request) (Extraction, error) {
	return prxCollection(ctx, req, "series")
}
func (x PRXAccount) Extract(ctx context.Context, req Request) (Extraction, error) {
	return prxCollection(ctx, req, "accounts")
}
func prxCollection(ctx context.Context, req Request, kind string) (Extraction, error) {
	k, id, ok := prxTarget(mustPRXURL(req.URL))
	if !ok || k != kind || req.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	var r prxResource
	if e := prxGet(ctx, req.Transport, kind+"/"+id, &r); e != nil {
		return Extraction{}, e
	}
	if string(r.ID) != id {
		return Extraction{}, fmt.Errorf("%w: PRX identity mismatch", ErrInvalidMetadata)
	}
	info, e := prxInfo(r, kind)
	if e != nil {
		return Extraction{}, e
	}
	endpoints := []string{kind + "/" + id + "/stories"}
	if kind == "accounts" {
		endpoints = []string{"accounts/" + id + "/series", "accounts/" + id + "/stories"}
	}
	return Playlist(*info, prxEntries{transport: req.Transport, endpoints: endpoints})
}

type prxEntries struct {
	transport Transport
	endpoints []string
}

func (s prxEntries) Iterator() EntryIterator { return &prxIterator{s: s, page: 1} }

type prxIterator struct {
	s                                            prxEntries
	endpoint, page, index, total, emitted, pages int
	items                                        []prxResource
	itemEndpoint                                 string
}

func (i *prxIterator) Next(ctx context.Context) (Entry, bool, error) {
	for {
		if e := ctx.Err(); e != nil {
			return Entry{}, false, e
		}
		if i.index < len(i.items) {
			r := i.items[i.index]
			i.index++
			if r.ID == "" || !prxRowID.MatchString(string(r.ID)) {
				continue
			}
			kind := "stories"
			key := "prx_story"
			if strings.Contains(i.itemEndpoint, "/series") {
				kind = "series"
				key = "prx_series"
			}
			return Entry{URL: "https://beta.prx.org/" + kind + "/" + string(r.ID), ExtractorKey: key, ID: string(r.ID), Title: firstPRX(r.Title, r.Name, string(r.ID))}, true, nil
		}
		if len(i.items) > 0 && i.index >= len(i.items) {
			i.items = nil
			i.itemEndpoint = ""
			continue
		}
		if i.endpoint >= len(i.s.endpoints) {
			return Entry{}, false, nil
		}
		if i.page > prxMaxPages || i.pages >= prxMaxPages || i.emitted >= prxMaxEntries {
			return Entry{}, false, fmt.Errorf("%w: PRX pagination overflow", ErrInvalidPlaylist)
		}
		var r prxResource
		e := prxGet(ctx, i.s.transport, i.s.endpoints[i.endpoint]+"?page="+strconv.Itoa(i.page)+"&per=100", &r)
		if e != nil {
			return Entry{}, false, e
		}
		if len(r.Embedded.Items) == 0 && r.Count == "" && r.Total == "" {
			i.endpoint++
			i.page = 1
			continue
		}
		count, ok := prxNumber(r.Count)
		total, ok2 := prxNumber(r.Total)
		if !ok || !ok2 || count > 100 || count > total || int(count) != len(r.Embedded.Items) || i.emitted+int(count) > prxMaxEntries {
			return Entry{}, false, fmt.Errorf("%w: invalid PRX pagination", ErrInvalidMetadata)
		}
		i.pages++
		i.emitted += int(count)
		i.items = r.Embedded.Items
		i.itemEndpoint = i.s.endpoints[i.endpoint]
		i.index = 0
		i.total += int(count)
		i.page++
		if len(i.items) == 0 || i.total >= int(total) {
			i.endpoint++
			i.page = 1
			i.total = 0
		}
	}
}
