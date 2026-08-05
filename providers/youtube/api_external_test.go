package youtube_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/providers/youtube"
)

func TestPublicCompositionContract(t *testing.T) {
	var composition engine.Composition = youtube.NewComposition()
	if reflect.ValueOf(composition).IsZero() {
		t.Fatal("zero YouTube composition")
	}
	want := []string{
		"youtube_music_search", "youtube_music_browse", "youtube_search", "youtube_hashtag",
		"youtube_alias_tab", "youtube_handle_tab", "youtube_channel_tab", "youtube",
	}
	if got := youtube.ProviderNames(); !reflect.DeepEqual(got, want) {
		t.Fatalf("provider names = %v", got)
	}
}

func TestPublicPOTProviderConfigurationCompiles(t *testing.T) {
	option := youtube.WithPOTProviders(youtube.POTConfig{
		Policy: youtube.POTFetchAlways,
		Providers: []youtube.POTProvider{youtube.POTProviderFunc{
			ProviderName: "fixture",
			Function: func(context.Context, youtube.POTRequest) (youtube.POTResponse, error) {
				return youtube.POTResponse{Token: "Zm9v"}, nil
			},
		}},
	})
	if client := engine.NewClient(youtube.NewComposition(), option); client == nil {
		t.Fatal("nil configured client")
	}
}
