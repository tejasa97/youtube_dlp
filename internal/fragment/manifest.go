package fragment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// artifactManifest makes a cancelled fragment download safely resumable. It
// records content digests only after each fragment is atomically published.
// A legacy state file without Artifacts remains readable for compatibility.
type artifactManifest struct {
	path  string
	state manifestState
	mu    sync.Mutex
}
type manifestState struct {
	Hash      string           `json:"hash"`
	Artifacts map[int]artifact `json:"artifacts,omitempty"`
}
type artifact struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

const maxManifestBytes = 4 << 20

func openArtifactManifest(workDir, hash string) (*artifactManifest, error) {
	path := filepath.Join(workDir, "state.json")
	if isSymlink(path) {
		return nil, ErrUnsafeDestination
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxManifestBytes+1))
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxManifestBytes {
		return nil, fmt.Errorf("fragment artifact manifest exceeds %d bytes", maxManifestBytes)
	}
	var state manifestState
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	if decoder.Decode(&state) != nil || decoder.Decode(&struct{}{}) != io.EOF || state.Hash != hash || len(state.Artifacts) > 10000 {
		return nil, fmt.Errorf("invalid fragment artifact manifest")
	}
	for index, artifact := range state.Artifacts {
		if index < 0 || artifact.Bytes <= 0 || len(artifact.SHA256) != 64 {
			return nil, fmt.Errorf("invalid fragment artifact manifest")
		}
	}
	return &artifactManifest{path: path, state: state}, nil
}
func (manifest *artifactManifest) Valid(index int, path string) bool {
	manifest.mu.Lock()
	known, present := manifest.state.Artifacts[index]
	manifest.mu.Unlock()
	if !present {
		return false
	} // Legacy state lacked integrity evidence; re-download safely.
	bytes, digest, err := digestFile(path)
	return err == nil && bytes == known.Bytes && digest == known.SHA256
}
func (manifest *artifactManifest) Record(index int, path string) error {
	bytes, digest, err := digestFile(path)
	if err != nil {
		return err
	}
	manifest.mu.Lock()
	defer manifest.mu.Unlock()
	if manifest.state.Artifacts == nil {
		manifest.state.Artifacts = make(map[int]artifact)
	}
	manifest.state.Artifacts[index] = artifact{Bytes: bytes, SHA256: digest}
	encoded, err := json.Marshal(manifest.state)
	if err != nil {
		return err
	}
	return writeManifestAtomically(manifest.path, encoded)
}

func writeManifestAtomically(path string, encoded []byte) error {
	if isSymlink(path) {
		return ErrUnsafeDestination
	}
	temporary := path + ".tmp"
	if isSymlink(temporary) {
		return ErrUnsafeDestination
	}
	if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(encoded); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return os.Rename(temporary, path)
}
func digestFile(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hasher := sha256.New()
	bytes, err := io.Copy(hasher, file)
	if err != nil {
		return 0, "", err
	}
	return bytes, hex.EncodeToString(hasher.Sum(nil)), nil
}
