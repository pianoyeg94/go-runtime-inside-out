package heap

const (
	_EAGAIN = 0xb
	_ENOMEM = 0xc

	_PROT_NONE  = 0x0
	_PROT_READ  = 0x1
	_PROT_WRITE = 0x2
	_PROT_EXEC  = 0x4

	_MAP_ANON    = 0x20
	_MAP_PRIVATE = 0x2
	// Don't interpret addr as a hint: place the mapping at
	// exactly that address.  addr must be suitably aligned: for
	// most architectures a multiple of the page size is
	// sufficient; however, some architectures may impose
	// additional restrictions.  If the memory region specified
	// by addr and length overlaps pages of any existing
	// mapping(s), then the overlapped part of the existing
	// mapping(s) will be discarded.  If the specified address
	// cannot be used, mmap() will fail.
	_MAP_FIXED = 0x10

	// Do not expect access in the near future.  (For the time
	// being, the application is finished with the given range,
	// so the kernel can free resources associated with it.)
	//
	// After a successful MADV_DONTNEED operation, the semantics
	// of memory access in the specified region are changed:
	// subsequent accesses of pages in the range will succeed,
	// but will result in either repopulating the memory contents
	// from the up-to-date contents of the underlying mapped file
	// (for shared file mappings, shared anonymous mappings, and
	// shmem-based techniques such as System V shared memory
	// segments) or zero-fill-on-demand pages for anonymous
	// private mappings.
	//
	// Note that, when applied to shared mappings, MADV_DONTNEED
	// might not lead to immediate freeing of the pages in the
	// range.  The kernel is free to delay freeing the pages
	// until an appropriate moment.  The resident set size (RSS)
	// of the calling process will be immediately reduced
	// however.
	//
	// MADV_DONTNEED cannot be applied to locked pages, Huge TLB
	// pages, or VM_PFNMAP pages.  (Pages marked with the kernel-
	// internal VM_PFNMAP flag are special memory areas that are
	// not managed by the virtual memory subsystem.  Such pages
	// are typically created by device drivers that map the pages
	// into user space.)
	_MADV_DONTNEED = 0x4
	// The application no longer requires the pages in the range
	// specified by addr and len.  The kernel can thus free these
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
	//
	// The MADV_FREE operation can be applied only to private
	// anonymous pages. In Linux before version 4.12, when freeing
	// pages on a swapless system, the pages in the given range are
	// freed instantly, regardless of memory pressure.
	_MADV_FREE = 0x8
	// Enable Transparent Huge Pages (THP) for pages in the range
	// specified by addr and length.  Currently, Transparent Huge
	// Pages work only with private anonymous pages. The kernel will
	// regularly scan the areas marked as huge page candidates to
	// replace them with huge pages. The kernel will also allocate
	// huge pages directly when the region is naturally aligned to
	// the huge page size (see posix_memalign(2)).
	_MADV_HUGEPAGE = 0xe
	// Ensures that memory in the address range specified by addr
	// and length will not be backed by transparent hugepages.
	_MADV_NOHUGEPAGE = 0xf // (since Linux 4.5)
)
