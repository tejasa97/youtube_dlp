package youtubeump

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestUMPReaderFragmentedAndMultipleMediaChunks(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 1, payload: []byte("abc")},
		testSegment{headerID: 3, sequence: 1, duration: 1, payload: []byte("defg")},
	)
	reader := NewReader(newByteReader(body), MaxRoundBytes)
	var parts []Part
	for {
		part, ok, err := reader.ReadPart()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		parts = append(parts, part)
	}
	if len(parts) != 11 {
		t.Fatalf("parts=%d", len(parts))
	}
}

func TestUMPReaderOneByteAtATime(t *testing.T) {
	body := encodePart(PartMediaHeader, []byte{0x08, 0x01})
	reader := NewReader(&byteAtATime{data: body}, int64(len(body)))
	part, ok, err := reader.ReadPart()
	if err != nil || !ok || part.Type != PartMediaHeader {
		t.Fatalf("ok=%v err=%v part=%+v", ok, err, part)
	}
}

type byteAtATime struct {
	data []byte
}

func (reader *byteAtATime) Read(p []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	p[0] = reader.data[0]
	reader.data = reader.data[1:]
	return 1, nil
}

func TestUMPReaderRejectsNonCanonicalVarint(t *testing.T) {
	_, _, err := readUMPVarint([]byte{0x80, 0x00})
	if err != ErrNonCanonicalVarint {
		t.Fatalf("err=%v", err)
	}
}

func TestUMPReaderRejectsTruncatedFrame(t *testing.T) {
	body := encodePart(PartMediaHeader, []byte{0x08, 0x01})
	reader := NewReader(newByteReader(body[:len(body)-1]), int64(len(body)))
	_, _, err := reader.ReadPart()
	if err != ErrTruncatedStream {
		t.Fatalf("err=%v", err)
	}
}

func TestConsumeStreamDeterministicMedia(t *testing.T) {
	body := buildTestUMP(
		137,
		testSegment{headerID: 1, init: true, payload: []byte("INIT")},
		testSegment{headerID: 2, sequence: 0, duration: 10000, payload: []byte("hello world")},
	)
	file, err := os.CreateTemp(t.TempDir(), "sabr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	if _, err := consumeStream(context.Background(), response.Body, assembler); err != nil {
		t.Fatal(err)
	}
	if !assembler.trackComplete() || assembler.totalWritten != int64(len("INIThello world")) {
		t.Fatalf("complete=%v written=%d", assembler.trackComplete(), assembler.totalWritten)
	}
}

func TestConsumeStreamRejectsMalformedSabrError(t *testing.T) {
	body := encodePart(PartSABRError, []byte{0x0A, 0x04, 'h', 't', 't', 'p'})
	file, err := os.CreateTemp(t.TempDir(), "sabr-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(file.Name())
	assembler := newTrackAssembler(FormatID{Itag: 137}, 10000, file, 1024)
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/vnd.yt-ump"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
	_, err = consumeStream(context.Background(), response.Body, assembler)
	if err == nil || !errors.Is(err, ErrInvalidProtobuf) {
		t.Fatalf("err=%v", err)
	}
}
