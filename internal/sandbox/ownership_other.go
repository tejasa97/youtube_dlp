//go:build !darwin && !linux

package sandbox

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return false }

func trustedReadOwner(os.FileInfo) bool { return false }
