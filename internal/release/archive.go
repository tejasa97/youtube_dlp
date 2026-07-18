package release

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"time"
)

type ArchiveFormat string

const (
	FormatTarGzip ArchiveFormat = "tar.gz"
	FormatZIP     ArchiveFormat = "zip"
)

type Entry struct {
	Name       string
	Data       []byte
	Executable bool
}

func WriteArchive(ctx context.Context, writer io.Writer, format ArchiveFormat, entries []Entry, epoch time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reasonableEpoch(epoch) || len(entries) == 0 || len(entries) > maxEntries {
		return ErrInvalidInput
	}
	ordered, err := validateEntries(entries)
	if err != nil {
		return err
	}
	switch format {
	case FormatTarGzip:
		return writeTarGzip(ctx, writer, ordered, epoch)
	case FormatZIP:
		return writeZIP(ctx, writer, ordered, epoch)
	default:
		return fmt.Errorf("%w: archive format", ErrInvalidInput)
	}
}

func validateEntries(entries []Entry) ([]Entry, error) {
	ordered := append([]Entry(nil), entries...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	seen := make(map[string]struct{}, len(ordered))
	var total int64
	for _, entry := range ordered {
		if !safeArchivePath(entry.Name) || len(entry.Data) > maxEntryBytes {
			return nil, ErrUnsafePath
		}
		total += int64(len(entry.Data))
		if total > maxTotalBytes {
			return nil, ErrTooLarge
		}
		folded := strings.ToLower(entry.Name)
		if _, duplicate := seen[folded]; duplicate {
			return nil, fmt.Errorf("%w: duplicate archive entry", ErrUnsafePath)
		}
		seen[folded] = struct{}{}
	}
	return ordered, nil
}

func writeTarGzip(ctx context.Context, writer io.Writer, entries []Entry, epoch time.Time) error {
	gzipWriter, err := gzip.NewWriterLevel(writer, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("%w: create gzip", ErrIO)
	}
	gzipWriter.Header.ModTime = epoch
	gzipWriter.Header.OS = 255
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			return err
		}
		mode := int64(0o644)
		if entry.Executable {
			mode = 0o755
		}
		header := &tar.Header{Name: entry.Name, Mode: mode, Size: int64(len(entry.Data)), ModTime: epoch, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR}
		if err := tarWriter.WriteHeader(header); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			return fmt.Errorf("%w: write tar header", ErrIO)
		}
		if _, err := tarWriter.Write(entry.Data); err != nil {
			tarWriter.Close()
			gzipWriter.Close()
			return fmt.Errorf("%w: write tar entry", ErrIO)
		}
	}
	if err := tarWriter.Close(); err != nil {
		gzipWriter.Close()
		return fmt.Errorf("%w: close tar", ErrIO)
	}
	if err := gzipWriter.Close(); err != nil {
		return fmt.Errorf("%w: close gzip", ErrIO)
	}
	return nil
}

func writeZIP(ctx context.Context, writer io.Writer, entries []Entry, epoch time.Time) error {
	zipWriter := zip.NewWriter(writer)
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			zipWriter.Close()
			return err
		}
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Deflate}
		header.Modified = epoch
		if entry.Executable {
			header.SetMode(0o755)
		} else {
			header.SetMode(0o644)
		}
		entryWriter, err := zipWriter.CreateHeader(header)
		if err != nil {
			zipWriter.Close()
			return fmt.Errorf("%w: write zip header", ErrIO)
		}
		if _, err := entryWriter.Write(entry.Data); err != nil {
			zipWriter.Close()
			return fmt.Errorf("%w: write zip entry", ErrIO)
		}
	}
	if err := zipWriter.Close(); err != nil {
		return fmt.Errorf("%w: close zip", ErrIO)
	}
	return nil
}

func safeArchivePath(value string) bool {
	if value == "" || len(value) > 100 || strings.IndexByte(value, 0) >= 0 || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	if cleaned != value || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") || windowsReserved(component) || strings.Contains(component, ":") {
			return false
		}
		for _, character := range component {
			if character < 0x21 || character > 0x7e {
				return false
			}
		}
	}
	return true
}
