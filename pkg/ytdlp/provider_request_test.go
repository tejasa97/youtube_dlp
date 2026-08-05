package ytdlp

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
	"github.com/tejasa97/youtube_dlp/internal/extractor"
	"github.com/tejasa97/youtube_dlp/internal/javascript/ejs"
	"github.com/tejasa97/youtube_dlp/internal/network"
	"github.com/tejasa97/youtube_dlp/internal/youtubepot"
)

type providerRequestCredentials struct{}

func (*providerRequestCredentials) Lookup(context.Context, string) (extractor.Credential, bool, error) {
	return extractor.Credential{}, false, nil
}

type providerRequestSolver struct{}

func (*providerRequestSolver) SolvePlayer(context.Context, string, string, []ejs.ChallengeRequest, bool) (ejs.Result, error) {
	return ejs.Result{}, nil
}

type providerRequestRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip providerRequestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestProductRequestAdaptsEveryYouTubeOptionWithoutWideningScope(t *testing.T) {
	transport, err := network.New(network.Config{RoundTripper: providerRequestRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	director, err := youtubepot.New(youtubepot.Config{Policy: youtubepot.FetchNever})
	if err != nil {
		t.Fatal(err)
	}
	solver := &providerRequestSolver{}
	credentials := &providerRequestCredentials{}
	productRequest := engine.Request{
		VideoPassword:             "video-secret",
		YouTubeTranslatedCaptions: true,
		LiveFromStart:             true,
		YouTubeComments: YouTubeCommentOptions{
			Enabled: true, Sort: "new", MaxComments: 11, MaxParents: 12,
			MaxReplies: 13, MaxRepliesPerThread: 14, MaxDepth: 15,
		},
		SoundCloudComments: SoundCloudCommentOptions{Enabled: true, Sort: "oldest", MaxComments: 16},
		NHK:                NHKOptions{RadiruArea: "130"},
		Playlist:           PlaylistOptions{Disabled: true},
	}
	legacy := broadProviderRequest(providerapi.Operation{
		Request: providerapi.Request{
			URL:     "https://user:url-secret@www.youtube.com/watch?v=abcdefghijk",
			Referer: "https://referrer.test/embed", SearchQueryOverride: "private query",
			Transport: transport, Credentials: credentials, VideoPassword: "video-secret", NoPlaylist: true,
		},
		ChallengeSolver: solver, POTResolver: director,
	}, productRequest)
	request := legacy.YouTubeRequest()
	if request.Transport != transport || request.Credentials != credentials || request.Options.ChallengeSolver != solver || request.Options.POT != director {
		t.Fatal("operation-scoped transport, credentials, solver, or POT director changed during adaptation")
	}
	if request.URL != legacy.URL || request.Referer != legacy.Referer || request.SearchQueryOverride != legacy.SearchQueryOverride || request.VideoPassword != "video-secret" || !request.NoPlaylist {
		t.Fatalf("neutral product state was not retained: %#v", request)
	}
	wantComments := extractor.YouTubeCommentOptions{
		Enabled: true, Sort: "new", MaxComments: 11, MaxParents: 12,
		MaxReplies: 13, MaxRepliesPerThread: 14, MaxDepth: 15,
	}
	if !request.Options.TranslatedCaptions || !request.Options.LiveFromStart || request.Options.Comments != wantComments {
		t.Fatalf("YouTube product options were not retained: %#v", request)
	}
	if legacy.SoundCloudComments != (extractor.SoundCloudCommentOptions{Enabled: true, Sort: "oldest", MaxComments: 16}) || legacy.NHK.RadiruArea != "130" {
		t.Fatal("other-provider state changed at the YouTube adaptation boundary")
	}
	for _, rendered := range []string{fmt.Sprint(legacy), fmt.Sprintf("%+v", legacy), fmt.Sprintf("%#v", legacy), fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request)} {
		for _, secret := range []string{"url-secret", "private query", "referrer", "video-secret", "providerRequestCredentials", "providerRequestSolver"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("formatted adapted request %q contains %q", rendered, secret)
			}
		}
	}
}
