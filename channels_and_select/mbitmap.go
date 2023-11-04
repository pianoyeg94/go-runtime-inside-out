package channels

// typeBitsBulkBarrier executes a write barrier for every
// pointer that would be copied from [src, src+size) to [dst,
// dst+size) by a memmove using the type bitmap to locate those
// pointer slots.
//
// The type typ must correspond exactly to [src, src+size) and [dst, dst+size).
// dst, src, and size must be pointer-aligned.
// The type typ must have a plain bitmap, not a GC program.
// The only use of this function is in channel sends, and the
// 64 kB channel element limit takes care of this for us.
//
// Must not be preempted because it typically runs right before memmove,
// and the GC must observe them as an atomic action.
//
// Callers must perform cgo checks if writeBarrier.cgo.
//
//go:nosplit
func typeBitsBulkBarrier(typ *_type, dst, src, size uintptr) {
	// TODO!!!!!
}
