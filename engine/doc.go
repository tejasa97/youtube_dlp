// Package engine preserves the original public provider-contract surface.
//
// New provider packages should import engine/provider, the cycle-free owner of
// these contracts. Root aliases remain for source compatibility and for the
// later public orchestration API.
package engine
