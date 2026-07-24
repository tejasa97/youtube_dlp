package sponsorblock

import (
	"testing"
)

func TestCategoriesAndTitles(t *testing.T) {
	for _, category := range AllCategories() {
		title, ok := CanonicalTitle(category)
		if !ok {
			t.Fatalf("missing title for category %q", category)
		}
		if title == "" {
			t.Fatalf("empty title for category %q", category)
		}
		if !IsValidCategory(string(category)) {
			t.Fatalf("IsValidCategory rejected %q", category)
		}
	}
	if _, ok := CanonicalTitle("not-a-category"); ok {
		t.Fatal("CanonicalTitle accepted unknown category")
	}
	if IsValidCategory("not-a-category") {
		t.Fatal("IsValidCategory accepted unknown category")
	}
	if IsValidCategory("") {
		t.Fatal("IsValidCategory accepted empty category")
	}
}

func TestActions(t *testing.T) {
	for _, action := range AllActions() {
		if !IsValidAction(string(action)) {
			t.Fatalf("IsValidAction rejected %q", action)
		}
	}
	for _, bad := range []string{"", "skipping", "poii", "review"} {
		if IsValidAction(bad) {
			t.Fatalf("IsValidAction accepted %q", bad)
		}
	}
}

func TestAllCategoriesPinnedSet(t *testing.T) {
	// Pin the canonical order to the reference table.
	expected := []Category{
		"sponsor", "intro", "outro", "selfpromo", "preview", "filler",
		"interaction", "music_offtopic", "hook", "poi_highlight", "chapter",
	}
	got := AllCategories()
	if len(got) != len(expected) {
		t.Fatalf("category count = %d, want %d", len(got), len(expected))
	}
	for index, category := range expected {
		if got[index] != category {
			t.Fatalf("category[%d] = %q, want %q", index, got[index], category)
		}
	}
}

func TestAllActionsPinnedSet(t *testing.T) {
	got := AllActions()
	want := []ActionType{"skip", "poi", "chapter"}
	if len(got) != len(want) {
		t.Fatalf("action count = %d, want %d", len(got), len(want))
	}
	for index, action := range want {
		if got[index] != action {
			t.Fatalf("action[%d] = %q, want %q", index, got[index], action)
		}
	}
}
