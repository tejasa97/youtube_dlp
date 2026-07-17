// Package events defines structured operation events shared by core components.
package events

import (
	"context"
	"errors"
)

type Kind string

const (
	KindExtracting           Kind = "extracting"
	KindExtracted            Kind = "extracted"
	KindStarting             Kind = "download_starting"
	KindProgress             Kind = "download_progress"
	KindRetry                Kind = "download_retry"
	KindCancelled            Kind = "download_cancelled"
	KindCompleted            Kind = "download_completed"
	KindFragmentStarting     Kind = "fragment_starting"
	KindFragmentCompleted    Kind = "fragment_completed"
	KindPostprocessStarting  Kind = "postprocess_starting"
	KindPostprocessProgress  Kind = "postprocess_progress"
	KindPostprocessCompleted Kind = "postprocess_completed"
)

// Event contains stable operation data. It intentionally excludes wall-clock
// timestamps so captured event streams remain deterministic.
type Event struct {
	Kind      Kind   `json:"kind"`
	Extractor string `json:"extractor,omitempty"`
	URL       string `json:"url,omitempty"`
	Path      string `json:"path,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Total     int64  `json:"total,omitempty"`
	Attempt   int    `json:"attempt,omitempty"`
	Resuming  bool   `json:"resuming,omitempty"`
	Message   string `json:"message,omitempty"`
	Fragment  int    `json:"fragment,omitempty"`
	Fragments int    `json:"fragments,omitempty"`
}

type Sink interface {
	Emit(context.Context, Event) error
}

type SinkFunc func(context.Context, Event) error

func (function SinkFunc) Emit(ctx context.Context, event Event) error {
	return function(ctx, event)
}

type nopSink struct{}

func (nopSink) Emit(context.Context, Event) error { return nil }

func Nop() Sink { return nopSink{} }

type multiSink []Sink

// Multi returns a sink that emits in argument order and joins sink failures.
func Multi(sinks ...Sink) Sink {
	filtered := make(multiSink, 0, len(sinks))
	for _, sink := range sinks {
		if sink != nil {
			filtered = append(filtered, sink)
		}
	}
	if len(filtered) == 0 {
		return Nop()
	}
	return filtered
}

func (sinks multiSink) Emit(ctx context.Context, event Event) error {
	var failures []error
	for _, sink := range sinks {
		if err := sink.Emit(ctx, event); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
