package channels

import (
	"unsafe"

	"github.com/pianoyeg94/golang-runtime/channels/atomic"
)

func dumpgstatus(gp *g) {
	thisg := getg()
	print("runtime:   gp: gp=", gp, ", goid=", gp.goid, ", gp->atomicstatus=", readgstatus(gp), "\n")
	print("runtime: getg:  g=", thisg, ", goid=", thisg.goid, ", gp->atomicstatus=", readgstatus(thisg), "\n")
}

// Mark gp ready to run.
func ready(gp *g, traceskip int, next bool) {
	// TODO!!!!! if trace.enabled {
	// 	traceGoUnpark(gp, traceskip)
	// }

	status := readgstatus(gp) // atomic load of G's status from its `.atomicstatus` attribute

	// Mark runnable.

	// bumps up the count of acquired or pending
	// to be acquired futex-based mutexes by this M.
	// Returns the current M.
	// TODO: 'keeps the garbage collector from being invoked'?
	// TODO: 'keeps the G from moving to another M?'
	mp := acquirem() // TODO!!!!! disable preemption because it can be holding p in a local var

	if status&^_Gscan != _Gwaiting { // if passed in goroutine not in Gwaiting or Gscanwaiting
		dumpgstatus(gp)
		throw("bad g->statius in ready")
	}

	// status is Gwaiting or Gscanwaiting, make Grunnable and put on runq
	//
	// Will loop if the g->atomicstatus is in a Gscan status until the routine that
	// put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
	// yielding to the OS scheduler).
	casgstatus(gp, _Gwaiting, _Grunnable)
	// runqput tries to put g on the local runnable queue.
	// If next is false, runqput adds g to the tail of the runnable queue.
	// If next is true, runqput puts g in the pp.runnext slot.
	// If the run queue is full, runnext puts g on the global queue.
	// Executed only by the owner P.
	runqput(mp.p.ptr(), gp, next)
	// TODO!!!!! wakep()

	// decrements the count of acquired or pending
	// to be acquired futex-based mutexes by this M.
	// If their are no more acquired or pending
	// to be acquired futex-based mutexes and there
	// was an asynchronous preemtion request issued
	// since the call to acquirem(), restores the preemption
	// request in case we've cleared it in newstack by
	// setting G's stackguard0 to stackPreemt.
	releasem(mp)
}

// Puts the current goroutine into a waiting state and calls unlockf on the
// system stack.
//
// If unlockf returns false, the goroutine is resumed.
//
// unlockf must not access this G's stack, as it may be moved between
// the call to gopark and the call to unlockf.
//
// Note that because unlockf is called after putting the G into a waiting
// state, the G may have already been readied by the time unlockf is called
// unless there is external synchronization preventing the G from being
// readied. If unlockf returns false, it must guarantee that the G cannot be
// externally readied.
// Reason explains why the goroutine has been parked. It is displayed in stack
// traces and heap dumps. Reasons should be unique and descriptive. Do not
// re-use reasons, add new ones.
func gopark(unlockf func(*g, unsafe.Pointer) bool, lock unsafe.Pointer, reason waitReason, traceEv byte, traceskip int) {
	if reason != waitReasonSleep {
		checkTimeouts() // no-op on every system except JS
	}

	// bumps up the count of acquired or pending
	// to be acquired futex-based mutexes by this M.
	// Returns the current M.
	// TODO: 'keeps the garbage collector from being invoked'?
	// TODO: 'keeps the G from moving to another M?'
	mp := acquirem()
	gp := mp.curg // current running goroutine that called gopark
	status := readgstatus(gp)
	// if the current goroutine that is about to park itself isn't running user code
	// or has just received a GC signal to scan its own stack, then it's a runtime error
	if status != _Grunning && status != _Gscanrunning {
		throw("gopark: bad g status")
	}

	mp.waitlock = lock // in case of a channel gopark, it's a pointer to hchan's lock
	// TODO:
	mp.waitunlockf = unlockf     // in case of a channel gopark, it's a callback that does TODO!!!! and unlocks hchan's lock
	gp.waitreason = reason       // in case of a channel gopark. it's waitReasonChanReceive
	mp.waittraceev = traceEv     // TODO:
	mp.waittraceskip = traceskip // TODO:
	// decreases the count of acquired or pending
	// to be acquired futex-based mutexes by this M.
	// TODO: and if there're no acquired or pending mutexes
	// restores the stack preemption request if one was pending
	// on the current goroutine.
	releasem(mp)
	// can't do anything that might move the G between Ms here.
	mcall(park_m) // never returns, saves this G's exeuction context and switches to g0 stack to call the passed in function

}

func goready(gp *g, traceskip int) {
	// systemstack runs fn on a system stack.
	// Switches to the per-OS-thread stack, calls fn,
	// and switches back.
	systemstack(func() {
		// 1. Does a CAS of gp's status from Gwaiting or Gscanwaiting to Grunnable,
		//    may loop if the g->atomicstatus is in a Gscan status until the routine that
		//    put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
		//    yielding to the OS scheduler).
		//
		// 2. Tries to put g on the local runnable queue into the pp.runnext slot.
		//    If there was a goroutine already in this slot tries to kick it out
		//    into the tail of the local runnable queue, if it's full, runnext puts
		//    the kicked out goroutine onto the global queue.
		//
		// 3. // TODO!!!!! wakep()
		ready(gp, traceskip, true)
	})
}

//go:nosplit
func acquireSudog() *sudog {
	// Delicate dance: the semaphore implementation calls
	// acquireSudog, acquireSudog calls new(sudog),
	// new calls malloc, malloc can call the garbage collector,
	// and the garbage collector calls the semaphore implementation
	// in stopTheWorld.
	// Break the cycle by doing acquirem/releasem around new(sudog).
	// The acquirem/releasem increments m.locks during new(sudog),
	// which keeps the garbage collector from being invoked.

	// bumps up the count of acquired or pending M.
	// Returns the current M.
	// to be acquired futex-based mutexes by this
	// TODO!!!!!: 'keeps the garbage collector from being invoked'?
	mp := acquirem()
	pp := mp.p.ptr()
	if len(pp.sudogcache) == 0 { // if per-P sudogcache is empty, fill it up from the global cache
		// First, try to grab a batch from central cache.
		// Ideally we should get a half-full per-P cache as a result.
		lock(&sched.sudoglock)
		for len(pp.sudogcache) < cap(pp.sudogcache)/2 && sched.sudogcache != nil {
			s := sched.sudogcache
			sched.sudogcache = s.next
			s.next = nil
			pp.sudogcache = append(pp.sudogcache, s)
		}
		unlock(&sched.sudoglock)

		// If the central cache is empty, and thus we counldn't fill our local cache,
		// allocate a new sudog and add it to our per-P cache.
		if len(pp.sudogcache) == 0 {
			pp.sudogcache = append(pp.sudogcache, new(sudog))
		}
	}

	// pop sudog from tail,
	// decreasing length,
	// but capacity remains the same
	n := len(pp.sudogcache)
	s := pp.sudogcache[n-1]
	pp.sudogcache[n-1] = nil
	pp.sudogcache = pp.sudogcache[:n-1]
	if s.elem != nil {
		throw("acquireSudog: found s.elem != nil in cache")
	}

	// decreases the count of acquired or pending
	// to be acquired futex-based mutexes by this M.

	// TODO: and if there're no acquired or pending mutexes
	// restores the stack preemption request if one was pending
	// on the current goroutine.
	releasem(mp)

	return s
}

// All reads and writes of g's status go through readgstatus, casgstatus
// castogscanstatus, casfrom_Gscanstatus.
//
//go:nosplit
func readgstatus(gp *g) uint32 {
	return gp.atomicstatus.Load()
}

// If asked to move to or from a Gscanstatus this will throw. Use the castogscanstatus
// and casfrom_Gscanstatus instead.
// casgstatus will loop if the g->atomicstatus is in a Gscan status until the routine that
// put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
// yielding to the OS scheduler)
//
//go:nosplit
func casgstatus(gp *g, oldval, newval uint32) {
	if (oldval&_Gscan != 0) || (newval&_Gscan != 0) || oldval == newval {
		// check out runtime·systemstack symbol in asm_amd64.s
		systemstack(func() {
			print("runtime: casgstatus: oldval=", hex(oldval), " newval=", hex(newval), "\n")
			throw("casgstatus: bad incoming values")
		})
	}

	acquireLockRank(lockRankGscan) // no-op by default
	releaseLockRank(lockRankGscan) // no-op by default

	// See https://golang.org/cl/21503 for justification of the yield delay:
	//    Active spinning is never a good idea in user-space. Once we wait several
	//    times more than the expected wait time (2.5 to 5 microseconds),
	//    something unexpected is happenning (e.g. the thread we are waiting for
	//    is descheduled or handling a page fault) and we need to yield to OS scheduler.
	//    Moreover, the expected wait time is very high, can be hundreds of microseconds.
	//    It does not make sense to spin even for that time.
	const yieldDelay = 5 * 1000 // 5 microseconds
	var nextYield int64

	// loop if gp->atomicstatus is in a scan state giving
	// GC time to finish and change the state to oldval.
	// This means that GC could have started in between when the
	// old value was read by the caller of this function and
	// this CAS.
	for i := 0; !gp.atomicstatus.CompareAndSwap(oldval, newval); i++ {
		if oldval == _Gwaiting && gp.atomicstatus.Load() == _Grunnable {
			throw("casgstatus: waiting for Gwaiting but is Grunnable")
		}

		if i == 0 {
			nextYield = nanotime() + yieldDelay // on first try spin for 5 microseconds
		}

		if nanotime() < nextYield { // spin for 5 microseconds on first try and then for 2.5 microseconds between yielding to the OS scheduler
			for x := 0; x < 10 && gp.atomicstatus.Load() != oldval; x++ {
				//	The processor uses this hint to avoid the memory order violation in most situations,
				//	which greatly improves processor performance.
				//	For this reason, it is recommended that a PAUSE instruction be placed
				//	in all spin-wait loops. An additional function of the PAUSE instruction
				//	is to reduce the power consumed by Intel processors.
				procyield(1) // calls the assembly PAUSE instruction once
			}
		} else {
			// Causes the calling thread to relinquish the CPU.
			// The thread is moved to the end of the queue
			// for its static priority and a new thread gets to run.
			osyield()
			nextYield = nanotime() + yieldDelay/2 // spin for another 2.5 microseconds
		}
	}

	// TODO!!!!!
	// if oldval == _Grunning {
	// // Track every gTrackingPeriod time a goroutine transitions out of running.
	// if casgstatusAlwaysTrack || gp.trackingSeq%gTrackingPeriod == 0 {
	// 	gp.tracking = true
	// }
	// gp.trackingSeq++
	// }
	if !gp.tracking {
		return
	}

	// TODO!!!!!
	// // Handle various kinds of tracking.
	// //
	// // Currently:
	// // - Time spent in runnable.
	// // - Time spent blocked on a sync.Mutex or sync.RWMutex.
	// switch oldval {
	// case _Grunnable:
	// 	// We transitioned out of runnable, so measure how much
	// 	// time we spent in this state and add it to
	// 	// runnableTime.
	// 	now := nanotime()
	// 	gp.runnableTime += now - gp.trackingStamp
	// 	gp.trackingStamp = 0
	// case _Gwaiting:
	// 	if !gp.waitreason.isMutexWait() {
	// 		// Not blocking on a lock.
	// 		break
	// 	}
	// 	// Blocking on a lock, measure it. Note that because we're
	// 	// sampling, we have to multiply by our sampling period to get
	// 	// a more representative estimate of the absolute value.
	// 	// gTrackingPeriod also represents an accurate sampling period
	// 	// because we can only enter this state from _Grunning.
	// 	now := nanotime()
	// 	sched.totalMutexWaitTime.Add((now - gp.trackingStamp) * gTrackingPeriod)
	// 	gp.trackingStamp = 0
	// }
	// switch newval {
	// case _Gwaiting:
	// 	if !gp.waitreason.isMutexWait() {
	// 		// Not blocking on a lock.
	// 		break
	// 	}
	// 	// Blocking on a lock. Write down the timestamp.
	// 	now := nanotime()
	// 	gp.trackingStamp = now
	// case _Grunnable:
	// 	// We just transitioned into runnable, so record what
	// 	// time that happened.
	// 	now := nanotime()
	// 	gp.trackingStamp = now
	// case _Grunning:
	// 	// We're transitioning into running, so turn off
	// 	// tracking and record how much time we spent in
	// 	// runnable.
	// 	gp.tracking = false
	// 	sched.timeToRun.record(gp.runnableTime)
	// 	gp.runnableTime = 0
	// }
}

// Schedules gp to run on the current M.
// If inheritTime is true, gp inherits the remaining time in the
// current time slice. Otherwise, it starts a new time slice.
// Never returns.
//
// Write barriers are allowed because this is called immediately after
// acquiring a P in several places.
//
//go:yeswritebarrierrec
func execute(gp *g, inheritTime bool) {
	mp := getg().m // gets g0 from the R14 register and after that the M currently executing it

	// TODO!!!!! if goroutineProfile.active {}

	// Assign gp.m before entering _Grunning so running Gs have an M.
	mp.curg = gp
	gp.m = mp
	casgstatus(gp, _Grunnable, _Grunning)
	gp.waitsince = 0                           // approx time when the g became blocked TODO!!!!!
	gp.preempt = false                         // in case the goroutine had an asynchronous preemtion request pending previously TODO!!!!!
	gp.stackguard0 = gp.stack.lo + _StackGuard // restore in this goroutine received a preemtion signal previously
	if !inheritTime {                          // TODO!!!!! cases when time is inherited?
		mp.p.ptr().schedtick++
	}

	// Check whether the profiler needs to be turned on or off.
	// TODO!!!!! hz := sched.profilehz
	// TODO!!!!! if mp.profilehz != hz {}

	// TODO!!!!! if trace.enabled {}

	// Never returns.
	//
	// Restores the pointer to the goroutine which
	// owns this `gobuf` in the TLS area and in the r14 register.
	//
	// Restores the goroutine's stack pointer and it's frame
	// pointer in rbp.
	//
	// Stores top-level function's return address executed by this goroutine in rax,
	// stores the goroutine's context in rdx.
	//
	// Clears sp, ret, ctxt and bp in the passed in `gobuf` to help the garbage collerctor.
	//
	// Restores goroutines program counter and does a long jump to that next instruction to execute.
	gogo(&gp.sched)
}

// TODO!!!!!
func schedule() {

}

// dropg removes the association between m and the current goroutine m->curg (gp for short).
// Typically a caller sets gp's status away from Grunning and then
// immediately calls dropg to finish the job. The caller is also responsible
// for arranging that gp will be restarted using ready at an
// appropriate time. After calling dropg and arranging for gp to be
// readied later, the caller can do other work but eventually should
// call schedule to restart the scheduling of goroutines on this m.
func dropg() {
	gp := getg() // returns pointer to g0

	setMNoWB(&gp.m.curg.m, nil) // set without write barrier current g's M to nil (called from g0, so first we get the M associated with g0)
	setGNoWB(&gp.m.curg, nil)   // set without write barrier M's current G to nil (called from g0, so first we get the M associated with g0)
}

// park g's continuation on g0.
// called on g0's system stack.
// by this time g's rbp, rsp and rip (PC)
// are saved in g's .sched<gobuf>.
func park_m(gp *g) {
	mp := getg().m

	// TODO!!!!! if trace.enabled {}

	// N.B. Not using casGToWaiting here because the waitreason is
	// set by park_m's caller - `gopark()`.
	// Will loop if the g->atomicstatus is in a Gscan status until the routine that
	// put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
	// yielding to the OS scheduler).
	casgstatus(gp, _Grunning, _Gwaiting)
	dropg() // sets `gp`s M association to nil + sets M's `.curg` association with `gp` to nil

	// `mp.waitunlockf` function passed into `gopark()` and saved in M, this function determines if
	// the goroutine should still be parked (if does not return false), does some additional
	// bookkeeping depending on the concrete function and unlocks the waitlock, passed in
	// to `gopark()` and saved in `mp.waitlock`.
	//
	// Channel recieve example, function `chanparkcommit`:
	// 		chanparkcommit is called as part of the `gopark` routine
	//      after the goroutine's status transfers to `_Gwaiting` and
	//      the goroutine is dissasociated from the M, which was executing
	//      it, BUT just before another goroutine gets scheduled by g0.
	//
	//      The main responsibility of chanparkcommit is to set goroutine's
	//      `activeStackChans` attribute to true, which tells the other parts
	//      of the runtime that there are unlocked sudogs that point into gp's stack (sudog.elem).
	//      So stack copying must lock the channels of those sudogs.
	//      In the end chanparkcommit unlocks channel's lock and returns true,
	//      which tells `gopark` to go on an schedule another goroutine.
	if fn := mp.waitunlockf; fn != nil {
		ok := fn(gp, mp.waitlock)
		mp.waitunlockf = nil
		mp.waitlock = nil
		if !ok { // some condition met, so that we don't need to park the goroutine anymore
			// TODO!!!!! if trace.enabled {}
			casgstatus(gp, _Gwaiting, _Grunnable) // transition goroutine back to _Grunnable
			execute(gp, true)                     // Schedule it back, never returns.
		}
	}

	// already on g0, schedule another goroutine
	schedule()
}

// Put a batch of runnable goroutines on the global runnable queue.
// This clears *batch (generally the batch is residing on callers stack).
// sched.lock must be held.
// May run during STW, so write barriers are not allowed.
//
//go:nowritebarrierrec
func globrunqputbatch(batch *gQueue, n int32) {
	assertLockHeld(&sched.lock) // no-op when static lock ranking is off, which is the default

	sched.runq.pushBackAll(*batch)
	sched.runqsize += n
	*batch = gQueue{} // only for correctnes if gQueue is accessible globally (guintptr memebers of gQueue are not counted by GC as live references)
}

// To shake out latent assumptions about scheduling order,
// we introduce some randomness into scheduling decisions
// when running with the race detector.
// The need for this was made obvious by changing the
// (deterministic) scheduling order in Go 1.5 and breaking
// many poorly-written tests.
// With the randomness here, as long as the tests pass
// consistently with -race, they shouldn't have latent scheduling
// assumptions.
const randomizeScheduler = false // TODO!!!!! raceenabled

// runqput tries to put g on the local runnable queue.
// If next is false, runqput adds g to the tail of the runnable queue.
// If next is true, runqput puts g in the pp.runnext slot.
// If the run queue is full, runnext puts g on the global queue.
// Executed only by the owner P.
func runqput(pp *p, gp *g, next bool) {
	// TODO!!!!! if randomizeScheduler && next && fastrandn(2) == 0 {
	//		next = false
	//	}

	if next {
	retryNext:
		oldnext := pp.runnext
		// only ever written to by CAS, which will observe the latest update
		// even if the the above relaxed read didn't
		if !pp.runnext.cas(oldnext, guintptr(unsafe.Pointer(gp))) {
			// can only fail if runnext was non-0 and it was stolen by another P,
			// because only the current P can set it to non-0.
			goto retryNext
		}
		if oldnext == 0 {
			// was zero from the beginning or was stolen by another P right under our nose
			return
		}
		// Kick the old runnext out to the regular run queue.
		gp = oldnext.ptr()
	}

retry:
	// load-acquire, synchronize with consumers
	// (other Ps that can steal from our local
	// runnable queue).
	// Has a synchronizes-with relation with
	// atomic cas-releases in the queue's
	// consume-related methods, which guarantees
	// that we will observe the latest commited
	// consume aka forward move of runqhead.
	//
	// On x86 LoadAcq is just a regular load.
	h := atomic.LoadAcq(&pp.runqhead)
	// load of runqtail can be relaxed, because is only
	// ever written by the current P, which even though
	// could run on another thread/core after the upcoming
	// store-release of runqtail (during next call to a
	// producer-related method), will still see the latest
	// store in the slot associated with runqtail, because
	// before this P tries to add another goroutine to the
	// local runnable queue some time in the future, it first
	// has to synchronize on consuming from this queue or the
	// global runnable queue. Thus happens-before of the upcoming
	// store-release of runqtail and the current relaxed load is
	// achieved through the synchronizes-with relationship of:
	//   - CAS of runnext slot or
	//   - CAS-release of runqhead in consume-related methods
	//     and the preceeding (in program order) LOAD-acquire
	//     of runqhead in this method or
	//   - Mutex acquire-release when consuming from the global runnable queue.
	//
	t := pp.runqtail
	// can use local copies of:
	//   - runqtail because is only ever changed by produce-related methods,
	//     which do not run concurrently
	//   - runqhead because concurrent consumers can only increase the runqhead
	//     pointer, and thus they cannot make the queue become full under our foot.
	//     The only thing that can happen here is that the check can yield false,
	//     telling us that the queue is full, when in fact it isn't, because in
	//     between the previous LOAD-acquire of runqhead and the upcoming check
	//     some spinning goroutine has consumed a bunch of goroutines from our queue.
	//     This is compensated by upcoming additional checks in `runqputslow()`, which
	//     will tell us to retry if something similar occurs.
	if t-h < uint32(len(pp.runq)) {
		// local runnable queue has place for another goroutine

		// length of local runnable queue is always 256, so load of it can be relaxed
		//
		// runqtail and runqhead are never reset manually, it's assumed that they would bever overflow?,
		// so queue slots are determined with the help of modulo division.
		pp.runq[t%uint32(len(pp.runq))].set(gp)
		// store-release, makes the item available for consumption (cannot be reordered before
		// the preceeding store of `gp`, so a consumer will not accidentally assume there's a
		// another goroutine in the runq, when in fact there isn't). On x86 uses the XCHG instruction
		// with pointer to runqtail referencing memory and the new incremented value of runqtail coming
		// from a register. If a memory operand is referenced (this is our case), the processor’s locking
		// protocol is automatically implemented for the duration of the exchange operation, regardless of
		// the presence or absence of the LOCK prefix or of the value of the IOPL.
		atomic.StoreRel(&pp.runqtail, t+1)
	}
	// Puts gp and a batch of work from local runnable queue on global queue.
	//
	// If in the meantime passed in runqhead became stale, because some other
	// spinning P consumed from P's local runnable queue since the passed in
	// runqhead was computed, the function execution stops and false is returned
	// to the caller, to inform it, that there's place for the new goroutine on
	// the local runnable queue and it should retry putting it onto that queue.
	//
	// Otherwise puts a half plus one (129) goroutines from the local runnable queue
	// onto the global runnable queue and returns true.
	if runqputslow(pp, gp, h, t) {
		return
	}
	// the queue is not full, now the put above must succeed
	goto retry
}

// Put g and a batch of work from local runnable queue on global queue.
// Executed only by the owner P.
//
// If in the meantime passed in runqhead became stale, because some other
// spinning P consumed from P's local runnable queue since the passed in
// runqhead was computed, the function execution stops and false is returned
// to the caller, to inform it, that there's place for the new goroutine on
// the local runnable queue and it should retry putting it onto that queue.
//
// Otherwise puts a half plus one (129) goroutines from the local runnable queue
// onto the global runnable queue and returns true.
func runqputslow(pp *p, gp *g, h, t uint32) bool {
	var batch [len(pp.runq)/2 + 1]*g // 129 goroutines (half plus one, the plus one is for the new goroutine, it will also go to the global runnable queue)

	// First, grab a batch from local queue.
	n := t - h // runqhead may be stale by this time if other spinning Ps consume from our local runnable queue, is validated by upcoming CAS-release of runqhead
	n = n / 2
	if n != uint32(len(pp.runq)/2) {
		// overflow???
		throw("runqputslow: queue is not full")
	}
	// consume half of the goroutines into batch (but do not yet commit the consume)
	for i := uint32(0); i < n; i++ {
		batch[i] = pp.runq[(h+i)%uint32(len(pp.runq))].ptr()
	}
	if !atomic.CasRel(&pp.runqhead, h, h+n) { // cas-release, commits consume (x86 uses the LOCK instruction)
		// passed in runqhead became stale, some other spinning P consumed
		// from our local runnable queue since the passed in runqhead was computed,
		// there's place for the new goroutine in the local runnable queue, return,
		// so the caller can retry enqueing it (will succeed this time)
		return false
	}
	batch[n] = gp // insert new goroutine into the last slot of the batch

	// TODO!!!!! if randomizeScheduler {
	// 	for i := uint32(1); i <= n; i++ {
	// 		j := fastrandn(i + 1)
	// 		batch[i], batch[j] = batch[j], batch[i]
	// 	}
	// }

	// Link the goroutines, prepare for gQueue.
	//
	// A gQueue is a dequeue of Gs linked through g.schedlink. A G can only
	// be on one gQueue or gList at a time.
	//
	// Used by the runtime instead of a goroutine slice, because a slice
	// would require a backing array allocation, while a dequeue can reside
	// on the stack and copied around, because it is only 2 machine words wide,
	// even if it holds quite a lot of goroutines, which are already allocated anyway.
	for i := uint32(0); i < n; i++ {
		batch[i].schedlink.set(batch[i+1])
	}
	var q gQueue
	q.head.set(batch[0])
	q.tail.set(batch[n]) // new goroutine is at slot n

	// Now put the batch on global queue,
	// which is also a gQueue
	lock(&sched.lock)
	globrunqputbatch(&q, int32(n+1))
	unlock(&sched.lock)
	return true
}

// A gQueue is a dequeue of Gs linked through g.schedlink. A G can only
// be on one gQueue or gList at a time.
//
// Used by the runtime instead of a goroutine slice, because a slice
// would require a backing array allocation, while a dequeue can reside
// on the stack and copied around, because it is only 2 machine words wide,
// even if it holds quite a lot of goroutines, which are already allocated anyway.
type gQueue struct {
	head guintptr
	tail guintptr
}

// empty reports whether q is empty.
func (q *gQueue) empty() bool {
	return q.head == 0
}

// push adds gp to the head of q.
func (q *gQueue) push(gp *g) {
	gp.schedlink = q.head
	q.head.set(gp)
	if q.tail == 0 {
		// queue was empty
		q.tail.set(gp)
	}
}

// pushBack adds gp to the tail of q.
func (q *gQueue) pushBack(gp *g) {
	gp.schedlink = 0
	if q.tail != 0 {
		// queue NOT empty
		q.tail.ptr().schedlink.set(gp)
	} else {
		// queue empty, so set head as well
		q.head.set(gp)
	}
	q.tail.set(gp)
}

// pushBackAll adds all Gs in q2 to the tail of q. After this q2 must
// not be used.
func (q *gQueue) pushBackAll(q2 gQueue) {
	if q2.tail == 0 {
		// q2 is empty
		return
	}
	q2.tail.ptr().schedlink = 0
	if q.tail != 0 {
		// original queue NOT empty
		q.tail.ptr().schedlink = q2.head
	} else {
		// original queue is empty, so set its head as well
		q.head = q2.head
	}
	q.tail = q2.tail
}

// pop removes and returns the head of queue q. It returns nil if
// q is empty.
func (q *gQueue) pop() *g {
	gp := q.head.ptr()
	if gp != nil {
		// queue NOT empty
		q.head = gp.schedlink
		if q.head == 0 {
			// gp is the last item the queue
			q.tail = 0
		}
	}
	return gp
}

// A gList is a list of Gs linked through g.schedlink. A G can only be
// on one gQueue or gList at a time.
//
// Pushing and poping happens only at the head of the singly-linked list.
type gList struct {
	head guintptr
}

// empty reports whether l is empty.
func (l *gList) empty() bool {
	return l.head == 0
}

// push adds gp to the head of l.
func (l *gList) push(gp *g) {
	gp.schedlink = l.head
	l.head.set(gp)
}

// pushAll prepends all Gs in q to l.
func (l *gList) pushAll(q gQueue) {
	if !q.empty() {
		q.tail.ptr().schedlink = l.head
		l.head = q.head
	}
}

func (l *gList) pop() *g {
	gp := l.head.ptr()
	if gp != nil {
		l.head = gp.schedlink
	}
	return gp
}
