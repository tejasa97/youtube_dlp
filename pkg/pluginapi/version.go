package pluginapi

// Version constructs the ABI's compact wire version. Version(1, 0) retains
// the Phase 1 value 1; later minors use the upper/lower 16-bit representation.
func Version(major, minor uint16) uint32 {
	if minor == 0 {
		return uint32(major)
	}
	return uint32(major)<<16 | uint32(minor)
}

func VersionParts(version uint32) (major, minor uint16) {
	if version <= 0xffff {
		return uint16(version), 0
	}
	return uint16(version >> 16), uint16(version)
}

func Compatible(left, right uint32) bool {
	leftMajor, _ := VersionParts(left)
	rightMajor, _ := VersionParts(right)
	return leftMajor != 0 && leftMajor == rightMajor
}

func CompareVersions(left, right uint32) int {
	lm, ln := VersionParts(left)
	rm, rn := VersionParts(right)
	if lm < rm || lm == rm && ln < rn {
		return -1
	}
	if lm == rm && ln == rn {
		return 0
	}
	return 1
}
