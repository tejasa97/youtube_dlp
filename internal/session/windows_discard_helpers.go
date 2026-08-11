package session

import (
	"errors"
	"os"
	"strings"
)

const maxWindowsNTPathLength = 32767

const requiredWindowsDiscardVolumeFlags uint32 = 0x00010000 | 0x01000000 // FILE_SUPPORTS_OBJECT_IDS | FILE_SUPPORTS_OPEN_BY_FILE_ID

var errInvalidWindowsNTPath = errors.New("invalid Windows NT path")

// windowsNTPath converts an absolute Win32 path into the NT namespace used by
// NtCreateFile. It accepts drive-rooted, UNC, and already-extended forms, but
// never guesses at device, relative, or dot-segment paths.
func windowsNTPath(path string) (string, error) {
	if path == "" || len(path) > maxWindowsNTPathLength || strings.ContainsRune(path, 0) {
		return "", errInvalidWindowsNTPath
	}
	path = strings.ReplaceAll(path, "/", `\`)
	switch {
	case strings.HasPrefix(path, `\??\`):
		return windowsNTPathTail(path[4:])
	case strings.HasPrefix(path, `\\?\`):
		return windowsNTPathTail(path[4:])
	case strings.HasPrefix(path, `\\.\`), strings.HasPrefix(path, `\Device\`):
		return "", errInvalidWindowsNTPath
	case strings.HasPrefix(path, `\\`):
		return windowsNTUNCPath(path[2:])
	default:
		return windowsNTDrivePath(path)
	}
}

func windowsNTPathTail(path string) (string, error) {
	if strings.HasPrefix(strings.ToUpper(path), `UNC\`) {
		return windowsNTUNCPath(path[4:])
	}
	return windowsNTDrivePath(path)
}

func windowsNTDrivePath(path string) (string, error) {
	if len(path) < 3 || !windowsNTDriveLetter(path[0]) || path[1] != ':' || path[2] != '\\' {
		return "", errInvalidWindowsNTPath
	}
	if !validWindowsNTComponents(path[3:], false) {
		return "", errInvalidWindowsNTPath
	}
	return `\??\` + path, nil
}

func windowsNTUNCPath(path string) (string, error) {
	if !validWindowsNTComponents(path, true) {
		return "", errInvalidWindowsNTPath
	}
	return `\??\UNC\` + path, nil
}

func validWindowsNTComponents(path string, unc bool) bool {
	components := strings.Split(path, `\`)
	if unc {
		if len(components) < 2 || components[0] == "" || components[1] == "" {
			return false
		}
	}
	for index, component := range components {
		if component == "" {
			if index == len(components)-1 {
				continue
			}
			return false
		}
		if component == "." || component == ".." || strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") ||
			strings.ContainsAny(component, `<>:"|?*`) {
			return false
		}
	}
	return true
}

func windowsNTDriveLetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func windowsDiscardMissingStatus(status uint32) bool {
	switch status {
	case 0xC000000F, // STATUS_NO_SUCH_FILE
		0xC0000034, // STATUS_OBJECT_NAME_NOT_FOUND
		0xC000003A: // STATUS_OBJECT_PATH_NOT_FOUND
		return true
	default:
		return false
	}
}

func normalizeWindowsDiscardOpenError(status uint32) error {
	if windowsDiscardMissingStatus(status) {
		return os.ErrNotExist
	}
	return nil
}

func windowsDiscardStableVolumeCapabilities(flags uint32) bool {
	return flags&requiredWindowsDiscardVolumeFlags == requiredWindowsDiscardVolumeFlags
}
