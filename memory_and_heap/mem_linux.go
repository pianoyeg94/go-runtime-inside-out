package heap

import (
	"syscall"
	"unsafe"

	"github.com/pianoyeg94/golang-runtime/heap/atomic"
)

const (
	_EACCES = 13
	_EINVAL = 22
)

// Don't split the stack as this method may be invoked without a valid G, which
// prevents us from allocating more stack.
//
//go:nosplit
func sysAllocOS(n uintptr) unsafe.Pointer {
	p, err := mmap(0, n, _PROT_READ|_PROT_WRITE, _MAP_ANON|_MAP_PRIVATE, -1, 0)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok {
			if errno == _EACCES {
				print("runtime: mmap: access denied\n")
				exit(2)
			}

			if errno == _EAGAIN {
				// https://geode.apache.org/docs/guide/114/managing/heap_use/lock_memory.html
				// https://www.gnu.org/software/libc/manual/html_node/Locked-Memory-Details.html
				print("runtime: mmap: too much locked memory (check 'ulimit -l').\n")
				exit(2)
			}
		}

		return nil
	}

	return unsafe.Pointer(p)
}

var adviseUnused = uint32(_MADV_FREE)

func sysUnusedOS(v unsafe.Pointer, n uintptr) {
	if physHugePageSize != 0 {
		// If it's a large allocation, we want to leave huge
		// pages enabled. Hence, we only adjust the huge page
		// flag on the huge pages containing v and v+n-1, and
		// only if those aren't aligned.
		var head, tail uintptr
		if uintptr(v)&(physHugePageSize-1) != 0 {
			// Compute huge page containing v+n-1.
			// We give up the huge pages optimization
			// for in-use memory above v+n-1, because even if
			// this in-use chunk consinst only of a single
			// regular page, Linux's "transparent huge page" support
			// will merge pages into a huge page, effectively
			// undoing the affect of madvise calls bellow and
			// conflicting with PROT_NONE if GODEBUG hardecommit
			// is set.
			// Compute huge page containing v.
			head = alignDown(uintptr(v), physHugePageSize)
		}
		if (uintptr(v)+n)&(physHugePageSize-1) != 0 {
			// Compute huge page containing v+n-1.
			// We give up the huge pages optimization
			// for in-use memory above v+n-1, because even if
			// this in-use chunk consinst only of a single
			// regular page, Linux's "transparent huge page" support
			// will merge pages into a huge page, effectively
			// undoing the affect of madvise calls bellow and
			// conflicting with PROT_NONE if GODEBUG hardecommit
			// is set.
			tail = alignDown(uintptr(v)+n-1, physHugePageSize)
		}

		// Note that madvise will return EINVAL if the flag is
		// already set, which is quite likely (because sysUnused can be called
		// on different chunks of a single huge page). We ignore errors.
		//
		// _MADV_NOHUGEPAGE ensures that memory in the address range specified by addr
		// and length will not be backed by transparent hugepages.
		if head != 0 && head+physHugePageSize == tail {
			// head and tail are different but adjacent
			// (belong to 2 adjacent huge pages), so do
			// this in one call.
			madvise(unsafe.Pointer(head), 2*physHugePageSize, _MADV_NOHUGEPAGE)
		} else {
			// Only need _MADV_NOHUGEPAGE on the head and tail huge pages
			// so that Linux's "transparent huge page" support doesn't
			// merge the still in-use memory with the "released" memory
			// into a single huge page.
			// The huge pages in the middle (if any) will always have _MADV_FREE
			// or _MADV_DOTNEED called on them.

			// Advise the huge pages containing v and v+n-1.
			if head != 0 {
				madvise(unsafe.Pointer(head), physHugePageSize, _MADV_NOHUGEPAGE)
			}
			if tail != 0 && tail != head {
				// if head and tail do not belong
				// to the same huge page
				madvise(unsafe.Pointer(tail), physHugePageSize, _MADV_NOHUGEPAGE)
			}
		}
	}

	if uintptr(v)&(physPageSize-1) != 0 || n&(physPageSize-1) != 0 {
		// bellow madvise call will round this to any physical page
		// *covered* by this range, so an unaligned madvise will
		// release more memory than intended.
		throw("unaligned sysUnused")
	}

	// RSS (Resident set size)
	// In computing, resident set size is the portion of memory occupied
	// by a process that is held in main memory. The rest of the occupied
	// memory exists in the swap space or file system, either because some
	// parts of the occupied memory were paged out, or because some parts
	// of the executable were never loaded.

	// GODEBUG madvdontneed: setting madvdontneed=0 will use MADV_FREE
	// instead of MADV_DONTNEED on Linux when returning memory to the
	// kernel. This is more efficient, but means RSS numbers will
	// drop only when the OS is under memory pressure. On the BSDs and
	// Illumos/Solaris, setting madvdontneed=1 will use MADV_DONTNEED instead
	// of MADV_FREE. This is less efficient, but causes RSS numbers to drop
	// more quickly.
	var advise uint32
	if debug.madvdontneed != 0 {
		// Do not expect access in the near future.  (For the time
		// being, the application is finished with the given range,
		// so the kernel can free resources associated with it.)
		//
		// After a successful MADV_DONTNEED operation, the semantics
		// of memory access in the specified region are changed:
		// subsequent accesses of pages in the range will succeed,
		// but will result in zero-fill-on-demand pages for anonymous
		// private mappings.
		advise = _MADV_DONTNEED
	} else {
		// The application no longer requires the pages in the range
		// specified by addr and len. The kernel can thus free these
		// pages, but the freeing could be delayed until memory
		// pressure occurs. For each of the pages that has been
		// marked to be freed but has not yet been freed, the free
		// operation will be canceled if the caller writes into the
		// page. After a successful MADV_FREE operation, any stale
		// data (i.e., dirty, unwritten pages) will be lost when the
		// kernel frees the pages.  However, subsequent writes to
		// pages in the range will succeed and then kernel cannot
		// free those dirtied pages, so that the caller can always
		// see just written data. If there is no subsequent write,
		// the kernel can free the pages at any time. Once pages in
		// the range have been freed, the caller will see zero-fill-
		// on-demand pages upon subsequent page references.
		advise = atomic.Load(&adviseUnused) // atomic load for reasons described in the `if errno` block bellow
	}
	if errno := madvise(v, n, int32(advise)); advise == _MADV_FREE && errno != 0 {
		// MADV_FREE was added in Linux 4.5. Fall back to MADV_DONTNEED if it is
		// not supported.
		atomic.Store(&adviseUnused, _MADV_DONTNEED)
		madvise(v, n, _MADV_DONTNEED)
	}

	if debug.harddecommit > 0 {
		p, err := mmap(uintptr(v), n, _PROT_NONE, _MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0)
		if p != uintptr(v) || err != nil {
			throw("runtime: cannot disable permissions in address space")
		}
	}

	if debug.harddecommit > 0 {
		// GODEBUG harddecommit: setting harddecommit=1 causes memory that is returned to the OS to
		// also have protections removed on it. On Linux this means that sysUnused remaps "released"
		// address ranges as PROT_NONE releasing physical memory backing the range to the OS.
		//
		// This behavior mimics the behavior of decommit on Windows, and is helpfulin debugging
		// the scavenger. sysUsed is also updated to re-map the pages as PROT_READ|PROT_WRITE, mimicing
		// Windows' explicit commit behavior.
		p, err := mmap(uintptr(v), n, _PROT_NONE, _MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0)
		if p != uintptr(v) || err != nil {
			throw("runtime: cannot disable permissions in address space")
		}
	}
}

func sysUsedOS(v unsafe.Pointer, n uintptr) {
	// GODEBUG harddecommit: setting harddecommit=1 causes memory that is returned to the OS to
	// also have protections removed on it. On Linux this means that sysUnused remaps "released"
	// address ranges as PROT_NONE releasing physical memory backing the range to the OS.
	//
	// Makes the runtime map pages PROT_NONE in sysUnused on Linux, in addition to the usual madvise
	// calls. This behavior mimics the behavior of decommit on Windows, and is helpfulin debugging
	// the scavenger. sysUsed is also updated to re-map the pages as PROT_READ|PROT_WRITE, mimicing
	// Windows' explicit commit behavior.
	if debug.harddecommit > 0 {
		// If called on a fresh address range, meaning it is allready mmapped with the same protocols
		// and flags (sysMap was called, not re-used after harddecommit in sysUnused), then this will
		// be a no-op thanks to the _MAP_FIXED - if the memory region specified by v and n overlaps
		// pages of any existing mapping(s), then the overlapped part of the existing mapping(s) will
		// be discarded.
		//
		// If we're reusing a previously sysUnused address range this will re-map the pages as PROT_READ and
		// PROT_WRITE.
		p, err := mmap(uintptr(v), n, _PROT_READ|_PROT_WRITE, _MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0)
		if err != nil {
			if errno, ok := err.(syscall.Errno); ok && errno == _ENOMEM {
				throw("runtime: out of memory")
			}
		}
		if p != uintptr(v) || err != nil {
			throw("runtime: cannot remap pages in address space")
		}

		// Don't do the sysHugePage optimization in hard decommit mode.
		// We're breaking up pages everywhere, there's no point.
	}

	// Partially undo the NOHUGEPAGE marks from sysUnused
	// for whole huge pages between v and v+n. This may
	// leave huge pages off at the end points v and v+n
	// even though allocations may cover these entire huge
	// pages (if sysUnused was called on parts of a single
	// contiguos mapping previously). We could detect this
	// and undo NOHUGEPAGE on the end points as well, but
	// it's probably not worth the cost because when neighboring
	// allocations are freed sysUnused will just set NOHUGEPAGE again.
	sysHugePageOS(v, n)
}

func sysHugePageOS(v unsafe.Pointer, n uintptr) {
	if physHugePageSize != 0 {
		// THP (Transparent Huge Pages) are enabled by default
		// in madvize mode and are 2MiB in size.

		// Round v up to a huge page boundary.
		beg := alignUp(uintptr(v), physHugePageSize)

		// Round v+n down to a huge page boundary.
		end := alignDown(uintptr(v)+n, physHugePageSize)

		if beg < end {
			// There's at least one possible huge page
			// sitting on a 2MiB bondary in the address
			// range determined by v and v + n. So advise
			// the kernel that it should turn the huge page
			// aligned address subrange into into a run of
			// 2MiB huge pages next time it scans this area.
			madvise(unsafe.Pointer(beg), end-beg, _MADV_HUGEPAGE)
		}
	}
}

// Don't split the stack as this function may be invoked without a valid G,
// which prevents us from allocating more stack.
//
//go:nosplit
func sysFreeOS(v unsafe.Pointer, n uintptr) {
	// The munmap() system call deletes the mappings for the specified
	// address range, and causes further references to addresses within
	// the range to generate invalid memory references.  The region is
	// also automatically unmapped when the process is terminated.  On
	// the other hand, closing the file descriptor does not unmap the
	// region.

	// The address addr must be a multiple of the page size (but length
	// need not be).  All pages containing a part of the indicated range
	// are unmapped, and subsequent references to these pages will
	// generate SIGSEGV.  It is not an error if the indicated range does
	// not contain any mapped pages.
	munmap(uintptr(v), n)
}

func sysReserveOS(v unsafe.Pointer, n uintptr) unsafe.Pointer {
	// PROT_NONE: The memory is reserved, but cannot be read, written, or executed.
	// If this flag is specified in a call to mmap, a virtual memory area will be set aside
	// for future use in the process, and mmap calls without the MAP_FIXED flag will not use it
	// for subsequent allocations. For anonymous mappings, the kernel will not reserve any physical
	// memory for the allocation at the time the mapping is created.
	// https://stackoverflow.com/questions/12916603/what-s-the-purpose-of-mmap-memory-protection-prot-none
	p, err := mmap(uintptr(v), n, _PROT_NONE, _MAP_ANON|_MAP_PRIVATE, -1, 0)
	if err != nil {
		return nil
	}

	return unsafe.Pointer(p)
}

// Transitions the space from Reserved to Prepared, but
// not to Commited (on Linux there's no explicit commit phase,
// so it's purely a state that only the go runtime knows about).
// sysUsedOS will transition parts of the previously mapped space
// to Commited as it get's allocated from the go heap.
// Commiting chunks of memory, which hold at least one huge page
// sitting on a 2MiB boundary involves calling `madvise` passing
// it the _MADV_HUGEPAGE hint to enable THP (Transparent Huge Pages).
// This is an advise to the kernel that it should turn the huge page
// aligned address subrange into a run of 2MiB huge pages next time it scans
// this area.
func sysMapOS(v unsafe.Pointer, n uintptr) {
	p, err := mmap(uintptr(v), n, _PROT_READ|_PROT_WRITE, _MAP_ANON|_MAP_FIXED|_MAP_PRIVATE, -1, 0)
	if err != nil {
		if errno, ok := err.(syscall.Errno); ok && errno == _ENOMEM {
			throw("runtime: out of memory")
		}
	}
	if p != uintptr(v) || err != nil {
		print("runtime: mmap(", v, ", ", n, ") returned ", p, ", ", err, "\n")
		throw("runtime: cannot map pages in arena address space")
	}
}
