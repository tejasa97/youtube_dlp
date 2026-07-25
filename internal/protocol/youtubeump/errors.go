package youtubeump

import "errors"

var (
	ErrMalformedFraming       = errors.New("malformed UMP framing")
	ErrTruncatedStream        = errors.New("truncated UMP stream")
	ErrNonCanonicalVarint     = errors.New("non-canonical UMP varint")
	ErrVarintOverflow         = errors.New("protobuf varint overflow")
	ErrUnsafeDestination      = errors.New("destination escapes output root")
	ErrDestinationExists      = errors.New("destination already exists")
	ErrOversizedPart          = errors.New("UMP part exceeds size bound")
	ErrTooManyParts           = errors.New("UMP response exceeds part count bound")
	ErrTooManyActiveHeaders   = errors.New("UMP response exceeds active header bound")
	ErrUnsupportedDirective   = errors.New("unsupported UMP directive")
	ErrInvalidMediaState      = errors.New("invalid UMP media state")
	ErrInvalidProtobuf        = errors.New("invalid protobuf field")
	ErrUnsupportedURL         = errors.New("unsupported SABR URL")
	ErrInvalidContentType     = errors.New("invalid SABR response content type")
	ErrResponseTooLarge       = errors.New("SABR response exceeds byte bound")
	ErrLiveUnsupported        = errors.New("live SABR playback is unsupported")
	ErrResumeUnsupported      = errors.New("SABR resume is unsupported")
	ErrCheckpointInvalid      = errors.New("invalid SABR checkpoint")
	ErrMissingConfig          = errors.New("missing SABR playback configuration")
	ErrDownloadFailed         = errors.New("SABR download failed")
	ErrRedirect               = errors.New("SABR HTTP redirect rejected")
	ErrUnsafeRedirect         = errors.New("SABR redirect URL rejected")
	ErrRedirectLoop           = errors.New("SABR redirect loop detected")
	ErrRedirectBudget         = errors.New("SABR redirect budget exhausted")
	ErrInvalidContextState    = errors.New("invalid SABR context state")
	ErrEventSink              = errors.New("SABR event sink failed")
	ErrTooManyAttempts        = errors.New("SABR retry attempts exceed limit")
	ErrRoundsExhausted        = errors.New("SABR round limit exhausted")
	ErrExcessivePolicyBackoff = errors.New("SABR policy backoff exceeds bound")
	ErrSabrError              = errors.New("SABR error directive")
	ErrReloadPlayerResponse   = errors.New("SABR reload player response")
	ErrSabrRecoveryBudget     = errors.New("SABR recovery budget exhausted")
	ErrReloadBudget           = errors.New("SABR reload budget exhausted")
	ErrRefreshBudget          = errors.New("SABR refresh budget exhausted")
	ErrReloadRejected         = errors.New("SABR reload inventory rejected")
	ErrRefreshRejected        = errors.New("SABR signed refresh rejected")
)

// SabrErrorSignal is a typed, retryable SABR_ERROR decision. Diagnostics never
// include remote body bytes; type/code are protocol fields only.
type SabrErrorSignal struct {
	Type string
	Code int32
}

func (err *SabrErrorSignal) Error() string {
	if err == nil {
		return ErrSabrError.Error()
	}
	return ErrSabrError.Error()
}

func (err *SabrErrorSignal) Unwrap() error { return ErrSabrError }

// ReloadPlayerSignal carries a redacted reload request. The reload token is
// available only through ReloadToken and never via Error/formatting.
type ReloadPlayerSignal struct {
	token string
}

func (err *ReloadPlayerSignal) Error() string { return ErrReloadPlayerResponse.Error() }
func (err *ReloadPlayerSignal) Unwrap() error { return ErrReloadPlayerResponse }
func (err *ReloadPlayerSignal) ReloadToken() string {
	if err == nil {
		return ""
	}
	return err.token
}
func (err *ReloadPlayerSignal) String() string   { return "[redacted SABR reload signal]" }
func (err *ReloadPlayerSignal) GoString() string { return "youtubeump.ReloadPlayerSignal{[redacted]}" }
