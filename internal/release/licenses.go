package release

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

type License struct {
	Component string
	Version   string
	SPDX      string
	Text      []byte
}

// WriteLicenseBundle produces a complete, stable concatenation suitable for
// shipping beside binary artifacts. Inputs are explicit so missing dependency
// notices cannot be silently fetched or guessed.
func WriteLicenseBundle(writer io.Writer, licenses []License) error {
	if len(licenses) == 0 || len(licenses) > maxComponents {
		return ErrInvalidInput
	}
	ordered := append([]License(nil), licenses...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Component != ordered[j].Component {
			return ordered[i].Component < ordered[j].Component
		}
		return ordered[i].Version < ordered[j].Version
	})
	seen := make(map[string]struct{}, len(ordered))
	for _, license := range ordered {
		if !validComponent(license.Component) || !validVersion(license.Version) || !validSPDXExpression(license.SPDX) || len(license.Text) == 0 || len(license.Text) > maxLicenseText || strings.IndexByte(string(license.Text), 0) >= 0 {
			return ErrInvalidInput
		}
		identity := license.Component + "@" + license.Version
		if _, duplicate := seen[identity]; duplicate {
			return ErrInvalidInput
		}
		seen[identity] = struct{}{}
		text := strings.ReplaceAll(string(license.Text), "\r\n", "\n")
		text = strings.TrimRight(text, "\n") + "\n"
		if _, err := fmt.Fprintf(writer, "===== %s %s (%s) =====\n%s\n", license.Component, license.Version, license.SPDX, text); err != nil {
			return fmt.Errorf("%w: write license bundle", ErrIO)
		}
	}
	return nil
}

func validComponent(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\x00\r\n\t")
}

// ValidateLicenseCoverage requires an exact component/version match between an
// SBOM and the bundled notices. It catches both omitted and stale licenses.
func ValidateLicenseCoverage(components []Component, licenses []License) error {
	if len(components) == 0 || len(components) > maxComponents || len(licenses) == 0 || len(licenses) > maxComponents {
		return ErrInvalidInput
	}
	expected := make(map[string]struct{}, len(components))
	for _, component := range components {
		if !validComponent(component.Name) || !validVersion(component.Version) {
			return ErrInvalidInput
		}
		expected[component.Name+"@"+component.Version] = struct{}{}
	}
	seen := make(map[string]struct{}, len(licenses))
	for _, license := range licenses {
		identity := license.Component + "@" + license.Version
		if _, ok := expected[identity]; !ok {
			return ErrInvalidInput
		}
		if _, duplicate := seen[identity]; duplicate {
			return ErrInvalidInput
		}
		seen[identity] = struct{}{}
	}
	if len(seen) != len(expected) {
		return ErrInvalidInput
	}
	return nil
}

func validSPDXExpression(value string) bool {
	if value == "" || len(value) > 256 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("-.+() ", character) {
			continue
		}
		return false
	}
	return true
}
