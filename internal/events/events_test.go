package events

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestMultiEmitsInOrderAndJoinsErrors(t *testing.T) {
	var calls []string
	firstError := errors.New("first")
	sink := Multi(
		SinkFunc(func(context.Context, Event) error {
			calls = append(calls, "first")
			return firstError
		}),
		SinkFunc(func(context.Context, Event) error {
			calls = append(calls, "second")
			return nil
		}),
	)
	err := sink.Emit(context.Background(), Event{Kind: KindStarting})
	if !errors.Is(err, firstError) {
		t.Fatalf("Emit() error = %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"first", "second"}) {
		t.Fatalf("call order = %v", calls)
	}
}
