package ytdlp

import (
	"reflect"
	"testing"

	"github.com/tejasa97/youtube_dlp/internal/extractor"
)

func TestBroadFacadePreservesCatalogAndYouTubeOrder(t *testing.T) {
	names := productRegistry().Names()
	wantYouTube := []string{
		"youtube_music_search", "youtube_music_browse", "youtube_search", "youtube_hashtag",
		"youtube_alias_tab", "youtube_handle_tab", "youtube_channel_tab", "youtube",
	}
	const firstYouTube = 7
	if got := names[firstYouTube : firstYouTube+len(wantYouTube)]; !reflect.DeepEqual(got, wantYouTube) {
		t.Fatalf("YouTube catalog order = %v", got)
	}
	providers := broadCompatibilityProviders(nil, nil)
	if got, want := len(names), len(providers); got != want {
		t.Fatalf("registry names=%d providers=%d", got, want)
	}
}

func TestBroadFacadeAppendsInstalledPluginsBeforeFallbacks(t *testing.T) {
	providers := broadCompatibilityProviders([]*InstalledPlugin{new(InstalledPlugin)}, nil)
	if len(providers) < 4 {
		t.Fatalf("providers = %d", len(providers))
	}
	pluginIndex := len(providers) - 4
	if _, ok := providers[pluginIndex].(extractor.ExplicitOnlyExtractor); !ok {
		t.Fatalf("provider at plugin position %d is %T", pluginIndex, providers[pluginIndex])
	}
	if got := []string{providers[pluginIndex+1].Name(), providers[pluginIndex+2].Name(), providers[pluginIndex+3].Name()}; !reflect.DeepEqual(got, []string{"amara", "fixture", "generic"}) {
		t.Fatalf("fallback order = %v", got)
	}
}
