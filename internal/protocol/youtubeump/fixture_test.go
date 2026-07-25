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

func encodePlaybackCookie(fields ...[]byte) []byte {
	var buf []byte
	for _, field := range fields {
		buf = append(buf, field...)
	}
	return buf
}

func encodeNextRequestPolicy(backoffMs int64, cookie []byte) []byte {
	var buf []byte
	if backoffMs != 0 {
		buf = appendProtobufVarint(buf, fPolicyBackoffTimeMs, uint64(backoffMs))
	}
	if len(cookie) > 0 {
		buf = appendProtobufBytes(buf, fPolicyPlaybackCookie, cookie)
	}
	return buf
}

func appendPolicyPart(body []byte, backoffMs int64, cookie []byte) []byte {
	return append(body, encodePart(PartNextRequestPolicy, encodeNextRequestPolicy(backoffMs, cookie))...)
}

func appendEndOfTrackPart(body []byte) []byte {
	return append(body, encodePart(PartEndOfTrack, nil)...)
}

func encodeSabrRedirect(url string) []byte {
	return appendProtobufBytes(nil, fSabrRedirectURL, []byte(url))
}

func appendRedirectPart(body []byte, url string) []byte {
	return append(body, encodePart(PartSABRRedirect, encodeSabrRedirect(url))...)
}

func encodeSabrContextUpdate(typ, scope, writePolicy int32, value []byte, sendByDefault bool) []byte {
	var buf []byte
	buf = appendProtobufVarint(buf, fSabrContextType, uint64(typ))
	if scope != 0 {
		buf = appendProtobufVarint(buf, fSabrContextScope, uint64(scope))
	}
	buf = appendProtobufBytes(buf, fSabrContextValue, value)
	if sendByDefault {
		buf = appendProtobufVarint(buf, fSabrContextSendByDefault, 1)
	}
	if writePolicy != 0 {
		buf = appendProtobufVarint(buf, fSabrContextWritePolicy, uint64(writePolicy))
	}
	return buf
}

func appendContextUpdatePart(body []byte, typ, scope, writePolicy int32, value []byte, sendByDefault bool) []byte {
	return append(body, encodePart(PartSABRContextUpdate, encodeSabrContextUpdate(typ, scope, writePolicy, value, sendByDefault))...)
}

func encodeSabrSendingPolicy(start, stop, discard []int32, packed bool) []byte {
	var buf []byte
	appendField := func(field uint64, values []int32) {
		if len(values) == 0 {
			return
		}
		if packed {
			buf = appendProtobufPackedInt32(buf, field, values)
			return
		}
		for _, value := range values {
			buf = appendProtobufVarint(buf, field, uint64(uint32(value)))
		}
	}
	appendField(fSabrSendingPolicyStart, start)
	appendField(fSabrSendingPolicyStop, stop)
	appendField(fSabrSendingPolicyDiscard, discard)
	return buf
}

func appendSendingPolicyPart(body []byte, start, stop, discard []int32, packed bool) []byte {
	return append(body, encodePart(PartSABRContextSendingPolicy, encodeSabrSendingPolicy(start, stop, discard, packed))...)
}

func encodeSabrError(errorType string, code int32) []byte {
	var buf []byte
	buf = appendProtobufBytes(buf, fSabrErrorType, []byte(errorType))
	buf = appendProtobufVarint(buf, fSabrErrorCode, uint64(uint32(code)))
	return buf
}

func appendSabrErrorPart(body []byte, errorType string, code int32) []byte {
	return append(body, encodePart(PartSABRError, encodeSabrError(errorType, code))...)
}

func encodeReloadPlayerResponse(token string) []byte {
	params := appendProtobufBytes(nil, fReloadPlaybackParamsToken, []byte(token))
	return appendProtobufBytes(nil, fReloadPlaybackContextParams, params)
}

func appendReloadPlayerPart(body []byte, token string) []byte {
	return append(body, encodePart(PartReloadPlayerResponse, encodeReloadPlayerResponse(token))...)
}

func validTestCookie() []byte {
	return encodePlaybackCookie(
		appendProtobufVarint(nil, fPlaybackCookieField1, 1),
		appendProtobufVarint(nil, fPlaybackCookieField2, 2),
	)
}

func fixtureRedirectURL(hostSuffix string) string {
	return "https://rr" + hostSuffix + "---sn-fixture.googlevideo.com/videoplayback/sabr/fixture?sig=fixture%2Btoken"
}
