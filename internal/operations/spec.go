// Package operations provides bounded, privacy-preserving primitives for
// canary execution, operational aggregation, and regression repair evidence.
// It performs no network access and has no credential backend of its own.
package operations

import (
	"errors"
	"regexp"
	"sort"
)

const (
	SchemaVersion   = 1
	MaxCanaries     = 4096
	MaxCapabilities = 64
	MaxTimeoutMS    = int64(30 * 60 * 1000)
)

var (
	ErrInvalidSpec    = errors.New("invalid operations canary specification")
	ErrResourceLimit  = errors.New("operations resource limit exceeded")
	ErrOptInRequired  = errors.New("canary execution requires explicit opt-in")
	ErrInvalidOutcome = errors.New("invalid canary outcome")
	ErrInvalidDrill   = errors.New("invalid regression drill transition")
	ErrDecode         = errors.New("invalid operations evidence document")
	ErrNotYetValid    = errors.New("operations canary policy is not yet valid")
	ErrExpired        = errors.New("operations canary policy has expired")
	ErrRateLimited    = errors.New("operations canary execution rate exceeded")
	ErrInvalidReplay  = errors.New("invalid operations replay capture")
)

var identifierPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

type CanaryClass string

const (
	ClassPublic     CanaryClass = "public"
	ClassCredential CanaryClass = "credential"
	ClassRegion     CanaryClass = "region"
)

func (class CanaryClass) valid() bool {
	return class == ClassPublic || class == ClassCredential || class == ClassRegion
}

// SecretHandle identifies a deployment-owned secret without carrying its
// value. Provider and Name are bounded identifiers, not paths or URLs.
type SecretHandle struct {
	Provider string `json:"provider"`
	Name     string `json:"name"`
}

func (handle SecretHandle) empty() bool { return handle.Provider == "" && handle.Name == "" }
func (handle SecretHandle) valid() bool {
	return identifierPattern.MatchString(handle.Provider) && identifierPattern.MatchString(handle.Name)
}

// CanarySpec is intentionally declarative. TargetRef is resolved by an
// explicitly configured Runner and cannot contain a URL, path, or query.
type CanarySpec struct {
	ID           string       `json:"id"`
	Class        CanaryClass  `json:"class"`
	Extractor    string       `json:"extractor"`
	TargetRef    string       `json:"target_ref"`
	Capabilities []string     `json:"capabilities"`
	Secret       SecretHandle `json:"secret_handle"`
	Region       string       `json:"region"`
	TimeoutMS    int64        `json:"timeout_ms"`
}

// Suite is the versioned deterministic canary interchange unit.
type Suite struct {
	SchemaVersion int          `json:"schema_version"`
	Canaries      []CanarySpec `json:"canaries"`
}

func NewSuite(specs []CanarySpec) (Suite, error) {
	if len(specs) == 0 {
		return Suite{}, ErrInvalidSpec
	}
	if len(specs) > MaxCanaries {
		return Suite{}, ErrResourceLimit
	}
	copySpecs := append([]CanarySpec(nil), specs...)
	seen := make(map[string]bool, len(copySpecs))
	for index := range copySpecs {
		copySpecs[index].Capabilities = append([]string(nil), copySpecs[index].Capabilities...)
		if err := validateSpec(copySpecs[index]); err != nil {
			return Suite{}, err
		}
		if seen[copySpecs[index].ID] {
			return Suite{}, ErrInvalidSpec
		}
		seen[copySpecs[index].ID] = true
		sort.Strings(copySpecs[index].Capabilities)
	}
	sort.Slice(copySpecs, func(i, j int) bool { return copySpecs[i].ID < copySpecs[j].ID })
	return Suite{SchemaVersion: SchemaVersion, Canaries: copySpecs}, nil
}

func validateSpec(spec CanarySpec) error {
	if !identifierPattern.MatchString(spec.ID) || !spec.Class.valid() ||
		!identifierPattern.MatchString(spec.Extractor) || !identifierPattern.MatchString(spec.TargetRef) ||
		spec.TimeoutMS < 1 || spec.TimeoutMS > MaxTimeoutMS || len(spec.Capabilities) == 0 {
		return ErrInvalidSpec
	}
	if len(spec.Capabilities) > MaxCapabilities {
		return ErrResourceLimit
	}
	seen := make(map[string]bool, len(spec.Capabilities))
	for _, capability := range spec.Capabilities {
		if !identifierPattern.MatchString(capability) || seen[capability] {
			return ErrInvalidSpec
		}
		seen[capability] = true
	}
	switch spec.Class {
	case ClassPublic:
		if !spec.Secret.empty() || spec.Region != "" {
			return ErrInvalidSpec
		}
	case ClassCredential:
		if !spec.Secret.valid() || spec.Region != "" {
			return ErrInvalidSpec
		}
	case ClassRegion:
		if !spec.Secret.empty() || !validRegion(spec.Region) {
			return ErrInvalidSpec
		}
	}
	return nil
}

func validRegion(region string) bool {
	if len(region) != 2 {
		return false
	}
	for _, character := range region {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}
