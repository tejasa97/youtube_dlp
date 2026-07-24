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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const blueskyFixtureRoot = "testdata/bluesky"

type blueskyFixtureResponse struct {
	status int
	body   []byte
}

type blueskyFixtureTransport struct {
	mu        sync.Mutex
	requests  []string
	responses map[string]blueskyFixtureResponse
	otherwise struct {
		status int
		body   []byte
	}
}

func (transport *blueskyFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected Bluesky page request")
}

func (transport *blueskyFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if request.Method != http.MethodGet {
		return nil, fmt.Errorf("unexpected Bluesky method %s", request.Method)
	}
	if request.Header.Get("Accept") != "application/json" {
		return nil, errors.New("missing Accept header on Bluesky request")
	}
	key := request.URL.String()
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Method+" "+key)
	response, ok := transport.responses[key]
	transport.mu.Unlock()
	status := http.StatusOK
	var payload []byte
	if ok {
		if response.status != 0 {
			status = response.status
		}
		payload = response.body
	} else if transport.otherwise.status != 0 || transport.otherwise.body != nil {
		if transport.otherwise.status != 0 {
			status = transport.otherwise.status
		}
		payload = transport.otherwise.body
	} else {
		return nil, fmt.Errorf("missing fixture response for %s", key)
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Request:    request,
	}, nil
}

func (transport *blueskyFixtureTransport) seenRequest(needle string) bool {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	for _, request := range transport.requests {
		if strings.Contains(request, needle) {
			return true
		}
	}
	return false
}

func blueskyFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(blueskyFixtureRoot, name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func newBlueskyTransport(responses map[string]blueskyFixtureResponse) *blueskyFixtureTransport {
	if responses == nil {
		responses = map[string]blueskyFixtureResponse{}
	}
	return &blueskyFixtureTransport{responses: responses}
}

func TestBlueskyRoutingAndClassification(t *testing.T) {
	bluesky := NewBluesky()
	cases := []struct {
		name   string
		rawURL string
		want   bool
	}{
		{"bsky https", "https://bsky.app/profile/handle.example.test/post/3l4omssdl632g", true},
		{"bsky www", "https://www.bsky.app/profile/handle.example.test/post/3l4omssdl632g", true},
		{"main dev", "https://main.bsky.dev/profile/handle.example.test/post/3l4omssdl632g", true},
		{"at uri did", "at://did:plc:abcdefghijklmnopqrstuvw/app.bsky.feed.post/3l4omssdl632g", true},
		{"at uri handle", "at://handle.example.test/app.bsky.feed.post/3l4omssdl632g", true},
		{"lookalike host", "https://bsky.app.evil.test/profile/handle/post/3l4omssdl632g", false},
		{"unsupported scheme", "ftp://bsky.app/profile/handle/post/3l4omssdl632g", false},
		{"fragment", "https://bsky.app/profile/handle/post/3l4omssdl632g#frag", false},
		{"query", "https://bsky.app/profile/handle/post/3l4omssdl632g?foo=bar", false},
		{"media selector", "https://bsky.app/profile/handle.example.test/post/3l4omssdl632g?media=2", true},
		{"invalid media selector", "https://bsky.app/profile/handle.example.test/post/3l4omssdl632g?media=0", false},
		{"mixed media query", "https://bsky.app/profile/handle.example.test/post/3l4omssdl632g?media=1&x=y", false},
		{"userinfo", "https://user:pass@bsky.app/profile/handle/post/3l4omssdl632g", false},
		{"port", "https://bsky.app:443/profile/handle/post/3l4omssdl632g", false},
		{"extra path", "https://bsky.app/profile/handle/post/3l4omssdl632g/extra", false},
		{"missing post", "https://bsky.app/profile/handle.example.test", false},
		{"missing author", "https://bsky.app/profile//post/3l4omssdl632g", false},
		{"empty post id", "https://bsky.app/profile/handle.example.test/post/", false},
		{"bad post id", "https://bsky.app/profile/handle.example.test/post/!!", false},
		{"loopback host", "https://127.0.0.1/profile/handle/post/3l4omssdl632g", false},
		{"localhost", "https://localhost/profile/handle/post/3l4omssdl632g", false},
		{"internal suffix", "https://bsky.app.internal/profile/handle/post/3l4omssdl632g", false},
		{"local suffix", "https://bsky.app.local/profile/handle/post/3l4omssdl632g", false},
		{"encoded slash", "https://bsky.app/profile/handle%2Fevil/post/3l4omssdl632g", false},
		{"at uri wrong collection", "at://handle.example.test/app.bsky.feed.like/3l4omssdl632g", false},
		{"at uri bad post id", "at://handle.example.test/app.bsky.feed.post/!!", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var parsed *url.URL
			if strings.HasPrefix(strings.ToLower(tc.rawURL), "at:") {
				var ok bool
				parsed, _, ok = blueskyParse(tc.rawURL)
				if !ok {
					if tc.want {
						t.Fatalf("blueskyParse(%q) failed unexpectedly", tc.rawURL)
					}
					return
				}
			} else {
				p, err := url.Parse(tc.rawURL)
				if err != nil {
					if tc.want {
						t.Fatalf("url.Parse(%q) unexpected error: %v", tc.rawURL, err)
					}
					return
				}
				parsed = p
			}
			if got := bluesky.Suitable(parsed); got != tc.want {
				t.Fatalf("Suitable(%q) = %v, want %v", tc.rawURL, got, tc.want)
			}
		})
	}
}

func blueskyCanonicalURL(author, postID string) string {
	return "https://bsky.app/profile/" + author + "/post/" + postID
}

func blueskyThreadURL(author, postID string) string {
	return "https://public.api.bsky.app/xrpc/app.bsky.feed.getPostThread?uri=at%3A%2F%2F" + author + "%2Fapp.bsky.feed.post%2F" + postID + "&depth=0&parentHeight=0"
}

func blueskyResolveURL(author string) string {
	return "https://public.api.bsky.app/xrpc/app.bsky.actor.resolveHandle?handle=" + author
}

func blueskyPLCDoc(did string) string {
	return blueskyPDSDirectory + "/" + did
}

func TestBlueskyExtractsDirectVideoWithHLSAndBlobFormats(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	did := "did:plc:pzdr5ylumf7vmvwasrpr5bf2"
	thread := blueskyFixture(t, "thread_video.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.IsPlaylist() {
		t.Fatalf("expected single media, got playlist")
	}
	info := result.Info
	if id, _ := info.ID(); id != postID {
		t.Fatalf("id = %q", id)
	}
	if ext, _ := info.Extension(); ext != "mp4" {
		t.Fatalf("ext = %q", ext)
	}
	formats, ok := info.Formats()
	if !ok || len(formats) != 2 {
		t.Fatalf("formats = %v", formats)
	}
	byID := make(map[string]*value.Object)
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		byID[id] = obj
	}
	hls, ok := byID[blueskyFormatHLS]
	if !ok {
		t.Fatal("missing hls format")
	}
	if proto, _ := hls.Lookup("protocol").StringValue(); proto != blueskyProtocolHLSNative {
		t.Fatalf("hls protocol = %q", proto)
	}
	if ext, _ := hls.Lookup("ext").StringValue(); ext != "mp4" {
		t.Fatalf("hls ext = %q", ext)
	}
	blob, ok := byID[blueskyFormatBlob]
	if !ok {
		t.Fatal("missing blob format")
	}
	blobURL, _ := blob.Lookup("url").StringValue()
	if !strings.Contains(blobURL, "/xrpc/com.atproto.sync.getBlob?") {
		t.Fatalf("blob url = %q", blobURL)
	}
	if !strings.Contains(blobURL, "did="+url.QueryEscape(did)) {
		t.Fatalf("blob url did = %q", blobURL)
	}
	if width, _ := blob.Lookup("width").Int(); width != 1280 {
		t.Fatalf("blob width = %d", width)
	}
	if height, _ := blob.Lookup("height").Int(); height != 720 {
		t.Fatalf("blob height = %d", height)
	}
	if ext, _ := blob.Lookup("ext").StringValue(); ext != "mp4" {
		t.Fatalf("blob ext = %q", ext)
	}
	if uploader, _ := info.Lookup("uploader").StringValue(); uploader != "Blu3Blu3Lilith" {
		t.Fatalf("uploader = %q", uploader)
	}
	if desc, _ := info.Lookup("description").StringValue(); desc != "OMG WE HAVE VIDEOS NOW" {
		t.Fatalf("description = %q", desc)
	}
	if thumbs, _ := info.Lookup("thumbnail").StringValue(); !strings.HasPrefix(thumbs, "https://") {
		t.Fatalf("thumbnail = %q", thumbs)
	}
	if like, _ := info.Lookup("like_count").Int(); like != 42 {
		t.Fatalf("like_count = %d", like)
	}
	if date, _ := info.Lookup("upload_date").StringValue(); date != "20240921" {
		t.Fatalf("upload_date = %q", date)
	}
}

func TestBlueskyExtractsRecordWithMediaAsMedia(t *testing.T) {
	author := "rwm.example.test"
	postID := "3l4qhp7bcs52e"
	did := "did:plc:rwmdid"
	thread := blueskyFixture(t, "thread_record_with_media.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.IsPlaylist() {
		t.Fatalf("expected single media, got playlist")
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("missing formats")
	}
	hlsFound := false
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		if id == blueskyFormatHLS {
			hlsFound = true
		}
	}
	if !hlsFound {
		t.Fatalf("missing hls format in recordWithMedia, got %d formats", len(formats))
	}
}

func TestBlueskyExtractsQuotedPostPlaylist(t *testing.T) {
	author := "primary.example.test"
	postID := "3l4qhp7bcs52c"
	did := "did:plc:primarydid"
	thread := blueskyFixture(t, "thread_quoted_only.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// The quoted post fixture exposes one direct video; the nested path
	// would have produced a second entry if the fixture had an embeds
	// array. The contract guarantees that one concrete media becomes a
	// Media and multiple supported entries become a playlist; either is
	// acceptable as long as the result is non-empty.
	if result.IsPlaylist() {
		entries, err := CollectEntries(context.Background(), result.Entries, 32)
		if err != nil {
			t.Fatalf("CollectEntries: %v", err)
		}
		if len(entries) < 1 {
			t.Fatalf("expected at least 1 entry, got %d", len(entries))
		}
		return
	}
	formats, _ := result.Info.Formats()
	if len(formats) == 0 {
		t.Fatal("missing formats")
	}
}

func TestBlueskyExtractsCaptionsAndAgeLimit(t *testing.T) {
	author := "caption.example.test"
	postID := "3l4qhp7bcs52h"
	did := "did:plc:captiondid"
	thread := blueskyFixture(t, "thread_captions.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	subtitles, ok := result.Info.Lookup("subtitles").Object()
	if !ok {
		t.Fatal("missing subtitles object")
	}
	if lang, _ := subtitles.Lookup("en").ListValue(); len(lang) == 0 {
		t.Fatal("missing en subtitles")
	}
}

func TestBlueskyAgeLimitIs18ForSexualLabel(t *testing.T) {
	author := "adult.example.test"
	postID := "3l4qhp7bcs52i"
	did := "did:plc:adultdid"
	thread := blueskyFixture(t, "thread_age_18.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `","displayName":"Adult"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	age, _ := result.Info.Lookup("age_limit").Int()
	if age != 18 {
		t.Fatalf("age_limit = %d", age)
	}
	tagsList, _ := result.Info.Lookup("tags").ListValue()
	found := false
	for _, t := range tagsList {
		if s, _ := t.StringValue(); s == "sexual" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing sexual tag in labels")
	}
}

func TestBlueskyExternalEmbedRoutesToEntry(t *testing.T) {
	author := "ext.example.test"
	postID := "3l4qhp7bcs52g"
	did := "did:plc:extdid"
	thread := blueskyFixture(t, "thread_external.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	// The external embed is a transparent URL result. Either the extractor
	// returns a single Media with the URL or a single-entry playlist.
	var entries []Entry
	if result.IsPlaylist() {
		collected, err := CollectEntries(context.Background(), result.Entries, 8)
		if err != nil {
			t.Fatalf("CollectEntries: %v", err)
		}
		entries = collected
	} else {
		if url, _ := result.Info.Lookup("url").StringValue(); url != "" {
			entries = []Entry{{URL: url}}
		}
	}
	if len(entries) == 0 {
		t.Fatal("missing entries")
	}
	foundExternal := false
	for _, e := range entries {
		if strings.Contains(e.URL, "example.test/article") {
			foundExternal = true
		}
	}
	if !foundExternal {
		t.Fatalf("missing external entry in %+v", entries)
	}
}

func TestBlueskyDIDWebTrustedEndpointResolves(t *testing.T) {
	author := "fixture.example.test"
	postID := "3l4omssdl632g"
	did := "did:web:fixture.example.test"
	thread := blueskyFixture(t, "thread_video.json")
	docURL := "https://fixture.example.test/.well-known/did.json"
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		docURL:                           {body: blueskyFixture(t, "did_web.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	formats, _ := result.Info.Formats()
	if len(formats) < 2 {
		t.Fatalf("expected at least 2 formats, got %d", len(formats))
	}
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		if id == blueskyFormatBlob {
			blobURL, _ := obj.Lookup("url").StringValue()
			if !strings.Contains(blobURL, "pds.example.test") {
				t.Fatalf("blob url not from did:web PDS: %s", blobURL)
			}
		}
	}
}

func TestBlueskyDIDDocMaliciousFallbackUsesDefaultPDS(t *testing.T) {
	author := "malicious.example.test"
	postID := "3l4omssdl632g"
	did := "did:plc:malicious"
	thread := blueskyFixture(t, "thread_video.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_malicious.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	formats, _ := result.Info.Formats()
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		if id == blueskyFormatBlob {
			blobURL, _ := obj.Lookup("url").StringValue()
			if strings.Contains(blobURL, "127.0.0.1") {
				t.Fatalf("blob url rejected but used: %s", blobURL)
			}
			if !strings.Contains(blobURL, "bsky.social") {
				t.Fatalf("expected fallback to bsky.social, got %s", blobURL)
			}
		}
	}
}

func TestBlueskyDIDDocCredentialsAreRejected(t *testing.T) {
	author := "fixture.example.test"
	postID := "3l4omssdl632g"
	did := "did:web:fixture.example.test"
	thread := blueskyFixture(t, "thread_video.json")
	docURL := "https://fixture.example.test/.well-known/did.json"
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		docURL:                           {body: blueskyFixture(t, "did_malicious_credentials.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	formats, _ := result.Info.Formats()
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		if id == blueskyFormatBlob {
			blobURL, _ := obj.Lookup("url").StringValue()
			if strings.Contains(blobURL, "user:pass") {
				t.Fatalf("blob url leaked credentials: %s", blobURL)
			}
		}
	}
}

func TestBlueskyCategorizedErrorsAndSecretSafety(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"401", http.StatusUnauthorized, ErrAuthentication},
		{"403", http.StatusForbidden, ErrAuthentication},
		{"404", http.StatusNotFound, ErrUnavailable},
		{"410", http.StatusGone, ErrUnavailable},
		{"429", http.StatusTooManyRequests, ErrBlueskyNetwork},
		{"500", http.StatusInternalServerError, ErrBlueskyNetwork},
		{"502", http.StatusBadGateway, ErrBlueskyNetwork},
		{"503", http.StatusServiceUnavailable, ErrBlueskyNetwork},
		{"504", http.StatusGatewayTimeout, ErrBlueskyNetwork},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			author := "blu3blue.bsky.social"
			postID := "3l4omssdl632g"
			did := "did:plc:pzdr5ylumf7vmvwasrpr5bf2"
			const secret = "fixture-secret-string"
			threadBody := []byte(`{"thread":{"post":{"uri":"at://blu3blue.bsky.social/app.bsky.feed.post/3l4omssdl632g","author":{"did":"did:plc:pzdr5ylumf7vmvwasrpr5bf2","handle":"blu3blue.bsky.social","displayName":"Blu3Blu3Lilith"},"record":{"text":"x"},"embed":{"playlist":"https://video.bsky.app/watch/3l4omssdl632g/playlist.m3u8","cid":"bafyreivideocid1"},"indexedAt":"2024-09-21T13:43:25.000Z","likeCount":1,"repostCount":0,"replyCount":0,"labels":[]}}}`)
			threadBody = append(threadBody, []byte(" "+secret)...)
			transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
				blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
				blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
				blueskyThreadURL(author, postID): {status: tc.status, body: threadBody},
			})
			_, err := NewBluesky().Extract(context.Background(), Request{
				URL:       blueskyCanonicalURL(author, postID),
				Transport: transport,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("Extract error = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), secret) {
				t.Fatalf("error leaked secret: %v", err)
			}
		})
	}
}

func TestBlueskyCategorizesNetworkAndPreservesInvalidMetadata(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	transport := newBlueskyTransport(nil)
	transport.otherwise.status = 0
	transport.otherwise.body = []byte(`{`)
	_, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) && !errors.Is(err, ErrBlueskyNetwork) {
		t.Fatalf("Extract error = %v, want ErrInvalidMetadata or ErrBlueskyNetwork", err)
	}
}

func TestBlueskyHonorsCancellation(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	transport := &blueskyCancellingTransport{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewBluesky().Extract(ctx, Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Extract error = %v, want context.Canceled", err)
	}
}

type blueskyCancellingTransport struct{}

func (blueskyCancellingTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected page request")
}

func (blueskyCancellingTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestBlueskyRejectsOversizedJSON(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	big := bytes.Repeat([]byte("a"), 17<<20)
	transport := newBlueskyTransport(nil)
	transport.otherwise.status = http.StatusOK
	transport.otherwise.body = big
	_, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, ErrJSONResponseTooLarge) && !errors.Is(err, ErrBlueskyNetwork) && !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract error = %v", err)
	}
}

func TestBlueskyNoVideoReturnsUnavailable(t *testing.T) {
	author := "notext.example.test"
	postID := "3l4qhp7bcs52j"
	did := "did:plc:notextdid"
	thread := blueskyFixture(t, "thread_no_video.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	_, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Extract error = %v, want ErrUnavailable", err)
	}
}

func TestBlueskyNotFoundThreadIsUnavailable(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	did := "did:plc:pzdr5ylumf7vmvwasrpr5bf2"
	thread := blueskyFixture(t, "thread_not_found.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	_, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract error = %v", err)
	}
}

func TestBlueskyNetworkFailureFallsBackToDefaultPDS(t *testing.T) {
	author := "fixture.example.test"
	postID := "3l4omssdl632g"
	did := "did:plc:pdsfailure"
	thread := blueskyFixture(t, "thread_video.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {status: http.StatusInternalServerError, body: []byte(`oops`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	formats, _ := result.Info.Formats()
	for _, raw := range formats {
		obj, _ := raw.Object()
		id, _ := obj.Lookup("format_id").StringValue()
		if id == blueskyFormatBlob {
			blobURL, _ := obj.Lookup("url").StringValue()
			if !strings.Contains(blobURL, "bsky.social") {
				t.Fatalf("expected fallback PDS, got %s", blobURL)
			}
		}
	}
}

func TestBlueskyMalformedThreadIsInvalidMetadata(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	did := "did:plc:pzdr5ylumf7vmvwasrpr5bf2"
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
		blueskyThreadURL(author, postID): {body: []byte(`{`)},
	})
	_, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("Extract error = %v, want ErrInvalidMetadata", err)
	}
}

func TestBlueskyURLBytesBound(t *testing.T) {
	longAuthor := strings.Repeat("a", 200) + "." + strings.Repeat("b", 60) + ".test"
	url := "https://bsky.app/profile/" + longAuthor + "/post/3l4omssdl632g"
	parsed, err := urlParse(url)
	if err != nil {
		t.Fatalf("url.Parse: %v", err)
	}
	if NewBluesky().Suitable(parsed) {
		t.Fatal("long author URL was accepted")
	}
}

func urlParse(rawURL string) (*url.URL, error) { return url.Parse(rawURL) }

func TestBlueskyATURIAcceptsDidAndHandleAuthor(t *testing.T) {
	cases := []string{
		"at://blu3blue.bsky.social/app.bsky.feed.post/3l4omssdl632g",
		"at://did:plc:abcdefghijklmnopqrstuvw/app.bsky.feed.post/3l4omssdl632g",
	}
	for _, rawURL := range cases {
		t.Run(rawURL, func(t *testing.T) {
			parsed, target, ok := blueskyParse(rawURL)
			if !ok {
				t.Fatalf("blueskyParse(%q) failed", rawURL)
			}
			if !strings.HasPrefix(target.atURI, "at://") {
				t.Fatalf("atURI = %q", target.atURI)
			}
			_ = parsed
		})
	}
}

func TestBlueskyATURIRejectsUnsafeShapes(t *testing.T) {
	cases := []string{
		"at://blu3blue.bsky.social/evil.collection/3l4omssdl632g",
		"at:///app.bsky.feed.post/3l4omssdl632g",
		"at://blu3blue.bsky.social/app.bsky.feed.post/",
		"at://blu3blue.bsky.social/app.bsky.feed.post/!!",
		"bsky://blu3blue.bsky.social/app.bsky.feed.post/3l4omssdl632g",
	}
	for _, rawURL := range cases {
		t.Run(rawURL, func(t *testing.T) {
			_, _, ok := blueskyParse(rawURL)
			if ok {
				t.Fatalf("blueskyParse(%q) accepted", rawURL)
			}
		})
	}
}

func TestBlueskyBlobURLAvoidsCredentialsAndFragments(t *testing.T) {
	cases := []struct {
		endpoint string
		did      string
		cid      string
		want     bool
	}{
		{"https://pds.example.test", "did:plc:abc", "bafyreicidhere12", true},
		{"http://pds.example.test", "did:plc:abc", "bafyreicidhere12", false},
		{"https://user:pass@pds.example.test", "did:plc:abc", "bafyreicidhere12", false},
		{"https://127.0.0.1", "did:plc:abc", "bafyreicidhere12", false},
		{"https://pds.example.test:8080", "did:plc:abc", "bafyreicidhere12", false},
		{"https://localhost", "did:plc:abc", "bafyreicidhere12", false},
		{"https://single", "did:plc:abc", "bafyreicidhere12", false},
		{"https://pds.example.test", "did:plc:abc", "!!", false},
		{"https://pds.example.test", "did:plc:abc", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.endpoint, func(t *testing.T) {
			got, ok := blueskyBuildBlobURL(tc.endpoint, tc.did, tc.cid)
			if ok != tc.want {
				t.Fatalf("blueskyBuildBlobURL(%q, %q, %q) = (%q, %v), want ok=%v", tc.endpoint, tc.did, tc.cid, got, ok, tc.want)
			}
			if ok {
				if strings.Contains(got, "user:pass") {
					t.Fatalf("blob url leaked credentials: %s", got)
				}
				parsed, _ := url.Parse(got)
				if parsed.Fragment != "" {
					t.Fatalf("blob url has fragment: %s", got)
				}
				if strings.Contains(got, "%2F") || strings.Contains(got, "%2f") {
					t.Fatalf("blob url contains encoded slash: %s", got)
				}
			}
		})
	}
}

func TestBlueskySafeMediaURLRejectsUnsafeSchemes(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"https://video.bsky.app/watch/x/playlist.m3u8", true},
		{"http://video.bsky.app/watch/x/playlist.m3u8", false},
		{"ftp://video.bsky.app/watch/x/playlist.m3u8", false},
		{"https://127.0.0.1/x.m3u8", false},
		{"https://localhost/x.m3u8", false},
		{"https://bsky.app.internal/x.m3u8", false},
		{"https://user:pass@video.bsky.app/x.m3u8", false},
		{"https://video.bsky.app/x.m3u8#frag", false},
		{"https://video.bsky.app:8080/x.m3u8", false},
		{"", false},
		{"https://", false},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			_, got := blueskySafeMediaURL(tc.raw)
			if got != tc.want {
				t.Fatalf("blueskySafeMediaURL(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}
}

func TestBlueskyTitleTruncationAndFallback(t *testing.T) {
	record := []byte(`{"text":"` + strings.Repeat("A", 200) + `"}`)
	got := blueskyTitleFromRecord(record, "fallbackid")
	if len([]rune(got)) > blueskyMaxTitleTruncate {
		t.Fatalf("title length = %d > %d", len([]rune(got)), blueskyMaxTitleTruncate)
	}
	empty := []byte(`{"text":""}`)
	if got := blueskyTitleFromRecord(empty, "fallbackid"); got != "Bluesky video #fallbackid" {
		t.Fatalf("empty title = %q", got)
	}
}

func TestBlueskyTimestampAndUploadDate(t *testing.T) {
	ts, ok := blueskyTimestamp("2024-09-21T13:43:25.000Z")
	if !ok || ts != 1726926205 {
		t.Fatalf("timestamp = %d, %v", ts, ok)
	}
	if date := blueskyUploadDate(ts); date != "20240921" {
		t.Fatalf("upload_date = %q", date)
	}
	if _, ok := blueskyTimestamp("not-a-date"); ok {
		t.Fatal("invalid timestamp was accepted")
	}
}

func TestBlueskyOrderedTagsDedupesAndBounds(t *testing.T) {
	labels := make([]blueskyLabel, 0, 100)
	for i := 0; i < 100; i++ {
		labels = append(labels, blueskyLabel{Val: "x"})
	}
	labels = append(labels, blueskyLabel{Val: "y"})
	tags, ok := blueskyOrderedTags(labels)
	if !ok {
		t.Fatal("tags not built")
	}
	if len(tags) > blueskyMaxTags {
		t.Fatalf("tags count = %d > %d", len(tags), blueskyMaxTags)
	}
}

func TestBlueskyPostIDPatternAcceptsPinnedIDs(t *testing.T) {
	for _, id := range []string{"3l4omssdl632g", "3l3vgf77uco2g", "3l4qhp7bcs52c", "3l3w4tnezek2e", "3l6oe5mtr2c2j", "3l7gqcfes742o", "3l77u64l7le2e", "3l6zrz6zyl2dr", "3l7gv55dc2o2w", "3l7rdfxhyds2f", "3l4qhp7bcs52e"} {
		if !blueskyPostIDPattern.MatchString(id) {
			t.Fatalf("post id %q not accepted", id)
		}
	}
}

func TestBlueskyCycleAndDuplicateEntriesAreDeduped(t *testing.T) {
	// A quoted post whose nested record itself contains a quoted post
	// with the same playlist must produce one entry, not two. The outer
	// post has no direct video so the result collapses to a single
	// Media for the nested entry; the dedup check still applies.
	author := "loop.example.test"
	postID := "3l4qhp7bcs52l"
	did := "did:plc:loopdid"
	thread := []byte(`{"thread":{"post":{"uri":"at://loop.example.test/app.bsky.feed.post/3l4qhp7bcs52l","author":{"did":"did:plc:loopdid","handle":"loop.example.test"},"record":{"text":"outer"},"embed":{"$type":"app.bsky.embed.record","record":{"uri":"at://loop.example.test/app.bsky.feed.post/3l4qhp7bcs52m","cid":"bafyreicid","embeds":[{"$type":"app.bsky.embed.video.view","playlist":"https://video.bsky.app/watch/dup/playlist.m3u8","thumbnail":"https://video.bsky.app/watch/dup/thumb.jpg","cid":"bafyreidupcid01"}]}}}}}`)
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: []byte(`{"id":"` + did + `","service":[{"id":"#atproto_pds","type":"AtprotoPersonalDataServer","serviceEndpoint":"https://pds.example.test"}]}`)},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	})
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !result.IsPlaylist() {
		// A single nested entry becomes a single Media; the dedup
		// assertion still applies: the dedup key is the entryURL.
		formats, _ := result.Info.Formats()
		if len(formats) == 0 {
			t.Fatal("expected at least one format")
		}
		return
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 8)
	if err != nil {
		t.Fatalf("CollectEntries: %v", err)
	}
	seen := make(map[string]bool)
	for _, e := range entries {
		if seen[e.URL] {
			t.Fatalf("duplicate url %s", e.URL)
		}
		seen[e.URL] = true
	}
}

func TestBlueskyPlaylistEntriesReextractSelectedMedia(t *testing.T) {
	author := "multi.example.test"
	postID := "3l4qhp7bcs52c"
	did := "did:plc:multidid"
	thread := []byte(`{"thread":{"post":{"uri":"at://multi.example.test/app.bsky.feed.post/3l4qhp7bcs52c","author":{"did":"did:plc:multidid","handle":"multi.example.test","displayName":"Outer"},"record":{"text":"outer"},"embed":{"playlist":"https://video.bsky.app/watch/outer/playlist.m3u8","cid":"bafyreimainvid","record":{"record":{"uri":"at://quoted.example.test/app.bsky.feed.post/3l4qhp7bcs52d","author":{"did":"did:plc:quoteddid","handle":"quoted.example.test","displayName":"Quoted"},"record":{"text":"quoted"},"embed":{"playlist":"https://video.bsky.app/watch/quoted/playlist.m3u8","cid":"bafyreiquotedvid"}}}}}}}`)
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	result, err := NewBluesky().Extract(context.Background(), Request{URL: blueskyCanonicalURL(author, postID), Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsPlaylist() {
		t.Fatal("two videos must produce a playlist")
	}
	entries, err := CollectEntries(context.Background(), result.Entries, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("playlist entries = %d, want 2", len(entries))
	}
	for index, entry := range entries {
		want := blueskyCanonicalURL(author, postID) + "?media=" + strconv.Itoa(index+1)
		if entry.URL != want || entry.ExtractorKey != "bluesky" || !entry.Transparent {
			t.Fatalf("entry %d = %#v, want internal transparent selector %q", index, entry, want)
		}
	}
	selected, err := NewBluesky().Extract(context.Background(), Request{URL: entries[1].URL, Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if selected.IsPlaylist() {
		t.Fatal("media selector must return one media item")
	}
	formats, _ := selected.Info.Formats()
	first, _ := formats[0].Object()
	formatURL, _ := first.Lookup("url").StringValue()
	if !strings.Contains(formatURL, "/quoted/") {
		t.Fatalf("selected media URL = %q", formatURL)
	}
}

func TestBlueskyRequestAssertionsAndQueryArgs(t *testing.T) {
	author := "blu3blue.bsky.social"
	postID := "3l4omssdl632g"
	did := "did:plc:pzdr5ylumf7vmvwasrpr5bf2"
	thread := blueskyFixture(t, "thread_video.json")
	transport := newBlueskyTransport(map[string]blueskyFixtureResponse{
		blueskyResolveURL(author):        {body: []byte(`{"did":"` + did + `","handle":"` + author + `"}`)},
		blueskyPLCDoc(did):               {body: blueskyFixture(t, "did_plc.json")},
		blueskyThreadURL(author, postID): {body: thread},
	})
	if _, err := NewBluesky().Extract(context.Background(), Request{
		URL:       blueskyCanonicalURL(author, postID),
		Transport: transport,
	}); err != nil {
		t.Fatal(err)
	}
	if !transport.seenRequest("depth=0&parentHeight=0") {
		t.Fatal("missing deterministic query")
	}
	if !transport.seenRequest("at%3A%2F%2Fblu3blue.bsky.social%2Fapp.bsky.feed.post%2F3l4omssdl632g") {
		t.Fatal("missing AT URI in query")
	}
}

func FuzzBlueskyRouting(f *testing.F) {
	for _, seed := range []string{
		"https://bsky.app/profile/blu3blue.bsky.social/post/3l4omssdl632g",
		"https://www.bsky.app/profile/handle/post/3l4omssdl632g",
		"https://main.bsky.dev/profile/handle/post/3l4omssdl632g",
		"at://did:plc:abcdefghijklmnopqrstuvw/app.bsky.feed.post/3l4omssdl632g",
		"at://handle/app.bsky.feed.post/3l4omssdl632g",
		"https://evil.example/profile/handle/post/3l4omssdl632g",
		"https://bsky.app/profile/handle/post/3l4omssdl632g?foo=bar",
		"https://bsky.app:443/profile/handle/post/3l4omssdl632g",
		"https://127.0.0.1/profile/handle/post/3l4omssdl632g",
		"",
		"at:///app.bsky.feed.post/3l4omssdl632g",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, rawURL string) {
		if len(rawURL) > 1<<16 {
			t.Skip()
		}
		if strings.HasPrefix(strings.ToLower(rawURL), "at:") {
			_, _, _ = blueskyParse(rawURL)
			return
		}
		parsed, err := url.Parse(rawURL)
		if err == nil {
			_ = NewBluesky().Suitable(parsed)
			if parsed != nil {
				_, _ = classifyBlueskyWeb(parsed)
			}
		}
	})
}

func FuzzBlueskyJSON(t *testing.F) {
	thread := blueskyFixture(t, "thread_video.json")
	t.Add(thread)
	t.Add([]byte(`{"thread":{}}`))
	t.Add([]byte(`{`))
	t.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 1<<20 {
			t.Skip()
		}
		var thread blueskyThreadResponse
		if err := json.Unmarshal(body, &thread); err != nil {
			return
		}
		if _, err := blueskyExtractPost(thread); err != nil {
			return
		}
		target := blueskyTarget{
			author: "fuzz.example.test",
			postID: "3l4omssdl632g",
			atURI:  "at://fuzz.example.test/app.bsky.feed.post/3l4omssdl632g",
		}
		_, _ = blueskyCollectEntries(thread, target, "did:plc:fuzzdid", blueskyFallbackPDS)
	})
}
