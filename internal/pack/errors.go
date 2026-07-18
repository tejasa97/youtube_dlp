// Package pack implements deterministic, signed plugin-pack verification and
// atomic installation without relying on a language runtime.
package pack

import "errors"

var (
	ErrInvalidManifest    = errors.New("invalid plugin pack manifest")
	ErrInvalidArchive     = errors.New("invalid plugin pack archive")
	ErrResourceLimit      = errors.New("plugin pack resource limit exceeded")
	ErrUnsafePath         = errors.New("unsafe plugin pack path")
	ErrUntrustedPublisher = errors.New("untrusted plugin pack publisher")
	ErrSignature          = errors.New("invalid plugin pack signature")
	ErrRevoked            = errors.New("plugin pack is revoked")
	ErrInvalidRevocations = errors.New("invalid plugin pack revocation metadata")
	ErrExpired            = errors.New("plugin pack is expired")
	ErrNotYetValid        = errors.New("plugin pack is not yet valid")
	ErrDowngrade          = errors.New("plugin pack downgrade rejected")
	ErrIncompatibleHost   = errors.New("plugin pack requires a newer host")
	ErrPermissionReview   = errors.New("plugin pack permission increase requires review")
	ErrAlreadyInstalled   = errors.New("plugin pack version is already installed")
	ErrLocked             = errors.New("plugin pack installation is locked")
	ErrCorruptInstall     = errors.New("installed plugin pack is corrupt")
	ErrPlatformSecurity   = errors.New("plugin pack secure installation is unsupported on this platform")
	ErrIO                 = errors.New("plugin pack I/O failure")
)
