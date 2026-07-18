//go:build !darwin && !linux

package pack

func syncDirectory(string) error       { return ErrPlatformSecurity }
func syncTreeDirectories(string) error { return ErrPlatformSecurity }
