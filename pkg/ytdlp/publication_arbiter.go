package ytdlp

import "github.com/tejasa97/ytdlp-go/engine"

// AtomicCommitError is the facade alias for engine.AtomicCommitError.
type AtomicCommitError = engine.AtomicCommitError

// PublicationArbiter is the facade alias for engine.PublicationArbiter.
type PublicationArbiter = engine.PublicationArbiter

// CancelReservation is the facade alias for engine.CancelReservation.
type CancelReservation = engine.CancelReservation

// PublicationReservation is the facade alias for engine.PublicationReservation.
type PublicationReservation = engine.PublicationReservation

var (
	// ErrAlreadyPublished reports that publication has already won.
	ErrAlreadyPublished = engine.ErrAlreadyPublished
	// ErrCancelAlreadyAccepted reports that cancellation has already won.
	ErrCancelAlreadyAccepted = engine.ErrCancelAlreadyAccepted
	// ErrCancelWon reports that cancellation won the publication race.
	ErrCancelWon = engine.ErrCancelWon
	// ErrCompletionInProgress reports a context-bounded wait behind publication.
	ErrCompletionInProgress = engine.ErrCompletionInProgress
	// ErrPublicationIndeterminate reports that reconciliation is required.
	ErrPublicationIndeterminate = engine.ErrPublicationIndeterminate
)

// NewPublicationArbiter returns an open per-worker publication arbiter.
func NewPublicationArbiter() *PublicationArbiter {
	return engine.NewPublicationArbiter()
}
