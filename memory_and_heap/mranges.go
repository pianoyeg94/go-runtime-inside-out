package heap

import (
	"unsafe"
)

// addrRange represents a region of address space.
//
// An addrRange must never span a gap in the address space.
type addrRange struct {
	// base and limit together represent the region of address space
	// [base, limit). That is, base is inclusive, limit is exclusive.
	// These are address over an offset view of the address space on
	// platforms with a segmented address space, that is, on platforms
	// where arenaBaseOffset != 0.
	base, limit offAddr
}

// makeAddrRange creates a new address range from two virtual addresses.
//
// Throws if the base and limit are not in the same memory segment.
func makeAddrRange(base, limit uintptr) addrRange {
	r := addrRange{offAddr{base}, offAddr{limit}}
	// Will be ">" if addr range is in the user-space segment and "<" if in kernel-space
	// ("=" - for systems where address space is linear and not segmented, not Linux x86-64bit)
	if (base-arenaBaseOffset >= base) != (limit-arenaBaseOffset >= limit) {
		throw("addr range base and limit are not in the same memory segment")
	}

	return r
}

// size returns the size of the range represented in bytes.
func (a addrRange) size() uintptr {
	if a.base.lessThan(a.limit) {
		return 0
	}

	// Subtraction is safe because limit and base must be in the same
	// segment of the address space.
	return a.limit.diff(a.base)
}

// contains returns whether or not the range contains a given address.
func (a addrRange) contains(addr uintptr) bool {
	return a.base.lessEqual(offAddr{addr}) && (offAddr{addr}).lessThan(a.limit)
}

// subtract takes the addrRange toPrune (a) and cuts out any overlap with
// from (b), then returns the new range. subtract assumes that a and b
// either don't overlap at all, only overlap on one side, or are equal.
// If b is strictly contained in a, thus forcing a split, it will throw.
func (a addrRange) subtract(b addrRange) addrRange {
	if b.base.lessThan(a.base) && a.limit.lessEqual(b.limit) { // a (toPrune) is strictly contained in b (from)
		return addrRange{}
	} else if a.base.lessThan(b.base) && b.limit.lessEqual(a.limit) { // b (from) is strictly contained in a (toPrune)
		throw("bad prune")
	} else if b.limit.lessThan(a.limit) && a.base.lessThan(b.limit) { // a overlaps with b on a's left
		a.base = b.limit
	} else if a.base.lessThan(b.base) && b.base.lessThan(a.limit) { // a overlaps with b on a's right
		a.limit = b.base
	}
	// if none of the above cases were true,
	// the toPrune addr range will remain unchanged,
	// since both toPrune and from are equal

	return a
}

var (
	// minOffAddr is the minimum address in the offset space, and
	// it corresponds to the virtual address arenaBaseOffset (17 sign bits + 47 bits of value)
	minOffAddr = offAddr{arenaBaseOffset} // 1111111111111111100000000000000000000000000000000000000000000000

	// ((1 << heapAddrBits) - 1) => 48 bits set, 16 high-order bits are 0, highest address in a linear
	//                              address space, but a non-canonical address in Linux's offset address space.
	//
	// arenaBaseOffset => 47 bits zeroed out and 17 high-order bits set to 1, lowest address in Linux's
	//                    offset address space
	//
	// (((1 << heapAddrBits) - 1) + arenaBaseOffset) => because of the 48th bit set in the first operand,
	//                                                  adding it with the second operand, where the 17 high-order
	//                                                  bits are set, zeroes out the 17 high-order bits and overflows
	//                                                  to the 65th bit, which gets set
	//
	// (((1 << heapAddrBits) - 1) + arenaBaseOffset) & uintptrMask => masks off the 65th bit to give the
	//                                                                highest address in Linux's offset address space
	maxOffAddr = offAddr{(((1 << heapAddrBits) - 1) + arenaBaseOffset) & uintPtrMask} // 0000000000000000011111111111111111111111111111111111111111111111
)

// offAddr represents an address in a contiguous view
// of the address space on systems where the address space is
// segmented. On other systems, it's just a normal address.
// https://www.kernel.org/doc/gorman/html/understand/understand007.html
// https://linux-kernel-labs.github.io/refs/heads/master/lectures/address-space.html
// https://unix.stackexchange.com/questions/509607/how-a-64-bit-process-virtual-address-space-is-divided-in-linux
// https://www.kernel.org/doc/html/latest/x86/x86_64/mm.html
type offAddr struct {
	// a is just the virtual address, but should never be used
	// directly. Call addr() to get this value instead.
	a uintptr
}

// add adds a uintptr offset to the offAddr.
func (l offAddr) add(bytes uintptr) offAddr {
	return offAddr{a: l.a + bytes}
}

// sub subtracts a uintptr offset from the offAddr.
func (l offAddr) sub(bytes uintptr) offAddr {
	return offAddr{a: l.a - bytes}
}

// diff returns the amount of bytes in between the
// two offAddrs.
func (l1 offAddr) diff(l2 offAddr) uintptr {
	return l1.a - l2.a
}

// lessThan returns true if l1 is less than l2 in the offset
// address space.
func (l1 offAddr) lessThan(l2 offAddr) bool {
	return (l1.a - arenaBaseOffset) < (l2.a - arenaBaseOffset)
}

// lessEqual returns true if l1 is less than or equal to l2 in
// the offset address space.
func (l1 offAddr) lessEqual(l2 offAddr) bool {
	return (l1.a - arenaBaseOffset) <= (l2.a - arenaBaseOffset)
}

// equal returns true if the two offAddr values are equal.
func (l1 offAddr) equal(l2 offAddr) bool {
	// No need to compare in the offset space, it
	// means the same thing.
	return l1.a == l2.a
}

// addr returns the virtual address for this offset address.
func (l offAddr) addr() uintptr {
	return l.a
}

// addrRanges is a data structure holding a collection of ranges of
// address space.
//
// The ranges are coalesced eagerly to reduce the
// number ranges it holds.
//
// The slice backing store for this field is persistentalloc'd
// and thus there is no way to free it.
//
// addrRanges is not thread-safe.
type addrRanges struct {
	// ranges is a slice of ranges sorted by base (not in heap, default capacity of 16)
	ranges []addrRange

	// totalBytes is the total amount of address space in bytes counted by
	// this addrRanges.
	totalBytes uintptr

	// sysStat is the stat to track allocations by this type
	sysStat *sysMemStat
}

func (a *addrRanges) init(sysStat *sysMemStat) {
	ranges := (*notInHeapSlice)(unsafe.Pointer(&a.ranges))
	ranges.len = 0
	ranges.cap = 16
	ranges.array = (*notInHeap)(persistentalloc(unsafe.Sizeof(addrRange{})*uintptr(ranges.cap), PtrSize, sysStat))
	a.sysStat = sysStat
	a.totalBytes = 0
}

// findSucc returns the first index in `a` such that `addr` is
// less than the base of the addrRange at that index:
//
//   - if addr is less than all of the existing addr range bases, index will be 0;
//
//   - if addr is greater than all of the existing addr range bases, index will be the append index.
//
//   - if addr is equal to some addr range's base, index will be of
func (a *addrRanges) findSucc(addr uintptr) int {
	base := offAddr{addr}

	// Narrow down the search space via binary search
	// for large addrRanges until we have at most iterMax
	// candidates left.
	const iterMax = 8
	bot, top := 0, len(a.ranges) // left and right range pointers used to narrow down the search
	for top-bot > iterMax {
		i := ((top - bot) / 2) + bot // get middle of current range, need to add `bot` because `((top - bot) / 2)` yields an offset index from `bot`
		if a.ranges[i].contains(base.a) {
			// a.ranges[i] contains base, so
			// its successor is the next addrRange
			// if any
			return i + 1
		}
		if base.lessThan(a.ranges[i].base) {
			// In this case addrRange at index `i`
			// might actually be the successor, but
			// we can't be sure until we check the ones
			// before it, so cap the range by `i` from
			// the top.
			top = i
		} else {
			// In this case we know `base` is greater than
			// or equal to a.ranges[i].limit-1, because the
			// "contains" case above didn't yield true,
			// so `i` is definitely not the successor.
			// We already checked ``, so pick the next
			// one and cap the range by `i + 1` from the bottom.
			bot = i + 1
		}
	}

	// There are top-bot candidates left (at max 8),
	// so iterate over them and find the first that
	// base is strictly less than.
	for i := bot; i < top; i++ {
		if base.lessThan(a.ranges[i].base) {
			return i
		}
	}

	return top
}

// findAddrGreaterEqual returns the smallest address represented by `a`
// that is >= addr. Thus, if the address is represented by `a`,
// then it returns addr. The second return value indicates whether
// such an address exists for addr in `a`. That is, if addr is larger than
// any address known to `a`, the second return value will be false.
func (a *addrRanges) findAddrGreaterEqual(addr uintptr) (uintptr, bool) {
	// findSucc returns the first index in `a` such that `addr` is
	// less than the base of the addrRange at that index:
	//
	//   - if addr is less than all of the existing addr range bases, index will be 0;
	//
	//   - if addr is greater than all of the existing addr range bases, index will be the append index.
	i := a.findSucc(addr)
	if i == 0 {
		// if addr is less than all of the existing addr range bases,
		// return the base of the first addr range in `a`
		return a.ranges[0].base.addr(), true
	}
	if a.ranges[i-1].contains(addr) {
		// addr range's base at index `i` should be strictly greater
		// than `addr`, so check if the previous `addr` range contains
		// `addr`
		return addr, true
	}
	if i < len(a.ranges) {
		// `addr` is not contained in any addr range,
		// so return the base addr of the addr range
		// which is strictly greater than `addr` and
		// is closest to `addr`
		return a.ranges[i].base.addr(), true
	}
	// `addr` is not contained in any addr range
	// and is greater than all of the addr ranges
	// contained in `a`
	return 0, false
}

// add inserts a new address range to a.
//
// r must not overlap with any address range in a and r.size() must be > 0.
func (a *addrRanges) add(r addrRange) {
	// The copies in this function are potentially expensive, but this data
	// structure is meant to represent the Go heap. At worst, copying this
	// would take ~160µs assuming a conservative copying rate of 25 GiB/s (the
	// copy will almost never trigger a page fault) for a 1 TiB heap with 4 MiB
	// arenas which is completely discontiguous. ~160µs is still a lot, but in
	// practice most platforms have 64 MiB arenas (which cuts this by a factor
	// of 16) and Go heaps are usually mostly contiguous, so the chance that
	// an addrRanges even grows to that size is extremely low.

	// An empty range has no effect on the set of addresses represented
	// by a, but passing a zero-sized range is almost always a bug.
	if r.size() == 0 {
		print("runtime: range = {", hex(r.base.addr()), ", ", hex(r.limit.addr()), "}\n")
		throw("attempted to add zero-sized address range")
	}

	// Because we assume r is not currently represented in a,
	// findSucc gives us our insertion index.
	i := a.findSucc(r.base.addr())
	coalesceDown := i > 0 && a.ranges[i-1].limit.equal(r.base)
	coalesceUp := i < len(a.ranges) && r.limit.equal(a.ranges[i].base)
	if coalesceUp && coalesceDown {
		// We have neighbors and they both border us.
		// Merge a.ranges[i-1], r, and a.ranges[i] together into a.ranges[i-1].
		a.ranges[i-1].limit = a.ranges[i].limit

		// Delete a.ranges[i].
		copy(a.ranges[i:], a.ranges[i+1:])
		a.ranges = a.ranges[:len(a.ranges)-1]
	} else if coalesceDown {
		// We have a neighbor at a lower address only and it borders us.
		// Merge the new space into a.ranges[i-1].
		a.ranges[i-1].limit = r.limit
	} else if coalesceUp {
		// We have a neighbor at a higher address only and it borders us.
		// Merge the new space into a.ranges[i].
		a.ranges[i].base = r.base
	} else {
		// We may or may not have neighbors which don't border us.
		// Add the new range.
		if len(a.ranges)+1 > cap(a.ranges) {
			// Grow the array. Note that this leaks the old array, but since
			// we're doubling we have at most 2x waste. For a 1 TiB heap and
			// 4 MiB arenas which are all discontiguous (both very conservative
			// assumptions), this would waste at most 4 MiB of memory.
			oldRanges := a.ranges
			ranges := (*notInHeapSlice)(unsafe.Pointer(&a.ranges))
			ranges.len = len(oldRanges) + 1
			ranges.cap = cap(oldRanges) * 2
			ranges.array = (*notInHeap)(persistentalloc(unsafe.Sizeof(addrRange{})*uintptr(ranges.cap), PtrSize, a.sysStat))

			// Copy in the old array, but make space for the new range.
			copy(a.ranges[:i], oldRanges[:i])
			copy(a.ranges[i+1:], oldRanges[i:])
		} else {
			// extend length
			a.ranges = a.ranges[:len(a.ranges)+1]
			// make space for new element
			copy(a.ranges[i+1:], a.ranges[i:])
		}

		a.ranges[i] = r
	}

	a.totalBytes += r.size()
}
