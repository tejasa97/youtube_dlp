package youtube

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/javascript/ejs"
	"github.com/tejasa97/ytdlp-go/internal/youtubepot"
)

type requestTestSolver struct{}

func (*requestTestSolver) SolvePlayer(context.Context, string, string, []ejs.ChallengeRequest, bool) (ejs.Result, error) {
	return ejs.Result{}, nil
}

func TestRequestRetainsAllTypedYouTubeOptionsAndRedactsDiagnostics(t *testing.T) {
	director, err := youtubepot.New(youtubepot.Config{Policy: youtubepot.FetchNever})
	if err != nil {
		t.Fatal(err)
	}
	solver := &requestTestSolver{}
	comments := CommentOptions{
		Enabled: true, Sort: "new", MaxComments: 101, MaxParents: 102,
		MaxReplies: 103, MaxRepliesPerThread: 104, MaxDepth: 105,
	}
	request := NewRequest(provider.Request{URL: "https://user:secret@example.test/watch?v=private"}, Options{
		ChallengeSolver: solver, POT: director, TranslatedCaptions: true,
		LiveFromStart: true, Comments: comments,
	})
	if request.Options.ChallengeSolver != solver || request.Options.POT != director || !request.Options.TranslatedCaptions || !request.Options.LiveFromStart || request.Options.Comments != comments {
		t.Fatalf("YouTube options were not retained: %#v", request)
	}
	for _, rendered := range []string{fmt.Sprint(request), fmt.Sprintf("%+v", request), fmt.Sprintf("%#v", request)} {
		for _, secret := range []string{"user", "secret", "private", "requestTestSolver", "POTDirector"} {
			if strings.Contains(rendered, secret) {
				t.Fatalf("formatted request %q contains %q", rendered, secret)
			}
		}
	}
}
