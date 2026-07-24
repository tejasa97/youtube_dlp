package youtubeump

import (
	"fmt"
	"math"
)

func int32FromUint64(value uint64) (int32, error) {
	if value > math.MaxInt32 {
		return 0, ErrVarintOverflow
	}
	return int32(value), nil
}

func int64FromUint64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, ErrVarintOverflow
	}
	return int64(value), nil
}

func uint32FromUint64(value uint64) (uint32, error) {
	if value > math.MaxUint32 {
		return 0, ErrVarintOverflow
	}
	return uint32(value), nil
}

func int32SegmentIndex(sequence uint64) (int32, error) {
	if sequence > math.MaxInt32 {
		return 0, ErrVarintOverflow
	}
	return int32(sequence), nil
}

func expectedDurationMs(durationSec int64) (int64, error) {
	if durationSec <= 0 || durationSec > math.MaxInt64/1000 {
		return 0, fmt.Errorf("%w: invalid finite VOD duration", ErrMissingConfig)
	}
	return durationSec * 1000, nil
}

func FormatIDFromItag(itag int64, lastModified int64, xtags string) (FormatID, error) {
	return formatIDFromItag(itag, lastModified, xtags)
}

func ClientInfoFromID(clientID int64, version string) (ClientInfo, error) {
	return clientInfoFromID(clientID, version)
}

func formatIDFromItag(itag int64, lastModified int64, xtags string) (FormatID, error) {
	if itag <= 0 || itag > math.MaxInt32 {
		return FormatID{}, fmt.Errorf("%w: invalid SABR itag", ErrMissingConfig)
	}
	if lastModified < 0 {
		return FormatID{}, fmt.Errorf("%w: invalid SABR last modified", ErrMissingConfig)
	}
	if lastModified > math.MaxInt64 {
		return FormatID{}, ErrVarintOverflow
	}
	return FormatID{
		Itag:         int32(itag),
		LastModified: uint64(lastModified),
		XTags:        xtags,
	}, nil
}

func clientInfoFromID(clientID int64, version string) (ClientInfo, error) {
	if clientID <= 0 || clientID > math.MaxInt32 {
		return ClientInfo{}, fmt.Errorf("%w: invalid SABR client id", ErrMissingConfig)
	}
	return ClientInfo{ClientName: int32(clientID), ClientVersion: version}, nil
}
