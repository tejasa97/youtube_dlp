// Package engine defines the public provider-neutral extraction contracts and
// deterministic registry used by media-engine composition.
//
// Registry is generic over the request supplied to providers. Request owns the
// common operation state; provider packages may define typed request structs
// that implement URLRequest and layer their own options on that state.
package engine
