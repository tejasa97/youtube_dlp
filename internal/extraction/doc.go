// Package extraction defines provider-neutral extraction contracts and the
// deterministic registry used by engine orchestration.
//
// Registry is generic over the request supplied to providers. This keeps the
// registry independent of provider-specific request options while the current
// internal/extractor compatibility package binds it to its legacy Request.
// Moving that request shape to an engine-owned contract is the next seam needed
// before concrete providers can move into separate packages.
package extraction
