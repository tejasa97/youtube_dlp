package engine

import (
	"context"
	"errors"

	providerapi "github.com/tejasa97/youtube_dlp/engine/provider"
)

// observingChallengeSolver emits a secret-free javascript_challenge event for
// every SolvePlayer attempt. The event carries only Kind and a closed
// key=value Message; URL, path, extractor, hashes, and helper output are omitted.
type observingChallengeSolver struct {
	inner  providerapi.ChallengeSolver
	client *Client
}

func newObservingChallengeSolver(inner providerapi.ChallengeSolver, client *Client) providerapi.ChallengeSolver {
	return observingChallengeSolver{inner: inner, client: client}
}

func (solver observingChallengeSolver) Available() bool {
	if solver.inner == nil {
		return false
	}
	if availability, ok := solver.inner.(interface{ Available() bool }); ok {
		return availability.Available()
	}
	return true
}

func (solver observingChallengeSolver) SolvePlayer(
	ctx context.Context,
	id string,
	player string,
	requests []providerapi.ChallengeRequest,
	outputPreprocessed bool,
) (providerapi.ChallengeResult, error) {
	if solver.inner == nil {
		err := &providerapi.ChallengeFailure{
			Diagnostics: providerapi.ChallengeDiagnostics{
				Cache:            providerapi.ChallengeCacheNone,
				PreprocessBucket: providerapi.ChallengeBucketNone,
				SolveBucket:      providerapi.ChallengeBucketNone,
				HelperCategory:   providerapi.ChallengeHelperUnavailable,
				Phase:            providerapi.ChallengePhasePreprocess,
			}.Sanitize(),
			Err: providerapi.ErrChallengeSolver,
		}
		emitErr := solver.emitDiagnostics(ctx, err.Diagnostics)
		return providerapi.ChallengeResult{}, challengeAndEventError(err, emitErr)
	}
	result, err := solver.inner.SolvePlayer(ctx, id, player, requests, outputPreprocessed)
	diagnostics := challengeDiagnosticsFrom(result, err)
	return result, challengeAndEventError(err, solver.emitDiagnostics(ctx, diagnostics))
}

func (solver observingChallengeSolver) emitDiagnostics(ctx context.Context, diagnostics providerapi.ChallengeDiagnostics) error {
	if solver.client == nil {
		return nil
	}
	return solver.client.emit(ctx, Event{
		Kind:    EventJavaScriptChallenge,
		Message: diagnostics.EventMessage(),
	})
}

func challengeAndEventError(challengeErr, emitErr error) error {
	if emitErr == nil {
		return challengeErr
	}
	eventErr := &Error{Category: ErrorInternal, Op: "emit JavaScript challenge event", Err: emitErr}
	if challengeErr == nil {
		return eventErr
	}
	return errors.Join(challengeErr, eventErr)
}

func unavailableChallengeFailure(err error) error {
	if err == nil {
		err = providerapi.ErrChallengeSolver
	}
	return &providerapi.ChallengeFailure{
		Diagnostics: providerapi.ChallengeDiagnostics{
			Cache:            providerapi.ChallengeCacheNone,
			PreprocessBucket: providerapi.ChallengeBucketNone,
			SolveBucket:      providerapi.ChallengeBucketNone,
			HelperCategory:   providerapi.ChallengeHelperUnavailable,
			Phase:            providerapi.ChallengePhasePreprocess,
		}.Sanitize(),
		Err: err,
	}
}

func challengeDiagnosticsFrom(result providerapi.ChallengeResult, err error) providerapi.ChallengeDiagnostics {
	var failure *providerapi.ChallengeFailure
	if errors.As(err, &failure) {
		return failure.Diagnostics.Sanitize()
	}
	if err != nil {
		return providerapi.ChallengeDiagnostics{
			Cache:            providerapi.ChallengeCacheNone,
			PreprocessBucket: providerapi.ChallengeBucketNone,
			SolveBucket:      providerapi.ChallengeBucketNone,
			HelperCategory:   providerapi.ChallengeHelperUnknown,
		}.Sanitize()
	}
	return result.Diagnostics.Sanitize()
}
