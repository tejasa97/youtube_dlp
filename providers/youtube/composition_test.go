package youtube

import (
	"reflect"
	"testing"
)

func TestCompleteProviderOrder(t *testing.T) {
	want := []string{
		"youtube_music_search", "youtube_music_browse", "youtube_search", "youtube_hashtag",
		"youtube_alias_tab", "youtube_handle_tab", "youtube_channel_tab", "youtube",
	}
	if got := ProviderNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider names = %v, want %v", got, want)
	}
	providers := completeProviders()
	got := make([]string, 0, len(providers))
	for _, provider := range providers {
		got = append(got, provider.Name())
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("composition provider order = %v, want %v", got, want)
	}
}
