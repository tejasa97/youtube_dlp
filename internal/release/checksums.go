package release

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteChecksums emits the conventional deterministic SHA256SUMS format.
func WriteChecksums(writer io.Writer, artifacts map[string][]byte) error {
	if len(artifacts) == 0 || len(artifacts) > maxEntries {
		return ErrInvalidInput
	}
	names := make([]string, 0, len(artifacts))
	for name := range artifacts {
		names = append(names, name)
	}
	sort.Strings(names)
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if !safeBase(name) || len(artifacts[name]) > maxEntryBytes {
			return ErrInvalidInput
		}
		folded := strings.ToLower(name)
		if _, duplicate := seen[folded]; duplicate {
			return ErrInvalidInput
		}
		seen[folded] = struct{}{}
		digest := sha256.Sum256(artifacts[name])
		if _, err := fmt.Fprintf(writer, "%s  %s\n", hex.EncodeToString(digest[:]), name); err != nil {
			return fmt.Errorf("%w: write checksums", ErrIO)
		}
	}
	return nil
}

func VerifyChecksums(encoded []byte, artifacts map[string][]byte) error {
	if len(encoded) == 0 || len(encoded) > 1<<20 || len(artifacts) == 0 || len(artifacts) > maxEntries {
		return ErrInvalidInput
	}
	expected := make(map[string]string, len(artifacts))
	foldedNames := make(map[string]struct{}, len(artifacts))
	for name, data := range artifacts {
		if !safeBase(name) || len(data) > maxEntryBytes {
			return ErrInvalidInput
		}
		digest := sha256.Sum256(data)
		expected[name] = hex.EncodeToString(digest[:])
		folded := strings.ToLower(name)
		if _, duplicate := foldedNames[folded]; duplicate {
			return ErrInvalidInput
		}
		foldedNames[folded] = struct{}{}
	}
	seen := make(map[string]struct{}, len(expected))
	scanner := bufio.NewScanner(bytes.NewReader(encoded))
	scanner.Buffer(make([]byte, 1024), 4096)
	for scanner.Scan() {
		line := scanner.Text()
		if len(line) < 67 || line[64:66] != "  " {
			return ErrInvalidInput
		}
		digest, name := line[:64], line[66:]
		if !validDigest(digest) || !safeBase(name) {
			return ErrInvalidInput
		}
		if _, duplicate := seen[name]; duplicate {
			return ErrInvalidInput
		}
		seen[name] = struct{}{}
		if expected[name] != digest {
			return ErrInvalidInput
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return ErrInvalidInput
	}
	if len(seen) != len(expected) || !bytes.HasSuffix(encoded, []byte("\n")) {
		return ErrInvalidInput
	}
	return nil
}
