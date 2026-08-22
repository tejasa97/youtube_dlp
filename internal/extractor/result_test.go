package extractor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/tejasa97/ytdlp-go/internal/value"
)

func TestEntryObjectMatchesURLResultShape(t *testing.T) {
	entry := Entry{URL: "https://example.test/video", ExtractorKey: "Example", ID: "one", Title: "One", Transparent: true}
	encoded, err := value.ObjectValue(entry.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"_type":"url_transparent","url":"https://example.test/video","ie_key":"Example","id":"one","title":"One"}`
	if string(encoded) != want {
		t.Fatalf("entry JSON = %s", encoded)
	}
}

func TestEntryObjectOptionalMetadataPresenceAndZeros(t *testing.T) {
	t.Parallel()
	base := Entry{URL: "https://example.test/video", ExtractorKey: "Example", ID: "one", Title: "One", Transparent: true}
	baseJSON, err := value.ObjectValue(base.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantBase := `{"_type":"url_transparent","url":"https://example.test/video","ie_key":"Example","id":"one","title":"One"}`
	if string(baseJSON) != wantBase {
		t.Fatalf("absent soft fields changed base shape: %s", baseJSON)
	}

	withZeros := base
	withZeros.Thumbnail = "https://static-cdn.example.test/thumb.jpg"
	withZeros.Duration = 0
	withZeros.HasDuration = true
	withZeros.Timestamp = 0
	withZeros.HasTimestamp = true
	withZeros.ViewCount = 0
	withZeros.HasViewCount = true
	withZeros.Language = "en"
	zeroJSON, err := value.ObjectValue(withZeros.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantZeros := `{"_type":"url_transparent","url":"https://example.test/video","ie_key":"Example","id":"one","title":"One","thumbnail":"https://static-cdn.example.test/thumb.jpg","duration":0,"timestamp":0,"view_count":0,"language":"en"}`
	if string(zeroJSON) != wantZeros {
		t.Fatalf("zero metadata JSON = %s", zeroJSON)
	}

	partial := base
	partial.HasDuration = true
	partial.Duration = 12.5
	partialJSON, err := value.ObjectValue(partial.Object()).MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	wantPartial := `{"_type":"url_transparent","url":"https://example.test/video","ie_key":"Example","id":"one","title":"One","duration":12.5}`
	if string(partialJSON) != wantPartial {
		t.Fatalf("partial metadata JSON = %s", partialJSON)
	}

	object := withZeros.Object()
	if _, ok := object.Lookup("thumbnail").StringValue(); !ok {
		t.Fatal("missing thumbnail")
	}
	if duration, ok := object.Lookup("duration").Float(); !ok || duration != 0 {
		t.Fatalf("duration = %v, %t", duration, ok)
	}
	if timestamp, ok := object.Lookup("timestamp").Int(); !ok || timestamp != 0 {
		t.Fatalf("timestamp = %v, %t", timestamp, ok)
	}
	if views, ok := object.Lookup("view_count").Int(); !ok || views != 0 {
		t.Fatalf("view_count = %v, %t", views, ok)
	}
	if language, ok := object.Lookup("language").StringValue(); !ok || language != "en" {
		t.Fatalf("language = %q, %t", language, ok)
	}
	if !base.Object().Lookup("duration").IsMissing() || !base.Object().Lookup("timestamp").IsMissing() ||
		!base.Object().Lookup("view_count").IsMissing() || !base.Object().Lookup("thumbnail").IsMissing() ||
		!base.Object().Lookup("language").IsMissing() {
		t.Fatal("absent fields were serialized")
	}
}

func TestURLResultRetainsLazyHandoff(t *testing.T) {
	entry := Entry{URL: "https://example.test/video", ExtractorKey: "Example", Transparent: true}
	result, err := URLResult(entry)
	if err != nil || !result.IsURL() || result.IsPlaylist() || result.Redirect == nil || *result.Redirect != entry {
		t.Fatalf("URLResult() = %#v, %v", result, err)
	}
	if _, err := URLResult(Entry{}); !errors.Is(err, ErrInvalidPlaylist) {
		t.Fatalf("empty URLResult error = %v", err)
	}
}

func TestOnDemandEntriesAreOrderedLazyAndReusable(t *testing.T) {
	var calls []int
	sequence, err := OnDemandEntries(2, func(_ context.Context, page int) ([]Entry, error) {
		calls = append(calls, page)
		switch page {
		case 0:
			return []Entry{{ID: "one"}, {ID: "two"}}, nil
		case 1:
			return []Entry{{ID: "three"}}, nil
		default:
			t.Fatalf("unexpected page %d", page)
			return nil, nil
		}
	})
	if err != nil || len(calls) != 0 {
		t.Fatalf("OnDemandEntries() = %v, calls=%v", err, calls)
	}
	iterator := sequence.Iterator()
	first, ok, err := iterator.Next(context.Background())
	if err != nil || !ok || first.ID != "one" || !reflect.DeepEqual(calls, []int{0}) {
		t.Fatalf("first=%#v ok=%v err=%v calls=%v", first, ok, err, calls)
	}
	entries, err := CollectEntries(context.Background(), sequence, 10)
	if err != nil || ids(entries) != "one,two,three" || !reflect.DeepEqual(calls, []int{0, 0, 1}) {
		t.Fatalf("entries=%#v err=%v calls=%v", entries, err, calls)
	}
}

func TestEntrySequencesPropagateCancellationErrorsAndLimits(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	iterator := StaticEntries(Entry{ID: "one"}).Iterator()
	if _, _, err := iterator.Next(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("static cancellation error = %v", err)
	}
	wantErr := errors.New("page failed")
	sequence, _ := OnDemandEntries(1, func(context.Context, int) ([]Entry, error) { return nil, wantErr })
	if entries, err := CollectEntries(context.Background(), sequence, 10); len(entries) != 0 || !errors.Is(err, wantErr) {
		t.Fatalf("entries=%v error=%v", entries, err)
	}
	if entries, err := CollectEntries(context.Background(), StaticEntries(Entry{ID: "one"}, Entry{ID: "two"}), 1); len(entries) != 1 || !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("entries=%v error=%v", entries, err)
	}
}

func TestContinuationEntriesFollowEmptyPagesAndStopLoops(t *testing.T) {
	var tokens []string
	sequence, err := ContinuationEntries([]Entry{{ID: "one"}}, "next-1", func(_ context.Context, token string) ([]Entry, string, error) {
		tokens = append(tokens, token)
		switch token {
		case "next-1":
			return nil, "next-2", nil
		case "next-2":
			return []Entry{{ID: "two"}}, "next-2", nil
		default:
			t.Fatalf("unexpected token %q", token)
			return nil, "", nil
		}
	})
	if err != nil || len(tokens) != 0 {
		t.Fatalf("ContinuationEntries() = %v, tokens=%v", err, tokens)
	}
	entries, err := CollectEntries(context.Background(), sequence, 10)
	if err != nil || ids(entries) != "one,two" || !reflect.DeepEqual(tokens, []string{"next-1", "next-2"}) {
		t.Fatalf("entries=%v err=%v tokens=%v", entries, err, tokens)
	}
	entries, err = CollectEntries(context.Background(), sequence, 10)
	if err != nil || ids(entries) != "one,two" {
		t.Fatalf("second iterator entries=%v err=%v", entries, err)
	}
}

func TestStatefulContinuationEntriesRotateIndependentIteratorState(t *testing.T) {
	var calls []string
	sequence, err := StatefulContinuationEntries([]Entry{{ID: "one"}}, "next-1", "visitor-1", func(_ context.Context, token, state string) ([]Entry, string, string, error) {
		calls = append(calls, token+"/"+state)
		switch token {
		case "next-1":
			return nil, "next-2", "visitor-2", nil
		case "next-2":
			return []Entry{{ID: "two"}}, "next-2", "visitor-3", nil
		default:
			t.Fatalf("unexpected token %q", token)
			return nil, "", state, nil
		}
	})
	if err != nil || len(calls) != 0 {
		t.Fatalf("StatefulContinuationEntries() = %v, calls=%v", err, calls)
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, collectErr := CollectEntries(context.Background(), sequence, 10)
		if collectErr != nil || ids(entries) != "one,two" {
			t.Fatalf("iteration %d entries=%v err=%v", iteration, entries, collectErr)
		}
	}
	want := []string{"next-1/visitor-1", "next-2/visitor-2", "next-1/visitor-1", "next-2/visitor-2"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v, want %v", calls, want)
	}
}

func TestPlaylistMarksMetadataWithoutMaterializingEntries(t *testing.T) {
	info := value.NewInfo(value.NewObject(value.Field{Key: "id", Value: value.String("list")}))
	result, err := Playlist(info, StaticEntries(Entry{ID: "one"}))
	if err != nil || !result.IsPlaylist() {
		t.Fatalf("Playlist() = %#v, %v", result, err)
	}
	if kind, _ := result.Info.Lookup("_type").StringValue(); kind != "playlist" {
		t.Fatalf("_type = %q", kind)
	}
	if !result.Info.Lookup("entries").IsMissing() {
		t.Fatal("playlist construction materialized entries")
	}
}

func FuzzOnDemandEntries(f *testing.F) {
	f.Add(uint8(5), uint8(2))
	f.Add(uint8(0), uint8(1))
	f.Add(uint8(8), uint8(4))
	f.Fuzz(func(t *testing.T, rawCount, rawPageSize uint8) {
		count := int(rawCount % 65)
		pageSize := int(rawPageSize%16) + 1
		sequence, err := OnDemandEntries(pageSize, func(_ context.Context, page int) ([]Entry, error) {
			start := page * pageSize
			if start >= count {
				return nil, nil
			}
			end := min(start+pageSize, count)
			entries := make([]Entry, end-start)
			for index := range entries {
				entries[index].ID = fmt.Sprint(start + index)
			}
			return entries, nil
		})
		if err != nil {
			t.Fatal(err)
		}
		entries, err := CollectEntries(context.Background(), sequence, count+1)
		if err != nil || len(entries) != count {
			t.Fatalf("count=%d pageSize=%d got=%d err=%v", count, pageSize, len(entries), err)
		}
		for index, entry := range entries {
			if entry.ID != fmt.Sprint(index) {
				t.Fatalf("entry %d = %q", index, entry.ID)
			}
		}
	})
}

func ids(entries []Entry) string {
	result := ""
	for index, entry := range entries {
		if index > 0 {
			result += ","
		}
		result += entry.ID
	}
	return result
}
