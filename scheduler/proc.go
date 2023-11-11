package scheduler

// pidleput puts p on the _Pidle list. now must be a relatively recent call
// to nanotime or zero. Returns now or the current time if now was zero.
//
// This releases ownership of p. Once sched.lock is released it is no longer
// safe to use p.
//
// sched.lock must be held.
//
// May run during STW, so write barriers are not allowed.
//
//go:nowritebarrierrec
// func pidleput(_p_ *p, now int64) int64 {
// 	assertLockHeld(&sched.lock)

// 	if !runqempty(_p_) {
// 		panic("pidleput: P has non-empty run queue")
// 	}

// 	if now == 0 {
// 		now = nanotime()
// 	}

// 	return now
// }

// runqempty reports whether _p_ has no Gs on its local run queue.
// It never returns true spuriously.
// func runqempty(_p_ *p) bool {
// 	// Defend against a race where 1) _p_ has G1 in runqnext but runqhead == runqtail,
// 	// 2) runqput on _p_ kicks G1 to the runq, 3) runqget on _p_ empties runqnext.
// 	// Simply observing that runqhead == runqtail and then observing that runqnext == nil
// 	// does not mean the queue is empty.
// 	for {
// 		head := atomic.LoadUint32(&_p_.runqhead)
// 		tail := atomic.LoadUint32(&_p_.runqtail)
// 		runnext := atomic.LoadUintptr((*uintptr)(unsafe.Pointer(&_p_.runnext)))
// 		if tail == atomic.LoadUint32(&_p_.runqtail) {
// 			return head == tail && runnext == 0
// 		}
// 	}
// }

// Always runs without a P, so write barriers are not allowed.
//
//go:nowritebarrierrec
// func sysmon() {
// 	lock(&sched.lock)
// 	sched.nmsys++
// 	checkdead()
// 	unlock(&sched.lock)

// 	lasttrace := int64(0)
// 	idle := 0 // how many cycles in succession we had not wokeup somebody
// 	delay := uint32(0)

// 	for {
// 		if idle == 0 { // start with 20us sleep...
// 			delay = 20
// 		} else if idle > 50 { // start doubling the sleep after 1ms...
// 			delay *= 2
// 		}
// 		if delay > 10*1000 { // up to 10ms
// 			delay = 10 * 1000
// 		}
// 		usleep(delay)
// 	}
// }

// forcePreemptNS is the time slice given to a G before it is
// preempted.
const forcePreemptNS = 10 * 1000 * 1000 // 10ms (10 nanoseconds * 1000 * 1000, nanoseconds from CLOCK_MONOTONIC)

type sysmontick struct {
	schedtick   uint32
	schedwen    int64
	syscalltick uint32
	syscallwhen int64
}

func retake(now int64) uint32 {
	n := 0

	// Prevent allp slice changes. This lock will be completely
	// uncontended unless we're already stopping the world.
	lock(&allpLock)

	return uint32(n)
}
