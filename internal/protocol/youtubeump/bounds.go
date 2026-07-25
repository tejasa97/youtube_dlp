package youtubeump

const (
	// MaxResponseBytes mirrors the direct downloader ceiling.
	MaxResponseBytes = 8 << 30
	// MaxRoundBytes caps one SABR response body.
	MaxRoundBytes = 64 << 20
	// MaxParts bounds the number of UMP parts per response.
	MaxParts = 10_000
	// MaxActiveHeaders bounds concurrent in-flight media headers per response.
	MaxActiveHeaders = 8
	// MaxPartPayload bounds a single UMP part payload.
	MaxPartPayload = MaxRoundBytes
	// MaxMediaBytes bounds total reconstructed media per track.
	MaxMediaBytes = MaxResponseBytes
	// MaxRounds bounds SABR POST rounds for finite VOD.
	MaxRounds = 64
	// MaxProtobufFieldBytes bounds one protobuf bytes field during decode.
	MaxProtobufFieldBytes = 16 << 20
	// MaxProtobufFields bounds fields decoded from one message.
	MaxProtobufFields = 256
	// MaxPlaybackCookieBytes bounds one validated playback cookie payload.
	MaxPlaybackCookieBytes = 4096
	// MaxPolicyBackoffMs bounds NEXT_REQUEST_POLICY backoff_time_ms.
	MaxPolicyBackoffMs = 30_000
	// MaxSabrContexts bounds stored SABR context entries per downloader.
	MaxSabrContexts = 64
	// MaxSabrContextValueBytes bounds one SABR context value.
	MaxSabrContextValueBytes = 16 << 10
	// MaxSabrContextValueBytesTotal bounds cumulative stored context values.
	MaxSabrContextValueBytesTotal = 256 << 10
	// MaxSabrContextPolicyOps bounds start+stop+discard entries in one policy.
	MaxSabrContextPolicyOps = 192
	// MaxRedirectURLBytes bounds one SABR_REDIRECT url field.
	MaxRedirectURLBytes = 4096
	// MaxDirectiveRedirects bounds committed UMP SABR_REDIRECT hops.
	MaxDirectiveRedirects = 8
)
