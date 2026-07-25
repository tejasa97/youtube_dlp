package youtubeump

import (
	"bytes"
	"context"
	"fmt"
	"time"
)

// ReloadRequest is the attributable RELOAD_PLAYER_RESPONSE callback input.
// The reload token is never logged by the downloader.
type ReloadRequest struct {
	VideoID     string
	ClientName  int32
	ClientVer   string
	VisitorData string
	TrackKind   TrackKind
	Format      FormatID
	Token       string
}

// RefreshMaterial is freshly extracted SABR inventory for the same identity.
type RefreshMaterial struct {
	ServerURL       string
	UstreamerConfig []byte
	POToken         []byte
	Format          FormatID
	ClientInfo      ClientInfo
	VisitorData     string
	DurationSec     int64
	UserAgent       string
	DrcEnabled      bool
	AudioTrackID    string
	VideoID         string
}

// ReloadFunc re-extracts player inventory after RELOAD_PLAYER_RESPONSE.
type ReloadFunc func(context.Context, ReloadRequest) (RefreshMaterial, error)

// RefreshFunc re-extracts signed SABR material for the current identity
// (process-restart / expiry refresh paths).
type RefreshFunc func(context.Context) (RefreshMaterial, error)

// POTokenSource resolves a mid-session PO token without exposing it to events.
type POTokenSource func(context.Context) ([]byte, error)

func (material RefreshMaterial) Validate(config Config) error {
	return material.validate(config)
}

func (material RefreshMaterial) validate(config Config) error {
	if material.VideoID == "" {
		material.VideoID = config.VideoID
	}
	if material.VideoID != config.VideoID {
		return fmt.Errorf("%w: video id mismatch", ErrRefreshRejected)
	}
	if material.DurationSec != 0 && material.DurationSec != config.DurationSec {
		return fmt.Errorf("%w: duration mismatch", ErrRefreshRejected)
	}
	if material.ClientInfo.ClientName != 0 && material.ClientInfo.ClientName != config.ClientInfo.ClientName {
		return fmt.Errorf("%w: client identity mismatch", ErrRefreshRejected)
	}
	if material.ClientInfo.ClientVersion != "" && config.ClientInfo.ClientVersion != "" &&
		material.ClientInfo.ClientVersion != config.ClientInfo.ClientVersion {
		return fmt.Errorf("%w: client version mismatch", ErrRefreshRejected)
	}
	if config.VisitorData != "" && material.VisitorData != "" && material.VisitorData != config.VisitorData {
		return fmt.Errorf("%w: visitor binding mismatch", ErrRefreshRejected)
	}
	if material.Format.Itag != 0 {
		if err := formatIdentityConflicts(config.Format, material.Format); err != nil {
			return fmt.Errorf("%w: %v", ErrRefreshRejected, err)
		}
		if material.Format.Itag != config.Format.Itag {
			return fmt.Errorf("%w: itag mismatch", ErrRefreshRejected)
		}
	}
	if material.DrcEnabled != config.DrcEnabled {
		return fmt.Errorf("%w: drc mismatch", ErrRefreshRejected)
	}
	if material.AudioTrackID != "" && config.AudioTrackID != "" && material.AudioTrackID != config.AudioTrackID {
		return fmt.Errorf("%w: audio track mismatch", ErrRefreshRejected)
	}
	if material.ServerURL == "" || len(material.UstreamerConfig) == 0 {
		return fmt.Errorf("%w: incomplete refresh material", ErrRefreshRejected)
	}
	if _, err := ValidateSABRURL(material.ServerURL); err != nil {
		return fmt.Errorf("%w: %v", ErrRefreshRejected, err)
	}
	return nil
}

func applyRefreshMaterial(config *Config, material RefreshMaterial, redirects **redirectTracker) error {
	if config == nil {
		return ErrMissingConfig
	}
	if err := material.validate(*config); err != nil {
		return err
	}
	config.ServerURL = material.ServerURL
	config.UstreamerConfig = bytes.Clone(material.UstreamerConfig)
	if material.POToken != nil {
		config.POToken = bytes.Clone(material.POToken)
	}
	if material.Format.Itag != 0 {
		merged := config.Format
		if material.Format.LastModified != 0 {
			merged.LastModified = material.Format.LastModified
		}
		if material.Format.XTags != "" {
			merged.XTags = material.Format.XTags
		}
		config.Format = merged
	}
	if material.ClientInfo.ClientName != 0 {
		config.ClientInfo = material.ClientInfo
	}
	if material.VisitorData != "" {
		config.VisitorData = material.VisitorData
	}
	if material.UserAgent != "" {
		config.UserAgent = material.UserAgent
	}
	if redirects != nil {
		tracker := newRedirectTracker(config.ServerURL)
		*redirects = tracker
	}
	return nil
}

func recoveryBackoff(config Config, attempt int) time.Duration {
	return retryDelay(config, attempt)
}
