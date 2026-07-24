package youtubeump

import "testing"

func TestProtobufVarintCanonicalAndOverflow(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   []byte
		wantErr error
	}{
		{"canonical_zero", []byte{0x00}, nil},
		{"canonical_one", []byte{0x01}, nil},
		{"canonical_127", []byte{0x7F}, nil},
		{"canonical_128", []byte{0x80, 0x01}, nil},
		{"noncanonical_single", []byte{0x80, 0x00}, ErrNonCanonicalVarint},
		{"truncated", []byte{0x80}, ErrTruncatedStream},
		{"overflow_tenth", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x02}, ErrVarintOverflow},
		{"overflow_long", []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x01}, ErrVarintOverflow},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := readProtobufVarint(test.input)
			if err != test.wantErr {
				t.Fatalf("err=%v want=%v", err, test.wantErr)
			}
		})
	}
}

func TestProtobufUint32RejectsOverflow(t *testing.T) {
	_, err := readProtobufUint32([]byte{0xFF, 0xFF, 0xFF, 0xFF, 0x10})
	if err != ErrVarintOverflow {
		t.Fatalf("err=%v", err)
	}
}

func TestMediaHeaderRejectsOverflowHeaderID(t *testing.T) {
	payload := appendProtobufVarint(nil, fMediaHdrHeaderID, uint64(^uint32(0))+1)
	_, err := unmarshalMediaHeader(payload)
	if err != ErrVarintOverflow {
		t.Fatalf("err=%v", err)
	}
}
