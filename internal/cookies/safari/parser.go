package safari

import (
	"context"
	"encoding/binary"
)

const (
	databaseMagic     = "cook"
	pageMagic         = "\x00\x00\x01\x00"
	maxFileBytes      = 64 << 20
	maxPageCount      = 65_536
	defaultMaxCookies = 1_000_000
	maxRecordBytes    = 1 << 20
	maxFieldBytes     = 16 << 10
)

// Parse decodes a complete Cookies.binarycookies file. Structurally corrupt
// framing invalidates the whole result. Semantically invalid individual
// records are counted as failed only when their framing is trustworthy.
func Parse(ctx context.Context, data []byte, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	byteLimit := options.MaxBytes
	if byteLimit <= 0 {
		byteLimit = maxFileBytes
	}
	if int64(len(data)) > byteLimit || int64(len(data)) > maxFileBytes {
		return Result{}, ErrLimit
	}
	if len(data) < 8 || string(data[:4]) != databaseMagic {
		return Result{}, ErrInvalidDatabase
	}
	pageCount := uint64(binary.BigEndian.Uint32(data[4:8]))
	if pageCount > maxPageCount {
		return Result{}, ErrLimit
	}
	headerBytes := uint64(8) + pageCount*4
	if headerBytes > uint64(len(data)) {
		return Result{}, ErrInvalidDatabase
	}
	pageSizes := make([]uint32, int(pageCount))
	var bodyBytes uint64
	for index := range pageSizes {
		size := binary.BigEndian.Uint32(data[8+index*4:])
		if size == 0 {
			return Result{}, ErrInvalidDatabase
		}
		bodyBytes += uint64(size)
		if bodyBytes > uint64(byteLimit) {
			return Result{}, ErrLimit
		}
		pageSizes[index] = size
	}
	if headerBytes+bodyBytes > uint64(len(data)) {
		return Result{}, ErrInvalidDatabase
	}

	maxCookies := options.MaxCookies
	if maxCookies <= 0 {
		maxCookies = defaultMaxCookies
	}
	result := Result{}
	offset := int(headerBytes)
	for _, pageSize := range pageSizes {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		end := offset + int(pageSize)
		pageResult, err := parsePage(ctx, data[offset:end], maxCookies-result.Total)
		if err != nil {
			if err == ErrLimit {
				return Result{}, err
			}
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return Result{}, ErrInvalidDatabase
		}
		result.Cookies = append(result.Cookies, pageResult.Cookies...)
		result.Total += pageResult.Total
		result.Imported += pageResult.Imported
		result.Failed += pageResult.Failed
		offset = end
	}
	return result, nil
}

func parsePage(ctx context.Context, page []byte, remaining int) (Result, error) {
	if len(page) < 8 || string(page[:4]) != pageMagic {
		return Result{}, ErrInvalidDatabase
	}
	recordCount := uint64(binary.LittleEndian.Uint32(page[4:8]))
	if recordCount > uint64(remaining) {
		return Result{}, ErrLimit
	}
	tableEnd := uint64(8) + recordCount*4
	if tableEnd > uint64(len(page)) {
		return Result{}, ErrInvalidDatabase
	}
	offsets := make([]uint32, int(recordCount))
	for index := range offsets {
		offset := binary.LittleEndian.Uint32(page[8+index*4:])
		if uint64(offset) < tableEnd || int(offset) >= len(page) {
			return Result{}, ErrInvalidDatabase
		}
		if index > 0 && offset <= offsets[index-1] {
			return Result{}, ErrInvalidDatabase
		}
		offsets[index] = offset
	}

	result := Result{Total: int(recordCount)}
	for index, rawOffset := range offsets {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		start := int(rawOffset)
		if start+4 > len(page) {
			return Result{}, ErrInvalidDatabase
		}
		size := uint64(binary.LittleEndian.Uint32(page[start : start+4]))
		if size < recordHeaderBytes || size > maxRecordBytes || uint64(start)+size > uint64(len(page)) {
			return Result{}, ErrInvalidDatabase
		}
		end := start + int(size)
		if index+1 < len(offsets) && end > int(offsets[index+1]) {
			return Result{}, ErrInvalidDatabase
		}
		cookie, valid, err := parseRecord(page[start:end])
		if err != nil {
			return Result{}, err
		}
		if !valid {
			result.Failed++
			continue
		}
		result.Cookies = append(result.Cookies, cookie)
		result.Imported++
	}
	return result, nil
}
