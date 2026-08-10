package ytdlp

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrAlreadyPublished      = errors.New("engine: already published")
	ErrCancelAlreadyAccepted = errors.New("engine: cancel already accepted")
	ErrCancelWon             = errors.New("engine: cancel won")
	ErrCompletionInProgress  = errors.New("engine: completion in progress")
)

// AtomicCommitError distinguishes failure before an atomic replacement from
// failure to confirm durability after the replacement committed.
type AtomicCommitError interface {
	error
	Committed() bool
}

type publicationArbiterState uint8

const (
	publicationOpen publicationArbiterState = iota
	cancelReserved
	cancelWon
	publicationReserved
	publicationWon
)

// PublicationArbiter serializes durable cancellation with output
// publication for one worker run. It must not be persisted or reused.
type PublicationArbiter struct {
	mu      sync.Mutex
	state   publicationArbiterState
	changed chan struct{}
}

// NewPublicationArbiter returns an arbiter in the open state.
func NewPublicationArbiter() *PublicationArbiter {
	return &PublicationArbiter{changed: make(chan struct{})}
}

// CancelReservation retains the arbiter's Cancel reservation until exactly
// one terminal operation is called.
type CancelReservation struct {
	arbiter  *PublicationArbiter
	finished bool
}

// PublicationReservation retains the publication reservation across the
// destination replacement and subsequent durable-state commit attempt.
type PublicationReservation struct {
	arbiter             *PublicationArbiter
	destinationReplaced bool
	finished            bool
}

// BeginCancel waits for any in-flight reservation or for ctx to expire.
func (arbiter *PublicationArbiter) BeginCancel(ctx context.Context) (*CancelReservation, error) {
	if arbiter == nil {
		panic("ytdlp: nil PublicationArbiter")
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
			arbiter.signalChangeLocked()
			reservation := &CancelReservation{arbiter: arbiter}
			arbiter.mu.Unlock()
			return reservation, nil
		case cancelWon:
			arbiter.mu.Unlock()
			return nil, ErrCancelAlreadyAccepted
		case publicationWon:
			arbiter.mu.Unlock()
			return nil, ErrAlreadyPublished
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
		panic("ytdlp: nil PublicationArbiter")
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
			arbiter.signalChangeLocked()
			reservation := &PublicationReservation{arbiter: arbiter}
			arbiter.mu.Unlock()
			return reservation, nil
		case cancelWon:
			arbiter.mu.Unlock()
			return nil, ErrCancelWon
		case publicationWon:
			arbiter.mu.Unlock()
			return nil, ErrAlreadyPublished
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
		panic("ytdlp: invalid CancelReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.finished || arbiter.state != cancelReserved {
		panic("ytdlp: invalid CancelReservation terminal operation")
	}
	reservation.finished = true
	arbiter.state = next
	arbiter.signalChangeLocked()
}

// MarkDestinationReplaced makes publication the irreversible winner while
// retaining the reservation for the following durable-state commit attempt.
func (reservation *PublicationReservation) MarkDestinationReplaced() {
	if reservation == nil || reservation.arbiter == nil {
		panic("ytdlp: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.finished || reservation.destinationReplaced || arbiter.state != publicationReserved {
		panic("ytdlp: invalid MarkDestinationReplaced operation")
	}
	reservation.destinationReplaced = true
	arbiter.state = publicationWon
	arbiter.signalChangeLocked()
}

// FinishPublication releases a reservation after destination replacement and
// the subsequent durable-state commit attempt.
func (reservation *PublicationReservation) FinishPublication() {
	if reservation == nil || reservation.arbiter == nil {
		panic("ytdlp: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.finished || !reservation.destinationReplaced || arbiter.state != publicationWon {
		panic("ytdlp: invalid FinishPublication operation")
	}
	reservation.finished = true
	arbiter.signalChangeLocked()
}

// AbortBeforeReplace releases the reservation and restores the open state.
func (reservation *PublicationReservation) AbortBeforeReplace() {
	if reservation == nil || reservation.arbiter == nil {
		panic("ytdlp: invalid PublicationReservation")
	}
	arbiter := reservation.arbiter
	arbiter.mu.Lock()
	defer arbiter.mu.Unlock()
	if reservation.finished || reservation.destinationReplaced || arbiter.state != publicationReserved {
		panic("ytdlp: invalid AbortBeforeReplace operation")
	}
	reservation.finished = true
	arbiter.state = publicationOpen
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
