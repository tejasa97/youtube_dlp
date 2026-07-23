package extractor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	defaultYouTubeCommentLimit = 100
	maxYouTubeCommentLimit     = 10_000
	maxYouTubeCommentPages     = 100
	maxYouTubeCommentAttempts  = 3
	maxYouTubeCommentTextBytes = 1 << 20
	maxYouTubeCommentField     = 16 << 10
)

var (
	ErrYouTubeCommentsRateLimited = errors.New("YouTube comments rate limited")
	ErrYouTubeCommentsNetwork     = errors.New("YouTube comments network failure")
	errYouTubeCommentsIncomplete  = errors.New("incomplete YouTube comment response")
)

var youtubeCommentCountPattern = regexp.MustCompile(`(?i)([0-9]+(?:\.[0-9]+)?)\s*([KMB]?)`)

type youtubeCommentPage struct {
	comments      []youtubeParsedComment
	continuations []youtubeCommentContinuation
	events        []youtubeCommentEvent
	top           youtubeCommentContinuation
	newest        youtubeCommentContinuation
	visitorData   string
	message       string
}

type youtubeCommentEvent struct {
	comment        youtubeParsedComment
	continuation   youtubeCommentContinuation
	isContinuation bool
}

type youtubeCommentParseBudget struct {
	items, comments, continuations int
}

type youtubeParsedComment struct {
	value value.Value
	depth int
}

type youtubeCommentContinuation struct {
	token         string
	clickTracking string
	parent        string
	depth         int
}

type youtubeCommentLimits struct {
	total, parents, replies, perThread, depth int
}

// youtubeCommentAuth is deliberately local to comment continuation handling.
// The WEB authentication configuration is reusable, but the API key belongs
// to this /next endpoint rather than to authenticated player recovery.
// A nil value preserves the public, cookie-agnostic comment path.
type youtubeCommentAuth struct {
	config *youtubeWEBAuthConfig
	apiKey string
}

func normalizeYouTubeCommentLimits(options YouTubeCommentOptions) (youtubeCommentLimits, error) {
	limits := youtubeCommentLimits{
		total: options.MaxComments, parents: options.MaxParents, replies: options.MaxReplies,
		perThread: options.MaxRepliesPerThread, depth: options.MaxDepth,
	}
	if limits.total == 0 {
		limits.total = defaultYouTubeCommentLimit
	}
	if limits.parents == 0 {
		limits.parents = limits.total
	}
	if limits.replies == 0 {
		limits.replies = limits.total
	}
	if limits.perThread == 0 {
		limits.perThread = min(20, limits.total)
	}
	if limits.depth == 0 {
		limits.depth = 2
	}
	for _, limit := range []int{limits.total, limits.parents, limits.replies, limits.perThread} {
		if limit < 0 || limit > maxYouTubeCommentLimit {
			return youtubeCommentLimits{}, fmt.Errorf("%w: invalid YouTube comment limit", ErrInvalidMetadata)
		}
	}
	if limits.depth < 0 || limits.depth > 8 {
		return youtubeCommentLimits{}, fmt.Errorf("%w: invalid YouTube comment depth", ErrInvalidMetadata)
	}
	return limits, nil
}

func extractYouTubeComments(ctx context.Context, transport Transport, webpage []byte, videoID string, options YouTubeCommentOptions, authenticated *youtubeCommentAuth) ([]value.Value, bool, error) {
	if !options.Enabled {
		return nil, false, nil
	}
	limits, err := normalizeYouTubeCommentLimits(options)
	if err != nil {
		return nil, false, err
	}
	sortOrder := options.Sort
	if sortOrder == "" {
		sortOrder = "new"
	}
	if sortOrder != "top" && sortOrder != "new" {
		return nil, false, fmt.Errorf("%w: invalid YouTube comment sort", ErrInvalidMetadata)
	}

	config := extractYouTubePlaylistConfig(webpage)
	if authenticated != nil {
		// Keep the page-derived endpoint key separate from the reusable WEB
		// authentication configuration. A bounded multi-ytcfg discovery result
		// supplied by the caller takes precedence over the legacy local parser.
		apiKey := authenticated.apiKey
		if apiKey == "" {
			apiKey = config.APIKey
		}
		authenticated = &youtubeCommentAuth{config: authenticated.config, apiKey: apiKey}
	}
	initial := extractYouTubeInitialCommentContinuation(webpage)
	if initial.token == "" {
		initial = youtubeCommentContinuation{token: generateYouTubeCommentContinuation(videoID), parent: "root", depth: 1}
	}
	visitorData := config.VisitorData
	comments := make([]value.Value, 0, min(limits.total, defaultYouTubeCommentLimit))
	seenIDs := make(map[string]struct{})
	pinnedIDs := make(map[string]struct{})
	acceptedIDs := make(map[string]struct{})
	repliesPerThread := make(map[string]int)
	parentCount, replyCount := 0, 0
	selectedSort := false
	seenTokens := make(map[string]struct{})
	requestAttempts := 0
	disabled, stopped := false, false
	var processContinuation func(youtubeCommentContinuation) error
	processContinuation = func(continuation youtubeCommentContinuation) error {
		if stopped {
			return nil
		}
		if continuation.depth > limits.depth ||
			continuation.parent == "root" && parentCount >= limits.parents ||
			continuation.parent != "root" && (replyCount >= limits.replies ||
				repliesPerThread[continuation.parent] >= limits.perThread) {
			return nil
		}
		if continuation.parent != "root" {
			if _, accepted := acceptedIDs[continuation.parent]; !accepted {
				return nil
			}
		}
		tokenKey := fmt.Sprintf("%d\x00%s\x00%s", continuation.depth, continuation.parent, continuation.token)
		if _, duplicate := seenTokens[tokenKey]; duplicate {
			return nil
		}
		seenTokens[tokenKey] = struct{}{}
		page, err := fetchYouTubeCommentPage(ctx, transport, continuation, visitorData, config, authenticated, &requestAttempts)
		if err != nil {
			return err
		}
		if page.visitorData != "" {
			visitorData = page.visitorData
		}
		if !selectedSort {
			sortContinuation := page.top
			if sortOrder == "new" {
				sortContinuation = page.newest
			}
			if sortContinuation.token != "" {
				selectedSort = true
				sortContinuation.parent, sortContinuation.depth = "root", 1
				return processContinuation(sortContinuation)
			}
			selectedSort = true
		}
		for _, event := range page.events {
			if event.isContinuation {
				if err := processContinuation(event.continuation); err != nil {
					return err
				}
				if stopped {
					return nil
				}
				continue
			}
			parsed := event.comment
			if parsed.depth > limits.depth {
				continue
			}
			object, _ := parsed.value.Object()
			id := objectString(object, "id")
			if id == "" {
				continue
			}
			if _, duplicate := seenIDs[id]; duplicate {
				_, wasPinned := pinnedIDs[id]
				isPinned, _ := object.Lookup("is_pinned").Bool()
				if wasPinned && !isPinned {
					continue
				}
				return nil
			}
			parent := objectString(object, "parent")
			if parent == "root" {
				if parentCount >= limits.parents {
					return nil
				}
				parentCount++
			} else {
				if _, accepted := acceptedIDs[parent]; !accepted ||
					replyCount >= limits.replies || repliesPerThread[parent] >= limits.perThread {
					continue
				}
				replyCount++
				repliesPerThread[parent]++
			}
			seenIDs[id] = struct{}{}
			acceptedIDs[id] = struct{}{}
			if pinned, _ := object.Lookup("is_pinned").Bool(); pinned {
				pinnedIDs[id] = struct{}{}
			}
			comments = append(comments, parsed.value)
			if len(comments) >= limits.total {
				stopped = true
				return nil
			}
		}
		if len(comments) == 0 && page.message != "" {
			disabled = true
		}
		return nil
	}
	if err := processContinuation(initial); err != nil {
		return nil, false, err
	}
	if disabled && len(comments) == 0 {
		return nil, true, nil
	}
	return comments, false, nil
}

func extractYouTubeInitialCommentContinuation(webpage []byte) youtubeCommentContinuation {
	raw, err := extractJSONObject(webpage, youtubeInitialDataMarker)
	if err != nil {
		return youtubeCommentContinuation{}
	}
	var root value.Value
	if json.Unmarshal(raw, &root) != nil {
		return youtubeCommentContinuation{}
	}
	var continuation youtubeCommentContinuation
	nodes := 0
	_ = walkOrderedJSON(root, 0, &nodes, func(key string, object *value.Object) {
		if continuation.token != "" || key != "itemSectionRenderer" {
			return
		}
		section := objectString(object, "sectionIdentifier")
		if section != "comment-item-section" && section != "engagement-panel-comments-section" {
			return
		}
		sectionNodes := 0
		_ = walkOrderedJSON(value.ObjectValue(object), 0, &sectionNodes, func(childKey string, child *value.Object) {
			if continuation.token == "" {
				if candidate, ok := youtubeCommentContinuationNode(childKey, child, "root", 1); ok {
					continuation = candidate
				}
			}
		})
	})
	return continuation
}

func generateYouTubeCommentContinuation(videoID string) string {
	raw := "\x12\r\x12\x0b" + videoID + "\x18\x062'\"\x11\"\x0b" + videoID + "0\x00x\x020\x00B\x10comments-section"
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func fetchYouTubeCommentPage(ctx context.Context, transport Transport, continuation youtubeCommentContinuation, visitorData string, config youtubePlaylistConfig, authenticated *youtubeCommentAuth, requestAttempts *int) (youtubeCommentPage, error) {
	if validYouTubeContinuationToken(continuation.token) == "" {
		return youtubeCommentPage{}, fmt.Errorf("%w: invalid YouTube comment continuation", ErrInvalidPlaylist)
	}
	version := config.ClientVersion
	if authenticated != nil && authenticated.config != nil && authenticated.config.ClientVersion != "" {
		version = authenticated.config.ClientVersion
	}
	if version == "" {
		version = youtubeDefaultClientVersion
	}
	payload := map[string]any{
		"context": map[string]any{"client": map[string]any{
			"clientName": "WEB", "clientVersion": version, "hl": "en",
			"timeZone": "UTC", "utcOffsetMinutes": 0, "visitorData": visitorData,
		}},
		"continuation": continuation.token,
	}
	if continuation.clickTracking != "" {
		payload["clickTracking"] = map[string]any{"clickTrackingParams": continuation.clickTracking}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return youtubeCommentPage{}, fmt.Errorf("%w: encode YouTube comment request", ErrInvalidMetadata)
	}
	endpoint, _ := url.Parse("https://www.youtube.com/youtubei/v1/next")
	query := endpoint.Query()
	query.Set("prettyPrint", "false")
	apiKey := config.APIKey
	if authenticated != nil {
		apiKey = authenticated.apiKey
	}
	if apiKey != "" {
		query.Set("key", apiKey)
	}
	endpoint.RawQuery = query.Encode()
	var lastErr error
	for attempt := 0; attempt < maxYouTubeCommentAttempts; attempt++ {
		if requestAttempts == nil || *requestAttempts >= maxYouTubeCommentPages {
			return youtubeCommentPage{}, fmt.Errorf("%w: YouTube comment request limit", ErrPlaylistLimit)
		}
		*requestAttempts++
		var response json.RawMessage
		var requestErr error
		if authenticated != nil {
			if authenticated.config == nil {
				return youtubeCommentPage{}, ErrAuthentication
			}
			authConfig := *authenticated.config
			// Continuation responses can rotate visitor data. Every subsequent
			// authenticated request uses that value both in the JSON context and
			// X-Goog-Visitor-Id generated by the auth helper.
			authConfig.VisitorData = visitorData
			authConfig.ClientVersion = version
			requestErr = requestAuthenticatedYouTubeWEBNext(ctx, transport, endpoint.String(), body, authConfig, time.Now, &response)
		} else {
			headers := make(http.Header)
			headers.Set("Content-Type", "application/json")
			headers.Set("Origin", "https://www.youtube.com")
			headers.Set("X-Youtube-Client-Name", "1")
			headers.Set("X-Youtube-Client-Version", version)
			if visitorData != "" {
				headers.Set("X-Goog-Visitor-Id", visitorData)
			}
			requestErr = RequestJSON(ctx, transport, http.MethodPost, endpoint.String(), body, headers, &response)
		}
		if requestErr != nil {
			lastErr = requestErr
			if attempt+1 < maxYouTubeCommentAttempts && retryableYouTubeCommentError(requestErr) {
				if err := waitYouTubeCommentRetry(ctx, attempt); err != nil {
					return youtubeCommentPage{}, err
				}
				continue
			}
			return youtubeCommentPage{}, categorizeYouTubeCommentError(requestErr)
		}
		page, err := parseYouTubeCommentPageFor(response, continuation.parent, continuation.depth)
		if err == nil {
			return page, nil
		}
		lastErr = err
		if attempt+1 >= maxYouTubeCommentAttempts || !errors.Is(err, errYouTubeCommentsIncomplete) {
			return youtubeCommentPage{}, err
		}
		if err := waitYouTubeCommentRetry(ctx, attempt); err != nil {
			return youtubeCommentPage{}, err
		}
	}
	return youtubeCommentPage{}, lastErr
}

func waitYouTubeCommentRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(attempt+1) * 10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryableYouTubeCommentError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, ErrJSONResponseTooLarge) || errors.Is(err, ErrInvalidMetadata) ||
		errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) || errors.Is(err, ErrInvalidPlaylist) {
		return false
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return status.Code == http.StatusRequestTimeout || status.Code >= 500 && status.Code <= 599
	}
	return true
}

func categorizeYouTubeCommentError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, ErrInvalidMetadata) || errors.Is(err, ErrJSONResponseTooLarge) ||
		errors.Is(err, ErrInvalidPlaylist) || errors.Is(err, ErrAuthentication) ||
		errors.Is(err, ErrUnavailable) {
		return err
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		switch status.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrAuthentication
		case http.StatusNotFound, http.StatusGone:
			return ErrUnavailable
		case http.StatusTooManyRequests:
			return ErrYouTubeCommentsRateLimited
		}
	}
	return fmt.Errorf("%w: request failed", ErrYouTubeCommentsNetwork)
}

func parseYouTubeCommentPage(data []byte) (youtubeCommentPage, error) {
	return parseYouTubeCommentPageFor(data, "root", 1)
}

func parseYouTubeCommentPageFor(data []byte, parent string, depth int) (youtubeCommentPage, error) {
	var root value.Value
	if err := json.Unmarshal(data, &root); err != nil {
		return youtubeCommentPage{}, fmt.Errorf("%w: decode YouTube comments", ErrInvalidMetadata)
	}
	rootObject, ok := root.Object()
	if !ok {
		return youtubeCommentPage{}, fmt.Errorf("%w: YouTube comments root", ErrInvalidMetadata)
	}
	items, recognized := youtubeCommentContinuationItems(rootObject)
	if !recognized {
		return youtubeCommentPage{}, fmt.Errorf("%w: %w", ErrInvalidMetadata, errYouTubeCommentsIncomplete)
	}
	entities := youtubeCommentEntities(rootObject)
	var page youtubeCommentPage
	budget := &youtubeCommentParseBudget{}
	if err := parseYouTubeCommentItems(&page, items, entities, parent, depth, 1, budget); err != nil {
		return youtubeCommentPage{}, err
	}
	page.visitorData = objectString(rootObject, "responseContext", "visitorData")
	return page, nil
}

func parseYouTubeCommentItems(page *youtubeCommentPage, items []value.Value, entities youtubeCommentEntitySet, parent string, depth, nesting int, budget *youtubeCommentParseBudget) error {
	if nesting > 8 {
		return fmt.Errorf("%w: YouTube comment nesting limit", ErrPlaylistLimit)
	}
	for _, item := range items {
		budget.items++
		if budget.items > 2*maxYouTubeCommentLimit {
			return fmt.Errorf("%w: YouTube comment item limit", ErrPlaylistLimit)
		}
		object, ok := item.Object()
		if !ok {
			continue
		}
		if header, ok := object.Lookup("commentsHeaderRenderer").Object(); ok {
			page.top, page.newest = youtubeCommentSortContinuations(header)
			continue
		}
		if thread, ok := object.Lookup("commentThreadRenderer").Object(); ok {
			if comment, ok, err := youtubeCommentFromThread(thread, entities, parent); err != nil {
				return err
			} else if ok {
				if err := page.appendComment(comment, depth, budget); err != nil {
					return err
				}
				parent := objectStringValue(comment, "id")
				replies, _ := thread.Lookup("replies").Object()
				replyRenderer, _ := replies.Lookup("commentRepliesRenderer").Object()
				for _, key := range []string{"contents", "subThreads"} {
					replyItems, _ := replyRenderer.Lookup(key).ListValue()
					if err := parseYouTubeCommentItems(page, replyItems, entities, parent, depth+1, nesting+1, budget); err != nil {
						return err
					}
				}
			}
			continue
		}
		if renderer, ok := object.Lookup("commentRenderer").Object(); ok {
			comment, valid, err := youtubeLegacyComment(renderer, parent)
			if err != nil {
				return err
			}
			if valid {
				if err := page.appendComment(comment, depth, budget); err != nil {
					return err
				}
			}
			continue
		}
		if viewModel, ok := object.Lookup("commentViewModel").Object(); ok {
			comment, valid, err := youtubeModernComment(viewModel, entities, parent)
			if err != nil {
				return err
			}
			if valid {
				if err := page.appendComment(comment, depth, budget); err != nil {
					return err
				}
			}
			continue
		}
		for _, continuation := range youtubeCommentContinuations(item, parent, depth) {
			if err := page.appendContinuation(continuation, budget); err != nil {
				return err
			}
		}
		if messageRenderer, ok := object.Lookup("messageRenderer").Object(); ok && page.message == "" {
			page.message = rendererText(messageRenderer.Lookup("text"))
		}
	}
	return nil
}

func (page *youtubeCommentPage) appendComment(comment value.Value, depth int, budget *youtubeCommentParseBudget) error {
	budget.comments++
	if budget.comments > maxYouTubeCommentLimit {
		return fmt.Errorf("%w: YouTube comment count limit", ErrPlaylistLimit)
	}
	parsed := youtubeParsedComment{value: comment, depth: depth}
	page.comments = append(page.comments, parsed)
	page.events = append(page.events, youtubeCommentEvent{comment: parsed})
	return nil
}

func (page *youtubeCommentPage) appendContinuation(continuation youtubeCommentContinuation, budget *youtubeCommentParseBudget) error {
	budget.continuations++
	if budget.continuations > maxYouTubeCommentLimit {
		return fmt.Errorf("%w: YouTube comment continuation limit", ErrPlaylistLimit)
	}
	page.continuations = append(page.continuations, continuation)
	page.events = append(page.events, youtubeCommentEvent{continuation: continuation, isContinuation: true})
	return nil
}

func youtubeCommentContinuationItems(root *value.Object) ([]value.Value, bool) {
	var result []value.Value
	recognized := false
	for _, key := range []string{"onResponseReceivedEndpoints", "onResponseReceivedActions"} {
		actions, ok := root.Lookup(key).ListValue()
		if !ok {
			continue
		}
		for _, actionValue := range actions {
			action, ok := actionValue.Object()
			if !ok {
				continue
			}
			for _, command := range []string{"reloadContinuationItemsCommand", "appendContinuationItemsAction"} {
				container, ok := action.Lookup(command).Object()
				if !ok {
					continue
				}
				if items, ok := container.Lookup("continuationItems").ListValue(); ok {
					recognized = true
					result = append(result, items...)
				}
			}
		}
	}
	return result, recognized
}

func youtubeCommentSortContinuations(header *value.Object) (top, newest youtubeCommentContinuation) {
	var continuations []youtubeCommentContinuation
	sortMenu, _ := header.Lookup("sortMenu").Object()
	submenu, _ := sortMenu.Lookup("sortFilterSubMenuRenderer").Object()
	items, _ := submenu.Lookup("subMenuItems").ListValue()
	for _, item := range items {
		object, _ := item.Object()
		for _, key := range []string{"serviceEndpoint", "continuationEndpoint"} {
			endpoint, _ := object.Lookup(key).Object()
			if continuation, ok := youtubeCommentEndpointContinuation(endpoint); ok {
				continuations = append(continuations, continuation)
				break
			}
		}
	}
	if len(continuations) > 0 {
		top = continuations[0]
	}
	if len(continuations) > 1 {
		newest = continuations[1]
	}
	return top, newest
}

func youtubeCommentContinuations(input value.Value, parent string, depth int) []youtubeCommentContinuation {
	var result []youtubeCommentContinuation
	nodes := 0
	_ = walkOrderedJSON(input, 0, &nodes, func(key string, object *value.Object) {
		if continuation, ok := youtubeCommentContinuationNode(key, object, parent, depth); ok {
			result = append(result, continuation)
		}
	})
	return result
}

func youtubeCommentContinuationNode(key string, object *value.Object, parent string, depth int) (youtubeCommentContinuation, bool) {
	var endpoint *value.Object
	switch key {
	case "continuationItemRenderer":
		endpoint, _ = object.Lookup("continuationEndpoint").Object()
		if endpoint == nil {
			endpoint, _ = objectValue(object, "button", "buttonRenderer", "command").Object()
		}
	case "continuationItemViewModel":
		endpoint, _ = objectValue(object, "continuationCommand", "innertubeCommand").Object()
	case "nextContinuationData", "reloadContinuationData":
		token := validYouTubeContinuationToken(objectString(object, "continuation"))
		if token == "" {
			return youtubeCommentContinuation{}, false
		}
		return youtubeCommentContinuation{
			token: token, clickTracking: validYouTubeCommentClickTracking(objectString(object, "clickTrackingParams")),
			parent: parent, depth: depth,
		}, true
	default:
		return youtubeCommentContinuation{}, false
	}
	continuation, ok := youtubeCommentEndpointContinuation(endpoint)
	if ok {
		continuation.parent, continuation.depth = parent, depth
	}
	return continuation, ok
}

func youtubeCommentEndpointContinuation(endpoint *value.Object) (youtubeCommentContinuation, bool) {
	if endpoint == nil {
		return youtubeCommentContinuation{}, false
	}
	candidates := []*value.Object{endpoint}
	if executor, ok := endpoint.Lookup("commandExecutorCommand").Object(); ok {
		if commands, ok := executor.Lookup("commands").ListValue(); ok && len(commands) <= youtubeMaxContinuationCommands {
			for _, command := range commands {
				if object, ok := command.Object(); ok {
					candidates = append(candidates, object)
				}
			}
		}
	}
	for _, candidate := range candidates {
		token := validYouTubeContinuationToken(objectString(candidate, "continuationCommand", "token"))
		if token == "" {
			continue
		}
		clickTracking := objectString(candidate, "clickTrackingParams")
		if clickTracking == "" {
			clickTracking = objectString(endpoint, "clickTrackingParams")
		}
		clickTracking = validYouTubeCommentClickTracking(clickTracking)
		return youtubeCommentContinuation{token: token, clickTracking: clickTracking}, true
	}
	return youtubeCommentContinuation{}, false
}

func validYouTubeCommentClickTracking(input string) string {
	if len(input) > maxYouTubeCommentField || strings.ContainsAny(input, "\x00\r\n") {
		return ""
	}
	return input
}

type youtubeCommentEntitySet map[string]*value.Object

func youtubeCommentEntities(root *value.Object) youtubeCommentEntitySet {
	result := make(youtubeCommentEntitySet)
	mutations, _ := objectValue(root, "frameworkUpdates", "entityBatchUpdate", "mutations").ListValue()
	for _, mutationValue := range mutations {
		mutation, _ := mutationValue.Object()
		key := objectString(mutation, "entityKey")
		payload, _ := mutation.Lookup("payload").Object()
		if key != "" && payload != nil {
			result[key] = payload
		}
	}
	return result
}

func youtubeCommentFromThread(thread *value.Object, entities youtubeCommentEntitySet, parent string) (value.Value, bool, error) {
	if commentContainer, ok := thread.Lookup("comment").Object(); ok {
		if renderer, ok := commentContainer.Lookup("commentRenderer").Object(); ok {
			return youtubeLegacyComment(renderer, parent)
		}
	}
	if renderer, ok := thread.Lookup("commentRenderer").Object(); ok {
		return youtubeLegacyComment(renderer, parent)
	}
	if container, ok := thread.Lookup("commentViewModel").Object(); ok {
		if viewModel, ok := container.Lookup("commentViewModel").Object(); ok {
			return youtubeModernComment(viewModel, entities, parent)
		}
		return youtubeModernComment(container, entities, parent)
	}
	return value.Missing(), false, nil
}

func youtubeLegacyComment(renderer *value.Object, parent string) (value.Value, bool, error) {
	id := objectString(renderer, "commentId")
	if id == "" || len(id) > maxYouTubeCommentField || strings.ContainsAny(id, "\x00\r\n") {
		return value.Missing(), false, nil
	}
	text := rendererText(renderer.Lookup("contentText"))
	if len(text) > maxYouTubeCommentTextBytes {
		return value.Missing(), false, fmt.Errorf("%w: YouTube comment text limit", ErrInvalidMetadata)
	}
	object := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "text", Value: value.String(text)},
		value.Field{Key: "parent", Value: value.String(firstNonEmpty(parent, "root"))},
	)
	setYouTubeCommentString(object, "author_id", objectString(renderer, "authorEndpoint", "browseEndpoint", "browseId"))
	setYouTubeCommentString(object, "author", rendererText(renderer.Lookup("authorText")))
	setYouTubeCommentString(object, "author_thumbnail", youtubeLastThumbnail(renderer.Lookup("authorThumbnail")))
	setYouTubeCommentString(object, "author_url", youtubeCommentAuthorURL(renderer))
	setYouTubeCommentString(object, "_time_text", rendererText(renderer.Lookup("publishedTimeText")))
	if count, ok := parseYouTubeCommentCount(rendererText(renderer.Lookup("voteCount"))); ok {
		object.Set("like_count", value.Int(count))
	}
	if uploader, ok := renderer.Lookup("authorIsChannelOwner").Bool(); ok {
		object.Set("author_is_uploader", value.Bool(uploader))
	}
	if badge, ok := renderer.Lookup("pinnedCommentBadge").Object(); ok && badge.Len() > 0 {
		object.Set("is_pinned", value.Bool(true))
	}
	actionButtons, hasActions := renderer.Lookup("actionButtons").Object()
	if hasActions {
		actions, _ := actionButtons.Lookup("commentActionButtonsRenderer").Object()
		object.Set("is_favorited", value.Bool(!actions.Lookup("creatorHeart").IsMissing()))
	}
	return value.ObjectValue(object), true, nil
}

func youtubeModernComment(viewModel *value.Object, entities youtubeCommentEntitySet, parent string) (value.Value, bool, error) {
	keys := []string{objectString(viewModel, "commentKey"), objectString(viewModel, "toolbarStateKey")}
	var commentPayload, toolbarPayload *value.Object
	for _, key := range keys {
		payload := entities[key]
		if payload == nil {
			continue
		}
		if object, ok := payload.Lookup("commentEntityPayload").Object(); ok {
			commentPayload = object
		}
		if object, ok := payload.Lookup("engagementToolbarStateEntityPayload").Object(); ok {
			toolbarPayload = object
		}
	}
	id := objectString(commentPayload, "properties", "commentId")
	if id == "" || len(id) > maxYouTubeCommentField || strings.ContainsAny(id, "\x00\r\n") {
		return value.Missing(), false, nil
	}
	text := objectString(commentPayload, "properties", "content", "content")
	if len(text) > maxYouTubeCommentTextBytes {
		return value.Missing(), false, fmt.Errorf("%w: YouTube comment text limit", ErrInvalidMetadata)
	}
	object := value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "text", Value: value.String(text)},
		value.Field{Key: "parent", Value: value.String(firstNonEmpty(parent, "root"))},
	)
	setYouTubeCommentString(object, "author_id", objectString(commentPayload, "author", "channelId"))
	setYouTubeCommentString(object, "author", objectString(commentPayload, "author", "displayName"))
	setYouTubeCommentString(object, "author_thumbnail", safeYouTubeCommentURL(objectString(commentPayload, "author", "avatarThumbnailUrl"), false))
	setYouTubeCommentString(object, "author_url", youtubeModernCommentAuthorURL(commentPayload))
	setYouTubeCommentString(object, "_time_text", objectString(commentPayload, "properties", "publishedTime"))
	if count, ok := parseYouTubeCommentCount(objectString(commentPayload, "toolbar", "likeCountA11y")); ok {
		object.Set("like_count", value.Int(count))
	}
	for field, key := range map[string]string{"author_is_uploader": "isCreator", "author_is_verified": "isVerified"} {
		if boolean, ok := objectValue(commentPayload, "author", key).Bool(); ok {
			object.Set(field, value.Bool(boolean))
		}
	}
	if toolbarPayload != nil {
		object.Set("is_favorited", value.Bool(objectString(toolbarPayload, "heartState") == "TOOLBAR_HEART_STATE_HEARTED"))
	}
	if pinnedText, ok := viewModel.Lookup("pinnedText").StringValue(); ok && pinnedText != "" {
		object.Set("is_pinned", value.Bool(true))
	}
	return value.ObjectValue(object), true, nil
}

func objectValue(object *value.Object, path ...string) value.Value {
	if object == nil {
		return value.Missing()
	}
	current := value.ObjectValue(object)
	for _, key := range path {
		nested, ok := current.Object()
		if !ok {
			return value.Missing()
		}
		current = nested.Lookup(key)
	}
	return current
}

func objectStringValue(input value.Value, key string) string {
	object, _ := input.Object()
	return objectString(object, key)
}

func setYouTubeCommentString(object *value.Object, key, text string) {
	if text == "" || len(text) > maxYouTubeCommentField || strings.ContainsRune(text, 0) {
		return
	}
	object.Set(key, value.String(text))
}

func youtubeLastThumbnail(input value.Value) string {
	object, _ := input.Object()
	thumbnails, _ := object.Lookup("thumbnails").ListValue()
	if len(thumbnails) == 0 {
		return ""
	}
	last, _ := thumbnails[len(thumbnails)-1].Object()
	return safeYouTubeCommentURL(objectString(last, "url"), false)
}

func youtubeCommentAuthorURL(renderer *value.Object) string {
	raw := objectString(renderer, "authorEndpoint", "browseEndpoint", "canonicalBaseUrl")
	if raw == "" {
		raw = objectString(renderer, "authorEndpoint", "commandMetadata", "webCommandMetadata", "url")
	}
	return safeYouTubeCommentURL(raw, true)
}

func youtubeModernCommentAuthorURL(payload *value.Object) string {
	raw := objectString(payload, "author", "channelCommand", "innertubeCommand", "browseEndpoint", "canonicalBaseUrl")
	if raw == "" {
		raw = objectString(payload, "author", "channelCommand", "innertubeCommand", "commandMetadata", "webCommandMetadata", "url")
	}
	return safeYouTubeCommentURL(raw, true)
}

func safeYouTubeCommentURL(raw string, allowRelative bool) string {
	if raw == "" || len(raw) > maxYouTubeCommentField {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil {
		return ""
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return ""
		}
		if allowRelative {
			host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
			if host != "youtube.com" && !strings.HasSuffix(host, ".youtube.com") {
				return ""
			}
		}
		return parsed.String()
	}
	if !allowRelative || !strings.HasPrefix(parsed.Path, "/") {
		return ""
	}
	base, _ := url.Parse("https://www.youtube.com")
	return base.ResolveReference(parsed).String()
}

func parseYouTubeCommentCount(input string) (int64, bool) {
	text := strings.TrimSpace(strings.ReplaceAll(input, ",", ""))
	if text == "" {
		return 0, false
	}
	match := youtubeCommentCountPattern.FindStringSubmatch(text)
	if match == nil {
		return 0, false
	}
	multiplier := float64(1)
	switch strings.ToUpper(match[2]) {
	case "K":
		multiplier = 1e3
	case "M":
		multiplier = 1e6
	case "B":
		multiplier = 1e9
	}
	number, err := strconv.ParseFloat(match[1], 64)
	result := math.Round(number * multiplier)
	if err != nil || number < 0 || math.IsNaN(result) || math.IsInf(result, 0) || result > math.MaxInt64 {
		return 0, false
	}
	return int64(result), true
}

func boundedDiagnostic(input string) string {
	input = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, input)
	if len(input) > 256 {
		input = input[:256]
	}
	return input
}
