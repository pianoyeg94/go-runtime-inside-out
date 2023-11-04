package heap

import "unsafe"

type mutex struct {
	// Futex-based impl treats it as uint32 key.
	key uintptr
}

// TODO!!!!!
// The guintptr, muintptr, and puintptr are all used to bypass write barriers.
// It is particularly important to avoid write barriers when the current P has
// been released, because the GC thinks the world is stopped, and an
// unexpected write barrier would not be synchronized with the GC,
// which can lead to a half-executed write barrier that has marked the object
// but not queued it. If the GC skips the object and completes before the
// queuing can occur, it will incorrectly free the object.
//
// We tried using special assignment functions invoked only when not
// holding a running P, but then some updates to a particular memory
// word went through write barriers and some did not. This breaks the
// write barrier shadow checking mode, and it is also scary: better to have
// a word that is completely ignored by the GC than to have one for which
// only a few updates are ignored.
//
// Gs and Ps are always reachable via true pointers in the
// allgs and allp lists or (during allocation before they reach those lists)
// from stack variables.
//
// Ms are always reachable via true pointers either from allm or
// freem. Unlike Gs and Ps we do free Ms, so it's important that
// nothing ever hold an muintptr across a safe point.

// A guintptr holds a goroutine pointer, but typed as a uintptr
// to bypass write barriers. It is used in the Gobuf goroutine state
// and in scheduling lists that are manipulated without a P.
//
// The Gobuf.g goroutine pointer is almost always updated by assembly code.
// In one of the few places it is updated by Go code - func save - it must be
// treated as a uintptr to avoid a write barrier being emitted at a bad time.
// Instead of figuring out how to emit the write barriers missing in the
// assembly manipulation, we change the type of the field to uintptr,
// so that it does not require write barriers at all.
//
// Goroutine structs are published in the allg list and never freed.
// That will keep the goroutine structs from being collected.
// There is never a time that Gobuf.g's contain the only references
// to a goroutine: the publishing of the goroutine in allg comes first.
// Goroutine pointers are also kept in non-GC-visible places like TLS,
// so I can't see them ever moving. If we did want to start moving data
// in the GC, we'd need to allocate the goroutine structs from an
// alternate arena. Using guintptr doesn't make that problem any worse.
// Note that pollDesc.rg, pollDesc.wg also store g in uintptr form,
// so they would need to be updated too if g's start moving.
type guintptr uintptr

//go:nosplit
func (gp guintptr) ptr() *g { return (*g)(unsafe.Pointer(gp)) }

//go:nosplit
func (gp *guintptr) set(g *g) { *gp = guintptr(unsafe.Pointer(g)) }

// TODO!!!!!
// setGNoWB performs *gp = new without a write barrier.
// For times when it's impractical to use a guintptr.
//
//go:nosplit
//go:nowritebarrier
func setGNoWB(gp **g, new *g) {
	(*guintptr)(unsafe.Pointer(gp)).set(new)
}

// TODO!!!!!
type puintptr uintptr

//go:nosplit
func (pp puintptr) ptr() *p { return (*p)(unsafe.Pointer(pp)) }

//go:nosplit
func (pp *puintptr) set(p *p) { *pp = puintptr(unsafe.Pointer(p)) }

// TODO!!!!!
// muintptr is a *m that is not tracked by the garbage collector.
//
// Because we do free Ms, there are some additional constrains on
// muintptrs:
//
//  1. Never hold an muintptr locally across a safe point.
//
//  2. Any muintptr in the heap must be owned by the M itself so it can
//     ensure it is not in use when the last true *m is released.
type muintptr uintptr

//go:nosplit
func (mp muintptr) ptr() *m { return (*m)(unsafe.Pointer(mp)) }

//go:nosplit
func (mp *muintptr) set(m *m) { *mp = muintptr(unsafe.Pointer(m)) }

// TODO!!!!!
// setMNoWB performs *mp = new without a write barrier.
// For times when it's impractical to use an muintptr.
//
//go:nosplit
//go:nowritebarrier
func setMNoWB(mp **m, new *m) {
	(*muintptr)(unsafe.Pointer(mp)).set(new)
}

type g struct {
	// stackguard0 is the stack pointer compared in the Go stack growth prologue. TODO!!!!!
	// It is stack.lo+StackGuard normally, which leaves 928 bytes for a small 128 byte stack frame + 800 bytes,
	// which a chain of NOSPLIT functions can use. But can be StackPreempt to trigger a preemption.
	//
	// 928 bytes lower the top of the stack (lower `lo` address, so basically is higher in memory than `lo`).
	//
	// Those 928 bytes leave room for one _StackSmall (128 bytes) frame plus a _StackLimit chain of NOSPLIT calls
	// plus _StackSystem 0 bytes for the OS.
	//
	// TODO!!!!!:
	// 		_StackSmall
	//          After a stack split check the SP is allowed to be this
	//          many bytes below the stack guard. This saves an instruction
	//          in the checking sequence for tiny frames.
	//
	// TODO!!!!!:
	//      _StackLimit = _StackGuard - _StackSystem - _StackSmall = 800 bytes
	//          The maximum number of bytes that a chain of NOSPLIT functions can use.
	stackguard0 uintptr // offset known golang's linker,

	m *m // current m

	preempt bool // preemption signal, duplicates stackguard0 = stackpreempt
}

type m struct {
	g0 *g // goroutine with scheduling stack

	curg *g // current running goroutine

	p puintptr // attached p for executing go code (nil if not executing go code)

	locks int32 // count of acquired or pending to be aqcuired futex-based mutexes
}

type p struct {
	// pageCache represents a per-p cache of pages the allocator can
	// allocate from without a lock. More specifically, it represents
	// a pageCachePages*pageSize chunk of memory with 0 or more free
	// pages in it (pageCachePages is 64).
	pcache pageCache

	// Cache of mspan objects from the heap.
	// mspan struct pointers allocated off-golang-heap
	// via a fixalloc.
	mspancache struct {
		// TODO!!!!!
		//
		// We need an explicit length here because this field is used
		// in allocation codepaths where write barriers are not allowed,
		// and eliminating the write barrier/keeping it eliminated from
		// slice updates is tricky, moreso than just managing the length
		// ourselves.
		len int
		buf [128]*mspan
	}

	palloc persistentAlloc // per-P to avoid mutex
}
