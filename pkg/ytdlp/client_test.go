package ytdlp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ytdlp-go/ytdlp/internal/archive"
	"github.com/ytdlp-go/ytdlp/internal/compat/matchfilter"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromium"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumlinux"
	"github.com/ytdlp-go/ytdlp/internal/cookies/chromiumwindows"
	"github.com/ytdlp-go/ytdlp/internal/cookies/firefox"
	"github.com/ytdlp-go/ytdlp/internal/cookies/netscape"
	"github.com/ytdlp-go/ytdlp/internal/cookies/safari"
	credentialnetrc "github.com/ytdlp-go/ytdlp/internal/credentials/netrc"
	"github.com/ytdlp-go/ytdlp/internal/downloader"
	"github.com/ytdlp-go/ytdlp/internal/extractor"
	mediaformat "github.com/ytdlp-go/ytdlp/internal/format"
	"github.com/ytdlp-go/ytdlp/internal/media/ffmpeg"
	"github.com/ytdlp-go/ytdlp/internal/media/pipeline"
	"github.com/ytdlp-go/ytdlp/internal/network"
	"github.com/ytdlp-go/ytdlp/internal/protocol/dash"
	"github.com/ytdlp-go/ytdlp/internal/protocol/hls"
	"github.com/ytdlp-go/ytdlp/internal/protocol/ism"
	"github.com/ytdlp-go/ytdlp/internal/protocol/youtubelive"
	"github.com/ytdlp-go/ytdlp/internal/testserver"
	"github.com/ytdlp-go/ytdlp/internal/value"
)

type prxProductRoundTripper struct {
	body   []byte
	bodies map[string][]byte
	calls  int
	paths  []string
}

func (t *prxProductRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	t.calls++
	t.paths = append(t.paths, r.URL.Path)
	body := t.body
	if t.bodies != nil {
		body = t.bodies[r.URL.Path]
	}
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

type aeonCoProductRoundTripper struct {
	aeonPage []byte
}

func readProductConformanceFixture(t *testing.T, parts ...string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(append([]string{"..", "..", "conformance", "extractors"}, parts...)...))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func (rt *aeonCoProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() == "aeon.co" && strings.HasPrefix(request.URL.Path, "/videos/") {
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header),
			Body: io.NopCloser(bytes.NewReader(rt.aeonPage)), Request: request,
		}, nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
}

// productVimeoRefererRecorder is a stand-in child extractor selected by the
// transparent URL handoff. It records only Request.Referer; Vimeo player-config
// re-entry is covered by internal/extractor.TestAeonCoVimeoReentryUsesAeonReferer.
type productVimeoRefererRecorder struct {
	referer string
}

func (recorder *productVimeoRefererRecorder) Name() string { return "vimeo" }

func (recorder *productVimeoRefererRecorder) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.EqualFold(parsed.Hostname(), "vimeo.com")
}

func (recorder *productVimeoRefererRecorder) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	recorder.referer = request.Referer
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("123456789")},
		value.Field{Key: "title", Value: value.String("Referer recorder")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "url", Value: value.String(request.URL)},
	))
	return extractor.Media(info), nil
}

func TestProductAeonCoPropagatesRefererThroughURLRecursion(t *testing.T) {
	rt := &aeonCoProductRoundTripper{
		aeonPage: readProductConformanceFixture(t, "shared", "aeonco", "vimeo_page.html"),
	}
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	recorder := &productVimeoRefererRecorder{}
	request := Request{URL: "https://aeon.co/videos/raw-solar-storm-footage", SkipDownload: true}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	op := &operation{
		client: NewClient(), request: request, transport: transport,
		registry:      extractor.NewRegistry(extractor.NewAeonCo(), recorder),
		compatibility: plan, rootExtractor: &root,
	}
	if _, err := op.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0); err != nil {
		t.Fatal(err)
	}
	if root != "aeonco" {
		t.Fatalf("root extractor = %q", root)
	}
	if recorder.referer != "https://aeon.co/" {
		t.Fatalf("child referer = %q", recorder.referer)
	}
}

func TestIsCategory(t *testing.T) {
	err := &Error{Category: ErrorNetwork, Op: "fetch", Err: errors.New("offline")}
	if !IsCategory(err, ErrorNetwork) {
		t.Fatal("IsCategory() = false, want true")
	}
	if IsCategory(err, ErrorInvalidInput) {
		t.Fatal("IsCategory() matched the wrong category")
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("Error does not unwrap its cause")
	}
}

func TestLoadNetRCCredentialsFromDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".netrc")
	const username, password = "fixture-user", "netrc-secret-never-export"
	if err := os.WriteFile(path, []byte("machine auth-fixture.invalid login "+username+" password "+password+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	provider, err := loadNetRCCredentials(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	credential, ok, err := provider.Lookup(context.Background(), "auth-fixture.invalid")
	if err != nil || !ok || credential.Username != username || credential.Password != password {
		t.Fatalf("credential lookup mismatch: found=%t error=%v", ok, err)
	}
	if rendered := fmt.Sprintf("%v", provider); strings.Contains(rendered, username) || strings.Contains(rendered, password) {
		t.Fatalf("provider formatting exposed credentials: %q", rendered)
	}
}

func TestClientRejectsUnsafeNetRCBeforeExtraction(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("mode-bit policy is Unix-specific")
	}
	path := filepath.Join(t.TempDir(), "credentials.netrc")
	if err := os.WriteFile(path, []byte("machine auth-fixture.invalid login user password secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://auth-fixture.invalid/watch/auth-001", SkipDownload: true,
		UseNetRC: true, NetRCLocation: path,
	})
	if !IsCategory(err, ErrorSecurity) || !errors.Is(err, credentialnetrc.ErrUnsafeFile) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestExtractorFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{extractor.ErrAuthentication, ErrorAuthentication},
		{extractor.ErrTwitchSubscriberOnly, ErrorAuthentication},
		{extractor.ErrUnavailable, ErrorUnsupported},
		{extractor.ErrInvalidMetadata, ErrorInternal},
		{extractor.ErrPlaylistLimit, ErrorInternal},
		{context.Canceled, ErrorCancelled},
		{extractor.ErrChallengeSolver, ErrorUnsupported},
		{extractor.ErrTransportIsolation, ErrorUnsupported},
		{extractor.ErrRegionRestricted, ErrorUnsupported},
		{extractor.ErrPeerTubeNetwork, ErrorNetwork},
		{extractor.ErrInternetArchiveNetwork, ErrorNetwork},
		{extractor.ErrYouTubeChannelRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeChannelNetwork, ErrorNetwork},
		{extractor.ErrYouTubeSearchRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeSearchNetwork, ErrorNetwork},
		{extractor.ErrYouTubeHandleTabRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeHandleTabNetwork, ErrorNetwork},
		{extractor.ErrYouTubeMusicSearchRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeMusicSearchNetwork, ErrorNetwork},
		{extractor.ErrYouTubeCommentsRateLimited, ErrorNetwork},
		{extractor.ErrYouTubeCommentsNetwork, ErrorNetwork},
		{extractor.ErrSoundCloudCommentsRateLimited, ErrorNetwork},
		{extractor.ErrSoundCloudCommentsNetwork, ErrorNetwork},
		{extractor.ErrSoundCloudOriginalRateLimited, ErrorNetwork},
	} {
		if err := categorized("extract", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v", test.err, err)
		}
	}
}

type twitchMalformedAuthTransport struct {
	token         string
	cookieURL     string
	ambientCalls  int
	redirectCalls int
}

func (transport *twitchMalformedAuthTransport) Do(context.Context, *http.Request) (*http.Response, error) {
	transport.ambientCalls++
	return nil, errors.New("unexpected ambient Twitch request")
}

func (transport *twitchMalformedAuthTransport) ReadPage(context.Context, string) ([]byte, http.Header, error) {
	transport.ambientCalls++
	return nil, nil, errors.New("unexpected Twitch page request")
}

func (transport *twitchMalformedAuthTransport) Cookies(rawURL string) ([]*http.Cookie, error) {
	transport.cookieURL = rawURL
	return []*http.Cookie{{Name: "auth-token", Value: transport.token}}, nil
}

func (transport *twitchMalformedAuthTransport) DoNoRedirect(context.Context, *http.Request) (*http.Response, error) {
	transport.redirectCalls++
	return nil, errors.New("unexpected no-redirect Twitch request")
}

func TestProductCategorizesMalformedTwitchAuthCookieWithoutNetworkOrLeakage(t *testing.T) {
	const marker = "synthetic-auth-token-marker"
	transport := &twitchMalformedAuthTransport{token: strings.Repeat(marker, 32)}
	_, extractErr := extractor.NewTwitch().Extract(context.Background(), extractor.Request{
		URL:       "https://www.twitch.tv/fixture_channel",
		Transport: transport,
	})
	if !errors.Is(extractErr, extractor.ErrAuthentication) || strings.Contains(extractErr.Error(), marker) {
		t.Fatalf("extract error=%v", extractErr)
	}
	if transport.cookieURL != "https://gql.twitch.tv" || transport.ambientCalls != 0 || transport.redirectCalls != 0 {
		t.Fatalf("cookie URL=%q ambient=%d no-redirect=%d", transport.cookieURL, transport.ambientCalls, transport.redirectCalls)
	}
	err := categorized("twitch extraction", extractErr)
	if !IsCategory(err, ErrorAuthentication) || !errors.Is(err, extractor.ErrAuthentication) || strings.Contains(err.Error(), marker) {
		t.Fatalf("categorized error=%v", err)
	}
}

func TestSoundCloudCommentOptionsValidation(t *testing.T) {
	t.Parallel()
	for _, options := range []SoundCloudCommentOptions{
		{},
		{Enabled: true},
		{Enabled: true, Sort: "newest", MaxComments: 1},
		{Enabled: true, Sort: "oldest", MaxComments: 100},
		{Enabled: true, Sort: "track-timestamp", MaxComments: 10_000},
	} {
		if err := validateRequestOptions(Request{SoundCloudComments: options}); err != nil {
			t.Errorf("validateRequestOptions(%+v): %v", options, err)
		}
	}
	for _, options := range []SoundCloudCommentOptions{
		{Enabled: true, Sort: "invalid"},
		{Enabled: true, MaxComments: -1},
		{Enabled: true, MaxComments: 10_001},
	} {
		if err := validateRequestOptions(Request{SoundCloudComments: options}); err == nil {
			t.Errorf("validateRequestOptions(%+v) succeeded", options)
		}
	}
}

func TestProductRegistryIncludesIntegratedExtractors(t *testing.T) {
	tests := []struct {
		rawURL string
		name   string
	}{
		{"https://www.youtube.com/watch?v=fixture0001", "youtube"},
		{"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos", "youtube_channel_tab"},
		{"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/releases", "youtube_channel_tab"},
		{"https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv", "youtube_channel_tab"},
		{"https://www.youtube.com/@synthetic-handle/videos", "youtube_handle_tab"},
		{"https://www.youtube.com/@synthetic-handle/community", "youtube_handle_tab"},
		{"https://www.youtube.com/@synthetic-handle", "youtube_handle_tab"},
		{"https://www.youtube.com/user/SyntheticAlias/videos", "youtube_alias_tab"},
		{"https://www.youtube.com/c/СинтетическийКанал/playlists", "youtube_alias_tab"},
		{"https://www.youtube.com/c/СинтетическийКанал/home", "youtube_alias_tab"},
		{"https://www.youtube.com/c/СинтетическийКанал", "youtube_alias_tab"},
		{"ytsearch5:fixture query", "youtube_search"},
		{"https://music.youtube.com/search?q=fixture#songs", "youtube_music_search"},
		{"https://music.youtube.com/browse/MPREbfixture0001", "youtube_music_browse"},
		{"https://vimeo.com/123456789", "vimeo"},
		{"https://www.tiktok.com/@fixture/video/1234567890123456789", "tiktok"},
		{"https://podcasts.apple.com/us/podcast/fixture/id123?i=456", "applepodcasts"},
		{"https://soundcloud.com/fixture-artist/reposts", "soundcloud"},
		{"https://w.soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Ffixture-artist%2Fsynthetic-signal", "soundcloud_embed"},
		{"https://soundcloud.com/player/?url=https%3A%2F%2Fsoundcloud.com%2Ffixture-artist%2Fsynthetic-signal", "soundcloud_embed"},
		{"https://hytale.com/news/2021/07/summer-2021-development-update", "hytale"},
		{"https://watch.cloudflarestream.com/9df17203414fd1db3e3ed74abbe936c1", "cloudflarestream"},
		{"https://www.washingtonpost.com/video/c/video/480ba4ee-1ec7-11e6-82c2-a7dcb313287d", "washingtonpost"},
		{"https://www.adn.com/politics/2020/11/02/video-senate-candidates/", "adn"},
		{"https://www.bostonglobe.com/video/2020/12/30/metro/example/", "bostonglobe"},
		{"https://www.wabi.tv/video/2020/12/30/example/", "gray"},
		{"https://www.clickondetroit.com/video/community/2020/05/15/example/", "clickondetroit"},
		{"arcpublishing:adn:8c99cb6e-b29c-4bc9-9173-7bf9979225ab", "arcpublishing"},
		{"https://www.fox9.com/news/bear-climbs-tree", "fox9_news"},
		{"https://www.fox9.com/video/314473", "fox9"},
		{"anvato:anvato_epfox_app_web_prod_b3373168e12f423f41504f207000188daf88251b:8032455", "anvato"},
		{"https://vplayer.nbcolympics.com/p/NnzsPC/widget/select/media/4Y0TlYUr_ZT7", "nbcolympics"},
		{"https://weather.com/storms/hurricane/video/invest-95l-fixture", "weathercom"},
		{"https://feed.theplatform.com/f/7wvmTC/msnbc_video-p-test?byGuid=n_hardball_5biden_140207", "theplatform_feed"},
		{"https://link.theplatform.com/s/kYEXFC/22d_qsQ6MIRT", "theplatform"},
		{"https://www.pgatour.com/video/features/6322506425112/slug", "pgatour"},
		{"https://www.9news.com.au/videos/national/fair-trading/clqgc7dvj000y0jnvfism0w5m", "ninenews"},
		{"https://media.netapp.com/collection/9820e190-f2a6-47ac-9c0a-98e5e64234a4", "netapp_collection"},
		{"https://www.amc.com/shows/dark-winds/videos/x", "amcnetworks"},
		{"https://www.tvanouvelles.ca/videos/5117035533001", "tvanouvelles"},
		{"https://webtv.un.org/en/asset/k1o/k1o7stmi6p", "unitednationswebtv"},
		{"https://www.spiegel.de/video/vulkan-video-1259285.html", "spiegel"},
		{"https://www.actionnewsjax.com/video/live-stream/", "actionnewsjax"},
		{"https://player.megaphone.fm/GLT9749789991", "megaphone"},
		{"https://shows.acast.com/sparpodcast/episodes/2.raggarmordet-rosterurdetforflutna", "acast"},
		{"https://www.acast.com/todayinfocus", "acast_channel"},
		{"https://player.simplecast.com/b6dc49a2-9404-4853-9aa9-9cfc097be876", "simplecast"},
		{"https://api.spreaker.com/show/4652058", "spreaker_show"},
		{"https://www.nowness.com/story/candor-the-art-of-gesticulation", "nowness"},
		{"https://www.nowness.com/playlist/3286", "nowness_playlist"},
		{"https://www.nowness.com/series/60-seconds", "nowness_series"},
		{"https://iframe.dacast.com/vod/acae82153ef4d7a7344ae4eaa86af534/1c6143e3-5a06-371d-8695-19b96ea49090", "dacast"},
		{"https://iframe.dacast.com/playlist/943bb1ab3c03695ba85330d92d6d226e/b632eb053cac17a9c9a02bcfc827f2d8", "dacast_playlist"},
		{"https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?id=26b3ae9e-4a48-4dcc-96ba-0befba08a0fb", "panopto"},
		{"https://demo.hosted.panopto.com/Panopto/Pages/Viewer.aspx?pid=f3b39fcf-882f-4849-93d6-a9f401236d36", "panopto_playlist"},
		{"https://www.teachingchannel.org/videos/teacher-teaming-evolution", "teachingchannel"},
		{"https://www.nowcanal.pt/ultimas/detalhe/pedro-sousa-hjulmand", "nowcanal"},
		{"https://www.democracynow.org/shows/2015/7/3", "democracynow"},
		{"https://www.buzzfeed.com/abagg/this-angry-ram-destroys-a-punching-bag-like-a-boss", "buzzfeed"},
		{"https://mdstrm.com/embed/6318e3f1d1d316083ae48831", "mediastream"},
		{"https://www.winsports.co/videos/siempre-castellanos-gran-atajada-del-portero-cardenal-para-evitar-la-caida-de-su-arco-60536", "winsports"},
		{"https://abc7news.com/entertainment/east-bay-museum/472581/", "abcotvs"},
		{"https://clips.abcotvs.com/kabc/video/214814", "abcotvs_clips"},
		{"https://how-to-video.vids.io/videos/799cd8b11c10efc1f0/how-to-video-live-streaming", "vidsio"},
		{"https://laracasts.com/series/30-days-to-learn-laravel-11/episodes/1", "laracasts"},
		{"https://laracasts.com/series/30-days-to-learn-laravel-11", "laracasts_series"},
		{"https://www.formula1.com/en/latest/video.race-highlights.6060988138001.html", "formula1"},
		{"https://www.europeantour.com/dpworld-tour/news/video/the-best-shots/", "europeantour"},
		{"https://www.maoritelevision.com/shows/korero-mai/S01E054/episode", "maoritv"},
		{"https://www.thestar.com/life/2016/02/01/article.html", "thestar"},
		{"https://www.thesun.co.uk/tvandshowbiz/2261604/slug", "thesun"},
		{"https://www.wimbledon.com/en_GB/video/media/6330247525112.html", "wimbledon"},
		{"https://www.usatoday.com/story/tech/science/2018/08/21/yellowstone/", "usatoday"},
		{"https://www.skynews.com.au/a/b/c/video/abc123def456", "skynewsau"},
		{"https://www.bundesliga.com/en/bundesliga/videos?vid=AbCd1234", "bundesliga"},
		{"https://uk.businessinsider.com/article-slug", "businessinsider"},
		{"https://www.dagbladet.no/video/slug/PalfB2Cw", "dbtv"},
		{"https://www.hollywoodreporter.com/video/slug/", "hollywoodreporter"},
		{"https://www.iltalehti.fi/ulkomaat/a/9fbd067f-94e4-46cd-8748-9d958eb4dae2", "iltalehti"},
		{"https://video.lefigaro.fr/embed/figaro/video/slug/", "lefigarovideoembed"},
		{"https://www.mirror.co.uk/tv/tv-news/article-27163139", "mirrorcouk"},
		{"http://www.outsidetv.com/home/play/ZjQYboH6/1/10/Hdg0jukV/4", "outsidetv"},
		{"https://theintercept.com/fieldofvision/slug/", "theintercept"},
		{"https://players.brightcove.net/12345/default_default/index.html?videoId=123", "brightcove"},
		{"kaltura:123:1_abcd1234", "kaltura"},
		{"https://cdn.jwplayer.com/players/AbCd1234-ABCDEFGHI.js", "jwplatform"},
		{"wistia:a1b2c3d4e5", "wistia"},
		{"https://videos.sproutvideo.com/embed/4abcdef1234567890a/0abcdef1234567890", "sproutvideo"},
		{"https://www.dailymotion.com/video/x12345", "dailymotion"},
		{"https://www.reddit.com/r/videos/comments/abc123/title/", "reddit"},
		{"https://x.com/fixture/status/1234567890", "twitter"},
		{"https://fixture.bandcamp.com/track/example", "bandcamp"},
		{"https://www.mixcloud.com/fixture/example/", "mixcloud"},
		{"https://rumble.com/embed/v12345/", "rumble"},
		{"https://www.bilibili.com/video/BV1abcdefgh", "bilibili"},
		{"https://www.instagram.com/p/aye83DjauH/", "instagram"},
		{"https://kick.com/fixture-channel", "kick"},
		{"https://www.bbc.co.uk/iplayer/episode/p0000000/title", "bbciplayer"},
		{"https://www.ardmediathek.de/player/Y3JpZDovL2ZpeHR1cmU", "ard"},
		{"nrk:MDDP12000117", "nrk"},
		{"https://www.twitch.tv/fixture_channel", "twitch"},
		{"scsearch3:fixture query", "soundcloud_search"},
		{"https://soundcloud.com/fixture-artist/synthetic-signal", "soundcloud"},
		{"https://streamable.com/e/fixture_1", "streamable"},
		{"https://aeon.co/videos/raw-solar-storm-footage", "aeonco"},
		{"https://www.aeon.co/videos/dazzling-timelapse-2", "aeonco"},
		{"peertube:peertube.example:00000000-0000-4000-8000-000000000001", "peertube"},
		{"https://archive.org/details/fixture_concert", "internetarchive"},
		{"https://www.svtplay.se/video/fixture-program?modalId=fixture123", "region_svt"},
		{"https://auth-fixture.invalid/watch/fixture123", "synthetic_auth"},
		{"https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", "amara"},
		{"https://example.com/media.mp4", "generic"},
	}
	registry := productRegistry()
	for _, test := range tests {
		selected, err := registry.Select(test.rawURL)
		if err != nil {
			t.Errorf("Select(%q): %v", test.rawURL, err)
			continue
		}
		if selected.Name() != test.name {
			t.Errorf("Select(%q) = %q, want %q", test.rawURL, selected.Name(), test.name)
		}
	}
}

func TestProductRegistryRoutesRaiAndPlaylistReentry(t *testing.T) {
	registry := productRegistry()
	for _, test := range []struct{ rawURL, want string }{
		{"https://www.raiplay.it/programmi/report", "raiplay_playlist"},
		{"https://www.raiplay.it/dirette/rainews24", "raiplay_live"},
		{"https://www.raiplaysound.it/programmi/report", "raiplaysound_playlist"},
		{"https://www.raicultura.it/letteratura/articoli/2018/12/Alberto-Asor-Rosa-Letteratura-e-potere-05ba8775-82b5-45c5-a89d-dd955fbde1fb.html", "raicultura"},
	} {
		selected, err := registry.Select(test.rawURL)
		if err != nil || selected.Name() != test.want {
			t.Fatalf("Select(%q) = %v, %v", test.rawURL, selected, err)
		}
	}
	for _, test := range []struct{ rawURL, key string }{
		{"https://www.raiplay.it/video/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html", "raiplay"},
		{"https://www.raiplaysound.it/audio/x-cb27157f-9dd0-4aee-b788-b1f67643a391.html", "raiplaysound"},
	} {
		selected, err := registry.SelectFor(test.rawURL, test.key)
		if err != nil || selected.Name() != test.key {
			t.Fatalf("SelectFor(%q, %q) = %v, %v", test.rawURL, test.key, selected, err)
		}
	}
}

func TestProductCategorizesPRXNetworkFailures(t *testing.T) {
	for _, err := range []error{extractor.ErrPRXRateLimited, extractor.ErrPRXNetwork} {
		if !errors.Is(err, err) {
			t.Fatalf("sentinel does not match itself: %v", err)
		}
		if !IsCategory(categorized("prx", err), ErrorNetwork) {
			t.Fatalf("PRX error %v was not categorized as network", err)
		}
	}
}

func TestProductRegistryRoutesPRXOpaqueSearches(t *testing.T) {
	registry := NewClient().productRegistry()
	for raw, want := range map[string]string{
		"prxstories:fixture query": "prx_stories_search",
		"prxseries:fixture query":  "prx_series_search",
	} {
		extractor, err := registry.Select(raw)
		if err != nil || extractor.Name() != want {
			t.Fatalf("route %q: extractor=%v err=%v", raw, extractor, err)
		}
	}
}

func TestProductPRXMultipartReentryTerminates(t *testing.T) {
	body := []byte(`{"id":"1","title":"Story","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"11","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/one.mp3"}}},{"id":"12","position":2,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/two.mp3"}}}]}}}}`)
	rt := &prxProductRoundTripper{body: body}
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{URL: "https://prx.org/stories/1", SkipDownload: true}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	root := ""
	op := &operation{client: NewClient(), request: request, transport: transport, registry: extractor.NewRegistry(extractor.NewPRXStory()), compatibility: plan, rootExtractor: &root}
	result, err := op.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if root != "prx_story" || rt.calls != 3 {
		t.Fatalf("root=%q calls=%d", root, rt.calls)
	}
	var decoded map[string]any
	if err := json.Unmarshal(result.InfoJSON, &decoded); err != nil {
		t.Fatal(err)
	}
	entries, ok := decoded["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("entries=%#v", decoded["entries"])
	}
	for n, raw := range entries {
		item := raw.(map[string]any)
		if item["id"] != fmt.Sprintf("1_part%d", n+1) {
			t.Fatalf("item=%#v", item)
		}
	}
}

func TestProductPRXAccountReentryOrder(t *testing.T) {
	media := func(id string) []byte {
		return []byte(`{"id":"` + id + `","_embedded":{"prx:audio":{"_embedded":{"prx:items":[{"id":"1","position":1,"contentType":"audio/mpeg","_links":{"enclosure":{"href":"https://media.example/a.mp3"}}}]}}}}`)
	}
	rt := &prxProductRoundTripper{bodies: map[string][]byte{
		"/api/v1/accounts/5": []byte(`{"id":"5","name":"A"}`), "/api/v1/accounts/5/series": []byte(`{"count":1,"total":1,"_embedded":{"prx:items":[{"id":"11"}]}}`), "/api/v1/series/11": []byte(`{"id":"11"}`), "/api/v1/series/11/stories": []byte(`{"count":1,"total":1,"_embedded":{"prx:items":[{"id":"12"}]}}`), "/api/v1/stories/12": media("12"), "/api/v1/accounts/5/stories": []byte(`{"count":1,"total":1,"_embedded":{"prx:items":[{"id":"13"}]}}`), "/api/v1/stories/13": media("13")}}
	transport, err := network.New(network.Config{RoundTripper: rt})
	if err != nil {
		t.Fatal(err)
	}
	req := Request{URL: "https://prx.org/accounts/5", SkipDownload: true}
	plan, err := prepareCompatibility(req)
	if err != nil {
		t.Fatal(err)
	}
	op := &operation{client: NewClient(), request: req, transport: transport, registry: extractor.NewRegistry(extractor.NewPRXAccount(), extractor.NewPRXSeries(), extractor.NewPRXStory()), compatibility: plan}
	if _, err = op.process(context.Background(), req.URL, "", nil, map[string]bool{}, 0); err != nil {
		t.Fatal(err)
	}
	want := "/api/v1/accounts/5,/api/v1/accounts/5/series,/api/v1/series/11,/api/v1/series/11/stories,/api/v1/stories/12,/api/v1/accounts/5/stories,/api/v1/stories/13"
	if rt.calls != 7 || strings.Join(rt.paths, ",") != want {
		t.Fatalf("paths=%v", rt.paths)
	}
}

func TestMediaFailuresAreCategorized(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{ffmpeg.ErrFFmpegUnavailable, ErrorUnsupported},
		{ffmpeg.ErrDestinationExists, ErrorInvalidInput},
		{ffmpeg.ErrMediaFailure, ErrorInternal},
		{pipeline.ErrMissingDASHTracks, ErrorInternal},
		{pipeline.ErrMissingToolset, ErrorInternal},
		{downloader.ErrExternalUnavailable, ErrorUnsupported},
		{downloader.ErrExternalFailed, ErrorInternal},
		{hls.ErrUnsupportedEncryption, ErrorUnsupported},
		{hls.ErrInvalidPlaylist, ErrorInternal},
		{dash.ErrUnsupportedAddressing, ErrorUnsupported},
		{dash.ErrInvalidMPD, ErrorInternal},
		{ism.ErrInvalidManifest, ErrorInternal},
		{mediaformat.ErrNoMatch, ErrorInvalidInput},
		{mediaformat.ErrFilterEvaluation, ErrorInvalidInput},
		{mediaformat.ErrNoFormats, ErrorInternal},
	} {
		if err := categorized("media", test.err); !IsCategory(err, test.category) {
			t.Fatalf("categorized(%v) = %v, want %s", test.err, err, test.category)
		}
	}
}

func TestClientCategorizesMissingCookieFileAsInvalidInput(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-cookies.txt")
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", CookieFile: missing, SkipDownload: true,
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, netscape.ErrFile) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientCategorizesCookieDirectoryAsInvalidInput(t *testing.T) {
	_, err := NewClient().Run(context.Background(), Request{
		URL: "https://example.com/media.mp4", CookieFile: t.TempDir(), SkipDownload: true,
	})
	if !IsCategory(err, ErrorInvalidInput) || !errors.Is(err, netscape.ErrFile) {
		t.Fatalf("error = %v", err)
	}
}

func TestClientSimulationSuppressesEveryOutputArtifact(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	result, err := NewClient().Run(context.Background(), Request{
		URL:             server.URL + "/page",
		OutputDir:       root,
		DownloadArchive: archivePath,
		Simulate:        true,
		Subtitles: SubtitleOptions{
			WriteManual: true,
			Languages:   []string{"es"},
		},
		Postprocessors: []Postprocessor{{
			Move: &MovePostprocessor{Destination: "moved.bin"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Downloaded || result.Archived || len(result.Artifacts) != 0 || result.Filename != "" || result.Bytes != 0 {
		t.Fatalf("simulation result = %+v", result)
	}
	for _, path := range []string{
		filepath.Join(root, "Deterministic Fixture.bin"),
		filepath.Join(root, "Deterministic Fixture.es.vtt"),
		filepath.Join(root, "moved.bin"),
		archivePath,
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("simulation wrote %s: %v", path, err)
		}
	}
	if !json.Valid(result.InfoJSON) {
		t.Fatalf("simulation metadata = %q", result.InfoJSON)
	}
	var info map[string]any
	if err := json.Unmarshal(result.InfoJSON, &info); err != nil {
		t.Fatal(err)
	}
	if requested, ok := info["requested_subtitles"].(map[string]any); !ok || requested["es"] == nil {
		t.Fatalf("simulation omitted selected subtitle metadata: %#v", info["requested_subtitles"])
	}
}

func TestClientDownloadsGenericOpenGraphMediaWithPageReferer(t *testing.T) {
	const media = "generic metadata media"
	var pageURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/article":
			writer.Header().Set("Content-Type", "text/html")
			if request.Method != http.MethodHead {
				_, _ = fmt.Fprintf(writer, `<meta property="og:title" content="Metadata Feature"><meta property="og:video" content="/protected.mp4">`)
			}
		case "/protected.mp4":
			if request.Header.Get("Referer") != pageURL {
				http.Error(writer, "missing referer", http.StatusForbidden)
				return
			}
			writer.Header().Set("Content-Type", "video/mp4")
			writer.Header().Set("Content-Length", strconv.Itoa(len(media)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write([]byte(media))
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	pageURL = server.URL + "/article"
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{
		URL: pageURL, OutputDir: root, OutputTemplate: "%(title)s.%(ext)s",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded {
		t.Fatalf("result = %+v", result)
	}
	downloaded, err := os.ReadFile(filepath.Join(root, "Metadata Feature.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != media {
		t.Fatalf("media = %q", downloaded)
	}
}

func TestClientRejectsInvalidWaveTwoOptionsBeforeNetwork(t *testing.T) {
	hits := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer server.Close()
	tests := []Request{
		{Downloader: DownloaderOptions{FragmentConcurrency: 129}},
		{Downloader: DownloaderOptions{RetryBaseDelay: 2 * time.Second, RetryMaxDelay: time.Second}},
		{Downloader: DownloaderOptions{External: &ExternalDownloader{Executable: "tool", Arguments: []string{"bad\nargument"}}}},
		{Postprocessors: []Postprocessor{{}}},
		{Postprocessors: []Postprocessor{{Move: &MovePostprocessor{Destination: "out.mp4"}, Remux: &RemuxPostprocessor{Destination: "out.mkv"}}}},
		{OutputDir: t.TempDir(), Postprocessors: []Postprocessor{{Move: &MovePostprocessor{Destination: "../escape.mp4"}}}},
		{YouTubeComments: YouTubeCommentOptions{Sort: "popular"}},
		{YouTubeComments: YouTubeCommentOptions{MaxComments: 10_001}},
		{YouTubeComments: YouTubeCommentOptions{MaxDepth: 9}},
		{BreakMatchFilters: []string{"-"}},
		{MatchFilters: []string{"-"}},
	}
	for index, request := range tests {
		request.URL = server.URL + "/media.mp4"
		if _, err := NewClient().Run(context.Background(), request); !IsCategory(err, ErrorInvalidInput) {
			t.Errorf("case %d error = %v", index, err)
		}
	}
	if hits != 0 {
		t.Fatalf("preflight-invalid requests made %d network calls", hits)
	}
}

func TestClientAppliesMetadataBeforeMatchFilter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	var events []Event
	result, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	})).Run(context.Background(), Request{
		URL: server.URL + "/clip.mp4", SkipDownload: true,
		ReplaceMetadata: []string{"title:clip:renamed"}, MatchFilters: []string{"title=other"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !strings.Contains(result.SkipReason, "renamed") {
		t.Fatalf("result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["title"] != "renamed" {
		t.Fatalf("metadata title = %#v", metadata["title"])
	}
	if events[len(events)-1].Kind != EventMatchFilterSkipped {
		t.Fatalf("events = %#v", events)
	}
}

type numericMetadataExtractor struct{}

func (numericMetadataExtractor) Name() string           { return "numeric-metadata" }
func (numericMetadataExtractor) Suitable(*url.URL) bool { return true }
func (numericMetadataExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("fixture")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "duration", Value: value.Int(4)},
	))), nil
}

type interactiveFormatExtractor struct{}

func TestFormatCheckModePrecedenceOverAllowUnplayable(t *testing.T) {
	for _, test := range []struct {
		name        string
		mode        FormatCheckMode
		allow, want bool
	}{
		{"auto", FormatCheckAuto, false, true},
		{"auto allow", FormatCheckAuto, true, false},
		{"none", FormatCheckNone, false, false},
		{"none allow", FormatCheckNone, true, false},
		{"selected", FormatCheckSelected, false, true},
		{"selected allow", FormatCheckSelected, true, true},
		{"all", FormatCheckAll, false, true},
		{"all allow", FormatCheckAll, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldCheckFormats(test.mode, test.allow); got != test.want {
				t.Fatalf("shouldCheckFormats(%d, %v)=%v want %v", test.mode, test.allow, got, test.want)
			}
		})
	}
}

func (interactiveFormatExtractor) Name() string           { return "interactive-format" }
func (interactiveFormatExtractor) Suitable(*url.URL) bool { return true }
func (interactiveFormatExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	info := formatSelectorInfo()
	info.Set("id", value.String("fixture"))
	info.Set("title", value.String("Fixture"))
	info.Set("duration", value.Int(4))
	if formats, ok := info.Formats(); ok {
		for _, candidate := range formats {
			object, objectOK := candidate.Object()
			if !objectOK {
				continue
			}
			formatID, _ := object.Lookup("format_id").StringValue()
			if formatID != "a128" {
				continue
			}
			object.Set("abr", value.Int(128))
			object.Set("language", value.String("en"))
			object.Set("container", value.String("m4a_dash"))
		}
	}
	return extractor.Media(info), nil
}

func TestClientInteractiveFormatRepromptsThenSelects(t *testing.T) {
	responses := []string{"[", "bestaudio"}
	calls := 0
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true, Format: "-",
		InteractiveFormat: func(_ context.Context, prompt InteractiveFormatPrompt) (string, error) {
			calls++
			if calls == 2 && prompt.Error == "" {
				t.Fatalf("second prompt has no diagnostic")
			}
			return responses[calls-1], nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}), compatibility: plan}
	prepared, err := mediaformat.Prepare(formatSelectorInfo(), plan.formatOptions)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := operation.planPreparedFormats(prepared)
	if err != nil || calls != 2 || len(plans) != 1 || len(plans[0].Tracks) != 1 {
		t.Fatalf("calls=%d plans=%#v err=%v", calls, plans, err)
	}
}

type deferredMetadataExtractor struct {
	calls *int
}

func (deferredMetadataExtractor) Name() string           { return "deferred-metadata" }
func (deferredMetadataExtractor) Suitable(*url.URL) bool { return true }
func (fixture deferredMetadataExtractor) Extract(context.Context, extractor.Request) (extractor.Extraction, error) {
	extraction := extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("deferred")},
		value.Field{Key: "title", Value: value.String("Fixture")},
		value.Field{Key: "url", Value: value.String("https://fixture.invalid/media.mp4")},
	)))
	extraction.Enrich = func(_ context.Context, info *value.Info) error {
		*fixture.calls++
		info.Set("comments", value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "id", Value: value.String("comment")},
		))))
		info.Set("comment_count", value.Int(1))
		return nil
	}
	return extraction, nil
}

func TestClientDefersExpensiveMetadataUntilAfterMatchFilter(t *testing.T) {
	for _, test := range []struct {
		filter    string
		wantCalls int
		wantSkip  bool
	}{
		{filter: "title=discarded", wantCalls: 0, wantSkip: true},
		{filter: "title=Fixture", wantCalls: 1},
	} {
		calls := 0
		request := Request{URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{test.filter}}
		compatibility, err := prepareCompatibility(request)
		if err != nil {
			t.Fatal(err)
		}
		operation := &operation{
			client: NewClient(), request: request,
			registry:      extractor.NewRegistry(deferredMetadataExtractor{calls: &calls}),
			compatibility: compatibility,
		}
		result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
		if err != nil {
			t.Fatal(err)
		}
		if calls != test.wantCalls || result.Skipped != test.wantSkip {
			t.Fatalf("filter=%q calls=%d skipped=%v", test.filter, calls, result.Skipped)
		}
		var metadata map[string]any
		if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		_, hasComments := metadata["comments"]
		if hasComments != (test.wantCalls == 1) {
			t.Fatalf("filter=%q metadata=%#v", test.filter, metadata)
		}
	}
}

func TestClientDoesNotEnrichArchivedMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "archive.txt")
	if err := os.WriteFile(path, []byte("deferred-metadata deferred\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(context.Background(), path, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	request := Request{URL: "https://fixture.invalid/video", SkipDownload: true}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request,
		registry:      extractor.NewRegistry(deferredMetadataExtractor{calls: &calls}),
		compatibility: compatibility, archive: store,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !result.Archived {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
}

func TestClientCategorizesMatchFilterEvaluationFailure(t *testing.T) {
	request := Request{URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{"duration *= 4"}}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	_, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err == nil {
		t.Fatal("process() error = nil")
	}
	if !IsCategory(err, ErrorInvalidInput) {
		t.Fatalf("process() category = %v, want %v", err, ErrorInvalidInput)
	}
	if !errors.Is(err, matchfilter.ErrEvaluation) {
		t.Fatalf("process() error = %v, want matchfilter.ErrEvaluation", err)
	}
}

func TestClientBreakMatchFilterStopsSingleMedia(t *testing.T) {
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true,
		BreakMatchFilters: []string{"title=other"},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !result.Stopped || result.SkipReason == "" || result.StopReason != result.SkipReason {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientOrdinaryFilterRejectionDoesNotBecomeBreakStop(t *testing.T) {
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true,
		BreakMatchFilters: []string{"title=Fixture"},
		MatchFilters:      []string{"title=other"},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.Stopped || result.StopReason != "" {
		t.Fatalf("result = %#v", result)
	}
}

func TestClientBreakMatchFilterUsesPythonRegex(t *testing.T) {
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true,
		BreakMatchFilters: []string{`title ~= ^Fixture(?=$)`},
		MatchFilters:      []string{"title=other"},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || result.Stopped {
		t.Fatalf("matched break regex result = %#v", result)
	}

	request.BreakMatchFilters = []string{`title ~= ^Other(?=$)`}
	plan, err = prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation.compatibility = plan
	result, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || !result.Stopped {
		t.Fatalf("rejected break regex did not stop: %#v", result)
	}
}

func TestClientInteractiveMatchFilterAcceptRejectAndOrdering(t *testing.T) {
	for _, test := range []struct {
		name         string
		filters      []string
		breakFilters []string
		accept       bool
		wantCalls    int
		wantSkipped  bool
		wantStopped  bool
	}{
		{name: "accept", filters: []string{"-"}, accept: true, wantCalls: 1},
		{
			name: "merged selected fields pass",
			filters: []string{
				"-",
				"height=1080 & resolution=1080p & abr=128 & language=en & protocol=https+https",
			},
			accept: true, wantCalls: 1,
		},
		{name: "reject", filters: []string{"-"}, wantCalls: 1, wantSkipped: true},
		{name: "ordinary filter rejects before prompt", filters: []string{"-", "title=other"}, accept: true, wantSkipped: true},
		{name: "missing non-format field rejects before prompt", filters: []string{"-", "uploader=alice"}, accept: true, wantSkipped: true},
		{
			name: "breaking accept bypasses selected-format rejection", filters: []string{"format_id=other"},
			breakFilters: []string{"-"}, accept: true, wantCalls: 1,
		},
		{
			name: "breaking reject stops", breakFilters: []string{"-"},
			wantCalls: 1, wantSkipped: true, wantStopped: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			request := Request{
				URL: "https://fixture.invalid/video", SkipDownload: true,
				OutputDir: t.TempDir(), OutputTemplate: "%(title)s.out",
				MatchFilters: test.filters, BreakMatchFilters: test.breakFilters,
				InteractiveMatchFilter: func(_ context.Context, prompt InteractiveMatchFilterPrompt) (bool, error) {
					calls++
					if prompt.ID != "fixture" || prompt.Title != "Fixture" ||
						filepath.Base(prompt.Filename) != "Fixture.out" {
						t.Fatalf("prompt = %#v", prompt)
					}
					return test.accept, nil
				},
			}
			plan, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			operation := &operation{
				client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}),
				compatibility: plan,
			}
			result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if err != nil {
				t.Fatal(err)
			}
			if calls != test.wantCalls || result.Skipped != test.wantSkipped || result.Stopped != test.wantStopped {
				t.Fatalf("calls=%d result=%#v", calls, result)
			}
			if test.wantSkipped && calls == 1 && result.SkipReason != "Skipping Fixture" {
				t.Fatalf("skip reason = %q", result.SkipReason)
			}
		})
	}
}

func TestClientInteractiveMatchFilterDoesNotPromptForArchiveMatch(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "archive.txt")
	if err := os.WriteFile(archivePath, []byte("numeric-metadata fixture\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := archive.Open(context.Background(), archivePath, archive.Options{})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{"-"},
		InteractiveMatchFilter: func(context.Context, InteractiveMatchFilterPrompt) (bool, error) {
			calls++
			return false, nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan, archive: store,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || !result.Archived || result.Skipped {
		t.Fatalf("calls=%d result=%#v", calls, result)
	}
}

func TestClientInteractiveMatchFilterUsesSelectedFormatFilename(t *testing.T) {
	calls := 0
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true,
		Format: "bestaudio", OutputDir: t.TempDir(), OutputTemplate: "%(format_id)s-%(vcodec)s-%(acodec)s.%(ext)s",
		MatchFilters: []string{
			"-",
			"format_id=a128 & vcodec=none & acodec=aac & height=0 & abr=128 & language=en & container=m4a_dash & protocol=https",
		},
		InteractiveMatchFilter: func(_ context.Context, prompt InteractiveMatchFilterPrompt) (bool, error) {
			calls++
			if filepath.Base(prompt.Filename) != "a128-none-aac.m4a" {
				t.Fatalf("prompt filename = %q", prompt.Filename)
			}
			return true, nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}),
		compatibility: plan,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || calls != 1 || result.Skipped {
		t.Fatalf("calls=%d result=%#v error=%v", calls, result, err)
	}
}

func TestClientInteractiveBreakingFilterReevaluatesSelectedFormat(t *testing.T) {
	calls := 0
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true, Format: "bestaudio",
		BreakMatchFilters: []string{"-", "format_id=wrong"},
		InteractiveMatchFilter: func(context.Context, InteractiveMatchFilterPrompt) (bool, error) {
			calls++
			return true, nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}),
		compatibility: plan,
	}
	result, err := operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || calls != 0 || !result.Skipped || !result.Stopped {
		t.Fatalf("calls=%d result=%#v error=%v", calls, result, err)
	}
}

func TestClientInteractiveMatchFilterDoesNotPromptWithoutFormats(t *testing.T) {
	calls := 0
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{"-"},
		InteractiveMatchFilter: func(context.Context, InteractiveMatchFilterPrompt) (bool, error) {
			calls++
			return true, nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(numericMetadataExtractor{}),
		compatibility: plan,
	}
	_, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if !errors.Is(err, mediaformat.ErrNoFormats) || calls != 0 {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestClientInteractiveMatchFilterPromptsPerOutput(t *testing.T) {
	var prompts []InteractiveMatchFilterPrompt
	request := Request{
		URL: "https://fixture.invalid/video", SkipDownload: true,
		Format: "bestvideo,bestaudio", MatchFilters: []string{"-"},
		InteractiveMatchFilter: func(_ context.Context, prompt InteractiveMatchFilterPrompt) (bool, error) {
			prompts = append(prompts, prompt)
			return true, nil
		},
	}
	plan, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}),
		compatibility: plan,
	}
	_, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
	if err != nil || len(prompts) != 2 {
		t.Fatalf("prompts=%#v error=%v", prompts, err)
	}
	if prompts[0].Filename == prompts[1].Filename || prompts[0].Filename == "" || prompts[1].Filename == "" {
		t.Fatalf("prompts do not identify distinct outputs: %#v", prompts)
	}
}

func TestClientInteractiveMatchFilterCallbackErrorsAreCategorized(t *testing.T) {
	for _, test := range []struct {
		name      string
		callback  error
		category  ErrorCategory
		wantInput bool
	}{
		{
			name: "input", callback: fmt.Errorf("%w: %v", ErrInteractiveInput, io.EOF),
			category: ErrorInvalidInput, wantInput: true,
		},
		{name: "cancelled", callback: context.Canceled, category: ErrorCancelled},
		{name: "unexpected callback failure", callback: io.ErrUnexpectedEOF, category: ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := Request{
				URL: "https://fixture.invalid/video", SkipDownload: true, MatchFilters: []string{"-"},
				InteractiveMatchFilter: func(context.Context, InteractiveMatchFilterPrompt) (bool, error) {
					return false, test.callback
				},
			}
			plan, err := prepareCompatibility(request)
			if err != nil {
				t.Fatal(err)
			}
			operation := &operation{
				client: NewClient(), request: request, registry: extractor.NewRegistry(interactiveFormatExtractor{}),
				compatibility: plan,
			}
			_, err = operation.process(context.Background(), request.URL, "", nil, make(map[string]bool), 0)
			if !IsCategory(err, test.category) || errors.Is(err, ErrInteractiveInput) != test.wantInput {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestClientRendersProgressTemplateIntoEvents(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	var messages []string
	result, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		if event.Kind == EventDownloadProgress {
			messages = append(messages, event.Message)
		}
		return nil
	})).Run(context.Background(), Request{
		URL: server.URL + "/one.mp4", OutputDir: t.TempDir(),
		ProgressTemplate: "%(status)s:%(downloaded_bytes)d",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || len(messages) == 0 || !strings.HasPrefix(messages[len(messages)-1], EventDownloadProgress+":") {
		t.Fatalf("result=%#v messages=%#v", result, messages)
	}
}

func TestOperationUsesRequestedFormatSelection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.TrimPrefix(request.URL.Path, "/")))
	}))
	defer server.Close()
	transport, _ := network.New(network.Config{})
	request := Request{OutputDir: t.TempDir(), Format: "low"}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("selection")},
		value.Field{Key: "title", Value: value.String("selection")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("high")}, value.Field{Key: "url", Value: value.String(server.URL + "/high")}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "height", Value: value.Int(1080)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
			value.ObjectValue(value.NewObject(value.Field{Key: "format_id", Value: value.String("low")}, value.Field{Key: "url", Value: value.String(server.URL + "/low")}, value.Field{Key: "ext", Value: value.String("mp4")}, value.Field{Key: "height", Value: value.Int(360)}, value.Field{Key: "vcodec", Value: value.String("avc")}, value.Field{Key: "acodec", Value: value.String("aac")})),
		)},
	))
	operation := &operation{client: NewClient(), request: request, transport: transport, compatibility: compatibility}
	result, err := operation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "low" {
		t.Fatalf("selected contents = %q, error = %v", contents, err)
	}
}

func TestOperationPostLivePreferenceKeepsExplicitDirectFormatAuthoritative(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/incomplete" || request.URL.Query().Get("pot") != "fixture" {
			http.Error(writer, "unexpected request", http.StatusBadRequest)
			return
		}
		_, _ = writer.Write([]byte("explicit-direct"))
	}))
	defer server.Close()
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("post-live-selection")},
		value.Field{Key: "title", Value: value.String("post-live-selection")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("137")},
				value.Field{Key: "url", Value: value.String(server.URL + "/video")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "height", Value: value.Int(720)},
				value.Field{Key: "vcodec", Value: value.String("avc")},
				value.Field{Key: "acodec", Value: value.String("none")},
				value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
				value.Field{Key: "target_duration", Value: value.Float(5)},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("140")},
				value.Field{Key: "url", Value: value.String(server.URL + "/audio")},
				value.Field{Key: "ext", Value: value.String("m4a")},
				value.Field{Key: "vcodec", Value: value.String("none")},
				value.Field{Key: "acodec", Value: value.String("aac")},
				value.Field{Key: "_youtube_post_live", Value: value.Bool(true)},
				value.Field{Key: "target_duration", Value: value.Float(5)},
			)),
			value.ObjectValue(value.NewObject(
				value.Field{Key: "format_id", Value: value.String("18")},
				value.Field{Key: "url", Value: value.String(server.URL + "/incomplete?pot=fixture")},
				value.Field{Key: "ext", Value: value.String("mp4")},
				value.Field{Key: "height", Value: value.Int(2160)},
				value.Field{Key: "vcodec", Value: value.String("avc")},
				value.Field{Key: "acodec", Value: value.String("aac")},
				value.Field{Key: "preference", Value: value.Int(-10)},
			)),
		)},
	))
	defaultOperation := &operation{compatibility: compatibilityPlan{}}
	selected, err := defaultOperation.selectFormats(info)
	if err != nil || len(selected) != 2 || selected[0].ID != "137" || selected[1].ID != "140" {
		t.Fatalf("default selected = %#v, %v", selected, err)
	}
	sortedCompatibility, err := prepareCompatibility(Request{FormatSort: []string{"height"}})
	if err != nil {
		t.Fatal(err)
	}
	sortedOperation := &operation{compatibility: sortedCompatibility}
	sorted, err := sortedOperation.selectFormats(info)
	if err != nil || len(sorted) != 2 || sorted[0].ID != "137" || sorted[1].ID != "140" {
		t.Fatalf("height-sorted selected = %#v, %v", sorted, err)
	}

	request := Request{OutputDir: t.TempDir(), Format: "18"}
	compatibility, err := prepareCompatibility(request)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	explicitOperation := &operation{
		client: NewClient(), request: request, transport: transport, compatibility: compatibility,
	}
	explicit, err := explicitOperation.selectFormats(info)
	if err != nil || len(explicit) != 1 || explicit[0].ID != "18" || explicit[0].YouTubePostLive {
		t.Fatalf("explicit selected = %#v, %v", explicit, err)
	}
	result, err := explicitOperation.processMedia(context.Background(), extractor.Media(info), "fixture")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(result.Filename)
	if err != nil || string(body) != "explicit-direct" {
		t.Fatalf("explicit body = %q, %v", body, err)
	}
}

func TestClientExtractAudioPostprocessorIntegration(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	root := t.TempDir()
	source := filepath.Join(fixtureRoot, "source.mp4")
	output, err := exec.Command(ffmpegPath, "-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=700:duration=0.2", "-c:a", "aac", source).CombinedOutput()
	if err != nil {
		t.Fatalf("generate fixture: %v: %s", err, output)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		http.ServeFile(writer, request, source)
	}))
	defer server.Close()
	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/source.mp4", OutputDir: root,
		Postprocessors: []Postprocessor{{ExtractAudio: &ExtractAudioPostprocessor{Codec: "mp3"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(result.Filename) != ".mp3" || len(result.Artifacts) != 1 || result.Artifacts[0].Kind != "media" {
		t.Fatalf("result = %#v", result)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(context.Background(), result.Filename)
	if err != nil || len(probe.Streams) == 0 || probe.Streams[0].CodecType != "audio" {
		t.Fatalf("probe = %#v, error = %v", probe, err)
	}
}

func TestJavaScriptHelperConfigurationTakesPrecedence(t *testing.T) {
	configured := filepath.Join(t.TempDir(), "custom-helper")
	if got := discoverJavaScriptHelper(configured); got != configured {
		t.Fatalf("discoverJavaScriptHelper() = %q, want %q", got, configured)
	}
}

func TestBrowserCookieSpec(t *testing.T) {
	for _, test := range []struct {
		spec, browser, profile, container string
	}{
		{"chrome", "chrome", "", ""},
		{"chromium:Default", "chromium", "Default", ""},
		{"brave:Profile 1", "brave", "Profile 1", ""},
		{"firefox:work::Work", "firefox", "work", "Work"},
		{"safari", "safari", "", ""},
		{"safari:/tmp/Cookies.binarycookies", "safari", "/tmp/Cookies.binarycookies", ""},
	} {
		options, err := parseBrowserCookieSpec(test.spec)
		if err != nil || options.browser != test.browser || options.profile != test.profile || options.container != test.container {
			t.Fatalf("parseBrowserCookieSpec(%q) = %#v, %v", test.spec, options, err)
		}
	}
	for _, spec := range []string{"safari:", "safari:relative", "safari::Work", "chrome:", "chrome:../Default", "chrome:one:two", "chrome::Work", "firefox:default::", "firefox:default::one:two"} {
		if _, err := parseBrowserCookieSpec(spec); !errors.Is(err, errInvalidBrowserCookieSpec) {
			t.Fatalf("parseBrowserCookieSpec(%q) error = %v", spec, err)
		}
	}
}

func TestClientImportsBrowserCookiesBeforeExtraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("authenticated")
		if err != nil || cookie.Value != "present" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	client.platform = "darwin"
	var optionsSeen chromium.Options
	client.browserCookieImporter = func(_ context.Context, options chromium.Options) (chromium.Result, error) {
		optionsSeen = options
		return chromium.Result{
			Cookies: []*http.Cookie{{Name: "authenticated", Value: "present", Domain: target.Hostname(), Path: "/"}},
			Total:   2, Imported: 1, Failed: 1,
		}, chromium.ErrDecrypt
	}
	result, err := client.Run(context.Background(), Request{
		URL: server.URL + "/protected.mp4", CookiesFromBrowser: "chrome:Profile 1", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "generic" || optionsSeen.Profile != "Profile 1" {
		t.Fatalf("result=%#v options=%#v", result, optionsSeen)
	}
	if len(events) < 2 || events[0].Kind != "browser_cookies" || events[0].Message != "imported 1 of 2 browser cookies; skipped 1" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientLoadsNetscapeCookieFileBeforeExtraction(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("from_file")
		if err != nil || cookie.Value != "present" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	cookieFile := filepath.Join(t.TempDir(), "cookies.txt")
	line := target.Hostname() + "\tFALSE\t/\tFALSE\t0\tfrom_file\tpresent\n"
	if err := os.WriteFile(cookieFile, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/media.mp4", CookieFile: cookieFile, SkipDownload: true})
	if err != nil || result.Extractor != "generic" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if len(events) < 3 || events[0].Kind != EventBrowserCookies || events[0].Message != "imported 1 of 1 cookie-file entries" {
		t.Fatalf("events = %#v", events)
	}
}

func TestPortableBrowserCookieDispatch(t *testing.T) {
	client := NewClient()
	client.safariCookieImporter = func(_ context.Context, options safari.Options) (safari.Result, error) {
		if options.DatabasePath != "/tmp/Cookies.binarycookies" {
			t.Fatalf("Safari options = %#v", options)
		}
		return safari.Result{Total: 2, Imported: 1, Failed: 1}, nil
	}
	result, err := client.importBrowserCookies(context.Background(), browserCookieSpec{
		browser: "safari", profile: "/tmp/Cookies.binarycookies",
	})
	if err != nil || result.Total != 2 || result.Imported != 1 || result.Failed != 1 {
		t.Fatalf("Safari result=%#v err=%v", result, err)
	}
	client.firefoxCookieImporter = func(_ context.Context, options firefox.Options) (firefox.Result, error) {
		if options.Profile != "fixture" || options.Container != "Work" {
			t.Fatalf("Firefox options = %#v", options)
		}
		return firefox.Result{Total: 1, Imported: 1}, nil
	}
	result, err = client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "firefox", profile: "fixture", container: "Work"})
	if err != nil || result.Imported != 1 {
		t.Fatalf("Firefox result=%#v err=%v", result, err)
	}
	client.platform = "linux"
	client.linuxCookieImporter = func(_ context.Context, options chromiumlinux.Options) (chromiumlinux.Result, error) {
		if options.Browser != chromiumlinux.Brave || options.Profile != "Profile 1" {
			t.Fatalf("Linux Chromium options = %#v", options)
		}
		return chromiumlinux.Result{Total: 2, Imported: 1, Failed: 1}, chromiumlinux.ErrKeyUnavailable
	}
	result, err = client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "brave", profile: "Profile 1"})
	if !errors.Is(err, chromiumlinux.ErrKeyUnavailable) || result.Imported != 1 || result.Failed != 1 {
		t.Fatalf("Linux result=%#v err=%v", result, err)
	}
	client.platform = "windows"
	client.windowsCookieImporter = func(_ context.Context, options chromiumwindows.Options) (chromiumwindows.Result, error) {
		if options.Browser != chromiumwindows.Edge || options.Profile != "Profile 2" {
			t.Fatalf("Windows Chromium options = %#v", options)
		}
		return chromiumwindows.Result{Total: 3, Imported: 2, Failed: 1}, chromiumwindows.ErrAppBound
	}
	result, err = client.importBrowserCookies(context.Background(), browserCookieSpec{browser: "edge", profile: "Profile 2"})
	if !errors.Is(err, chromiumwindows.ErrAppBound) || result.Imported != 2 || result.Failed != 1 {
		t.Fatalf("Windows result=%#v err=%v", result, err)
	}
}

func TestClientWindowsCookiePartialAppBoundFailurePreservesCookies(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("windows_session")
		if err != nil || cookie.Value != "present" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	target, _ := url.Parse(server.URL)
	client := NewClient()
	client.platform = "windows"
	client.windowsCookieImporter = func(context.Context, chromiumwindows.Options) (chromiumwindows.Result, error) {
		return chromiumwindows.Result{
			Cookies: []*http.Cookie{{Name: "windows_session", Value: "present", Domain: target.Hostname(), Path: "/"}},
			Total:   2, Imported: 1, Failed: 1,
		}, chromiumwindows.ErrAppBound
	}
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/protected.mp4", CookiesFromBrowser: "edge:Default", SkipDownload: true})
	if err != nil || result.Extractor != "generic" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestExtractionEventsRedactInputURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", "4")
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte("data"))
		}
	}))
	defer server.Close()
	var captured []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		captured = append(captured, event)
		return nil
	}))
	_, err := client.Run(context.Background(), Request{
		URL: server.URL + "/media.mp4?token=input-secret&visible=yes", SkipDownload: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(captured) != 2 {
		t.Fatalf("events = %#v", captured)
	}
	for _, event := range captured {
		if strings.Contains(event.URL, "secret") || !strings.Contains(event.URL, "visible=yes") {
			t.Fatalf("event URL was not safely redacted: %#v", event)
		}
	}
}

func TestClientBrowserCookieFailureIsAuthenticationError(t *testing.T) {
	client := NewClient()
	client.platform = "darwin"
	client.browserCookieImporter = func(context.Context, chromium.Options) (chromium.Result, error) {
		return chromium.Result{}, chromium.ErrKeyUnavailable
	}
	_, err := client.Run(context.Background(), Request{URL: "https://example.invalid/media.mp4", CookiesFromBrowser: "chrome", SkipDownload: true})
	if !IsCategory(err, ErrorAuthentication) || !errors.Is(err, chromium.ErrKeyUnavailable) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientDoesNotRecoverFromCancelledCookieImport(t *testing.T) {
	client := NewClient()
	client.platform = "darwin"
	client.browserCookieImporter = func(context.Context, chromium.Options) (chromium.Result, error) {
		return chromium.Result{
			Cookies:  []*http.Cookie{{Name: "partial", Value: "secret", Domain: "example.invalid", Path: "/"}},
			Total:    2,
			Imported: 1,
		}, context.Canceled
	}
	_, err := client.Run(context.Background(), Request{URL: "https://example.invalid/media.mp4", CookiesFromBrowser: "chrome", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientCancellationReachesTransport(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, err := NewClient().Run(ctx, Request{URL: server.URL + "/slow?delay=1s", SkipDownload: true})
	if !IsCategory(err, ErrorCancelled) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestClientWalkingSkeleton(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	var events []Event
	client := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	}))
	result, err := client.Run(context.Background(), Request{URL: server.URL + "/page", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Extractor != "fixture" {
		t.Fatalf("result = %#v", result)
	}
	if !json.Valid(result.InfoJSON) {
		t.Fatalf("invalid metadata JSON: %s", result.InfoJSON)
	}
	downloaded, err := os.ReadFile(result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	if string(downloaded) != string(server.Media()) {
		t.Fatal("downloaded media mismatch")
	}
	if len(events) < 4 || events[0].Kind != "extracting" || events[len(events)-1].Kind != "download_completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestClientDownloadArchiveRecordsAndSkips(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	root := t.TempDir()
	archivePath := filepath.Join(root, "archive.txt")
	request := Request{URL: server.URL + "/page", OutputDir: root, DownloadArchive: archivePath}
	first, err := NewClient().Run(context.Background(), request)
	if err != nil || !first.Downloaded || first.Archived {
		t.Fatalf("first result=%#v err=%v", first, err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil || string(data) != "fixture fixture-direct\n" {
		t.Fatalf("archive=%q err=%v", data, err)
	}
	var events []Event
	second, err := NewClient(WithEventHandler(func(_ context.Context, event Event) error {
		events = append(events, event)
		return nil
	})).Run(context.Background(), request)
	if err != nil || second.Downloaded || !second.Archived || second.Bytes != 0 {
		t.Fatalf("second result=%#v err=%v", second, err)
	}
	found := false
	for _, event := range events {
		if event.Kind == EventArchiveMatch && event.Message == "fixture fixture-direct" {
			found = true
		}
	}
	if !found {
		t.Fatalf("archive event missing: %#v", events)
	}
}

func TestClientInitializesConfiguredCacheSafely(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	cacheRoot := filepath.Join(t.TempDir(), "cache")
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/page", CacheDir: cacheRoot, SkipDownload: true})
	if err != nil || result.Extractor != "fixture" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	info, err := os.Lstat(cacheRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("cache root info=%v err=%v", info, err)
	}
	unsafe := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(cacheRoot, unsafe); err == nil {
		_, err = NewClient().Run(context.Background(), Request{URL: server.URL + "/page", CacheDir: unsafe, SkipDownload: true})
		if !IsCategory(err, ErrorInvalidInput) {
			t.Fatalf("unsafe cache error = %v", err)
		}
	}
}

func TestClientHLSAndDASHDispatch(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	for _, test := range []struct {
		name     string
		path     string
		expected []byte
	}{
		{"HLS", "/hls/master.m3u8", server.HLSMedia()},
		{"DASH", "/dash/manifest.mpd", server.DASHMedia()},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := NewClient().Run(context.Background(), Request{URL: server.URL + test.path, OutputDir: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			contents, err := os.ReadFile(result.Filename)
			if err != nil {
				t.Fatal(err)
			}
			if string(contents) != string(test.expected) {
				t.Fatalf("contents = %q, want %q", contents, test.expected)
			}
		})
	}
}

func TestClientHLSSuppressesAttributedAdFragments(t *testing.T) {
	var adRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/media.m3u8":
			writer.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			if request.Method != http.MethodHead {
				_, _ = fmt.Fprint(writer, `#EXTM3U
#EXTINF:1,
content-a.bin
#ANVATO-SEGMENT-INFO:type=ad
#EXTINF:1,
ad.bin
#ANVATO-SEGMENT-INFO:type=master
#EXTINF:1,
content-b.bin
#EXT-X-ENDLIST
`)
			}
		case "/content-a.bin":
			_, _ = writer.Write([]byte("content-a-"))
		case "/content-b.bin":
			_, _ = writer.Write([]byte("content-b"))
		case "/ad.bin":
			adRequests.Add(1)
			_, _ = writer.Write([]byte("advertisement"))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	result, err := NewClient().Run(context.Background(), Request{
		URL: server.URL + "/media.m3u8", OutputDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "content-a-content-b" || adRequests.Load() != 0 {
		t.Fatalf("contents=%q ad requests=%d err=%v", contents, adRequests.Load(), err)
	}
}

func TestClientISMDispatch(t *testing.T) {
	const manifest = `<SmoothStreamingMedia TimeScale="10" Duration="20"><StreamIndex Type="video" Url="video/QualityLevels({bitrate})/Fragments(video={start time})"><QualityLevel Bitrate="200" FourCC="H264"/><c t="0" d="10" r="1"/></StreamIndex></SmoothStreamingMedia>`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/Manifest" {
			writer.Header().Set("Content-Type", "application/vnd.ms-sstr+xml")
			writer.Header().Set("Content-Length", fmt.Sprint(len(manifest)))
			if request.Method != http.MethodHead {
				_, _ = writer.Write([]byte(manifest))
			}
			return
		}
		_, _ = writer.Write([]byte(filepath.Base(request.URL.Path)))
	}))
	defer server.Close()
	root := t.TempDir()
	result, err := NewClient().Run(context.Background(), Request{URL: server.URL + "/Manifest", OutputDir: root})
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(result.Filename)
	if err != nil || string(contents) != "Fragments(video=0)Fragments(video=10)" {
		t.Fatalf("ISM output = %q, error = %v", contents, err)
	}
}

func TestClientDASHMergeDispatch(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	video := filepath.Join(root, "source-video.mp4")
	audio := filepath.Join(root, "source-audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.2", "-an", "-c:v", "mpeg4", video)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=700:duration=0.2", "-vn", "-c:a", "aac", audio)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/manifest.mpd":
			writer.Header().Set("Content-Type", "application/dash+xml")
			_, _ = fmt.Fprint(writer, `<MPD type="static"><Period>
<AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="video" bandwidth="1000"><BaseURL>video.mp4</BaseURL></Representation></AdaptationSet>
<AdaptationSet contentType="audio" mimeType="audio/mp4"><Representation id="audio" bandwidth="128"><BaseURL>audio.m4a</BaseURL></Representation></AdaptationSet>
</Period></MPD>`)
		case "/video.mp4":
			http.ServeFile(writer, request, video)
		case "/audio.m4a":
			http.ServeFile(writer, request, audio)
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	result, err := NewClient().Run(ctx, Request{URL: server.URL + "/manifest.mpd", OutputDir: root, Overwrite: true})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, result.Filename)
	if err != nil {
		t.Fatal(err)
	}
	types := make(map[string]bool)
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("merged streams = %#v", probe.Streams)
	}
}

func TestYouTubePostLiveAdaptiveTracksDownloadAndMerge(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.mp4")
	audioPath := filepath.Join(fixtureRoot, "audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=blue:s=16x16:d=0.3", "-an", "-c:v", "mpeg4", videoPath)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=880:duration=0.3", "-vn", "-c:a", "aac", audioPath)
	readChunks := func(path string) [][]byte {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		chunks := make([][]byte, 4)
		for index := range chunks {
			start := len(body) * index / len(chunks)
			end := len(body) * (index + 1) / len(chunks)
			chunks[index] = body[start:end]
		}
		return chunks
	}
	chunks := map[string][][]byte{
		"/video": readChunks(videoPath),
		"/audio": readChunks(audioPath),
	}
	var requestMu sync.Mutex
	requested := map[string][]int{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Live-Fixture") != "post-live" ||
			(request.URL.Query().Get("token") != "video" && request.URL.Query().Get("token") != "audio") {
			http.Error(writer, "missing signed request context", http.StatusForbidden)
			return
		}
		sequenceText := request.URL.Query().Get("sq")
		if sequenceText == "" {
			writer.Header().Set("X-Head-Seqnum", "5")
			return
		}
		sequence, err := strconv.Atoi(sequenceText)
		if err != nil || sequence < 0 || sequence >= len(chunks[request.URL.Path]) {
			http.Error(writer, "bad sequence", http.StatusBadRequest)
			return
		}
		requestMu.Lock()
		requested[request.URL.Path] = append(requested[request.URL.Path], sequence)
		requestMu.Unlock()
		_, _ = writer.Write(chunks[request.URL.Path][sequence])
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{OutputDir: root, Downloader: DownloaderOptions{
			MaxSegments: 16, MaxSegmentBytes: 8 << 20, FragmentConcurrency: 2,
		}},
	}
	headers := http.Header{"X-Live-Fixture": []string{"post-live"}}
	selections := []mediaformat.Selection{
		{
			ID: "137", URL: server.URL + "/video?token=video", Ext: "mp4",
			Protocol: "http_dash_segments", VCodec: "mpeg4", ACodec: "none",
			Headers: headers, YouTubePostLive: true, TargetDuration: 5,
			LiveStartTimestamp: time.Now().Unix(),
		},
		{
			ID: "140", URL: server.URL + "/audio?token=audio", Ext: "m4a",
			Protocol: "http_dash_segments", VCodec: "none", ACodec: "aac",
			Headers: headers, YouTubePostLive: true, TargetDuration: 5,
			LiveStartTimestamp: time.Now().Unix(),
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	destination := filepath.Join(root, "post-live.mp4")
	path, bytes, err := operation.downloadSelections(ctx, selections, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytes <= 0 {
		t.Fatalf("path=%q bytes=%d", path, bytes)
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("streams=%#v", probe.Streams)
	}
	requestMu.Lock()
	defer requestMu.Unlock()
	for _, path := range []string{"/video", "/audio"} {
		sort.Ints(requested[path])
		if got := fmt.Sprint(requested[path]); got != "[0 1 2 3]" {
			t.Fatalf("%s sequences=%s", path, got)
		}
	}
}

func TestYouTubeLiveFromStartDownloadsTracksConcurrentlyAndMerges(t *testing.T) {
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	fixtureRoot := t.TempDir()
	videoPath := filepath.Join(fixtureRoot, "video.mp4")
	audioPath := filepath.Join(fixtureRoot, "audio.m4a")
	generate := func(arguments ...string) {
		output, err := exec.Command(ffmpegPath, arguments...).CombinedOutput()
		if err != nil {
			t.Fatalf("generate fixture: %v: %s", err, output)
		}
	}
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "color=c=green:s=16x16:d=0.3", "-an", "-c:v", "mpeg4", videoPath)
	generate("-nostdin", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.3", "-vn", "-c:a", "aac", audioPath)
	chunk := func(path string) [][]byte {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		result := make([][]byte, 4)
		for index := range result {
			result[index] = body[len(body)*index/4 : len(body)*(index+1)/4]
		}
		return result
	}
	chunks := map[string][][]byte{"/video": chunk(videoPath), "/audio": chunk(audioPath)}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sequenceText := request.URL.Query().Get("sq")
		if sequenceText == "" {
			writer.Header().Set("X-Head-Seqnum", "3")
			return
		}
		sequence, parseErr := strconv.Atoi(sequenceText)
		if parseErr != nil || sequence < 0 || sequence >= 4 {
			http.Error(writer, "bad sequence", http.StatusBadRequest)
			return
		}
		_, _ = writer.Write(chunks[request.URL.Path][sequence])
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var refreshes atomic.Int32
	bothRefreshing := make(chan struct{})
	operation := &operation{
		client: NewClient(), transport: transport,
		request: Request{OutputDir: t.TempDir(), Downloader: DownloaderOptions{
			MaxSegments: 16, MaxSegmentBytes: 8 << 20, Attempts: 1,
			LivePollInterval: time.Millisecond, LiveRefreshInterval: time.Nanosecond,
			LiveMaxPolls: 4, LiveMaxNoProgressPolls: 2,
		}},
	}
	operation.youtubeLiveRefresh = func(selection mediaformat.Selection) youtubelive.LiveRefreshFunc {
		return func(ctx context.Context, request youtubelive.LiveRefreshRequest) (youtubelive.LiveRefreshResult, error) {
			if refreshes.Add(1) == 2 {
				close(bothRefreshing)
			}
			select {
			case <-bothRefreshing:
			case <-ctx.Done():
				return youtubelive.LiveRefreshResult{}, ctx.Err()
			}
			return youtubelive.LiveRefreshResult{
				URL: request.URL, Headers: request.Headers, StillLive: false,
			}, nil
		}
	}
	selections := []mediaformat.Selection{
		{
			ID: "137", URL: server.URL + "/video?pot=video", Ext: "mp4",
			VCodec: "mpeg4", ACodec: "none", YouTubeLiveFromStart: true, TargetDuration: 5,
		},
		{
			ID: "140", URL: server.URL + "/audio?pot=audio", Ext: "m4a",
			VCodec: "none", ACodec: "aac", YouTubeLiveFromStart: true, TargetDuration: 5,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	root := operation.request.OutputDir
	destination := filepath.Join(root, "live-from-start.mp4")
	path, bytes, err := operation.downloadSelections(ctx, selections, root, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	if path != destination || bytes <= 0 || refreshes.Load() != 2 {
		t.Fatalf("path=%q bytes=%d refreshes=%d", path, bytes, refreshes.Load())
	}
	tools, err := ffmpeg.Discover(ffmpeg.Config{})
	if err != nil {
		t.Fatal(err)
	}
	probe, err := tools.Probe(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, stream := range probe.Streams {
		types[stream.CodecType] = true
	}
	if !types["video"] || !types["audio"] {
		t.Fatalf("streams=%#v", probe.Streams)
	}
}

func TestYouTubePostLiveRejectsExternalDownloaderAndCategorizesFailures(t *testing.T) {
	op := &operation{request: Request{Downloader: DownloaderOptions{External: &ExternalDownloader{
		Executable: "unused",
	}}}}
	_, _, err := op.downloadSelection(context.Background(), mediaformat.Selection{
		URL: "https://media.example/video", YouTubePostLive: true, TargetDuration: 5,
	}, t.TempDir(), filepath.Join(t.TempDir(), "out"), nil)
	if !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("external error=%v", err)
	}
	_, _, err = op.downloadSelection(context.Background(), mediaformat.Selection{
		URL: "https://media.example/video", YouTubeLiveFromStart: true, TargetDuration: 5,
	}, t.TempDir(), filepath.Join(t.TempDir(), "out"), nil)
	if !errors.Is(err, extractor.ErrUnsupported) {
		t.Fatalf("live external error=%v", err)
	}
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{youtubelive.ErrInvalidConfig, ErrorInvalidInput},
		{youtubelive.ErrOutputExists, ErrorInvalidInput},
		{youtubelive.ErrHeadSequence, ErrorInternal},
		{youtubelive.ErrDownloadFailed, ErrorInternal},
		{youtubelive.ErrEventSink, ErrorInternal},
		{youtubelive.ErrLiveInvalidConfig, ErrorInvalidInput},
		{youtubelive.ErrLiveHeadSequence, ErrorInternal},
		{youtubelive.ErrLiveNoProgress, ErrorInternal},
		{youtubelive.ErrLivePollLimit, ErrorInternal},
		{youtubelive.ErrLiveProbeFailed, ErrorNetwork},
		{youtubelive.ErrLiveRefreshFailed, ErrorNetwork},
		{youtubelive.ErrProbeFailed, ErrorNetwork},
	} {
		if got := categorized("post-live", test.err); !IsCategory(got, test.category) {
			t.Fatalf("categorized(%v)=%v", test.err, got)
		}
	}

	var networkCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		networkCalls.Add(1)
	}))
	defer server.Close()
	transport, transportErr := network.New(network.Config{})
	if transportErr != nil {
		t.Fatal(transportErr)
	}
	observerFailure := errors.New("observer failed")
	sinkOp := &operation{
		client: NewClient(WithEventHandler(func(_ context.Context, event Event) error {
			if event.Kind == EventDownloadStarting {
				return observerFailure
			}
			return nil
		})),
		transport: transport,
		request:   Request{},
	}
	root := t.TempDir()
	_, _, err = sinkOp.downloadSelection(context.Background(), mediaformat.Selection{
		URL: server.URL + "/video?pot=secret", YouTubePostLive: true, TargetDuration: 5,
	}, root, filepath.Join(root, "out"), sinkOp.eventSink())
	if !errors.Is(err, youtubelive.ErrEventSink) || !errors.Is(err, observerFailure) {
		t.Fatalf("operation sink error = %v", err)
	}
	if got := categorized("post-live", err); !IsCategory(got, ErrorInternal) {
		t.Fatalf("categorized operation sink error = %v", got)
	}
	if networkCalls.Load() != 0 {
		t.Fatalf("network calls = %d", networkCalls.Load())
	}
}

func TestClientConcurrentOperationsDoNotShareState(t *testing.T) {
	server := testserver.New()
	defer server.Close()
	client := NewClient()
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, err := client.Run(context.Background(), Request{
				URL: server.URL + "/page", OutputDir: filepath.Join(t.TempDir(), "operation"),
			})
			errorsSeen <- err
		}(index)
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatal(err)
		}
	}
}

type playlistFixtureExtractor struct{}

func (playlistFixtureExtractor) Name() string { return "playlist-fixture" }

func (playlistFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed.Path == "/list" || parsed.Path == "/nested" || parsed.Path == "/cycle"
}

func (playlistFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	base := parsed.Scheme + "://" + parsed.Host
	var id, title string
	var entries []extractor.Entry
	switch parsed.Path {
	case "/list":
		id, title = "root", "Root Playlist"
		entries = []extractor.Entry{
			{URL: base + "/one.mp4", ExtractorKey: "generic", ID: "one"},
			{URL: base + "/nested", ExtractorKey: "playlist-fixture", ID: "nested"},
		}
	case "/nested":
		id, title = "nested", "Nested Playlist"
		entries = []extractor.Entry{{URL: base + "/two.mp4", ExtractorKey: "generic", ID: "two"}}
	case "/cycle":
		id, title = "cycle", "Cycle"
		entries = []extractor.Entry{{URL: request.URL, ExtractorKey: "playlist-fixture"}}
	default:
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String(title)},
		value.Field{Key: "webpage_url", Value: value.String(request.URL)},
	))
	return extractor.Playlist(info, extractor.StaticEntries(entries...))
}

func TestOperationResolvesNestedPlaylistInOrder(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "playlist-fixture" || len(result.Entries) != 2 || len(result.Entries[1].Entries) != 1 {
		t.Fatalf("result = %#v", result)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	entries, ok := metadata["entries"].([]any)
	if !ok || len(entries) != 2 {
		t.Fatalf("metadata entries = %#v", metadata["entries"])
	}
	first := entries[0].(map[string]any)
	second := entries[1].(map[string]any)
	if first["id"] != "one" || first["playlist_index"] != float64(1) || second["_type"] != "playlist" {
		t.Fatalf("entries = %#v", entries)
	}
	nested := second["entries"].([]any)[0].(map[string]any)
	if nested["id"] != "two" || nested["playlist_id"] != "nested" || nested["playlist_index"] != float64(1) {
		t.Fatalf("nested entry = %#v", nested)
	}
}

func TestOperationDownloadsPlaylistAndAggregatesBytes(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	root := t.TempDir()
	operation := &operation{
		client: NewClient(), request: Request{OutputDir: root}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Downloaded || result.Bytes != int64(len("one")+len("second")) {
		t.Fatalf("aggregate result = %#v", result)
	}
	for name, want := range map[string]string{"one.mp4": "one", "two.mp4": "second"} {
		got, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(got) != want {
			t.Fatalf("%s = %q, %v", name, got, err)
		}
	}
}

func TestOperationRejectsNestedPlaylistCycle(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(playlistFixtureExtractor{}, extractor.NewGeneric()),
	}
	_, err := operation.process(context.Background(), server.URL+"/cycle", "", nil, make(map[string]bool), 0)
	if !IsCategory(err, ErrorInternal) || !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("cycle error = %v", err)
	}
}

func TestOperationMergesTransparentEntryMetadata(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(extractor.NewGeneric()),
	}
	overlay := &extractor.Entry{
		ID:           "producer-id",
		Title:        "Producer Title",
		Thumbnail:    "https://images.example/thumb.jpg",
		Availability: "subscriber_only",
		Language:     "fr",
		Duration:     12.5,
		HasDuration:  true,
		Timestamp:    1730000000,
		HasTimestamp: true,
		ViewCount:    7,
		HasViewCount: true,
		Transparent:  true,
	}
	result, err := operation.process(context.Background(), server.URL+"/one.mp4", "generic", overlay, make(map[string]bool), 1)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "producer-id" || metadata["title"] != "Producer Title" {
		t.Fatalf("transparent metadata = %#v", metadata)
	}
	if metadata["thumbnail"] != "https://images.example/thumb.jpg" {
		t.Fatalf("thumbnail = %#v", metadata["thumbnail"])
	}
	if metadata["availability"] != "subscriber_only" {
		t.Fatalf("availability = %#v", metadata["availability"])
	}
	if metadata["language"] != "fr" {
		t.Fatalf("language = %#v", metadata["language"])
	}
	if metadata["duration"] != 12.5 {
		t.Fatalf("duration = %#v", metadata["duration"])
	}
	if metadata["timestamp"] != float64(1730000000) {
		t.Fatalf("timestamp = %#v", metadata["timestamp"])
	}
	if metadata["view_count"] != float64(7) {
		t.Fatalf("view_count = %#v", metadata["view_count"])
	}
}

// TestOperationTransparentOverlayPreservesExplicitZeroNumerics asserts that
// HasDuration/HasTimestamp/HasViewCount with value 0 are propagated as the
// numeric zero rather than erased or replaced by backend values.
func TestOperationTransparentOverlayPreservesExplicitZeroNumerics(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(extractor.NewGeneric()),
	}
	overlay := &extractor.Entry{
		ID:           "zero-overlay",
		Title:        "Zero Title",
		Duration:     0,
		HasDuration:  true,
		Timestamp:    0,
		HasTimestamp: true,
		ViewCount:    0,
		HasViewCount: true,
		Transparent:  true,
	}
	result, err := operation.process(context.Background(), server.URL+"/one.mp4", "generic", overlay, make(map[string]bool), 1)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	dur, ok := metadata["duration"].(float64)
	if !ok {
		t.Fatalf("duration missing or wrong type: %#v", metadata["duration"])
	}
	if dur != 0 {
		t.Fatalf("duration=%v want 0", dur)
	}
	ts, ok := metadata["timestamp"].(float64)
	if !ok {
		t.Fatalf("timestamp missing or wrong type: %#v", metadata["timestamp"])
	}
	if ts != 0 {
		t.Fatalf("timestamp=%v want 0", ts)
	}
	views, ok := metadata["view_count"].(float64)
	if !ok {
		t.Fatalf("view_count missing or wrong type: %#v", metadata["view_count"])
	}
	if views != 0 {
		t.Fatalf("view_count=%v want 0", views)
	}
}

// TestOperationTransparentZeroNumericsSurviveURLResultHandoff asserts
// explicit-zero numeric overlays survive the URL-result handoff step
// (overlayOntoEntry -> recursive processWithTransparentParent).
func TestOperationTransparentZeroNumericsSurviveURLResultHandoff(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(zeroNumericsHandoffParent{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/zero-numerics-handoff", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"duration", "timestamp", "view_count"} {
		value, ok := metadata[key].(float64)
		if !ok {
			t.Fatalf("%s missing or wrong type: %#v", key, metadata[key])
		}
		if value != 0 {
			t.Fatalf("%s=%v want 0", key, value)
		}
	}
	if metadata["id"] != "zero-parent-id" {
		t.Fatalf("id=%#v", metadata["id"])
	}
}

type zeroNumericsHandoffParent struct{}

func (zeroNumericsHandoffParent) Name() string { return "zero-numerics-handoff-parent" }
func (zeroNumericsHandoffParent) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/zero-numerics-handoff"
}
func (ext zeroNumericsHandoffParent) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parentURL, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	middle := *parentURL
	middle.Path = "/one.mp4"
	entry := extractor.Entry{
		URL:          middle.String(),
		ExtractorKey: "generic",
		ID:           "zero-parent-id",
		Title:        "Zero Title",
		Duration:     0,
		HasDuration:  true,
		Timestamp:    0,
		HasTimestamp: true,
		ViewCount:    0,
		HasViewCount: true,
		Transparent:  true,
	}
	return extractor.URLResult(entry)
}

// TestOperationTransparentOverlayDoesNotEraseBackendMetadata confirms that an
// overlay supplying only an ID and title never erases backend fields such as
// duration or description.
func TestOperationTransparentOverlayDoesNotEraseBackendMetadata(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(extractor.NewGeneric()),
	}
	overlay := &extractor.Entry{ID: "Producer", Title: "Producer Title", Transparent: true}
	result, err := operation.process(context.Background(), server.URL+"/one.mp4", "generic", overlay, make(map[string]bool), 1)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "Producer" || metadata["title"] != "Producer Title" {
		t.Fatalf("producer fields not applied: %#v", metadata)
	}
}

// TestOperationTransparentEntryAcrossTwoHopURLResults verifies that provider
// metadata is preserved across two consecutive URL-result handoffs without
// erasing fields supplied by the intermediate parent.
func TestOperationTransparentEntryAcrossTwoHopURLResults(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(
			twoHopParentExtractor{},
			twoHopMiddleExtractor{},
			extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), server.URL+"/two-hop", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "parent-id" {
		t.Fatalf("two-hop id = %#v", metadata["id"])
	}
	if metadata["title"] != "Parent Title" {
		t.Fatalf("two-hop title = %#v", metadata["title"])
	}
	if metadata["description"] != "Middle description" {
		t.Fatalf("two-hop description erased = %#v", metadata["description"])
	}
}

type twoHopMiddleExtractor struct{}

func (twoHopMiddleExtractor) Name() string { return "two-hop-middle" }
func (twoHopMiddleExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/middle"
}
func (twoHopMiddleExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	if parsed.Path != "/middle" {
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	parsed.Path = "/one.mp4"
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("middle-id")},
		value.Field{Key: "description", Value: value.String("Middle description")},
	))
	result, err := extractor.URLResult(extractor.Entry{
		URL: parsed.String(), ExtractorKey: "generic", ID: "middle-id", Title: "Middle Title", Transparent: true,
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	result.Info = info
	return result, nil
}

type twoHopParentExtractor struct{}

func (twoHopParentExtractor) Name() string { return "two-hop-parent" }
func (twoHopParentExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/two-hop"
}
func (ext twoHopParentExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parentURL, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	// Preserve the test server's host but route through the middle extractor.
	middle := *parentURL
	middle.Path = "/middle"
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("parent-id")},
		value.Field{Key: "title", Value: value.String("Parent Title")},
	))
	result, err := extractor.URLResult(extractor.Entry{
		URL: middle.String(), ExtractorKey: "two-hop-middle", ID: "parent-id", Title: "Parent Title", Transparent: true,
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	result.Info = info
	return result, nil
}

func TestOperationMergesTransparentParentInfoFromURLResult(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(transparentParentFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/parent-handoff", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "parent-id" || metadata["title"] != "Parent Title" {
		t.Fatalf("overlay metadata = %#v", metadata)
	}
	if metadata["description"] != "Parent description" {
		t.Fatalf("parent description = %#v", metadata["description"])
	}
	subtitles, ok := metadata["subtitles"].(map[string]any)
	if !ok || subtitles["en"] == nil {
		t.Fatalf("parent subtitles = %#v", metadata["subtitles"])
	}
}

type transparentParentFixtureExtractor struct{}

func (transparentParentFixtureExtractor) Name() string { return "transparent-parent-fixture" }
func (transparentParentFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/parent-handoff"
}
func (transparentParentFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	parsed.Path = "/one.mp4"
	parent := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("parent-id")},
		value.Field{Key: "title", Value: value.String("Parent Title")},
		value.Field{Key: "description", Value: value.String("Parent description")},
		value.Field{Key: "subtitles", Value: value.ObjectValue(value.NewObject(
			value.Field{Key: "en", Value: value.List(value.ObjectValue(value.NewObject(
				value.Field{Key: "url", Value: value.String("https://amara.org/api/videos/parent/subtitles/en/?format=vtt")},
				value.Field{Key: "ext", Value: value.String("vtt")},
			)))},
		))},
	))
	result, err := extractor.URLResult(extractor.Entry{
		URL: parsed.String(), ExtractorKey: "generic", ID: "parent-id", Title: "Parent Title", Transparent: true,
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	result.Info = parent
	return result, nil
}

func TestProductCategorizesAmaraFailures(t *testing.T) {
	for _, test := range []struct {
		err      error
		category ErrorCategory
	}{
		{extractor.ErrAmaraRateLimited, ErrorNetwork},
		{extractor.ErrAmaraNetwork, ErrorNetwork},
		{extractor.ErrInvalidMetadata, ErrorInternal},
	} {
		got := categorized("amara extraction", test.err)
		if !IsCategory(got, test.category) {
			t.Fatalf("category=%v err=%v", got, test.err)
		}
	}
}

type amaraProductFixtureTransport struct {
	t        *testing.T
	fixtures map[string][]byte
}

func newAmaraProductFixtureTransport(t *testing.T) *amaraProductFixtureTransport {
	t.Helper()
	transport := &amaraProductFixtureTransport{t: t, fixtures: make(map[string][]byte)}
	for _, name := range []string{"youtube.json", "vimeo.json"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "internal", "extractor", "testdata", "amara", name))
		if err != nil {
			t.Fatal(err)
		}
		transport.fixtures[name] = data
	}
	return transport
}

func (transport *amaraProductFixtureTransport) response(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() != "amara.org" || !strings.HasPrefix(request.URL.Path, "/api/videos/") {
		transport.t.Fatalf("unexpected request: %s", request.URL)
	}
	parts := strings.Split(strings.Trim(request.URL.Path, "/"), "/")
	if len(parts) < 3 {
		transport.t.Fatalf("unexpected API path: %s", request.URL.Path)
	}
	var fixture string
	switch parts[2] {
	case "jVx79ZKGK1ky":
		fixture = "youtube.json"
	case "kYkK1VUTWW5I":
		fixture = "vimeo.json"
	default:
		transport.t.Fatalf("unexpected video id: %s", parts[2])
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(transport.fixtures[fixture])),
		Request:    request,
	}, nil
}

type amaraProductRoundTripper struct {
	amara *amaraProductFixtureTransport
}

func (tripper amaraProductRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Hostname() == "amara.org" {
		return tripper.amara.response(request)
	}
	return http.DefaultTransport.RoundTrip(request)
}

func newAmaraProductNetworkClient(t *testing.T) *network.Client {
	t.Helper()
	client, err := network.New(network.Config{RoundTripper: amaraProductRoundTripper{amara: newAmaraProductFixtureTransport(t)}})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type amaraProductYouTubeChild struct{}

func (amaraProductYouTubeChild) Name() string { return "youtube" }
func (amaraProductYouTubeChild) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.Contains(parsed.Hostname(), "youtube.com")
}
func (amaraProductYouTubeChild) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	id := parsed.Query().Get("v")
	if id == "" {
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String("YouTube child title")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(request.URL)},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)))},
	))), nil
}

type amaraProductVimeoChild struct{}

func (amaraProductVimeoChild) Name() string { return "vimeo" }
func (amaraProductVimeoChild) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.Contains(parsed.Hostname(), "vimeo.com")
}
func (amaraProductVimeoChild) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	id := strings.Trim(parsed.Path, "/")
	if id == "" {
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	return extractor.Media(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String(id)},
		value.Field{Key: "title", Value: value.String("Vimeo child title")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "formats", Value: value.List(value.ObjectValue(value.NewObject(
			value.Field{Key: "url", Value: value.String(request.URL)},
			value.Field{Key: "ext", Value: value.String("mp4")},
		)))},
	))), nil
}

func amaraProductOperation(t *testing.T, transport *network.Client, extra ...extractor.Extractor) *operation {
	t.Helper()
	extractors := []extractor.Extractor{extractor.NewAmara(), amaraProductYouTubeChild{}, amaraProductVimeoChild{}}
	extractors = append(extractors, extra...)
	extractors = append(extractors, extractor.NewGeneric())
	return &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(extractors...),
	}
}

func TestOperationReentersAmaraYouTubeHandoff(t *testing.T) {
	t.Parallel()
	transport := newAmaraProductNetworkClient(t)
	operation := amaraProductOperation(t, transport)
	result, err := operation.process(context.Background(), "https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "h6ZuVdvYnfE" {
		t.Fatalf("child id = %#v", metadata["id"])
	}
	if metadata["title"] != "Why jury trials are becoming less common" {
		t.Fatalf("title = %#v", metadata["title"])
	}
	if metadata["description"] == "" || metadata["thumbnail"] == "" || metadata["webpage_url"] != "https://amara.org/en/videos/jVx79ZKGK1ky" {
		t.Fatalf("metadata = %#v", metadata)
	}
	if metadata["duration"] != float64(312) || metadata["timestamp"] != float64(1471046400) {
		t.Fatalf("timing metadata = %#v", metadata)
	}
	subtitles, ok := metadata["subtitles"].(map[string]any)
	if !ok || subtitles["en"] == nil {
		t.Fatalf("subtitles = %#v", metadata["subtitles"])
	}
}

func TestOperationReentersAmaraVimeoHandoff(t *testing.T) {
	t.Parallel()
	transport := newAmaraProductNetworkClient(t)
	operation := amaraProductOperation(t, transport)
	result, err := operation.process(context.Background(), "https://amara.org/en/videos/kYkK1VUTWW5I/info/vimeo-at-ces-2011", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "18622084" {
		t.Fatalf("child id = %#v", metadata["id"])
	}
	if metadata["title"] != "Vimeo at CES 2011!" {
		t.Fatalf("title = %#v", metadata["title"])
	}
}

func TestOperationAmaraHandoffDoesNotLeakMetadataBetweenCalls(t *testing.T) {
	t.Parallel()
	transport := newAmaraProductNetworkClient(t)
	operation := amaraProductOperation(t, transport)
	first, err := operation.process(context.Background(), "https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := operation.process(context.Background(), "https://amara.org/en/videos/kYkK1VUTWW5I/info/vimeo-at-ces-2011", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var firstMeta, secondMeta map[string]any
	if err := json.Unmarshal(first.InfoJSON, &firstMeta); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second.InfoJSON, &secondMeta); err != nil {
		t.Fatal(err)
	}
	if firstMeta["description"] == secondMeta["description"] || firstMeta["id"] == secondMeta["id"] {
		t.Fatalf("metadata leaked across calls: %#v %#v", firstMeta, secondMeta)
	}
}

func TestOperationAmaraHandoffsAreConcurrentSafe(t *testing.T) {
	t.Parallel()
	transport := newAmaraProductNetworkClient(t)
	operation := amaraProductOperation(t, transport)
	urls := []string{
		"https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/",
		"https://amara.org/en/videos/kYkK1VUTWW5I/info/vimeo-at-ces-2011",
	}
	type outcome struct {
		url    string
		result Result
		err    error
	}
	outcomes := make(chan outcome, len(urls))
	var wait sync.WaitGroup
	for _, rawURL := range urls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := operation.process(context.Background(), rawURL, "", nil, make(map[string]bool), 0)
			outcomes <- outcome{url: rawURL, result: result, err: err}
		}()
	}
	wait.Wait()
	close(outcomes)
	for outcome := range outcomes {
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(outcome.result.InfoJSON, &metadata); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(outcome.url, "jVx79ZKGK1ky") {
			if metadata["id"] != "h6ZuVdvYnfE" || metadata["title"] != "Why jury trials are becoming less common" {
				t.Fatalf("youtube metadata = %#v", metadata)
			}
		} else if metadata["id"] != "18622084" || metadata["title"] != "Vimeo at CES 2011!" {
			t.Fatalf("vimeo metadata = %#v", metadata)
		}
	}
}

type amaraPlaylistYouTubeChild struct {
	mediaURL string
}

func (child amaraPlaylistYouTubeChild) Name() string { return "youtube" }
func (child amaraPlaylistYouTubeChild) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.Contains(parsed.Hostname(), "youtube.com")
}
func (child amaraPlaylistYouTubeChild) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	if parsed.Query().Get("v") == "" {
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	return extractor.Playlist(value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("child-playlist")},
		value.Field{Key: "title", Value: value.String("Child playlist")},
	)), extractor.StaticEntries(extractor.Entry{
		URL: child.mediaURL, ExtractorKey: "generic", Transparent: true, Title: "Playlist entry title",
	}))
}

func TestOperationAmaraParentMetadataDoesNotLeakIntoPlaylistEntries(t *testing.T) {
	t.Parallel()
	server := playlistMediaServer(t)
	defer server.Close()
	transport := newAmaraProductNetworkClient(t)
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(
			extractor.NewAmara(),
			amaraPlaylistYouTubeChild{mediaURL: server.URL + "/one.mp4"},
			extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), "https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries=%d", len(result.Entries))
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.Entries[0].InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["description"] != nil || metadata["subtitles"] != nil || metadata["webpage_url"] == "https://amara.org/en/videos/jVx79ZKGK1ky" {
		t.Fatalf("Amara parent metadata leaked into playlist entry: %#v", metadata)
	}
	if metadata["title"] != "Playlist entry title" {
		t.Fatalf("playlist entry title = %#v", metadata["title"])
	}
}

type amaraNestedYouTubeHandoffChild struct {
	mediaURL string
}

func (child amaraNestedYouTubeHandoffChild) Name() string { return "youtube" }
func (child amaraNestedYouTubeHandoffChild) Suitable(parsed *url.URL) bool {
	return parsed != nil && strings.Contains(parsed.Hostname(), "youtube.com")
}
func (child amaraNestedYouTubeHandoffChild) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	if parsed.Query().Get("v") == "" {
		return extractor.Extraction{}, extractor.ErrUnsupported
	}
	parent := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("nested-parent-id")},
		value.Field{Key: "description", Value: value.String("nested parent description")},
	))
	result, err := extractor.URLResult(extractor.Entry{
		URL: child.mediaURL, ExtractorKey: "generic", Transparent: true, Title: "Nested overlay title",
	})
	if err != nil {
		return extractor.Extraction{}, err
	}
	result.Info = parent
	return result, nil
}

func TestOperationAmaraNestedTransparentPreservesChildID(t *testing.T) {
	t.Parallel()
	server := playlistMediaServer(t)
	defer server.Close()
	transport := newAmaraProductNetworkClient(t)
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(
			extractor.NewAmara(),
			amaraNestedYouTubeHandoffChild{mediaURL: server.URL + "/one.mp4"},
			extractor.NewGeneric(),
		),
	}
	result, err := operation.process(context.Background(), "https://amara.org/en/videos/jVx79ZKGK1ky/info/why-jury-trials/", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(result.InfoJSON, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["id"] != "one" {
		t.Fatalf("nested child id = %#v", metadata["id"])
	}
	if metadata["title"] != "Why jury trials are becoming less common" {
		t.Fatalf("amara title lost: %#v", metadata["title"])
	}
	if metadata["description"] != "A PBS NewsHour segment on declining jury trials." {
		t.Fatalf("amara description lost: %#v", metadata["description"])
	}
}

type URLResultFixtureExtractor struct{}

func (URLResultFixtureExtractor) Name() string { return "url-result-fixture" }
func (URLResultFixtureExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Path == "/handoff"
}
func (URLResultFixtureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, err := url.Parse(request.URL)
	if err != nil {
		return extractor.Extraction{}, err
	}
	parsed.Path = "/one.mp4"
	return extractor.URLResult(extractor.Entry{URL: parsed.String(), ExtractorKey: "generic", Transparent: true})
}

func TestOperationURLResultBypassesPlaylistControls(t *testing.T) {
	server := playlistMediaServer(t)
	defer server.Close()
	transport, _ := network.New(network.Config{})
	operation := &operation{
		client: NewClient(),
		request: Request{
			SkipDownload: true,
			Playlist:     PlaylistOptions{Flat: true, Items: "2", Start: 4, End: 4, Reverse: true},
		},
		transport: transport,
		registry:  extractor.NewRegistry(URLResultFixtureExtractor{}, extractor.NewGeneric()),
	}
	result, err := operation.process(context.Background(), server.URL+"/handoff", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if result.Extractor != "generic" || len(result.Entries) != 0 || result.Skipped {
		t.Fatalf("result = %#v", result)
	}
}

type cyclicURLResultExtractor struct{}

func (cyclicURLResultExtractor) Name() string { return "url-cycle" }
func (cyclicURLResultExtractor) Suitable(parsed *url.URL) bool {
	return parsed != nil && parsed.Host == "cycle.invalid"
}
func (cyclicURLResultExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	return extractor.URLResult(extractor.Entry{URL: request.URL, ExtractorKey: "url-cycle"})
}

func TestOperationRejectsURLResultCycle(t *testing.T) {
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true},
		registry: extractor.NewRegistry(cyclicURLResultExtractor{}),
	}
	_, err := operation.process(context.Background(), "https://cycle.invalid/video", "", nil, make(map[string]bool), 0)
	if !errors.Is(err, extractor.ErrPlaylistLimit) {
		t.Fatalf("cycle error = %v", err)
	}
}

func playlistMediaServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body string
		switch request.URL.Path {
		case "/one.mp4":
			body = "one"
		case "/two.mp4":
			body = "second"
		default:
			http.NotFound(writer, request)
			return
		}
		writer.Header().Set("Content-Type", "video/mp4")
		writer.Header().Set("Content-Length", fmt.Sprint(len(body)))
		if request.Method != http.MethodHead {
			_, _ = writer.Write([]byte(body))
		}
	}))
}

func FuzzConfinedPostprocessPath(f *testing.F) {
	f.Add("media.mp4")
	f.Add("nested/output.mkv")
	f.Add("../escape")
	f.Fuzz(func(t *testing.T, requested string) {
		root := t.TempDir()
		path, err := confinedPostprocessPath(root, requested)
		if err != nil {
			return
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatalf("accepted escaping path %q as %q", requested, path)
		}
	})
}

// TestClientConcurrentRunAndClose is a basic concurrency smoke test verifying
// that concurrent Run and Close calls do not panic. It uses the generic
// fixture extractor (no JavaScript helper). The helper-backed active-solve
// drain test is TestSupervisorConcurrentExecuteAndCloseDrainsActiveSolves in
// the supervisor package, which exercises real JavaScript execution, asserts
// operation results, and verifies helper process cleanup.
func TestClientConcurrentRunAndClose(t *testing.T) {
	server := testserver.New()
	defer server.Close()

	for iteration := 0; iteration < 5; iteration++ {
		client := NewClient()
		var wg sync.WaitGroup
		// Launch concurrent Run calls.
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, _ = client.Run(context.Background(), Request{URL: server.URL + "/page", SkipDownload: true})
			}()
		}
		// Concurrently close while runs may be in flight.
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Millisecond)
			client.Close()
		}()
		wg.Wait()
		// After Close, subsequent Run calls should still work (lazy re-creation)
		// or fail gracefully—no panics.
		_, _ = client.Run(context.Background(), Request{URL: server.URL + "/page", SkipDownload: true})
		client.Close()
	}
}

type refererCaptureExtractor struct {
	referers []string
}

func (capture *refererCaptureExtractor) Name() string { return "referer-capture" }

func (capture *refererCaptureExtractor) Suitable(parsed *url.URL) bool {
	return parsed.Path == "/child"
}

func (capture *refererCaptureExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	capture.referers = append(capture.referers, request.Referer)
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("child")},
		value.Field{Key: "title", Value: value.String("Child")},
		value.Field{Key: "ext", Value: value.String("mp4")},
		value.Field{Key: "url", Value: value.String(request.URL)},
	))
	return extractor.Media(info), nil
}

type refererPlaylistExtractor struct{}

func (refererPlaylistExtractor) Name() string { return "referer-playlist" }

func (refererPlaylistExtractor) Suitable(parsed *url.URL) bool {
	return parsed.Path == "/referer-list"
}

func (refererPlaylistExtractor) Extract(_ context.Context, request extractor.Request) (extractor.Extraction, error) {
	parsed, _ := url.Parse(request.URL)
	base := parsed.Scheme + "://" + parsed.Host
	info := value.NewInfo(value.NewObject(
		value.Field{Key: "id", Value: value.String("referer-list")},
		value.Field{Key: "title", Value: value.String("Referer Playlist")},
	))
	return extractor.Playlist(info, extractor.StaticEntries(
		extractor.Entry{URL: base + "/child", ExtractorKey: "referer-capture", Referer: "https://publisher.example/embed"},
		extractor.Entry{URL: base + "/child", ExtractorKey: "referer-capture"},
	))
}

func TestOperationPropagatesRefererThroughPlaylistRecursion(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	}))
	defer server.Close()
	transport, err := network.New(network.Config{})
	if err != nil {
		t.Fatal(err)
	}
	child := &refererCaptureExtractor{}
	operation := &operation{
		client: NewClient(), request: Request{SkipDownload: true}, transport: transport,
		registry: extractor.NewRegistry(refererPlaylistExtractor{}, child),
	}
	result, err := operation.process(context.Background(), server.URL+"/referer-list", "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 2 || len(child.referers) != 2 {
		t.Fatalf("entries=%d referers=%#v", len(result.Entries), child.referers)
	}
	if child.referers[0] != "https://publisher.example/embed" || child.referers[1] != "" {
		t.Fatalf("referers = %#v", child.referers)
	}
}

func TestProductRegistrySelectsDiscoveryDPlayConcreteKeys(t *testing.T) {
	registry := productRegistry()
	tests := map[string]string{
		"https://ahctv.com/video/a/b": "amhistorychannel", "https://animalplanet.com/video/a/b": "animalplanet", "https://watch.cookingchanneltv.com/video/a/b": "cookingchannel", "https://dplay.no/videoer/a/b": "dplay", "https://destinationamerica.com/video/a/b": "destinationamerica", "https://discoverylife.com/video/a/b": "discoverylife", "https://dmax.de/sendungen/a/b": "discoverynetworksde", "https://discoveryplus.com/gb/video/a/b": "discoveryplus", "https://discoveryplus.in/videos/a/b": "discoveryplusindia", "https://discoveryplus.in/show/a": "discoveryplusindiashow", "https://discoveryplus.com/it/video/a/b": "discoveryplusitaly", "https://discoveryplus.it/programmi/a": "discoveryplusitalyshow", "https://foodnetwork.com/video/a/b": "foodnetwork", "https://go.discovery.com/video/a/b": "godiscovery", "https://de.hgtv.com/sendungen/a/b": "hgtvde", "https://hgtv.com/video/a/b": "hgtvusa", "https://investigationdiscovery.com/video/a/b": "investigationdiscovery", "https://sciencechannel.com/video/a/b": "sciencechannel", "https://go.tlc.com/video/a/b": "tlc", "https://travelchannel.com/video/a/b": "travelchannel", "https://tele5.de/mediathek/a/b": "tele5",
	}
	for rawURL, want := range tests {
		selected, err := registry.Select(rawURL)
		if err != nil || selected.Name() != want {
			t.Fatalf("Select(%q) = %v, %v; want %q", rawURL, selected, err, want)
		}
	}
}

func TestProductDiscoveryHLSAndDASHDownloadDispatch(t *testing.T) {
	for _, test := range []struct {
		name, kind, manifestPath, manifest, want string
	}{
		{"HLS", "hls", "/master.m3u8", "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmedia.m3u8\n", "hls-one-hls-two"},
		{"DASH", "dash", "/manifest.mpd", `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video" mimeType="video/mp4"><Representation id="fixture" bandwidth="1000"><SegmentTemplate duration="1" initialization="init.bin" media="$Number$.bin"/></Representation></AdaptationSet></Period></MPD>`, "dash-init-dash-one-dash-two"},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				body := ""
				if request.URL.Host == "cdn.example.invalid" {
					for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Referer"} {
						if got := request.Header.Get(key); got != "" {
							return nil, fmt.Errorf("%s leaked to Discovery CDN: %q", key, got)
						}
					}
				}
				switch {
				case strings.HasSuffix(request.URL.Path, "/token"):
					body = `{"data":{"attributes":{"token":"fixture-token"}}}`
				case strings.Contains(request.URL.Path, "/content/videos/"):
					body = `{"data":{"id":"video-1","attributes":{"name":"Discovery Dispatch","videoDuration":2000}}}`
				case strings.Contains(request.URL.Path, "videoPlaybackInfo"):
					body = fmt.Sprintf(`{"data":{"attributes":{"streaming":[{"type":%q,"url":%q}]}}}`, test.kind, "https://cdn.example.invalid"+test.manifestPath)
				case request.URL.Path == test.manifestPath:
					body = test.manifest
				case request.URL.Path == "/media.m3u8":
					body = "#EXTM3U\n#EXTINF:1,\none.bin\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n"
				case request.URL.Path == "/one.bin":
					body = "hls-one-"
				case request.URL.Path == "/two.bin":
					body = "hls-two"
				case request.URL.Path == "/init.bin":
					body = "dash-init-"
				case request.URL.Path == "/1.bin":
					body = "dash-one-"
				case request.URL.Path == "/2.bin":
					body = "dash-two"
				default:
					return nil, fmt.Errorf("unexpected Discovery dispatch request %s", request.URL.Redacted())
				}
				header := make(http.Header)
				header.Set("Content-Length", strconv.Itoa(len(body)))
				return &http.Response{StatusCode: 200, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			transport, err := network.New(network.Config{RoundTripper: roundTrip})
			if err != nil {
				t.Fatal(err)
			}
			request := Request{OutputDir: t.TempDir()}
			operation := &operation{client: NewClient(), request: request, transport: transport, registry: productRegistry(), rootExtractor: new(string)}
			result, err := operation.process(context.Background(), "https://go.discovery.com/video/show/episode", "", nil, make(map[string]bool), 0)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := os.ReadFile(result.Filename)
			if err != nil || string(payload) != test.want {
				t.Fatalf("payload=%q want=%q err=%v", payload, test.want, err)
			}
		})
	}
}

func TestProductDiscoveryDownloadRefererScope(t *testing.T) {
	for _, site := range []struct {
		name, rawURL, referer string
		legacy                bool
	}{
		{"DPlay", "https://www.dplay.no/videoer/show/episode", "dplay.no", true},
		{"DiscoveryPlusIndia", "https://www.discoveryplus.in/videos/show/episode", "https://www.discoveryplus.in/", false},
	} {
		for _, media := range []struct {
			name, kind, mediaURL, want string
		}{
			{"HLS", "hls", "https://cdn.example.invalid/master.m3u8", "one-two"},
			{"Direct", "http", "https://cdn.example.invalid/video.mp4", "direct-media"},
		} {
			t.Run(site.name+"/"+media.name, func(t *testing.T) {
				seen := make(map[string][]http.Header)
				var seenMu sync.Mutex
				roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
					body := ""
					if request.URL.Host == "cdn.example.invalid" {
						seenMu.Lock()
						seen[request.URL.Path] = append(seen[request.URL.Path], request.Header.Clone())
						seenMu.Unlock()
						for _, key := range []string{"Authentication", "Authorization", "Cookie", "Proxy-Authorization"} {
							if got := request.Header.Get(key); got != "" {
								return nil, fmt.Errorf("%s leaked to Discovery CDN: %q", key, got)
							}
						}
						switch request.URL.Path {
						case "/master.m3u8":
							body = "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000\nmedia.m3u8\n"
						case "/media.m3u8":
							body = "#EXTM3U\n#EXTINF:1,\none.bin\n#EXTINF:1,\ntwo.bin\n#EXT-X-ENDLIST\n"
						case "/one.bin":
							body = "one-"
						case "/two.bin":
							body = "two"
						case "/video.mp4":
							body = "direct-media"
						default:
							return nil, fmt.Errorf("unexpected Discovery CDN request %s", request.URL.Redacted())
						}
					} else {
						switch {
						case strings.HasSuffix(request.URL.Path, "/token"):
							body = `{"data":{"attributes":{"token":"api-bearer"}}}`
						case strings.Contains(request.URL.Path, "/content/videos/"):
							if request.Header.Get("Authorization") != "Bearer api-bearer" {
								return nil, fmt.Errorf("content request bearer=%q", request.Header.Get("Authorization"))
							}
							body = `{"data":{"id":"video-1","attributes":{"name":"Referer fixture","videoDuration":2000}}}`
						case strings.Contains(request.URL.Path, "videoPlaybackInfo"):
							if request.Header.Get("Authorization") != "Bearer api-bearer" {
								return nil, fmt.Errorf("playback request bearer=%q", request.Header.Get("Authorization"))
							}
							if site.legacy {
								body = fmt.Sprintf(`{"data":{"attributes":{"streaming":{%q:{"url":%q}}}}}`, media.kind, media.mediaURL)
							} else {
								body = fmt.Sprintf(`{"data":{"attributes":{"streaming":[{"type":%q,"url":%q}]}}}`, media.kind, media.mediaURL)
							}
						default:
							return nil, fmt.Errorf("unexpected Discovery API request %s", request.URL.Redacted())
						}
					}
					headers := make(http.Header)
					headers.Set("Content-Length", strconv.Itoa(len(body)))
					return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
				})
				transport, err := network.New(network.Config{RoundTripper: roundTrip})
				if err != nil {
					t.Fatal(err)
				}
				operation := &operation{
					client: NewClient(), request: Request{OutputDir: t.TempDir()}, transport: transport,
					registry: productRegistry(), rootExtractor: new(string),
				}
				result, err := operation.process(context.Background(), site.rawURL, "", nil, make(map[string]bool), 0)
				if err != nil {
					t.Fatal(err)
				}
				payload, err := os.ReadFile(result.Filename)
				if err != nil || string(payload) != media.want {
					t.Fatalf("payload=%q want=%q err=%v", payload, media.want, err)
				}
				requiredPaths := []string{"/video.mp4"}
				if media.name == "HLS" {
					requiredPaths = []string{"/media.m3u8", "/one.bin", "/two.bin"}
				}
				for _, path := range requiredPaths {
					headers := seen[path]
					if len(headers) == 0 {
						t.Fatalf("%s was not requested; seen=%v", path, seen)
					}
					for _, header := range headers {
						if got := header.Get("Referer"); got != site.referer {
							t.Fatalf("%s Referer=%q want=%q", path, got, site.referer)
						}
					}
				}
			})
		}
	}
}

func TestProductCategorizesDiscoveryFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   ErrorCategory
	}{
		{"authentication", 401, `{}`, ErrorAuthentication},
		{"unavailable", 404, `{}`, ErrorUnsupported},
		{"rate-limit", 429, `{}`, ErrorNetwork},
		{"service", 500, `{}`, ErrorNetwork},
		{"region", 451, `{}`, ErrorUnsupported},
		{"malformed", 200, `{`, ErrorInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				status, body := test.status, test.body
				if strings.HasSuffix(request.URL.Path, "/token") {
					status, body = 200, `{"data":{"attributes":{"token":"fixture-token"}}}`
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})
			transport, err := network.New(network.Config{RoundTripper: roundTrip})
			if err != nil {
				t.Fatal(err)
			}
			operation := &operation{client: NewClient(), request: Request{SkipDownload: true}, transport: transport, registry: productRegistry(), rootExtractor: new(string)}
			_, err = operation.process(context.Background(), "https://go.discovery.com/video/show/episode", "", nil, make(map[string]bool), 0)
			if !IsCategory(err, test.want) {
				t.Fatalf("category error=%v want=%s", err, test.want)
			}
		})
	}
}

func TestProductTele5CMSOpaqueReentryPreservesPublicIdentity(t *testing.T) {
	publicURL := "https://tele5.de/mediathek/star-trek/vox-sola"
	var contentReferer string
	roundTrip := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var body string
		if strings.Contains(request.URL.Host, "aurora.enhanced.live") || strings.HasSuffix(request.URL.Path, "/token") {
			for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Referer"} {
				if got := request.Header.Get(key); got != "" {
					return nil, fmt.Errorf("%s leaked to isolated Discovery request: %q", key, got)
				}
			}
		}
		switch {
		case strings.Contains(request.URL.Host, "aurora.enhanced.live"):
			body = `{"blocks":[{"videoId":"4140114"}]}`
		case strings.HasSuffix(request.URL.Path, "/token"):
			body = `{"data":{"attributes":{"token":"fixture-token"}}}`
		case strings.Contains(request.URL.Path, "/content/videos/"):
			contentReferer = request.Header.Get("Referer")
			body = `{"data":{"id":"4140114","attributes":{"name":"Vox Sola","videoDuration":1000}}}`
		case strings.Contains(request.URL.Path, "videoPlaybackInfo"):
			body = `{"data":{"attributes":{"streaming":[{"type":"http","url":"https://cdn.example.invalid/video.mp4"}]}}}`
		default:
			return nil, fmt.Errorf("unexpected Discovery request %s", request.URL.Redacted())
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})
	transport, err := network.New(network.Config{RoundTripper: roundTrip})
	if err != nil {
		t.Fatal(err)
	}
	operation := &operation{client: NewClient(), request: Request{SkipDownload: true}, transport: transport, registry: productRegistry(), rootExtractor: new(string)}
	result, err := operation.process(context.Background(), publicURL, "", nil, make(map[string]bool), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entries) != 1 {
		t.Fatalf("entries=%d", len(result.Entries))
	}
	jsonText := string(result.Entries[0].InfoJSON)
	if !strings.Contains(jsonText, publicURL) || strings.Contains(jsonText, "discovery:tele5:") {
		t.Fatalf("identity JSON=%s", jsonText)
	}
	if contentReferer != publicURL {
		t.Fatalf("content Referer=%q", contentReferer)
	}
}
