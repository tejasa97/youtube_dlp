//go:build !darwin && !linux

package pack

func acquireLock(string) (func(), error) { return nil, ErrPlatformSecurity }
