// go/src/runtime/lockrank.go
package channels

type lockRank int

// Constants representing the ranks of all non-leaf runtime locks, in rank order.
// Locks with lower rank must be taken before locks with higher rank,
// in addition to satisfying the partial order in lockPartialOrder.
// A few ranks allow self-cycles, which are specified in lockPartialOrder.
const (
	lockRankUnknown lockRank = iota

	lockRankSysmon
	lockRankScavenge
	lockRankForcegc
	lockRankDefer
	lockRankSweepWaiters
	lockRankAssistQueue
	lockRankSweep
	lockRankPollDesc
	lockRankCpuprof
	lockRankSched
	lockRankAllg
	lockRankAllp
	lockRankTimers
	lockRankNetpollInit
	lockRankHchan
	lockRankNotifyList
	lockRankSudog
	lockRankRwmutexW
	lockRankRwmutexR
	lockRankRoot
	lockRankItab
	lockRankReflectOffs
	lockRankUserArenaState
	// TRACEGLOBAL
	lockRankTraceBuf
	lockRankTraceStrings
	// MALLOC
	lockRankFin
	lockRankGcBitsArenas
	lockRankMheapSpecial
	lockRankMspanSpecial
	lockRankSpanSetSpine
	// MPROF
	lockRankProfInsert
	lockRankProfBlock
	lockRankProfMemActive
	lockRankProfMemFuture
	// STACKGROW
	lockRankGscan
	lockRankStackpool
	lockRankStackLarge
	lockRankHchanLeaf
	// WB
	lockRankWbufSpans
	lockRankMheap
	lockRankGlobalAlloc
	// TRACE
	lockRankTrace
	lockRankTraceStackTab
	lockRankPanic
	lockRankDeadlock
)
