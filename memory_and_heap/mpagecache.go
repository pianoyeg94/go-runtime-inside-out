package heap

import "unsafe"

const pageCachePages = 8 * unsafe.Sizeof(pageCache{}.cache) // 64

// pageCache represents a per-p cache of pages the allocator can
// allocate from without a lock. More specifically, it represents
// a pageCachePages*pageSize chunk of memory with 0 or more free
// pages in it (pageCachePages is 64).
type pageCache struct {
	base  uintptr // base address of the chunk
	cache uint64  // 64-bit bitmap representing free pages (1 means free)
	// TODO!!!!!
	scav uint64 // 64-bit bitmap representing scavenged pages (1 means scavenged)
}

// empty returns true if the pageCache has any free pages, and false
// otherwise.
func (c *pageCache) empty() bool {
	return c.cache == 0
}

// alloc allocates npages from the page cache and is the main entry
// point for allocation.
//
// Returns a base address and the amount of scavenged memory in the
// allocated region in bytes.
//
// Returns a base address of zero on failure, in which case the
// amount of scavenged memory should be ignored.
func (c *pageCache) alloc(npages uintptr) (uintptr, uintptr) {
	if c.cache == 0 {
		// no more free pages left in cache
		return 0, 0
	}
	if npages == 1 {
		// find first free page from the right side
		i := uintptr(TrailingZeros64(c.cache)) // returns the number of trailing zero bits, thus i is the index of the first bit set to 1
		// scav := (c.scav >> i) & 1 // TODO!!!!!
		c.cache &^= 1 << i // set bit to mark in-use
		// c.scav &^= 1 << i  // TODO!!!!! clear bit to mark unscavenged
		// TODO!!!!! second return value is `uintptr(scav) * pageSize`
		return c.base + i*pageSize, 0
	}
	return c.allocN(uintptr(npages)) // implements similar logic to code above but for npages > 1
}

// allocN is a helper which attempts to allocate npages worth of pages
// from the cache. It represents the general case for allocating from
// the page cache.
//
// Returns a base address and the amount of scavenged memory in the
// allocated region in bytes.
func (c *pageCache) allocN(npages uintptr) (uintptr, uintptr) {
	i := findBitRange64(c.cache, uint(npages)) // find starting bit index of npages contiguous bits of 1s (free pages)
	if i >= 64 {
		// no contiguous npages chunk found in cache
		return 0, 0
	}
	mask := ((uint64(1) << i) - 1) << i // consecutive run of npages 1 bits starting at bit number i
	// scav := sys.OnesCount64(c.scav & mask) TODO!!!!!
	c.cache &^= mask // // mark in-use bits (turn them off)
	// c.scav &^= mask  // clear scavenged bits // TODO!!!!!

	// TODO!!!!! second return value is `uintptr(scav) * pageSize`
	return c.base + uintptr(i*pageSize), 0
}

// allocToCache acquires a pageCachePages-aligned chunk (64 pages) of free pages
// which may not be contiguous (not all pages may not be free in the chunk), and
// returns a pageCache structure which owns the chunk.
//
// p.mheapLock must be held.
//
// Must run on the system stack because p.mheapLock must be held.
//
//go:systemstack
func (p *pageAlloc) allocToCache() pageCache {
	assertLockHeld(p.mheapLock)

	// TODO!!!!! add details about how seacrhAddr can become greater
	// than any known chunk?
	//
	// If the searchAddr refers to a region which has a higher address than
	// any known chunk, then we know we're out of memory.
	if chunkIdx(p.searchAddr.addr()) >= p.end {
		return pageCache{}
	}

	c := pageCache{}
	ci := chunkIdx(p.searchAddr.addr()) // chunk index
	var chunk *pallocData
	if p.summary[len(p.summary)-1][ci] != 0 {
		// Fast path: there's free pages at or near the searchAddr address.
		chunk := p.chunkOf(ci)

		// 1. `chunkPageIndex(p.searchAddr.addr())` gets the page number relative
		//     to the `chunk` bitmap chunk within which `p.searchAddr` is contained
		//     (page 0 to page 511)
		//
		// 2. Returns first free page index into `j` in which `p.searchAddr` is contained,
		//    or which comes somewhere after this page if it's not free.
		//
		// 3. `j, _` ignores the new `searchIndex`, which represents the first known
		//     free page in `chunk` and where to begin the next search from. Ignores
		//     because we search only for a single page and in this case `j` is equal
		//     to new `searchIndex`.
		j, _ := chunk.find(1, chunkPageIndex(p.searchAddr.addr()))
		if j == ^uint(0) {
			// no free pages in `chunk` bitmap chunk
			throw("bad summary data")
		}
		c = pageCache{
			// Offset address aligned down to a multiple of 64 8KiB golang pages above which we found the first free page.
			//
			// 1. `chunkBase(ci)` get's the start address to which the `chunk` bitmap chunk corresponds to.
			//
			// 2. `alignDown(uintptr(j), 64)` aligns free page index down to the uint64 boundary in the bitmap chunk 8 element array.
			//
			// 3. `alignDown(uintptr(j), 64)*pageSize` gives us the number of bytes from chunk base address to the uint64 in which
			//     free page index is contained
			//
			// 4. `chunkBase(ci) + alignDown(uintptr(j), 64)*pageSize` gives us the start address for which a particular uint64 is responsible for
			//    and in which the free page is contained ((we don't know how more free pages are there in this 64-page chunk, so the allocation may
			//    not be contifuos.)
			base: chunkBase(ci) + alignDown(uintptr(j), 64)*pageSize,
			// 1. `chunk.pages64(j)` Returns a 64-bit bitmap representing a block of 64 pages aligned to 64.
			//     The returned block of pages is the one containing the j'th page in this pallocData. Each bit
			//     represents whether a page is in-use.
			//
			// 2. ^ - convert 0s which mean free pages in pallocData to 1s, which mean free pages in `pageCache.cache`.
			//        And 1s to 0s which mean in-use pages in pallocData, but 0s should mean in-use pages in `pageCache.cache`.
			cache: ^chunk.pages64(j),
			// scav: TODO!!!!! ,
		}
	} else {
		// Slow path: the searchAddr address had nothing there, so go find
		// the first free page the slow way.

		// Search algorithm.
		//
		// This algorithm walks each level l of the summaries radix tree from the root level to the leaf level.
		// It iterates over at most 16384 summaries at level 0 (whole level) and at most 8 summaries at the remaining
		// 4 levels in the radix tree (some iterations may be pruned with the help of `p.searchAddr`), and uses the
		// summary information to find either:
		//     1) That a given subtree contains a large enough contiguous region, at
		//        which point it continues iterating on the next level, or
		//
		//     2) That there are enough contiguous boundary-crossing bits to satisfy
		//        the allocation, at which point it knows exactly where to start
		//        allocating from.
		//
		//
		// TODO!!!!!
		// Returns updated `p.searchAddr` as the second value. It's updated according to the starting address of the first
		// page found at a particular level, if that page isn't reserved/mapped, returns starting address of reserved/mapped
		// address range with base strictly greater than unmapped page's base.
		// Updated `p.searchAddr` isn't used here, because we ask only for one page, so the returned address will be also the
		// updated `p.searchAddr`.
		addr, _ := p.find(1)
		if addr == 0 {
			// We failed to find adequate free space, so mark the searchAddr as OoM (Out of Memory)
			// and return an empty pageCache.
			p.searchAddr = maxSearchAddr()
			return pageCache{}
		}
		ci := chunkIndex(addr) // get global bitmap index of chunk in which addr is contained (chunk with at least a single free page)
		chunk = p.chunkOf(ci)  // get bitmap chunk
		c = pageCache{
			// Offset address aligned down to a multiple of 64 8KiB golang pages above which we found the first free page.
			// 1. `64*pageSize` - gives the size in bytes of 64 8KiB golang pages
			//
			// 2. `alignDown(addr, 64*pageSize),` gives us the start address for which a particular uint64 is responsible for
			//    and in which the free page is contained (we don't know how more free pages are there in this 64-page chunk,
			//    so the allocation may not be contifuos.
			base: alignDown(addr, 64*pageSize),
			// 1. `chunk.pages64(chunkPageIndex(addr))` Returns a 64-bit bitmap representing a block of 64 pages aligned to 64 pages.
			//     The returned block of pages is the one containing the `chunkPageIndex(addr)`'th page in this pallocData. Each bit
			//     represents whether the page is in-use.
			//
			// 2. ^ - convert 0s which mean free pages in pallocData to 1s, which mean free pages in `pageCache.cache`.
			//        And 1s to 0s which mean in-use pages in pallocData, but 0s should mean in-use pages in `pageCache.cache`.
			cache: ^chunk.pages64(chunkPageIndex(addr)),
			// scav: TODO!!!!! ,
		}
	}

	cpi := chunkPageIndex(c.base)    // get index within the bitmap which corrsponds to the 64-page chunk (uint64)
	chunk.allocPages64(cpi, c.cache) // mark free pages as taken within the 64-bit bitmap chunk

	// TODO!!!!!
	// chunk.scavenged.clearBlock64(cpi, c.cache&c.scav /* free and scavenged */)

	// Update as an allocation, but note that it's not contiguous.
	//
	// Updates last level palloc radix tree summary corresponding to
	// the 64-bit bitmap, this summary will tell the user that this
	// 64-page block is allocated. After that updates all the parent
	// summaries above (up to level 0 if neccessary).
	p.update(c.base, pageCachePages, false, true)

	// Set the search address to the last page represented by the cache.
	// Since all of the pages in this block are going to the cache, and we
	// searched for the first free page, we can confidently start at the
	// next page.
	//
	// However, p.searchAddr is not allowed to point into unmapped heap memory
	// unless it is maxSearchAddr, so make it the last page as opposed to
	// the page after.
	p.searchAddr = offAddr{c.base + pageSize*(pageCachePages-1)}
	return c
}
