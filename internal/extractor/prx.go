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
	prxMaxEntries = 100000
)

var prxRoute = regexp.MustCompile(`^/(stories|series|accounts)/([0-9]{1,16})/?$`)

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
	if u == nil || hostedRejectUnsafeURL(u) || u.Scheme != "https" || u.RawQuery != "" {
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

type prxResource struct {
	ID          string      `json:"id"`
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
	ID     string      `json:"id"`
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
	ID          string      `json:"id"`
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

func prxGet(ctx context.Context, t Transport, path string, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw := prxAPI + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return ErrInvalidMetadata
	}
	r, err := t.Do(ctx, req)
	if err != nil {
		return err
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
	if r.StatusCode == 404 {
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
		title = r.ID
	}
	o := value.NewObject(value.Field{Key: "id", Value: value.String(r.ID)}, value.Field{Key: "title", Value: value.String(title)})
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
		o.Set("channel_id", value.String(r.ID))
		o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+r.ID))
		o.Set("channel", value.String(title))
	}
	if kind == "series" {
		o.Set("series_id", value.String(r.ID))
		o.Set("series", value.String(title))
		if a := r.Embedded.Account; a != nil && a.ID != "" {
			n := a.Name
			if n == "" {
				n = a.Title
			}
			o.Set("channel_id", value.String(a.ID))
			o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+a.ID))
			o.Set("channel", value.String(n))
		}
	}
	if kind == "stories" {
		if s := r.Embedded.Series; s != nil && s.ID != "" {
			o.Set("series_id", value.String(s.ID))
			o.Set("series", value.String(firstPRX(s.Title, s.ID)))
		}
		if a := r.Embedded.Account; a != nil && a.ID != "" {
			n := firstPRX(a.Name, a.Title, a.ID)
			o.Set("channel_id", value.String(a.ID))
			o.Set("channel_url", value.String("https://beta.prx.org/accounts/"+a.ID))
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
	k, id, ok := prxTarget(mustPRXURL(req.URL))
	if !ok || k != "stories" || req.Transport == nil {
		return Extraction{}, ErrUnsupported
	}
	var r prxResource
	if e := prxGet(ctx, req.Transport, "stories/"+id, &r); e != nil {
		return Extraction{}, e
	}
	if r.ID != id {
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
	ps := append([]prxPiece(nil), pieces.Embedded.Items...)
	sort.SliceStable(ps, func(i, j int) bool {
		a, _ := prxNumber(ps[i].Position)
		b, _ := prxNumber(ps[j].Position)
		return a < b
	})
	formats := make([]value.Value, 0, len(ps))
	for _, p := range ps {
		if p.ID == "" || !prxURL(p.Links.Enclosure.Href) {
			return Extraction{}, fmt.Errorf("%w: invalid PRX audio", ErrInvalidMetadata)
		}
		f := value.NewObject(value.Field{Key: "format_id", Value: value.String(p.ID)}, value.Field{Key: "format_note", Value: value.String(p.Label)}, value.Field{Key: "url", Value: value.String(p.Links.Enclosure.Href)}, value.Field{Key: "vcodec", Value: value.String("none")})
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
		f.Set("ext", value.String(prxExt(p.ContentType)))
		formats = append(formats, value.ObjectValue(f))
	}
	if len(formats) == 1 {
		info.Set("formats", value.List(formats...))
		info.Set("ext", value.String(prxExt(ps[0].ContentType)))
		return Media(*info), nil
	}
	entries := make([]Entry, len(ps))
	for i := range ps {
		entries[i] = Entry{URL: "https://beta.prx.org/stories/" + id, ExtractorKey: "prx_story", ID: fmt.Sprintf("%s_part%d", id, i+1), Title: firstPRX(r.Title, id)}
	}
	return Playlist(*info, StaticEntries(entries...))
}
func mustPRXURL(s string) *url.URL { u, _ := url.Parse(s); return u }
func prxExt(m string) string {
	m = strings.ToLower(m)
	if strings.Contains(m, "mpeg") {
		return "mp3"
	}
	if strings.Contains(m, "aac") {
		return "aac"
	}
	if strings.Contains(m, "ogg") {
		return "ogg"
	}
	return "mp3"
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
	if r.ID != id {
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
	s                            prxEntries
	endpoint, page, index, total int
	items                        []prxResource
}

func (i *prxIterator) Next(ctx context.Context) (Entry, bool, error) {
	for {
		if e := ctx.Err(); e != nil {
			return Entry{}, false, e
		}
		if i.index < len(i.items) {
			r := i.items[i.index]
			i.index++
			if r.ID == "" {
				continue
			}
			kind := "stories"
			key := "prx_story"
			if strings.Contains(i.s.endpoints[i.endpoint], "/series") {
				kind = "series"
				key = "prx_series"
			}
			return Entry{URL: "https://beta.prx.org/" + kind + "/" + r.ID, ExtractorKey: key, ID: r.ID, Title: firstPRX(r.Title, r.Name, r.ID)}, true, nil
		}
		if i.endpoint >= len(i.s.endpoints) {
			return Entry{}, false, nil
		}
		if i.page > prxMaxPages || i.total > prxMaxEntries {
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
		if !ok || !ok2 || count > 100 || count > total {
			return Entry{}, false, fmt.Errorf("%w: invalid PRX pagination", ErrInvalidMetadata)
		}
		i.items = r.Embedded.Items
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
