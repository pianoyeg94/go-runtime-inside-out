package heap

import "unsafe"

const (
	// number of pages for which a single bit-map chunk is responsible (512)
	pallocChunkPages = 1 << logPallocChunkPages

	// how much memory a single bit-map chunk is responsible for (4MB)
	pallocChunkBytes = pallocChunkPages * pageSize

	// log2 number of pages for which a single bit-map chunk is responsible
	logPallocChunkPages = 9

	// log2 of how much memory a single bit-map chunk is responsible for (22)
	logPallocChunkBytes = logPallocChunkPages + pageShift

	// The number of radix bits for each level.
	//
	// The value of 3 is chosen such that the block of summaries we need to scan at
	// each level fits in 64 bytes (2^3 summaries * 8 bytes per summary), which is
	// close to the L1 cache line width on many systems. Also, a value of 3 fits 4 tree
	// levels perfectly into the 21-bit pallocBits summary field at the root level.
	//
	// The following equation explains how each of the constants relate:
	// summaryL0Bits + (summaryLevels-1)*summaryLevelBits + logPallocChunkBytes = heapAddrBits
	//
	// summaryLevels is an architecture-dependent value defined in mpagealloc_*.go.
	summaryLevelBits = 3

	// 22 bits out of the 48 bit address are used to address individual bytes in a memory chunk
	// represented by a single bitmap chunk. Each level of the radix tree also needs additional
	// 3 bits to accomodate 8 times more memory as the previous more granular level.
	// This leaves us with summaryL0Bits equal to 14.
	summaryL0Bits = heapAddrBits - logPallocChunkBytes - (summaryLevels-1)*summaryLevelBits

	// pallocChunksL2Bits is the number of bits of the chunk index number
	// covered by the second level of the chunks map.
	//
	// See (*pageAlloc).chunks for more details. Update the documentation
	// there should this change.
	//
	// 48-bit address - (minus)
	// number of bits to index a single byte within a bitmap chunk - (minus)
	// number of bits to index into the first level of 'chunks'
	pallocChunksL2Bits = heapAddrBits - logPallocChunkBytes - pallocChunksL1Bits

	// 35 bits to shift to the right to get the 13 high-order bits of a 48-bit virtual address,
	// which are used to index the first level of 'chunks'
	pallocChunksL1Shift = pallocChunksL2Bits
)

// maxSearchAddr returns the maximum searchAddr value, which indicates
// that the heap has no free space.
//
// This function exists just to make it clear that this is the maximum address
// for the page allocator's search space. See maxOffAddr for details.
//
// It's a function (rather than a variable) because it needs to be
// usable before package runtime's dynamic initialization is complete.
// See #51913 for details.
func maxSearchAddr() offAddr { return maxOffAddr }

// Global chunk index.
//
// Represents an index into the leaf level of the radix tree as well as the index
// into the 2-level chunks array.
// Similar to arenaIndex, except instead of arenas, it divides the address
// space into chunks.
type chunkIdx uint

func chunkIndex(p uintptr) chunkIdx {
	return chunkIdx((p - arenaBaseOffset) / pallocChunkBytes)
}

// chunkBase returns the base address of the palloc chunk at index ci.
func chunkBase(ci chunkIdx) uintptr {
	//  - `uintptr(ci)*pallocChunkBytes` gets the addres in a linear address space
	//  - + arenaBaseOffset - converts the linear address to an address in an offset address space as on x86-64
	return uintptr(ci)*pallocChunkBytes + arenaBaseOffset
}

// chunkPageIndex computes the index of the page that contains p,
// relative to the chunk which contains p.
func chunkPageIndex(p uintptr) uint {
	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	// - `p % pallocChunkBytes` gets the byte number relative to the memory chunk represented by bitmap chunk which contains p.
	// - `p % pallocChunkBytes / pageSize` gets the page number relative to the bitmap chunk (page 0 to page 511) within which p is contained
	return uint(p % pallocChunkBytes / pageSize)
}

// l1 returns the index into the first level of (*pageAlloc).chunks.
// For details check out the 'chunks' field of pageAlloc.
func (i chunkIdx) l1() uint {
	return uint(i) >> pallocChunksL1Shift
}

// l2 returns the index into the second level of (*pageAlloc).chunks.
// For details check out the 'chunks' field of pageAlloc.
func (i chunkIdx) l2() uint {
	return uint(i) & (1<<pallocChunksL2Bits - 1)
}

// offAddrToLevelIndex converts an address in the offset address space
// to the index into summary[level] containing addr.
func offAddrToLevelIndex(level int, addr offAddr) int {
	// 1. addr.addr() - arenaBaseOffset - acquire linear address
	// 2. >> levelShift[level] - since we got a linear address, we can aqcuire part of the address,
	//                           which correspond to an index in the specified level of the radix tree
	return int((addr.addr() - arenaBaseOffset) >> levelShift[level])
}

// levelIndexToOffAddr converts an index into summary[level] into
// the corresponding address in the offset address space.
func levelIndexToOffAddr(level, idx int) offAddr {
	// 1. (uintptr(idx) << levelShift[level]) - convert idx to linear address
	// 2. + arenaBaseOffset - convert linear address to an address in the offset address space
	return offAddr{(uintptr(idx) << levelShift[level]) + arenaBaseOffset}
}

func addrsToSummaryRange(level int, base, limit uintptr) (lo int, hi int) {
	lo = int((base - arenaBaseOffset) >> levelShift[level])
	hi = int((limit-arenaBaseOffset)>>levelShift[level]) + 1
	return
}

func blockAlignSummaryRange(level int, lo, hi int) (int, int) {
	e := uintptr(1) << levelBits[level]
	return int(alignDown(uintptr(lo), e)), int(alignUp(uintptr(hi), e))
}

type pageAlloc struct {
	// Radix tree of summaries.
	//
	// There are 5 summary levels, where each level devides the process 256TiB (TB) virtual address space
	// into more granular address space chunks:
	// 		level 0 - devides the virtual address space into 16GiB(GB) chunks, for which a single pallocSum
	//                at this level is resposible for, thus this level needs 16384 pallocSum entries to repesent
	//                the 256TiB (TB) virtual address space.
	//                To represent 0-16383 entries level 0 needs 14 bits.
	//
	//      level 1 - devides the virtual address space into 2GiB(GB) chunks, for which a single pallocSum
	//                at this level is resposible for, thus this level needs 131072 pallocSum entries to repesent
	//                the 256TiB (TB) virtual address space.
	//                To represent 0-131071 entries level 1 needs 17 bits.
	//
	//      level 2 - devides the virtual address space into 256MiB(MB) chunks, for which a single pallocSum
	//                at this level is resposible for, thus this level needs 1048576 pallocSum entries to repesent
	//                the 256TiB (TB) virtual address space.
	//                To represent 0-1048575 entries level 2 needs 20 bits.
	//
	//      level 3 - devides the virtual address space into 32MiB(MB) chunks, for which a single pallocSum
	//                at this level is resposible for, thus this level needs 8388608 pallocSum entries to repesent
	//                the 256TiB (TB) virtual address space.
	//                To represent 0-8388607 entries level 3 needs 20 bits.
	//
	//      level 4 - devides the virtual address space into 4MiB(MB) chunks, for which a single pallocSum
	//                at this level is resposible for, thus this level needs 67108864 pallocSum entries to repesent
	//                the 256TiB (TB) virtual address space.
	//                To represent 0-67108863 entries level 4 needs 23 bits.
	//
	// Each slice's cap represents the whole memory reservation.
	// Each slice's len reflects the allocator's maximum known
	// mapped heap address for that level.
	//
	// The backing store of each summary level is reserved in init
	// and may or may not be committed in grow (small address spaces
	// may commit all the memory in init).
	//
	// The purpose of keeping len <= cap is to enforce bounds checks
	// on the top end of the slice so that instead of an unknown
	// runtime segmentation fault, we get a much friendlier out-of-bounds
	// error.
	//
	// To iterate over a summary level, use inUse to determine which ranges
	// are currently available. Otherwise one might try to access
	// memory which is only Reserved which may result in a hard fault.
	//
	// We may still get segmentation faults < len since some of that
	// memory may not be committed yet.
	summary [summaryLevels][]pallocSum

	// chunks is a slice of bitmap chunks.
	//
	// Because each bitmap chunk is responsible for 4MiB (MB) of virtual address space,
	// we require 67108864 such chunks to represent the whole 256TiB address space.
	// Because each chunk occupies 64 bytes, tho total virtual memory needed to accomodate
	// 'chunks' is 512MiB.
	//
	// The total size of chunks is quite large on most 64-bit platforms
	// (O(GiB) or more) if flattened, so rather than making one large mapping
	// (which has problems on some platforms, even when PROT_NONE) we use a
	// two-level sparse array approach similar to the arena index in mheap.
	//
	// Basically 'chunks' is a 8192 by 8192 matrix and for two-level indexing we need
	// 13 bits per level.
	//
	// Below is a table describing the configuration for chunks for various
	// heapAddrBits supported by the runtime.
	//
	// heapAddrBits | L1 Bits | L2 Bits | L2 Entry Size
	// ------------------------------------------------
	// 32           | 0       | 10      | 128 KiB
	// 33 (iOS)     | 0       | 11      | 256 KiB
	// 48           | 13      | 13      | 1 MiB (size of a single L2 array)
	//
	// There's no reason to use the L1 part of chunks on 32-bit, the
	// address space is small so the L2 is small. For platforms with a
	// 48-bit address space, we pick the L1 such that the L2 is 1 MiB
	// in size, which is a good balance between low granularity without
	// making the impact on BSS too high (note the L1 is stored directly
	// in pageAlloc).
	//
	// To convert a 48 bit virtual offset address into an index into flattened out 'chunks',
	// it must first be coverted to a linear address and then divided my 4MiB,
	// which is the memory chunk for which a single pallocData bitmap is responsible for.
	//
	// The index into the flattened out 'chunks' occupies at most 26 low-order bits of the uint,
	// which is used to store such an index.
	//
	// So to further convert the linear index into a 2-level index, the high 13 bits out of 26
	// are used to index into the first level and the remaining 13 low-order bits to index
	// a single bitmap chunk.
	//
	// The lower 22 bits of the 48-bit linear address could be used in theory to index
	// individual bytes within some page that is part of a single bitmap chunk representation.
	// Out of those 22 bits, 9 bits could be used to index a particular page within a single
	// bitmap chunk and the left over 13 bits to index a single byte of virtual memory.
	chunks [1 << pallocChunksL1Bits]*[1 << pallocChunksL2Bits]pallocData

	// The address to start an allocation search with. It must never
	// point to any memory that is not contained in inUse, i.e.
	// inUse.contains(searchAddr.addr()) must always be true. The one
	// exception to this rule is that it may take on the value of
	// maxOffAddr to indicate that the heap is exhausted.
	//
	// We guarantee that all valid heap addresses below this value
	// are allocated and not worth searching.
	searchAddr offAddr

	// start and end represent the chunk indices
	// which pageAlloc knows about. It assumes
	// chunks in the range [start, end) are
	// currently ready to use.
	start, end chunkIdx

	// inUse is a slice of ranges of address space which are
	// known by the page allocator to be currently in-use (passed
	// to grow).
	//
	// This field is currently unused on 32-bit architectures but
	// is harmless to track. We care much more about having a
	// contiguous heap in these cases and take additional measures
	// to ensure that, so in nearly all cases this should have just
	// 1 element.
	//
	// All access is protected by the mheapLock.
	inUse addrRanges

	// mheap_.lock. This level of indirection makes it possible
	// to test pageAlloc indepedently of the runtime allocator.
	mheapLock *mutex

	// sysStat is the runtime memstat to update when new system
	// memory is committed by the pageAlloc for allocation metadata.
	sysStat *sysMemStat

	// summaryMappedReady is the number of bytes mapped in the Ready state
	// in the summary structure. Used only for testing currently.
	//
	// Protected by mheapLock.
	summaryMappedReady uintptr

	// Whether or not this struct is being used in tests.
	test bool
}

func (p *pageAlloc) init(mheapLock *mutex, sysStat *sysMemStat) {
	if levelLogPages[0] > logMaxPackedValue {
		// We can't represent 1<<levelLogPages[0] pages, the maximum number
		// of pages we need to represent at the root level, in a summary, which
		// is a big problem. Throw.
		print("runtime: root level max pages = ", 1<<levelLogPages[0], "\n")
		print("runtime: summary max pages = ", maxPackedValue, "\n")
		throw("root level max pages doesn't fit in summary")
	}
	p.sysStat = sysStat

	// Initialize p.inUse.
	p.inUse.init(sysStat)

	// System-dependent initialization.
	p.sysInit()

	// Start with the searchAddr in a state indicating there's no free memory.
	p.searchAddr = maxSearchAddr()

	// Set the mheapLock.
	p.mheapLock = mheapLock
}

// chunkOf returns the chunk at the given chunk index.
//
// The chunk index must be valid or this method may throw.
func (p *pageAlloc) chunkOf(ci chunkIdx) *pallocData {
	return &p.chunks[ci.l1()][ci.l2()]
}

// grow sets up the metadata for the address range [base, base+size).
// It may allocate metadata, in which case *p.sysStat will be updated.
//
// p.mheapLock must be held.
func (p *pageAlloc) grow(base, size uintptr) {
	assertLockHeld(p.mheapLock)

	// Round up to chunks, since we can't deal with increments smaller
	// than chunks. Also, sysGrow expects aligned values.
	limit := alignUp(base+size, pallocChunkBytes)
	base = alignDown(base, pallocChunkBytes)

	// Grow the summary levels in a system-dependent manner.
	// We just update a bunch of additional metadata here.
	p.sysGrow(base, limit)

	// TODO:
	// Update p.start and p.end.
	// If no growth happened yet, start == 0. This is generally
	// safe since the zero page is unmapped.
	firstGrowth := p.start == 0
	start, end := chunkIndex(base), chunkIndex(limit)
	if firstGrowth || start < p.start {
		p.start = start
	}
	if end > p.end {
		p.end = end
	}

	// Note that [base, limit) will never overlap with any existing
	// range inUse because grow only ever adds never-used memory
	// regions to the page allocator.
	p.inUse.add(makeAddrRange(base, limit))

	// A grow operation is a lot like a free operation, so if our
	// chunk ends up below p.searchAddr, update p.searchAddr to the
	// new address, just like in free.
	if b := (offAddr{base}); b.lessThan(p.searchAddr) {
		p.searchAddr = b
	}

	// Add entries into chunks, which is sparse, if needed. Then,
	// initialize the bitmap.
	//
	// Newly-grown memory is always considered scavenged.
	// Set all the bits in the scavenged bitmaps high.
	for c := chunkIndex(base); c < chunkIndex(limit); c++ {
		if p.chunks[c.l1()] == nil {
			// Create the necessary l2 entry.
			r := sysAlloc(unsafe.Sizeof(*p.chunks[0]), p.sysStat)
			if r == nil {
				throw("pageAlloc: out of memory")
			}

			// Store the new chunk block but avoid a write barrier.
			// grow is used in call chains that disallow write barriers.
			*(*uintptr)(unsafe.Pointer(&p.chunks[c.l1()])) = uintptr(r)
		}

		// TODO: p.chunkOf(c).scavenged.setRange(0, pallocChunkPages)
	}

	// Update summaries accordingly. The grow acts like a free, so
	// we need to ensure this newly-free memory is visible in the
	// summaries
	// Update summaries in radix tree according to changes made to the free/in-use
	// pages in the bitmap.
	// The grow operation works with a new contiguos chunk of memory and
	// since it's new memory it's not allocated as well (true and false accordingly).
	p.update(base, size/pageSize, true, false)
}

func (p *pageAlloc) update(base, npages uintptr, contig, alloc bool) {
	assertLockHeld(p.mheapLock)

	// base, limit, start, and end are inclusive.
	limit := base + npages*pageSize - 1
	sc, ec := chunkIndex(base), chunkIndex(limit)

	// Handle updating the lowest level first.
	if sc == ec {
		// Fast path: the allocation doesn't span more than one chunk,
		// so update this one and if the summary didn't change, return.
		x := p.summary[len(p.summary)-1][sc]
		y := p.chunkOf(sc).summarize()
		if x == y {
			return
		}
		p.summary[len(p.summary)-1][sc] = y
	} else if contig {
		// Slow contiguous path: the allocation spans more than one chunk
		// and at least one summary is guaranteed to change.
		summary := p.summary[len(p.summary)-1]

		// Update the summary for chunk sc.
		summary[sc] = p.chunkOf(sc).summarize()

		// Update the summaries for chunks in between, which are
		// either totally allocated or freed.
		whole := p.summary[len(p.summary)-1][sc+1 : ec]
		if alloc {
			// Should optimize into a memclr.
			for i := range whole {
				whole[i] = 0
			}
		} else {
			for i := range whole {
				whole[i] = freeChunkSum
			}
		}

		// Update the summary for chunk ec.
		summary[ec] = p.chunkOf(ec).summarize()
	} else {
		// Slow general path: the allocation spans more than one chunk
		// and at least one summary is guaranteed to change.
		//
		// We can't assume a contiguous allocation happened, so walk over
		// every chunk in the range and manually recompute the summary.
		summary := p.summary[len(p.summary)-1]
		for c := sc; c <= ec; c++ {
			summary[c] = p.chunkOf(c).summarize()
		}
	}

	// Walk up the radix tree and update the summaries appropriately.
	changed := true // break if no summaries have changed at a particular level, no point in modifying the higher levels
	for l := len(p.summary) - 2; l >= 0 && changed; l-- {
		// Update summaries at level l from summaries at level l+1.
		changed = false

		// "Constants" for the previous level which we
		// need to compute the summary from that level.

		// log number of summaries from the previous level,
		// is used to caculate the child summary range by
		// calculating power of 2 of index i and i+1 at this level.
		logEntriesPerBlock := levelBits[l+1]

		// log number of pages a single summary from the previous level
		// is responsible for, used to determine max summary values,
		// when merging summaries in mergeSummaries().
		logMaxPages := levelLogPages[l+1]

		lo, hi := addrsToSummaryRange(l, base, limit+1) // summary range to update at the current level

		// Iterate over each block, updating the corresponding summary in the less-granular level from summaries
		// in the more granular one.
		for i := lo; i < hi; i++ {
			children := p.summary[l+1][i<<logEntriesPerBlock : (i+1)<<logEntriesPerBlock] // get child summaries
			sum := mergeSummaries(children, logMaxPages)                                  // merge child summaries for the current summary
			old := p.summary[l][i]
			if old != sum {
				changed = true
				p.summary[l][i] = sum
			}
		}
	}

}

// TODO!!!!! scavenged
//
// allocRange marks the range of memory [base, base+npages*pageSize) as
// allocated. It also updates the summaries to reflect the newly-updated
// bitmap.
//
// Returns the amount of scavenged memory in bytes present in the
// allocated range.
//
// p.mheapLock must be held.
func (p *pageAlloc) allocRange(base, npages uintptr) uintptr {
	assertLockHeld(p.mheapLock)

	limit := base + npages*pageSize - 1           // get end address of allocation
	sc, ec := chunkIndex(base), chunkIndex(limit) // bitmap global indexes
	// indexes of the pages that contain base and end,
	// relative to the chunks at sc and ec bitmap global
	// indexes
	si, ei := chunkPageIndex(base), chunkPageIndex(limit)

	// scav := uint(0) // TODO!!!!!
	if sc == ec {
		// The range doesn't cross any chunk boundaries.
		// Allocation doesn't span more than a single bitmap chunk.
		chunk := p.chunkOf(sc) // get chunk within which base and limit addresses are contained
		// scav += chunk.scavenged.popcntRange(si, ei+1-si) // TODO!!!!!
		chunk.allocRange(si, ei+1-si) // set bits according to base and end in the bitmap chunk, meaning that the pages are allocated
	} else {
		// The range crosses at least one chunk boundary.
		chunk := p.chunkOf(sc) // get bitmap chunk within which the base address is contained
		// scav += chunk.scavenged.popcntRange(si, pallocChunkPages-si) // TODO!!!!!

		// allocate all pages starting from base address
		// up to the address that corresponds to the end
		// address of the starting bitmap chunk
		chunk.allocRange(si, pallocChunkPages-si)
		for c := sc + 1; c < ec; c++ {
			// allocate whole bitmap chunks in between and not including chunks
			// corresponding to the base and end addresses
			chunk := p.chunkOf(c)
			// scav += chunk.scavenged.popcntRange(0, pallocChunkPages) // TODO!!!!!
			chunk.allocAll() // set's all bits in bitmap chunk to 1 (allocated)
		}
		chunk = p.chunkOf(ec) // get bitmap chunk within which the end address is contained
		// scav += chunk.scavenged.popcntRange(0, ei+1) // TODO!!!!!

		// allocate all pages starting from start of last
		// bitmap chunk up to the address that corresponds
		// to the end address
		chunk.allocRange(0, ei)
	}

	// Updates last level palloc radix tree summaries corresponding to
	// the bitmap chunks, these summaries will tell the user that these
	// bitmap chunks are allocated. After that updates all the parent
	// summaries above (up to level 0 if neccessary).
	p.update(base, npages, true, true)
	return 0 // return uintptr(scav) * pageSize // TODO!!!!!
}

// findMappedAddr returns the smallest mapped offAddr that is
// >= addr. That is, if addr refers to mapped memory, then it is
// returned. If addr is higher than any mapped region, then
// it returns maxOffAddr.
//
// p.mheapLock must be held.
func (p *pageAlloc) findMappedAddr(addr offAddr) offAddr {
	assertLockHeld(p.mheapLock)

	// If we're not in a test, validate first by checking mheap_.arenas.
	// This is a fast path which is only safe to use outside of testing.
	//
	// 48-bit address converted to an address in linear address space divided
	// by 64MB (arena size) gives us the top level index, which is the same
	// as the 2nd level index on all 64-bit systems except Windows (level 1
	// index is always 0).
	ai := arenaIndex(addr.addr())
	if p.test || mheap_.arenas[ai.l1()] == nil || mheap_.arenas[ai.l1()][ai.l2()] == nil {
		// not in reserved or already mapped arena memory region,
		// it's highly likely that we're out of memory
		// Likely, but there still could be a valid address greater
		// than or equal to `addr` - TODO!!!!! in what cases could this
		// happen?
		vAddr, ok := p.inUse.findAddrGreaterEqual(addr.addr())
		if ok {
			return offAddr{vAddr}
		} else {
			// The candidate search address is greater than any
			// known address, which means we definitely have no
			// free memory left.
			return maxOffAddr
		}
	}

	// OK, in reserved or already mapped arena memory region
	return addr
}

func (p *pageAlloc) find(npages uintptr) (uintptr, offAddr) {
	assertLockHeld(p.mheapLock)

	// Search algorithm.
	//
	// This algorithm walks each level l of the radix tree from the root level
	// to the leaf level. It iterates over at most 1 << levelBits[l] of entries
	// in a given level in the radix tree, and uses the summary information to
	// find either:
	//  1) That a given subtree contains a large enough contiguous region, at
	//     which point it continues iterating on the next level, or
	//  2) That there are enough contiguous boundary-crossing bits to satisfy
	//     the allocation, at which point it knows exactly where to start
	//     allocating from.
	//
	// i tracks the index into the current level l's structure for the
	// contiguous 1 << levelBits[l] entries we're actually interested in.
	//
	// NOTE: Technically this search could allocate a region which crosses
	// the arenaBaseOffset boundary, which when arenaBaseOffset != 0, is
	// a discontinuity. However, the only way this could happen is if the
	// page at the zero address is mapped, and this is impossible on
	// every system we support where arenaBaseOffset != 0. So, the
	// discontinuity is already encoded in the fact that the OS will never
	// map the zero page for us, and this function doesn't try to handle
	// this case in any way.

	// i is the beginning of the block of entries we're searching at the current level:
	// 	 - zero for 0th level;
	//   - for remaining levels is deduced by left shifting by 3 the index of the previous level at which
	//     a sufficient summary was found (gives us 8 child summaries).
	i := 0

	// firstFree is the region of address space that we are certain to
	// find the first free page in the heap. base and bound are the inclusive
	// bounds of this window, and both are addresses in the linearized, contiguous
	// view of the address space (with arenaBaseOffset pre-added). At each level,
	// this window is narrowed as we find the memory region containing the
	// first free page of memory. To begin with, the range reflects the
	// full process address space.
	//
	// firstFree is updated by calling foundFree each time free space in the
	// heap is discovered.
	//
	// At the end of the search, base.addr() is the best new
	// searchAddr we could deduce in this search.
	firstFree := struct {
		base, bound offAddr
	}{
		base:  minOffAddr,
		bound: maxOffAddr,
	}
	// foundFree takes the given address range [addr, addr+size) and
	// updates firstFree if it is a narrower range. The input range must
	// either be fully contained within firstFree or not overlap with it
	// at all (in the case when we iterate consecutive summaries at a certain level).
	//
	// This way, we'll record the first summary we find with any free
	// pages on the root level and narrow that down if we descend into
	// that summary. But as soon as we need to iterate beyond that summary
	// in a level to find a large enough range, we'll stop narrowing (no-op in this case).
	foundFree := func(addr offAddr, size uintptr) {
		if firstFree.base.lessEqual(addr) && addr.add(size-1).lessEqual(firstFree.bound) {
			// This range fits within the current firstFree window, so narrow
			// down the firstFree window to the base and bound of this range.
			firstFree.base = addr
			firstFree.bound = addr.add(size - 1)
		} else if !(addr.add(size-1).lessThan(firstFree.base) || firstFree.bound.lessThan(addr)) {
			// This range only partially overlaps with the firstFree range,
			// so throw.
			print("runtime: addr = ", hex(addr.addr()), ", size = ", size, "\n")
			print("runtime: base = ", hex(firstFree.base.addr()), ", bound = ", hex(firstFree.bound.addr()), "\n")
			throw("range partially overlaps")
		}
	}

	// lastSum is the summary which we saw on the previous level that made us
	// move on to the next level. Used to print additional information in the
	// case of a catastrophic failure.
	// lastSumIdx is that summary's index in the previous level.
	lastSum := packPallocSum(0, 0, 0)
	lastSumIdx := -1

nextLevel:
	for l := 0; l < len(p.summary); l++ { // may break out early if finds a sufficient contigous run of free pages at the start of a summary or crossing summary boundaries
		entriesPerBlock := 1 << levelBits[l] // for the root level is the whole level (for all other levels is 8)
		logMaxPages := levelLogPages[l]      // log number of pages a single summary at a particular level is responsible for

		// We've moved into a new level, so let's update i to our new
		// starting index. This is a no-op for level 0.
		i <<= levelBits[l]

		// Slice out the block of entries we care about (whole level for level 0).
		entries := p.summary[l][i : i+entriesPerBlock]

		// Determine j0, the first index we should start iterating from on this level.
		// The searchAddr may help us eliminate iterations if we followed the
		// searchAddr on the previous level or we're on the root level, in which
		// case the searchAddr should be the same as i after levelShift.
		//
		//   1. offAddrToLevelIndex(l, p.searchAddr) - gets the current level's summary index inside which p.searchAddr is contained;

		//   2. ^(entriesPerBlock-1) for all levels except level 0 is ^(8 - 1) = ^(7) = 0b1111111111111111111111111111111111111111111111111111111111111000
		//                           for level 0 is ^(16384 - 1) = ^(16383) =           0b1111111111111111111111111111111111111111111111111100000000000000

		//   3. searchIdx&^(entriesPerBlock-1) - by masking off (set to 0) the lower 14 bits (level 0) or the lower 3 bits (remaining levels) we get
		//                                       the start index of the aligned block of summaries in which the p.searchAddr address is contained.
		//      									- level 0 block size = 16384 (start index 0)
		//      									- remainining levels block size = 8 (level is aligned by 8 from the start)
		//
		//   4. searchIdx&^(entriesPerBlock-1) == i - since `i` is always aligned to the start index of an aligned block of summaries,
		//                                            if `i` and the result we got in the previous step are equal, this means that p.searchAddr is contained
		//                                            in the same block of summaries as `i` and we can eliminate iterations on this level
		//                                            by starting our search at searchIdx instead of `i`.
		j0 := 0
		if searchIdx := offAddrToLevelIndex(l, p.searchAddr); searchIdx&^(entriesPerBlock-1) == i {
			// mask off (set to 0) the lower 14 (level 0) or 3 bits (remaining levels) to convert searchIdx
			// to an index relative to index 0 in entries level subslice since on all levels except level 0
			// we slice out the block from the original level
			j0 = searchIdx & (entriesPerBlock - 1)
		}

		// Run over the level entries looking for
		// a contiguous run of at least npages either
		// within an entry or across entries.
		//
		// base contains the page index (relative to the first page of
		// the first summary of the current block) of the currently
		// considered run of consecutive pages.
		//
		// size contains the size of the currently considered
		// run of consecutive pages.
		var base, size uint
		for j := j0; i < len(entries); j++ { // go through the block of summaries at the current level
			sum := entries[j]
			if sum == 0 {
				// A full entry means we broke any streak and
				// that we should skip it altogether.
				size = 0
				continue
			}

			// We've encountered a non-zero summary which means
			// free memory, so update firstFree
			// (either the first free summary encountered on the root level,
			// a narrower range in descending levels, or no-op non-overlapping
			// if we have to iterate beyond the first summary found at a particular
			// level.
			//
			// This is done to compute a new p.searchAddr to eliminate even more
			// iterations next time (`firstFree.base` is passed to `p.findMappedAddr()`
			// before returning from this method).
			foundFree(levelIndexToOffAddr(l, i+j), (uintptr(1)<<logMaxPages)*pageSize)

			s := sum.start() // get the number of free pages at the start of the summary

			// if the starting run of the current summary can accomodate the requested number
			// of contiguous pages or if by crossing the boundary from the previous summary
			// together with the start of the current summary we get the requested number
			// of contiguos pages
			if size+s >= uint(npages) {
				// If size == 0 we don't have a run yet,
				// which means base isn't valid. So, set
				// base to the first page in this block.
				if size == 0 {
					base = uint(j) << logMaxPages // convert relative to the start of the current block of summaries index to an absolute index into the current level
				}
				// We hit npages; we're done!
				size += s
				break
			}
			if sum.max() >= uint(npages) { // if this summary's internal run of pages satisfies the request
				// The entry itself contains npages contiguous
				// free pages, so continue on the next level
				// to find that run.
				i += j // summary index at the current level which satisfies `npages`, will be the parent of the 8 block of summaries at the next level
				lastSumIdx = i
				lastSum = sum
				continue nextLevel // descend down into the summary
			}
			if size == 0 || s < 1<<logMaxPages {
				// We either don't have a current run started, or this entry
				// isn't totally free (meaning we can't continue the current
				// one), so try to begin a new run by setting size and base
				// based on sum.end.
				size = sum.end()
				base = uint(j+1)<<logMaxPages - size // get page index relative to the first page of the current block of summaries
				continue                             // continue to next summary in the block of summaries at the current level
			}
			// The entry is completely free, so continue the run.
			size += 1 << logMaxPages // + number of pages a single summary is responsible for at the current level
		}

		if size >= uint(npages) {
			// We found a sufficiently large run of free pages at the current
			// level straddling some boundary, so compute the address and return it:
			//   - levelIndexToOffAddr(l, i) gives us the offset address which the first run of summaries at this level is responsible for;
			//   - uintptr(base) is the page index relative to the first page of the first summary of the current block;
			//   - by multiplying the above value we get the offset addresss at which the required consecutive run of free pages starts;
			//   - by adding the above values we get the starting address within x86-64 offset address space of the required contiguos run of free pages
			addr := levelIndexToOffAddr(l, i).add(uintptr(base) * pageSize).addr()

			// return address at which the required run of contiguos pages starts and
			// check the possible next search address against currently mapped address
			// ranges and if it's not contained in any of them return the closest
			// mapped address greater than it or max offset address if it's greater than
			// any known address, which designates we're out of memory TODO!!!!! how can
			// this happen? `addr` is always greater than or equal to the returned search address?
			return addr, p.findMappedAddr(firstFree.base)
		}

		if l == 0 {
			// We're at level zero, so that means we've exhausted our search without finding
			// a sufficient run of conitguous pages.
			return 0, maxSearchAddr()
		}

		// We're not at level zero, and we exhausted the level we were looking in.
		// This means that either our calculations were wrong or the level above
		// lied to us. In either case, dump some useful state and throw.
		print("runtime: summary[", l-1, "][", lastSumIdx, "] = ", lastSum.start(), ", ", lastSum.max(), ", ", lastSum.end(), "\n")
		print("runtime: level = ", l, ", npages = ", npages, ", j0 = ", j0, "\n")
		print("runtime: p.searchAddr = ", hex(p.searchAddr.addr()), ", i = ", i, "\n")
		print("runtime: levelShift[level] = ", levelShift[l], ", levelBits[level] = ", levelBits[l], "\n")
		for j := 0; j < len(entries); j++ {
			sum := entries[j]
			print("runtime: summary[", l, "][", i+j, "] = (", sum.start(), ", ", sum.max(), ", ", sum.end(), ")\n")
		}
		throw("bad summary data")
	}

	// Since we've gotten to this point, that means we haven't found a
	// sufficiently-sized free region straddling some boundary (chunk or larger).
	// This means the last summary at the last level we inspected must have had a large enough "max"
	// value, so look inside the chunk to find a suitable run.
	//
	// After iterating over all levels, i must contain a chunk index which
	// is what the final level represents.
	ci := chunkIdx(i) // convert index of summary at the last level into an index into the bitmap

	// convert global index into a 2-level index into the bitmap and find an index within the bitmap chunk
	// which starts a suitable run of free pages.
	//
	// `j` - is the start index within the bitmap chunk at which a suitable contiguos run of free pages starts.
	//       If fails to find any free space, it an index of ^uint(0) is returned and the new searchIdx should be ignored.
	//
	// `searchIdx` - is the index at which the first free page within the bitmap chunk is found, will help narrow down
	//               the next search within the same bitmap chunk TODO!!!!!
	j, searchIdx := p.chunkOf(ci).find(npages, 0)
	if j == ^uint(0) {
		// We couldn't find any space in this chunk despite the summaries telling
		// us it should be there. There's likely a bug, so dump some state and throw.
		sum := p.summary[len(p.summary)-1][i]
		print("runtime: summary[", len(p.summary)-1, "][", i, "] = (", sum.start(), ", ", sum.max(), ", ", sum.end(), ")\n")
		print("runtima: npages = ", npages, "\n")
		throw("bad summary data")
	}

	// Compute the address at which the free space starts:
	//   - chunkBase(ci) converts index of found bitmap chunk with sufficiet run of contiguos free pages into an address within x86-64 offset address space;
	//   - (uintptr(j)*pageSize) gives us the offset in bytes from the start of the bitmap chunk at which the contiguos run of free pages starts;
	//   - adding those two together gives us the starting address within x86-64 offset address space of the required contiguos run of free pages.
	addr := chunkBase(ci) + uintptr(j)*pageSize // starting address of the requested run of free pages

	// Since we actually searched the chunk, we may have
	// found an even narrower free window:
	//   - chunkBase(ci) converts index of found bitmap chunk with sufficiet run of contiguos free pages into an address within x86-64 offset address space;
	//   - (uintptr(searchIdx)*pageSize) gives us the offset in bytes from the start of the bitmap chunk at which the first free page was found;
	//   - adding those 2 together give us an even more precise address within x86-64 offset address space at which the next search may begin.
	searchAddr := chunkBase(ci) + uintptr(searchIdx)*pageSize // address within x86-64 offset address space at which the next search may begin

	// - chunkBase(ci+1) gives us the starting address of the page the next bitmap chunk's first page is responsible for;
	// - subtracting searchAddr from the above value gives us the size in bytes from the new searchAddress to the starting page of the next bitmap chunk;
	foundFree(offAddr{searchAddr}, chunkBase(ci+1)-searchAddr)

	// return address at which the required run of contiguos pages starts and
	// check the possible next search address against currently mapped address
	// ranges and if it's not contained in any of them return the closest address
	// mapped address greater than it or max offset address if it's greater than
	// any known address, which designates we're out of memory TODO!!!!! how can
	// this happen? `addr` is always greater than or equal to the returned search address?
	return addr, p.findMappedAddr(firstFree.base)
}

const (
	pallocSumBytes = unsafe.Sizeof(pallocSum(0))

	// maxPackedValue is the maximum value that any of the three fields in
	// the pallocSum may take on.
	maxPackedValue = 1 << logMaxPackedValue

	// each of the 3 packed values at level 0 should be able to represent
	// 2097151 pages, which is (1<<21) - 1 (log2(2097151 + 1) = 21).
	//
	// Level 4 is responsible for 512 pages, log2 of which is logPallocChunkPages (9).
	// Walking up the radix tree, a palloSum at the current level is represented
	// by 8 pallocSums at the level right bellow it, log2 of which is 3.
	// Those are levels 0-3, and so (summaryLevels-1)*summaryLevelBits stems from this.
	logMaxPackedValue = logPallocChunkPages + (summaryLevels-1)*summaryLevelBits // 21

	// freeChunkSum represents a palloSum on level 4,
	// where each of the 3 fields equals 512, and thus freeChunkSum
	// represents a single bitmap chunk, which is responsible for 512 pages, that are free
	freeChunkSum = pallocSum(uint64(pallocChunkPages) | // 1st field = 512
		uint64(pallocChunkPages<<logMaxPackedValue) | // shift by 21 => 2nd field = 512
		uint64(pallocChunkPages<<(2*logMaxPackedValue))) // shift by 21*2 => 3rd field = 512
)

// pallocSum is a packed summary type which packs three numbers: start, max,
// and end into a single 8-byte value. Each of these values are a summary of
// a bitmap and are thus counts, each of which may have a maximum value of
// 2^21 - 1, or all three may be equal to 2^21. The latter case is represented
// by just setting the 64th bit.
type pallocSum uint64

// packPallocSum takes a start, max, and end value and produces a pallocSum.
func packPallocSum(start, max, end uint) pallocSum {
	if max == maxPackedValue {
		return pallocSum(uint64(1 << 63))
	}

	return pallocSum((uint64(start) & (maxPackedValue - 1)) | // mask of the lower 21 bits
		((uint64(max)) & (maxPackedValue - 1) << logMaxPackedValue) | // mask of the lower 21 bits and shift up by 21
		((uint64(end) & (maxPackedValue - 1)) << (2 * logMaxPackedValue))) // mask of the lower 21 bits and shift up by 42
}

func (p pallocSum) start() uint {
	if uint64(p)&uint64(1<<63) != 0 {
		return maxPackedValue
	}

	return uint(uint64(p) & (maxPackedValue - 1))
}

func (p pallocSum) max() uint {
	if uint64(p)&uint64(1<<63) != 0 {
		return maxPackedValue
	}

	return uint((uint64(p) >> logMaxPackedValue) & (maxPackedValue - 1))
}

func (p pallocSum) end() uint {
	if uint64(p)&uint64(1<<63) != 0 {
		return maxPackedValue
	}

	return uint((uint64(p) >> (2 * logMaxPackedValue)) & (maxPackedValue - 1))
}

func (p pallocSum) unpack() (uint, uint, uint) {
	if uint64(p)&uint64(1<<63) != 0 {
		return maxPackedValue, maxPackedValue, maxPackedValue
	}

	return uint(uint64(p) & (maxPackedValue - 1)),
		uint((uint64(p) >> logMaxPackedValue) & (maxPackedValue - 1)),
		uint((uint64(p) >> (2 * logMaxPackedValue)) & (maxPackedValue - 1))
}

// mergeSummaries merges consecutive summaries which may each represent at
// most 1 << logMaxPagesPerSum pages each together into one.
func mergeSummaries(sums []pallocSum, logMaxPagesPerSum uint) pallocSum {
	// Merge the summaries in sums into one.
	//
	// We do this by keeping a running summary representing the merged
	// summaries of sums[:i] in start, max, and end.
	start, max, end := sums[0].unpack() // get starting values from the first summary
	for i := 1; i < len(sums); i++ {
		si, mi, ei := sums[i].unpack()

		// Merge in sums[i].start only if the running summary is
		// completely free, otherwise this summary's si
		// plays no role in the combined sum of start.
		if start == uint(i)<<logMaxPagesPerSum { //
			start += si
		}

		// Recompute the max value of the running sum by looking
		// across the boundary between the running sum and sums[i]
		// and at the max sums[i], taking the greatest of those two
		// and the max of the running sum.
		// If number of free pages at the end of the previous summary
		// combined with free pages at the start of the current summary
		// are greater than max, we have a new running max
		if end+si > max {
			max = end + si
		}

		if mi > max {
			max = mi
		}

		// Merge in end by checking if this new summary is totally
		// free. If it is, then we want to extend the running sum's
		// end by the new summary. If not, then we have some alloc'd
		// pages in there and we just want to take the end value in
		// sums[i].
		if ei == 1<<logMaxPagesPerSum { // if current summary is completely free
			end += 1 << logMaxPagesPerSum
		} else {
			end = ei
		}
	}

	return packPallocSum(start, max, end)
}
