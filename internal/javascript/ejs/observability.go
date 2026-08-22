package ejs

import (
	"errors"
	"fmt"

	"github.com/tejasa97/ytdlp-go/engine/provider"
	"github.com/tejasa97/ytdlp-go/internal/javascript/protocol"
)

func challengeFailure(category, phase string, err error) error {
	if err == nil {
		err = fmt.Errorf("EJS challenge failed")
	}
	return &provider.ChallengeFailure{
		Diagnostics: provider.ChallengeDiagnostics{
			HelperCategory: category,
			Phase:          phase,
		},
		Err: err,
	}
}

func helperCategory(code protocol.ErrorCode) string {
	if code == "" {
		return provider.ChallengeHelperUnknown
	}
	return string(code)
}

func annotateChallengeFailure(err error, diagnostics provider.ChallengeDiagnostics) error {
	if err == nil {
		return nil
	}
	var failure *provider.ChallengeFailure
	if errors.As(err, &failure) {
		merged := diagnostics
		if failure.Diagnostics.HelperCategory != "" {
			merged.HelperCategory = failure.Diagnostics.HelperCategory
		}
		if merged.Phase == "" {
			merged.Phase = failure.Diagnostics.Phase
		}
		// Wrap the full incoming chain rather than only the matched failure's
		// cause, so any authoritative outer sentinel or context remains visible.
		return &provider.ChallengeFailure{Diagnostics: merged.Sanitize(), Err: err}
	}
	if diagnostics.HelperCategory == "" {
		diagnostics.HelperCategory = provider.ChallengeHelperUnknown
	}
	return &provider.ChallengeFailure{Diagnostics: diagnostics.Sanitize(), Err: err}
}
