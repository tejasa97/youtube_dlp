package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhtml "html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tejasa97/ytdlp-go/internal/value"
	xhtml "golang.org/x/net/html"
)

const (
	svtPageMaxURLBytes    = 4 << 10
	svtPageMaxHTMLBytes   = 4 << 20
	svtPageMaxStateBytes  = 2 << 20
	svtPageMaxTitleBytes  = 512
	svtPageMaxSlugBytes   = 256
	svtPageMaxEntries     = 256
	svtPageMaxJSONDepth   = 64
	svtPageMaxJSONNodes   = 100_000
	svtPageJSONStartSlack = 32
	svtPageMaxStateBlocks = 8
	svtPageMaxDataBlocks  = 64
)

var (
	ErrSVTPageNetwork  = errors.New("SVT page network failure")
	svtPageStateMarker = regexp.MustCompile(`\burqlState\s*[=:]`)
)

type svtPageTarget struct {
	id        string
	canonical string
}

func classifySVTPageURL(parsed *url.URL) (svtPageTarget, bool) {
	if parsed == nil || len(parsed.String()) == 0 || len(parsed.String()) > svtPageMaxURLBytes ||
		parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery {
		return svtPageTarget{}, false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "svt.se" && host != "www.svt.se" {
		return svtPageTarget{}, false
	}
	escapedPath := parsed.EscapedPath()
	lowerPath := strings.ToLower(escapedPath)
	if !strings.HasPrefix(escapedPath, "/") || strings.HasPrefix(escapedPath, "//") ||
		strings.HasSuffix(escapedPath, "/") || strings.Contains(lowerPath, "%2f") ||
		strings.Contains(lowerPath, "%5c") || strings.Contains(lowerPath, "%00") {
		return svtPageTarget{}, false
	}
	decodedPath, err := url.PathUnescape(escapedPath)
	if err != nil || !utf8.ValidString(decodedPath) || strings.Contains(decodedPath, "\\") {
		return svtPageTarget{}, false
	}
	parts := strings.Split(strings.TrimPrefix(decodedPath, "/"), "/")
	if len(parts) == 0 {
		return svtPageTarget{}, false
	}
	if len(parts) >= 2 && parts[0] == "barnkanalen" && parts[1] == "barnplay" {
		return svtPageTarget{}, false
	}
	for _, part := range parts {
		if !validSVTPagePathPart(part) {
			return svtPageTarget{}, false
		}
	}
	id := parts[len(parts)-1]
	return svtPageTarget{
		id:        id,
		canonical: "https://" + host + escapedPath,
	}, true
}

func validSVTPagePathPart(part string) bool {
	if part == "" || part == "." || part == ".." || len(part) > svtPageMaxSlugBytes {
		return false
	}
	for _, character := range part {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func extractSVTPagePlaylist(ctx context.Context, request Request, target svtPageTarget) (Extraction, error) {
	page, err := requestSVTPage(ctx, request.Transport, target.canonical)
	if err != nil {
		return Extraction{}, categorizeSVTPageError(err)
	}
	title, entries, err := parseSVTPage(ctx, page)
	if err != nil {
		return Extraction{}, err
	}
	info := value.NewObject(
		value.Field{Key: "id", Value: value.String(target.id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(target.canonical)},
	)
	return Playlist(value.NewInfo(info), StaticEntries(entries...))
}

func requestSVTPage(ctx context.Context, transport Transport, rawURL string) ([]byte, error) {
	if transport == nil {
		return nil, ErrUnsupported
	}
	isolated, ok := transport.(CredentialIsolatedNoRedirectTransport)
	if !ok {
		return nil, ErrTransportIsolation
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, ErrInvalidMetadata
	}
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	response, err := isolated.DoWithoutCredentialsNoRedirect(ctx, request)
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil {
		return nil, ErrSVTPageNetwork
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, &HTTPStatusError{Code: response.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, svtPageMaxHTMLBytes+1))
	if err != nil {
		return nil, ErrSVTPageNetwork
	}
	if len(body) > svtPageMaxHTMLBytes {
		return nil, ErrJSONResponseTooLarge
	}
	return body, nil
}

func categorizeSVTPageError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrTransportIsolation) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrPlaylistLimit) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusUnavailableForLegalReasons:
			return ErrRegionRestricted
		}
	}
	return ErrSVTPageNetwork
}

func parseSVTPage(ctx context.Context, page []byte) (string, []Entry, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if len(page) > svtPageMaxHTMLBytes {
		return "", nil, ErrJSONResponseTooLarge
	}
	title := svtPageTitle(page)
	if title == "" {
		return "", nil, fmt.Errorf("%w: missing SVT page title", ErrInvalidMetadata)
	}
	document, err := findSVTPageDocument(ctx, page)
	if err != nil {
		return "", nil, err
	}
	videoIDs, err := collectSVTPageVideoIDs(ctx, document)
	if err != nil {
		return "", nil, err
	}
	if len(videoIDs) == 0 {
		return "", nil, ErrUnavailable
	}
	entries := make([]Entry, 0, len(videoIDs))
	for _, videoID := range videoIDs {
		entries = append(entries, Entry{
			URL:          "svt:" + videoID,
			ExtractorKey: "region_svt",
			ID:           videoID,
			Title:        title,
			Transparent:  true,
		})
	}
	return title, entries, nil
}

func findSVTPageDocument(ctx context.Context, page []byte) (any, error) {
	matches := svtPageStateMarker.FindAllIndex(page, svtPageMaxStateBlocks+1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: missing SVT page state", ErrInvalidMetadata)
	}
	if len(matches) > svtPageMaxStateBlocks {
		return nil, fmt.Errorf("%w: SVT page state block bound exceeded", ErrInvalidMetadata)
	}
	for _, match := range matches {
		rawState, _, err := extractJSONObjectFrom(page, match[1], svtPageJSONStartSlack)
		if err != nil {
			continue
		}
		if len(rawState) > svtPageMaxStateBytes {
			return nil, ErrJSONResponseTooLarge
		}
		var state any
		decoder := json.NewDecoder(bytes.NewReader(rawState))
		if err := decoder.Decode(&state); err != nil || ensureJSONEOF(decoder) != nil {
			continue
		}
		document, found, err := findSVTPageDocumentInState(ctx, state)
		if err != nil {
			return nil, err
		}
		if found {
			return document, nil
		}
	}
	return nil, fmt.Errorf("%w: missing SVT page data", ErrInvalidMetadata)
}

func findSVTPageDocumentInState(ctx context.Context, root any) (any, bool, error) {
	nodes, candidates, candidateBytes := 0, 0, 0
	var visit func(any, int) (any, bool, error)
	visit = func(current any, depth int) (any, bool, error) {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		nodes++
		if depth > svtPageMaxJSONDepth || nodes > svtPageMaxJSONNodes {
			return nil, false, fmt.Errorf("%w: SVT page state bound exceeded", ErrInvalidMetadata)
		}
		switch typed := current.(type) {
		case map[string]any:
			if raw, ok := typed["data"].(string); ok {
				candidates++
				candidateBytes += len(raw)
				if candidates > svtPageMaxDataBlocks || candidateBytes > svtPageMaxStateBytes {
					return nil, false, ErrJSONResponseTooLarge
				}
				var document any
				decoder := json.NewDecoder(strings.NewReader(raw))
				if decoder.Decode(&document) == nil && ensureJSONEOF(decoder) == nil {
					if object, ok := document.(map[string]any); ok {
						if _, ok := object["page"].(map[string]any); ok {
							return document, true, nil
						}
					}
				}
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if document, found, err := visit(typed[key], depth+1); found || err != nil {
					return document, found, err
				}
			}
		case []any:
			for _, item := range typed {
				if document, found, err := visit(item, depth+1); found || err != nil {
					return document, found, err
				}
			}
		}
		return nil, false, nil
	}
	return visit(root, 0)
}

func collectSVTPageVideoIDs(ctx context.Context, root any) ([]string, error) {
	document, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: invalid SVT page data root", ErrInvalidMetadata)
	}
	page, ok := document["page"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%w: missing SVT page object", ErrInvalidMetadata)
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	add := func(candidate any) error {
		id, ok := candidate.(string)
		if !ok || !svtIDPattern.MatchString(id) {
			return nil
		}
		if _, exists := seen[id]; exists {
			return nil
		}
		if len(ids) >= svtPageMaxEntries {
			return ErrPlaylistLimit
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
		return nil
	}
	if topMedia, ok := page["topMedia"].(map[string]any); ok {
		if err := add(topMedia["svtId"]); err != nil {
			return nil, err
		}
	}
	nodes := 0
	var visit func(any, int) error
	visit = func(current any, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		nodes++
		if depth > svtPageMaxJSONDepth || nodes > svtPageMaxJSONNodes {
			return fmt.Errorf("%w: SVT page data bound exceeded", ErrInvalidMetadata)
		}
		switch typed := current.(type) {
		case map[string]any:
			if video, ok := typed["video"].(map[string]any); ok {
				if err := add(video["svtId"]); err != nil {
					return err
				}
			}
			keys := make([]string, 0, len(typed))
			for key := range typed {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if err := visit(typed[key], depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, item := range typed {
				if err := visit(item, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if body, exists := page["body"]; exists {
		if err := visit(body, 0); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

func svtPageTitle(page []byte) string {
	tokenizer := xhtml.NewTokenizer(bytes.NewReader(page))
	for {
		switch tokenizer.Next() {
		case xhtml.ErrorToken:
			return ""
		case xhtml.StartTagToken, xhtml.SelfClosingTagToken:
			token := tokenizer.Token()
			if !strings.EqualFold(token.Data, "meta") {
				continue
			}
			var property, content string
			for _, attribute := range token.Attr {
				switch strings.ToLower(attribute.Key) {
				case "property":
					property = attribute.Val
				case "content":
					content = attribute.Val
				}
			}
			if !strings.EqualFold(strings.TrimSpace(property), "og:title") {
				continue
			}
			title := strings.TrimSpace(stdhtml.UnescapeString(content))
			if title != "" && len(title) <= svtPageMaxTitleBytes && utf8.ValidString(title) {
				return title
			}
		}
	}
}
