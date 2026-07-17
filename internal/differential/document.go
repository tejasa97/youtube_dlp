// Package differential compares normalized Go snapshots with attributable
// reference snapshots without invoking the reference implementation.
package differential

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ytdlp-go/ytdlp/internal/value"
)

const (
	SchemaVersion    = 1
	MaxDocumentBytes = 16 << 20
)

var requiredSections = map[string]value.Kind{
	"metadata":  value.KindObject,
	"formats":   value.KindList,
	"playlists": value.KindList,
	"events":    value.KindList,
	"selection": value.KindObject,
	"outputs":   value.KindList,
}

// Document is the normalized comparison envelope. The ordered Value tree is
// retained so field-order differences remain observable.
type Document struct {
	root value.Value
}

// ParseDocument decodes and validates a bounded normalized snapshot.
func ParseDocument(reader io.Reader) (Document, error) {
	limited := io.LimitReader(reader, MaxDocumentBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return Document{}, fmt.Errorf("read document: %w", err)
	}
	if len(data) > MaxDocumentBytes {
		return Document{}, fmt.Errorf("document exceeds %d bytes", MaxDocumentBytes)
	}
	var root value.Value
	if err := root.UnmarshalJSON(data); err != nil {
		return Document{}, fmt.Errorf("decode document: %w", err)
	}
	document := Document{root: root}
	if err := document.Validate(); err != nil {
		return Document{}, err
	}
	return document, nil
}

// LoadDocument reads a normalized snapshot from disk.
func LoadDocument(path string) (Document, error) {
	file, err := os.Open(path)
	if err != nil {
		return Document{}, fmt.Errorf("open document: %w", err)
	}
	defer file.Close()
	document, err := ParseDocument(file)
	if err != nil {
		return Document{}, fmt.Errorf("load %s: %w", path, err)
	}
	return document, nil
}

// Validate checks the stable comparison envelope while permitting additional
// capability-specific top-level sections.
func (document Document) Validate() error {
	root, ok := document.root.Object()
	if !ok {
		return errors.New("document root must be an object")
	}
	version, ok := root.Lookup("schema_version").Int()
	if !ok || version != SchemaVersion {
		return fmt.Errorf("schema_version must be integer %d", SchemaVersion)
	}
	for name, kind := range requiredSections {
		section := root.Lookup(name)
		if section.IsMissing() {
			return fmt.Errorf("required section %q is missing", name)
		}
		if section.Kind() != kind {
			return fmt.Errorf("section %q has kind %s, want %s", name, section.Kind(), kind)
		}
	}
	return nil
}

func (document Document) rootValue() value.Value {
	return document.root
}
