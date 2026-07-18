package telemetry

import "math/bits"

// Coverage summarizes a snapshot without changing its denominator. Overflow
// remains in the denominator, and Exact is false if any total saturated.
// BasisPoints is floor(successful * 10_000 / denominator) when Exact is true.
type Coverage struct {
	Denominator uint64 `json:"denominator"`
	Successful  uint64 `json:"successful"`
	Failed      uint64 `json:"failed"`
	Unsupported uint64 `json:"unsupported"`
	Fallback    uint64 `json:"fallback"`
	Overflow    uint64 `json:"overflow"`
	BasisPoints uint32 `json:"basis_points"`
	Exact       bool   `json:"exact"`
}

func CalculateCoverage(snapshot Snapshot) (Coverage, error) {
	if err := validateStructural(snapshot); err != nil {
		return Coverage{}, err
	}
	coverage := Coverage{Exact: true}
	add := func(destination *uint64, amount uint64) {
		if saturatingAdd(destination, amount) != 0 {
			coverage.Exact = false
		}
	}
	for _, count := range snapshot.Counts {
		switch count.Outcome {
		case OutcomeSuccess:
			add(&coverage.Successful, count.Count)
		case OutcomeError:
			add(&coverage.Failed, count.Count)
		case OutcomeUnsupported:
			add(&coverage.Unsupported, count.Count)
		case OutcomeFallback:
			add(&coverage.Fallback, count.Count)
		}
	}
	add(&coverage.Overflow, snapshot.Overflow.CellLimit)
	add(&coverage.Overflow, snapshot.Overflow.CounterSaturation)
	for _, total := range []uint64{coverage.Successful, coverage.Failed, coverage.Unsupported, coverage.Fallback, coverage.Overflow} {
		add(&coverage.Denominator, total)
	}
	if coverage.Exact && coverage.Denominator != 0 {
		high, low := bits.Mul64(coverage.Successful, 10_000)
		quotient, _ := bits.Div64(high, low, coverage.Denominator)
		coverage.BasisPoints = uint32(quotient)
	}
	return coverage, nil
}
