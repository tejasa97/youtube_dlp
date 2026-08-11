package ytdlp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tejasa97/youtube_dlp/engine"
	"github.com/tejasa97/youtube_dlp/pkg/ytdlp"
)

func TestPublicationArbiterFacadeAliasesEngineContracts(t *testing.T) {
	var _ ytdlp.AtomicCommitError = engine.AtomicCommitError(nil)
	var _ *ytdlp.PublicationArbiter = (*engine.PublicationArbiter)(nil)
	var _ *ytdlp.CancelReservation = (*engine.CancelReservation)(nil)
	var _ *ytdlp.PublicationReservation = (*engine.PublicationReservation)(nil)

	for _, test := range []struct {
		name string
		got  error
		want error
	}{
		{"already published", ytdlp.ErrAlreadyPublished, engine.ErrAlreadyPublished},
		{"cancel already accepted", ytdlp.ErrCancelAlreadyAccepted, engine.ErrCancelAlreadyAccepted},
		{"cancel won", ytdlp.ErrCancelWon, engine.ErrCancelWon},
		{"completion in progress", ytdlp.ErrCompletionInProgress, engine.ErrCompletionInProgress},
		{"publication indeterminate", ytdlp.ErrPublicationIndeterminate, engine.ErrPublicationIndeterminate},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want || !errors.Is(test.got, test.want) {
				t.Fatalf("facade error = %v, want engine identity %v", test.got, test.want)
			}
		})
	}
}

func TestPublicationArbiterFacadeDelegatesToEngineImplementation(t *testing.T) {
	arbiter := ytdlp.NewPublicationArbiter()
	reservation, err := arbiter.BeginCancel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.AbortCancel()

	engineArbiter := engine.NewPublicationArbiter()
	publication, err := engineArbiter.BeginPublication(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	publication.AbortBeforeReplace()
}
