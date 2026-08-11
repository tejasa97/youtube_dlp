package fragment

import "fmt"

type noClobberPublicationError struct {
	operation     string
	cause         error
	committed     bool
	indeterminate bool
}

func (failure *noClobberPublicationError) Error() string {
	return fmt.Sprintf("fragment no-clobber publication: %s: %v", failure.operation, failure.cause)
}

func (failure *noClobberPublicationError) Unwrap() error       { return failure.cause }
func (failure *noClobberPublicationError) Committed() bool     { return failure.committed }
func (failure *noClobberPublicationError) Indeterminate() bool { return failure.indeterminate }

func noClobberFailure(operation string, cause error, committed, indeterminate bool) error {
	if cause == nil {
		return nil
	}
	return &noClobberPublicationError{
		operation: operation, cause: cause, committed: committed, indeterminate: indeterminate,
	}
}
