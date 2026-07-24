package youtubeump

import (
	"fmt"
	"math"
	"math/bits"
)

// Field numbers verified against LuanRT/googlevideo commit d2fa40d761034a286cf60ee033653307a1295b0c.
const (
	fFormatItag         uint64 = 1
	fFormatLastModified uint64 = 2
	fFormatXTags        uint64 = 3

	fAbrStatePlayerTimeMs  uint64 = 28
	fAbrStateEnabledTracks uint64 = 40
	fAbrStateDrcEnabled    uint64 = 46
	fAbrStateAudioTrackID  uint64 = 69

	fClientInfoDeviceMake     uint64 = 12
	fClientInfoDeviceModel    uint64 = 13
	fClientInfoClientName     uint64 = 16
	fClientInfoClientVersion  uint64 = 17
	fClientInfoOSName         uint64 = 18
	fClientInfoOSVersion      uint64 = 19
	fClientInfoAcceptLanguage uint64 = 21

	fStreamerCtxClientInfo     uint64 = 1
	fStreamerCtxPOToken        uint64 = 2
	fStreamerCtxPlaybackCookie uint64 = 3
	fStreamerCtxSabrContexts   uint64 = 5

	fAbrClientState           uint64 = 1
	fAbrSelectedFormats       uint64 = 2
	fAbrBufferedRanges        uint64 = 3
	fAbrUstreamerConfig       uint64 = 5
	fAbrPreferredAudioFormats uint64 = 16
	fAbrPreferredVideoFormats uint64 = 17
	fAbrStreamerContext       uint64 = 19

	fBufferedFormatID          uint64 = 1
	fBufferedStartTimeMs       uint64 = 2
	fBufferedDurationMs        uint64 = 3
	fBufferedStartSegmentIndex uint64 = 4
	fBufferedEndSegmentIndex   uint64 = 5
	fBufferedTimeRange         uint64 = 6

	fFormatInitVideoID  uint64 = 1
	fFormatInitFormatID uint64 = 2

	fMediaHdrHeaderID      uint64 = 1
	fMediaHdrItag          uint64 = 3
	fMediaHdrIsInitSeg     uint64 = 8
	fMediaHdrSequenceNum   uint64 = 9
	fMediaHdrDurationMs    uint64 = 12
	fMediaHdrFormatID      uint64 = 13
	fMediaHdrContentLength uint64 = 14
	fMediaHdrTimeRange     uint64 = 15

	fTimeRangeStartTicks    uint64 = 1
	fTimeRangeDurationTicks uint64 = 2
	fTimeRangeTimescale     uint64 = 3
)

const (
	enabledTrackTypesAudioOnly = 1
	enabledTrackTypesVideoOnly = 2
)

// FormatID identifies one adaptive encoding.
type FormatID struct {
	Itag         int32
	LastModified uint64
	XTags        string
}

func (format FormatID) marshal() []byte {
	var buf []byte
	buf = appendProtobufVarint(buf, fFormatItag, uint64(format.Itag))
	if format.LastModified != 0 {
		buf = appendProtobufVarint(buf, fFormatLastModified, format.LastModified)
	}
	if format.XTags != "" {
		buf = appendProtobufBytes(buf, fFormatXTags, []byte(format.XTags))
	}
	return buf
}

// ClientInfo is the wire identity echoed in StreamerContext.
type ClientInfo struct {
	ClientName     int32
	ClientVersion  string
	OSName         string
	OSVersion      string
	DeviceMake     string
	DeviceModel    string
	AcceptLanguage string
}

func (info ClientInfo) marshal() []byte {
	var buf []byte
	if info.DeviceMake != "" {
		buf = appendProtobufBytes(buf, fClientInfoDeviceMake, []byte(info.DeviceMake))
	}
	if info.DeviceModel != "" {
		buf = appendProtobufBytes(buf, fClientInfoDeviceModel, []byte(info.DeviceModel))
	}
	if info.ClientName != 0 {
		buf = appendProtobufVarint(buf, fClientInfoClientName, uint64(info.ClientName))
	}
	if info.ClientVersion != "" {
		buf = appendProtobufBytes(buf, fClientInfoClientVersion, []byte(info.ClientVersion))
	}
	if info.OSName != "" {
		buf = appendProtobufBytes(buf, fClientInfoOSName, []byte(info.OSName))
	}
	if info.OSVersion != "" {
		buf = appendProtobufBytes(buf, fClientInfoOSVersion, []byte(info.OSVersion))
	}
	if info.AcceptLanguage != "" {
		buf = appendProtobufBytes(buf, fClientInfoAcceptLanguage, []byte(info.AcceptLanguage))
	}
	return buf
}

// MediaHeader describes one media segment in a UMP response.
type MediaHeader struct {
	HeaderID       uint32
	Itag           int32
	IsInitSeg      bool
	SequenceNumber uint64
	DurationMs     int64
	FormatID       FormatID
	ContentLength  int64
	TimeRange      TimeRange
}

type TimeRange struct {
	StartTicks    int64
	DurationTicks int64
	Timescale     int32
}

func (header MediaHeader) effectiveDurationMs() int64 {
	if header.DurationMs > 0 {
		return header.DurationMs
	}
	timescale := int64(header.TimeRange.Timescale)
	if header.TimeRange.DurationTicks <= 0 || timescale <= 0 {
		return 0
	}
	duration, ok := ceilMillisFromTicks(header.TimeRange.DurationTicks, timescale)
	if !ok {
		return 0
	}
	return duration
}

func ceilMillisFromTicks(ticks, timescale int64) (int64, bool) {
	if ticks <= 0 || timescale <= 0 {
		return 0, false
	}
	quotient := ticks / timescale
	remainder := ticks % timescale
	base, ok := mulInt64(quotient, 1000)
	if !ok {
		return 0, false
	}
	if remainder == 0 {
		return base, true
	}
	fraction, ok := ceilMulDiv(remainder, 1000, timescale)
	if !ok {
		return 0, false
	}
	return addInt64(base, fraction)
}

func mulInt64(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}

func addInt64(a, b int64) (int64, bool) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, false
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, false
	}
	return a + b, true
}

func ceilMulDiv(a, b, divisor int64) (int64, bool) {
	if a <= 0 || b <= 0 || divisor <= 0 {
		return 0, false
	}
	hi, lo := bits.Mul64(uint64(a), uint64(b))
	lo, carry := bits.Add64(lo, uint64(divisor)-1, 0)
	hi, _ = bits.Add64(hi, 0, carry)
	quotient, _ := bits.Div64(hi, lo, uint64(divisor))
	if quotient > uint64(math.MaxInt64) {
		return 0, false
	}
	return int64(quotient), true
}

func unmarshalFormatID(data []byte) (FormatID, error) {
	var format FormatID
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fFormatItag && wireType == wireVarint:
			itag, err := int32FromUint64(reader.varint())
			if reader.err != nil {
				return FormatID{}, reader.err
			}
			if err != nil {
				return FormatID{}, err
			}
			format.Itag = itag
		case num == fFormatLastModified && wireType == wireVarint:
			format.LastModified = reader.varint()
		case num == fFormatXTags && wireType == wireBytes:
			format.XTags = reader.string()
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return FormatID{}, reader.err
	}
	return format, nil
}

func unmarshalMediaHeader(data []byte) (MediaHeader, error) {
	var header MediaHeader
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fMediaHdrHeaderID && wireType == wireVarint:
			header.HeaderID = reader.uint32()
		case num == fMediaHdrItag && wireType == wireVarint:
			itag, err := int32FromUint64(reader.varint())
			if reader.err != nil {
				return MediaHeader{}, reader.err
			}
			if err != nil {
				return MediaHeader{}, err
			}
			header.Itag = itag
		case num == fMediaHdrIsInitSeg && wireType == wireVarint:
			header.IsInitSeg = reader.varint() != 0
		case num == fMediaHdrSequenceNum && wireType == wireVarint:
			header.SequenceNumber = reader.varint()
		case num == fMediaHdrDurationMs && wireType == wireVarint:
			duration, err := int64FromUint64(reader.varint())
			if reader.err != nil {
				return MediaHeader{}, reader.err
			}
			if err != nil {
				return MediaHeader{}, err
			}
			header.DurationMs = duration
		case num == fMediaHdrFormatID && wireType == wireBytes:
			format, err := unmarshalFormatID(reader.bytes())
			if err != nil {
				return MediaHeader{}, err
			}
			header.FormatID = format
		case num == fMediaHdrContentLength && wireType == wireVarint:
			length, err := int64FromUint64(reader.varint())
			if reader.err != nil {
				return MediaHeader{}, reader.err
			}
			if err != nil {
				return MediaHeader{}, err
			}
			header.ContentLength = length
		case num == fMediaHdrTimeRange && wireType == wireBytes:
			tr, err := unmarshalTimeRange(reader.bytes())
			if err != nil {
				return MediaHeader{}, err
			}
			header.TimeRange = tr
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return MediaHeader{}, reader.err
	}
	return header, nil
}

func unmarshalTimeRange(data []byte) (TimeRange, error) {
	var tr TimeRange
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fTimeRangeStartTicks && wireType == wireVarint:
			ticks, err := int64FromUint64(reader.varint())
			if reader.err != nil {
				return TimeRange{}, reader.err
			}
			if err != nil {
				return TimeRange{}, err
			}
			tr.StartTicks = ticks
		case num == fTimeRangeDurationTicks && wireType == wireVarint:
			ticks, err := int64FromUint64(reader.varint())
			if reader.err != nil {
				return TimeRange{}, reader.err
			}
			if err != nil {
				return TimeRange{}, err
			}
			tr.DurationTicks = ticks
		case num == fTimeRangeTimescale && wireType == wireVarint:
			scale, err := int32FromUint64(reader.varint())
			if reader.err != nil {
				return TimeRange{}, reader.err
			}
			if err != nil {
				return TimeRange{}, err
			}
			tr.Timescale = scale
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return TimeRange{}, reader.err
	}
	return tr, nil
}

func headerItag(header *MediaHeader) int32 {
	if header.FormatID.Itag != 0 {
		return header.FormatID.Itag
	}
	return header.Itag
}

type FormatInitializationMetadata struct {
	VideoID  string
	FormatID FormatID
}

func unmarshalFormatInitializationMetadata(data []byte) (FormatInitializationMetadata, error) {
	var meta FormatInitializationMetadata
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fFormatInitVideoID && wireType == wireBytes:
			meta.VideoID = reader.string()
		case num == fFormatInitFormatID && wireType == wireBytes:
			format, err := unmarshalFormatID(reader.bytes())
			if err != nil {
				return FormatInitializationMetadata{}, err
			}
			meta.FormatID = format
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return FormatInitializationMetadata{}, reader.err
	}
	return meta, nil
}

func (br BufferedRange) marshal() ([]byte, error) {
	if err := validateBufferedRange(br); err != nil {
		return nil, err
	}
	var buf []byte
	buf = appendProtobufBytes(buf, fBufferedFormatID, br.FormatID.marshal())
	buf = appendProtobufVarint(buf, fBufferedStartTimeMs, uint64(br.StartTimeMs))
	buf = appendProtobufVarint(buf, fBufferedDurationMs, uint64(br.DurationMs))
	buf = appendProtobufVarint(buf, fBufferedStartSegmentIndex, uint64(br.StartSegmentIndex))
	buf = appendProtobufVarint(buf, fBufferedEndSegmentIndex, uint64(br.EndSegmentIndex))
	if br.TimeRange.Timescale != 0 || br.TimeRange.DurationTicks != 0 || br.TimeRange.StartTicks != 0 {
		timeRange, err := br.TimeRange.marshal()
		if err != nil {
			return nil, err
		}
		buf = appendProtobufBytes(buf, fBufferedTimeRange, timeRange)
	}
	return buf, nil
}

func (tr TimeRange) marshal() ([]byte, error) {
	if err := validateTimeRangeWire(tr); err != nil {
		return nil, err
	}
	var buf []byte
	if tr.StartTicks != 0 {
		buf = appendProtobufVarint(buf, fTimeRangeStartTicks, uint64(tr.StartTicks))
	}
	if tr.DurationTicks != 0 {
		buf = appendProtobufVarint(buf, fTimeRangeDurationTicks, uint64(tr.DurationTicks))
	}
	if tr.Timescale != 0 {
		buf = appendProtobufVarint(buf, fTimeRangeTimescale, uint64(tr.Timescale))
	}
	return buf, nil
}

type playbackRequest struct {
	Format          FormatID
	TrackKind       string
	UstreamerConfig []byte
	ClientInfo      ClientInfo
	POToken         []byte
	BufferedRanges  []BufferedRange
	RequestNumber   int
	PlayerTimeMs    int64
	SelectedFormat  bool
	DrcEnabled      bool
	AudioTrackID    string
}

func (request playbackRequest) marshal() ([]byte, error) {
	if len(request.UstreamerConfig) == 0 {
		return nil, fmt.Errorf("%w: missing ustreamer config", ErrMissingConfig)
	}
	if err := validateFormatIDWire(request.Format); err != nil {
		return nil, err
	}
	if err := validateClientInfoWire(request.ClientInfo); err != nil {
		return nil, err
	}
	if request.PlayerTimeMs < 0 {
		return nil, fmt.Errorf("%w: negative player time", ErrInvalidMediaState)
	}
	if request.RequestNumber < 0 {
		return nil, fmt.Errorf("%w: negative request number", ErrInvalidMediaState)
	}
	enabledTracks := enabledTrackTypesAudioOnly
	if request.TrackKind == "video" {
		enabledTracks = enabledTrackTypesVideoOnly
	}
	var clientState []byte
	clientState = appendProtobufVarint(clientState, fAbrStatePlayerTimeMs, uint64(request.PlayerTimeMs))
	clientState = appendProtobufVarint(clientState, fAbrStateEnabledTracks, uint64(enabledTracks))
	if request.DrcEnabled {
		clientState = appendProtobufVarint(clientState, fAbrStateDrcEnabled, 1)
	}
	if request.AudioTrackID != "" {
		clientState = appendProtobufBytes(clientState, fAbrStateAudioTrackID, []byte(request.AudioTrackID))
	}

	var streamer []byte
	streamer = appendProtobufBytes(streamer, fStreamerCtxClientInfo, request.ClientInfo.marshal())
	if len(request.POToken) > 0 {
		streamer = appendProtobufBytes(streamer, fStreamerCtxPOToken, request.POToken)
	}

	var body []byte
	body = appendProtobufBytes(body, fAbrClientState, clientState)
	if request.SelectedFormat {
		body = appendProtobufBytes(body, fAbrSelectedFormats, request.Format.marshal())
	}
	for _, buffered := range request.BufferedRanges {
		encoded, err := buffered.marshal()
		if err != nil {
			return nil, err
		}
		body = appendProtobufBytes(body, fAbrBufferedRanges, encoded)
	}
	body = appendProtobufBytes(body, fAbrUstreamerConfig, request.UstreamerConfig)
	if request.TrackKind == "video" {
		body = appendProtobufBytes(body, fAbrPreferredVideoFormats, request.Format.marshal())
	} else {
		body = appendProtobufBytes(body, fAbrPreferredAudioFormats, request.Format.marshal())
	}
	body = appendProtobufBytes(body, fAbrStreamerContext, streamer)
	return body, nil
}

func marshalMediaHeader(header MediaHeader) []byte {
	var buf []byte
	buf = appendProtobufVarint(buf, fMediaHdrHeaderID, uint64(header.HeaderID))
	if header.Itag != 0 {
		buf = appendProtobufVarint(buf, fMediaHdrItag, uint64(header.Itag))
	}
	if header.IsInitSeg {
		buf = appendProtobufVarint(buf, fMediaHdrIsInitSeg, 1)
	}
	if header.SequenceNumber != 0 {
		buf = appendProtobufVarint(buf, fMediaHdrSequenceNum, header.SequenceNumber)
	}
	if header.DurationMs != 0 {
		buf = appendProtobufVarint(buf, fMediaHdrDurationMs, uint64(header.DurationMs))
	}
	if header.FormatID.Itag != 0 || header.FormatID.LastModified != 0 || header.FormatID.XTags != "" {
		buf = appendProtobufBytes(buf, fMediaHdrFormatID, header.FormatID.marshal())
	}
	if header.ContentLength != 0 {
		buf = appendProtobufVarint(buf, fMediaHdrContentLength, uint64(header.ContentLength))
	}
	return buf
}
