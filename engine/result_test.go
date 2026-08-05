package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestEntrySequencesAreLazyReusableAndBounded(t *testing.T) {
	var pages []int
	sequence, err := OnDemandEntries(2, func(_ context.Context, page int) ([]Entry, error) {
		pages = append(pages, page)
		switch page {
		case 0:
			return []Entry{{ID: "one"}, {ID: "two"}}, nil
		case 1:
			return []Entry{{ID: "three"}}, nil
		default:
			return nil, nil
		}
	})
	if err != nil || len(pages) != 0 {
		t.Fatalf("OnDemandEntries() err=%v pages=%v", err, pages)
	}
	for iteration := 0; iteration < 2; iteration++ {
		entries, collectErr := CollectEntries(context.Background(), sequence, 10)
		if collectErr != nil || entryIDs(entries) != "one,two,three" {
			t.Fatalf("iteration %d entries=%v err=%v", iteration, entries, collectErr)
		}
	}
	if !reflect.DeepEqual(pages, []int{0, 1, 0, 1}) {
		t.Fatalf("pages = %v", pages)
	}
	if entries, err := CollectEntries(context.Background(), StaticEntries(Entry{ID: "one"}, Entry{ID: "two"}), 1); len(entries) != 1 || !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("bounded entries=%v err=%v", entries, err)
	}
}

func TestContinuationEntriesStopRepeatedTokensAndHonorProviderLimit(t *testing.T) {
	var tokens []string
	sequence, err := ContinuationEntries(nil, "one", func(_ context.Context, token string) ([]Entry, string, error) {
		tokens = append(tokens, token)
		return []Entry{{ID: token}}, token, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	entries, err := CollectEntries(context.Background(), sequence, 10)
	if err != nil || entryIDs(entries) != "one" || !reflect.DeepEqual(tokens, []string{"one"}) {
		t.Fatalf("entries=%v tokens=%v err=%v", entries, tokens, err)
	}

	limited, err := ContinuationEntriesWithPageLimit(nil, "one", 1, func(_ context.Context, token string) ([]Entry, string, error) {
		return nil, token + "-next", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CollectEntries(context.Background(), limited, 10); !errors.Is(err, ErrPlaylistLimit) {
		t.Fatalf("page-limit error = %v", err)
	}
}

func TestEntryIteratorPropagatesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	iterator := StaticEntries(Entry{ID: "one"}).Iterator()
	if _, _, err := iterator.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next() error = %v", err)
	}
}

func entryIDs(entries []Entry) string {
	result := ""
	for index, entry := range entries {
		if index > 0 {
			result += ","
		}
		result += entry.ID
	}
	return result
}
