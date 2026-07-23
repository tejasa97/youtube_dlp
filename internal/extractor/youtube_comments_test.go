package extractor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

type youtubeCommentFixtureTransport struct {
	responses      map[string][]byte
	statuses       map[string]int
	tokens         []string
	visitors       []string
	clicks         []string
	visitorHeaders []string
}

type youtubeCommentRetryTransport struct {
	statuses []int
	bodies   [][]byte
	calls    int
}

type youtubeCommentChainTransport struct{ calls int }

func (*youtubeCommentChainTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (transport *youtubeCommentChainTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	transport.calls++
	body := fmt.Sprintf(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
		{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"next-%d"}}}}
	]}}]}`, transport.calls)
	return &http.Response{
		StatusCode: http.StatusOK, Header: make(http.Header),
		Body: io.NopCloser(strings.NewReader(body)), Request: request,
	}, nil
}

func (*youtubeCommentRetryTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (transport *youtubeCommentRetryTransport) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	index := transport.calls
	transport.calls++
	status := transport.statuses[min(index, len(transport.statuses)-1)]
	body := transport.bodies[min(index, len(transport.bodies)-1)]
	return &http.Response{
		StatusCode: status, Header: make(http.Header),
		Body: io.NopCloser(bytes.NewReader(body)), Request: request,
	}, nil
}

func (*youtubeCommentFixtureTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	return nil, nil, errors.New("unexpected ReadPage")
}

func (transport *youtubeCommentFixtureTransport) Do(ctx context.Context, request *http.Request) (*http.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Method != http.MethodPost || request.URL.Path != "/youtubei/v1/next" ||
		request.URL.Query().Get("prettyPrint") != "false" ||
		request.Header.Get("X-Youtube-Client-Name") != "1" ||
		request.Header.Get("X-Youtube-Client-Version") != "2.fixture-comments" {
		return nil, errors.New("unexpected comment request")
	}
	var payload struct {
		Context struct {
			Client struct {
				VisitorData string `json:"visitorData"`
			} `json:"client"`
		} `json:"context"`
		Continuation  string `json:"continuation"`
		ClickTracking struct {
			ClickTrackingParams string `json:"clickTrackingParams"`
		} `json:"clickTracking"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	transport.tokens = append(transport.tokens, payload.Continuation)
	transport.visitors = append(transport.visitors, payload.Context.Client.VisitorData)
	transport.clicks = append(transport.clicks, payload.ClickTracking.ClickTrackingParams)
	transport.visitorHeaders = append(transport.visitorHeaders, request.Header.Get("X-Goog-Visitor-Id"))
	status := transport.statuses[payload.Continuation]
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(transport.responses[payload.Continuation])),
		Request:    request,
	}, nil
}

func youtubeCommentFixtureResponses(t *testing.T) map[string][]byte {
	t.Helper()
	page := readYouTubeFixture(t, "comments-page.json")
	return map[string][]byte{
		"initial-comments-token": readYouTubeFixture(t, "comments-header.json"),
		"top-comments-token":     page,
		"new-comments-token":     page,
		"comments-page-2":        readYouTubeFixture(t, "comments-page-2.json"),
	}
}

func TestYouTubeCommentsDefaultNewSortVisitorRotationAndFields(t *testing.T) {
	transport := &youtubeCommentFixtureTransport{responses: youtubeCommentFixtureResponses(t)}
	comments, disabled, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 10})
	if err != nil || disabled {
		t.Fatalf("comments=%v disabled=%v err=%v", comments, disabled, err)
	}
	if got := strings.Join(transport.tokens, ","); got != "initial-comments-token,new-comments-token,comments-page-2" {
		t.Fatalf("tokens=%q", got)
	}
	if got := strings.Join(transport.visitors, ","); got != "visitor-initial,visitor-header,visitor-comments-page" {
		t.Fatalf("visitors=%q", got)
	}
	if got := strings.Join(transport.visitorHeaders, ","); got != "visitor-initial,visitor-header,visitor-comments-page" {
		t.Fatalf("visitor headers=%q", got)
	}
	if len(comments) != 4 {
		t.Fatalf("comments=%#v", comments)
	}
	legacy, _ := comments[0].Object()
	if objectString(legacy, "id") != "legacy-comment" ||
		objectString(legacy, "text") != "Legacy comment text" ||
		objectString(legacy, "parent") != "root" ||
		objectString(legacy, "author_url") != "https://www.youtube.com/@legacy-author" {
		t.Fatalf("legacy=%#v", legacy)
	}
	if likes, _ := legacy.Lookup("like_count").Int(); likes != 12 {
		t.Fatalf("legacy likes=%d", likes)
	}
	reply, _ := comments[1].Object()
	if objectString(reply, "id") != "legacy-reply" || objectString(reply, "parent") != "legacy-comment" {
		t.Fatalf("reply=%#v", reply)
	}
	modern, _ := comments[2].Object()
	if objectString(modern, "id") != "modern-comment" ||
		objectString(modern, "author_url") != "https://www.youtube.com/@modern-author" {
		t.Fatalf("modern=%#v", modern)
	}
	if likes, _ := modern.Lookup("like_count").Int(); likes != 1200 {
		t.Fatalf("modern likes=%d", likes)
	}
	for _, field := range []string{"author_is_uploader", "author_is_verified", "is_favorited", "is_pinned"} {
		if boolean, ok := modern.Lookup(field).Bool(); !ok || !boolean {
			t.Fatalf("modern %s=%v/%v", field, boolean, ok)
		}
	}
}

func TestYouTubeCommentsTopSortAndBounds(t *testing.T) {
	transport := &youtubeCommentFixtureTransport{responses: youtubeCommentFixtureResponses(t)}
	comments, disabled, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{
			Enabled: true, Sort: "top", MaxComments: 10,
			MaxParents: 1, MaxReplies: 1, MaxRepliesPerThread: 1, MaxDepth: 2,
		})
	if err != nil || disabled {
		t.Fatalf("comments=%v disabled=%v err=%v", comments, disabled, err)
	}
	if got := strings.Join(transport.tokens, ","); got != "initial-comments-token,top-comments-token" {
		t.Fatalf("tokens=%q", got)
	}
	if len(comments) != 2 || objectStringValue(comments[0], "id") != "legacy-comment" ||
		objectStringValue(comments[1], "id") != "legacy-reply" {
		t.Fatalf("bounded comments=%#v", comments)
	}
}

func TestYouTubeCommentsWrappedSortReplyContinuationAndMultipleActions(t *testing.T) {
	header := []byte(`{
		"responseContext":{"visitorData":"visitor-header-2"},
		"onResponseReceivedEndpoints":[
			{"reloadContinuationItemsCommand":{"continuationItems":[]}},
			{"appendContinuationItemsAction":{"continuationItems":[
				{"commentsHeaderRenderer":{"sortMenu":{"sortFilterSubMenuRenderer":{"subMenuItems":[
					{"serviceEndpoint":{"continuationCommand":{"token":"top-root"}}},
					{"serviceEndpoint":{"commandExecutorCommand":{"commands":[
						{"clickTrackingParams":"click-new","continuationCommand":{"token":"new-root"}}
					]}}}
				]}}}}
			]}}
		]}`)
	root := []byte(`{
		"responseContext":{"visitorData":"visitor-root-2"},
		"onResponseReceivedActions":[
			{"reloadContinuationItemsCommand":{"continuationItems":[]}},
			{"appendContinuationItemsAction":{"continuationItems":[
				{"commentThreadRenderer":{
					"comment":{"commentRenderer":{"commentId":"parent-2","contentText":{"simpleText":"parent"}}},
					"replies":{"commentRepliesRenderer":{"contents":[
						{"continuationItemRenderer":{"continuationEndpoint":{
							"clickTrackingParams":"click-reply",
							"continuationCommand":{"token":"reply-token"}
						}}}
					]}}
				}}
			]}}
		]}`)
	reply := []byte(`{
		"onResponseReceivedActions":[
			{"appendContinuationItemsAction":{"continuationItems":[
				{"commentThreadRenderer":{
					"comment":{"commentRenderer":{"commentId":"reply-2","contentText":{"simpleText":"reply"}}},
					"replies":{"commentRepliesRenderer":{"subThreads":[
						{"commentThreadRenderer":{"comment":{"commentRenderer":{
							"commentId":"reply-3","contentText":{"simpleText":"nested reply"}
						}}}}
					]}}
				}}
			]}}
		]}`)
	transport := &youtubeCommentFixtureTransport{responses: map[string][]byte{
		"initial-comments-token": header,
		"new-root":               root,
		"reply-token":            reply,
	}}
	comments, disabled, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 10, MaxParents: 1, MaxReplies: 2, MaxDepth: 2})
	if err != nil || disabled {
		t.Fatalf("comments=%v disabled=%v err=%v", comments, disabled, err)
	}
	if len(comments) != 2 || objectStringValue(comments[0], "id") != "parent-2" ||
		objectStringValue(comments[1], "id") != "reply-2" ||
		objectStringValue(comments[1], "parent") != "parent-2" {
		t.Fatalf("comments=%#v", comments)
	}
	if got := strings.Join(transport.tokens, ","); got != "initial-comments-token,new-root,reply-token" {
		t.Fatalf("tokens=%q", got)
	}
	if got := strings.Join(transport.clicks, ","); got != ",click-new,click-reply" {
		t.Fatalf("click tracking=%q", got)
	}
	if got := strings.Join(transport.visitorHeaders, ","); got != "visitor-initial,visitor-header-2,visitor-root-2" {
		t.Fatalf("visitor headers=%q", got)
	}
	deepTransport := &youtubeCommentFixtureTransport{responses: map[string][]byte{
		"initial-comments-token": header, "new-root": root, "reply-token": reply,
	}}
	deepComments, _, err := extractYouTubeComments(context.Background(), deepTransport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 10, MaxParents: 1, MaxReplies: 3, MaxDepth: 3})
	if err != nil || len(deepComments) != 3 ||
		objectStringValue(deepComments[2], "parent") != "reply-2" {
		t.Fatalf("deep comments=%#v err=%v", deepComments, err)
	}
}

func TestYouTubeCommentsPinnedDuplicateDoesNotStopTraversal(t *testing.T) {
	first := []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
		{"commentRenderer":{"commentId":"duplicate","contentText":{"simpleText":"pinned"},"pinnedCommentBadge":{"pinnedCommentBadgeRenderer":{}}}},
		{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"after-pinned"}}}}
	]}}]}`)
	second := []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
		{"commentRenderer":{"commentId":"duplicate","contentText":{"simpleText":"normal"}}},
		{"commentRenderer":{"commentId":"after","contentText":{"simpleText":"after"}}}
	]}}]}`)
	transport := &youtubeCommentFixtureTransport{responses: map[string][]byte{
		"initial-comments-token": first,
		"after-pinned":           second,
	}}
	comments, disabled, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 10})
	if err != nil || disabled || len(comments) != 2 ||
		objectStringValue(comments[0], "id") != "duplicate" ||
		objectStringValue(comments[1], "id") != "after" {
		t.Fatalf("comments=%#v disabled=%v err=%v", comments, disabled, err)
	}
}

func TestYouTubeCommentsReplyContinuationIsDepthFirstForTotalLimit(t *testing.T) {
	root := []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
		{"commentThreadRenderer":{
			"comment":{"commentRenderer":{"commentId":"parent-a","contentText":{"simpleText":"A"}}},
			"replies":{"commentRepliesRenderer":{"contents":[
				{"continuationItemRenderer":{"continuationEndpoint":{"continuationCommand":{"token":"reply-a"}}}}
			]}}
		}},
		{"commentRenderer":{"commentId":"parent-b","contentText":{"simpleText":"B"}}}
	]}}]}`)
	reply := []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[
		{"commentRenderer":{"commentId":"reply-a-1","contentText":{"simpleText":"reply A"}}}
	]}}]}`)
	transport := &youtubeCommentFixtureTransport{responses: map[string][]byte{
		"initial-comments-token": root,
		"reply-a":                reply,
	}}
	comments, _, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 2})
	if err != nil || len(comments) != 2 ||
		objectStringValue(comments[0], "id") != "parent-a" ||
		objectStringValue(comments[1], "id") != "reply-a-1" {
		t.Fatalf("comments=%#v err=%v", comments, err)
	}
}

func TestYouTubeCommentsDisabledAndForcedContinuation(t *testing.T) {
	page := []byte(`ytcfg.set({"INNERTUBE_CLIENT_VERSION":"2.fixture-comments"});`)
	forced := generateYouTubeCommentContinuation("fixture0001")
	transport := &youtubeCommentFixtureTransport{responses: map[string][]byte{
		forced: readYouTubeFixture(t, "comments-disabled.json"),
	}}
	comments, disabled, err := extractYouTubeComments(context.Background(), transport, page, "fixture0001",
		YouTubeCommentOptions{Enabled: true})
	if err != nil || !disabled || comments != nil {
		t.Fatalf("comments=%v disabled=%v err=%v", comments, disabled, err)
	}
	if len(transport.tokens) != 1 || transport.tokens[0] != forced {
		t.Fatalf("tokens=%v", transport.tokens)
	}
}

func TestYouTubeCommentFailuresCancellationAndLimits(t *testing.T) {
	page := readYouTubeFixture(t, "comments-watch.html")
	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusForbidden, ErrAuthentication},
		{http.StatusNotFound, ErrUnavailable},
		{http.StatusTooManyRequests, ErrYouTubeCommentsRateLimited},
		{http.StatusInternalServerError, ErrYouTubeCommentsNetwork},
	} {
		transport := &youtubeCommentFixtureTransport{
			responses: youtubeCommentFixtureResponses(t),
			statuses:  map[string]int{"initial-comments-token": test.status},
		}
		_, _, err := extractYouTubeComments(context.Background(), transport, page, "fixture0001",
			YouTubeCommentOptions{Enabled: true})
		if !errors.Is(err, test.want) {
			t.Fatalf("status %d error=%v", test.status, err)
		}
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := extractYouTubeComments(cancelled,
		&youtubeCommentFixtureTransport{responses: youtubeCommentFixtureResponses(t)},
		page, "fixture0001", YouTubeCommentOptions{Enabled: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
	if _, err := normalizeYouTubeCommentLimits(YouTubeCommentOptions{MaxComments: maxYouTubeCommentLimit + 1}); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("limit error=%v", err)
	}
}

func TestYouTubeCommentRetriesTransientAndIncompleteResponses(t *testing.T) {
	config := youtubePlaylistConfig{ClientVersion: "fixture"}
	for _, test := range []struct {
		statuses []int
		bodies   [][]byte
	}{
		{
			statuses: []int{http.StatusInternalServerError, http.StatusOK},
			bodies:   [][]byte{[]byte(`server error`), readYouTubeFixture(t, "comments-page-2.json")},
		},
		{
			statuses: []int{http.StatusOK, http.StatusOK},
			bodies:   [][]byte{[]byte(`{}`), readYouTubeFixture(t, "comments-page-2.json")},
		},
	} {
		transport := &youtubeCommentRetryTransport{statuses: test.statuses, bodies: test.bodies}
		attempts := 0
		page, err := fetchYouTubeCommentPage(context.Background(), transport,
			youtubeCommentContinuation{token: "retry-token", parent: "root", depth: 1}, "visitor", config, &attempts)
		if err != nil || len(page.comments) != 1 || transport.calls != 2 {
			t.Fatalf("page=%#v calls=%d err=%v", page, transport.calls, err)
		}
	}
}

func TestYouTubeCommentsCapActualHTTPAttempts(t *testing.T) {
	transport := &youtubeCommentChainTransport{}
	_, _, err := extractYouTubeComments(context.Background(), transport,
		readYouTubeFixture(t, "comments-watch.html"), "fixture0001",
		YouTubeCommentOptions{Enabled: true, MaxComments: 10})
	if !errors.Is(err, ErrPlaylistLimit) || transport.calls != maxYouTubeCommentPages {
		t.Fatalf("calls=%d err=%v", transport.calls, err)
	}
}

func TestParseYouTubeCommentPageRejectsMalformedAndOversizedText(t *testing.T) {
	if _, err := parseYouTubeCommentPage([]byte(`{`)); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("malformed error=%v", err)
	}
	data := []byte(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[{"commentRenderer":{"commentId":"one","contentText":{"simpleText":"` +
		strings.Repeat("x", maxYouTubeCommentTextBytes+1) + `"}}}]}}]}`)
	if _, err := parseYouTubeCommentPage(data); !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestYouTubeModernCommentSanitizesIdentityAndThumbnail(t *testing.T) {
	var fixture value.Value
	if err := json.Unmarshal([]byte(`{
		"view":{"commentKey":"key"},
		"payload":{"commentEntityPayload":{
			"properties":{"commentId":"safe-id","content":{"content":"text"}},
			"author":{"avatarThumbnailUrl":"file:///private/secret"}
		}}
	}`), &fixture); err != nil {
		t.Fatal(err)
	}
	root, _ := fixture.Object()
	view, _ := root.Lookup("view").Object()
	payload, _ := root.Lookup("payload").Object()
	comment, valid, err := youtubeModernComment(view, youtubeCommentEntitySet{"key": payload}, "root")
	if err != nil || !valid {
		t.Fatalf("comment=%#v valid=%v err=%v", comment, valid, err)
	}
	object, _ := comment.Object()
	if !object.Lookup("author_thumbnail").IsMissing() {
		t.Fatalf("unsafe thumbnail retained: %#v", object)
	}
	properties, _ := objectValue(payload, "commentEntityPayload", "properties").Object()
	properties.Set("commentId", value.String("bad\nid"))
	if _, valid, err := youtubeModernComment(view, youtubeCommentEntitySet{"key": payload}, "root"); err != nil || valid {
		t.Fatalf("control-character id valid=%v err=%v", valid, err)
	}
}

func TestParseYouTubeCommentPageEnforcesStructuralBudgets(t *testing.T) {
	var many strings.Builder
	many.WriteString(`{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[`)
	for index := 0; index <= maxYouTubeCommentLimit; index++ {
		if index > 0 {
			many.WriteByte(',')
		}
		fmt.Fprintf(&many, `{"commentRenderer":{"commentId":"c%d","contentText":{"simpleText":"x"}}}`, index)
	}
	many.WriteString(`]}}]}`)
	if _, err := parseYouTubeCommentPage([]byte(many.String())); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("comment budget error=%v", err)
	}

	item := `{"commentRenderer":{"commentId":"leaf","contentText":{"simpleText":"x"}}}`
	for depth := 0; depth < 9; depth++ {
		item = fmt.Sprintf(`{"commentThreadRenderer":{
			"comment":{"commentRenderer":{"commentId":"d%d","contentText":{"simpleText":"x"}}},
			"replies":{"commentRepliesRenderer":{"subThreads":[%s]}}
		}}`, depth, item)
	}
	nested := `{"onResponseReceivedActions":[{"appendContinuationItemsAction":{"continuationItems":[` + item + `]}}]}`
	if _, err := parseYouTubeCommentPage([]byte(nested)); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("nesting budget error=%v", err)
	}
}

func FuzzParseYouTubeCommentPage(f *testing.F) {
	f.Add(readYouTubeFixture(f, "comments-header.json"))
	f.Add(readYouTubeFixture(f, "comments-page.json"))
	f.Add(readYouTubeFixture(f, "comments-page-2.json"))
	f.Add(readYouTubeFixture(f, "comments-disabled.json"))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if int64(len(data)) > maxExtractorJSONBytes {
			return
		}
		page, err := parseYouTubeCommentPage(data)
		if err != nil {
			return
		}
		if len(page.comments) > maxYouTubeCommentLimit {
			t.Fatalf("comments=%d", len(page.comments))
		}
		for _, comment := range page.comments {
			object, ok := comment.value.Object()
			if !ok || objectString(object, "id") == "" || objectString(object, "parent") == "" {
				t.Fatalf("unsafe comment=%#v", comment)
			}
		}
	})
}
