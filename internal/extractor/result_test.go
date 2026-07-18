package extractor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/value"
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
