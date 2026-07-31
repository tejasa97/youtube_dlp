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
	"os"
	"path"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type dailymotionDiscoveryFixtureTransport struct {
	mu             sync.Mutex
	tokenCalls     int
	tokenRequests  []*http.Request
	tokenForms     []string
	graphQLBodies  []json.RawMessage
	graphQLReqs    []*http.Request
	searchPages    map[int][]byte
	userPages      map[int][]byte
	playlistPages  map[int][]byte
	mediaBody      []byte
	metadataBodies map[string][]byte
	metadataStatus map[string]int
	tokenBody      []byte
	failToken      bool
	failGraphQL    int
	failGraphQL403 bool
	cancelAfter    int
	graphQLSeen    int
}

func readDailymotionDiscoveryFixture(t testing.TB, name string) []byte {
	t.Helper()
	body, err := os.ReadFile("../../conformance/extractors/dailymotion/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func dailymotionDiscoveryFixture(t testing.TB) *dailymotionDiscoveryFixtureTransport {
	t.Helper()
	return &dailymotionDiscoveryFixtureTransport{
		tokenBody:   readDailymotionDiscoveryFixture(t, "token.json"),
		searchPages: map[int][]byte{1: readDailymotionDiscoveryFixture(t, "search_page1.json"), 2: readDailymotionDiscoveryFixture(t, "search_page2.json")},
		userPages:   map[int][]byte{1: readDailymotionDiscoveryFixture(t, "user_page1.json"), 2: readDailymotionDiscoveryFixture(t, "user_page2.json")},
		playlistPages: map[int][]byte{
			1: readDailymotionDiscoveryFixture(t, "playlist_page1.json"),
		},
		mediaBody: readDailymotionDiscoveryFixture(t, "media.json"),
		metadataBodies: map[string][]byte{
			"xfixture": readPublicFixture(t, "dailymotion", "success.json"),
		},
		metadataStatus: make(map[string]int),
	}
}

func (transport *dailymotionDiscoveryFixtureTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return publicExtractorResponse(http.StatusNotFound, nil), nil
}

func (transport *dailymotionDiscoveryFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page read")
}

func (transport *dailymotionDiscoveryFixtureTransport) DoWithoutCredentialsNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if request.Header.Get(header) != "" {
			return publicExtractorResponse(http.StatusBadRequest, nil), nil
		}
	}
	if strings.HasPrefix(request.URL.String(), "https://www.dailymotion.com/player/metadata/video/") && request.Method == http.MethodGet {
		if request.URL.Query().Get("app") != "com.dailymotion.neon" || len(request.URL.Query()) != 1 {
			return publicExtractorResponse(http.StatusBadRequest, nil), nil
		}
		id := path.Base(request.URL.Path)
		if status := transport.metadataStatus[id]; status != 0 {
			return publicExtractorResponse(status, transport.metadataBodies[id]), nil
		}
		if body, ok := transport.metadataBodies[id]; ok {
			return publicExtractorResponse(http.StatusOK, body), nil
		}
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	transport.mu.Lock()
	transport.tokenCalls++
	bodyBytes, _ := io.ReadAll(request.Body)
	request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	cloned := request.Clone(context.Background())
	cloned.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	transport.tokenRequests = append(transport.tokenRequests, cloned)
	transport.tokenForms = append(transport.tokenForms, string(bodyBytes))
	transport.mu.Unlock()
	if request.URL.String() != dailymotionTokenEndpoint || request.Method != http.MethodPost {
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	if request.Header.Get("Origin") != dailymotionGraphQLOrigin {
		return publicExtractorResponse(http.StatusBadRequest, []byte(`{"error":"origin"}`)), nil
	}
	if transport.failToken {
		return publicExtractorResponse(http.StatusUnauthorized, []byte(`{"error_description":"denied"}`)), nil
	}
	return publicExtractorResponse(http.StatusOK, transport.tokenBody), nil
}

func (transport *dailymotionDiscoveryFixtureTransport) DoWithScopedAuthorizationNoRedirect(_ context.Context, request *http.Request) (*http.Response, error) {
	if request.URL.String() != dailymotionGraphQLEndpoint || request.Method != http.MethodPost {
		return publicExtractorResponse(http.StatusNotFound, nil), nil
	}
	if request.Header.Get("Origin") != dailymotionGraphQLOrigin || request.Header.Get("Content-Type") != "application/json" {
		return publicExtractorResponse(http.StatusBadRequest, nil), nil
	}
	if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer ") {
		return publicExtractorResponse(http.StatusUnauthorized, nil), nil
	}
	for _, header := range []string{"Cookie", "Proxy-Authorization", "Referer"} {
		if request.Header.Get(header) != "" {
			return publicExtractorResponse(http.StatusBadRequest, nil), nil
		}
	}
	body, err := io.ReadAll(request.Body)
	if err != nil {
		return nil, err
	}
	transport.mu.Lock()
	transport.graphQLBodies = append(transport.graphQLBodies, append(json.RawMessage(nil), body...))
	transport.graphQLReqs = append(transport.graphQLReqs, request.Clone(context.Background()))
	transport.graphQLSeen++
	seen := transport.graphQLSeen
	if transport.cancelAfter > 0 && seen > transport.cancelAfter {
		transport.mu.Unlock()
		return nil, context.Canceled
	}
	if transport.failGraphQL > 0 {
		transport.failGraphQL--
		transport.mu.Unlock()
		return publicExtractorResponse(http.StatusUnauthorized, nil), nil
	}
	if transport.failGraphQL403 {
		transport.mu.Unlock()
		return publicExtractorResponse(http.StatusForbidden, nil), nil
	}
	transport.mu.Unlock()
	var payload struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			Page int `json:"page"`
		} `json:"variables"`
		Query string `json:"query"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.OperationName == "SEARCH_QUERY" {
		page := payload.Variables.Page
		if page == 0 {
			page = 1
		}
		if fixture, ok := transport.searchPages[page]; ok {
			return publicExtractorResponse(http.StatusOK, fixture), nil
		}
		return publicExtractorResponse(http.StatusOK, []byte(`{"data":{"search":{"videos":{"edges":[]}}}}`)), nil
	}
	if strings.Contains(payload.Query, "media(xid:") {
		return publicExtractorResponse(http.StatusOK, transport.mediaBody), nil
	}
	if strings.Contains(payload.Query, "collection(xid:") {
		page := dailymotionFixtureQueryPage(payload.Query)
		if fixture, ok := transport.playlistPages[page]; ok {
			return publicExtractorResponse(http.StatusOK, fixture), nil
		}
		return publicExtractorResponse(http.StatusOK, []byte(`{"data":{"collection":{"videos":{"edges":[]}}}}`)), nil
	}
	if strings.Contains(payload.Query, "channel(xid:") {
		page := dailymotionFixtureQueryPage(payload.Query)
		if fixture, ok := transport.userPages[page]; ok {
			return publicExtractorResponse(http.StatusOK, fixture), nil
		}
		return publicExtractorResponse(http.StatusOK, []byte(`{"data":{"channel":{"videos":{"edges":[]}}}}`)), nil
	}
	return publicExtractorResponse(http.StatusBadRequest, []byte(`{"errors":[{"message":"unknown"}]}`)), nil
}

func dailymotionFixtureQueryPage(query string) int {
	page := 1
	if index := strings.LastIndex(query, "page: "); index >= 0 {
		var parsed int
		_, _ = fmt.Sscanf(query[index:], "page: %d", &parsed)
		if parsed > 0 {
			page = parsed
		}
	}
	return page
}

var (
	_ CredentialIsolatedNoRedirectTransport  = (*dailymotionDiscoveryFixtureTransport)(nil)
	_ ScopedAuthorizationNoRedirectTransport = (*dailymotionDiscoveryFixtureTransport)(nil)
)

func TestDailymotionDiscoveryRoutePrecedence(t *testing.T) {
	registry := NewRegistry(NewDailymotionPlaylist(), NewDailymotionSearch(), NewDailymotionUser(), NewDailymotion())
	for raw, want := range map[string]string{
		"https://www.dailymotion.com/search/king%20of%20turtles/videos": "dailymotion_search",
		"https://www.dailymotion.com/user/nqtv":                         "dailymotion_user",
		"https://www.dailymotion.com/nqtv":                              "dailymotion_user",
		"https://www.dailymotion.com/old/user/nqtv":                     "dailymotion_user",
		"https://dailymotion.fr/search/query/videos":                    "dailymotion_search",
		"https://dailymotion.de/user/channel":                           "dailymotion_user",
		"https://www.dailymotion.co/search/query/videos":                "dailymotion_search",
		"https://www.dailymotion.com/video/xfixture":                    "dailymotion",
		"https://www.dailymotion.com/playlist/xfixture":                 "dailymotion_playlist",
		"https://dai.ly/xfixture":                                       "dailymotion",
	} {
		selected, err := registry.Select(raw)
		if err != nil || selected.Name() != want {
			t.Fatalf("Select(%q) = %v err=%v want %q", raw, selected, err, want)
		}
	}
}

func TestDailymotionPlaylistLazyReusablePaginationAndContract(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionPlaylist().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/playlist/xfixture_fixture-list/1#video=xfixture", Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if transport.tokenCalls != 0 || len(transport.graphQLBodies) != 0 {
		t.Fatalf("playlist fetched eagerly: token=%d graphql=%d", transport.tokenCalls, len(transport.graphQLBodies))
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, err := CollectEntries(context.Background(), result.Entries, dailymotionPlaylistMaxEntries)
		if err != nil || len(entries) != 2 || entries[0].ID != "xplaylist01" || entries[1].ID != "xplaylist02" {
			t.Fatalf("iteration=%d entries=%#v err=%v", iteration, entries, err)
		}
	}
	if transport.tokenCalls != 1 || len(transport.graphQLBodies) != 2 {
		t.Fatalf("token=%d graphql=%d", transport.tokenCalls, len(transport.graphQLBodies))
	}
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(transport.graphQLBodies[0], &payload); err != nil ||
		!strings.Contains(payload.Query, `collection(xid: "xfixture")`) ||
		!strings.Contains(payload.Query, "videos(allowExplicit: true, first: 100, page: 1)") {
		t.Fatalf("payload=%s err=%v", transport.graphQLBodies[0], err)
	}
	if _, ok := result.Info.Lookup("title").StringValue(); ok {
		t.Fatal("playlist extractor invented a title")
	}
}

func TestDailymotionPlaylistMalformedAndCancellation(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.playlistPages[1] = []byte(`{"data":{"collection":{"videos":{"edges":[{"node":{"xid":"xbad","url":"https://evil.invalid/video/xbad"}}]}}}}`)
	result, err := NewDailymotionPlaylist().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/playlist/xfixture", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, dailymotionPlaylistMaxEntries); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed=%v", err)
	}
	transport = dailymotionDiscoveryFixture(t)
	result, err = NewDailymotionPlaylist().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/playlist/xfixture", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := result.Entries.Iterator().Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
}

func TestDailymotionDiscoveryRouteRejects(t *testing.T) {
	for _, raw := range []string{
		"https://user@www.dailymotion.com/search/query/videos",
		"https://www.dailymotion.com:443/search/query/videos",
		"https://www.dailymotion.com/search/query/videos#frag",
		"https://www.dailymotion.com/search/query/videos?x=1",
		"https://www.dailymotion.com/search/query/extra/videos",
		"https://geo.dailymotion.com/search/query/videos",
		"https://www.dailymotion.com/search/%2fquery/videos",
		"https://www.dailymotion.com/video/nqtv",
		"https://www.dailymotion.com/playlist/xfixture",
		"https://www.dailymotion.com/embed/video/xfixture",
		"https://www.dailymotion.com/user/nqtv/extra",
		"https://www.dailymotion.com/search",
		"https://www.dailymotion.com/search/",
		"https://notdailymotion.com/nqtv",
		"https://www.dailymotion.com./user/nqtv",
		"https://www.evil-dailymotion.com/user/nqtv",
		"https://www.dailymotion.com.co/user/nqtv",
		"https://www.dailymotion.com/search/\xff/videos",
		"https://www.dailymotion.com/search/query%00/videos",
		"https://www.dailymotion.com/crawler/x",
		"https://www.dailymotion.com/player/x",
		"https://www.dailymotion.com/swf/x",
	} {
		parsed, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if NewDailymotionSearch().Suitable(parsed) || NewDailymotionUser().Suitable(parsed) {
			t.Fatalf("unexpectedly suitable %q", raw)
		}
	}
}

func TestDailymotionDiscoveryTokenRequestShape(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := result.Entries.Iterator().Next(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(transport.tokenRequests) != 1 {
		t.Fatalf("token requests=%d", len(transport.tokenRequests))
	}
	req := transport.tokenRequests[0]
	if req.Method != http.MethodPost || req.URL.String() != dailymotionTokenEndpoint {
		t.Fatalf("token request=%s %s", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" || req.Header.Get("Origin") != dailymotionGraphQLOrigin {
		t.Fatalf("headers=%v", req.Header)
	}
	if req.Header.Get("Referer") != "" {
		t.Fatalf("token Referer=%q, want no Referer", req.Header.Get("Referer"))
	}
	body, _ := io.ReadAll(req.Body)
	values, _ := url.ParseQuery(string(body))
	if len(transport.tokenForms) > 0 {
		values, _ = url.ParseQuery(transport.tokenForms[0])
	}
	if values.Get("client_id") != dailymotionAnonymousClientID ||
		values.Get("client_secret") != dailymotionAnonymousClientSecret ||
		values.Get("grant_type") != "client_credentials" {
		t.Fatalf("form=%v", values)
	}
}

func TestDailymotionDiscoveryGraphQLRequestShape(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/king%20of%20turtles/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries); err != nil {
		t.Fatal(err)
	}
	if len(transport.graphQLReqs) == 0 {
		t.Fatal("missing graphql requests")
	}
	req := transport.graphQLReqs[0]
	if req.Method != http.MethodPost || req.URL.String() != dailymotionGraphQLEndpoint {
		t.Fatalf("graphql request=%s %s", req.Method, req.URL)
	}
	if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Origin") != dailymotionGraphQLOrigin {
		t.Fatalf("headers=%v", req.Header)
	}
	if req.Header.Get("Referer") != "" {
		t.Fatalf("GraphQL Referer=%q, want no Referer", req.Header.Get("Referer"))
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer fixture-") {
		t.Fatalf("authorization=%q", req.Header.Get("Authorization"))
	}
	var payload struct {
		OperationName string `json:"operationName"`
		Variables     struct {
			Query string `json:"query"`
			Page  int    `json:"page"`
			Limit int    `json:"limit"`
		} `json:"variables"`
	}
	if err := json.Unmarshal(transport.graphQLBodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	if payload.OperationName != "SEARCH_QUERY" || payload.Variables.Query != "king of turtles" ||
		payload.Variables.Page != 1 || payload.Variables.Limit != dailymotionSearchPageSize {
		t.Fatalf("payload=%#v", payload)
	}
}

func TestDailymotionGraphQLURLPolicy(t *testing.T) {
	for _, test := range []struct {
		rawURL string
		want   bool
	}{
		{rawURL: dailymotionGraphQLEndpoint, want: true},
		{rawURL: "https://graphql.api.dailymotion.com.evil.invalid/", want: false},
		{rawURL: "https://graphql.api.dailymotion.com:443/", want: false},
		{rawURL: "http://graphql.api.dailymotion.com/", want: false},
		{rawURL: "https://graphql.api.dailymotion.com/graphql", want: false},
		{rawURL: "https://graphql.api.dailymotion.com/?token=secret", want: false},
	} {
		if got := dailymotionGraphQLURLSafe(test.rawURL); got != test.want {
			t.Fatalf("dailymotionGraphQLURLSafe(%q)=%t want %t", test.rawURL, got, test.want)
		}
	}
}

func TestDailymotionSearchPaginationReuseAndRequests(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture%20query/videos", Transport: transport,
	})
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("extract err=%v playlist=%v", err, result.IsPlaylist())
	}
	if transport.tokenCalls != 0 {
		t.Fatalf("token fetched eagerly: calls=%d", transport.tokenCalls)
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
		if err != nil || len(entries) != 21 {
			t.Fatalf("iteration=%d entries=%d err=%v", iteration, len(entries), err)
		}
		if entries[0].URL != "https://www.dailymotion.com/video/xsearch01" || !entries[0].Transparent {
			t.Fatalf("entry=%#v", entries[0])
		}
	}
	if transport.tokenCalls != 1 {
		t.Fatalf("token calls=%d", transport.tokenCalls)
	}
	if len(transport.graphQLBodies) != 4 {
		t.Fatalf("graphql bodies=%d", len(transport.graphQLBodies))
	}
}

func TestDailymotionSearchFullPageMalformedEdgeFailsClosed(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[1] = readDailymotionDiscoveryFixture(t, "search_page1_bad_edge.json")
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionUserMalformedNodeFailsClosed(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.userPages[1] = []byte(`{"data":{"channel":{"videos":{"edges":[
		{"node":{"xid":"xuser01","url":"https://www.dailymotion.com/video/xuser01"}},
		{"node":{"xid":"xuser02","url":"https://evil.invalid/video/xuser02"}}
	]}}}}`)
	result, err := NewDailymotionUser().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/user/fixture-user", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionUserMaxEntries)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionUserNoInventedTitle(t *testing.T) {
	result, err := NewDailymotionUser().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/user/fixture-user", Transport: dailymotionDiscoveryFixture(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Info.Lookup("title").StringValue(); ok {
		t.Fatal("user playlist invented title")
	}
}

func TestDailymotionUserAllowExplicitDefault(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionUser().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/user/fixture-user", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, dailymotionUserMaxEntries); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(transport.graphQLBodies[0], &payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload.Query, "allowExplicit: true") {
		t.Fatalf("query=%q", payload.Query)
	}
}

func TestDailymotionDiscoveryTokenRefreshOn401Only(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.failGraphQL = 1
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries); err != nil {
		t.Fatal(err)
	}
	if transport.tokenCalls < 2 {
		t.Fatalf("token calls=%d", transport.tokenCalls)
	}
	transport = dailymotionDiscoveryFixture(t)
	transport.failGraphQL403 = true
	result, err = NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrAuthentication) || transport.tokenCalls != 1 {
		t.Fatalf("403 refresh err=%v tokenCalls=%d", err, transport.tokenCalls)
	}
}

func TestDailymotionDiscoveryTokenConfinementAndSecretSafeErrors(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.failToken = true
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = result.Entries.Iterator().Next(context.Background())
	if !errors.Is(err, ErrAuthentication) {
		t.Fatalf("err=%v", err)
	}
	for _, secret := range []string{dailymotionAnonymousClientSecret, "fixture-dailymotion-token", "denied"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestDailymotionDiscoveryCancellationBetweenPages(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.cancelAfter = 1
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, context.Canceled) || len(entries) < 20 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestDailymotionDiscoveryRegistryReentry(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	registry := NewRegistry(NewDailymotionSearch(), NewDailymotionUser(), NewDailymotion())
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok, err := result.Entries.Iterator().Next(context.Background())
	if err != nil || !ok {
		t.Fatalf("entry=%#v ok=%v err=%v", entry, ok, err)
	}
	selected, err := registry.SelectFor(entry.URL, entry.ExtractorKey)
	if err != nil || selected.Name() != "dailymotion" {
		t.Fatalf("reentry=%v err=%v", selected, err)
	}
}

func TestDailymotionDiscoveryTransportIsolation(t *testing.T) {
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: &publicExtractorTransport{},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = result.Entries.Iterator().Next(context.Background())
	if !errors.Is(err, ErrTransportIsolation) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionDiscoveryMalformedGraphQL(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[1] = []byte(`{"data":{"search":`)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, collectErr := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(collectErr, ErrInvalidMetadata) {
		t.Fatalf("collect=%v", collectErr)
	}
}

func TestDailymotionDiscoveryGraphQLErrorWithoutEcho(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[1] = []byte(`{"errors":[{"message":"secret-denied-token"}]}`)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret-denied-token") {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionDiscoveryNullSearchEnvelope(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[1] = []byte(`{"data":{"search":null}}`)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionDiscoveryEmptySearchEdgesAllowed(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[1] = []byte(`{"data":{"search":{"videos":{"edges":[]}}}}`)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if err != nil || len(entries) != 0 {
		t.Fatalf("entries=%d err=%v", len(entries), err)
	}
}

func TestDailymotionDiscoveryTerminalPartialPageEmitsAllEntries(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages = map[int][]byte{
		1: []byte(`{"data":{"search":{"videos":{"edges":[
			{"node":{"xid":"xpartial01"}},
			{"node":{"xid":"xpartial02"}},
			{"node":{"xid":"xpartial03"}}
		]}}}}`),
	}
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.tokenCalls != 0 {
		t.Fatalf("not lazy: token calls=%d", transport.tokenCalls)
	}
	entries, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if err != nil || len(entries) != 3 {
		t.Fatalf("entries=%#v err=%v", entries, err)
	}
	for i, want := range []string{"xpartial01", "xpartial02", "xpartial03"} {
		if entries[i].ID != want {
			t.Fatalf("entries[%d].ID=%q want %q", i, entries[i].ID, want)
		}
	}
}

func TestDailymotionDiscoveryRepeatedPageABACycle(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	pageA := transport.searchPages[1]
	pageB := []byte(`{"data":{"search":{"videos":{"edges":[
{"node":{"xid":"xbsearch01"}},{"node":{"xid":"xbsearch02"}},{"node":{"xid":"xbsearch03"}},{"node":{"xid":"xbsearch04"}},{"node":{"xid":"xbsearch05"}},
{"node":{"xid":"xbsearch06"}},{"node":{"xid":"xbsearch07"}},{"node":{"xid":"xbsearch08"}},{"node":{"xid":"xbsearch09"}},{"node":{"xid":"xbsearch10"}},
{"node":{"xid":"xbsearch11"}},{"node":{"xid":"xbsearch12"}},{"node":{"xid":"xbsearch13"}},{"node":{"xid":"xbsearch14"}},{"node":{"xid":"xbsearch15"}},
{"node":{"xid":"xbsearch16"}},{"node":{"xid":"xbsearch17"}},{"node":{"xid":"xbsearch18"}},{"node":{"xid":"xbsearch19"}},{"node":{"xid":"xbsearch20"}}
]}}}}`)
	transport.searchPages[2] = pageB
	transport.searchPages[3] = pageA
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionDiscoveryRepeatedPageDetection(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	transport.searchPages[2] = transport.searchPages[1]
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("err=%v", err)
	}
}

func TestDailymotionEntryFromNodeCanonicalOnly(t *testing.T) {
	entry, err := dailymotionEntryFromNode("xfixture", "https://www.dailymotion.com/video/xfixture_title")
	if err != nil || entry.URL != "https://www.dailymotion.com/video/xfixture" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if _, err := dailymotionEntryFromNode("xfixture", "https://evil.invalid/video/xfixture"); err == nil {
		t.Fatal("accepted hostile url")
	}
	if _, err := dailymotionEntryFromNode("xfixture", "https://www.notdailymotion.com/video/xfixture"); err == nil {
		t.Fatal("accepted lookalike host")
	}
}

func FuzzDailymotionSearchRoute(f *testing.F) {
	f.Add("https://www.dailymotion.com/search/query/videos")
	f.Add("https://www.dailymotion.com/video/x123")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		if NewDailymotionSearch().Suitable(parsed) && NewDailymotion().Suitable(parsed) {
			t.Fatalf("ambiguous route %q", raw)
		}
	})
}

func FuzzDailymotionUserRoute(f *testing.F) {
	f.Add("https://www.dailymotion.com/user/nqtv")
	f.Add("https://www.dailymotion.com/playlist/xfixture")
	f.Fuzz(func(t *testing.T, raw string) {
		if len(raw) > sharedHostingMaxURLBytes {
			t.Skip()
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			return
		}
		if NewDailymotionUser().Suitable(parsed) && NewDailymotion().Suitable(parsed) {
			t.Fatalf("ambiguous route %q", raw)
		}
	})
}

func TestDailymotionDiscoveryLazyFirstPage(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	if transport.tokenCalls != 0 || len(transport.graphQLBodies) != 0 {
		t.Fatalf("not lazy: token=%d graphql=%d", transport.tokenCalls, len(transport.graphQLBodies))
	}
	entry, ok, err := result.Entries.Iterator().Next(context.Background())
	if err != nil || !ok || entry.ID != "xsearch01" {
		t.Fatalf("entry=%#v ok=%v err=%v", entry, ok, err)
	}
}

func TestDailymotionDiscoveryIndependentIterators(t *testing.T) {
	transport := dailymotionDiscoveryFixture(t)
	result, err := NewDailymotionSearch().Extract(context.Background(), Request{
		URL: "https://www.dailymotion.com/search/fixture/videos", Transport: transport,
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	second, err2 := CollectEntries(context.Background(), result.Entries, dailymotionSearchMaxEntries)
	if err != nil || err2 != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("first=%#v second=%#v err=%v err2=%v", first, second, err, err2)
	}
}

func dailymotionDiscoveryTestEntries(prefix string, count int) []Entry {
	entries := make([]Entry, count)
	for i := range entries {
		id := fmt.Sprintf("%s%02d", prefix, i+1)
		entries[i] = Entry{URL: "https://www.dailymotion.com/video/" + id, ExtractorKey: "dailymotion", ID: id, Transparent: true}
	}
	return entries
}

func TestDailymotionDiscoverySequenceMaxEntriesNotDivisible(t *testing.T) {
	const pageSize = 10
	const maxEntries = 15
	fetches := 0
	sequence, err := newDailymotionDiscoverySequence(pageSize, 5, maxEntries, func(_ context.Context, page int) ([]Entry, bool, error) {
		fetches++
		return dailymotionDiscoveryTestEntries(fmt.Sprintf("xe%dp", page), pageSize), false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := sequence.Iterator()
	for i := 0; i < maxEntries; i++ {
		entry, ok, err := iterator.Next(context.Background())
		if err != nil || !ok {
			t.Fatalf("entry %d: ok=%v err=%v", i, ok, err)
		}
		if entry.ID == "" {
			t.Fatalf("entry %d empty", i)
		}
	}
	_, ok, err := iterator.Next(context.Background())
	if ok || !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("after cap: ok=%v err=%v", ok, err)
	}
	if fetches != 2 {
		t.Fatalf("fetches=%d want 2", fetches)
	}
}

func TestDailymotionDiscoverySequenceMaxPages(t *testing.T) {
	const pageSize = 5
	const maxPages = 2
	fetches := 0
	sequence, err := newDailymotionDiscoverySequence(pageSize, maxPages, 100, func(_ context.Context, page int) ([]Entry, bool, error) {
		fetches++
		return dailymotionDiscoveryTestEntries(fmt.Sprintf("xp%dp", page), pageSize), false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	iterator := sequence.Iterator()
	for i := 0; i < pageSize*maxPages; i++ {
		_, ok, err := iterator.Next(context.Background())
		if err != nil || !ok {
			t.Fatalf("entry %d: ok=%v err=%v", i, ok, err)
		}
	}
	_, ok, err := iterator.Next(context.Background())
	if ok || !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("after pages: ok=%v err=%v fetches=%d", ok, err, fetches)
	}
	if fetches != maxPages {
		t.Fatalf("fetches=%d want %d", fetches, maxPages)
	}
}

func TestDailymotionDiscoverySequenceOversizedPage(t *testing.T) {
	sequence, err := newDailymotionDiscoverySequence(5, 2, 100, func(_ context.Context, _ int) ([]Entry, bool, error) {
		return dailymotionDiscoveryTestEntries("xo", 6), false, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, ok, err := sequence.Iterator().Next(context.Background())
	if ok || !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
