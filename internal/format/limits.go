package format

// MaxMergeTracks is the maximum number of format tracks that may be merged
// into one output product. It matches the selector parser and evaluator bound.
const MaxMergeTracks = 16

// MaxNormalizedFormats is the maximum number of canonical formats accepted
// for one media entry. Product boundaries which inspect every candidate must
// use this value rather than inventing a lower, incompatible ceiling.
const MaxNormalizedFormats = 4096
