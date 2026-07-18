package pluginapi

import (
	"bytes"
	"errors"
	"testing"
)

func TestVersionEncodingAndCompatibility(t *testing.T) {
	if got := Version(1, 0); got != V1_0 {
		t.Fatalf("Version(1, 0) = %d", got)
	}
	if got := Version(1, 1); got != V1_1 {
		t.Fatalf("Version(1, 1) = %d", got)
	}
	if major, minor := VersionParts(V1_1); major != 1 || minor != 1 {
		t.Fatalf("VersionParts(V1_1) = %d, %d", major, minor)
	}
	if !Compatible(V1_0, V1_1) || Compatible(V1_1, Version(2, 0)) {
		t.Fatal("major-version compatibility is incorrect")
	}
}

func TestCodecRoundTripAndBounds(t *testing.T) {
	codec := Codec{Maximum: 1024}
	input := Envelope{Type: "hello", ABI: &VersionRange{Minimum: V1_0, Maximum: V1_1}}
	var buffer bytes.Buffer
	if err := codec.Write(&buffer, input); err != nil {
		t.Fatal(err)
	}
	output, err := codec.Read(&buffer)
	if err != nil || output.Type != "hello" || output.ABI.Maximum != V1_1 {
		t.Fatalf("output, error = %#v, %v", output, err)
	}
	if _, err := (Codec{Maximum: 2}).Read(bytes.NewReader([]byte{0, 0, 0, 3})); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("oversize error = %v", err)
	}
}

func FuzzCodecRead(f *testing.F) {
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Add([]byte{0, 0, 0, 0})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = (Codec{Maximum: 4096}).Read(bytes.NewReader(data))
	})
}
