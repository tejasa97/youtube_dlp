package ytdlp

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

// failingSelectionExtractor is a deterministic playlist extractor that emits a
// controllable error at a configured 1-based source index. The injection
// modes are:
//
//   - "entry": the entry-level process() call returns a categorized error.
//   - "iterator": the iterator Next call itself returns the error.
//   - "cancel": the iterator returns context.Canceled.
type failingSelectionExtractor struct {
	pageFetches *atomic.Int32
	failAtIndex int
	failError   error
	failMode    string
}

func (*failingSelectionExtractor) Name() string { return "failing-selection" }

func (*failingSelectionExtractor) Suitable(parsed *url.URL) bool {
	return parsed.Path == "/failing-selection"
}

func (f *failingSelectionExtractor) Extract(ctx context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	base := parsed.Scheme + "://" + parsed.Host
	sequence, err := extractor.OnDemandEntries(2, func(_ context.Context, page int) ([]extractor.Entry, error) {
		f.pageFetches.Add(1)
		if page > 2 {
			return nil, nil
		}
		first, last := page*2+1, page*2+2
		if last > 5 {
			last = 5
		}
		entries := make([]extractor.Entry, 0, last-first+1)
		for index := first; index <= last; index++ {
			entries = append(entries, extractor.Entry{
				URL:          base + "/media" + strconv.Itoa(index) + ".mp4",
				ExtractorKey: "generic",
				ID:           "item-" + strconv.Itoa(index),
				Title:        "Item " + strconv.Itoa(index),
			})
		}
		return entries, nil
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("failing-selection")},
		value.Field{Key: "title", Value: value.String("Failing Selection")},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	wrapped := &failingEntryIterator{
		source:  sequence.Iterator(),
		page:    f.failAtIndex,
		err:     f.failError,
		mode:    f.failMode,
		failURL: base + "/fail",
	}
	return extractor.Playlist(info, failingEntrySequence{inner: wrapped})
}

// failingEntrySequence adapts a single *failingEntryIterator to the
// EntrySequence contract required by extractor.Playlist.
type failingEntrySequence struct {
	inner *failingEntryIterator
}

func (sequence failingEntrySequence) Iterator() extractor.EntryIterator {
	return sequence.inner
}

// failEntryExtractor matches URLs with the /fail/N path and surfaces the
// supplied error from Extract. The failingEntryIterator swaps the entry
// URL to this pattern at the configured failure index so per-entry errors
// flow through the regular process() path rather than the iterator.
type failEntryExtractor struct {
	err error
}

func (*failEntryExtractor) Name() string { return "fail-entry" }

func (*failEntryExtractor) Suitable(parsed *url.URL) bool {
	return strings.HasPrefix(parsed.Path, "/fail/")
}

func (f *failEntryExtractor) Extract(_ context.Context, _ extractor.Request) (extractor.Extraction, error) {
	return extractor.Extraction{}, f.err
}

// failingEntryIterator injects the configured failure at the configured
// 1-based source index. The pagination iterator itself remains healthy so
// cancellation semantics can be tested separately.
type failingEntryIterator struct {
	source  extractor.EntryIterator
	page    int
	err     error
	mode    string
	index   int
	failURL string
}

func (f *failingEntryIterator) Next(ctx context.Context) (extractor.Entry, bool, error) {
	entry, ok, err := f.source.Next(ctx)
	if err != nil || !ok {
		return entry, ok, err
	}
	f.index++
	if f.index == f.page {
		switch f.mode {
		case "iterator":
			return extractor.Entry{}, false, f.err
		case "cancel":
			return extractor.Entry{}, true, context.Canceled
		case "entry":
			entry.URL = f.failURL + "/" + strconv.Itoa(f.index) + ".mp4"
			entry.ExtractorKey = ""
		}
	}
	return entry, ok, nil
}

// failingEntryIterator injects the configured failure at the configured
// 1-based source index. The pagination iterator itself remains healthy so
// cancellation semantics can be tested separately.

// runFailingSelection executes the playlist loop against the failing
// extractor and returns the captured events alongside the result/error pair.
func runFailingSelection(t *testing.T, base string, options PlaylistOptions, fixture *failingSelectionExtractor) (Result, []Event, error) {
	return runFailingSelectionWithHandler(t, base, options, fixture, nil)
}

func runFailingSelectionWithHandler(
	t *testing.T,
	base string,
	options PlaylistOptions,
	fixture *failingSelectionExtractor,
	handler EventHandler,
) (Result, []Event, error) {
	t.Helper()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var events []Event
	client := NewClient(WithEventHandler(func(handlerCtx context.Context, event Event) error {
		mu.Lock()
		events = append(events, event)
		mu.Unlock()
		if handler != nil {
			return handler(handlerCtx, event)
		}
		return nil
	}))
	operation := &operation{
		client:    client,
		request:   Request{SkipDownload: true, Playlist: options},
		transport: transport,
		registry:  extractor.NewRegistry(fixture, &failEntryExtractor{err: fixture.failError}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), base+"/failing-selection", "", nil, make(map[string]bool), 0)
	mu.Lock()
	defer mu.Unlock()
	return result, append([]Event(nil), events...), err
}

func TestPlaylistErrorPolicyDefaultsToContinue(t *testing.T) {
	if PlaylistErrorContinue.String() != "continue" || PlaylistErrorAbort.String() != "abort" {
		t.Fatalf("strings = %q, %q", PlaylistErrorContinue.String(), PlaylistErrorAbort.String())
	}
	if !PlaylistErrorContinue.Valid() || !PlaylistErrorAbort.Valid() {
		t.Errorf("Valid() returned false on a defined constant")
	}
	if PlaylistErrorPolicy(99).Valid() {
		t.Errorf("Invalid value reported Valid")
	}
}

func TestValidateAcceptsPlaylistRandomAndReverseForPinnedPrecedence(t *testing.T) {
	if err := validateRequestOptions(Request{Playlist: PlaylistOptions{Random: true, Reverse: true}}); err != nil {
		t.Fatalf("validation = %v", err)
	}
}

func TestValidateRejectsInvalidPlaylistErrorPolicyAndMaxFailures(t *testing.T) {
	for _, options := range []PlaylistOptions{
		{ErrorPolicy: PlaylistErrorPolicy(99)},
		{MaxFailures: -1},
		{MaxFailures: maxPlaylistEntries + 1},
	} {
		err := validateRequestOptions(Request{Playlist: options})
		if !errors.Is(err, errInvalidRequestOptions) {
			t.Errorf("validateRequestOptions(%+v) = %v", options, err)
		}
	}
	for _, options := range []PlaylistOptions{{MaxFailures: 0}, {MaxFailures: 1}, {MaxFailures: 100}} {
		if err := validateRequestOptions(Request{Playlist: options}); err != nil {
			t.Errorf("validateRequestOptions(%+v): %v", options, err)
		}
	}
}

func TestPlaylistRandomOrderIsDeterministicWithFixedSeed(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	source := func() *rand.Rand { return rand.New(rand.NewSource(0xC0FFEE)) }
	collectIDs := func() []string {
		var pages atomic.Int32
		result := runSelectionFixture(t, server.URL, PlaylistOptions{
			Items:        "1-5",
			Random:       true,
			RandomSource: source,
		}, &pages)
		ids := make([]string, 0, len(result.Entries))
		for _, entry := range result.Entries {
			var metadata map[string]any
			if err := json.Unmarshal(entry.InfoJSON, &metadata); err != nil {
				t.Fatal(err)
			}
			ids = append(ids, metadata["id"].(string))
		}
		return ids
	}
	first := collectIDs()
	second := collectIDs()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("shuffled order is not deterministic across runs: %v vs %v", first, second)
	}
	sortedCopy := append([]string(nil), first...)
	sort.Strings(sortedCopy)
	if reflect.DeepEqual(first, sortedCopy) {
		t.Fatalf("shuffle left the slice ordered: %v", first)
	}
	wantMedia := make([]string, 0, len(first))
	for _, id := range first {
		wantMedia = append(wantMedia, "/media"+strings.TrimPrefix(id, "item-")+".mp4")
	}
	wantRequests := append(append([]string(nil), wantMedia...), wantMedia...)
	if got := requests(); !reflect.DeepEqual(got, wantRequests) {
		t.Fatalf("media requests = %v; want %v", got, wantRequests)
	}
}

func TestPlaylistLazyPlusReverseEmitsWarningAndDoesNotReverse(t *testing.T) {
	server, requests := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	var warning string
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventMetadataWarning {
			warning = event.Message
		}
		return nil
	}))
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client:    client,
		request:   Request{SkipDownload: true, Playlist: PlaylistOptions{Lazy: true, Reverse: true}},
		transport: transport,
		registry:  extractor.NewRegistry(&selectionFixtureExtractor{pageFetches: &pages}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/selection", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if warning != "--playlist-reverse is ignored since --lazy-playlist was given" {
		t.Fatalf("warning = %q", warning)
	}
	assertSelectedPlaylist(t, result, []string{"item-1", "item-2", "item-3", "item-4", "item-5"}, []float64{1, 2, 3, 4, 5})
	if got := requests(); !reflect.DeepEqual(got, []string{"/media1.mp4", "/media2.mp4", "/media3.mp4", "/media4.mp4", "/media5.mp4"}) {
		t.Fatalf("media requests = %v", got)
	}
}

func TestPlaylistRandomOverridesReverseWithWarning(t *testing.T) {
	effective, warnings := normalizedPlaylistExecutionOptions(PlaylistOptions{Reverse: true, Random: true})
	if effective.Reverse || !effective.Random || !reflect.DeepEqual(warnings, []string{"--playlist-reverse is ignored since --playlist-random was given"}) {
		t.Fatalf("effective=%+v warnings=%v", effective, warnings)
	}
	effective, warnings = normalizedPlaylistExecutionOptions(PlaylistOptions{Reverse: true, Random: true, Lazy: true})
	if effective.Reverse || effective.Random || !effective.Lazy || !reflect.DeepEqual(warnings, []string{
		"--playlist-reverse is ignored since --playlist-random was given",
		"--playlist-random is ignored since --lazy-playlist was given",
	}) {
		t.Fatalf("effective=%+v warnings=%v", effective, warnings)
	}
}

func TestPlaylistContinuePolicyContinuesPastEntryFailure(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 3, failError: errors.New("entry gone"), failMode: "entry"}
	result, events, err := runFailingSelection(t, server.URL, PlaylistOptions{
		ErrorPolicy: PlaylistErrorContinue,
	}, failing)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 4 {
		t.Fatalf("entries = %d; want 4 (one skipped)", len(result.Entries))
	}
	if result.SuppressedFailures != 1 {
		t.Fatalf("suppressed failures = %d; want 1", result.SuppressedFailures)
	}
	recorded := eventsByKind(events, playlistEntryErrorEventKind)
	if len(recorded) != 1 {
		t.Fatalf("recorded events = %d; want 1", len(recorded))
	}
	if !strings.Contains(recorded[0].Message, "playlist entry 3: ") {
		t.Fatalf("event message = %q", recorded[0].Message)
	}
}

func TestPlaylistAbortPolicyAbortsOnFirstEntryFailure(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 3, failError: errors.New("entry gone"), failMode: "entry"}
	result, _, err := runFailingSelection(t, server.URL, PlaylistOptions{
		ErrorPolicy: PlaylistErrorAbort,
	}, failing)
	if err == nil {
		t.Fatal("expected propagated error")
	}
	if !strings.Contains(err.Error(), "playlist entry 3") {
		t.Fatalf("error = %v", err)
	}
	if len(result.Entries) != 0 {
		t.Fatalf("entries = %d; abort returns no finalized partial playlist", len(result.Entries))
	}
}

func TestPlaylistCancellationPropagatesUnderEveryErrorPolicy(t *testing.T) {
	for _, mode := range []PlaylistErrorPolicy{PlaylistErrorContinue, PlaylistErrorAbort} {
		server, _ := selectionMediaServer(t)
		failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 3, failMode: "cancel"}
		_, _, err := runFailingSelection(t, server.URL, PlaylistOptions{ErrorPolicy: mode}, failing)
		server.Close()
		if !errors.Is(err, context.Canceled) {
			t.Errorf("mode %s: error = %v; want context.Canceled", mode, err)
		}
	}
}

func TestPlaylistMaxFailuresStopsAtConfiguredBound(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 3, failError: errors.New("entry gone"), failMode: "entry"}
	result, events, err := runFailingSelection(t, server.URL, PlaylistOptions{
		ErrorPolicy: PlaylistErrorContinue,
		MaxFailures: 1,
	}, failing)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 {
		t.Fatalf("entries = %d; want 2 after the configured bound", len(result.Entries))
	}
	if result.SuppressedFailures != 1 || len(eventsByKind(events, playlistMaxFailuresEventKind)) != 1 {
		t.Fatalf("suppressed=%d threshold events=%d", result.SuppressedFailures, len(eventsByKind(events, playlistMaxFailuresEventKind)))
	}
}

func TestPlaylistExtractorPlaylistLimitIsNotSwallowed(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 2, failError: extractor.ErrPlaylistLimit, failMode: "iterator"}
	_, _, err := runFailingSelection(t, server.URL, PlaylistOptions{
		ErrorPolicy: PlaylistErrorContinue,
	}, failing)
	if !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("error = %v; want ErrPlaylistLimit", err)
	}
}

func TestPlaylistInvalidRequestOptionsIsNotSwallowed(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	failing := &failingSelectionExtractor{pageFetches: new(atomic.Int32), failAtIndex: 2, failError: errInvalidRequestOptions, failMode: "iterator"}
	_, _, err := runFailingSelection(t, server.URL, PlaylistOptions{
		ErrorPolicy: PlaylistErrorContinue,
	}, failing)
	if !errors.Is(err, errInvalidRequestOptions) {
		t.Fatalf("error = %v; want errInvalidRequestOptions", err)
	}
}

func TestPlaylistSecurityAndEventHandlerFailuresAreNeverSuppressed(t *testing.T) {
	server, _ := selectionMediaServer(t)
	defer server.Close()
	securityFailure := &Error{Category: ErrorSecurity, Op: "policy", Err: errors.New("private")}
	fixture := &failingSelectionExtractor{
		pageFetches: new(atomic.Int32), failAtIndex: 2, failError: securityFailure, failMode: "entry",
	}
	securityResult, securityEvents, err := runFailingSelection(t, server.URL, PlaylistOptions{}, fixture)
	if !IsCategory(err, ErrorSecurity) {
		t.Fatalf("security error=%v result=%+v events=%+v", err, securityResult, securityEvents)
	}

	handlerFailure := errors.New("event handler failed")
	fixture = &failingSelectionExtractor{
		pageFetches: new(atomic.Int32), failAtIndex: 2, failError: errors.New("ordinary"), failMode: "entry",
	}
	_, _, err = runFailingSelectionWithHandler(t, server.URL, PlaylistOptions{}, fixture, func(_ context.Context, event Event) error {
		if event.Kind == playlistEntryErrorEventKind {
			return handlerFailure
		}
		return nil
	})
	if !errors.Is(err, handlerFailure) {
		t.Fatalf("entry event error=%v", err)
	}

	fixture = &failingSelectionExtractor{
		pageFetches: new(atomic.Int32), failAtIndex: 2, failError: errors.New("ordinary"), failMode: "entry",
	}
	_, _, err = runFailingSelectionWithHandler(t, server.URL, PlaylistOptions{MaxFailures: 1}, fixture, func(_ context.Context, event Event) error {
		if event.Kind == playlistMaxFailuresEventKind {
			return handlerFailure
		}
		return nil
	})
	if !errors.Is(err, handlerFailure) {
		t.Fatalf("threshold event error=%v", err)
	}
}

func TestShufflePlaylistEntriesIsInPlaceAndPreservesSourceIndex(t *testing.T) {
	entries := []indexedPlaylistEntry{
		{Entry: extractor.Entry{ID: "a"}, SourceIndex: 1},
		{Entry: extractor.Entry{ID: "b"}, SourceIndex: 2},
		{Entry: extractor.Entry{ID: "c"}, SourceIndex: 3},
		{Entry: extractor.Entry{ID: "d"}, SourceIndex: 4},
	}
	original := append([]indexedPlaylistEntry(nil), entries...)
	source := func() *rand.Rand { return rand.New(rand.NewSource(0xBEEF)) }
	shufflePlaylistEntries(entries, source)
	if len(entries) != len(original) {
		t.Fatalf("length changed: %d vs %d", len(entries), len(original))
	}
	originalIndexes := make([]int, len(original))
	for i, e := range original {
		originalIndexes[i] = e.SourceIndex
	}
	gotIndexes := make([]int, len(entries))
	for i, e := range entries {
		gotIndexes[i] = e.SourceIndex
	}
	sort.Ints(originalIndexes)
	sort.Ints(gotIndexes)
	if !reflect.DeepEqual(originalIndexes, gotIndexes) {
		t.Fatalf("SourceIndex multiset changed: %v vs %v", originalIndexes, gotIndexes)
	}
}

func TestReversePlaylistEntriesSwapsInPlace(t *testing.T) {
	entries := []indexedPlaylistEntry{
		{Entry: extractor.Entry{ID: "a"}, SourceIndex: 1},
		{Entry: extractor.Entry{ID: "b"}, SourceIndex: 2},
		{Entry: extractor.Entry{ID: "c"}, SourceIndex: 3},
		{Entry: extractor.Entry{ID: "d"}, SourceIndex: 4},
	}
	reversePlaylistEntries(entries)
	want := []indexedPlaylistEntry{
		{Entry: extractor.Entry{ID: "d"}, SourceIndex: 4},
		{Entry: extractor.Entry{ID: "c"}, SourceIndex: 3},
		{Entry: extractor.Entry{ID: "b"}, SourceIndex: 2},
		{Entry: extractor.Entry{ID: "a"}, SourceIndex: 1},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Fatalf("reversed = %#v; want %#v", entries, want)
	}
}

func TestMaterializableExecutionOrderHonorsLazy(t *testing.T) {
	cases := []struct {
		options PlaylistOptions
		want    bool
	}{
		{PlaylistOptions{}, false},
		{PlaylistOptions{Reverse: true}, true},
		{PlaylistOptions{Random: true}, true},
		{PlaylistOptions{Lazy: true}, false},
		{PlaylistOptions{Lazy: true, Reverse: true}, false},
		{PlaylistOptions{Lazy: true, Random: true}, false},
	}
	for _, c := range cases {
		if got := materializableExecutionOrder(c.options); got != c.want {
			t.Errorf("materializableExecutionOrder(%+v) = %v; want %v", c.options, got, c.want)
		}
	}
}

func TestIsPlaylistErrorNonOverridableClassifiesCategories(t *testing.T) {
	for _, err := range []error{
		context.Canceled,
		context.DeadlineExceeded,
		extractor.ErrPlaylistLimit,
		&Error{Category: ErrorCancelled, Op: "x"},
		&Error{Category: ErrorSecurity, Op: "x"},
		&Error{Category: ErrorInternal, Op: "x"},
		errInvalidRequestOptions,
	} {
		if !isPlaylistErrorNonOverridable(err) {
			t.Errorf("expected %v to be non-overridable", err)
		}
	}
	for _, err := range []error{
		nil,
		errors.New("plain"),
		&Error{Category: ErrorNetwork, Op: "x"},
		&Error{Category: ErrorUnsupported, Op: "x"},
	} {
		if isPlaylistErrorNonOverridable(err) {
			t.Errorf("expected %v to be overridable", err)
		}
	}
}

func TestPlaylistEntryErrorMessageNeverEmbedsRawErrorText(t *testing.T) {
	secret := "very-private-text-must-not-leak"
	err := errors.New(secret)
	message := playlistEntryErrorMessage(7, err)
	if strings.Contains(message, secret) {
		t.Fatalf("message leaked raw error text: %q", message)
	}
	if !strings.Contains(message, "playlist entry 7") {
		t.Fatalf("message = %q; want source index", message)
	}
}

func TestPlaylistEntryErrorMessageUsesCategoryWhenTyped(t *testing.T) {
	err := &Error{Category: ErrorNetwork, Op: "download", Err: errors.New("private details")}
	message := playlistEntryErrorMessage(11, err)
	if !strings.Contains(message, "network") || !strings.Contains(message, "download") {
		t.Fatalf("message = %q; want category and op", message)
	}
	if strings.Contains(message, "private details") {
		t.Fatalf("message leaked wrapped error: %q", message)
	}
}

// eventsByKind returns the captured events that match the supplied kind.
func eventsByKind(events []Event, kind string) []Event {
	matches := make([]Event, 0)
	for _, event := range events {
		if event.Kind == kind {
			matches = append(matches, event)
		}
	}
	return matches
}

// TestPlaylistExecutionConformanceFixture is a lazy loader so the file still
// builds when the conformance fixture is absent. It reads the deterministic
// random-order fixture under conformance/playlists/random.expected.json.
func TestPlaylistExecutionConformanceFixture(t *testing.T) {
	payload, err := os.ReadFile(filepath.Join("..", "..", "conformance", "playlists", "random.expected.json"))
	if err != nil {
		t.Skip("random.expected.json not present yet")
	}
	var fixture struct {
		Seed    uint64   `json:"seed"`
		Indexes []int    `json:"indexes"`
		IDs     []string `json:"ids"`
	}
	if err := json.Unmarshal(payload, &fixture); err != nil {
		t.Fatal(err)
	}
	server, _ := selectionMediaServer(t)
	defer server.Close()
	var pages atomic.Int32
	source := func() *rand.Rand { return rand.New(rand.NewSource(int64(fixture.Seed))) }
	result := runSelectionFixture(t, server.URL, PlaylistOptions{
		Items:        "1-5",
		Random:       true,
		RandomSource: source,
	}, &pages)
	gotIDs := make([]string, 0, len(result.Entries))
	for _, entry := range result.Entries {
		var metadata map[string]any
		if err := json.Unmarshal(entry.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		gotIDs = append(gotIDs, metadata["id"].(string))
	}
	if !reflect.DeepEqual(gotIDs, fixture.IDs) {
		t.Fatalf("ids = %v; want %v", gotIDs, fixture.IDs)
	}
}
