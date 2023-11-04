// Fixed-size object allocator.
//
// See malloc.go for overview.

package heap

import "unsafe"

// FixAlloc is a simple free-list allocator for fixed size objects.
// Malloc uses a FixAlloc wrapped around sysAlloc to manage its
// mcache and mspan objects.
//
// Memory returned by fixalloc.alloc is zeroed by default, but the
// caller may take responsibility for zeroing allocations by setting
// the zero flag to false. This is only safe if the memory never
// contains heap pointers.
//
// The caller is responsible for locking around FixAlloc calls.
// Callers can keep state in the object but the first word is
// smashed by freeing and reallocating.
//
// Consider marking fixalloc'd types not in heap by embedding
// runtime/internal/sys.NotInHeap.
//
// Basically is:
//
// A second-level proxy to the runtime's globally accessible `persistentChunks` linked list.
// This list holds 256KB off-heap non-GC mmapped and prefaulted memory chunks.
// `fixalloc`s sits on top of a `persistentalloc` first-level proxy, which is responsible for
// asking the OS for more memory in 256KB chunks, filling the `persistentChunks` list with
// new chunks and handing out blocks of memory to `P`s.
//
// `persitentalloc` first-level proxies come in 2 flavors:
//   - per-p to avoid lock contention when allocating new chunks to the persistentChunks list
//   - one globally accessible and locked when we run without a P
//
// So each chunk from the list is owned at maximum by one of the per-p or a single globally accessible `persistentalloc`
// first-level proxy. Both proxies only communicate with one chunk at a time until it gets exhausted.
//
// On first alloc gets a ~16KB chunk through the `persistentalloc` proxy which owns one of the 256KB chunks
// from `persistentChunks`, this may result in actually asking the OS for more memory if the `persistentalloc`
// doesn't yet own a chunk from `persistentChunks` or the current chunk is exhausted, in the end the ~16KB allocation
// is fullfilled from the 256KB chunk owned by the `persistentalloc`.
type fixalloc struct {
	// size is the size of allocations this `fixalloc` will hand out.
	// The minimum value of size is word size (8 bytes on 64-bit),
	// and the maximum size is 16KB, because this is the size of chunks
	// in which `fixalloc` allocates memory from `persistentalloc`, which
	// itself allocated memory in chunks of 256KB from the OS.
	size uintptr

	first func(arg, p unsafe.Pointer) // called first time p is returned from the underlying ~16KB chunk

	arg unsafe.Pointer

	list *mlink // linked list of allocated and freed `size` sized memory blocks

	nalloc uint32 // size of new chunks in bytes (~16KB, multiple of `size`)

	chunk uintptr // pointer to the current active ~16KB chunk (use uintptr instead of unsafe.Pointer to avoid write barriers)

	nchunk uint32 // bytes remaining in current chunk

	inuse uintptr // total in-use bytes now (in all ~16KB chunks owned by this `fixalloc` - residing in `list`)

	stat *sysMemStat

	zero bool // zero-out allocations or not
}

// A generic linked list of ~16KB blocks (typically the block is bigger than sizeof(mlink{})).
// TODO!!!!! Since assignments to mlink.next will result in a write barrier being performed
// this cannot be used by some of the internal GC structures. For example when
// the sweeper is placing an unmarked object on the free list it does not want the
// write barrier to be called since that could result in the object being reachable.
type mlink struct {
	_    NotInHeap
	next *mlink
}

func (f *fixalloc) init(size uintptr, first func(arg, p unsafe.Pointer), arg unsafe.Pointer, stat *sysMemStat) {
	if size > _FixAllocChunk {
		throw("runtime: fixalloc size too large")
	}

	if min := unsafe.Sizeof(mlink{}); size < min {
		size = min
	}

	f.size = size // size of future fixed off-heap allocations
	f.first = first
	f.arg = arg

	f.list = nil                                    // at the start not backed by any memory at all (~16KB chunks linked list)
	f.chunk = 0                                     // at the start not backed by any memory at all (~16KB chunks linked list)
	f.nchunk = 0                                    // at the start not backed by any memory at all (~16KB chunks linked list)
	f.nalloc = uint32(_FixAllocChunk / size * size) // Round _FixAllocChunk down to an exact multiple of `f.size` to eliminate tail waste

	f.inuse = 0 // at the start not backed by any memory at all (~16KB chunks linked list)
	f.stat = stat
	f.zero = true
}

func (f *fixalloc) alloc() unsafe.Pointer {
	if f.size == 0 {
		print("runtime: use of FixAlloc_Alloc before FixAlloc_Init\n")
		throw("runtime: internal error")
	}

	if f.list != nil { // at least one `f.size`d blocke was freed
		v := unsafe.Pointer(f.list) // get pointer to the last (first? TODO!!!!!) `f.size`d freed block
		f.list = f.list.next        // push the next `f.sized` freed block to the head of the list (if any) for later reuser
		f.inuse += f.size           // update total in-use bytes
		if f.zero {
			memclrNoHeapPointers(v, f.size)
		}
		return v
	}

	/* no freed `f.size`d blocks */
	if uintptr(f.nchunk) < f.size { // if active ~16KB chunks cannot accomodate the new allocation request
		// request a ~16KB chunk pointer-size-aligned from persistent alloc,
		// which may in turn go to the OS to mmap another 256KB chunk,
		// if the current one cannot accomodate another ~16KB.
		// After that the pointer to the previous chunk is lost and it's memory
		// will be heald in `f.size`d free blocks when freed in `f.list`
		f.chunk = uintptr(persistentalloc(uintptr(f.nalloc), 0, f.stat))
		f.nchunk = f.nalloc // update the number of bytes remaining in the current chunk to be ~16KB, because a fresh chunk was obtained from persistentalloc
	}

	v := unsafe.Pointer(f.chunk) // get pointer into current position with the ~16KB cached underlying chunk
	if f.first != nil {
		f.first(f.arg, v)
	}
	f.chunk = f.chunk + f.size // move pointer up to slice out a fixed `f.size`d block to return to the user
	f.nchunk -= uint32(f.size) // update bytes remaining in the current ~16KB underlying chunk
	f.inuse += f.size          // update amount of memory in use by this persistent chunk across all ~16KB chunks
	return v                   // return pointer to the start of the sliced out fixed size block
}

func (f *fixalloc) free(p unsafe.Pointer) {
	f.inuse -= f.size // update amount of memory in use by this persistent chunk across all ~16KB chunks
	v := (*mlink)(p)
	v.next = f.list // release fixed size chunk into the head of the linked list of freed chunks for later reuse
	f.list = v
}
