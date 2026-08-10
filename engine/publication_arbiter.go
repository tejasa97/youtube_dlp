package engine

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrAlreadyPublished         = errors.New("engine: already published")
	ErrCancelAlreadyAccepted    = errors.New("engine: cancel already accepted")
	ErrCancelWon                = errors.New("engine: cancel won")
	ErrCompletionInProgress     = errors.New("engine: completion in progress")
	ErrPublicationIndeterminate = errors.New("engine: publication outcome indeterminate; reconciliation required")
)

// AtomicCommitError distinguishes failure before an atomic replacement,
// failure after it committed, and an indeterminate failure where neither old
// nor new authority could be established. Committed and Indeterminate are
// mutually exclusive; both false identifies an ordinary pre-commit failure.
type AtomicCommitError interface {
	error
	Committed() bool
	Indeterminate() bool
}

type publicationArbiterState uint8

const (
	publicationOpen publicationArbiterState = iota
	cancelReserved
	cancelWon
	publicationReserved
	publicationWon
	publicationIndeterminate
)

// PublicationArbiter serializes durable cancellation with output
// publication for one worker run. It must not be persisted or reused.
type PublicationArbiter struct {
	mu          sync.Mutex
	state       publicationArbiterState
	activeGuard *reservationGuard
	changed     chan struct{}
}

// reservationGuard is non-zero-sized so distinct live allocations have
// distinct addresses. Value copies of a reservation intentionally share it.
type reservationGuard struct{ marker byte }

// NewPublicationArbiter returns an arbiter in the open state.
func NewPublicationArbiter() *PublicationArbiter {
	return &PublicationArbiter{changed: make(chan struct{})}
}

// CancelReservation retains the arbiter's Cancel reservation until exactly
// one terminal operation is called.
type CancelReservation struct {
	arbiter *PublicationArbiter
	guard   *reservationGuard
}

// PublicationReservation retains the publication reservation across the
// destination replacement and subsequent durable-state commit attempt.
type PublicationReservation struct {
	arbiter *PublicationArbiter
	guard   *reservationGuard
}

// BeginCancel waits for any in-flight reservation or for ctx to expire.
func (arbiter *PublicationArbiter) BeginCancel(ctx context.Context) (*CancelReservation, error) {
	if arbiter == nil {
		panic("engine: nil PublicationArbiter")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		arbiter.mu.Lock()
		arbiter.initializeLocked()
		switch arbiter.state {
		case publicationOpen:
			if err := ctx.Err(); err != nil {
				arbiter.mu.Unlock()
				return nil, err
			}
			arbiter.state = cancelReserved
			guard := &reservationGuard{}
			arbiter.activeGuard = guard
			arbiter.signalChangeLocked()
			reservation := &CancelReservation{arbiter: arbiter, guard: guard}
			arbiter.mu.Unlock()
			return reservation, nil
		case cancelWon:
			arbiter.mu.Unlock()
			return nil, ErrCancelAlreadyAccepted
		case publicationWon:
			arbiter.mu.Unlock()
			return nil, ErrAlreadyPublished
		case publicationIndeterminate:
			arbiter.mu.Unlock()
			return nil, ErrPublicationIndeterminate
		default:
			waitingOnPublication := arbiter.state == publicationReserved
			changed := arbiter.changed
			arbiter.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				if waitingOnPublication {
					return nil, errors.Join(ErrCompletionInProgress, ctx.Err())
				}
				return nil, ctx.Err()
			}
		}
	}
}

// BeginPublication waits for any in-flight reservation or for ctx to expire.
func (arbiter *PublicationArbiter) BeginPublication(ctx context.Context) (*PublicationReservation, error) {
	if arbiter == nil {
		panic("engine: nil PublicationArbiter")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		arbiter.mu.Lock()
		arbiter.initializeLocked()
		switch arbiter.state {
		case publicationOpen:
			if err := ctx.Err(); err != nil {
				arbiter.mu.Unlock()
				return nil, err
			}
			arbiter.state = publicationReserved
			guard := &reservationGuard{}
			arbiter.activeGuard = guard
			arbiter.signalChangeLocked()
			reservation := &PublicationReservation{arbiter: arbiter, guard: guard}
			arbiter.mu.Unlock()
			return reservation, nil
		case cancelWon:
			arbiter.mu.Unlock()
			return nil, ErrCancelWon
		case publicationWon:
			arbiter.mu.Unlock()
			return nil, ErrAlreadyPublished
		case publicationIndeterminate:
			arbiter.mu.Unlock()
			return nil, ErrPublicationIndeterminate
		default:
			changed := arbiter.changed
			arbiter.mu.Unlock()
			select {
			case <-changed:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
}

// WinCancel makes Cancel the irreversible winner and releases the reservation.
func (reservation *CancelReservation) WinCancel() {
	reservation.terminal(cancelWon)
}

// AbortCancel releases the reservation and restores the open state.
func (reservation *CancelReservation) AbortCancel() {
	reservation.terminal(publicationOpen)
}

func (reservation *CancelReservation) terminal(next publicationArbiterState) {
	if reservation == nil || reservation.arbiter == nil {
		panic("engine: invalid CancelReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.guard == nil ||
		arbiter.activeGuard != reservation.guard || arbiter.state != cancelReserved {
		panic("engine: invalid CancelReservation terminal operation")
	}
	arbiter.state = next
	arbiter.activeGuard = nil
	arbiter.signalChangeLocked()
}

// MarkDestinationReplaced makes publication the irreversible winner while
// retaining the reservation for the following durable-state commit attempt.
func (reservation *PublicationReservation) MarkDestinationReplaced() {
	if reservation == nil || reservation.arbiter == nil {
		panic("engine: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.guard == nil ||
		arbiter.activeGuard != reservation.guard ||
		arbiter.state != publicationReserved {
		panic("engine: invalid MarkDestinationReplaced operation")
	}
	arbiter.state = publicationWon
	arbiter.signalChangeLocked()
}

// FinishPublication releases a reservation after destination replacement and
// the subsequent durable-state commit attempt.
func (reservation *PublicationReservation) FinishPublication() {
	if reservation == nil || reservation.arbiter == nil {
		panic("engine: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.guard == nil ||
		arbiter.activeGuard != reservation.guard || arbiter.state != publicationWon {
		panic("engine: invalid FinishPublication operation")
	}
	arbiter.activeGuard = nil
	arbiter.signalChangeLocked()
}

// MarkIndeterminate terminalizes a publication reservation when replacement
// authority cannot be established. Neither publication nor Cancel wins; all
// later acquisition attempts require durable reconciliation.
func (reservation *PublicationReservation) MarkIndeterminate() {
	if reservation == nil || reservation.arbiter == nil {
		panic("engine: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.guard == nil ||
		arbiter.activeGuard != reservation.guard ||
		arbiter.state != publicationReserved {
		panic("engine: invalid MarkIndeterminate operation")
	}
	arbiter.state = publicationIndeterminate
	arbiter.activeGuard = nil
	arbiter.signalChangeLocked()
}

// AbortBeforeReplace releases the reservation and restores the open state.
func (reservation *PublicationReservation) AbortBeforeReplace() {
	if reservation == nil || reservation.arbiter == nil {
		panic("engine: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.guard == nil ||
		arbiter.activeGuard != reservation.guard ||
		arbiter.state != publicationReserved {
		panic("engine: invalid AbortBeforeReplace operation")
	}
	arbiter.state = publicationOpen
	arbiter.activeGuard = nil
	arbiter.signalChangeLocked()
}

func (arbiter *PublicationArbiter) signalChangeLocked() {
	close(arbiter.changed)
	arbiter.changed = make(chan struct{})
}

func (arbiter *PublicationArbiter) initializeLocked() {
	if arbiter.changed == nil {
		arbiter.changed = make(chan struct{})
	}
}
