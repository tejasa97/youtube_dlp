package youtubeump

import (
	"errors"
	"testing"
)

func FuzzUMPVarint(f *testing.F) {
	f.Add(byte(0x7F))
	f.Add(byte(0x80))
	f.Add(byte(0xC1))
	f.Fuzz(func(t *testing.T, prefix byte) {
		data := []byte{prefix, 0x01, 0x02, 0x03, 0x04}
		_, _, err := readUMPVarint(data)
		if err != nil && err != ErrTruncatedStream && err != ErrNonCanonicalVarint {
			t.Fatalf("unexpected err=%v", err)
		}
	})
}

func FuzzUMPStream(f *testing.F) {
	f.Add([]byte{0x14, 0x02, 0x08, 0x01})
	f.Add([]byte{0x15, 0x03, 0x01, 0x61, 0x62})
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxRoundBytes {
			return
		}
		reader := NewReader(newByteReader(body), int64(len(body)))
		for {
			_, ok, err := reader.ReadPart()
			if err != nil {
				return
			}
			if !ok {
				return
			}
		}
	})
}

func FuzzProtobufWire(f *testing.F) {
	f.Add([]byte{0x08, 0x89, 0x09})
	f.Add([]byte{0x1A, 0x03, 'f', 'o', 'o'})
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 4096 {
			return
		}
		_, err := unmarshalMediaHeader(body)
		if err != nil && !errors.Is(err, ErrInvalidProtobuf) && !errors.Is(err, ErrTruncatedStream) &&
			!errors.Is(err, ErrNonCanonicalVarint) && !errors.Is(err, ErrVarintOverflow) {
			t.Fatalf("unexpected err=%v", err)
		}
		_, n, varintErr := readProtobufVarint(body)
		if varintErr != nil && !errors.Is(varintErr, ErrTruncatedStream) &&
			!errors.Is(varintErr, ErrNonCanonicalVarint) && !errors.Is(varintErr, ErrVarintOverflow) {
			t.Fatalf("unexpected varint err=%v", varintErr)
		}
		if varintErr == nil && (n <= 0 || n > maxProtobufVarintBytes) {
			t.Fatalf("invalid consumed=%d", n)
		}
	})
}

func FuzzNextRequestPolicy(f *testing.F) {
	f.Add([]byte{0x20, 0x05})
	f.Add([]byte{0x3A, 0x03, 0x08, 0x01})
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxPlaybackCookieBytes+64 {
			return
		}
		_, _, _, err := parseNextRequestPolicy(body)
		if err != nil && !errors.Is(err, ErrInvalidProtobuf) && !errors.Is(err, ErrTruncatedStream) &&
			!errors.Is(err, ErrNonCanonicalVarint) && !errors.Is(err, ErrVarintOverflow) &&
			!errors.Is(err, ErrInvalidMediaState) && !errors.Is(err, ErrExcessivePolicyBackoff) {
			t.Fatalf("unexpected err=%v", err)
		}
		_ = validatePlaybackCookie(body)
	})
}

func FuzzSabrRedirect(f *testing.F) {
	f.Add([]byte{0x0A, 0x04, 'h', 't', 't', 'p'})
	f.Add(encodeSabrRedirect("https://rr1---sn-fixture.googlevideo.com/videoplayback?sig=x"))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxRedirectURLBytes+64 {
			return
		}
		_, err := parseSabrRedirect(body)
		if err != nil && !errors.Is(err, ErrInvalidProtobuf) && !errors.Is(err, ErrTruncatedStream) &&
			!errors.Is(err, ErrNonCanonicalVarint) && !errors.Is(err, ErrVarintOverflow) &&
			!errors.Is(err, ErrUnsafeRedirect) && !errors.Is(err, ErrUnsupportedURL) {
			t.Fatalf("unexpected err=%v", err)
		}
	})
}

func FuzzSabrContextUpdate(f *testing.F) {
	f.Add(encodeSabrContextUpdate(1, 1, 1, []byte("x"), true))
	f.Add([]byte{0x08, 0x01, 0x1A, 0x01, 'x'})
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxSabrContextValueBytes+64 {
			return
		}
		_, err := parseSabrContextUpdate(body)
		if err != nil && !errors.Is(err, ErrInvalidProtobuf) && !errors.Is(err, ErrTruncatedStream) &&
			!errors.Is(err, ErrNonCanonicalVarint) && !errors.Is(err, ErrVarintOverflow) &&
			!errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("unexpected err=%v", err)
		}
	})
}

func FuzzSabrContextSendingPolicy(f *testing.F) {
	f.Add(encodeSabrSendingPolicy([]int32{1, 2}, []int32{3}, []int32{4}, true))
	f.Add(encodeSabrSendingPolicy([]int32{1}, nil, nil, false))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > 4096 {
			return
		}
		_, err := parseSabrContextSendingPolicy(body)
		if err != nil && !errors.Is(err, ErrInvalidProtobuf) && !errors.Is(err, ErrTruncatedStream) &&
			!errors.Is(err, ErrNonCanonicalVarint) && !errors.Is(err, ErrVarintOverflow) &&
			!errors.Is(err, ErrInvalidContextState) {
			t.Fatalf("unexpected err=%v", err)
		}
	})
}

func FuzzMixedUMPStream(f *testing.F) {
	f.Add([]byte{0x23, 0x02, 0x20, 0x00})
	f.Add([]byte{0x3E, 0x00})
	f.Add(encodePart(PartSABRRedirect, encodeSabrRedirect("https://rr1---sn-fixture.googlevideo.com/x")))
	f.Add(encodePart(PartSABRContextUpdate, encodeSabrContextUpdate(1, 1, 1, []byte("v"), true)))
	f.Fuzz(func(t *testing.T, body []byte) {
		if len(body) > MaxRoundBytes {
			return
		}
		assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, nil, 1024)
		consumer := newStreamConsumer(assembler)
		reader := NewReader(newByteReader(body), int64(len(body)))
		for {
			part, ok, err := reader.ReadPart()
			if err != nil {
				return
			}
			if !ok {
				_, _ = consumer.finish()
				return
			}
			if err := consumer.consumePart(part); err != nil {
				return
			}
		}
	})
}
