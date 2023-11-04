package channels

// Helpers for Go. Must be NOSPLIT, must only call NOSPLIT functions, and must not block.

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
