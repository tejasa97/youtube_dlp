package session

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// discardDirectory and discardEntryHandle keep the directory authority used
// by destructive cleanup open for the whole operation. Paths are retained
// only for diagnostics and sync fault seams; traversal and mutation use the
// platform-specific handle operations below.
type discardDirectory struct {
	file     *os.File
	path     string
	identity string
}

type discardEntryHandle struct {
	file      *os.File
	parent    *discardDirectory
	path      string
	name      string
	expected  os.FileInfo
	identity  string
	directory bool
}

func (directory *discardDirectory) close() error {
	if directory == nil || directory.file == nil {
		return nil
	}
	err := directory.file.Close()
	directory.file = nil
	return err
}

func (entry *discardEntryHandle) close() error {
	if entry == nil || entry.file == nil {
		return nil
	}
	err := entry.file.Close()
	entry.file = nil
	return err
}

func openDiscardTarget(path, expectedIdentity string) (*discardDirectory, *discardEntryHandle, error) {
	parent, err := openDiscardRoot(filepath.Dir(path), "")
	if err != nil {
		return nil, nil, err
	}
	name := filepath.Base(path)
	target, err := openDiscardEntry(parent, name, nil, true)
	if err != nil {
		_ = parent.close()
		return nil, nil, err
	}
	if expectedIdentity != "" && target.identity != expectedIdentity {
		_ = target.close()
		_ = parent.close()
		return nil, nil, ErrNeedsReconciliation
	}
	return parent, target, nil
}

func removeDiscardNamedFile(directory *discardDirectory, name string) error {
	if fault := discardFault(discardFaultChildRemoval); fault != nil {
		return fault
	}
	entry, err := openDiscardEntry(directory, name, nil, false)
	if err != nil {
		return err
	}
	removeErr := entry.remove()
	closeErr := entry.close()
	if removeErr != nil {
		return removeErr
	}
	return closeErr
}

func removeDiscardChildren(path string) error {
	return removeDiscardChildrenWithBudget(path, &discardTreeBudget{})
}

func removeDiscardChildrenWithBudget(path string, budget *discardTreeBudget) error {
	directory, err := openDiscardRoot(path, "")
	if err != nil {
		return err
	}
	defer directory.close()
	return removeDiscardChildrenFromHandle(directory, budget)
}

func removeDiscardChildrenFromHandle(directory *discardDirectory, budget *discardTreeBudget) error {
	remaining := discardTreeEntryLimit - budget.entries
	if remaining <= 0 {
		return ErrNeedsReconciliation
	}

	// There are at most two protected entries in a workspace directory. Read a
	// small bounded surplus so marker/lease names do not consume useful
	// deletion budget. A full window is treated as truncated; this is
	// deliberately conservative and causes another bounded pass even when the
	// directory contains exactly that many entries.
	const protectedEntries = 2
	readWindow := remaining + protectedEntries + 1
	entries, readErr := directory.file.ReadDir(readWindow)
	if readErr != nil && !errorsIsEOF(readErr) {
		return readErr
	}
	sortDiscardEntries(entries)

	progress := false
	truncated := len(entries) == readWindow
	for _, entry := range entries {
		if entry.Name() == discardMarkerName || entry.Name() == LeaseFileName {
			continue
		}
		if budget.entries >= discardTreeEntryLimit {
			if progress {
				return errDiscardTreeBudget
			}
			return ErrNeedsReconciliation
		}
		if fault := discardFault(discardFaultChildRemoval); fault != nil {
			return fault
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		isDirectory := info.IsDir()
		if info.Mode()&os.ModeSymlink != 0 || (!isDirectory && !info.Mode().IsRegular()) {
			return ErrUnsafePath
		}
		discardBeforeChildOpen(filepath.Join(directory.path, entry.Name()))
		child, err := openDiscardEntry(directory, entry.Name(), info, isDirectory)
		if err != nil {
			return err
		}
		if isDirectory {
			if budget.depth >= discardTreeDepthLimit {
				_ = child.close()
				if progress {
					return errDiscardTreeBudget
				}
				return ErrNeedsReconciliation
			}
			budget.depth++
			removeErr := removeDiscardChildrenFromHandle(&discardDirectory{
				file: child.file, path: child.path, identity: child.identity,
			}, budget)
			budget.depth--
			if removeErr != nil {
				// The recursive helper consumed the child file. Reopen is not
				// safe or necessary: the child handle remains the mutation
				// authority and is closed only after this branch settles.
				returnWithClose := removeErr
				_ = child.close()
				return returnWithClose
			}
		}
		if budget.entries >= discardTreeEntryLimit {
			_ = child.close()
			if progress {
				return errDiscardTreeBudget
			}
			return ErrNeedsReconciliation
		}
		if err := child.remove(); err != nil {
			_ = child.close()
			return err
		}
		if err := child.close(); err != nil {
			return err
		}
		budget.entries++
		progress = true
		if err := discardSyncHandle(directory); err != nil {
			return err
		}
	}
	if truncated {
		if progress {
			return errDiscardTreeBudget
		}
		return ErrNeedsReconciliation
	}
	return nil
}

func errorsIsEOF(err error) bool { return errors.Is(err, io.EOF) }

func sortDiscardEntries(entries []fs.DirEntry) {
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })
}

func validDiscardEntryName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsRune(name, 0) && !strings.ContainsAny(name, `/\`)
}
