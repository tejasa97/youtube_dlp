package youtubeump

func encodePart(partType int, payload []byte) []byte {
	typeBytes, _ := encodeUMPVarint(uint64(partType))
	sizeBytes, _ := encodeUMPVarint(uint64(len(payload)))
	return append(append(typeBytes, sizeBytes...), payload...)
}

func encodeFormatInitialization(itag int32) []byte {
	return appendProtobufBytes(nil, fFormatInitFormatID, FormatID{Itag: itag}.marshal())
}

type testSegment struct {
	headerID uint32
	sequence uint64
	duration int64
	init     bool
	payload  []byte
}

type multiplexSegment struct {
	itag int32
	testSegment
}

func buildTestUMP(itag int32, segments ...testSegment) []byte {
	parts := make([]multiplexSegment, len(segments))
	for index, segment := range segments {
		parts[index] = multiplexSegment{itag: itag, testSegment: segment}
	}
	return buildMultiplexUMP(itag, parts...)
}

func buildMultiplexUMP(selectedItag int32, segments ...multiplexSegment) []byte {
	otherItag := int32(140)
	if selectedItag == 140 {
		otherItag = 137
	}
	var body []byte
	body = append(body, encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(selectedItag))...)
	body = append(body, encodePart(PartFormatInitializationMetadata, encodeFormatInitialization(otherItag))...)
	for _, segment := range segments {
		header := marshalMediaHeader(MediaHeader{
			HeaderID:       segment.headerID,
			Itag:           segment.itag,
			IsInitSeg:      segment.init,
			SequenceNumber: segment.sequence,
			DurationMs:     segment.duration,
			ContentLength:  int64(len(segment.payload)),
		})
		body = append(body, encodePart(PartMediaHeader, header)...)
		body = append(body, encodePart(PartMedia, append(mustTestVarint(uint64(segment.headerID)), segment.payload...))...)
		body = append(body, encodePart(PartMediaEnd, mustTestVarint(uint64(segment.headerID)))...)
	}
	return body
}

func mustTestVarint(value uint64) []byte {
	encoded, _ := encodeUMPVarint(value)
	return encoded
}
