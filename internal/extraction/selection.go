package extraction

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	maxSelectionRules      = 128
	maxSelectionRuleBytes  = 256
	maxSelectionTotalBytes = 8 << 10
	selectionEndSentinel   = "\x00end"
)

var (
	// ErrInvalidSelection identifies malformed or over-budget extractor
	// selection rules. It is returned before a request can reach the network.
	ErrInvalidSelection = errors.New("invalid extractor selection")
	// ErrSelectionDisabled identifies a known extractor that was excluded by
	// the active selection policy.
	ErrSelectionDisabled = errors.New("extractor disabled by selection policy")
)

// ExplicitOnlyProvider marks a provider that is intentionally unavailable
// to automatic URL routing. Installed signed plugins use this contract: a
// caller may select one by its exact PluginID, but extractor rules must not
// turn it into an automatic fallback.
type ExplicitOnlyProvider interface {
	ExplicitOnly()
}

type selectionPolicy struct {
	configured bool
	ordered    []string
	allowed    map[string]struct{}
}

// validateSelectionRules checks the syntax and resource bounds shared by the
// public Request and registry compilation boundaries. Empty comma-separated
// fields are already discarded by the CLI, but API callers may provide them
// directly and receive the same behavior.
func validateSelectionRules(rules []string) error {
	if len(rules) > maxSelectionRules {
		return fmt.Errorf("%w: too many rules", ErrInvalidSelection)
	}
	total := 0
	for index, raw := range rules {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		if len(rule) > maxSelectionRuleBytes {
			return fmt.Errorf("%w: rule %d is too long", ErrInvalidSelection, index)
		}
		total += len(rule)
		if total > maxSelectionTotalBytes {
			return fmt.Errorf("%w: rules are too large", ErrInvalidSelection)
		}
		if strings.ContainsAny(rule, "\x00\r\n") || !utf8.ValidString(rule) {
			return fmt.Errorf("%w: rule %d is malformed", ErrInvalidSelection, index)
		}
		if rule == "-" {
			return fmt.Errorf("%w: rule %d has no extractor", ErrInvalidSelection, index)
		}
		if strings.HasPrefix(rule, "-") {
			rule = strings.TrimSpace(rule[1:])
			if rule == "" {
				return fmt.Errorf("%w: rule %d has no extractor", ErrInvalidSelection, index)
			}
		}
		if rule == "all" || rule == "default" || rule == "end" {
			continue
		}
		if _, err := selectionRegexp(rule); err != nil {
			return fmt.Errorf("%w: rule %d is malformed", ErrInvalidSelection, index)
		}
	}
	return nil
}

// ValidateSelectionRules checks only grammar and resource bounds. Registry
// membership is checked when ConfigureSelection sees the concrete product
// registry, before network setup.
func ValidateSelectionRules(rules []string) error {
	return validateSelectionRules(rules)
}

func selectionRegexp(rule string) (*regexp.Regexp, error) {
	return regexp.Compile("(?i)^(?:" + rule + ")$")
}

func selectionRuleName(name string) string {
	if name == selectionEndSentinel {
		return "end"
	}
	return name
}

func compileSelectionPolicy[R any](rules []string, candidates []Provider[R]) (selectionPolicy, error) {
	if len(rules) == 0 {
		return selectionPolicy{}, nil
	}
	if err := validateSelectionRules(rules); err != nil {
		return selectionPolicy{}, err
	}

	policy := selectionPolicy{configured: true, allowed: make(map[string]struct{})}
	all := make([]string, 0, len(candidates))
	defaults := make([]string, 0, len(candidates))
	byName := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		name := candidate.Name()
		key := strings.ToLower(name)
		if name == "" || key == "" {
			continue
		}
		if _, exists := byName[key]; exists {
			continue
		}
		byName[key] = name
		all = append(all, name)
		if _, explicitOnly := candidate.(ExplicitOnlyProvider); !explicitOnly {
			defaults = append(defaults, name)
		}
	}
	// UnsupportedURLIE is the pinned `end` entry. It is deliberately not a
	// candidate and is retained at the exact position where the alias rule
	// placed it in the ordered set.
	all = append(all, selectionEndSentinel)

	add := func(names []string) {
		for _, name := range names {
			key := strings.ToLower(name)
			if _, exists := policy.allowed[key]; exists {
				continue
			}
			policy.allowed[key] = struct{}{}
			policy.ordered = append(policy.ordered, name)
		}
	}
	remove := func(names []string) {
		for _, name := range names {
			key := strings.ToLower(name)
			delete(policy.allowed, key)
		}
		if len(policy.ordered) == 0 {
			return
		}
		filtered := policy.ordered[:0]
		for _, name := range policy.ordered {
			if _, exists := policy.allowed[strings.ToLower(name)]; exists {
				filtered = append(filtered, name)
			}
		}
		policy.ordered = filtered
	}

	for index, raw := range rules {
		rule := strings.TrimSpace(raw)
		if rule == "" {
			continue
		}
		exclude := strings.HasPrefix(rule, "-")
		if exclude {
			rule = strings.TrimSpace(rule[1:])
		}
		var matches []string
		switch rule {
		case "all":
			matches = all
		case "default":
			matches = defaults
		case "end":
			matches = []string{selectionEndSentinel}
		default:
			pattern, err := selectionRegexp(rule)
			if err != nil {
				return selectionPolicy{}, fmt.Errorf("%w: rule %d is malformed", ErrInvalidSelection, index)
			}
			for _, name := range all {
				if pattern.MatchString(selectionRuleName(name)) {
					matches = append(matches, name)
				}
			}
		}
		if exclude {
			remove(matches)
		} else {
			add(matches)
		}
	}
	return policy, nil
}
