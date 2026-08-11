//go:build windows

package fragment

// Windows has no portable directory fsync. Fragment and ledger replacement
// already use write-through platform primitives; ordered marker-last removal
// still preserves the authority boundary for every observable cleanup failure.
func syncCheckpointDirectory(string) error { return nil }
