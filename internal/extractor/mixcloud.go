package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const mixcloudGraphQLEndpoint = "https://app.mixcloud.com/graphql"
const mixcloudXORKey = "IFYOUWANTTHEARTISTSTOGETPAIDDONOTDOWNLOADFROMMIXCLOUD"

// Mixcloud uses its documented public GraphQL lookup route. Stream URL values
// are sometimes XOR/base64 obfuscated by the page API; decoding is local and
// never appears in errors, fixtures, or request logs.
type Mixcloud struct{}

func NewMixcloud() Mixcloud   { return Mixcloud{} }
func (Mixcloud) Name() string { return "mixcloud" }

var mixcloudSegment = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,200}$`)

func (Mixcloud) Suitable(u *url.URL) bool {
	if u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Port() != "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "mixcloud.com" && h != "www.mixcloud.com" && h != "m.mixcloud.com" && h != "beta.mixcloud.com" {
		return false
	}
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) == 2 {
		return mixcloudSegment.MatchString(p[0]) && mixcloudSegment.MatchString(p[1])
	}
	if len(p) == 1 {
		return mixcloudSegment.MatchString(p[0])
	}
	if len(p) == 3 && p[1] == "playlists" {
		return mixcloudSegment.MatchString(p[0]) && mixcloudSegment.MatchString(p[2])
	}
	return false
}

type mixcloudTarget struct{ username, slug, kind string }

func mixcloudParseTarget(u *url.URL) (mixcloudTarget, bool) {
	p := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(p) == 2 && p[1] != "uploads" && p[1] != "favorites" && p[1] != "listens" && p[1] != "stream" {
		return mixcloudTarget{username: p[0], slug: p[1], kind: "cloudcast"}, true
	}
	if len(p) == 1 {
		return mixcloudTarget{username: p[0], slug: "uploads", kind: "user"}, true
	}
	if len(p) == 2 {
		return mixcloudTarget{username: p[0], slug: p[1], kind: "user"}, true
	}
	if len(p) == 3 && p[1] == "playlists" {
		return mixcloudTarget{username: p[0], slug: p[2], kind: "playlist"}, true
	}
	return mixcloudTarget{}, false
}
func (Mixcloud) Extract(ctx context.Context, request Request) (Extraction, error) {
	u, err := url.Parse(request.URL)
	if err != nil || !NewMixcloud().Suitable(u) {
		return Extraction{}, ErrUnsupported
	}
	if err := contextError(ctx); err != nil {
		return Extraction{}, err
	}
	target, ok := mixcloudParseTarget(u)
	if !ok {
		return Extraction{}, ErrUnsupported
	}
	if target.kind == "cloudcast" {
		cast, err := mixcloudCloudcastRequest(ctx, request.Transport, target.username, target.slug)
		if err != nil {
			return Extraction{}, err
		}
		return normalizeMixcloudCloudcast(cast, target, "https://www.mixcloud.com/"+target.username+"/"+target.slug+"/")
	}
	collection, err := mixcloudCollectionRequest(ctx, request.Transport, target, "")
	if err != nil {
		return Extraction{}, err
	}
	return normalizeMixcloudCollection(ctx, request.Transport, target, collection)
}

type mixcloudCloudcast struct {
	ID               string  `json:"id"`
	Slug             string  `json:"slug"`
	URL              string  `json:"url"`
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	AudioLength      float64 `json:"audioLength"`
	PublishDate      string  `json:"publishDate"`
	IsExclusive      bool    `json:"isExclusive"`
	RestrictedReason string  `json:"restrictedReason"`
	Plays            int64   `json:"plays"`
	Owner            struct {
		DisplayName string `json:"displayName"`
		Username    string `json:"username"`
		URL         string `json:"url"`
	} `json:"owner"`
	Picture struct {
		URL string `json:"url"`
	} `json:"picture"`
	StreamInfo struct {
		URL     string `json:"url"`
		HLSURL  string `json:"hlsUrl"`
		DASHURL string `json:"dashUrl"`
	} `json:"streamInfo"`
}
type mixcloudGraphResponse struct {
	Data   map[string]json.RawMessage `json:"data"`
	Errors []json.RawMessage          `json:"errors"`
}

func mixcloudCloudcastRequest(ctx context.Context, t Transport, user, slug string) (mixcloudCloudcast, error) {
	query := `query { cloudcastLookup(lookup: {username: ` + mixcloudQuote(user) + `, slug: ` + mixcloudQuote(slug) + `}) { id slug url name description audioLength publishDate isExclusive restrictedReason plays owner { displayName username url } picture(width: 1024, height: 1024) { url } streamInfo { url hlsUrl dashUrl } } }`
	var r mixcloudGraphResponse
	if err := mixcloudRequest(ctx, t, query, &r); err != nil {
		return mixcloudCloudcast{}, err
	}
	raw := r.Data["cloudcastLookup"]
	if len(raw) == 0 || string(raw) == "null" {
		return mixcloudCloudcast{}, ErrUnavailable
	}
	var cast mixcloudCloudcast
	if json.Unmarshal(raw, &cast) != nil {
		return mixcloudCloudcast{}, fmt.Errorf("%w: malformed Mixcloud cloudcast", ErrInvalidMetadata)
	}
	return cast, nil
}
func mixcloudQuote(s string) string { encoded, _ := json.Marshal(s); return string(encoded) }
func mixcloudRequest(ctx context.Context, t Transport, query string, target any) error {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return fmt.Errorf("%w: Mixcloud query", ErrInvalidMetadata)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	headers.Set("Accept", "application/json")
	if err := RequestJSON(ctx, t, http.MethodPost, mixcloudGraphQLEndpoint, body, headers, target); err != nil {
		return categorizeMixcloudError(err)
	}
	if r, ok := target.(*mixcloudGraphResponse); ok && len(r.Errors) > 0 {
		return ErrUnavailable
	}
	return nil
}
func normalizeMixcloudCloudcast(c mixcloudCloudcast, target mixcloudTarget, webpage string) (Extraction, error) {
	if c.RestrictedReason != "" {
		reason := strings.ToLower(c.RestrictedReason)
		if strings.Contains(reason, "country") || strings.Contains(reason, "licens") {
			return Extraction{}, ErrRegionRestricted
		}
		if strings.Contains(reason, "login") || c.IsExclusive {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, ErrUnavailable
	}
	if c.Name == "" {
		return Extraction{}, fmt.Errorf("%w: missing Mixcloud title", ErrInvalidMetadata)
	}
	formats := make([]value.Value, 0, 3)
	for _, spec := range []struct{ id, raw, protocol string }{{"http", c.StreamInfo.URL, "https"}, {"hls", c.StreamInfo.HLSURL, "m3u8_native"}, {"dash", c.StreamInfo.DASHURL, "http_dash_segments"}} {
		stream := mixcloudStreamURL(spec.raw)
		if !validHTTPURL(stream) {
			continue
		}
		if spec.protocol == "m3u8_native" {
			formats = append(formats, value.ObjectValue(manifestFormat(spec.id, stream, spec.protocol)))
		} else if spec.protocol == "http_dash_segments" {
			formats = append(formats, value.ObjectValue(manifestFormat(spec.id, stream, spec.protocol)))
		} else {
			formats = append(formats, value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String(spec.id)}, value.Field{Key: "url", Value: value.String(stream)}, value.Field{Key: "ext", Value: value.String("m4a")}, value.Field{Key: "vcodec", Value: value.String("none")}, value.Field{Key: "protocol", Value: value.String("https")})))
		}
	}
	if len(formats) == 0 {
		if c.IsExclusive {
			return Extraction{}, ErrAuthentication
		}
		return Extraction{}, fmt.Errorf("%w: no Mixcloud formats", ErrUnavailable)
	}
	id := c.ID
	if id == "" {
		id = target.username + "_" + target.slug
	}
	info := value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(c.Name)}, value.Field{Key: "description", Value: value.String(c.Description)}, value.Field{Key: "uploader", Value: value.String(c.Owner.DisplayName)}, value.Field{Key: "uploader_id", Value: value.String(c.Owner.Username)}, value.Field{Key: "uploader_url", Value: value.String(c.Owner.URL)}, value.Field{Key: "webpage_url", Value: value.String(webpage)}, value.Field{Key: "ext", Value: value.String("m4a")}, value.Field{Key: "formats", Value: value.List(formats...)})
	if c.AudioLength > 0 {
		info.Set("duration", value.Float(c.AudioLength))
	}
	setPositiveInt(info, "view_count", c.Plays)
	if validHTTPURL(c.Picture.URL) {
		info.Set("thumbnail", value.String(c.Picture.URL))
	}
	if ts, err := time.Parse(time.RFC3339, c.PublishDate); err == nil {
		info.Set("timestamp", value.Int(ts.Unix()))
	}
	return Media(value.NewInfo(info)), nil
}
func mixcloudStreamURL(raw string) string {
	if validHTTPURL(raw) {
		return raw
	}
	cipher, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(cipher) == 0 {
		return ""
	}
	plain := make([]byte, len(cipher))
	for i, b := range cipher {
		plain[i] = b ^ mixcloudXORKey[i%len(mixcloudXORKey)]
	}
	candidate := string(plain)
	if validHTTPURL(candidate) {
		return candidate
	}
	return ""
}

type mixcloudConnection struct {
	Edges []struct {
		Node mixcloudCloudcast `json:"node"`
	} `json:"edges"`
	PageInfo struct {
		EndCursor   string `json:"endCursor"`
		HasNextPage bool   `json:"hasNextPage"`
	} `json:"pageInfo"`
}
type mixcloudCollection struct {
	Name        string             `json:"name"`
	DisplayName string             `json:"displayName"`
	Description string             `json:"description"`
	Uploads     mixcloudConnection `json:"uploads"`
	Favorites   mixcloudConnection `json:"favorites"`
	Listens     mixcloudConnection `json:"listens"`
	Stream      mixcloudConnection `json:"stream"`
	Items       mixcloudConnection `json:"items"`
}

func mixcloudCollectionRequest(ctx context.Context, t Transport, target mixcloudTarget, cursor string) (mixcloudCollection, error) {
	field := target.slug
	if target.kind == "playlist" {
		field = "items"
	}
	if target.kind == "user" && field == "" {
		field = "uploads"
	}
	after := ""
	if cursor != "" {
		after = `, after: ` + mixcloudQuote(cursor)
	}
	lookup := "userLookup"
	args := "username: " + mixcloudQuote(target.username)
	if target.kind == "playlist" {
		lookup = "playlistLookup"
		args += `, slug: ` + mixcloudQuote(target.slug)
	}
	query := `query { ` + lookup + `(lookup: {` + args + `}) { name displayName description ` + field + `(first: 100` + after + `) { edges { node { id name slug url owner { username displayName url } } } pageInfo { endCursor hasNextPage } } } }`
	var r mixcloudGraphResponse
	if err := mixcloudRequest(ctx, t, query, &r); err != nil {
		return mixcloudCollection{}, err
	}
	raw := r.Data[lookup]
	if len(raw) == 0 || string(raw) == "null" {
		return mixcloudCollection{}, ErrUnavailable
	}
	var collection mixcloudCollection
	if json.Unmarshal(raw, &collection) != nil {
		return mixcloudCollection{}, fmt.Errorf("%w: malformed Mixcloud collection", ErrInvalidMetadata)
	}
	return collection, nil
}
func normalizeMixcloudCollection(ctx context.Context, t Transport, target mixcloudTarget, c mixcloudCollection) (Extraction, error) {
	conn := mixcloudConnectionFor(c, target)
	entries := mixcloudEntries(conn)
	next := ""
	if conn.PageInfo.HasNextPage {
		next = conn.PageInfo.EndCursor
		if next == "" {
			return Extraction{}, fmt.Errorf("%w: missing Mixcloud cursor", ErrInvalidPlaylist)
		}
	}
	sequence, err := ContinuationEntries(entries, next, func(ctx context.Context, cursor string) ([]Entry, string, error) {
		page, err := mixcloudCollectionRequest(ctx, t, target, cursor)
		if err != nil {
			return nil, "", err
		}
		connection := mixcloudConnectionFor(page, target)
		nextCursor := ""
		if connection.PageInfo.HasNextPage {
			nextCursor = connection.PageInfo.EndCursor
			if nextCursor == "" {
				return nil, "", fmt.Errorf("%w: missing Mixcloud cursor", ErrInvalidPlaylist)
			}
		}
		return mixcloudEntries(connection), nextCursor, nil
	})
	if err != nil {
		return Extraction{}, err
	}
	id := target.username + "_" + target.slug
	title := firstMixcloudString(c.Name, c.DisplayName, id)
	return Playlist(value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String(id)}, value.Field{Key: "title", Value: value.String(title)}, value.Field{Key: "description", Value: value.String(c.Description)}, value.Field{Key: "webpage_url", Value: value.String("https://www.mixcloud.com/" + target.username + "/")})), sequence)
}
func mixcloudConnectionFor(c mixcloudCollection, t mixcloudTarget) mixcloudConnection {
	if t.kind == "playlist" {
		return c.Items
	}
	switch t.slug {
	case "favorites":
		return c.Favorites
	case "listens":
		return c.Listens
	case "stream":
		return c.Stream
	default:
		return c.Uploads
	}
}
func mixcloudEntries(c mixcloudConnection) []Entry {
	out := make([]Entry, 0, len(c.Edges))
	for _, e := range c.Edges {
		n := e.Node
		raw := n.URL
		if !validHTTPURL(raw) && n.Owner.Username != "" && mixcloudSegment.MatchString(n.Slug) {
			raw = "https://www.mixcloud.com/" + url.PathEscape(n.Owner.Username) + "/" + url.PathEscape(n.Slug) + "/"
		}
		if !validHTTPURL(raw) {
			continue
		}
		out = append(out, Entry{URL: raw, ExtractorKey: "mixcloud", ID: firstMixcloudString(n.ID, n.Owner.Username+"_"+n.Slug), Title: n.Name, Transparent: true})
	}
	return out
}
func firstMixcloudString(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}
func categorizeMixcloudError(err error) error {
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		case http.StatusTooManyRequests:
			return ErrUnavailable
		}
	}
	return err
}
