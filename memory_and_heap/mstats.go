package heap

import "sync/atomic"

// TODO!!!!!
type mstats struct {
	// Statistics about allocation of low-level fixed-size structures.
	mspan_sys sysMemStat // TODO!!!!!

	// TODO!!!!! Miscellaneous statistics.
	other_sys sysMemStat // updated atomically or during STW
}

var memstats mstats

// sysMemStat represents a global system statistic that is managed atomically.
//
// This type must structurally be a uint64 so that mstats aligns with MemStats.
type sysMemStat uint64

// load atomically reads the value of the stat.
//
// Must be nosplit as it is called in runtime initialization, e.g. newosproc0.
//
//go:nosplit
func (s *sysMemStat) load() uint64 {
	return atomic.LoadUint64((*uint64)(s))
}

func (s *sysMemStat) add(n int64) {
	val := Xadd64((*uint64)(s), n)
	if (n > 0 && int64(val) < n) || (n < 0 && int64(val)+n < n) {
		print("runtime val=", val, "n=", n, "\n")
		throw("sysMemStat overflow")
	}
}

// consistentHeapStats represents a set of various memory statistics
// whose updates must be viewed completely to get a consistent
// state of the world.
//
// To write updates to memory stats use the acquire and release
// methods. To obtain a consistent global snapshot of these statistics,
// use read.
type consistentHeapStats struct{}
