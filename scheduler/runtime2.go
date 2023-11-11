package scheduler

import "unsafe"

type guintptr uintptr

type gobuf struct {
	// The offsets of sp, pc, and g are known to (hard-coded in) libmach.
	//
	// ctxt is unusual with respect to GC: it may be a
	// heap-allocated funcval, so GC needs to track it, but it
	// needs to be set and cleared from assembly, where it's
	// difficult to have write barriers. However, ctxt is really a
	// saved, live register, and we only ever exchange it between
	// the real register and the gobuf. Hence, we treat it as a
	// root during stack scanning, which means assembly that saves
	// and restores it doesn't need write barriers. It's still
	// typed as a pointer so that any other writes from Go get
	// write barriers.
	sp   uintptr        // stack pointer
	pc   uintptr        // program counter
	g    guintptr       // pointer to the goroutine owning the gobuf
	ctxt unsafe.Pointer // TODO
	ret  uintptr        // return address of the top-level function executed by the goroutine
	lr   uintptr        // for arm only, maps to the r14 register, used to hold the return address for a function call
	bp   uintptr        // base pointer: x86-64 is a framepointer-enabled architecture
}

type p struct {
	sysmontick sysmontick // last tick observed by sysmon

	// Queue of runnable goroutines. Accessed without lock.
	runqhead uint32
	runqtail uint32
	runq     [256]guintptr
	// runnext, if non-nil, is a runnable G that was ready'd by
	// the current G and should be run next instead of what's in
	// runq if there's time remaining in the running G's time
	// slice. It will inherit the time left in the current time
	// slice. If a set of goroutines is locked in a
	// communicate-and-wait pattern, this schedules that set as a
	// unit and eliminates the (potentially large) scheduling
	// latency that otherwise arises from adding the ready'd
	// goroutines to the end of the run queue.
	//
	// Note that while other P's may atomically CAS this to zero,
	// only the owner P can CAS it to a valid G.
	runnext guintptr
}

var (
	sched schedt

	// allpLock protects P-less reads and size changes of allp, idlepMask,
	// and timerpMask, and all writes to allp.
	allpLock mutex

	// len(allp) == gomaxprocs; may change at safe points, otherwise
	// immutable.
	allp []*p
)
