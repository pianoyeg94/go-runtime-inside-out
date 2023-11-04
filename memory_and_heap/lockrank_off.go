package heap

// lockRankStruct is embedded in mutex, but is empty when staticklockranking is
// disabled (the default)
type lockRankStruct struct{}

func lockInit(l *mutex, rank lockRank) {}

func getLockRank(l *mutex) lockRank { return 0 }

func lockWithRank(l *mutex, rank lockRank) {
	lock2(l)
}

// This function may be called in nosplit context and thus must be nosplit.
//
//go:nosplit
func acquireLockRank(rank lockRank) {
}

func unlockWithRank(l *mutex) {
	unlock2(l)
}

// This function may be called in nosplit context and thus must be nosplit.
//
//go:nosplit
func releaseLockRank(rank lockRank) {
}
