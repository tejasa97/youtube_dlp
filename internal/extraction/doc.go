// Package extraction defines provider-neutral extraction contracts and the
// deterministic registry used by engine orchestration.
//
// Registry is generic over the request supplied to providers. Request owns the
// common engine state; provider packages layer typed options on it. The current
// internal/extractor compatibility package adapts its legacy mixed Request to
// these contracts until concrete provider dependency closures move.
package extraction
