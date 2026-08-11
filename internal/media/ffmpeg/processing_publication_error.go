package ffmpeg

import "fmt"

type processingPublicationError struct {
	operation     string
	cause         error
	committed     bool
	indeterminate bool
}

func (failure *processingPublicationError) Error() string {
	return fmt.Sprintf("ffmpeg processing publication: %s: %v", failure.operation, failure.cause)
}

func (failure *processingPublicationError) Unwrap() error       { return failure.cause }
func (failure *processingPublicationError) Committed() bool     { return failure.committed }
func (failure *processingPublicationError) Indeterminate() bool { return failure.indeterminate }
