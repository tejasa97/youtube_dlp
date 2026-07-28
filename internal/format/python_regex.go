package format

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dlclark/regexp2"
)

const (
	maxTranslatedRegexBytes       = 16 << 10
	maxRegexNesting               = 64
	maxRegexGroups                = 64
	maxRegexPredicatesPerAST      = 32
	maxRegexInputBytes            = 64 << 10
	maxRegexInspectedBytesPerPlan = 64 << 20
	maxRegexAttemptsPerPlan       = 131072
	regexMatchTimeout             = 25 * time.Millisecond
	regexTimeoutClockPeriod       = 5 * time.Millisecond
	regexAggregateWallBudget      = 250 * time.Millisecond
)

var (
	regexTimeoutInit sync.Once
	errRegexTimeout  = errors.New("regular expression match timed out")
	errRegexBudget   = errors.New("regular expression budget exhausted")
)

type pythonRegex struct {
	re *regexp2.Regexp
}

type regexEvalBudget struct {
	attempts       int
	inspectedBytes int64
	wall           time.Duration
	started        time.Time
}

func newRegexEvalBudget() *regexEvalBudget {
	return &regexEvalBudget{started: time.Now()}
}

func initRegexTimeoutClock() {
	regexTimeoutInit.Do(func() {
		regexp2.SetTimeoutCheckPeriod(regexTimeoutClockPeriod)
	})
}

func compilePythonRegex(pattern string, start, end int) (*pythonRegex, error) {
	initRegexTimeoutClock()
	if len(pattern) > maxRegexBytes {
		return nil, selectorLimit(start, end, "regular expression exceeds size limit")
	}
	if !utf8.ValidString(pattern) {
		return nil, selectorSyntax(start, end, "regular expression is not valid UTF-8")
	}
	translated, err := translatePythonRegex(pattern)
	if err != nil {
		return nil, selectorSyntax(start, end, err.Error())
	}
	if len(translated) > maxTranslatedRegexBytes {
		return nil, selectorLimit(start, end, "translated regular expression exceeds size limit")
	}
	re, err := regexp2.Compile(translated, regexp2.None)
	if err != nil {
		return nil, selectorSyntax(start, end, "invalid regular expression")
	}
	re.MatchTimeout = regexMatchTimeout
	return &pythonRegex{re: re}, nil
}

func (expression *pythonRegex) search(input string, budget *regexEvalBudget) (bool, error) {
	if expression == nil || expression.re == nil {
		return false, fmt.Errorf("%w: missing regular expression", ErrFilterEvaluation)
	}
	if len(input) > maxRegexInputBytes {
		return false, selectorLimit(0, 0, "regular expression input exceeds size limit")
	}
	if !utf8.ValidString(input) {
		return false, fmt.Errorf("%w: regular expression input is not valid UTF-8", ErrFilterEvaluation)
	}
	if budget != nil {
		if budget.started.IsZero() {
			budget.started = time.Now()
		}
		budget.attempts++
		budget.inspectedBytes += int64(len(input))
		if budget.attempts > maxRegexAttemptsPerPlan {
			return false, selectorLimit(0, 0, "regular expression attempt budget exhausted")
		}
		if budget.inspectedBytes > maxRegexInspectedBytesPerPlan {
			return false, selectorLimit(0, 0, "regular expression inspected-byte budget exhausted")
		}
		if time.Since(budget.started) > regexAggregateWallBudget {
			return false, selectorLimit(0, 0, "regular expression wall budget exhausted")
		}
	}
	start := time.Now()
	matched, err := expression.re.MatchString(input)
	if budget != nil {
		budget.wall += time.Since(start)
		if budget.wall > regexAggregateWallBudget {
			return false, selectorLimit(0, 0, "regular expression wall budget exhausted")
		}
	}
	if err != nil {
		return false, sanitizeRegexMatchError(err)
	}
	return matched, nil
}

func sanitizeRegexMatchError(err error) error {
	if err == nil {
		return nil
	}
	// regexp2 timeout errors embed the complete input string; never surface them.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "timeout") || strings.Contains(message, "timed out") {
		return selectorLimit(0, 0, errRegexTimeout.Error())
	}
	return selectorLimit(0, 0, errRegexBudget.Error())
}

func countRegexPredicates(node *astNode) int {
	if node == nil {
		return 0
	}
	total := 0
	for index := range node.filters {
		if node.filters[index].predicate != nil && node.filters[index].predicate.expression != nil {
			total++
		}
	}
	for index := range node.children {
		total += countRegexPredicates(&node.children[index])
	}
	return total
}

func enforceRegexPredicateLimit(node *astNode) error {
	if count := countRegexPredicates(node); count > maxRegexPredicatesPerAST {
		return selectorLimit(node.span.start, node.span.end, "too many regular expression predicates")
	}
	return nil
}
