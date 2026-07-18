package ytdlp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"time"

	internalupdate "github.com/ytdlp-go/ytdlp/internal/update"
)

// UpdateChannel identifies an independently selected release stream.
type UpdateChannel = internalupdate.Channel

const (
	UpdateChannelStable  = internalupdate.ChannelStable
	UpdateChannelBeta    = internalupdate.ChannelBeta
	UpdateChannelNightly = internalupdate.ChannelNightly
)

type UpdatePlatform = internalupdate.Platform
type UpdateTarget = internalupdate.Target
type UpdateMetadata = internalupdate.Metadata
type UpdateInstalled = internalupdate.Installed
type UpdateState = internalupdate.State

// UpdateTrust is an explicit, caller-owned trust snapshot. Key identifiers
// must be derived with UpdateKeyID; no trust-on-first-use behavior exists.
type UpdateTrust struct {
	Keys      map[string]ed25519.PublicKey
	Threshold int
	Role      string
	Product   string
	Channels  []UpdateChannel
	Platforms []UpdatePlatform
}

// UpdateHealthChecker validates an activated artifact before an update is
// committed. Implementations must honor cancellation and avoid shell parsing.
type UpdateHealthChecker interface {
	Check(context.Context, string, UpdateTarget) error
}

type UpdateHealthCheckFunc func(context.Context, string, UpdateTarget) error

func (function UpdateHealthCheckFunc) Check(ctx context.Context, path string, target UpdateTarget) error {
	return function(ctx, path, target)
}

// CommandUpdateHealthChecker executes the signed artifact directly and
// requires exact, bounded version output.
type CommandUpdateHealthChecker = internalupdate.CommandHealthChecker

type UpdateOptions struct {
	Trust           UpdateTrust
	Product         string
	Channel         UpdateChannel
	GOOS            string
	GOARCH          string
	Clock           func() time.Time
	Health          UpdateHealthChecker
	MaxArtifactSize int64
	LockPoll        time.Duration
	StaleLockAfter  time.Duration
}

// Updater manages immutable signed releases below a private local root.
// Transport is intentionally outside this API: Apply accepts metadata and
// artifact bytes that the caller has already obtained.
type Updater struct {
	manager *internalupdate.Manager
}

func OpenUpdater(ctx context.Context, root string, options UpdateOptions) (*Updater, error) {
	manager, err := internalupdate.Open(ctx, root, internalupdate.Options{
		Trust: internalupdate.Root{
			Keys: options.Trust.Keys, Threshold: options.Trust.Threshold,
			Role: options.Trust.Role, Product: options.Trust.Product,
			Channels: options.Trust.Channels, Platforms: options.Trust.Platforms,
		},
		Product: options.Product, Channel: options.Channel, GOOS: options.GOOS, GOARCH: options.GOARCH,
		Clock: options.Clock, Health: options.Health, MaxArtifactSize: options.MaxArtifactSize,
		LockPoll: options.LockPoll, StaleLockAfter: options.StaleLockAfter,
	})
	if err != nil {
		return nil, categorizeUpdate("open updater", err)
	}
	return &Updater{manager: manager}, nil
}

func (updater *Updater) Snapshot(ctx context.Context) (UpdateState, error) {
	if updater == nil || updater.manager == nil {
		return UpdateState{}, &Error{Category: ErrorInvalidInput, Op: "snapshot updater", Err: errors.New("nil updater")}
	}
	state, err := updater.manager.Snapshot(ctx)
	return state, categorizeUpdate("snapshot updater", err)
}

func (updater *Updater) ActivePath(ctx context.Context) (string, error) {
	if updater == nil || updater.manager == nil {
		return "", &Error{Category: ErrorInvalidInput, Op: "resolve active update", Err: errors.New("nil updater")}
	}
	path, err := updater.manager.ActivePath(ctx)
	return path, categorizeUpdate("resolve active update", err)
}

func (updater *Updater) Apply(ctx context.Context, envelope []byte, artifact io.Reader) (UpdateState, error) {
	if updater == nil || updater.manager == nil {
		return UpdateState{}, &Error{Category: ErrorInvalidInput, Op: "apply update", Err: errors.New("nil updater")}
	}
	state, err := updater.manager.Apply(ctx, envelope, artifact)
	return state, categorizeUpdate("apply update", err)
}

func (updater *Updater) Rollback(ctx context.Context) (UpdateState, error) {
	if updater == nil || updater.manager == nil {
		return UpdateState{}, &Error{Category: ErrorInvalidInput, Op: "roll back update", Err: errors.New("nil updater")}
	}
	state, err := updater.manager.Rollback(ctx)
	return state, categorizeUpdate("roll back update", err)
}

func UpdateKeyID(key ed25519.PublicKey) string { return internalupdate.KeyID(key) }

func VerifyUpdateMetadata(envelope []byte, trust UpdateTrust) (UpdateMetadata, error) {
	metadata, err := internalupdate.Verify(envelope, internalupdate.Root{
		Keys: trust.Keys, Threshold: trust.Threshold, Role: trust.Role,
		Product: trust.Product, Channels: trust.Channels, Platforms: trust.Platforms,
	})
	return metadata, categorizeUpdate("verify update metadata", err)
}

func categorizeUpdate(op string, err error) error {
	if err == nil {
		return nil
	}
	category := ErrorInternal
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		category = ErrorCancelled
	case errors.Is(err, internalupdate.ErrWrongChannel), errors.Is(err, internalupdate.ErrWrongPlatform):
		category = ErrorUnsupported
	case errors.Is(err, internalupdate.ErrInvalidMetadata), errors.Is(err, internalupdate.ErrTooLarge), errors.Is(err, internalupdate.ErrNoRollback):
		category = ErrorInvalidInput
	case errors.Is(err, internalupdate.ErrSignature), errors.Is(err, internalupdate.ErrExpired),
		errors.Is(err, internalupdate.ErrFreeze), errors.Is(err, internalupdate.ErrDowngrade),
		errors.Is(err, internalupdate.ErrUnsafePath), errors.Is(err, internalupdate.ErrHash):
		category = ErrorSecurity
	}
	return &Error{Category: category, Op: op, Err: err}
}
