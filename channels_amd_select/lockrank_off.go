//go:build !goexperiment.staticlockranking

// go/src/runtime/lockrank_off.go
package channels

// lockRankStruct is embedded in mutex, but is empty when staticklockranking is
// disabled (the default)
type lockRankStruct struct{}

func lockInit(l *mutex, rank lockRank) {}

func getLockRank(l *mutex) lockRank { return 0 }

// This function may be called in nosplit context and thus must be nosplit.
//
//go:nosplit
func acquireLockRank(rank lockRank) {
}

// This function may be called in nosplit context and thus must be nosplit.
//
//go:nosplit
func releaseLockRank(rank lockRank) {
}

//go:nosplit
func assertLockHeld(l *mutex) {
}
