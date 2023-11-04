package heap

import (
	"unsafe"

	"github.com/pianoyeg94/go-runtime-inside-out/memory_and_heap/atomic"
)

const (
	// log base 2 of pageSize
	pageShift = _PageShift
	// golang page size (8192 bytes)
	// (not system page size, which is by default 4096 bytes on x86-64 Linux)
	pageSize = _PageSize

	_PageSize = 1 << _PageShift

	// _64bit = 1 on 64-bit systems, 0 on 32-bit systems
	_64bit = 1 << (^uintptr(0) >> 63) / 2 // (1 << (^uintptr(0) >> 63)) is equal to to on 64 bit and is equal to 1 on 32 bit

	_FixAllocChunk = 16 << 10 // approximate size of chunks FixAlloc allocated from persistentalloc (16KB), точный размер TODO!!!!!

	// heapAddrBits is the number of bits in a heap address. On
	// amd64, addresses are sign-extended beyond heapAddrBits. On
	// other arches, they are zero-extended.
	//
	// On most 64-bit platforms, we limit this to 48 bits based on a
	// combination of hardware and OS limitations.
	//
	// amd64 hardware limits addresses to 48 bits, sign-extended
	// to 64 bits. Addresses where the top 16 bits are not either
	// all 0 or all 1 are "non-canonical" and invalid. Because of
	// these "negative" addresses, we offset addresses by 1<<47
	// (arenaBaseOffset) on amd64 before computing indexes into
	// the heap arenas index. In 2017, amd64 hardware added
	// support for 57 bit addresses; however, currently only Linux
	// supports this extension and the kernel will never choose an
	// address above 1<<47 unless mmap is called with a hint
	// address above 1<<47 (which we never do).
	//
	// arm64 hardware (as of ARMv8) limits user addresses to 48
	// bits, in the range [0, 1<<48).
	//
	// ppc64, mips64, and s390x support arbitrary 64 bit addresses
	// in hardware. On Linux, Go leans on stricter OS limits. Based
	// on Linux's processor.h, the user address space is limited as
	// follows on 64-bit architectures:
	//
	// Architecture  Name              Maximum Value (exclusive)
	// ---------------------------------------------------------------------
	// amd64         TASK_SIZE_MAX     0x007ffffffff000 (47 bit addresses)
	// arm64         TASK_SIZE_64      0x01000000000000 (48 bit addresses)
	// ppc64{,le}    TASK_SIZE_USER64  0x00400000000000 (46 bit addresses)
	// mips64{,le}   TASK_SIZE64       0x00010000000000 (40 bit addresses)
	// s390x         TASK_SIZE         1<<64 (64 bit addresses)
	//
	// These limits may increase over time, but are currently at
	// most 48 bits except on s390x. On all architectures, Linux
	// starts placing mmap'd regions at addresses that are
	// significantly below 48 bits, so even if it's possible to
	// exceed Go's 48 bit limit, it's extremely unlikely in
	// practice.
	//
	// On 32-bit platforms, we accept the full 32-bit address
	// space because doing so is cheap.
	// mips32 only has access to the low 2GB of virtual memory, so
	// we further limit it to 31 bits.
	//
	// On ios/arm64, although 64-bit pointers are presumably
	// available, pointers are truncated to 33 bits in iOS <14.
	// Furthermore, only the top 4 GiB of the address space are
	// actually available to the application. In iOS >=14, more
	// of the address space is available, and the OS can now
	// provide addresses outside of those 33 bits. Pick 40 bits
	// as a reasonable balance between address space usage by the
	// page allocator, and flexibility for what mmap'd regions
	// we'll accept for the heap. We can't just move to the full
	// 48 bits because this uses too much address space for older
	// iOS versions.
	// TODO(mknyszek): Once iOS <14 is deprecated, promote ios/arm64
	// to a 48-bit address space like every other arm64 platform.
	//
	// WebAssembly currently has a limit of 4GB linear memory.
	heapAddrBits = 48

	// The number of bits in a heap address, the size of heap
	// arenas, and the L1 and L2 arena map sizes are related by
	//
	//   (1 << addr bits) = arena size * L1 entries * L2 entries
	//
	// Currently, we balance these as follows:
	//
	//       Platform  Addr bits  Arena size  L1 entries   L2 entries
	// --------------  ---------  ----------  ----------  -----------
	//       */64-bit         48        64MB           1    4M (32MB)
	// windows/64-bit         48         4MB          64    1M  (8MB)
	//      ios/arm64         33         4MB           1  2048  (8KB)
	//       */32-bit         32         4MB           1  1024  (4KB)
	//     */mips(le)         31         4MB           1   512  (2KB)

	// heapArenaBytes is the size of a heap arena. The heap
	// consists of mappings of size heapArenaBytes, aligned to
	// heapArenaBytes. The initial heap mapping is one arena.
	//
	// This is currently 64MB on 64-bit non-Windows and 4MB on
	// 32-bit and on Windows. We use smaller arenas on Windows
	// because all committed memory is charged to the process,
	// even if it's not touched. Hence, for processes with small
	// heaps, the mapped arena space needs to be commensurate.
	// This is particularly important with the race detector,
	// since it significantly amplifies the cost of committed
	// memory.
	heapArenaBytes = 1 << logHeapArenaBytes // 67108864 (64MB)

	// logHeapArenaBytes is log_2 of heapArenaBytes. For clarity,
	// prefer using heapArenaBytes where possible (we need the
	// constant to compute some other constants).
	logHeapArenaBytes = (6 + 20) * _64bit // 26

	// arenaL1Bits is the number of bits of the arena number
	// covered by the first level arena map.
	//
	// This number should be small, since the first level arena
	// map requires PtrSize*(1<<arenaL1Bits) of space in the
	// binary's BSS. It can be zero (all platforms except 64-bit Windows),
	// in which case the first level index is effectively unused.
	// There is a performance benefit to this, since the generated
	// code can be more efficient, but comes at the cost of having
	// a large L2 mapping.
	//
	// We use the L1 map on 64-bit Windows because the arena size
	// is small, but the address space is still 48 bits, and
	// there's a high cost to having a large L2.
	arenaL1Bits = 0

	// TODO!!!!! arenaL2Bits is the number of bits of the arena number
	// covered by the second level arena index.
	//
	// The size of each arena map allocation is proportional to
	// 1<<arenaL2Bits, so it's important that this not be too
	// large. 48 bits leads to 32MB arena index allocations, which
	// is about the practical threshold.
	arenaL2Bits = heapAddrBits - logHeapArenaBytes - arenaL1Bits // 22

	// arenaL1Shift is the number of bits to shift an arena frame
	// number by to compute an index into the first level arena map.
	// NOT used by 64-bit systems, except Windows.
	arenaL1Shift = arenaL2Bits

	// arenaBits is the total bits in a combined arena map index.
	// This is split between the index into the L1 arena map and
	// the L2 arena map.
	//
	// On all platforms except 64-bit Windows has the same value as arenaL2Bits, because
	// arenaL1Bits == 0.
	arenaBits = arenaL1Bits + arenaL2Bits // 22

	// arenaBaseOffset is the pointer value that corresponds to
	// index 0 in the heap arena map.
	//
	// On amd64, the address space is 48 bits, sign extended to 64
	// bits. This offset lets us handle "negative" addresses (or
	// high addresses if viewed as unsigned).
	//
	// On aix/ppc64, this offset allows to keep the heapAddrBits to
	// 48. Otherwise, it would be 60 in order to handle mmap addresses
	// (in range 0x0a00000000000000 - 0x0afffffffffffff). But in this
	// case, the memory reserved in (s *pageAlloc).init for chunks
	// is causing important slowdowns.
	//
	// On other platforms, the user address space is contiguous
	// and starts at 0, so no offset is necessary.
	arenaBaseOffset = 0xffff800000000000
)

// physPageSize is the size in bytes of the OS's physical pages.
// Mapping and unmapping operations must be done at multiples of
// physPageSize.
//
// This must be set by the OS init code (typically in osinit) before
// mallocinit.
//
// On 64-bit Linux is usually 4096 bytes (4KiB).
var physPageSize uintptr

// physHugePageSize is the size in bytes of the OS's default physical huge
// page size whose allocation is opaque to the application. It is assumed
// and verified to be a power of two.
//
// On Linux physHugePageSize is equal to 2MiB, which is the only possible
// value to be used with THP (Transparent Huge Pages).
// If set, this must be set by the OS init code (typically in osinit) before
// mallocinit. However, setting it at all is optional, and leaving the default
// value is always safe (though potentially less efficient).
//
// Since physHugePageSize is always assumed to be a power of two,
// physHugePageShift is defined as physHugePageSize == 1 << physHugePageShift.
// The purpose of physHugePageShift is to avoid doing divisions in
// performance critical functions. TODO!!!!! what are these functions?
//
// physHugePageShift is equal to 21 on Linux, which is the log base 2 of 2MiB.
var (
	physHugePageSize  uintptr
	physHugePageShift uint
)

func mallocinit() {
	// TODO!!!!! everything up to "Create initial arena growth hints"

	// Create initial arena growth hints.
	if PtrSize == 8 {
		// Allocates off heap a 128-element singly-linked list of arenaHint pointers,
		// where the first hint holds an address of 0x000000c000000000 and the last
		// one an address of 0x00007fc000000000. All of the arenaHint addresses are
		// 1 TiB apart from one another.
		//
		// On a 64-bit machine, we pick the following hints
		// because:
		//
		// 1. Starting from the middle of the address space
		// makes it easier to grow out a contiguous range
		// without running in to some other mapping.
		//
		// 2. This makes Go heap addresses more easily
		// recognizable when debugging.
		//
		// 3. Stack scanning in gccgo is still conservative,
		// so it's important that addresses be distinguishable
		// from other data.
		//
		// Starting at 0x00c0 means that the valid memory addresses
		// will begin 0x00c0, 0x00c1, ...
		// In little-endian, that's c0 00, c1 00, ... None of those are valid
		// UTF-8 sequences, and they are otherwise as far away from
		// ff (likely a common byte) as possible. If that fails, we try other 0xXXc0
		// addresses. An earlier attempt to use 0x11f8 caused out of memory errors
		// on OS X during thread allocations.  0x00c0 causes conflicts with
		// AddressSanitizer which reserves all memory up to 0x0100.
		// These choices reduce the odds of a conservative garbage collector
		// not collecting memory because some non-pointer block of memory
		// had a bit pattern that matched a memory address.
		//
		// However, on arm64, we ignore all this advice above and slam the
		// allocation at 0x40 << 32 because when using 4k pages with 3-level
		// translation buffers, the user address space is limited to 39 bits
		// On ios/arm64, the address space is even smaller.
		//
		// On AIX, mmaps starts at 0x0A00000000000000 for 64-bit.
		// processes.
		//
		// Space mapped for user arenas comes immediately after the range
		// originally reserved for the regular heap when race mode is not
		// enabled because user arena chunks can never be used for regular heap
		// allocations and we want to avoid fragmenting the address space.
		//
		// In race mode we have no choice but to just use the same hints because
		// the race detector requires that the heap be mapped contiguously.
		//
		// Omitted code for architectures other than amd64. Also ommited
		// code for the race detector.
		for i := 0x7f; i >= 0; i-- { // generate 128 arena hints
			p := uintptr(i)<<40 | uintPtrMask&(0x00c0<<32)
			hintList := &mheap_.arenaHints

			// TODO!!!!! Switch to generating hints for user arenas if we've gone
			// through about half the hints. In race mode, take only about
			// a quarter; we don't have very much space to work with.
			// if (!raceenabled && i > 0x3f) || (raceenabled && i > 0x5f) {
			// 	hintList = &mheap_.userArena.arenaHints
			// }

			hint := (*arenaHint)(mheap_.arenaHintAlloc.alloc()) // fixalloc off heap alloc of an arenaHint
			hint.addr = p
			// in the end will be a singly-linked list of 128 arenaHint pointers,
			// where the first hint will hold an address of 0x000000c000000000
			// and the last one will hold the address 0x00007fc000000000.
			// All of the arenaHint addresses will be 1 TiB apart from one another.
			hint.next, *hintList = *hintList, hint
		}
	} else {
		// On 32-bit machine ...
	}
}

// sysAlloc allocates heap arena space for at least n bytes. The
// returned pointer is always heapArenaBytes-aligned and backed by
// h.arenas metadata. The returned size is always a multiple of
// heapArenaBytes. sysAlloc returns nil on failure.
// There is no corresponding free function.
//
// hintList is a list of hint addresses for where to allocate new
// heap arenas. It must be non-nil.
//
// register indicates whether the heap arena should be registered
// in allArenas.
//
// sysAlloc returns a memory region in the Reserved state. This region must
// be transitioned to Prepared and then Ready before use.
//
// h must be locked.
func (h *mheap) sysAlloc(n uintptr, hintList **arenaHint, register bool) (v unsafe.Pointer, size uintptr) {
	assertLockHeld(&h.lock) // no-op with lockrank off by default

	n = alignUp(n, heapArenaBytes) // align up to a multiple of 64MiB

	// only on 32-bit systems because heaps `arena` linearAlloc
	// isn't initialized on 64-bit systems, and it's `alloc()`
	// method will always return a `nil` pointer
	if hintList == &h.arenaHints {
		// 32-bit ONLY, no-op on 64-bit

		// First, try the arena pre-reservation.
		// Newly-used mappings are considered released.
		//
		// Only do this if we're using the regular heap arena hints.
		// This behavior is only for the heap.
		v = h.arena.alloc(n, heapArenaBytes, nil) // TODO!!!!! last parameter is &gcController.heapReleased instead of nil
		if v != nil {
			size = n
			goto mapped
		}
	}

	// Try to grow the heap at a hint address (64-bit systems).
	//
	// hintList is an off-heap 128-element singly-linked list of arenaHint pointers,
	// where the first hint holds an address of 0x000000c000000000 and the last
	// one an address of 0x00007fc000000000. All of the arenaHint addresses are
	// 1 TiB apart from one another.
	//
	// TODO!!!!! down seems always be set to false?
	for *hintList != nil {
		hint := *hintList
		p := hint.addr
		if hint.down {
			// reserve arena(s) space bellow the hint address
			p -= n
		}
		if p+n < p {
			// We can't use this, so don't ask.
			// Address would overflow.
			v = nil
		} else if arenaIndex(p+n-1) >= 1<<arenaBits { // 1<<arenaBits yield the maximum index (can also be calculated by dividing the 48 bit linear address space by 64MiB)
			// Outside addressable heap. Can't use.
			v = nil
		} else {
			// sysReserve transitions a memory region from None to Reserved. It reserves
			// address space in such a way that it would cause a fatal fault upon access
			// (either via permissions or not committing the memory). Such a reservation is
			// thus never backed by physical memory.
			//
			// If the pointer passed to it is non-nil, the caller wants the
			// reservation there, but sysReserve can still choose another
			// location if that one is unavailable.
			//
			// NOTE: sysReserve returns OS-aligned memory, but the heap allocator
			// may use larger alignment, so the caller must be careful to realign the
			// memory obtained by sysReserve.
			//
			// On Linux uses an anonymous private mmap with the PROT_NONE protocol flag.
			// PROT_NONE: The memory is reserved, but cannot be read, written, or executed.
			// If this flag is specified in a call to mmap, a virtual memory area will be set aside
			// for future use in the process, and mmap calls without the MAP_FIXED flag will not use it
			// for subsequent allocations. For anonymous mappings, the kernel will not reserve any physical
			// memory for the allocation at the time the mapping is created.
			//
			// TODO!!!!! Memory will be later commited upon allocating of golang 8192 byte pages?
			// TODO!!!!! Where is the memory commited?
			// when an application wishes to map multiple independent mappings as a virtually contiguous mapping.
			// This would be done by first mmapping a large enough chunk with PROT_NONE, and then performing other
			// mmap calls with the MAP_FIXED flag and an address set within the region of the PROT_NONE mapping
			// (the use of MAP_FIXED automatically unmaps part of mappings that are being "overridden").
			// https://stackoverflow.com/questions/12916603/what-s-the-purpose-of-mmap-memory-protection-prot-none
			v = sysReserve(unsafe.Pointer(p), n)
		}
		if p == uintptr(v) {
			// Success. Update the hint for next reservation.
			if !hint.down {
				// for hint.down already updated above
				p += n
			}
			hint.addr = p
			size = n
			break
		}
		// Failed to reserve virtual memory at specified address
		// (may be already in use). Discard this hint and try the next.
		//
		// TODO: This would be cleaner if sysReserve could be
		// told to only return the requested address. In
		// particular, this is already how Windows behaves, so
		// it would simplify things there.
		//
		// BUT on Linux if another mapping already exists at hint address
		// the kernel picks a new address that may or may not depend on the
		// hint. The address of the new mapping is returned as the result
		// of the mmap call.
		if v != nil {
			// mapping already exists at hint address, so Linux kernel
			// returned a new address that may or may not depend on the hint
			sysFreeOS(v, n) // calls munmap under the hood on Linux
		}
		*hintList = hint.next
		h.arenaHintAlloc.free(unsafe.Pointer(hint)) // hint unusable, so release chunk of memory back to fixalloc's linked list of `arenaHint`-sized chunks
	}

	if size == 0 {
		// all hints exhausted, but still aren't able to reserve memory

		// TODO!!!!! if raceenabled {
		// 	// The race detector assumes the heap lives in
		// 	// [0x00c000000000, 0x00e000000000), but we
		// 	// just ran out of hints in this region. Give
		// 	// a nice failure.
		// 	throw("too many address space collisions for -race mode")
		// }

		// All of the hints failed, so we'll take any
		// (sufficiently aligned) address the kernel will give
		// us.
		//
		// If the OS returns an address aligned to 64MiB, will
		// reserve one extra arena on top of the arena(s) required
		// to accomodate a size of `n`, else (if OS returns start address
		// not aligned to arena size) will reserve only the arena(s) required
		// to accomodate `n` and `size` will remain unchanged.
		v, size = sysReserveAligned(nil, n, heapArenaBytes)
		if v == nil {
			return nil, 0
		}

		// Create new hints for extending this region.
		// Since we exhausted all of the hints, they
		// were released back to fixalloc's linked list
		// of `arenaHint`-sized chunks, so probably
		// the upcoming 2 allocations will reuse those same
		// chunks.
		hint := (*arenaHint)(h.arenaHintAlloc.alloc())
		hint.addr, hint.down = uintptr(v), true
		// next time if growing up just right after the current
		// reservation fails, try to contiguosly grow down from
		// this region (is inserted after the "up" hint bellow)
		hint.next, mheap_.arenaHints = mheap_.arenaHints, hint // insert hint at the head of the linked list
		hint = (*arenaHint)(h.arenaHintAlloc.alloc())          // next time try to contiguosly grow up just right after the current reservation
		hint.addr = uintptr(v) + size
		hint.next, mheap_.arenaHints = mheap_.arenaHints, hint // insert hint at the head of the linked list
	}

	// Check for bad pointers or pointers we can't use
	// (in a separate to block to avoid overwriting `p`
	// from the outer block of the function body).
	{
		var bad string
		p := uintptr(v)
		if p+size < p {
			bad = "region exceeds uintptr range"
		} else if arenaIndex(p) >= 1<<arenaBits {
			bad = "base outside usable address space"
		} else if arenaIndex(p+size-1) >= 1<<arenaBits {
			bad = "end outside usable address space"
		}
		if bad != "" {
			// This should be impossible on most architectures,
			// but it would be really confusing to debug.
			print("runtime: memory allocated by OS [", hex(p), ", ", hex(p+size), ") not in usable address space: ", bad, "\n")
			throw("memory reservation exceeds address space limit")
		}
	}

	if uintptr(v)&(heapArenaBytes-1) != 0 {
		// should be impossible
		throw("misrounded allocation is sysAlloc")
	}

mapped:
	// Create arena metadata for arena(s) allocated.

	// On 64-bit Linux arenaIndex is calculated by dividing the offset address
	// translated to a linear address into 64MiB (size of a single arena).
	// So basically the global arena index is identical to the level 2 arena index
	// (level 1 only holds one entry at index 0, this entry is a pointer to an array
	// of pointers to `heapArena` structs. This level 2 array is of size "the 48 bit
	// linear address space divided by 64MiB", which is 4194304).
	for ri := arenaIndex(uintptr(v)); ri <= arenaIndex(uintptr(v)+size-1); ri++ {
		l2 := h.arenas[ri.l1()]
		if l2 == nil {
			// On all 64-bit systems except Windows is nil only on first call to this method.
			//
			// Allocate an L2 arena map (uses an anonymous private mmap on Linux).
			//
			// Use sysAllocOS instead of sysAlloc or persistentalloc because there's no
			// statistic we can comfortably account for this space in. With this structure,
			// we rely on demand paging to avoid large overheads, but tracking which memory
			// is paged in is too expensive. Trying to account for the whole region means
			// that it will appear like an enormous memory overhead in statistics, even though
			// it is not (on all 64-bit systems except Windows requires 32MiB of virtual address
			// space to fit the single level 2 array of 4194304 pointers to `heapArena` structs).
			l2 = (*[1 << arenaL2Bits]*heapArena)(sysAllocOS(unsafe.Sizeof(*l2)))
			if l2 == nil {
				throw("out of memory allocating heap arena map")
			}
			atomic.StorepNoWB(unsafe.Pointer(&h.arenas[ri.l1()]), unsafe.Pointer(l2))
		}

		if l2[ri.l2()] != nil {
			throw("arena already initialized")
		}

		var r *heapArena
		r = (*heapArena)(h.heapArenaAlloc.alloc(unsafe.Sizeof(*r), PtrSize, nil)) // on 64-bit systems always returns nil, last argument is `&memstats.gcMiscSys``
		if r == nil {
			// always nil on 64-bit systems

			// request a pointer-size-aligned `heapArena`-sized zeroed chunk in the Ready state
			// from persistent alloc, which may in turn go to the OS to mmap another 256KB
			// chunk, if the current one cannot accomodate another `heapArena`.
			// persistentalloc runs on system stack because stack growth can (re)invoke it.
			r = (*heapArena)(persistentalloc(unsafe.Sizeof(*r), PtrSize, nil)) // last argument is `&memstats.gcMiscSys`
			if r == nil {
				throw("out of memory allocating heap arena metadata")
			}
		}

		// Register the arena in allArenas if requested
		// (NOT requested only when allocting "user" arenas).
		if register {
			if len(h.allArenas) == cap(h.allArenas) {
				// allocate new backing array twice the size of the current one
				// via persistentalloc, copy content from current to new backing
				// array and adjust mheap_.allArenas slice header accordingly
				// (mheap_.lock is already held).
				size := 2 * uintptr(cap(h.allArenas)) * PtrSize // arenaIdx is an alias for uinptr
				if size == 0 {
					// on overflow allocate an OS page (4096 bytes on Linux)
					size = physPageSize
				}
				// request a pointer-size-aligned zeroed chunk of memory in the Ready state
				// from persistent alloc, which may in turn go to the OS to mmap another 256KB
				// chunk if the current one cannot accomodate the new backing array. If the new
				// backing array exceeds 64KB, mmaps directly from the OS bypassing `persistentChunks` list.
				// persistentalloc runs on system stack because stack growth can (re)invoke it.
				newArray := (*notInHeap)(persistentalloc(size, PtrSize, nil)) // last argument is `&memstats.gcMiscSys`
				if newArray == nil {
					throw("out of memory allocating allArenas")
				}
				oldSlice := h.allArenas // copy slice header, so that readers can access the slice concurrently without acquiring mheap_.lock
				*(*notInHeapSlice)(unsafe.Pointer(&h.allArenas)) = notInHeapSlice{newArray, len(h.allArenas), int(size / PtrSize)}
				copy(h.allArenas, oldSlice)
				// Do not free the old backing array because
				// there may be concurrent readers. Since we
				// double the array each time, this can lead
				// to at most 2x waste.
			}
			h.allArenas = h.allArenas[:len(h.allArenas)+1] // increment slice's `len` field
			h.allArenas[len(h.allArenas)-1] = ri
		}

		// Store atomically in level 2 array just in case an object
		// from the new heap arena becomes visible before the heap lock
		// is released (which shouldn't happen, but there's little downside
		// to this).
		atomic.StorepNoWB(unsafe.Pointer(&l2[ri.l2()]), unsafe.Pointer(r))
	}

	// Tell the race detector about the new heap memory.
	// TODO!!!!! if raceenabled {
	// 	racemapshadow(v, size)
	// }

	// Return pointer to start of reserved arena space
	// and the size of the reserved space
	return
}

// sysReserveAligned is like sysReserve, but the returned pointer is
// aligned to align bytes. It may reserve either n or n+align bytes,
// so it returns the size that was reserved.
func sysReserveAligned(v unsafe.Pointer, size, align uintptr) (unsafe.Pointer, uintptr) {
	// Since the alignment is rather large in uses of this
	// function, we're not likely to get it by chance, so we ask
	// for a larger region and remove the parts we don't need.

	// sysReserve transitions a memory region from None to Reserved. It reserves
	// address space in such a way that it would cause a fatal fault upon access
	// (either via permissions or not committing the memory). Such a reservation is
	// thus never backed by physical memory.
	//
	// If the pointer passed to it is non-nil, the caller wants the
	// reservation there, but sysReserve can still choose another
	// location if that one is unavailable.
	//
	// NOTE: sysReserve returns OS-aligned memory, but the heap allocator
	// may use larger alignment, so the caller must be careful to realign the
	// memory obtained by sysReserve.
	//
	// On Linux uses an anonymous private mmap with the PROT_NONE protocol flag.
	// PROT_NONE: The memory is reserved, but cannot be read, written, or executed.
	// If this flag is specified in a call to mmap, a virtual memory area will be set aside
	// for future use in the process, and mmap calls without the MAP_FIXED flag will not use it
	// for subsequent allocations. For anonymous mappings, the kernel will not reserve any physical
	// memory for the allocation at the time the mapping is created.
	//
	// TODO!!!!! Memory will be later commited upon allocating of golang 8192 byte pages?
	// TODO!!!!! Where is the memory commited?
	// when an application wishes to map multiple independent mappings as a virtually contiguous mapping.
	// This would be done by first mmapping a large enough chunk with PROT_NONE, and then performing other
	// mmap calls with the MAP_FIXED flag and an address set within the region of the PROT_NONE mapping
	// (the use of MAP_FIXED automatically unmaps part of mappings that are being "overridden").
	// https://stackoverflow.com/questions/12916603/what-s-the-purpose-of-mmap-memory-protection-prot-none

	// when called by `mheap_.sysAlloc` `size+align` will be equal to
	// `size` + `one extra arena (64MiB)`
	p := uintptr(sysReserve(v, size+align))
	switch {
	case p == 0:
		return nil, 0
	case p&(align-1) == 0:
		// The system returned an already sufficiently aligned address.

		// when called by `_mheap.sysAlloc` reserves an extra arena,
		// because `align` will be equal to the size of a 64MiB arena
		return unsafe.Pointer(p), size + align
	default:
		// The system returned an unaligned address,
		// trim off the unaligned parts.

		// // when called by `mheap_.sysAlloc` will align start
		// to the nearest up address, which is a multiple of arena
		// size (64MiB), memory bellow that will be freed.
		pAligned := alignUp(p, align)
		sysFreeOS(unsafe.Pointer(p), pAligned-p)
		end := pAligned + size // cap to requested size

		// when called by `mheap_.sysAlloc` the end address,
		// will be aligned down towards the nearest address,
		// which is a multiple of arena size (64MiB), memory
		// above it will be freed
		endLen := (p + size + align) - end
		if endLen > 0 {
			sysFreeOS(unsafe.Pointer(end), endLen)
		}
		// when called by `mheap_.sysAlloc` `pAligned` will be an address,
		// which is a multiple of arena size (64MiB), `size` will be also
		// a multiple of 64MiB
		return unsafe.Pointer(pAligned), size
	}
}

type persistentAlloc struct {
	base *notInHeap // TODO!!!!!
	off  uintptr    // TODO!!!!!
}

// TODO!!!!!
var globalAlloc struct {
	mutex
	persistentAlloc
}

// persistentChunkSize is the number of bytes we allocate when we grow
// a persistentAlloc.
const persistentChunkSize = 256 << 10 // 256KB

// persistentChunks is a list of all the persistent chunks we have
// allocated. The list is maintained through the first word in the
// persistent chunk. This is updated atomically.
// TODO!!!!! add more details!
var persistentChunks *notInHeap

// Wrapper around sysAlloc that can allocate small chunks.
// There is no associated free operation.
// Intended for things like function/type/debug-related persistent data.
// If align is 0, uses default align (currently 8).
// The returned memory will be zeroed.
// sysStat must be non-nil.
//
// Consider marking persistentalloc'd types not in heap by embedding
// runtime/internal/sys.NotInHeap.
func persistentalloc(size, align uintptr, sysStat *sysMemStat) unsafe.Pointer {
	var p *notInHeap
	systemstack(func() {
		p = persistentalloc1(size, align, sysStat)
	})
	return unsafe.Pointer(p)
}

// Must run on system stack because stack growth can (re)invoke it.
// See issue 9174.
//
//go:systemstack
func persistentalloc1(size, align uintptr, sysStat *sysMemStat) *notInHeap {
	const (
		maxBlock = 64 << 10 // VM reservation granularity is 64K on windows
	)

	if size == 0 {
		throw("persistentalloc: size == 0")
	}
	if align != 0 {
		if align&(align-1) != 0 {
			throw("persistentalloc: align is not a power of 2")
		}
		if align > _PageSize { // golang page size (8192 bytes)
			throw("persistentalloc: align is too large")
		}
	} else {
		align = 8 // align by word size by default
	}

	// allocate blocks of memory greater than 64KB directly
	// from the OS and do not store them in the globally accessible
	// `persistentChunks` linked list.
	if size >= maxBlock {
		// anonymous private read and write mmap under the hood
		// (non-prefaulted, physicall memory allocation
		// only occurs when the virtual memory is written to)
		return (*notInHeap)(sysAlloc(size, sysStat))
	}

	// TODO!!!!! retrieves the current M running the current G
	// and increments M's `.locks` attribute,
	// which is the count of acquired or pending
	// to be aqcuired futex-based mutexes
	// (&globalAlloc.mutex in this particular case)
	mp := acquirem()
	var persistent *persistentAlloc

	// if we're executing user's go code (M executing in a P's context),
	// use per-P `persistentAlloc` to avoid lock contention
	// TODO!!!!! add more details abot the per-P `persistentAlloc`
	if mp != nil && mp.p != 0 {
		persistent = &mp.p.ptr().palloc
	} else { // otherwise the global persistentAlloc which is protected by a futex
		lock(&globalAlloc.mutex)
		persistent = &globalAlloc.persistentAlloc
	}

	persistent.off = alignUp(persistent.off, align) // align pointer into current active 256KB chunk as requested or to machine word by default

	// if no 256KB chunk yet allocated for the `persistentAlloc` or current
	// active chunk has to little space left to accomodate the requested allocation,
	// in this case, the the left over space will be waisted, because allocation
	// accross chunks isn't practical
	if persistent.off+size > persistentChunkSize || persistent.base == nil {
		// anonymous private read and write mmap under the hood
		// (non-prefaulted, physicall memory allocation
		// only occurs when the virtual memory is written to)
		persistent.base = (*notInHeap)(sysAlloc(persistentChunkSize, &memstats.other_sys))
		if persistent.base == nil {
			if persistent == &globalAlloc.persistentAlloc {
				unlock(&globalAlloc.mutex)
			}
			throw("runtime: cannot allocate memory")
		}

		// Add the new chunk to the persistentChunks list.
		for {
			chunks := uintptr(unsafe.Pointer(persistentChunks))

			// 1. (*uintptr)(unsafe.Pointer(persistent.base)) - acquires a pointer to the first word of the allocated 256KB persistent chunk
			// 2. *(*uintptr)(unsafe.Pointer(persistent.base)) = chunks - writes pointer to the head persistent chunk residing
			//                                                            in the globally accessible singly-linked list of all the persistent
			//                                                            chunks we have into new chunk's first word.
			*(*uintptr)(unsafe.Pointer(persistent.base)) = chunks

			// atomically re-write head of `persistentChunks` to be the newly allocated persistent chunk
			if atomic.Casuintptr((*uintptr)(unsafe.Pointer(&persistentChunks)), chunks, uintptr(unsafe.Pointer(persistent.base))) {
				break
			}
		}
		persistent.off = alignUp(PtrSize, align) // skip the first word of the newly allocated chunk which points to the previous head of `persistentChunks`
	}

	p := persistent.base.add(persistent.off) // get pointer into newly allocated persistent chunk just after the word-sized pointer to the previous head of `persistentChunks`
	persistent.off += size                   // slice off a `size` sized chunk of memory from the newly allocated 256KB persistent chunk for the caller
	if persistent == &globalAlloc.persistentAlloc {
		unlock(&globalAlloc.mutex)
	}

	if sysStat != &memstats.other_sys {
		sysStat.add(int64(size))
		memstats.other_sys.add(-int64(size))
	}

	return p // return pointer into newly allocated 256KB persistent chunk just after the linked-list word-sized pointer
}

// notInHeap is off-heap memory allocated by a lower-level allocator
// like sysAlloc or persistentAlloc.
//
// In general, it's better to use real types which embed
// runtime/internal/sys.NotInHeap, but this serves as a generic type
// for situations where that isn't possible (like in the allocators).
//
// TODO: Use this as the return type of sysAlloc, persistentAlloc, etc?
type notInHeap struct{ _ NotInHeap }

func (p *notInHeap) add(bytes uintptr) *notInHeap {
	return (*notInHeap)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + bytes))
}

// linearAlloc is a simple linear allocator that pre-reserves a region
// of memory and then optionally maps that region into the Ready state
// as needed.
//
// The caller is responsible for locking.
type linearAlloc struct {
	next   uintptr // next free byte
	mapped uintptr // one byte past end of mapped space
	end    uintptr // end of reserved space

	mapMemory bool // transition memory from Reserved to Ready if true
}

func (l *linearAlloc) init(base, size uintptr, mapMemory bool) {
	if base+size < base {
		// Chop off the last byte. The runtime isn't prepared
		// to deal with situations where the bounds could overflow.
		// Leave that memory reserved, though, so we don't map it
		// later.
		size -= 1
	}

	l.next, l.mapped = base, base
	l.end = base + size
	l.mapMemory = mapMemory
}

func (l *linearAlloc) alloc(size, align uintptr, sysStat *sysMemStat) unsafe.Pointer {
	p := alignUp(l.next, align)
	if p+size > l.end {
		return nil
	}

	// Used only on 32-bit systems, on 64-bit systems always returns nil

	return unsafe.Pointer(p)
}
