package ytdlp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/ytdlp-go/ytdlp/internal/extractor"
	"github.com/ytdlp-go/ytdlp/internal/javascript/ejs"
	"github.com/ytdlp-go/ytdlp/internal/javascript/engine"
)

type playlistChoicePageTransport struct {
	pages map[string][]byte
	reads []string
}

func (transport *playlistChoicePageTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected playlist-choice API request")
}

func (transport *playlistChoicePageTransport) ReadPage(ctx context.Context, rawURL string) ([]byte, http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	transport.reads = append(transport.reads, rawURL)
	page, ok := transport.pages[rawURL]
	if !ok {
		return nil, nil, fmt.Errorf("unexpected playlist-choice page %q", rawURL)
	}
	return bytes.Clone(page), make(http.Header), nil
}

func TestProductYouTubeNoPlaylistChoice(t *testing.T) {
	ambiguousURL := "https://www.youtube.com/watch?v=fixture0001&list=PL_fixture"
	playlistURL := "https://www.youtube.com/playlist?list=PL_fixture"
	watchURL := "https://www.youtube.com/watch?v=fixture0001"
	playlistPage := readProductConformanceFixture(t, "youtube", "playlist.html")
	watchPage := readProductConformanceFixture(t, "youtube", "watch.html")
	player, err := os.ReadFile(filepath.Join("..", "..", "conformance", "javascript", "ejs-0.8.0", "synthetic-player.js"))
	if err != nil {
		t.Fatal(err)
	}

	solver, err := ejs.New(engine.New(4))
	if err != nil {
		t.Fatal(err)
	}
	registry := productRegistry()
	selected, err := registry.Select(ambiguousURL)
	if err != nil || selected.Name() != "youtube" {
		t.Fatalf("selected=%v err=%v", selected, err)
	}

	t.Run("default-prefers-lazy-playlist", func(t *testing.T) {
		transport := &playlistChoicePageTransport{pages: map[string][]byte{playlistURL: playlistPage}}
		result, name, err := registry.Extract(context.Background(), extractor.Request{URL: ambiguousURL, Transport: transport})
		if err != nil || name != "youtube" || !result.IsPlaylist() {
			t.Fatalf("result=%#v name=%q err=%v", result, name, err)
		}
		if len(transport.reads) != 1 || transport.reads[0] != playlistURL {
			t.Fatalf("reads=%v want playlist page only", transport.reads)
		}
	})

	t.Run("no-playlist-prefers-video", func(t *testing.T) {
		transport := &playlistChoicePageTransport{pages: map[string][]byte{
			watchURL: watchPage,
			"https://www.youtube.com/s/player/fixture/base.js": player,
		}}
		result, name, err := registry.Extract(context.Background(), extractor.Request{
			URL: ambiguousURL, Transport: transport, NoPlaylist: true, ChallengeSolver: solver,
		})
		if err != nil || name != "youtube" || result.IsPlaylist() || result.IsURL() {
			t.Fatalf("result=%#v name=%q err=%v", result, name, err)
		}
		if len(transport.reads) == 0 || transport.reads[0] != watchURL {
			t.Fatalf("reads=%v want video page first", transport.reads)
		}
	})

	t.Run("cancellation-does-not-enter-discarded-branch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		transport := &playlistChoicePageTransport{pages: map[string][]byte{playlistURL: playlistPage}}
		_, _, err := registry.Extract(ctx, extractor.Request{URL: ambiguousURL, Transport: transport})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want cancellation", err)
		}
		if len(transport.reads) != 0 {
			t.Fatalf("cancelled extraction read pages=%v", transport.reads)
		}
	})
}

func TestProductPlaylistChoiceRegistryKeepsExplicitPlaylistRouting(t *testing.T) {
	registry := productRegistry()
	for rawURL, want := range map[string]string{
		"https://www.youtube.com/playlist?list=PL_fixture": "youtube",
		"https://www.dailymotion.com/playlist/xlist01":     "dailymotion_playlist",
		"https://space.bilibili.com/1/lists/2":             "bilibili_collection",
	} {
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != want {
			t.Fatalf("Select(%q)=%v err=%v want %q", rawURL, selected, err, want)
		}
	}
}
