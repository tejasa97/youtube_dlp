package safari

import (
	"encoding/binary"
	"math"
	"net/http"
	"time"
	"unicode/utf8"
)

const (
	recordHeaderBytes = 56
	macEpochUnix      = 978307200
	maxUnixTimestamp  = 253402300799 // 9999-12-31T23:59:59Z
)

func parseRecord(record []byte) (*http.Cookie, bool, error) {
	if len(record) < recordHeaderBytes ||
		int(binary.LittleEndian.Uint32(record[:4])) != len(record) {
		return nil, false, ErrInvalidDatabase
	}
	flags := binary.LittleEndian.Uint32(record[8:12])
	offsets := [...]uint32{
		binary.LittleEndian.Uint32(record[16:20]),
		binary.LittleEndian.Uint32(record[20:24]),
		binary.LittleEndian.Uint32(record[24:28]),
		binary.LittleEndian.Uint32(record[28:32]),
	}
	for index, offset := range offsets {
		if offset < recordHeaderBytes || int(offset) >= len(record) {
			return nil, false, ErrInvalidDatabase
		}
		if index > 0 && offset <= offsets[index-1] {
			return nil, false, ErrInvalidDatabase
		}
	}
	fields := make([]string, len(offsets))
	for index, offset := range offsets {
		end := len(record)
		if index+1 < len(offsets) {
			end = int(offsets[index+1])
		}
		value, ok := boundedCString(record, int(offset), end)
		if !ok {
			return nil, false, ErrInvalidDatabase
		}
		fields[index] = value
	}
	expiry, ok := macAbsoluteTime(record[40:48])
	if !ok {
		return nil, false, nil
	}
	if _, ok := macAbsoluteTime(record[48:56]); !ok {
		return nil, false, nil
	}
	cookie := &http.Cookie{
		Domain:  fields[0],
		Name:    fields[1],
		Path:    fields[2],
		Value:   fields[3],
		Secure:  flags&1 != 0,
		Expires: expiry,
	}
	if cookie.Domain == "" || cookie.Name == "" || cookie.Path == "" ||
		cookie.Path[0] != '/' || cookie.Valid() != nil {
		return nil, false, nil
	}
	return cookie, true, nil
}

func boundedCString(data []byte, start, end int) (string, bool) {
	if start < 0 || end <= start || end > len(data) || end-start > maxFieldBytes+1 {
		return "", false
	}
	nul := -1
	for index := start; index < end; index++ {
		if data[index] == 0 {
			nul = index
			break
		}
	}
	if nul < 0 || nul-start > maxFieldBytes || !utf8.Valid(data[start:nul]) {
		return "", false
	}
	return string(data[start:nul]), true
}

func macAbsoluteTime(encoded []byte) (time.Time, bool) {
	if len(encoded) != 8 {
		return time.Time{}, false
	}
	seconds := math.Float64frombits(binary.LittleEndian.Uint64(encoded))
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) ||
		seconds < -macEpochUnix || seconds > maxUnixTimestamp-macEpochUnix {
		return time.Time{}, false
	}
	unixSeconds := int64(seconds) + macEpochUnix
	fraction := seconds - math.Trunc(seconds)
	nanos := int64(fraction * float64(time.Second))
	return time.Unix(unixSeconds, nanos).UTC(), true
}
