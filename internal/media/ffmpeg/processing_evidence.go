package ffmpeg

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

func readProcessingEvidence(path string, maximum int64) ([]byte, error) {
	file, info, err := openProcessingEvidence(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if info.Size() < 0 || info.Size() > maximum {
		return nil, processingFailure(ErrInvalidProcessingWorkspace, "processing evidence is oversized", nil)
	}
	reader := io.LimitReader(file, maximum+1)
	encoded, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > maximum {
		return nil, processingFailure(ErrInvalidProcessingWorkspace, "processing evidence is oversized", nil)
	}
	return encoded, nil
}

func validateProcessingEvidence(path string) error {
	file, _, err := openProcessingEvidence(path)
	if err != nil {
		return err
	}
	return file.Close()
}

func processingArtifactDigest(path string) (processingArtifact, error) {
	file, info, err := openProcessingEvidence(path)
	if err != nil {
		return processingArtifact{}, err
	}
	if info.Size() <= 0 || info.Size() > maxProcessingArtifact {
		_ = file.Close()
		return processingArtifact{}, processingFailure(ErrInvalidProcessingWorkspace, "processing output is empty or oversized", nil)
	}
	hasher := sha256.New()
	bytes, copyErr := io.Copy(hasher, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		return processingArtifact{}, errors.Join(copyErr, closeErr)
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	return processingArtifact{Bytes: bytes, SHA256: digest}, nil
}

func validateProcessingArtifact(artifact processingArtifact) bool {
	if artifact.Bytes <= 0 || artifact.Bytes > maxProcessingArtifact || len(artifact.SHA256) != 64 {
		return false
	}
	if _, err := hex.DecodeString(artifact.SHA256); err != nil {
		return false
	}
	return artifact.SHA256 == strings.ToLower(artifact.SHA256)
}
