package format

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// filterNumericValue holds a comparison constant parsed from filter text.
// Integer values preserve pinned parse_filesize magnitudes (including values
// beyond int64) after float64 multiplication and round-half-to-even.
type filterNumericValue struct {
	integer  *big.Int
	floating float64
	isInt    bool
}

func (value filterNumericValue) asFloat() float64 {
	if value.isInt {
		f, _ := new(big.Float).SetInt(value.integer).Float64()
		return f
	}
	return value.floating
}

// parseFilterNumber parses a numeric-filter value using pinned grammar selection:
// float(value), else parse_filesize(value), else parse_filesize(value+"B").
func parseFilterNumber(raw string) (filterNumericValue, bool) {
	if raw == "" {
		return filterNumericValue{}, false
	}
	if number, ok := parseFilterFloat(raw); ok {
		return number, true
	}
	if size, ok := parseFilterFilesize(raw); ok {
		return filterNumericValue{integer: size, isInt: true}, true
	}
	if size, ok := parseFilterFilesize(raw + "B"); ok {
		return filterNumericValue{integer: size, isInt: true}, true
	}
	return filterNumericValue{}, false
}

func parseFilterFloat(raw string) (filterNumericValue, bool) {
	if strings.ContainsAny(raw, "eE+") || strings.HasPrefix(raw, "-") {
		return filterNumericValue{}, false
	}
	if strings.Count(raw, ".") > 1 {
		return filterNumericValue{}, false
	}
	for _, r := range raw {
		if r != '.' && (r < '0' || r > '9') {
			return filterNumericValue{}, false
		}
	}
	if raw == "." || raw == "" {
		return filterNumericValue{}, false
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	if err != nil && !math.IsInf(parsed, 1) || math.IsNaN(parsed) {
		return filterNumericValue{}, false
	}
	// Pinned _build_format_filter calls float(value) first, including for
	// digit-only literals. Keep the resulting binary64 rounding observable.
	return filterNumericValue{floating: parsed}, true
}

// filterFilesizeUnits is the pinned parse_filesize unit table through yotta,
// including intentionally irregular case mappings. Multipliers are float64 to
// reproduce Python float multiplication before round-half-to-even.
var filterFilesizeUnits = map[string]float64{
	"B": 1, "b": 1, "bytes": 1,
	"KiB": 1024, "KB": 1000, "kB": 1024, "Kb": 1000, "kb": 1000,
	"kilobytes": 1000, "kibibytes": 1024,
	"MiB": 1024 * 1024, "MB": 1e6, "mB": 1024 * 1024, "Mb": 1e6, "mb": 1e6,
	"megabytes": 1e6, "mebibytes": 1024 * 1024,
	"GiB": 1024 * 1024 * 1024, "GB": 1e9, "gB": 1024 * 1024 * 1024, "Gb": 1e9, "gb": 1e9,
	"gigabytes": 1e9, "gibibytes": 1024 * 1024 * 1024,
	"TiB": pow1024(4), "TB": 1e12, "tB": pow1024(4), "Tb": 1e12, "tb": 1e12,
	"terabytes": 1e12, "tebibytes": pow1024(4),
	"PiB": pow1024(5), "PB": 1e15, "pB": pow1024(5), "Pb": 1e15, "pb": 1e15,
	"petabytes": 1e15, "pebibytes": pow1024(5),
	"EiB": pow1024(6), "EB": 1e18, "eB": pow1024(6), "Eb": 1e18, "eb": 1e18,
	"exabytes": 1e18, "exbibytes": pow1024(6),
	"ZiB": pow1024(7), "ZB": 1e21, "zB": pow1024(7), "Zb": 1e21, "zb": 1e21,
	"zettabytes": 1e21, "zebibytes": pow1024(7),
	"YiB": pow1024(8), "YB": 1e24, "yB": pow1024(8), "Yb": 1e24, "yb": 1e24,
	"yottabytes": 1e24, "yobibytes": pow1024(8),
}

func pow1024(exp int) float64 {
	value := 1.0
	for index := 0; index < exp; index++ {
		value *= 1024
	}
	return value
}

func parseFilterFilesize(raw string) (*big.Int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, false
	}
	index := 0
	for index < len(raw) {
		r, width := utf8.DecodeRuneInString(raw[index:])
		if r == ',' || r == '.' || (r >= '0' && r <= '9') {
			index += width
			continue
		}
		break
	}
	if index == 0 {
		return nil, false
	}
	numText := strings.ReplaceAll(raw[:index], ",", ".")
	if strings.Count(numText, ".") > 1 {
		return nil, false
	}
	number, err := strconv.ParseFloat(numText, 64)
	if err != nil || number < 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return nil, false
	}
	rest := strings.TrimLeftFunc(raw[index:], unicode.IsSpace)
	if rest == "" {
		return nil, false
	}
	unitEnd := 0
	for unitEnd < len(rest) {
		r, width := utf8.DecodeRuneInString(rest[unitEnd:])
		if unicode.IsLetter(r) {
			unitEnd += width
			continue
		}
		break
	}
	if unitEnd == 0 {
		return nil, false
	}
	unit := rest[:unitEnd]
	if unitEnd < len(rest) {
		r, _ := utf8.DecodeRuneInString(rest[unitEnd:])
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			return nil, false
		}
	}
	mult, ok := filterFilesizeUnits[unit]
	if !ok {
		return nil, false
	}
	// Pinned Python: round(float(num) * float(mult)) with banker's rounding.
	product := number * mult
	if math.IsNaN(product) || math.IsInf(product, 0) {
		return nil, false
	}
	rounded := math.RoundToEven(product)
	integer, ok := float64ToBigInt(rounded)
	if !ok {
		return nil, false
	}
	return integer, true
}

func float64ToBigInt(value float64) (*big.Int, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, false
	}
	text := strconv.FormatFloat(value, 'f', 0, 64)
	integer, ok := new(big.Int).SetString(text, 10)
	return integer, ok
}

func compareFilterNumbers(left filterNumericValue, right filterNumericValue, op filterOperation) (bool, error) {
	if left.isInt || right.isInt {
		if !left.isInt && math.IsInf(left.floating, 0) {
			return compareNumericOrder(int(math.Copysign(1, left.floating)), op), nil
		}
		if !right.isInt && math.IsInf(right.floating, 0) {
			return compareNumericOrder(-int(math.Copysign(1, right.floating)), op), nil
		}
		var leftRat, rightRat *big.Rat
		if left.isInt {
			leftRat = new(big.Rat).SetInt(left.integer)
		} else {
			leftRat = new(big.Rat).SetFloat64(left.floating)
		}
		if right.isInt {
			rightRat = new(big.Rat).SetInt(right.integer)
		} else {
			rightRat = new(big.Rat).SetFloat64(right.floating)
		}
		if leftRat == nil || rightRat == nil {
			return false, fmtFilterEval("non-finite numeric comparison")
		}
		cmp := leftRat.Cmp(rightRat)
		switch op {
		case filterOpEQ:
			return cmp == 0, nil
		case filterOpNE:
			return cmp != 0, nil
		case filterOpGT:
			return cmp > 0, nil
		case filterOpGE:
			return cmp >= 0, nil
		case filterOpLT:
			return cmp < 0, nil
		case filterOpLE:
			return cmp <= 0, nil
		}
	}
	l, r := left.asFloat(), right.asFloat()
	switch op {
	case filterOpEQ:
		return l == r, nil
	case filterOpNE:
		return l != r, nil
	case filterOpGT:
		return l > r, nil
	case filterOpGE:
		return l >= r, nil
	case filterOpLT:
		return l < r, nil
	case filterOpLE:
		return l <= r, nil
	default:
		return false, fmtFilterEval("unknown numeric operator")
	}
}

func compareNumericOrder(cmp int, op filterOperation) bool {
	switch op {
	case filterOpEQ:
		return cmp == 0
	case filterOpNE:
		return cmp != 0
	case filterOpGT:
		return cmp > 0
	case filterOpGE:
		return cmp >= 0
	case filterOpLT:
		return cmp < 0
	case filterOpLE:
		return cmp <= 0
	default:
		return false
	}
}

func int64Numeric(value int64) filterNumericValue {
	return filterNumericValue{integer: big.NewInt(value), isInt: true}
}
