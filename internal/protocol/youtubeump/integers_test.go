package youtubeump

import (
	"math"
	"testing"
)

func TestInt32FromUint64RejectsOverflow(t *testing.T) {
	_, err := int32FromUint64(uint64(math.MaxInt32) + 1)
	if err != ErrVarintOverflow {
		t.Fatalf("err=%v", err)
	}
}

func TestInt64FromUint64RejectsOverflow(t *testing.T) {
	_, err := int64FromUint64(uint64(math.MaxInt64) + 1)
	if err != ErrVarintOverflow {
		t.Fatalf("err=%v", err)
	}
}

func TestExpectedDurationMsRejectsOverflow(t *testing.T) {
	_, err := expectedDurationMs(math.MaxInt64/1000 + 1)
	if err == nil {
		t.Fatal("expected overflow rejection")
	}
}

func TestFormatIDFromItagRejectsInvalid(t *testing.T) {
	if _, err := FormatIDFromItag(0, 0, ""); err == nil {
		t.Fatal("expected rejection")
	}
	if _, err := FormatIDFromItag(math.MaxInt32+1, 0, ""); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestClientInfoFromIDRejectsInvalid(t *testing.T) {
	if _, err := ClientInfoFromID(0, "fixture"); err == nil {
		t.Fatal("expected rejection")
	}
}

func TestInt32SegmentIndexRejectsOverflow(t *testing.T) {
	_, err := int32SegmentIndex(uint64(math.MaxInt32) + 1)
	if err != ErrVarintOverflow {
		t.Fatalf("err=%v", err)
	}
}
