package pack

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion struct {
	core [3]uint64
	pre  []string
}

func parseVersion(input string) (semanticVersion, error) {
	var result semanticVersion
	if input == "" || len(input) > 64 || strings.TrimSpace(input) != input || strings.ContainsAny(input, "+\x00") {
		return result, fmt.Errorf("%w: invalid semantic version", ErrInvalidManifest)
	}
	coreText, preText, hasPre := strings.Cut(input, "-")
	parts := strings.Split(coreText, ".")
	if len(parts) != 3 {
		return result, fmt.Errorf("%w: version must have three numeric components", ErrInvalidManifest)
	}
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, fmt.Errorf("%w: invalid version component", ErrInvalidManifest)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return result, fmt.Errorf("%w: invalid version component", ErrInvalidManifest)
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("%w: oversized version component", ErrInvalidManifest)
		}
		result.core[index] = value
	}
	if hasPre {
		if preText == "" {
			return result, fmt.Errorf("%w: empty prerelease", ErrInvalidManifest)
		}
		result.pre = strings.Split(preText, ".")
		for _, part := range result.pre {
			if part == "" || len(part) > 32 {
				return semanticVersion{}, fmt.Errorf("%w: invalid prerelease", ErrInvalidManifest)
			}
			numeric := true
			for _, character := range part {
				if !((character >= '0' && character <= '9') || (character >= 'A' && character <= 'Z') || (character >= 'a' && character <= 'z') || character == '-') {
					return semanticVersion{}, fmt.Errorf("%w: invalid prerelease", ErrInvalidManifest)
				}
				if character < '0' || character > '9' {
					numeric = false
				}
			}
			if numeric && len(part) > 1 && part[0] == '0' {
				return semanticVersion{}, fmt.Errorf("%w: invalid numeric prerelease", ErrInvalidManifest)
			}
		}
	}
	return result, nil
}

func compareVersions(leftText, rightText string) (int, error) {
	left, err := parseVersion(leftText)
	if err != nil {
		return 0, err
	}
	right, err := parseVersion(rightText)
	if err != nil {
		return 0, err
	}
	for index := range left.core {
		if left.core[index] < right.core[index] {
			return -1, nil
		}
		if left.core[index] > right.core[index] {
			return 1, nil
		}
	}
	if len(left.pre) == 0 && len(right.pre) == 0 {
		return 0, nil
	}
	if len(left.pre) == 0 {
		return 1, nil
	}
	if len(right.pre) == 0 {
		return -1, nil
	}
	for index := 0; index < len(left.pre) && index < len(right.pre); index++ {
		leftPart, rightPart := left.pre[index], right.pre[index]
		leftNumber, leftErr := strconv.ParseUint(leftPart, 10, 64)
		rightNumber, rightErr := strconv.ParseUint(rightPart, 10, 64)
		switch {
		case leftErr == nil && rightErr == nil:
			if leftNumber < rightNumber {
				return -1, nil
			}
			if leftNumber > rightNumber {
				return 1, nil
			}
		case leftErr == nil:
			return -1, nil
		case rightErr == nil:
			return 1, nil
		default:
			if leftPart < rightPart {
				return -1, nil
			}
			if leftPart > rightPart {
				return 1, nil
			}
		}
	}
	if len(left.pre) < len(right.pre) {
		return -1, nil
	}
	if len(left.pre) > len(right.pre) {
		return 1, nil
	}
	return 0, nil
}
