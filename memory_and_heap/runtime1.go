package heap

// Helpers for Go. Must be NOSPLIT, must only call NOSPLIT functions, and must not block.

// Holds variables parsed from GODEBUG env var,
// except for "memprofilerate" since there is an
// existing int var for that value, which may
// already have an initial value.
var debug struct {
	// Setting madvdontneed=0 will use MADV_FREE
	// instead of MADV_DONTNEED on Linux when returning memory to the
	// kernel. This is more efficient, but means RSS numbers will
	// drop only when the OS is under memory pressure. On the BSDs and
	// Illumos/Solaris, setting madvdontneed=1 will use MADV_DONTNEED instead
	// of MADV_FREE. This is less efficient, but causes RSS numbers to drop
	// more quickly.
	madvdontneed int32 // for Linux; issue 28466

	// Setting harddecommit=1 causes memory that is returned to the OS to also have protections removed
	// on it. On Linux this means that sysUnused remaps "released" address ranges as PROT_NONE releasing
	// physical memory backing the range to the OS.
	//
	// Makes the runtime map pages PROT_NONE in sysUnused on Linux, in addition to the usual madvise
	// calls. This behavior mimics the behavior of decommit on Windows, and is helpfulin debugging
	// the scavenger. sysUsed is also updated to re-map the pages as PROT_READ|PROT_WRITE, mimicing
	// Windows' explicit commit behavior.
	harddecommit int32
}

// TODO - use cases and why
//
//go:nosplit
func acquirem() *m {
	gp := getg()
	gp.m.locks++
	return gp.m
}

// TODO - use cases and why
//
//go:nosplit
func releasem(mp *m) {
	gp := getg()
	mp.locks--
	if mp.locks == 0 && gp.preempt {
		// restore the preemption request in case we've cleared it in newstack
		gp.stackguard0 = stackPreemt
	}
}
