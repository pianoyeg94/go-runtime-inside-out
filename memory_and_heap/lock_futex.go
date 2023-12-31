package heap

import (
	"unsafe"

	"github.com/pianoyeg94/go-runtime-inside-out/memory_and_heap/atomic"
)

const (
	mutex_unlocked = 0
	mutex_locked   = 1
)

// We use the uintptr mutex.key and note.key as a uint32.
//
//go:nosplit
func key32(p *uintptr) *uint32 {
	return (*uint32)(unsafe.Pointer(p))
}

func lock(l *mutex) { // TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	lockWithRank(l, getLockRank(l))
}

func lock2(l *mutex) {
	gp := getg()

	if gp.m.locks < 0 {
		throw("runtime·lock: lock count")
	}
	gp.m.locks++

	// Speculative grab for lock (mutex was unlocked).
	v := atomic.Xchg(key32(&l.key), mutex_locked)
	if v == mutex_unlocked {
		return
	}

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
}

// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
func unlock(l *mutex) {
	unlockWithRank(l)
}

func unlock2(l *mutex) {
	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
}
