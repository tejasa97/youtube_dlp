package main

import (
	"encoding/binary"
	"io"
	"os"
)

func main() {
	// Consume one complete request before replying. Without this handshake the
	// helper can exit while the supervisor is still writing, racing a broken
	// pipe (crash classification) against the intended malformed response.
	var requestHeader [4]byte
	if _, err := io.ReadFull(os.Stdin, requestHeader[:]); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(requestHeader[:])
	if size > 4<<20 {
		return
	}
	if _, err := io.CopyN(io.Discard, os.Stdin, int64(size)); err != nil {
		return
	}
	payload := []byte("not-json")
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	_, _ = os.Stdout.Write(header[:])
	_, _ = os.Stdout.Write(payload)
}
