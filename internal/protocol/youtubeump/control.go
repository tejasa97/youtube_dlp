package youtubeump

import (
	"bytes"
	"fmt"
	"time"
)

// Field numbers from LuanRT/googlevideo next_request_policy.proto and
// playback_cookie.proto at commit d2fa40d761034a286cf60ee033653307a1295b0c.
const (
	fPolicyBackoffTimeMs  uint64 = 4
	fPolicyPlaybackCookie uint64 = 7

	fPlaybackCookieField1   uint64 = 1
	fPlaybackCookieField2   uint64 = 2
	fPlaybackCookieFormatID uint64 = 7
	fPlaybackCookieAltFmtID uint64 = 8
)

// roundControl is committed only after consumeStream finishes without error.
// Media assembler state may advance during a failed response; that replay/dedup
// behavior is separate from transactional cookie/backoff/end/redirect/context control.
type roundControl struct {
	backoff      time.Duration
	cookie       []byte
	updateCookie bool
	redirectURL  string
	hasRedirect  bool
	contexts     *sabrContextState
}

type responseControl struct {
	hasPolicy     bool
	backoffMs     int64
	cookie        []byte
	hasCookie     bool
	sawEndOfTrack bool
	frozen        bool
	hasRedirect   bool
	redirectURL   string
	policyOps     int
	contexts      *sabrContextState
	hasSabrError  bool
	hasReload     bool
}

type streamConsumer struct {
	assembler *trackAssembler
	control   responseControl
	redirects *redirectTracker
}

func newStreamConsumer(assembler *trackAssembler) *streamConsumer {
	return newStreamConsumerState(assembler, newSabrContextState(), newRedirectTracker(""))
}

func newStreamConsumerState(assembler *trackAssembler, contexts *sabrContextState, redirects *redirectTracker) *streamConsumer {
	if contexts == nil {
		contexts = newSabrContextState()
	}
	return &streamConsumer{
		assembler: assembler,
		control:   responseControl{contexts: contexts.clone()},
		redirects: redirects,
	}
}

func (consumer *streamConsumer) consumePart(part Part) error {
	if consumer.control.frozen {
		return fmt.Errorf("%w: data after end of track", ErrInvalidMediaState)
	}
	switch part.Type {
	case PartNextRequestPolicy:
		return consumer.control.consumePolicy(part.Payload)
	case PartEndOfTrack:
		if err := consumer.control.consumeEndOfTrack(part.Payload, consumer.assembler); err != nil {
			return err
		}
		consumer.control.frozen = true
		return nil
	case PartSABRRedirect:
		return consumer.control.consumeRedirect(part.Payload)
	case PartSABRContextUpdate:
		return consumer.control.consumeContextUpdate(part.Payload)
	case PartSABRContextSendingPolicy:
		return consumer.control.consumeSendingPolicy(part.Payload)
	case PartSABRError:
		return consumer.control.consumeSabrError(part.Payload)
	case PartReloadPlayerResponse:
		return consumer.control.consumeReloadPlayer(part.Payload)
	default:
		return consumer.assembler.consumePart(part)
	}
}

func (consumer *streamConsumer) finish() (roundControl, error) {
	if err := consumer.assembler.finishResponse(); err != nil {
		return roundControl{}, err
	}
	ctrl, err := consumer.control.commit(consumer.redirects)
	if err != nil {
		return roundControl{}, err
	}
	if consumer.control.sawEndOfTrack {
		if err := consumer.assembler.applyEndOfTrackCompletion(); err != nil {
			return roundControl{}, err
		}
	}
	return ctrl, nil
}

func (control *responseControl) consumePolicy(payload []byte) error {
	if control.hasPolicy {
		return fmt.Errorf("%w: duplicate next request policy", ErrInvalidMediaState)
	}
	backoffMs, cookie, hasCookie, err := parseNextRequestPolicy(payload)
	if err != nil {
		return err
	}
	control.hasPolicy = true
	control.backoffMs = backoffMs
	if hasCookie {
		control.hasCookie = true
		control.cookie = cookie
	}
	return nil
}

func (control *responseControl) consumeEndOfTrack(payload []byte, assembler *trackAssembler) error {
	if control.sawEndOfTrack {
		return fmt.Errorf("%w: duplicate end of track", ErrInvalidMediaState)
	}
	if len(payload) != 0 {
		return fmt.Errorf("%w: end of track payload must be empty", ErrInvalidMediaState)
	}
	if len(assembler.active) > 0 {
		return fmt.Errorf("%w: end of track with active media headers", ErrInvalidMediaState)
	}
	if !assembler.canCompleteByEndOfTrack() {
		return fmt.Errorf("%w: premature end of track", ErrInvalidMediaState)
	}
	control.sawEndOfTrack = true
	return nil
}

func (control *responseControl) consumeRedirect(payload []byte) error {
	if control.hasRedirect {
		return fmt.Errorf("%w: duplicate sabr redirect", ErrInvalidMediaState)
	}
	directive, err := parseSabrRedirect(payload)
	if err != nil {
		return err
	}
	control.hasRedirect = true
	control.redirectURL = directive.URL
	return nil
}

func (control *responseControl) consumeContextUpdate(payload []byte) error {
	directive, err := parseSabrContextUpdate(payload)
	if err != nil {
		return err
	}
	return control.contexts.applyUpdate(directive)
}

func (control *responseControl) consumeSendingPolicy(payload []byte) error {
	directive, err := parseSabrContextSendingPolicy(payload, &control.policyOps)
	if err != nil {
		return err
	}
	return control.contexts.applySendingPolicy(directive)
}

func (control *responseControl) consumeSabrError(payload []byte) error {
	if control.hasSabrError || control.hasReload {
		return fmt.Errorf("%w: duplicate recovery directive", ErrInvalidMediaState)
	}
	directive, err := parseSabrError(payload)
	if err != nil {
		return err
	}
	control.hasSabrError = true
	// Returning a typed signal aborts finish()/commit so cookie/redirect/context
	// state from this response is never applied.
	return &SabrErrorSignal{Type: directive.Type, Code: directive.Code}
}

func (control *responseControl) consumeReloadPlayer(payload []byte) error {
	if control.hasSabrError || control.hasReload {
		return fmt.Errorf("%w: duplicate recovery directive", ErrInvalidMediaState)
	}
	directive, err := parseReloadPlayerResponse(payload)
	if err != nil {
		return err
	}
	control.hasReload = true
	return &ReloadPlayerSignal{token: directive.Token}
}

func (control *responseControl) commit(redirects *redirectTracker) (roundControl, error) {
	var result roundControl
	if control.hasPolicy {
		result.backoff = time.Duration(control.backoffMs) * time.Millisecond
		if control.hasCookie {
			result.updateCookie = true
			result.cookie = bytes.Clone(control.cookie)
		}
	}
	if control.hasRedirect {
		if err := redirects.validate(control.redirectURL); err != nil {
			return roundControl{}, err
		}
		result.hasRedirect = true
		result.redirectURL = control.redirectURL
	}
	result.contexts = control.contexts.clone()
	return result, nil
}

func parseNextRequestPolicy(payload []byte) (backoffMs int64, cookie []byte, hasCookie bool, err error) {
	var (
		seenBackoff bool
		seenCookie  bool
	)
	reader := fieldReader{data: payload}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fPolicyBackoffTimeMs:
			if wireType != wireVarint {
				return 0, nil, false, fmt.Errorf("%w: wrong wire type %d for backoff", ErrInvalidProtobuf, wireType)
			}
			if seenBackoff {
				return 0, nil, false, fmt.Errorf("%w: duplicate backoff field", ErrInvalidProtobuf)
			}
			seenBackoff = true
			raw := reader.varint()
			if reader.err != nil {
				return 0, nil, false, reader.err
			}
			value, convErr := int32FromUint64(raw)
			if convErr != nil {
				return 0, nil, false, convErr
			}
			if value < 0 {
				return 0, nil, false, fmt.Errorf("%w: negative backoff", ErrInvalidMediaState)
			}
			if int64(value) > MaxPolicyBackoffMs {
				return 0, nil, false, ErrExcessivePolicyBackoff
			}
			backoffMs = int64(value)
		case num == fPolicyPlaybackCookie:
			if wireType != wireBytes {
				return 0, nil, false, fmt.Errorf("%w: wrong wire type %d for playback cookie", ErrInvalidProtobuf, wireType)
			}
			if seenCookie {
				return 0, nil, false, fmt.Errorf("%w: duplicate playback cookie field", ErrInvalidProtobuf)
			}
			seenCookie = true
			raw := reader.bytes()
			if reader.err != nil {
				return 0, nil, false, reader.err
			}
			if err := validatePlaybackCookie(raw); err != nil {
				return 0, nil, false, err
			}
			cookie = bytes.Clone(raw)
			hasCookie = true
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return 0, nil, false, reader.err
	}
	return backoffMs, cookie, hasCookie, nil
}

func validatePlaybackCookie(data []byte) error {
	if len(data) == 0 {
		return fmt.Errorf("%w: empty playback cookie", ErrInvalidMediaState)
	}
	if len(data) > MaxPlaybackCookieBytes {
		return fmt.Errorf("%w: playback cookie exceeds bound", ErrInvalidMediaState)
	}
	var (
		seenField1 bool
		seenField2 bool
		seenField7 bool
		seenField8 bool
	)
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fPlaybackCookieField1:
			if wireType != wireVarint {
				return fmt.Errorf("%w: wrong wire type %d for playback cookie field 1", ErrInvalidProtobuf, wireType)
			}
			if seenField1 {
				return fmt.Errorf("%w: duplicate playback cookie field 1", ErrInvalidProtobuf)
			}
			seenField1 = true
			_ = reader.varint()
		case num == fPlaybackCookieField2:
			if wireType != wireVarint {
				return fmt.Errorf("%w: wrong wire type %d for playback cookie field 2", ErrInvalidProtobuf, wireType)
			}
			if seenField2 {
				return fmt.Errorf("%w: duplicate playback cookie field 2", ErrInvalidProtobuf)
			}
			seenField2 = true
			_ = reader.varint()
		case num == fPlaybackCookieFormatID:
			if wireType != wireBytes {
				return fmt.Errorf("%w: wrong wire type %d for playback cookie field 7", ErrInvalidProtobuf, wireType)
			}
			if seenField7 {
				return fmt.Errorf("%w: duplicate playback cookie field 7", ErrInvalidProtobuf)
			}
			seenField7 = true
			if err := validateEmbeddedFormatID(reader.bytes()); err != nil {
				return err
			}
		case num == fPlaybackCookieAltFmtID:
			if wireType != wireBytes {
				return fmt.Errorf("%w: wrong wire type %d for playback cookie field 8", ErrInvalidProtobuf, wireType)
			}
			if seenField8 {
				return fmt.Errorf("%w: duplicate playback cookie field 8", ErrInvalidProtobuf)
			}
			seenField8 = true
			if err := validateEmbeddedFormatID(reader.bytes()); err != nil {
				return err
			}
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return reader.err
	}
	return nil
}

func validateEmbeddedFormatID(data []byte) error {
	var (
		seenItag         bool
		seenLastModified bool
		seenXTags        bool
	)
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fFormatItag:
			if wireType != wireVarint {
				return fmt.Errorf("%w: wrong wire type %d for format itag", ErrInvalidProtobuf, wireType)
			}
			if seenItag {
				return fmt.Errorf("%w: duplicate format itag", ErrInvalidProtobuf)
			}
			seenItag = true
			_ = reader.varint()
		case num == fFormatLastModified:
			if wireType != wireVarint {
				return fmt.Errorf("%w: wrong wire type %d for format lastModified", ErrInvalidProtobuf, wireType)
			}
			if seenLastModified {
				return fmt.Errorf("%w: duplicate format lastModified", ErrInvalidProtobuf)
			}
			seenLastModified = true
			_ = reader.varint()
		case num == fFormatXTags:
			if wireType != wireBytes {
				return fmt.Errorf("%w: wrong wire type %d for format xtags", ErrInvalidProtobuf, wireType)
			}
			if seenXTags {
				return fmt.Errorf("%w: duplicate format xtags", ErrInvalidProtobuf)
			}
			seenXTags = true
			_ = reader.bytes()
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return reader.err
	}
	return nil
}

func playbackCookieFromStreamer(streamer []byte) ([]byte, bool, error) {
	reader := fieldReader{data: streamer}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		if num == fStreamerCtxPlaybackCookie && wireType == wireBytes {
			return reader.bytes(), true, reader.err
		}
		reader.skip(num, wireType)
	}
	if reader.err != nil {
		return nil, false, reader.err
	}
	return nil, false, nil
}
