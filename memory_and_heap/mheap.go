package heap

import (
	"unsafe"

	"github.com/pianoyeg94/golang-runtime/heap/atomic"
)

// Main malloc heap.
// The heap itself is the "free" and "scav" treaps,
// but all the other global data is here too.
//
// mheap must not be heap-allocated because it contains mSpanLists,
// which must not be heap-allocated.
type mheap struct {
	_ NotInHeap

	// TODO!!!!! lock must only be acquired on the system stack, otherwise a g
	// could self-deadlock if its stack grows with the lock held.
	lock mutex

	pages pageAlloc // page allocation data structure

	// TODO!!!!!
	//
	// allspans is a slice of all mspans ever created. Each mspan
	// appears exactly once.
	//
	// The memory for allspans is manually managed and can be
	// reallocated and move as the heap grows.
	//
	// In general, allspans is protected by mheap_.lock, which
	// prevents concurrent access as well as freeing the backing
	// store. Accesses during STW might not hold the lock, but
	// must ensure that allocation cannot happen around the
	// access (since that may free the backing store).
	allspans []*mspan // all spans out there (backed by off-heap directly allocated memory)

	// arenas is the heap arena map. It points to the metadata for
	// the heap for every arena frame of the entire usable virtual
	// address space.
	//
	// Use arenaIndex to compute indexes into this array.
	//
	// For regions of the address space that are not backed by the
	// Go heap, the arena map contains nil.
	//
	// Modifications are protected by mheap_.lock. Reads can be
	// performed without locking; however, a given entry can
	// transition from nil to non-nil at any time when the lock
	// isn't held. (Entries never transitions back to nil.)
	//
	// In general, this is a two-level mapping consisting of an L1
	// map and possibly many L2 maps. This saves space when there
	// are a huge number of arena frames. However, on many
	// platforms (even 64-bit), arenaL1Bits is 0, making this
	// effectively a single-level map. In this case, arenas[0]
	// will never be nil.
	arenas [1 << arenaL1Bits]*[1 << arenaL2Bits]*heapArena

	// heapArenaAlloc is pre-reserved space for allocating heapArena
	// objects. This is only used on 32-bit, where we pre-reserve
	// this space to avoid interleaving it with the heap itself.
	heapArenaAlloc linearAlloc

	// TODO!!!!! arenaHints is a list of addresses at which to attempt to
	// add more heap arenas. This is initially populated with a
	// set of general hint addresses, and grown with the bounds of
	// actual heap arena ranges.
	arenaHints *arenaHint

	// arena is a pre-reserved space for allocating heap arenas
	// (the actual arenas). This is only used on 32-bit
	// (not initialized on 64-bit, `alloc()` method always
	// returns a `nil` pointer)
	arena linearAlloc

	// allArenas is the arenaIndex of every mapped arena. This can
	// be used to iterate through the address space.
	//
	// Access is protected by mheap_.lock. However, since this is
	// append-only and old backing arrays are never freed because they're
	// allocated using persistentalloc, it is safe to acquire mheap_.lock,
	// copy the slice header, and then release mheap_.lock.
	//
	// When there's no more capacity left a new backing array twice the size
	// of the current one is allocted manually via persistenalloc and all content
	// is copied over.
	// This is done in mheap_.sysAlloc() after reserving space for new arenas
	// and aftert allocating `heapArena` metadata structs.
	allArenas []arenaIdx

	// curArena is the arena that the heap is currently growing
	// into. This should always be physPageSize-aligned.
	//
	// TODO!!!!! Add details about how and when this field is set
	curArena struct {
		// base is the current offset into the arena, which denotes
		// the start of the remaining address range which is not yet
		// mapped into Prepared state.
		//
		// end is the upper boundary of arena's Reserved memory.
		base, end uintptr // are addresses into the offset address space
	}

	// allocator for pointers to mspan structs, which get inserted into
	// per-p mspan struct caches if spanalloc is called with an M with a P,
	// otherwise allocates directly
	spanalloc      fixalloc
	arenaHintAlloc fixalloc // allocator for arenaHints
}

var mheap_ mheap

// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!! A heapArena stores metadata for a heap arena. heapArenas are stored
// outside of the Go heap and accessed via the mheap_.arenas index.
type heapArena struct {
	_ NotInHeap

	// zeroedBase marks the first byte of the first page in this
	// arena which hasn't been used yet and is therefore already
	// zero. zeroedBase is relative to the arena base.
	// Increases monotonically until it hits heapArenaBytes.
	//
	// This field is sufficient to determine if an allocation
	// needs to be zeroed because the page allocator follows an
	// address-ordered first-fit policy.
	//
	// Read atomically and written with an atomic CAS.
	//
	// On first allocation from this arena is 0.
	// When the arena is reused TODO!!!!!
	zeroedBase uintptr
}

// arenaHint is a hint for where to grow the heap arenas. See
// mheap_.arenaHints.
type arenaHint struct {
	_    NotInHeap
	addr uintptr    // addr to allocate at or to allocate to (depending on the value of `down`)
	down bool       // whether or not should allocated below addr
	next *arenaHint // next hint in singly-linked list
}

// An mspan is a run of pages.
//
// When a mspan is in the heap free treap, state == mSpanFree
// and heapmap(s->start) == span, heapmap(s->start+s->npages-1) == span.
// If the mspan is in the heap scav treap, then in addition to the
// above scavenged == true. scavenged == false in all other cases.
//
// When a mspan is allocated, state == mSpanInUse or mSpanManual
// and heapmap(i) == span for all s->start <= i < s->start+s->npages.

// Every mspan is in one doubly-linked list, either in the mheap's
// busy list or one of the mcentral's span lists.

// An mspan representing actual memory has state mSpanInUse,
// mSpanManual, or mSpanFree. Transitions between these states are
// constrained as follows:
//
//   - A span may transition from free to in-use or manual during any GC
//     phase.
//
//   - During sweeping (gcphase == _GCoff), a span may transition from
//     in-use to free (as a result of sweeping) or manual to free (as a
//     result of stacks being freed).
//
//   - During GC (gcphase != _GCoff), a span *must not* transition from
//     manual or in-use to free. Because concurrent GC may read a pointer
//     and then look up its span, the span state must be monotonic.
//
// Setting mspan.state to mSpanInUse or mSpanManual must be done
// atomically and only after all other span fields are valid.
// Likewise, if inspecting a span is contingent on it being
// mSpanInUse, the state should be loaded atomically and checked
// before depending on other fields. This allows the garbage collector
// to safely deal with potentially invalid pointers, since resolving
// such pointers may race with a span being allocated.
type mSpanState uint8

const (
	mSpanDead   mSpanState = iota
	mSpanInUse             // allocated for garbage collected heap
	mSpanManual            // allocated for manual management (e.g., stack allocator)
)

// mSpanStateNames are the names of the span states, indexed by
// mSpanState.
var mSpanStateNames = []string{
	"mSpanDead",
	"mSpanInUse",
	"mSpanManual",
}

// mSpanStateBox holds an atomic.Uint8 to provide atomic operations on
// an mSpanState. This is a separate type to disallow accidental comparison
// or assignment with mSpanState.
type mSpanStateBox struct {
	s atomic.Uint8
}

// It is nosplit to match get, below.

//go:nosplit
func (b *mSpanStateBox) set(s mSpanState) {
	b.s.Store(uint8(s))
}

// It is nosplit because it's called indirectly by typedmemclr,
// which must not be preempted.

//go:nosplit
func (b *mSpanStateBox) get() mSpanState {
	return mSpanState(b.s.Load())
}

// mSpanList heads a linked list of spans.
type mSpanList struct {
	_     NotInHeap
	first *mspan // first span in list, or nil if none
	last  *mspan // last span in list, or nil if none
}

//go:notinheap
type mspan struct {
	// In this particular case mspans are allocated from golang heap's spanalloc
	// fixalloc, which uses off-golang-heap memory.
	//
	// NotInHeap is a type must never be allocated from the GC'd heap or on the stack,
	// and is called not-in-heap.
	//
	// Other types can embed NotInHeap to make it not-in-heap. Specifically, pointers
	// to these types must always fail the `runtime.inheap` check. The type may be used
	// for global variables, or for objects in unmanaged memory (e.g., allocated with
	// sysAlloc, persistentalloc, fixalloc, or from a manually-managed span).
	//
	// Write barriers on pointers to not-in-heap types can be omitted.
	//
	// The last point is the real benefit of NotInHeap. The runtime uses
	// it for low-level internal structures to avoid memory barriers in the
	// scheduler and the memory allocator where they are illegal or simply
	// inefficient. This mechanism is reasonably safe and does not compromise
	// the readability of the runtime.
	_    NotInHeap
	next *mspan     // next span in list, or nil if none
	prev *mspan     // previous span in list, or nil if none
	list *mSpanList // For debugging. TODO: Remove.

	startAddr uintptr // address of first byte of span aka s.base()
	npages    uintptr // number of golang pages in span

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	// TODO!!!!!
	//
	// freeindex is the slot index between 0 and nelems at which to begin scanning
	// for the next free object in this span.
	// Each allocation scans allocBits starting at freeindex until it encounters a 0
	// indicating a free object. freeindex is then adjusted so that subsequent scans begin
	// just past the newly discovered free object.
	//
	// If freeindex == nelem, this span has no free objects.
	//
	// allocBits is a bitmap of objects in this span.
	// If n >= freeindex and allocBits[n/8] & (1<<(n%8)) is 0
	// then object n is free;
	// otherwise, object n is allocated. Bits starting at nelem are
	// undefined and should never be referenced.
	//
	// Object n starts at address n*elemsize + (start << pageShift).
	freeindex uintptr

	// TODO: Look up nelems from sizeclass and remove this field if it
	// helps performance.
	nelems uintptr // number of object in the span.

	// Cache of the allocBits at freeindex. allocCache is shifted
	// such that the lowest bit corresponds to the bit freeindex.
	// allocCache holds the complement of allocBits, thus allowing
	// ctz (count trailing zero) to use it directly.
	// allocCache may contain bits beyond s.nelems; the caller must ignore
	// these.
	allocCache uint64

	// TODO!!!!!
	//
	// allocBits and gcmarkBits hold pointers to a span's mark and
	// allocation bits. The pointers are 8 byte aligned.
	// There are three arenas where this data is held.
	// free: Dirty arenas that are no longer accessed
	//       and can be reused.
	// next: Holds information to be used in the next GC cycle.
	// current: Information being used during this GC cycle.
	// previous: Information being used during the last GC cycle.
	// A new GC cycle starts with the call to finishsweep_m.
	// finishsweep_m moves the previous arena to the free arena,
	// the current arena to the previous arena, and
	// the next arena to the current arena.
	// The next arena is populated as the spans request
	// memory to hold gcmarkBits for the next GC cycle as well
	// as allocBits for newly allocated spans.
	//
	// The pointer arithmetic is done "by hand" instead of using
	// arrays to avoid bounds checks along critical performance
	// paths.
	// The sweep will free the old allocBits and set allocBits to the
	// gcmarkBits. The gcmarkBits are replaced with a fresh zeroed
	// out memory.
	allocBits *gcBits

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	// divMul is required to compute object index from span offset
	// using 32-bit multiplication.
	//
	// Span offset / size of sizeclass is implemented as
	// (<span offset> * (^uint32(0)/uint32(<size of sizeclass>) + 1)) >> 32
	// for all 0 <= n <= c.npages * pageSize
	divMul     uint32        // for divide by elemsize
	allocCount uint16        // number of allocated objects
	spanclass  spanClass     // size class and noscan (uint8)
	state      mSpanStateBox // mSpanInUse etc; accessed atomically (get/set methods)

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	needzero uint8 // needs to be zeroed before allocation

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	elemsize uintptr // computed from sizeclass or from npages

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	// freeIndexForScan is like freeindex, except that freeindex is
	// used by the allocator whereas freeIndexForScan is used by the
	// GC scanner. They are two fields so that the GC sees the object
	// is allocated only when the object and the heap bits are
	// initialized (see also the assignment of freeIndexForScan in
	// mallocgc, and issue 54596).
	freeIndexForScan uintptr
}

// recordspan adds a newly allocated span to h.allspans.
//
// This only happens the first time a span is allocated from
// mheap.spanalloc fixalloc (it is not called when a span is reused).
//
// TODO!!!!!
// Write barriers are disallowed here because it can be called from
// gcWork when allocating new workbufs. However, because it's an
// indirect call from the fixalloc initializer, the compiler can't see
// this.
//
// The heap lock must be held.
//
//go:nowritebarrierrec
func recordspan(vh unsafe.Pointer, p unsafe.Pointer) {
	h := (*mheap)(vh)
	s := (*mspan)(p)

	assertLockHeld(&h.lock)

	if len(h.allspans) >= cap(h.allspans) {
		// not enough space for a new *mspan
		n := 64 * 1024 / PtrSize // number of mspan pointers a 64KiB slice can accomodate (8192 pointers), on first call grow slice capacity to 8192
		if n < cap(h.allspans)*3/2 {
			// as soon as slice length ecxeeds capacity of 8192 pointers (64KiB),
			// grow slice current capacity times 1.5 every time length exceeds capacity.
			n = cap(h.allspans) * 3 / 2
		}
		var new []*mspan
		sp := (*slice)(unsafe.Pointer(&new))
		sp.array = sysAlloc(uintptr(n)*PtrSize, &memstats.other_sys) // mmap backing array with new capacity (off-heap memory)
		if sp.array == nil {
			throw("runtime: cannot allocate memory")
		}
		sp.len = len(h.allspans)
		sp.cap = n
		if len(h.allspans) > 0 {
			copy(new, h.allspans) // copy over data into new backing array from old backing array
		}
		oldAllSpans := h.allspans // reference, to free memory bellow
		// cast new and h.allspans to not in heap slices and store new in h.allspans
		*(*notInHeapSlice)(unsafe.Pointer(&h.allspans)) = *(*notInHeapSlice)(unsafe.Pointer(&new))
		if len(oldAllSpans) != 0 {
			// NOT first allocation of a 64KiB backing array
			sysFree(unsafe.Pointer(&oldAllSpans[0]), uintptr(cap(oldAllSpans))*unsafe.Sizeof(oldAllSpans[0]), &memstats.other_sys)
		}
	}
	h.allspans = h.allspans[:len(h.allspans)+1]
	h.allspans[len(h.allspans)-1] = s // store new mspan pointer in allspans (guarded by heap lock)
}

// A spanClass represents the size class and noscan-ness of a span.
//
// Each size class has a noscan spanClass and a scan spanClass. The
// noscan spanClass contains only noscan objects, which do not contain
// pointers and thus do not need to be scanned by the garbage
// collector.
type spanClass uint8

const (
	numSpanClasses = _NumSizeClasses << 1 // double _NumSizeClasses for noscan classes
	// TODO!!!!! tinySpanClass  = spanClass(tinySizeClass<<1 | 1)
)

func makeSpanClass(sizeclass uint8, noscan bool) spanClass {
	return spanClass(sizeclass<<1) | spanClass(bool2int(noscan)) // least-significant bit marks noscan-ness if set
}

func (sc spanClass) sizeclass() int8 {
	return int8(sc >> 1) // get original sizeclass removing the noscan-ness least-significant bit
}

func (sc spanClass) noscan() bool {
	return sc&1 != 0 // check the noscan-ness least-significant bit
}

// arenaIndex returns the index into mheap_.arenas of the arena
// containing metadata for p. This index combines of an index into the
// L1 map and an index into the L2 map and should be used as
// mheap_.arenas[ai.l1()][ai.l2()].
//
// On many platforms (even 64-bit), arenaL1Bits is 0, making this
// effectively a single-level map. In this case, arenas[0]
// will never be nil and l1() will always return 0. So basically
// this returns a direct index into L2.
//
// If p is outside the range of valid heap addresses, either l1() or
// l2() will be out of bounds.
//
// It is nosplit because it's called by spanOf and several other
// nosplit functions.
//
//go:nosplit
func arenaIndex(p uintptr) arenaIdx {
	return arenaIdx((p - arenaBaseOffset) / heapArenaBytes) // 48-bit address in linear address space divided by 64MB (arena size) gives us the top level index
}

type arenaIdx uint

// l1 returns the "l1" portion of an arenaIdx.
//
// Marked nosplit because it's called by spanOf and other nosplit
// functions.
//
//go:nosplit
func (i arenaIdx) l1() uint {
	if arenaL1Bits == 0 {
		// Let the compiler optimize this away if there's no
		// L1 map (all 64-bit systems except Windows.)
		return 0
	} else {
		return uint(i) >> arenaL1Shift
	}
}

// l2 returns the "l2" portion of an arenaIdx.
//
// Marked nosplit because it's called by spanOf and other nosplit funcs.
// functions.
//
//go:nosplit
func (i arenaIdx) l2() uint {
	if arenaL1Bits == 0 {
		// on 64-bit systems except Windows
		// is equal to the global arena index,
		// which is calculated by dividing a
		// 48-bit linear address by 64MiB (size
		// of a single arena)
		return uint(i)
	} else {
		return uint(i) & (1<<arenaL2Bits - 1)
	}
}

// Initialize the heap.
func (h *mheap) init() {
	// recordspan adds a newly allocated span to h.allspans.
	// This only happens the first time a span is allocated from
	// mheap.spanalloc fixalloc (it is not called when a span is reused).
	h.spanalloc.init(unsafe.Sizeof(mspan{}), recordspan, unsafe.Pointer(h), &memstats.mspan_sys)
	h.arenaHintAlloc.init(unsafe.Sizeof(arenaHint{}), nil, nil, &memstats.other_sys)
}

// spanAllocType represents the type of allocation to make, or
// the type of allocation to be freed.
type spanAllocType uint8

const (
	spanAllocHeap  spanAllocType = iota // heap span
	spanAllocStack                      // stack span
	// TODO!!!!! other types
)

// TODO!!!!!
//
// manual returns true if the span allocation is manually managed.
func (s spanAllocType) manual() bool {
	return s != spanAllocHeap
}

// alloc allocates a new span of npage pages from the GC'd heap.
//
// spanclass indicates the span's size class and scannability.
//
// Returns a span that has been fully initialized. span.needzero indicates
// whether the span has been zeroed. Note that it may not be.
func (h *mheap) alloc(npages uintptr, spanclass spanClass) *mspan {
	// Don't do any operations that lock the heap on the G stack.
	// It might trigger stack growth, and the stack growth code needs
	// to be able to allocate heap.
	var s *mspan
	_ = s

	return nil
}

// TODO!!!!!
//
// allocNeedsZero checks if the region of address space [base, base+npage*pageSize),
// assumed to be allocated, needs to be zeroed, updating heap arena metadata for
// future allocations.
//
// This must be called each time pages are allocated from the heap, even if the page
// allocator can otherwise prove the memory it's allocating is already zero because
// they're fresh from the operating system. It updates heapArena metadata that is
// critical for future page allocations.
//
// There are no locking constraints on this method.
func (h *mheap) allocNeedsZero(base, npage uintptr) (needZero bool) {
	for npage > 0 {
		// arenaIndex returns the index into mheap_.arenas of the arena
		// containing metadata for p. This index combines an index into the
		// L1 map and an index into the L2 map and should be used as
		// mheap_.arenas[ai.l1()][ai.l2()].
		//
		// 48-bit address in linear address space divided by 64MB (arena size)
		// gives us the top level index.
		ai := arenaIndex(base)
		// arenas is the heap arena map. It points to the metadata for
		// the heap for every arena frame of the entire usable virtual
		// address space.
		ha := h.arenas[ai.l1()][ai.l2()]

		// zeroedBase marks the first byte of the first page in this
		// arena which hasn't been used yet and is therefore already
		// zero. zeroedBase is relative to the arena base.
		// Increases monotonically until it hits heapArenaBytes.
		//
		// This field is sufficient to determine if an allocation
		// needs to be zeroed because the page allocator follows an
		// address-ordered first-fit policy.
		//
		// Read atomically and written with an atomic CAS.
		//
		// On first allocation from this arena is 0.
		// When the arena is reused TODO!!!!!
		zeroedBase := atomic.Loaduintptr(&ha.zeroedBase)
		arenaBase := base % heapArenaBytes // byte offset relative to arena's base address
		if arenaBase < zeroedBase {
			// TODO!!!!!
			//
			// We extended into the non-zeroed part of the
			// arena, so this region needs to be zeroed before use.
			//
			// zeroedBase is monotonically increasing, so if we see this now then
			// we can be sure we need to zero this memory region.
			//
			// We still need to update zeroedBase for this arena, and
			// potentially more arenas.
			needZero = true
		}
		// TODO!!!!!
		//
		// We may observe arenaBase > zeroedBase if we're racing with one or more
		// allocations which are acquiring memory directly before us in the address
		// space. But, because we know no one else is acquiring *this* memory, it's
		// still safe to not zero.

		// Compute how far into the arena we extend into, capped
		// at heapArenaBytes.
		arenaLimit := arenaBase + npage*pageSize
		if arenaLimit > heapArenaBytes {
			arenaLimit = heapArenaBytes
		}
		// Increase ha.zeroedBase so it's >= arenaLimit.
		// We may be racing with other updates.
		for arenaLimit > zeroedBase {
			if atomic.Casuintptr(&ha.zeroedBase, zeroedBase, arenaLimit) {
				break
			}
			zeroedBase = atomic.Loaduintptr(&ha.zeroedBase)
			// Double check basic conditions of zeroedBase.
			if zeroedBase <= arenaLimit && zeroedBase > arenaBase {
				// The zeroedBase moved into the space we were trying to
				// claim. That's very bad, and indicates someone allocated
				// the same region we did.
				throw("potentially overlapping in-use allocations detected")
			}
		}

		// Move base forward and subtract from npage to move into
		// the next arena, or finish.
		base += arenaLimit - arenaBase
		npage -= (arenaLimit - arenaBase) / pageSize
	}
	return
}

// TODO!!!!!
//
// tryAllocMSpan attempts to allocate an mspan object from
// the P-local cache, but may fail.
//
// h.lock need not be held.
//
// This caller must ensure that its P won't change underneath
// it during this function. Currently to ensure that we enforce
// that the function is run on the system stack, because that's
// the only place it is used now. In the future, this requirement
// may be relaxed if its use is necessary elsewhere.
//
//go:systemstack
func (h *mheap) tryAllocMSpan() *mspan {
	pp := getg().m.p.ptr()
	// If we don't have a p or the cache is empty, we can't do
	// anything here.
	if pp == nil || pp.mspancache.len == 0 {
		return nil
	}
	// Pull off the last entry in the cache.
	s := pp.mspancache.buf[pp.mspancache.len-1]
	pp.mspancache.len--
	return s
}

// TODO!!!!!
//
// allocMSpanLocked allocates an mspan object.
//
// h.lock must be held.
//
// allocMSpanLocked must be called on the system stack because
// its caller holds the heap lock. See mheap for details.
// Running on the system stack also ensures that we won't
// switch Ps during this function. See tryAllocMSpan for details.
//
//go:systemstack
func (h *mheap) allocMSpanLocked() *mspan {
	assertLockHeld(&h.lock)

	pp := getg().m.p.ptr()
	if pp == nil {
		// We don't have a p so just do the normal thing.
		return (*mspan)(h.spanalloc.alloc())
	}
	// Refill the cache if necessary.
	if pp.mspancache.len == 0 {
		const refillCount = len(pp.mspancache.buf) / 2
		for i := 0; i < refillCount; i++ {
			pp.mspancache.buf[i] = (*mspan)(h.spanalloc.alloc())
		}
		pp.mspancache.len = refillCount
	}
	// Pull off the last entry in the cache.
	s := pp.mspancache.buf[pp.mspancache.len-1]
	pp.mspancache.len--
	return s
}

// TODO!!!!!
//
// freeMSpanLocked free an mspan object.
//
// h.lock must be held.
//
// freeMSpanLocked must be called on the system stack because
// its caller holds the heap lock. See mheap for details.
// Running on the system stack also ensures that we won't
// switch Ps during this function. See tryAllocMSpan for details.
//
//go:systemstack
func (h *mheap) freeMSpanLocked(s *mspan) {
	assertLockHeld(&h.lock)

	pp := getg().m.p.ptr()
	// First try to free the mspan directly to the cache
	// (if we have a P or the cache isn't full).
	if pp != nil && pp.mspancache.len < len(pp.mspancache.buf) {
		pp.mspancache.buf[pp.mspancache.len] = s
		pp.mspancache.len++
		return
	}

	// Failing that (or if we don't have a p), just free it to
	// the heap.
	h.spanalloc.free(unsafe.Pointer(s)) // release fixed size chunk into the head of the linked list of freed chunks managed by spanalloc for later reuse
}

// allocSpan allocates an mspan which owns npages worth of memory.
//
// If typ.manual() == false, allocSpan allocates a heap span of class spanclass
// and updates heap accounting. If manual == true, allocSpan allocates a
// manually-managed span (spanclass is ignored), and the caller is
// responsible for any accounting related to its use of the span. Either
// way, allocSpan will atomically add the bytes in the newly allocated
// span to *sysStat.
//
// The returned span is fully initialized.
//
// h.lock must not be held.
//
// allocSpan must be called on the system stack both because it acquires
// the heap lock and because it must block GC transitions.
//
//go:systemstack
func (h *mheap) allocSpan(npages uintptr, typ spanAllocType, spanclass spanClass) (s *mspan) {
	// Function-global state.
	gp := getg()
	base, scav := uintptr(0), uintptr(0)
	growth := uintptr(0)

	// If the allocation is small enough, try the page cache!
	pp := gp.m.p.ptr()
	if pp != nil && npages < pageCachePages/4 {
		// If we own a P and the allocation requires less than 16 pages,
		// allocate pages from P's page cache which holds 64 pages at-once, some of
		// which may be already in-use.

		c := &pp.pcache

		// If the cache is empty, refill it.
		if c.empty() { // if c.cache == 0
			lock(&h.lock) // lock because requires access to heap's bitmap and summary radix tree

			// FAST PATH:
			// There's free pages at or near the searchAddr address
			// (decuced from last level radix tree summary which is
			// responsible for a single bitmap chunk).
			//
			// Acquires the bitmap chunk within which searchAddr is contained
			// and gets first free page index into the bitmap chunk.
			//
			// Aligns the first free page index down to the 64 pages chunk
			// within the 512 pages bitmap chunk.
			//
			// Sets page cache's base address to offset address which corresponds
			// to the base address of the calculated 64 pages chunk.
			//
			// Sets page cache's 64-bit bitmap cache, where 1s mean free pages and 0s
			// mean in-use pages.
			//
			//
			// SLOW PATH:
			// The searchAddr address had nothing there, so go find
			// the first free page the slow way. Search algorithm:
			//     This algorithm walks each level l of the summaries radix tree from the root level to the leaf level.
			//     It iterates over at most 16384 summaries at level 0 (whole level) and at most 8 summaries at the remaining
			//     4 levels in the radix tree (some iterations may be pruned with the help of `p.searchAddr`), and uses the
			//     summary information to find either:
			//         1) That a given subtree contains a large enough contiguous region, at
			//            which point it continues iterating on the next level, or
			//
			//         2) That there are enough contiguous boundary-crossing bits to satisfy
			//            the allocation, at which point it knows exactly where to start
			//            allocating from.
			//
			// Acquires the bitmap chunk within which the found offset address is contained
			// and gets first free page index into the bitmap chunk.
			//
			// Aligns the first free page index down to the 64 pages chunk
			// within the 512 pages bitmap chunk.
			//
			// Sets page cache's base address to offset address which corresponds
			// to the base address of the calculated 64 pages chunk.
			//
			// Sets page cache's 64-bit bitmap cache, where 1s mean free pages and 0s
			// mean in-use pages.
			//
			//
			// FAST AND SLOW PATH:
			// Updates last level palloc radix tree summary corresponding to
			// the 64-bit bitmap, this summary will tell the user that this
			// 64-page block is allocated. After that updates all the parent
			// summaries above (up to level 0 if neccessary).
			//
			// Set the search address to the last page represented by the cache.
			// Since all of the pages in this block are going to the cache, and we
			// searched for the first free page, we can confidently start at the
			// next page.
			//
			// However, p.searchAddr is not allowed to point into unmapped heap memory
			// unless it is maxSearchAddr, so make it the last page as opposed to
			// the page after.
			*c = h.pages.allocToCache()

			unlock(&h.lock)
		}

		// Try to allocate from the cache.

		// alloc allocates npages from the page cache and is the main entry
		// point for allocation (just updates the 64-bit bitmap).
		//
		// Returns a base address and the amount of scavenged memory in the
		// allocated region in bytes.
		//
		// Returns a base address of zero on failure, in which case the
		// amount of scavenged memory should be ignored.
		base, scav = c.alloc(npages) // TODO!!!!! scav
		if base != 0 {
			// tryAllocMSpan attempts to allocate an mspan object from
			// the P-local cache, but may fail. The mspan object will take
			// charge of the allocated npages.
			s = h.tryAllocMSpan()
			if s != nil {

			}
			// We have a base but no mspan (cache is empty), so we need
			// to lock the heap.
		}
	}

HaveSpan:
	// TODO!!!!!
	//
	// // Decide if we need to scavenge in response to what we just allocated.
	// // Specifically, we track the maximum amount of memory to scavenge of all
	// // the alternatives below, assuming that the maximum satisfies *all*
	// // conditions we check (e.g. if we need to scavenge X to satisfy the
	// // memory limit and Y to satisfy heap-growth scavenging, and Y > X, then
	// // it's fine to pick Y, because the memory limit is still satisfied).
	// //
	// // It's fine to do this after allocating because we expect any scavenged
	// // pages not to get touched until we return. Simultaneously, it's important
	// // to do this before calling sysUsed because that may commit address space.
	// bytesToScavenge := uintptr(0)
	// if limit := gcController.memoryLimit.Load(); go119MemoryLimitSupport && !gcCPULimiter.limiting() {
	// 	// Assist with scavenging to maintain the memory limit by the amount
	// 	// that we expect to page in.
	// 	inuse := gcController.mappedReady.Load()
	// 	// Be careful about overflow, especially with uintptrs. Even on 32-bit platforms
	// 	// someone can set a really big memory limit that isn't maxInt64.
	// 	if uint64(scav)+inuse > uint64(limit) {
	// 		bytesToScavenge = uintptr(uint64(scav) + inuse - uint64(limit))
	// 	}
	// }
	// if goal := scavenge.gcPercentGoal.Load(); goal != ^uint64(0) && growth > 0 {
	// 	// We just caused a heap growth, so scavenge down what will soon be used.
	// 	// By scavenging inline we deal with the failure to allocate out of
	// 	// memory fragments by scavenging the memory fragments that are least
	// 	// likely to be re-used.
	// 	//
	// 	// Only bother with this because we're not using a memory limit. We don't
	// 	// care about heap growths as long as we're under the memory limit, and the
	// 	// previous check for scaving already handles that.
	// 	if retained := heapRetained(); retained+uint64(growth) > goal {
	// 		// The scavenging algorithm requires the heap lock to be dropped so it
	// 		// can acquire it only sparingly. This is a potentially expensive operation
	// 		// so it frees up other goroutines to allocate in the meanwhile. In fact,
	// 		// they can make use of the growth we just created.
	// 		todo := growth
	// 		if overage := uintptr(retained + uint64(growth) - goal); todo > overage {
	// 			todo = overage
	// 		}
	// 		if todo > bytesToScavenge {
	// 			bytesToScavenge = todo
	// 		}
	// 	}
	// }
	// // There are a few very limited cirumstances where we won't have a P here.
	// // It's OK to simply skip scavenging in these cases. Something else will notice
	// // and pick up the tab.
	// var now int64
	// if pp != nil && bytesToScavenge > 0 {
	// 	// Measure how long we spent scavenging and add that measurement to the assist
	// 	// time so we can track it for the GC CPU limiter.
	// 	//
	// 	// Limiter event tracking might be disabled if we end up here
	// 	// while on a mark worker.
	// 	start := nanotime()
	// 	track := pp.limiterEvent.start(limiterEventScavengeAssist, start)

	// 	// Scavenge, but back out if the limiter turns on.
	// 	h.pages.scavenge(bytesToScavenge, func() bool {
	// 		return gcCPULimiter.limiting()
	// 	})

	// 	// Finish up accounting.
	// 	now = nanotime()
	// 	if track {
	// 		pp.limiterEvent.stop(limiterEventScavengeAssist, now)
	// 	}
	// 	scavenge.assistTime.Add(now - start)
	// }

	return nil
}

// initSpan initializes a blank span s which will represent the range
// [base, base+npages*pageSize). typ is the type of span being allocated.
func (h *mheap) initSpan(s *mspan, typ spanAllocType, spanclass spanClass, base, npages uintptr) {
	// At this point, both s != nil and base != 0, and the heap
	// lock is no longer held. Initialize the span.
	s.init(base, npages) // zeroes out mspan fields
	// allocNeedsZero checks if the region of address space [base, base+npage*pageSize),
	// assumed to be allocated, needs to be zeroed, updating heap arena metadata for
	// future allocations.
	//
	// This must be called each time pages are allocated from the heap, even if the page
	// allocator can otherwise prove the memory it's allocating is already zero because
	// they're fresh from the operating system. It updates heapArena metadata that is
	// critical for future page allocations.
	if h.allocNeedsZero(base, npages) {
		s.needzero = 1
	}
	nbytes := npages * pageSize
	if typ.manual() {
		// TODO!!!!!
	} else {
		// We must set span properties before the span is published anywhere
		// since we're not holding the heap lock.
		s.spanclass = spanclass
		if sizeclass := s.spanclass.sizeclass(); sizeclass == 0 {
			// no sizeclass, thus no need to calculate number of
			// elements of a particular sizeclass, whic can fit
			// into this span
			s.elemsize = nbytes
			s.nelems = 1
			// divMul is required to compute object index from span offset
			// using 32-bit multiplication.
			//
			// Span offset / size of sizeclass is implemented as
			// (<span offset> * (^uint32(0)/uint32(<size of sizeclass>) + 1)) >> 32
			// for all 0 <= n <= c.npages * pageSize
			s.divMul = 0
		} else {
			s.elemsize = uintptr(class_to_size[sizeclass])
			s.nelems = nbytes / s.elemsize
			// divMul is required to compute object index from span offset
			// using 32-bit multiplication.
			//
			// Span offset / size of sizeclass is implemented as
			// (<span offset> * (^uint32(0)/uint32(<size of sizeclass>) + 1)) >> 32
			// for all 0 <= n <= c.npages * pageSize
			s.divMul = class_to_divmagic[sizeclass]
		}

		// Initialize mark and allocation structures.

		// freeindex is the slot index between 0 and nelems at which to begin scanning
		// for the next free object in this span.
		// Each allocation scans allocBits starting at freeindex until it encounters a 0
		// indicating a free object. freeindex is then adjusted so that subsequent scans begin
		// just past the newly discovered free object.
		//
		// If freeindex == nelem, this span has no free objects.
		//
		// allocBits is a bitmap of objects in this span.
		// If n >= freeindex and allocBits[n/8] & (1<<(n%8)) is 0
		// then object n is free;
		// otherwise, object n is allocated. Bits starting at nelem are
		// undefined and should never be referenced.
		//
		// Object n starts at address n*elemsize + (start << pageShift).
		s.freeindex = 0

		// freeIndexForScan is like freeindex, except that freeindex is
		// used by the allocator whereas freeIndexForScan is used by the
		// GC scanner. They are two fields so that the GC sees the object
		// is allocated only when the object and the heap bits are
		// initialized (see also the assignment of freeIndexForScan in
		// mallocgc, and issue 54596).
		s.freeIndexForScan = 0

		// Cache of the allocBits at freeindex. allocCache is shifted
		// such that the lowest bit corresponds to the bit freeindex.
		// allocCache holds the complement of allocBits, thus allowing
		// ctz (count trailing zero) to use it directly.
		// allocCache may contain bits beyond s.nelems; the caller must ignore
		// these.
		s.allocCache = ^uint64(0) // all 1s indicating all free.

		// s.gcmarkBits = newMarkBits(s.nelems)  TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

		// newAllocBits returns a pointer to 8 byte aligned bytes
		// to be used for this span's alloc bits.
		// newAllocBits is used to provide newly initialized spans
		// allocation bits. For spans not being initialized the
		// mark bits are repurposed as allocation bits when
		// the span is swept (TODO!!!!! repurposed part).
		s.allocBits = newAllocBits(s.nelems)

		// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
		// It's safe to access h.sweepgen without the heap lock because it's
		// only ever updated with the world stopped and we run on the
		// systemstack which blocks a STW transition.
		// atomic.Store(&s.sweepgen, h.sweepgen)
	}

	// TODO!!!!!
}

// Try to add at least npage golang pages of memory to the heap,
// returning how much the heap grew by and whether it worked.
//
// h.lock must be held.
func (h *mheap) grow(npage uintptr) (uintptr, bool) {
	assertLockHeld(&h.lock)

	// We must grow the heap in whole palloc chunks,
	// since palloc cannot deel with increments smaller
	// than its bitmap chunks. The whole bitmap is a 2-level
	// array consisting of 8192-element top-level array
	// of pointers to arrays, where each pointer points either
	// to nothing (nil-pointer), signifying, that its corresponding
	// address range isn't mapped (not mapped at all, or is in Reserved
	// state, but not Prepared), or it points to an 8-element array of
	// 64-bit unsigned integers, which comprise a single bitmap chunk.
	// That's why palloc can grow its coverage of the address space only
	// in such increments, because the smallest unit of allocation is this
	// 8-element array. A single bitmap chunk (2nd level array) is responsible
	// for 4MiB of address space, which is 512 golang 8KiB pages in total.
	//
	// We call sysMap below but note that because we
	// round up to pallocChunkPages which is on the order
	// of MiB (generally >= to the huge page size) we
	// won't be calling it too much (each virtual address
	// in the address space will only be mapped once).
	//
	// Align growth bellow (transition part of arena from
	// Reserved to Prepared) to a multiple of 512 8KiB golang
	// pages.
	ask := alignUp(npage, pallocChunkPages) * pageSize

	totalGrowth := uintptr(0) // how much heap memory was transitioned from Reserved to Prepared

	// curArena is the arena that the heap is currently growing
	// into. This should always be physPageSize-aligned (4KiB).
	//
	// This may overflow because ask could be very large
	// and is otherwise unrelated to h.curArena.base
	//
	// Initially h.curArena.base is equal to 0 as well as
	// h.curArena.end, on first heap growth nBase will always
	// exceed h.curArena.end, which leeds to reservation of at
	// least one arena at one of the hint addresses (or more
	// than one arena if ask (realy nBase) bytes exceed the
	// size of a single arena).
	//
	// As the heap grows, new arena reservations will happen,
	// when the current reservation cannot accomodate ask bytes
	// (realy nBase bytes).
	end := h.curArena.base + ask
	nBase := alignUp(end, physPageSize) // heap growth must always be alligned to physical 4KiB pages
	if nBase > h.curArena.end || /* overflow */ end < h.curArena.base {
		// Not enough room in the current arena. Allocate
		// (reserve) more arena space. This may not be contiguous
		// with the current arena, so we have to request the full ask.
		//
		// h.sysAlloc always aligns ask to be a multiple of arena size
		// (64MiB).
		//
		// Tries to reserve arena space at address pointed to by the
		// first arenaHint in the arenaHints, if it fails it tries
		// the next arenaHint, discarding the unsuccessfull one from
		// the list and so on, until the reservation succeeds or no
		// more hints are left (all discared from list).
		//
		// If all hints fail, reserves space at any address which the
		// OS hands out. In this case 2 new hints are created and inserted
		// into the list of hints. First comes the hint that comes after the
		// end address of the current reservation and the second one resides
		// just bellow the base address of the reservation, marked as growing
		// down.
		//
		// hintList is an off-heap 128-element singly-linked list of arenaHint pointers,
		// where the first hint initally holds an address of 0x000000c000000000 and the last
		// one an address of 0x00007fc000000000 (also initally). All of the arenaHint addresses are
		// 1 TiB apart from one another.
		//
		// Also creates arena metadata for reserved arena(s). On 64-bit Linux base
		// arenaIndex is calculated by dividing the offset address translated to a
		// linear address into 64MiB (size of a single arena). So basically the global
		// arena index is identical to the level 2 arena index  (level 1 only holds one
		// entry at index 0, this entry is a pointer to an array of pointers to `heapArena`
		// structs. This level 2 array is of size "the 48 bit linear address space divided
		// by 64MiB", which is 4194304).
		//
		// On all 64-bit systems except Windows the 2nd-level array is nil only on first call
		// to h.sysAlloc. This 2nd-level array uses an anonymous private mmap on Linux.
		// Uses sysAllocOS instead of sysAlloc or persistentalloc to allocate backing memory
		// for this array. because there's no statistic we can comfortably account for this space
		// in. With this structure, we rely on demand paging to avoid large overheads, but tracking
		// which memory is paged in is too expensive. Trying to account for the whole region means
		// that it will appear like an enormous memory overhead in statistics, even though it is not
		// (on all 64-bit systems except Windows requires 32MiB of virtual address space to fit the single
		// level 2 array of 4194304 pointers to `heapArena` structs).
		//
		// Allocation of heapArena structs on 64-bit systems happens through persistentalloc, which
		// requests a pointer-size-aligned `heapArena`-sized zeroed chunk in the Ready state
		// from persistent alloc, which may in turn go to the OS to mmap another 256KB
		// chunk, if the current one cannot accomodate another `heapArena`.
		av, asize := h.sysAlloc(ask, &h.arenaHints, true)
		if av == nil {
			// TODO!!!!!
			// inUse := gcController.heapFree.load() + gcController.heapReleased.load() + gcController.heapInUse.load()
			inUse := 12345678 // dummy value, need to uncoment above line
			print("runtime: out of memory: cannot allocate ", ask, "-byte block (", inUse, " in use)\n")
			return 0, false
		}

		if uintptr(av) == h.curArena.end {
			// The new space is contiguous with the old
			// space, so just extend the current space.
			//
			// This scenario is more frequent than the
			// discontiguous one bellow, because it's very
			// unlikely that some off-heap allocation will
			// happen in the 1 TiB space covered by a single
			// arenaHint.
			h.curArena.end = uintptr(av) + asize
		} else {
			// The new space is discontiguous. Track what
			// remains of the current space and switch to
			// the new space. This should be rare, because
			// it's very unlikely that some off-heap allocation
			// will happen in the 1 TiB space covered by a single
			// arenaHint.
			//
			// Since the memory backing an arena is initially
			// in Reserved state and only parts of it are lazilly
			// and contiguously transitioned to Prepared state in
			// this same method, tracking the remaining space means,
			// mapping it into Prepared state (on Linux this means
			// mmapping this space with read and write protocol flags)
			// and setting up the page allocater to track the pages
			// belonging to this remaining space, so that the it can be
			// used for allocation.
			if size := h.curArena.end - h.curArena.base; size != 0 {
				// On Linux mmaps the remaining space with read and write
				// protocol flags + registers the mapped in memory with the
				// `&gcController.heapReleased` sysMemStat. On Linux this means
				// that it's ready for allocation, but within the go runtime
				// the memory is still considered in the Prepared state. Chunks
				// of this remaining space will transition to Ready, when they're
				// used for allocation. The process of transitioning Prepared memory
				// to Ready includes possibly applying the Transparent Huge Pages
				// optimization.
				sysMap(unsafe.Pointer(h.curArena.base), size, nil) // last argument is `&gcController.heapReleased`
				// TODO!!!!!
				//
				// Update stats.
				// stats := memstats.heapStats.acquire()
				// atomic.Xaddint64(&stats.released, int64(size))
				// memstats.heapStats.release()

				// Update the page allocator's structures to make this
				// space ready for allocation.
				//
				// Rounds h.curArena.base down and (h.curArena.base + size)
				// up to whole palloc chunks, since palloc cannot deel with
				// increments smaller than its bitmap chunks. The whole bitmap
				// is a 2-level array consisting of 8192-element top-level array
				// of pointers to arrays, where each pointer points either
				// to nothing (nil-pointer), signifying, that its corresponding
				// address range isn't mapped (not mapped at all, or is in Reserved
				// state, but not Prepared), or it points to an 8-element array of
				// 64-bit unsigned integers, which comprise a single bitmap chunk.
				// That's why palloc can grow its coverage of the address space only
				// in such increments, because the smallest unit of allocation is this
				// 8-element array. A single bitmap chunk (2nd level array) is responsible
				// for 4MiB of address space, which is 512 golang 8KiB pages in total.
				//
				// Maps in memory (transitions from Reserved to Prepared and then to Ready)
				// backing parts of palloc's summaries radix trie (at all levels), which are
				// responsible for palloc's bitmap-chunk-aligned h.curArena.base and
				// h.curArena.base + size. Uses palloc's inUse address ranges structure to
				// avoid remapping parts of summary levels, which are already mapped. Also
				// extends the length of each summary level if neccessary.
				//
				// Registers the bitmap-chunk-aligned h.curArena.base and h.curArena.base + size
				// address range in palloc's inUse structure, possibly merging with contiguous
				// address ranges.
				//
				// Maps off-heap bitmap chunks covering the bitmap-chunk-aligned h.curArena.base
				// and h.curArena.base + size address range.
				//
				// Walks up the summary radix tree and updates the summaries appropriately, according
				// to changes made to the free/in-use pages in the bitmap. The grow operation works
				// with a new contiguos chunk of memory and since it's new memory it's not allocated as well
				h.pages.grow(h.curArena.base, size)
				totalGrowth += size // TODO!!!!!
			}
			// Switch to the new space.
			h.curArena.base = uintptr(av)
			h.curArena.end = uintptr(av) + asize
		}

		// Recalculate nBase according to the base of the freshly reserved
		// arena.
		// We know this won't overflow, because sysAlloc returned
		// a valid region starting at h.curArena.base which is at
		// least ask bytes in size.
		nBase = alignUp(h.curArena.base+ask, physPageSize)
	}

	// Grow into the current arena (possibly freshly allocated).
	v := h.curArena.base
	h.curArena.base = nBase

	// TODO!!!!!
	//
	// Transition the space we're going to use from Reserved to Prepared.
	//
	// The allocation is always aligned to the heap arena
	// size which is always > physPageSize, so its safe to
	// just add directly to heapReleased.
	sysMap(unsafe.Pointer(v), nBase-v, nil) // last argument should be `&gcController.heapReleased`

	// TODO!!!!!
	//
	// The memory just allocated counts as both released
	// and idle, even though it's not yet backed by spans.
	// stats := memstats.heapStats.acquire()
	// atomic.Xaddint64(&stats.released, int64(nBase-v))
	// memstats.heapStats.release()

	// Update the page allocator's structures to make this
	// space ready for allocation.
	//
	// Rounds h.curArena.base down and (h.curArena.base + size)
	// up to whole palloc chunks, since palloc cannot deel with
	// increments smaller than its bitmap chunks. The whole bitmap
	// is a 2-level array consisting of 8192-element top-level array
	// of pointers to arrays, where each pointer points either
	// to nothing (nil-pointer), signifying, that its corresponding
	// address range isn't mapped (not mapped at all, or is in Reserved
	// state, but not Prepared), or it points to an 8-element array of
	// 64-bit unsigned integers, which comprise a single bitmap chunk.
	// That's why palloc can grow its coverage of the address space only
	// in such increments, because the smallest unit of allocation is this
	// 8-element array. A single bitmap chunk (2nd level array) is responsible
	// for 4MiB of address space, which is 512 golang 8KiB pages in total.
	//
	// Maps in memory (transitions from Reserved to Prepared and then to Ready)
	// backing parts of palloc's summaries radix trie (at all levels), which are
	// responsible for palloc's bitmap-chunk-aligned h.curArena.base and
	// h.curArena.base + size. Uses palloc's inUse address ranges structure to
	// avoid remapping parts of summary levels, which are already mapped. Also
	// extends the length of each summary level if neccessary.
	//
	// Registers the bitmap-chunk-aligned h.curArena.base and h.curArena.base + size
	// address range in palloc's inUse structure, possibly merging with contiguous
	// address ranges.
	//
	// Maps off-heap bitmap chunks covering the bitmap-chunk-aligned h.curArena.base
	// and h.curArena.base + size address range.
	//
	// Walks up the summary radix tree and updates the summaries appropriately, according
	// to changes made to the free/in-use pages in the bitmap. The grow operation works
	// with a new contiguos chunk of memory and since it's new memory it's not allocated as well.
	h.pages.grow(v, nBase-v)
	totalGrowth += nBase - v // how much heap memory was transitioned from Reserved to Prepared
	return totalGrowth, true
}

// Initialize a new span with the given start and npages.
func (span *mspan) init(base uintptr, npages uintptr) {
	// span is *not* zeroed.
	span.next = nil       // next span in list, or nil if none
	span.prev = nil       // previous span in list, or nil if none
	span.list = nil       // For debugging. TODO: Remove.
	span.startAddr = base // address of first byte of span aka s.base()
	span.npages = npages  // number of golang pages in span
	span.allocCount = 0   // number of allocated objects
	span.spanclass = 0    // size class and noscan (uint8)
	span.elemsize = 0     // computed from sizeclass or from npages

	// TODO!!!!! span.speciallock.key = 0

	// TODO!!!!! span.specials = nil

	span.needzero = 0 // needs to be zeroed before allocation
	// freeindex is the slot index between 0 and nelems at which to begin scanning
	// for the next free object in this span.
	// Each allocation scans allocBits starting at freeindex until it encounters a 0
	// indicating a free object. freeindex is then adjusted so that subsequent scans begin
	// just past the newly discovered free object.
	//
	// If freeindex == nelem, this span has no free objects.
	//
	// allocBits is a bitmap of objects in this span.
	// If n >= freeindex and allocBits[n/8] & (1<<(n%8)) is 0
	// then object n is free;
	// otherwise, object n is allocated. Bits starting at nelem are
	// undefined and should never be referenced.
	//
	// Object n starts at address n*elemsize + (start << pageShift).
	span.freeindex = 0

	// TODO!!!!! span.freeIndexForScan = 0

	// TODO!!!!!
	// allocBits and gcmarkBits hold pointers to a span's mark and
	// allocation bits. The pointers are 8 byte aligned.
	// There are three arenas where this data is held.
	// free: Dirty arenas that are no longer accessed
	//       and can be reused.
	// next: Holds information to be used in the next GC cycle.
	// current: Information being used during this GC cycle.
	// previous: Information being used during the last GC cycle.
	// A new GC cycle starts with the call to finishsweep_m.
	// finishsweep_m moves the previous arena to the free arena,
	// the current arena to the previous arena, and
	// the next arena to the current arena.
	// The next arena is populated as the spans request
	// memory to hold gcmarkBits for the next GC cycle as well
	// as allocBits for newly allocated spans.
	//
	// The pointer arithmetic is done "by hand" instead of using
	// arrays to avoid bounds checks along critical performance
	// paths.
	// The sweep will free the old allocBits and set allocBits to the
	// gcmarkBits. The gcmarkBits are replaced with a fresh zeroed
	// out memory.
	span.allocBits = nil

	// TODO!!!!! span.gcmarkBits = nil

	// TODO!!!!! span.pinnerBits = nil

	// TODO!!!!! span.state.set(mSpanDead)

	// TODO!!!!! lockInit(&span.speciallock, lockRankMspanSpecial)
}

// gcBits is an alloc/mark bitmap. This is always used as gcBits.x.
type gcBits struct {
	_ NotInHeap
	x uint8 // first byte of bitmap, size of bitmap byte array depends on the number of elements to be tracked (1 bit = 1 element)
}

// bytep returns a pointer to the n'th byte of b.
func (b *gcBits) bytep(n uintptr) *uint8 {
	return addb(&b.x, n)
}

// bitp returns a pointer to the byte containing bit n and a mask for
// selecting that bit from *bytep.
func (b *gcBits) bitp(n uintptr) (bytep *uint8, mask uint8) {
	return b.bytep(n / 8), 1 << (n % 8)
}

const gcBitsChunkBytes = uintptr(64 << 10)              // TODO!!!!! size of special arenas which hold mark bit bytes (64KiB)
const gcBitsHeaderBytes = unsafe.Sizeof(gcBitsHeader{}) // currently the fields of gcBitsHeader are directly embedded in gcBitsArena

// currently the fields of gcBitsHeader are directly embedded in gcBitsArena
type gcBitsHeader struct {
	free uintptr // free is the index into bits of the next free byte.
	next uintptr // *gcBits triggers recursive type bug. (issue 14620)
}

// TODO!!!!!
type gcBitsArena struct {
	_ NotInHeap
	// gcBitsHeader // side step recursive type bug (issue 14620) by including fields by hand.
	free uintptr // free is the index into bits of the next free byte; read/write atomically
	next *gcBitsArena
	bits [gcBitsChunkBytes - gcBitsHeaderBytes]gcBits // TODO!!!!! array length is 65520
}

var gcBitsArenas struct {
	lock     mutex
	free     *gcBitsArena // recycled and "race-condition" released gcBitsArenas
	next     *gcBitsArena // Read atomically. Write atomically under lock. (current gcBitsArena which we allocate from)
	current  *gcBitsArena // TODO!!!!! check out nextMarkBitArenaEpoch
	previous *gcBitsArena // TODO!!!!! check out nextMarkBitArenaEpoch
}

// tryAlloc allocates from b or returns nil if b does not have enough room.
// This is safe to call concurrently.
func (b *gcBitsArena) tryAlloc(bytes uintptr) *gcBits {
	if b == nil || atomic.Loaduintptr(&b.free)+bytes > uintptr(len(b.bits)) /* or if bytes requested would exceed the gcBitsArena.bits array  */ {
		return nil
	}

	// Try to allocate from this block.
	end := atomic.Xadduintptr(&b.free, bytes)
	if end > uintptr(len(b.bits)) { // someone allocated the left over gcBits from gcBitsArena.bits array right after the previous check
		return nil
	}

	// There was enough room.
	start := end - bytes
	return &b.bits[start]
}

// newMarkBits returns a pointer to 8 byte aligned bytes
// to be used for a span's mark bits.
func newMarkBits(nelems uintptr) *gcBits {
	blocksNeeded := uintptr((nelems + 63) / 64) // adding 63 before division makes sure we have enough blocks to accommodate n elements
	bytesNeeded := blocksNeeded * 8

	// Try directly allocating from the current head arena.
	head := (*gcBitsArena)(atomic.Loadp(unsafe.Pointer(&gcBitsArenas.next)))
	if p := head.tryAlloc(bytesNeeded); p != nil {
		return p
	}

	// There's not enough room in the head arena. We may need to
	// allocate a new arena.
	lock(&gcBitsArenas.lock)
	// Try the head arena again, since it may have changed. Now
	// that we hold the lock, the list head can't change, but its
	// free position still can (someone can be in the middle of a
	// concurrent call to newMarkBits, the first `head.tryAlloc`
	// isn't done while holding the lock and head's free position is
	// incremented with an atomic operation and doesn't require a lock).
	if p := gcBitsArenas.next.tryAlloc(bytesNeeded); p != nil {
		unlock(&gcBitsArenas.lock)
		return p
	}

	// Allocate a new arena. This may temporarily drop the lock while allocating from the OS.
	fresh := newArenaMayUnlock()
	// If newArenaMayUnlock dropped the lock, another thread may
	// have put a fresh arena on the "next" list (section down bellow
	// commented as `Add the fresh arena to the "next" list.`).
	// Try allocating from next again.
	if p := gcBitsArenas.next.tryAlloc(bytesNeeded); p != nil {
		// Put fresh back on the free list (as head)
		// TODO: Mark it "already zeroed"
		fresh.next = gcBitsArenas.free
		gcBitsArenas.free = fresh
		unlock(&gcBitsArenas.lock)
		return p
	}

	// Allocate from the fresh arena. We haven't linked it in yet
	// and the lock is held since last time we've checked gcBitsArenas.next,
	// so this cannot race and is guaranteed to succeed.
	p := fresh.tryAlloc(bytesNeeded)
	if p == nil {
		throw("markBits overflow")
	}

	// Add the fresh arena to the "next" list.
	fresh.next = gcBitsArenas.next
	// atomic write since a concurrent atomic load
	// can happen without the lock held.
	atomic.StorepNoWB(unsafe.Pointer(&gcBitsArenas.next), unsafe.Pointer(fresh))

	unlock(&gcBitsArenas.lock)
	return p
}

// newAllocBits returns a pointer to 8 byte aligned bytes
// to be used for this span's alloc bits.
// newAllocBits is used to provide newly initialized spans
// allocation bits. For spans not being initialized the
// mark bits are repurposed as allocation bits when
// the span is swept (TODO!!!!! repurposed part).
func newAllocBits(nelems uintptr) *gcBits {
	return newMarkBits(nelems)
}

// newArenaMayUnlock allocates and zeroes a gcBits arena, which is
// 64KiB in size on 64-bit architectures.
// The caller must hold gcBitsArena.lock. This may temporarily release it.
func newArenaMayUnlock() *gcBitsArena {
	var result *gcBitsArena
	if gcBitsArenas.free == nil { // No previously cached gcBitsArena (check comments in else case and comments in newMarkBits)
		unlock(&gcBitsArenas.lock)
		// anonymous private read and write off-go-heap mmap under the hood
		// (non-prefaulted, physicall memory allocation
		// only occurs when the virtual memory is written to)
		result = (*gcBitsArena)(sysAlloc(gcBitsChunkBytes, nil)) // TODO!!!! &memstats.gcMiscSys should passed in instead of nil
		if result == nil {
			throw("runtime: cannot allocate memory")
		}
		lock(&gcBitsArenas.lock)
	} else {
		// TODO!!!!! check out nextMarkBitArenaEpoch

		// if there's an already cached arena in .free,
		// because on a previous call to newMarkBits we
		// had to cache a newly allocated gcBitsArena
		// in .free, due to the fact that we released
		// the lock during allocation of this new
		// gcBitsArena in the `if gcBitsArenas.free == nil`
		// case above, and some other concurrent call to
		// newMarkBits put a new gcBitsArena into .next.
		//
		// Pop free from the list.
		result = gcBitsArenas.free
		gcBitsArenas.free = gcBitsArenas.free.next
		memclrNoHeapPointers(unsafe.Pointer(result), gcBitsChunkBytes) // clear gcBitsArena's fields, has no pointers into runtime's heap
	}
	result.next = nil
	// If result.bits is not 8 byte aligned adjust index so
	// that &result.bits[result.free] is 8 byte aligned.
	//
	// Currently always 8 byte aligned even for 32-bit architectures,
	// because gcBitsHeader is currently commented out in gcBitsArena.
	// On 32-bit architectures if gcBitsHeader gets uncommented, &result.bits[0]
	// will resided on a 4 byte boundary, so result.free will start at index 4.
	if uintptr(unsafe.Offsetof(gcBitsArena{}.bits))&7 == 0 { // 8 byte alligned
		result.free = 0
	} else {
		result.free = 8 - (uintptr(unsafe.Pointer(&result.bits[0])) & 7)
	}
	return result
}
