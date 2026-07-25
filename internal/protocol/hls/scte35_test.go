package hls

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestValidateSCTE35DirectionSpliceInsertAndTimeSignal(t *testing.T) {
	out, err := validateSCTE35Direction(testSCTE35OutImmediate, scte35DirectionOut)
	if err != nil || !out {
		t.Fatalf("OUT immediate splice_insert = %v err=%v", out, err)
	}
	in, err := validateSCTE35Direction(testSCTE35InImmediate, scte35DirectionIn)
	if err != nil || in {
		t.Fatalf("IN immediate splice_insert = %v err=%v", in, err)
	}
	if _, err := validateSCTE35Direction(testSCTE35OutWithTime, scte35DirectionOut); err != nil {
		t.Fatalf("OUT timed splice_insert: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35OutWithDuration, scte35DirectionOut); err != nil {
		t.Fatalf("OUT duration splice_insert: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35ComponentImmediate, scte35DirectionOut); err != nil {
		t.Fatalf("component immediate splice_insert: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35ComponentTimed, scte35DirectionOut); err != nil {
		t.Fatalf("component timed splice_insert: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35ComponentDuration, scte35DirectionOut); err != nil {
		t.Fatalf("component duration splice_insert: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35TimeSignal, scte35DirectionOut); err != nil {
		t.Fatalf("time_signal OUT: %v", err)
	}
	if _, err := validateSCTE35Direction(testSCTE35TimeSignal, scte35DirectionIn); err != nil {
		t.Fatalf("time_signal IN: %v", err)
	}

	cmdOut, err := validateSCTE35Direction(testSCTE35OutImmediate, scte35DirectionFromCommand)
	if err != nil || !cmdOut {
		t.Fatalf("CMD OUT = %v err=%v", cmdOut, err)
	}
	cmdIn, err := validateSCTE35Direction(testSCTE35InImmediate, scte35DirectionFromCommand)
	if err != nil || cmdIn {
		t.Fatalf("CMD IN = %v err=%v", cmdIn, err)
	}
}

func TestValidateSCTE35RejectsMalformedPayloads(t *testing.T) {
	oversized := "0x" + strings.Repeat("00", maxSCTE35DecodedSize+1)
	cases := []struct {
		name    string
		payload string
		mode    scte35Direction
	}{
		{"missing_prefix", "FC301B00", scte35DirectionOut},
		{"odd_hex", "0xFC3", scte35DirectionOut},
		{"non_hex", "0xFCZZ", scte35DirectionOut},
		{"wrong_table", testSCTE35WrongTableID, scte35DirectionOut},
		{"bad_crc", testSCTE35BadCRC, scte35DirectionOut},
		{"inconsistent_length", testSCTE35InconsistentLength, scte35DirectionOut},
		{"trailing_section", testSCTE35TrailingSection, scte35DirectionOut},
		{"encrypted", testSCTE35EncryptedPacket, scte35DirectionOut},
		{"cancelled", testSCTE35CancelledInsert, scte35DirectionOut},
		{"unsupported_schedule", testSCTE35SpliceSchedule, scte35DirectionOut},
		{"cmd_requires_insert", testSCTE35TimeSignal, scte35DirectionFromCommand},
		{"out_payload_in_network", testSCTE35InImmediate, scte35DirectionOut},
		{"in_payload_out_network", testSCTE35OutImmediate, scte35DirectionIn},
		{"oversized_payload", oversized, scte35DirectionOut},
		{"missing_descriptor_loop", testSCTE35MissingDescriptorLoop, scte35DirectionOut},
		{"bad_section_syntax", testSCTE35BadSectionSyntax, scte35DirectionOut},
		{"bad_private_indicator", testSCTE35BadPrivateIndicator, scte35DirectionOut},
		{"bad_section_reserved", testSCTE35BadSectionReserved, scte35DirectionOut},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateSCTE35Direction(test.payload, test.mode); err == nil {
				t.Fatalf("expected error for %q", test.payload)
			}
		})
	}
}

func TestApplyDaterangeSCTE35GrammarAndAmbiguity(t *testing.T) {
	start, end, handled, err := applyDaterangeSCTE35(" #EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35OutImmediate)
	if err != nil || handled || start || end {
		t.Fatalf("leading whitespace ignored: start=%v end=%v handled=%v err=%v", start, end, handled, err)
	}
	start, end, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:CLASS=ad,START-DATE=2020-01-01T00:00:00Z")
	if err != nil || !handled || start || end {
		t.Fatalf("ordinary daterange ignored: start=%v end=%v handled=%v err=%v", start, end, handled, err)
	}
	start, end, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:ID=ad1,SCTE35-OUT=" + testSCTE35OutImmediate + "  ")
	if err != nil || !handled || !start || end {
		t.Fatalf("trailing whitespace accepted: start=%v end=%v handled=%v err=%v", start, end, handled, err)
	}
	_, _, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:ID=ad1,SCTE35-OUT=" + testSCTE35OutImmediate + ",SCTE35-IN=" + testSCTE35InImmediate)
	if err == nil || !handled {
		t.Fatalf("ambiguous attributes err=%v handled=%v", err, handled)
	}
	_, _, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:ID=ad1,SCTE35-OUT=" + testSCTE35OutImmediate + ",SCTE35-OUT=" + testSCTE35OutImmediate)
	if err == nil || !handled {
		t.Fatalf("duplicate attributes err=%v handled=%v", err, handled)
	}
	_, _, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:SCTE35-OUT=" + testSCTE35OutImmediate)
	if err == nil || !handled {
		t.Fatalf("missing ID err=%v handled=%v", err, handled)
	}
	_, _, handled, err = applyDaterangeSCTE35("#EXT-X-DATERANGE:ID=ad1,scte35-out=" + testSCTE35OutImmediate)
	if err != nil || !handled {
		t.Fatalf("lowercase attribute ignored without error: err=%v handled=%v", err, handled)
	}
	for _, line := range []string{
		"#EXT-X-DATERANGE:ID=ad1,SCTE35-OUT=",
		"#EXT-X-DATERANGE:ID=ad1,SCTE35-IN=",
		"#EXT-X-DATERANGE:ID=ad1,SCTE35-CMD=",
	} {
		_, _, handled, err = applyDaterangeSCTE35(line)
		if err == nil || !handled {
			t.Fatalf("empty directional attribute %q err=%v handled=%v", line, err, handled)
		}
	}
}

func TestParseDaterangeSCTE35AdvertisementState(t *testing.T) {
	playlist, err := Parse("https://example.invalid/daterange.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:30
#EXTINF:1,
media-30.bin
#EXT-X-DATERANGE:ID="break-1",SCTE35-OUT=`+testSCTE35OutImmediate+`
#EXT-X-PART:DURATION=0.5,URI="ad-31.0.bin"
#EXTINF:1,
ad-31.bin
#EXT-X-DATERANGE:ID="break-1",SCTE35-IN=`+testSCTE35InImmediate+`
#EXTINF:1,
media-32.bin
#EXT-X-DATERANGE:ID="break-2",SCTE35-CMD=`+testSCTE35OutImmediate+`
#EXTINF:1,
ad-33.bin
#EXT-X-DATERANGE:ID="break-2",SCTE35-CMD=`+testSCTE35InImmediate+`
#EXTINF:1,
media-34.bin
#EXT-X-DATERANGE:ID="break-3",SCTE35-OUT=`+testSCTE35TimeSignal+`
#EXTINF:1,
ad-35.bin
#EXT-X-DATERANGE:ID="break-3",SCTE35-IN=`+testSCTE35TimeSignal+`
#EXTINF:1,
media-36.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	wantAds := []bool{false, true, true, false, true, false, true, false}
	if len(playlist.Media.Segments) != len(wantAds) {
		t.Fatalf("segments=%#v", playlist.Media.Segments)
	}
	for index, segment := range playlist.Media.Segments {
		if segment.Advertisement != wantAds[index] {
			t.Fatalf("segment[%d]=%#v want ad=%v", index, segment, wantAds[index])
		}
	}
}

func TestParseDaterangeSCTE35RejectsInvalidLines(t *testing.T) {
	invalid := []string{
		"#EXTM3U\n#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35BadCRC + "\n#EXTINF:1,\nseg\n",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35OutImmediate + ",SCTE35-IN=" + testSCTE35InImmediate + "\n#EXTINF:1,\nseg\n",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=x,SCTE35-CMD=" + testSCTE35TimeSignal + "\n#EXTINF:1,\nseg\n",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35InImmediate + "\n#EXTINF:1,\nseg\n",
		"#EXTM3U\n#EXT-X-DATERANGE:ID=x,SCTE35-OUT=\n#EXTINF:1,\nseg\n",
	}
	for index, input := range invalid {
		if _, err := Parse("https://example.invalid/bad.m3u8", []byte(input)); !errors.Is(err, ErrInvalidPlaylist) {
			t.Fatalf("input[%d] error=%v", index, err)
		}
	}
}

func TestParseDaterangeSCTE35DeltaPartsAndOrdinaryIgnored(t *testing.T) {
	playlist, err := Parse("https://example.invalid/live.m3u8", []byte(`#EXTM3U
#EXT-X-MEDIA-SEQUENCE:100
#EXT-X-SKIP:SKIPPED-SEGMENTS=2
#EXT-X-DATERANGE:CLASS=ad,START-DATE=2020-01-01T00:00:00Z
#EXT-X-DATERANGE:ID="mid-break",SCTE35-OUT=`+testSCTE35OutImmediate+`
#EXT-X-PART:DURATION=0.5,URI="ad-part.bin",BYTERANGE="4@10"
#EXT-X-DATERANGE:ID="mid-break",SCTE35-IN=`+testSCTE35InImmediate+`
#EXT-X-BYTERANGE:9@20
#EXTINF:1,
complete.bin
#EXT-X-ENDLIST
`))
	if err != nil {
		t.Fatal(err)
	}
	segments := playlist.Media.Segments
	if len(segments) != 2 {
		t.Fatalf("segments=%#v", segments)
	}
	adPart := segments[0]
	if !adPart.Advertisement || adPart.Sequence != 102 || !adPart.Partial || adPart.PartIndex != 0 ||
		adPart.RangeStart != 10 || adPart.RangeLength != 4 {
		t.Fatalf("ad part=%#v", adPart)
	}
	complete := segments[1]
	if complete.Advertisement || complete.Sequence != 102 || complete.Partial ||
		complete.RangeStart != 20 || complete.RangeLength != 9 || complete.Duration != time.Second {
		t.Fatalf("complete=%#v", complete)
	}
}

func FuzzSCTE35Daterange(f *testing.F) {
	f.Add("#EXT-X-DATERANGE:CLASS=ad")
	f.Add("#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35OutImmediate)
	f.Add("#EXT-X-DATERANGE:ID=x,SCTE35-IN=" + testSCTE35InImmediate)
	f.Add("#EXT-X-DATERANGE:ID=x,SCTE35-CMD=" + testSCTE35OutImmediate)
	f.Add(" #EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35OutImmediate)
	f.Add("#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35BadCRC)
	f.Add("#EXT-X-DATERANGE:ID=x,SCTE35-OUT=" + testSCTE35OutImmediate + ",SCTE35-IN=" + testSCTE35InImmediate)
	f.Fuzz(func(t *testing.T, line string) {
		if len(line) > 1<<20 {
			t.Skip()
		}
		start, end, handled, err := applyDaterangeSCTE35(line)
		if err != nil {
			return
		}
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#EXT-X-DATERANGE:") && line != "" && line[0] != ' ' && line[0] != '\t' {
			if !handled {
				t.Fatalf("expected handled daterange for %q", line)
			}
		}
		if start && end {
			t.Fatalf("conflicting directions for %q", line)
		}
		if start || end {
			trimmed := strings.TrimRight(line, " \t")
			if !strings.Contains(trimmed, "SCTE35-OUT=") && !strings.Contains(trimmed, "SCTE35-IN=") && !strings.Contains(trimmed, "SCTE35-CMD=") {
				t.Fatalf("ad direction without directional attribute for %q", line)
			}
		}
	})
}

func FuzzSCTE35Payload(f *testing.F) {
	f.Add(testSCTE35OutImmediate)
	f.Add(testSCTE35BadCRC)
	f.Add("0xFC")
	f.Add("FC301B00")
	f.Fuzz(func(t *testing.T, payload string) {
		if len(payload) > maxSCTE35HexChars+4 {
			t.Skip()
		}
		_, err := validateSCTE35Direction(payload, scte35DirectionOut)
		if err == nil {
			if !strings.HasPrefix(payload, "0x") {
				t.Fatalf("accepted payload without 0x prefix: %q", payload)
			}
			decoded, decodeErr := decodeSCTE35Hex(payload)
			if decodeErr != nil {
				t.Fatalf("validate succeeded but decode failed: %v", decodeErr)
			}
			if len(decoded) == 0 || decoded[0] != scte35TableID {
				t.Fatalf("accepted invalid table_id payload %q", payload)
			}
		}
	})
}
