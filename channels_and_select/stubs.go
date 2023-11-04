package channels

import "unsafe"

const (
	heapAddrBits = 48

	// _64bit = 1 on 64-bit systems, 0 on 32-bit systems
	_64bit = 1 << (^uintptr(0) >> 63) / 2

	// maxAlloc is the maximum size of an allocation. On 64-bit,
	// it's theoretically possible to allocate 1<<heapAddrBits bytes. On
	// 32-bit, however, this is one less than 1<<32 because the
	// number of bytes in the address space doesn't actually fit
	// in a uintptr.
	maxAlloc = (1 << heapAddrBits) - (1-_64bit)*1 // 256 tebibytes
)

// Should be a built-in for unsafe.Pointer?
//
//go:nosplit
func add(p unsafe.Pointer, x uintptr) unsafe.Pointer {
	return unsafe.Pointer(uintptr(p) + x)
}

// getg returns the pointer to the current g.
// The compiler rewrites calls to this function into instructions
// that fetch the g directly (from TLS or from the dedicated register).
// For amd64 returns *g from the R14 register.
func getg() *g

// link - src/runtime/asm_amd64.s
// mcall switches from the g to the g0 stack and invokes fn(g),
// where g is the goroutine that made the call.
// mcall saves g's current PC/SP in g->sched so that it can be restored later.
// It is up to fn to arrange for that later execution, typically by recording
// g in a data structure, causing something to call ready(g) later.
// mcall returns to the original goroutine g later, when g has been rescheduled.
// fn must not return at all; typically it ends by calling schedule, to let the m
// run other goroutines.
//
// mcall can only be called from g stacks (not g0, not gsignal).
//
// This must NOT be go:noescape: if fn is a stack-allocated closure,
// fn puts g on a run queue, and g executes before fn returns, the
// closure will be invalidated while it is still executing.
func mcall(fn func(*g))

// src/runtime/asm_amd64.s:runtime·mcall<ABIInternal>
//
// systemstack runs fn on a system stack.
// If systemstack is called from the per-OS-thread (g0) stack, or
// if systemstack is called from the signal handling (gsignal) stack,
// systemstack calls fn directly and returns.
// Otherwise, systemstack is being called from the limited stack
// of an ordinary goroutine. In this case, systemstack switches
// to the per-OS-thread stack, calls fn, and switches back.
// It is common to use a func literal as the argument, in order
// to share inputs and outputs with the code around the call
// to system stack:
//
//	... set up y ...
//	systemstack(func() {
//		x = bigcall(y)
//	})
//	... use x ...
//
//go:noescape
func systemstack(fn func())

// TODO!!!!! memmove_amd64.s
//
// memmove copies n bytes from "from" to "to".
//
// memmove ensures that any pointer in "from" is written to "to" with
// an indivisible write, so that racy reads cannot observe a
// half-written pointer. This is necessary to prevent the garbage
// collector from observing invalid pointers, and differs from memmove
// in unmanaged languages. However, memmove is only required to do
// this if "from" and "to" may contain pointers, which can only be the
// case if "from", "to", and "n" are all be word-aligned.
//
// Implementations are in memmove_*.s.
//
//go:noescape
func memmove(to, from unsafe.Pointer, n uintptr)

//go:nosplit
func fastrandn(n uint32) uint32 {
	// TODO!!!!!
	return 0
}

// The compiler knows that a print of a value of this type
// should use printhex instead of printuint (decimal).
type hex uint64

// prototype for src/runtime/asm_amd64.s:gogo
//
// Never returns.
//
// gogo restores the pointer to the goroutine which
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
func gogo(buf *gobuf)

// prototype for src/runtime/asm_amd64.s:procyield
//
// procyield calls the assembly PAUSE instruction repeatedly
// in a loop determined by the `cycles` parameter
//
// https://stackoverflow.com/questions/12894078/what-is-the-purpose-of-the-pause-instruction-in-x86
//
// PAUSE notifies the CPU that this is a spinlock wait loop
// so memory and cache accesses may be optimized.
// PAUSE may actually stop CPU for some time to save power.
// Older CPUs decode it as REP NOP, so you don't have to check if its supported.
// Older CPUs will simply do nothing (NOP) as fast as possible.
//
// The intel manual and other information available state that:
//
//	The processor uses this hint to avoid the memory order violation in most situations,
//	which greatly improves processor performance.
//	For this reason, it is recommended that a PAUSE instruction be placed
//	in all spin-wait loops. An additional function of the PAUSE instruction
//	is to reduce the power consumed by Intel processors.
//
// Branch mispredictions and pipeline flushes:
//
//	https://stackoverflow.com/questions/4725676/how-does-x86-pause-instruction-work-in-spinlock-and-can-it-be-used-in-other-sc
func procyield(cycles uint32)

// Allocate an object of size bytes.
// Small objects are allocated from the per-P cache's free lists.
// Large objects (> 32 kB) are allocated straight from the heap.
func mallocgc(size uintptr, typ *_type, needszero bool) unsafe.Pointer

// ------------------------------------------------------------------------------------------------------------------------------------------------

func throw(s string)

func lock(m *mutex)   {}
func unlock(m *mutex) {}
