package main

import (
	"encoding/binary"
	"os"
)

func main() {
	payload := []byte("not-json")
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	_, _ = os.Stdout.Write(header[:])
	_, _ = os.Stdout.Write(payload)
}
