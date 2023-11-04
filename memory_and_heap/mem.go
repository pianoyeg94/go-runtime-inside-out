package heap

import "unsafe"

// OS memory management abstraction layer
//
// Regions of the address space managed by the runtime may be in one of four
// states at any given time:
// 1) None - Unreserved and unmapped, the default state of any region.
// 2) Reserved - Owned by the runtime, but accessing it would cause a fault.
//               Does not count against the process' memory footprint.
// 3) Prepared - Reserved, intended not to be backed by physical memory (though
//               an OS may implement this lazily). Can transition efficiently to
//               Ready. Accessing memory in such a region is undefined (may
//               fault, may give back unexpected zeroes, etc.).
// 4) Ready - may be accessed safely.
//
// This set of states is more than is strictly necessary to support all the
// currently supported platforms. One could get by with just None, Reserved, and
// Ready. However, the Prepared state gives us flexibility for performance
// purposes. For example, on POSIX-y operating systems, Reserved is usually a
// private anonymous mmap'd region with PROT_NONE set, and to transition
// to Ready would require setting PROT_READ|PROT_WRITE. However the
// underspecification of Prepared lets us use just MADV_FREE to transition from
// Ready to Prepared. Thus with the Prepared state we can set the permission
// bits just once early on, we can efficiently tell the OS that it's free to
// take pages away from us when we don't strictly need them.
//
// This file defines a cross-OS interface for a common set of helpers
// that transition memory regions between these states. The helpers call into
// OS-specific implementations that handle errors, while the interface boundary
// implements cross-OS functionality, like updating runtime accounting.

// sysAlloc transitions an OS-chosen region of memory from None to Ready.
// More specifically, it obtains a large chunk of zeroed memory from the
// operating system, typically on the order of a hundred kilobytes
// or a megabyte. This memory is always immediately available for use.
//
// sysStat must be non-nil.
//
// Don't split the stack as this function may be invoked without a valid G,
// which prevents us from allocating more stack.
//
//go:nosplit
func sysAlloc(n uintptr, sysStat *sysMemStat) unsafe.Pointer {
	sysStat.add(int64(n))
	// TODO!!!!! gcController.mappedReady.Add(int64(n))
	return sysAllocOS(n)
}

// sysFree transitions a memory region from any state to None. Therefore, it
// returns memory unconditionally. It is used if an out-of-memory error has been
// detected midway through an allocation or to carve out an aligned section of
// the address space. It is okay if sysFree is a no-op only if sysReserve always
// returns a memory region aligned to the heap allocator's alignment
// restrictions.
//
// sysStat must be non-nil.
//
// Don't split the stack as this function may be invoked without a valid G,
// which prevents us from allocating more stack.
//
//go:nosplit
func sysFree(v unsafe.Pointer, n uintptr, sysStat *sysMemStat) {
	sysStat.add(-int64(n))
	// TODO!!!!! gcController.mappedReady.Add(-int64(n))

	// The munmap() system call deletes the mappings for the specified
	// address range, and causes further references to addresses within
	// the range to generate invalid memory references.
	sysFreeOS(v, n)
}

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
func sysReserve(v unsafe.Pointer, n uintptr) unsafe.Pointer {
	return sysReserveOS(v, n)
}

// sysMap transitions a memory region from Reserved to Prepared. It ensures the
// memory region can be efficiently transitioned to Ready.
//
// sysStat must be non-nil.
func sysMap(v unsafe.Pointer, n uintptr, sysStat *sysMemStat) {
	sysStat.add(int64(n))
	sysMapOS(v, n)
}
