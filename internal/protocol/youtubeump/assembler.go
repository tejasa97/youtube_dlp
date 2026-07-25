package youtubeump

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// BufferedRange records one acknowledged segment interval for later SABR requests.
type BufferedRange struct {
	FormatID          FormatID
	StartTimeMs       int64
	DurationMs        int64
	StartSegmentIndex int32
	EndSegmentIndex   int32
	TimeRange         TimeRange
}

type segmentDigest = [sha256.Size]byte

type segmentBuilder struct {
	header   MediaHeader
	data     []byte
	ended    bool
	selected bool
}

type trackAssembler struct {
	format             FormatID
	expectedDurationMs int64
	file               *os.File
	remaining          int64
	onCommit           func() error

	initWritten      bool
	initDigest       segmentDigest
	initLength       int64
	formatVerified   bool
	nextSequence     uint64
	hasSequence      bool
	cumulativeMs     int64
	bufferedRanges   []BufferedRange
	active           map[uint32]*segmentBuilder
	writtenSequences map[uint64]segmentDigest
	segmentLengths   map[uint64]int64
	totalWritten     int64
	endOfTrackDone   bool
}

func newTrackAssembler(format FormatID, expectedDurationMs int64, file *os.File, remaining int64) *trackAssembler {
	return &trackAssembler{
		format:             format,
		expectedDurationMs: expectedDurationMs,
		file:               file,
		remaining:          remaining,
		active:             make(map[uint32]*segmentBuilder),
		writtenSequences:   make(map[uint64]segmentDigest),
		segmentLengths:     make(map[uint64]int64),
	}
}

func (assembler *trackAssembler) consumePart(part Part) error {
	if isCriticalUnsupportedPart(part.Type) {
		return fmt.Errorf("%w: part type %d", ErrUnsupportedDirective, part.Type)
	}
	if !isHandledPart(part.Type) {
		return fmt.Errorf("%w: part type %d", ErrUnsupportedDirective, part.Type)
	}
	switch part.Type {
	case PartFormatInitializationMetadata:
		return assembler.consumeFormatInit(part.Payload)
	case PartMediaHeader:
		return assembler.consumeHeader(part.Payload)
	case PartMedia:
		return assembler.consumeMedia(part.Payload)
	case PartMediaEnd:
		return assembler.finalizeSegment(part.Payload)
	default:
		return nil
	}
}

func (assembler *trackAssembler) finishResponse() error {
	if len(assembler.active) > 0 {
		return fmt.Errorf("%w: active media headers at response end", ErrTruncatedStream)
	}
	return nil
}

func (assembler *trackAssembler) consumeFormatInit(payload []byte) error {
	meta, err := unmarshalFormatInitializationMetadata(payload)
	if err != nil {
		return err
	}
	if meta.FormatID.Itag != assembler.format.Itag {
		return nil
	}
	if err := formatIdentityConflicts(assembler.format, meta.FormatID); err != nil {
		return err
	}
	assembler.formatVerified = true
	return nil
}

func (assembler *trackAssembler) consumeHeader(payload []byte) error {
	header, err := unmarshalMediaHeader(payload)
	if err != nil {
		return err
	}
	if err := validateMediaHeader(header); err != nil {
		return err
	}
	if active := assembler.active[header.HeaderID]; active != nil {
		return fmt.Errorf("%w: duplicate active header id %d", ErrInvalidMediaState, header.HeaderID)
	}
	selected := assembler.isSelectedHeader(&header)
	if selected {
		if err := formatIdentityConflicts(assembler.format, headerRepresentationID(&header)); err != nil {
			return err
		}
	}
	if len(assembler.active) >= MaxActiveHeaders {
		return ErrTooManyActiveHeaders
	}
	if selected && !header.IsInitSeg {
		if _, replay := assembler.writtenSequences[header.SequenceNumber]; replay {
			// Server may resend a finalized segment; bytes are deduplicated at MEDIA_END.
		} else if assembler.hasSequence {
			if header.SequenceNumber != assembler.nextSequence {
				return fmt.Errorf("%w: unexpected sequence %d want %d", ErrInvalidMediaState, header.SequenceNumber, assembler.nextSequence)
			}
		} else if header.SequenceNumber != 0 {
			return fmt.Errorf("%w: unexpected sequence %d want 0", ErrInvalidMediaState, header.SequenceNumber)
		} else {
			assembler.hasSequence = true
			assembler.nextSequence = 0
		}
	}
	assembler.active[header.HeaderID] = &segmentBuilder{header: header, selected: selected}
	return nil
}

func (assembler *trackAssembler) consumeMedia(payload []byte) error {
	headerID, rest, err := mediaHeaderPrefix(payload)
	if err != nil {
		return err
	}
	segment := assembler.active[headerID]
	if segment == nil {
		return fmt.Errorf("%w: media without header %d", ErrInvalidMediaState, headerID)
	}
	if segment.ended {
		return fmt.Errorf("%w: media after end", ErrInvalidMediaState)
	}
	if int64(len(segment.data))+int64(len(rest)) > MaxMediaBytes {
		return ErrOversizedPart
	}
	segment.data = append(segment.data, rest...)
	return nil
}

func (assembler *trackAssembler) finalizeSegment(payload []byte) error {
	headerID, err := mediaHeaderOnly(payload)
	if err != nil {
		return err
	}
	segment := assembler.active[headerID]
	if segment == nil {
		return fmt.Errorf("%w: media end without header %d", ErrInvalidMediaState, headerID)
	}
	if segment.ended {
		return fmt.Errorf("%w: duplicate media end", ErrInvalidMediaState)
	}
	segment.ended = true
	header := segment.header
	if header.ContentLength > 0 && int64(len(segment.data)) != header.ContentLength {
		return fmt.Errorf("%w: content length mismatch", ErrInvalidMediaState)
	}
	if !segment.selected {
		delete(assembler.active, headerID)
		return nil
	}
	if !assembler.formatVerified {
		return fmt.Errorf("%w: media end before format initialization", ErrInvalidMediaState)
	}
	if !header.IsInitSeg {
		duration := header.effectiveDurationMs()
		if duration <= 0 {
			return fmt.Errorf("%w: missing media duration", ErrInvalidMediaState)
		}
	}
	digest := hashSegment(segment.data)
	if header.IsInitSeg {
		if assembler.initWritten {
			if digest == assembler.initDigest && int64(len(segment.data)) == assembler.initLength {
				delete(assembler.active, headerID)
				return nil
			}
			return fmt.Errorf("%w: init segment changed on replay", ErrInvalidMediaState)
		}
		if assembler.remaining < int64(len(segment.data)) {
			return ErrResponseTooLarge
		}
		if err := assembler.writeBytes(segment.data); err != nil {
			return err
		}
		assembler.initWritten = true
		assembler.initDigest = digest
		assembler.initLength = int64(len(segment.data))
		delete(assembler.active, headerID)
		return assembler.commitProgress()
	}
	if prior, ok := assembler.writtenSequences[header.SequenceNumber]; ok {
		if prior != digest {
			return fmt.Errorf("%w: replay sequence %d with different bytes", ErrInvalidMediaState, header.SequenceNumber)
		}
		delete(assembler.active, headerID)
		return nil
	}
	if assembler.remaining < int64(len(segment.data)) {
		return ErrResponseTooLarge
	}
	if !assembler.initWritten {
		return fmt.Errorf("%w: media before init segment", ErrInvalidMediaState)
	}
	if err := assembler.writeBytes(segment.data); err != nil {
		return err
	}
	duration := header.effectiveDurationMs()
	startSegmentIndex, err := int32SegmentIndex(header.SequenceNumber)
	if err != nil {
		return err
	}
	assembler.cumulativeMs += duration
	assembler.bufferedRanges = append(assembler.bufferedRanges, BufferedRange{
		FormatID:          assembler.format,
		StartTimeMs:       assembler.cumulativeMs - duration,
		DurationMs:        duration,
		StartSegmentIndex: startSegmentIndex,
		EndSegmentIndex:   startSegmentIndex,
		TimeRange:         header.TimeRange,
	})
	assembler.writtenSequences[header.SequenceNumber] = digest
	assembler.segmentLengths[header.SequenceNumber] = int64(len(segment.data))
	assembler.nextSequence = header.SequenceNumber + 1
	assembler.hasSequence = true
	delete(assembler.active, headerID)
	return assembler.commitProgress()
}

func (assembler *trackAssembler) commitProgress() error {
	if assembler.onCommit == nil {
		return nil
	}
	if assembler.file != nil {
		if err := assembler.file.Sync(); err != nil {
			return fmt.Errorf("%w: sync committed segment: %v", ErrDownloadFailed, err)
		}
	}
	return assembler.onCommit()
}

func (assembler *trackAssembler) writeBytes(data []byte) error {
	if len(data) == 0 {
		return nil
	}
	written, err := assembler.file.Write(data)
	if err != nil {
		return err
	}
	if written != len(data) {
		return io.ErrShortWrite
	}
	assembler.totalWritten += int64(written)
	assembler.remaining -= int64(written)
	return nil
}

func (assembler *trackAssembler) trackComplete() bool {
	if assembler.endOfTrackDone {
		return true
	}
	if assembler.expectedDurationMs <= 0 {
		return false
	}
	return assembler.initWritten &&
		len(assembler.writtenSequences) > 0 &&
		assembler.cumulativeMs >= assembler.expectedDurationMs
}

func (assembler *trackAssembler) canCompleteByEndOfTrack() bool {
	return assembler.formatVerified &&
		assembler.initWritten &&
		len(assembler.writtenSequences) > 0
}

func (assembler *trackAssembler) applyEndOfTrackCompletion() error {
	if !assembler.canCompleteByEndOfTrack() {
		return fmt.Errorf("%w: premature end of track", ErrInvalidMediaState)
	}
	assembler.endOfTrackDone = true
	return assembler.commitProgress()
}

func (assembler *trackAssembler) isSelectedHeader(header *MediaHeader) bool {
	itag := headerItag(header)
	if itag == 0 {
		return false
	}
	return itag == assembler.format.Itag
}

func validateMediaHeader(header MediaHeader) error {
	if header.HeaderID == 0 {
		return fmt.Errorf("%w: missing header id", ErrInvalidMediaState)
	}
	itag := headerItag(&header)
	if itag == 0 {
		return fmt.Errorf("%w: missing itag", ErrInvalidMediaState)
	}
	if itag < 0 {
		return ErrVarintOverflow
	}
	if header.ContentLength < 0 {
		return ErrVarintOverflow
	}
	if header.DurationMs < 0 {
		return ErrVarintOverflow
	}
	return nil
}

func mediaHeaderPrefix(payload []byte) (uint32, []byte, error) {
	value, size, err := readUMPVarint(payload)
	if err != nil {
		return 0, nil, err
	}
	headerID, err := uint32FromUint64(value)
	if err != nil {
		return 0, nil, err
	}
	return headerID, payload[size:], nil
}

func mediaHeaderOnly(payload []byte) (uint32, error) {
	value, size, err := readUMPVarint(payload)
	if err != nil {
		return 0, err
	}
	if size != len(payload) {
		return 0, fmt.Errorf("%w: trailing media end payload", ErrInvalidMediaState)
	}
	headerID, err := uint32FromUint64(value)
	if err != nil {
		return 0, err
	}
	return headerID, nil
}

func hashSegment(data []byte) segmentDigest {
	return sha256.Sum256(data)
}

func (assembler *trackAssembler) playbackState() (playerTimeMs int64, buffered []BufferedRange, selected bool) {
	return assembler.cumulativeMs, append([]BufferedRange(nil), assembler.bufferedRanges...), assembler.formatVerified
}

func decodePlaybackRequestBody(body []byte) (playerTimeMs int64, bufferedCount int, selected bool, err error) {
	reader := fieldReader{data: body}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		switch {
		case num == fAbrClientState && wireType == wireBytes:
			state := reader.bytes()
			if reader.err != nil {
				return 0, 0, false, reader.err
			}
			playerTimeMs, err = decodeClientStatePlayerTime(state)
			if err != nil {
				return 0, 0, false, err
			}
		case num == fAbrSelectedFormats && wireType == wireBytes:
			selected = true
			_ = reader.bytes()
		case num == fAbrBufferedRanges && wireType == wireBytes:
			_ = reader.bytes()
			bufferedCount++
		default:
			reader.skip(num, wireType)
		}
	}
	if reader.err != nil {
		return 0, 0, false, reader.err
	}
	return playerTimeMs, bufferedCount, selected, nil
}

func decodeClientStatePlayerTime(state []byte) (int64, error) {
	reader := fieldReader{data: state}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		if num == fAbrStatePlayerTimeMs && wireType == wireVarint {
			value, err := int64FromUint64(reader.varint())
			if reader.err != nil {
				return 0, reader.err
			}
			if err != nil {
				return 0, err
			}
			return value, nil
		}
		reader.skip(num, wireType)
	}
	if reader.err != nil {
		return 0, reader.err
	}
	return 0, nil
}

func bufferedRangeBodies(body []byte) ([][]byte, error) {
	reader := fieldReader{data: body}
	var ranges [][]byte
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		if num == fAbrBufferedRanges && wireType == wireBytes {
			ranges = append(ranges, bytes.Clone(reader.bytes()))
			continue
		}
		reader.skip(num, wireType)
	}
	if reader.err != nil {
		return nil, reader.err
	}
	return ranges, nil
}

func streamerContextBytes(body []byte) ([]byte, bool, error) {
	reader := fieldReader{data: body}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			break
		}
		if num == fAbrStreamerContext && wireType == wireBytes {
			return reader.bytes(), true, reader.err
		}
		reader.skip(num, wireType)
	}
	if reader.err != nil {
		return nil, false, reader.err
	}
	return nil, false, nil
}

func containsProtobufField(data []byte, field uint64) bool {
	reader := fieldReader{data: data}
	for {
		num, wireType, ok := reader.next()
		if !ok {
			return false
		}
		if num == field {
			return true
		}
		reader.skip(num, wireType)
		if reader.err != nil {
			return false
		}
	}
}
