package ffmpeg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxEmbeddedThumbnailBytes = 16 << 20

func (tools *Toolset) writeXiphThumbnailMetadata(
	ctx context.Context,
	inputPath, imagePath, destination string,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	probe, err := tools.Probe(ctx, inputPath)
	if err != nil {
		return "", err
	}
	picture, err := encodeFLACPicture(ctx, imagePath)
	if err != nil {
		return "", err
	}
	tags := make(Metadata, len(probe.Format.Tags)+1)
	for key, value := range probe.Format.Tags {
		if strings.EqualFold(key, "METADATA_BLOCK_PICTURE") {
			continue
		}
		tags[key] = value
	}
	for _, stream := range probe.Streams {
		if stream.CodecType != "audio" {
			continue
		}
		for key, value := range stream.Tags {
			if strings.EqualFold(key, "METADATA_BLOCK_PICTURE") {
				continue
			}
			if !metadataContainsFold(tags, key) {
				tags[key] = value
			}
		}
	}
	return writeThumbnailMetadata(ctx, destination, tags, picture)
}

func metadataContainsFold(metadata Metadata, key string) bool {
	for candidate := range metadata {
		if strings.EqualFold(candidate, key) {
			return true
		}
	}
	return false
}

func encodeFLACPicture(ctx context.Context, path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: open thumbnail: %v", ErrInvalidOperation, err)
	}
	defer file.Close()
	data, err := readEmbeddedThumbnail(ctx, file)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, fmt.Errorf("%w: read thumbnail: %v", ErrInvalidOperation, err)
	}
	if len(data) == 0 || len(data) > maxEmbeddedThumbnailBytes {
		return nil, fmt.Errorf("%w: thumbnail size exceeds limit", ErrInvalidOperation)
	}
	return encodeFLACPictureData(data)
}

func readEmbeddedThumbnail(ctx context.Context, reader io.Reader) ([]byte, error) {
	var data bytes.Buffer
	buffer := make([]byte, 32<<10)
	for data.Len() <= maxEmbeddedThumbnailBytes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, err := reader.Read(buffer)
		if read > 0 {
			_, _ = data.Write(buffer[:read])
		}
		if err == io.EOF {
			return data.Bytes(), nil
		}
		if err != nil {
			return nil, err
		}
	}
	return data.Bytes(), nil
}

func encodeFLACPictureData(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > maxEmbeddedThumbnailBytes {
		return nil, fmt.Errorf("%w: thumbnail size exceeds limit", ErrInvalidOperation)
	}
	mime := ""
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		mime = "image/png"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte{0xff, 0xd8, 0xff}):
		mime = "image/jpeg"
	default:
		return nil, fmt.Errorf("%w: Xiph thumbnail must be jpg or png", ErrInvalidOperation)
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 {
		return nil, fmt.Errorf("%w: decode thumbnail dimensions", ErrInvalidOperation)
	}
	if config.Width > 1<<20 || config.Height > 1<<20 {
		return nil, fmt.Errorf("%w: thumbnail dimensions exceed limit", ErrInvalidOperation)
	}
	var picture bytes.Buffer
	writePictureUint32 := func(value uint32) {
		_ = binary.Write(&picture, binary.BigEndian, value)
	}
	writePictureUint32(3)
	writePictureUint32(uint32(len(mime)))
	picture.WriteString(mime)
	writePictureUint32(0)
	writePictureUint32(uint32(config.Width))
	writePictureUint32(uint32(config.Height))
	writePictureUint32(0)
	writePictureUint32(0)
	writePictureUint32(uint32(len(data)))
	picture.Write(data)
	return picture.Bytes(), nil
}

func writeThumbnailMetadata(
	ctx context.Context, destination string, tags Metadata, picture []byte,
) (string, error) {
	if len(tags) > maxMetadataFields {
		return "", fmt.Errorf("%w: too many thumbnail metadata fields", ErrInvalidOperation)
	}
	directory := filepath.Dir(destination)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", fmt.Errorf("%w: create thumbnail metadata directory: %v", ErrMediaFailure, err)
	}
	file, err := os.CreateTemp(directory, ".ytdlp-thumbnail-*.ffmetadata")
	if err != nil {
		return "", fmt.Errorf("%w: create thumbnail metadata: %v", ErrMediaFailure, err)
	}
	name := file.Name()
	fail := func(err error) (string, error) {
		_ = file.Close()
		_ = os.Remove(name)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		return "", err
	}
	writer := contextWriter{ctx: ctx, writer: file}
	if _, err := io.WriteString(writer, ";FFMETADATA1\n"); err != nil {
		return fail(fmt.Errorf("%w: write thumbnail metadata: %v", ErrMediaFailure, err))
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		if key == "" || len(key) > 128 || strings.ContainsAny(key, "=\r\n\x00") {
			return fail(fmt.Errorf("%w: unsafe thumbnail metadata key", ErrInvalidOperation))
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := tags[key]
		if strings.ContainsRune(value, 0) || len(value) > 32<<20 {
			return fail(fmt.Errorf("%w: unsafe thumbnail metadata value", ErrInvalidOperation))
		}
		line := escapeFFMetadata(key) + "=" + escapeFFMetadataMultiline(value) + "\n"
		if _, err := io.WriteString(writer, line); err != nil {
			return fail(fmt.Errorf("%w: write thumbnail metadata: %v", ErrMediaFailure, err))
		}
	}
	if _, err := io.WriteString(writer, "METADATA_BLOCK_PICTURE="); err != nil {
		return fail(fmt.Errorf("%w: write thumbnail metadata: %v", ErrMediaFailure, err))
	}
	encoder := base64.NewEncoder(base64.StdEncoding, writer)
	for offset := 0; offset < len(picture); offset += 32 << 10 {
		end := min(offset+(32<<10), len(picture))
		if _, err := encoder.Write(picture[offset:end]); err != nil {
			_ = encoder.Close()
			return fail(fmt.Errorf("%w: write thumbnail picture: %v", ErrMediaFailure, err))
		}
	}
	if err := encoder.Close(); err != nil {
		return fail(fmt.Errorf("%w: finalize thumbnail picture: %v", ErrMediaFailure, err))
	}
	if _, err := io.WriteString(writer, "\n"); err != nil {
		return fail(fmt.Errorf("%w: write thumbnail metadata: %v", ErrMediaFailure, err))
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return "", fmt.Errorf("%w: finalize thumbnail metadata: %v", ErrMediaFailure, err)
	}
	return name, nil
}

type contextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (writer contextWriter) Write(input []byte) (int, error) {
	if err := writer.ctx.Err(); err != nil {
		return 0, err
	}
	return writer.writer.Write(input)
}

func escapeFFMetadataMultiline(value string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	value = escapeFFMetadata(value)
	return strings.ReplaceAll(value, "\n", "\\\n")
}
