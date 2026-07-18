package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

type semanticVersion [3]uint64

func parseSemver(input string) (semanticVersion, error) {
	var result semanticVersion
	if input == "" || len(input) > 64 || strings.ContainsAny(input, "+- \t\r\n\x00") {
		return result, fmt.Errorf("%w: semantic version", ErrInvalidContract)
	}
	parts := strings.Split(input, ".")
	if len(parts) != len(result) {
		return result, fmt.Errorf("%w: semantic version", ErrInvalidContract)
	}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return result, fmt.Errorf("%w: semantic version", ErrInvalidContract)
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return result, fmt.Errorf("%w: semantic version", ErrInvalidContract)
		}
		result[index] = value
	}
	return result, nil
}

func compareSemver(left, right string) (int, error) {
	leftVersion, err := parseSemver(left)
	if err != nil {
		return 0, err
	}
	rightVersion, err := parseSemver(right)
	if err != nil {
		return 0, err
	}
	for index := range leftVersion {
		if leftVersion[index] < rightVersion[index] {
			return -1, nil
		}
		if leftVersion[index] > rightVersion[index] {
			return 1, nil
		}
	}
	return 0, nil
}
