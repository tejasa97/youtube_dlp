package ytdlp

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublicationArbiterCancelTransitions(t *testing.T) {
	t.Run("abort reopens", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		cancel, err := arbiter.BeginCancel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		cancel.AbortCancel()
		publication, err := arbiter.BeginPublication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		publication.AbortBeforeReplace()
	})

	t.Run("win is terminal", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		cancel, err := arbiter.BeginCancel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		cancel.WinCancel()
		if _, err := arbiter.BeginCancel(context.Background()); !errors.Is(err, ErrCancelAlreadyAccepted) {
			t.Fatalf("BeginCancel error = %v", err)
		}
		if _, err := arbiter.BeginPublication(context.Background()); !errors.Is(err, ErrCancelWon) {
			t.Fatalf("BeginPublication error = %v", err)
		}
	})
}

func TestPublicationArbiterPublicationTransitions(t *testing.T) {
	t.Run("abort reopens", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		publication, err := arbiter.BeginPublication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		publication.AbortBeforeReplace()
		cancel, err := arbiter.BeginCancel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		cancel.AbortCancel()
	})

	t.Run("replacement wins before finish", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		publication, err := arbiter.BeginPublication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		publication.MarkDestinationReplaced()
		if _, err := arbiter.BeginCancel(context.Background()); !errors.Is(err, ErrAlreadyPublished) {
			t.Fatalf("BeginCancel error = %v", err)
		}
		publication.FinishPublication()
		if _, err := arbiter.BeginPublication(context.Background()); !errors.Is(err, ErrAlreadyPublished) {
			t.Fatalf("BeginPublication error = %v", err)
		}
	})
}

func TestPublicationArbiterCanceledContexts(t *testing.T) {
	for _, begin := range []struct {
		name string
		call func(*PublicationArbiter, context.Context) error
	}{
		{"cancel", func(arbiter *PublicationArbiter, ctx context.Context) error {
			_, err := arbiter.BeginCancel(ctx)
			return err
		}},
		{"publication", func(arbiter *PublicationArbiter, ctx context.Context) error {
			_, err := arbiter.BeginPublication(ctx)
			return err
		}},
	} {
		t.Run(begin.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err := begin.call(NewPublicationArbiter(), ctx); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPublicationArbiterZeroValueIsOpen(t *testing.T) {
	var arbiter PublicationArbiter
	reservation, err := arbiter.BeginCancel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	reservation.AbortCancel()
}

func TestPublicationArbiterContextExpiresWhileAcquisitionIsLocked(t *testing.T) {
	tests := []struct {
		name string
		call func(*PublicationArbiter, context.Context) error
	}{
		{
			name: "cancel",
			call: func(arbiter *PublicationArbiter, ctx context.Context) error {
				_, err := arbiter.BeginCancel(ctx)
				return err
			},
		},
		{
			name: "publication",
			call: func(arbiter *PublicationArbiter, ctx context.Context) error {
				_, err := arbiter.BeginPublication(ctx)
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arbiter := NewPublicationArbiter()
			arbiter.mu.Lock()
			ctx := newLockBoundaryContext()
			result := make(chan error, 1)
			go func() {
				result <- test.call(arbiter, ctx)
			}()
			<-ctx.initialErrChecked

			// The first Err check has returned nil and acquisition remains
			// blocked. Cancel before allowing it to take the mutex.
			ctx.cancel()
			<-ctx.Done()
			arbiter.mu.Unlock()

			if err := receive(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context.Canceled", err)
			}

			reservation, err := arbiter.BeginCancel(context.Background())
			if err != nil {
				t.Fatalf("arbiter mutated despite canceled acquisition: %v", err)
			}
			reservation.AbortCancel()
		})
	}
}

type lockBoundaryContext struct {
	initialErrChecked chan struct{}
	done              chan struct{}
	canceled          atomic.Bool
	firstErr          sync.Once
}

func newLockBoundaryContext() *lockBoundaryContext {
	return &lockBoundaryContext{
		initialErrChecked: make(chan struct{}),
		done:              make(chan struct{}),
	}
}

func (ctx *lockBoundaryContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *lockBoundaryContext) Done() <-chan struct{}       { return ctx.done }
func (ctx *lockBoundaryContext) Value(any) any               { return nil }

func (ctx *lockBoundaryContext) Err() error {
	if ctx.canceled.Load() {
		return context.Canceled
	}
	ctx.firstErr.Do(func() { close(ctx.initialErrChecked) })
	return nil
}

func (ctx *lockBoundaryContext) cancel() {
	ctx.canceled.Store(true)
	close(ctx.done)
}

func TestPublicationArbiterWaitsAndWakesAfterAbort(t *testing.T) {
	t.Run("cancel waits for publication", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		publication, err := arbiter.BeginPublication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			cancel, err := arbiter.BeginCancel(context.Background())
			if err == nil {
				cancel.AbortCancel()
			}
			result <- err
		}()
		assertStillWaiting(t, result)
		publication.AbortBeforeReplace()
		if err := receive(t, result); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("publication waits for cancel", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		cancel, err := arbiter.BeginCancel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result := make(chan error, 1)
		go func() {
			publication, err := arbiter.BeginPublication(context.Background())
			if err == nil {
				publication.AbortBeforeReplace()
			}
			result <- err
		}()
		assertStillWaiting(t, result)
		cancel.AbortCancel()
		if err := receive(t, result); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPublicationArbiterWaitDeadline(t *testing.T) {
	t.Run("cancel reports completion in progress", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		publication, err := arbiter.BeginPublication(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer publication.AbortBeforeReplace()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = arbiter.BeginCancel(ctx)
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrCompletionInProgress) {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("publication preserves deadline", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		cancelReservation, err := arbiter.BeginCancel(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer cancelReservation.AbortCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err = arbiter.BeginPublication(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestPublicationArbiterConcurrentWinner(t *testing.T) {
	for range 100 {
		arbiter := NewPublicationArbiter()
		start := make(chan struct{})
		var waitGroup sync.WaitGroup
		waitGroup.Add(2)
		outcomes := make(chan error, 2)

		go func() {
			defer waitGroup.Done()
			<-start
			reservation, err := arbiter.BeginCancel(context.Background())
			if err == nil {
				reservation.WinCancel()
			}
			outcomes <- err
		}()
		go func() {
			defer waitGroup.Done()
			<-start
			reservation, err := arbiter.BeginPublication(context.Background())
			if err == nil {
				reservation.MarkDestinationReplaced()
				reservation.FinishPublication()
			}
			outcomes <- err
		}()

		close(start)
		waitGroup.Wait()
		close(outcomes)
		wins := 0
		for err := range outcomes {
			if err == nil {
				wins++
				continue
			}
			if !errors.Is(err, ErrCancelWon) && !errors.Is(err, ErrAlreadyPublished) {
				t.Fatalf("unexpected loser error: %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("winner count = %d", wins)
		}
	}
}

func TestPublicationReservationRejectsInvalidTerminalOperations(t *testing.T) {
	t.Run("double cancel terminal", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		reservation, _ := arbiter.BeginCancel(context.Background())
		reservation.AbortCancel()
		assertPanics(t, reservation.WinCancel)
	})
	t.Run("finish before replacement", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		reservation, _ := arbiter.BeginPublication(context.Background())
		assertPanics(t, reservation.FinishPublication)
		reservation.AbortBeforeReplace()
	})
	t.Run("abort after replacement", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		reservation, _ := arbiter.BeginPublication(context.Background())
		reservation.MarkDestinationReplaced()
		assertPanics(t, reservation.AbortBeforeReplace)
		reservation.FinishPublication()
	})
	t.Run("double replacement mark", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		reservation, _ := arbiter.BeginPublication(context.Background())
		reservation.MarkDestinationReplaced()
		assertPanics(t, reservation.MarkDestinationReplaced)
		reservation.FinishPublication()
	})
	t.Run("double publication finish", func(t *testing.T) {
		arbiter := NewPublicationArbiter()
		reservation, _ := arbiter.BeginPublication(context.Background())
		reservation.MarkDestinationReplaced()
		reservation.FinishPublication()
		assertPanics(t, reservation.FinishPublication)
	})
}

func assertStillWaiting(t *testing.T, result <-chan error) {
	t.Helper()
	select {
	case err := <-result:
		t.Fatalf("operation returned early: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
}

func receive(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for operation")
		return nil
	}
}

func assertPanics(t *testing.T, call func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	call()
}
