//go:build !darwin && !linux

package pack

import "os"

func secureOwnership(os.FileInfo) bool { return false }

func singleLink(os.FileInfo) bool { return false }

// The standard library does not expose sufficient owner/ACL information to
// make the same safe-root claim on these platforms. Verification remains
// portable; installation fails closed.
func secureInstallPlatform() bool { return false }
