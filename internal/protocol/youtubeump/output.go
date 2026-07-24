package youtubeump

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateDestination(root, destination string) error {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	destinationAbs, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(rootAbs, destinationAbs)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ErrUnsafeDestination
	}
	current := rootAbs
	components := strings.Split(filepath.Dir(relative), string(filepath.Separator))
	for _, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		if pathSymlink(current) {
			return ErrUnsafeDestination
		}
	}
	return nil
}

func outputPreflight(destination string, overwrite bool) error {
	info, err := os.Lstat(destination)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !overwrite {
		return ErrDestinationExists
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafeDestination
	}
	return nil
}

func pathSymlink(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode()&os.ModeSymlink != 0
}

func regularOrAbsent(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrUnsafeDestination
	}
	return nil
}

type outputFile struct {
	path      string
	file      *os.File
	published bool
}

func openOutputTemp(destination string) (*outputFile, error) {
	if err := regularOrAbsent(destination); err != nil {
		return nil, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), "."+filepath.Base(destination)+".tmp-*")
	if err != nil {
		return nil, fmt.Errorf("create temporary output: %w", err)
	}
	return &outputFile{path: temporary.Name(), file: temporary}, nil
}

func (output *outputFile) closeAndRemove() {
	if output.file != nil {
		_ = output.file.Close()
		output.file = nil
	}
	if !output.published {
		_ = os.Remove(output.path)
	}
}

func (output *outputFile) syncClose() error {
	if output.file == nil {
		return nil
	}
	if err := output.file.Sync(); err != nil {
		_ = output.file.Close()
		output.file = nil
		return fmt.Errorf("sync output: %w", err)
	}
	if err := output.file.Close(); err != nil {
		output.file = nil
		return err
	}
	output.file = nil
	return nil
}

func publishOutput(tempPath, destination string, overwrite bool) error {
	if overwrite {
		return replaceDestination(tempPath, destination)
	}
	return installDestination(tempPath, destination)
}
