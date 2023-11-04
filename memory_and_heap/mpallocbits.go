package heap

// numbering of bits within a single uint64
// starts from the right-most bit (little-endian bit order)
// 1s mean allocated pages, 0s mean free pages
type pageBits [pallocChunkPages / 64]uint64

// pageBits is a bitmap representing one bit per page in a palloc chunk.
func (b *pageBits) get(i uint) uint {
	// b[i/64] - get uint64 at index
	// (i % 64) - get the shift to acquire the requested bit
	// & 1 - mask off all bits except the value of the requested on
	return uint((b[i/64] >> (i % 64)) & 1)
}

// block64 returns the 64-bit aligned block of bits containing the i'th bit.
func (b *pageBits) block64(i uint) uint64 {
	return b[i/64]
}

// set sets bit i of pageBits.
func (b *pageBits) set(i uint) {
	// b[i/64] - get uint64 at index
	// (i % 64) - get the shift to acquire the requested bit
	// |= set the required bit
	b[i/64] |= 1 << (i % 64)
}

// setRange sets bits in the range [i, i+n).
func (b *pageBits) setRange(i, n uint) {
	_ = b[i/64]
	if n == 1 {
		// Fast path for the n == 1 case.
		b.set(i)
		return
	}

	// Set bits [i, j].
	j := i + n - 1

	// range doesn't span more than one uint64
	if i/64 == j/64 {
		b[i/64] |= ((uint64(1) << n) - 1) << (i % 64)
	}

	_ = b[j/64]
	// Set leading bits.
	b[i/64] = ^uint64(0) << (i % 64)

	// Set bits between leading and trailing bits
	for k := i/64 + 1; k < j/64; k++ {
		b[k] = ^uint64(0)
	}

	// Set trailing bits.
	// j%64 + 1 - +1 because the range is inclusive
	b[j/64] = (uint64(1) << (j%64 + 1)) - 1
}

// setAll sets all the bits of b.
func (b *pageBits) setAll() {
	for i := range b {
		b[i] = ^uint64(0)
	}
}

// setBlock64 sets the 64-bit aligned block of bits containing the i'th bit that
// are set in v.
func (b *pageBits) setBlock64(i uint, v uint64) {
	b[i/64] |= v
}

// clear clears bit i of pageBits.
func (b *pageBits) clear(i uint) {
	// 1 << (i % 64) - turn on the requested bit only
	// ^= 1 << (i % 64) - turn off the requested bit an turn on the rest
	// &^= 1 << (i % 64) - and with mask, which has the requested bit turned off and the others turned on
	b[i/64] &^= 1 << (i % 64)
}

// clearRange clears bits in the range [i, i+n).
func (b *pageBits) clearRange(i, n uint) {
	_ = b[i/64]
	if n == 1 {
		// Fast path for the n == 1 case.
		b.clear(i)
		return
	}

	// Clear bits [i, j].
	j := i + n - 1

	// range doesn't span more than one uint64
	if i/64 == j/64 {
		b[i/64] &^= ((uint64(1) << n) - 1) << (i % 64)
		return
	}

	_ = b[j/64]
	// Clear leading bits.
	b[i/64] &^= ^uint64(0) << (i % 64)

	// clear bits between leading and trailing bits
	for k := i + 1; k < j/64; k++ {
		b[k] = 0
	}

	// Clear trailing bits.
	// j%64 + 1 - +1 because the range is inclusive
	b[j/64] &^= (uint64(1) << (j%64 + 1)) - 1
}

// clearAll frees all the bits of b.
func (b *pageBits) clearAll() {
	for i := range b {
		b[i] = 0
	}
}

// clearBlock64 clears the 64-bit aligned block of bits containing the i'th bit that
// are set in v.
func (b *pageBits) clearBlock64(i uint, v uint64) {
	b[i/64] &^= v
}

// popcntRange counts the number of set bits in the
// range [i, i+n).
func (b *pageBits) popcntRange(i, n uint) (s uint) {
	if n == 1 {
		return uint(b[64/i] >> (i % 64) & 1)
	}

	_ = b[i/64]
	j := i + n - 1

	// range doesn't span more than one uint64
	if i/64 == j/64 {
		return uint(OnesCount64((b[i/64] >> (i % 64)) & ((1 << n) - 1)))
	}

	_ = b[j/64]
	// Count leading bits (shift to make zero out high bits)
	s += uint(OnesCount64(b[i/64] >> (i % 64)))
	for k := i/64 + 1; k < j/64; k++ {
		s += uint(OnesCount64(b[k]))
	}

	// Count trailing bits
	s += uint(OnesCount64(b[j/64] & ((1 << (j%64 + 1)) - 1)))
	return
}

// pallocBits is a bitmap that tracks page allocations for at most one
// palloc chunk.
//
// The precise representation is an implementation detail, but for the
// sake of documentation, 0s are free pages and 1s are allocated pages.
type pallocBits pageBits

// summarize returns a packed summary of the bitmap in pallocBits.
func (b *pallocBits) summarize() pallocSum {
	var start, max, cur uint
	const notSetYet = ^uint(0) // sentinel for start value
	start = notSetYet
	for i := 0; i < len(b); i++ {
		x := b[i]
		if x == 0 {
			cur += 64
			continue
		}

		t := uint(TrailingZeros64(x))
		l := uint(LeadingZeros64(x))

		// Finish any region spanning the uint64s
		cur += t
		if start == notSetYet {
			start = cur
		}
		if cur > max {
			max = cur
		}

		// Final region that might span to next uint64
		cur = l
	}

	if start == notSetYet {
		// Made it all the way through without finding a single 1 bit.
		const n = uint(64 * len(b))
		return packPallocSum(n, n, n)
	}

	if cur > max {
		max = cur
	}

	if max >= 64-2 {
		// There is no way an internal run of zeros in a single uint64 could beat max.
		return packPallocSum(start, max, cur)
	}

	// Now look inside each uint64 for runs of zeros.
	// All uint64s must be nonzero, or we would have aborted above.
outer:
	for i := 0; i < len(b); i++ {
		x := b[i]

		// Look inside this uint64. We have a pattern like
		// 000000 1xxxxx1 000000
		// We need to look inside the 1xxxxx1 for any contiguous
		// region of zeros.

		// We already know the trailing zeros are no larger than max. Remove them.
		x >>= TrailingZeros64(x) & 63
		if x&(x+1) == 0 { // no more zeros (except at the top).
			continue
		}

		// Strategy: shrink all runs of zeros by max. If any runs of zero
		// remain, then we've identified a larger maxiumum zero run.
		p := max     // number of zeros we still need to shrink by.
		k := uint(1) // current minimum length of runs of ones in x.
		for {
			// Shrink all runs of zeros by p places (except the top zeros).
			for p > 0 {
				// Shift p ones down into the top of each run of zeros.
				if p <= k {
					x |= x >> (p & 63)
					if x&(x+1) == 0 { // no more zeros (except at the top).
						continue outer
					}
					break
				}

				// Shift k ones down into the top of each run of zeros.
				x |= x >> (k & 63)
				if x&(x+1) == 0 { // no more zeros (except at the top).
					continue outer
				}

				p -= k
				// We've just doubled the minimum length of 1-runs.
				// This allows us to shift farther in the next iteration.
				k *= 2
			}

			// The length of the lowest-order zero run is an increment to our maximum.
			j := uint(TrailingZeros64(^x)) // count contiguous trailing ones
			x >>= j & 63                   // remove trailing ones
			j = uint(TrailingZeros64(x))   // count contiguous trailing zeros
			x >>= j & 63                   // remove zeros
			max += j                       // we have a new maximum!
			if x&(x+1) == 0 {              // no more zeros (except at the top).
				continue outer
			}

			p = j // remove j more zeros from each zero run.
		}
	}

	return packPallocSum(start, max, cur)
}

// find searches for npages contiguous free pages in pallocBits and returns
// the index where that run starts, as well as the index of the first free page
// it found in the search. searchIdx represents the first known free page and
// where to begin the next search from.
//
// If find fails to find any free space, it returns an index of ^uint(0) and
// the new searchIdx should be ignored.
//
// Note that if npages == 1, the two returned values will always be identical.
func (b *pallocBits) find(npages uintptr, searchIdx uint) (uint, uint) {
	if npages == 1 {
		// the index at which the first free page is found,
		// is also the index where to start the next search from
		// in this particular bitmap chunk
		addr := b.find1(searchIdx)
		return addr, addr
	} else if npages <= 64 {
		// tries to find a contiguous run of free pages, which satisfies the request for at most a conitguous run of 64 free pages:
		//    - at the start of a uint64
		//    - inside a uint64
		//    - a contiguous run which crosses the boundary between 2 uint64s
		//    - even if the requested number of pages isn't found the searchIdx could still be updated
		return b.findSmallN(npages, searchIdx)
	}

	// tries to find a contiguous run of free pages, which satisfies the request or at most a conitguous run of 512 free pages:
	//    - a contiguous run which crosses the boundary between 2 or more uint64s
	//    - even if the requested number of pages isn't found the searchIdx could still be updated
	return b.findLargeN(npages, searchIdx)
}

// find1 is a helper for find which searches for a single free page
// in the pallocBits and returns the index.
//
// See find for an explanation of the searchIdx parameter.
func (b *pallocBits) find1(searchIdx uint) uint {
	_ = b[0]                                         // lift nil check out of loop
	for i := searchIdx / 64; i < uint(len(b)); i++ { // i := searchIdx / 64 gives the index of the uint64 where to start the search from
		x := b[i]    // get uint64 which represents 64 pages out 512 pages represented by pallocBits
		if ^x == 0 { // if uint64 is a full run of 1 bits, it means all pages are allocated, go to next uint64
			continue
		}

		// TrailingZeros64(^x) - gives us the number of bits
		// till the first free page (0 bit flipped to 1 bit) in the current uint64,
		// so if there's a righ-most run of zeroes uint(TrailingZeros64(^x)) will be 0
		return i*64 + uint(TrailingZeros64(^x))
	}

	return ^uint(0) // if find fails to find any free space, it returns an index of ^uint(0) and the new searchIdx should be ignored.
}

// findSmallN is a helper for find which searches for npages contiguous free pages
// in this pallocBits and returns the index where that run of contiguous pages
// starts as well as the index of the first free page it finds in its search.
//
// See find for an explanation of the searchIdx parameter.
//
// Returns a ^uint(0) index on failure and the new searchIdx should be ignored.
//
// findSmallN assumes npages <= 64, where any such contiguous run of pages
// crosses at most one aligned 64-bit boundary in the bits.
func (b *pallocBits) findSmallN(npages uintptr, searchIdx uint) (uint, uint) {
	// newSearchIdx is computed only once as soon as a free pages is found
	end, newSearchIdx := uint(0), ^uint(0)           // TODO: `end`
	for i := searchIdx / 64; i < uint(len(b)); i++ { // i := searchIdx / 64 gives the index of the uint64 where to start the search from
		bi := b[i]    // get uint64 which represents 64 pages out 512 pages represented by pallocBits
		if ^bi == 0 { // if uint64 is a full run of 1 bits, it means all pages are allocated, go to next uint64
			end = 0 // TODO:
			continue
		}

		// First see if we can pack our allocation in the trailing
		// zeros plus the end of the last 64 bits.
		// TrailingZeros64 - give us bits from idx 0 to idx n (n < 64), from right to left
		//
		// newSearchIdx not yet computed, meaning no free page found up till now.
		// Since we've passed the previous ^bi == 0 check, there will be a least 1 free page in this 64-bit chunk
		if newSearchIdx == ^uint(0) {
			// The new searchIdx is going to be at these 64 bits after any
			// 1s we file, so count trailing 1s.
			//
			// 		- TrailingZeros64(^bi) - by flipping 1s, which mean allocated pages, and 0s, which mean free pages,
			//                               and counting the number of 0s from right to left, we get the fisrt free page index
			//                               within the uint64 (because a 1 in flipped bi means a free page).
			//      - i * 64               - gives us the base index (0, 64, 128 and so on until 448)
			newSearchIdx = i*64 + uint(TrailingZeros64(^bi))
		}

		//  - If there are no free pages at the start of this 64-bit chunk, start will be equal to 0
		//  - If there is only one free page at the start of this 64-bit chunk, start will be equal to 1
		//  - If there's more than one free page at the start of this 64-bit chunk, start will be equal to
		//    the number of contiguous free pages (from right to left), until an allocated page is encountered.
		start := uint(TrailingZeros64(bi))

		//  - If this is the first 64-bit chunk we're inspecting and the contiguous run of free pages
		//    at the start of the chunk satisfies the `npages` request, then the returned index will be on the 64-bit chunk boundary
		//
		//  - If there's a requested contiguous run of free pages which crosses a boundary between 2 uint64s
		if end+start >= uint(npages) {
			return i*64 - end, newSearchIdx
		}

		// Next, check the interior of the 64-bit chunk.
		j := findBitRange64(^bi, uint(npages)) // ^bi, because findBitRange64 uses the "shrink runs of 1s technique"
		if j < 64 {                            // if found a contiguous run of requested free pages within the current uint64
			return i*64 + j, searchIdx
		}

		end = uint(LeadingZeros64(bi)) // maybe some run of free pages in the high order bits of the current uint64
	}

	// not found requested contiguous run of free pages,
	// but the search index may still get updated BUT should be ignored
	// by the caller
	return ^uint(0), newSearchIdx
}

// findLargeN searches for npages contiguous free pages
// in this pallocBits and returns the index where that run starts, as well as the
// index of the first free page it found while searching.
//
// See alloc for an explanation of the searchIdx parameter. TODO!!!!!
//
// Returns a ^uint(0) index on failure and the new searchIdx should be ignored.
//
// findLargeN assumes npages > 64, where any such run of free pages
// crosses at least one aligned 64-bit boundary in the bits.
func (b *pallocBits) findLargeN(npages uintptr, searchIdx uint) (uint, uint) {
	// size holds the current size of the currently filed contiguous run of zeroes
	// start is always reset when a coniguous run of zeroes doesn't cross uint64 boundaries, and we still haven't satisfied the `npages` request
	start, size, newSearchIdx := ^uint(0), uint(0), ^uint(0)
	for i := searchIdx / 64; i < uint(len(b)); i++ {
		x := b[i]
		if x == ^uint64(0) { // if all pages are allocated within this 64-bit block of the 512-bit bitmap chunk
			size = 0 // no contiguous run of 0s crossing boundary from the previous uint64
			continue
		}

		if newSearchIdx == ^uint(0) { // if first free page within this 512-bit bitmap chunk is not yet found
			// The new searchIdx is going to be at these 64 bits after any
			// 1s we file, so count trailing 1s.
			newSearchIdx = i*64 + uint(TrailingZeros64(^x)) // ^x - count 1s (allocated) as 0s from right to left
		}

		if size == 0 { // no contiguous run of 0s crossing boundary from the previous uint64, so begin counting from the current uint64
			size = uint(LeadingZeros64(x)) // count high-order contiguous 0 bits
			start = i*64 + 64 - size       // gives us the index into the 512-bit bitmap chunk
			continue                       // cannot return here because we need a conitguous run of free pages greater than 64 (one single uint64 cannot provide us with that)
		}

		s := uint(TrailingZeros64(x)) // we have a contiguous run of 0s crossing boundary from the previous uint64
		if s+size >= uint(npages) {   // we have a contiguous run of 0s crossing uint64 boundaries that satisfy the `npages` request
			size += s
			// since start is awlays reset when a coniguous run of zeroes doesn't cross uint64 boundaries,
			// and we still haven't satisfied the `npages` request, we can be sure that `start` is the index
			// that started this contiguous run of 0s which satisfied the `npages` request
			return start, newSearchIdx
		}

		// if we still haven't got the requested number of pages
		// and the current uint64 doesn't consist of all free pages
		// the we broke the conitguos 0s stride, because there's
		// at least one 1 bit inside the current uint64, so we need to reset
		// the run of zeroes
		if s < 64 {
			size = uint(LeadingZeros64(x)) // count high-order contiguous 0 bits
			start = i*64 + 64 - size       // gives us the index into the 512-bit bitmap chunk
			continue                       // cannot return here because we need a conitguous run of free pages greater than 64 (one single uint64 cannot provide us with that)
		}

		// we still haven't got the requested number of pages,
		// but since the current uint64 consists of 0s only,
		// we haven't broken the coniguous stride of free pages
		size += 64
	}

	// we have gotten to the end of the 512 bitmap chunk
	// and didn't find the requested number of conitguous
	// free pages
	if size < uint(npages) {
		return ^uint(0), newSearchIdx
	}

	// we have gotten to the end of the 512 bitmap chunk
	// and FOUND the requested number of conitguous free pages
	// thank's to the last uint64
	return start, newSearchIdx
}

// allocRange allocates the range [i, i+n).
func (b *pallocBits) allocRange(i, n uint) {
	(*pageBits)(b).setRange(i, n) // sets bits in the range [i, i+n)
}

// allocAll allocates all the bits of b.
func (b *pallocBits) allocAll() {
	(*pageBits)(b).setAll()
}

// pages64 returns a 64-bit bitmap representing a block of 64 pages aligned
// to 64 pages. The returned block of pages is the one containing the i'th
// page in this pallocBits. Each bit represents whether the page is in-use.
func (b *pallocBits) pages64(i uint) uint64 {
	return (*pageBits)(b).block64(i)
}

// pages64 returns a 64-bit bitmap representing a block of 64 pages aligned
// to 64 pages. The returned block of pages is the one containing the i'th
// page in this pallocBits. Each bit represents whether the page is in-use.
//
// alloc is a bitmask with free pages set to 1, when ORing it with pallocBits,
// 0s (free pages) become 1s within the 64-bit bitmap (allocated pages).
func (b *pallocBits) allocPages64(i uint, alloc uint64) {
	(*pageBits)(b).setBlock64(i, alloc)
}

// findBitRange64 returns the bit index of the first set of
// n consecutive 1 bits. If no consecutive set of 1 bits of
// size n may be found in c, then it returns an integer >= 64.
// n must be > 0.
// Check out mpallocbits.txt:func findBitRange64(c uint64, n uint) uint {}
// for an example, which explains the code of this function.
// The last part of the method is very similar to this implementation.
func findBitRange64(c uint64, n uint) uint {
	// This implementation is based on shrinking the length of
	// runs of contiguous 1 bits. We remove the top n-1 1 bits
	// from each run of 1s, then look for the first remaining 1 bit.
	p := n - 1   // number of 1s we want to remove.
	k := uint(1) // current minimum width of runs of 0 in c.
	for p > 0 {
		if p <= k {
			// Shift p 0s down into the top of each run of 1s.
			c &= c >> (p & 63)
			break
		}

		// Shift k 0s down into the top of each run of 1s.
		c &= c >> (k & 63)
		if c == 0 {
			return 64
		}
		p -= k
		// We've just doubled the minimum length of 0-runs.
		// This allows us to shift farther in the next iteration.
		k *= 2
	}

	// Find first remaining 1.
	// Since we shrunk from the top down, the first 1 is in
	// its correct original position.
	return uint(TrailingZeros64(c))
}

type pallocData struct {
	pallocBits
	scavenged pageBits // TODO!!!!!
}

// TODO!!!!!
//
// allocRange sets bits [i, i+n) in the bitmap to 1 and
// updates the scavenged bits appropriately.
func (m *pallocData) allocRange(i, n uint) {
	// Clear the scavenged bits when we alloc the range.
	m.pallocBits.allocRange(i, n) // sets bits in the range [i, i+n)
	// m.scavenged.clearRange(i, n) // TODO!!!!!
}

// TODO!!!!!
//
// allocAll sets every bit in the bitmap to 1 and updates
// the scavenged bits appropriately.
func (m *pallocData) allocAll() {
	// Clear the scavenged bits when we alloc the range.
	m.pallocBits.allocAll()
	// m.scavenged.clearAll() // TODO!!!!!
}
