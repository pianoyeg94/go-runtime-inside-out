// go/src/runtime/internal/atomic/atomic_amd64.go
package atomic

import "unsafe"

// Export some functions via linkname to assembly in sync/atomic.
//
//go:linkname Load
//go:linkname Loadp
//go:linkname Load64

//go:nosplit
//go:noinline
func Load(ptr *uint32) uint32 {
	return *ptr
}

//go:nosplit
//go:noinline
func Loadp(ptr unsafe.Pointer) unsafe.Pointer {
	return *(*unsafe.Pointer)(ptr)
}

//go:nosplit
//go:noinline
func Load64(ptr *uint64) uint64 {
	return *ptr
}

//go:nosplit
//go:noinline
func LoadAcq(ptr *uint32) uint32 {
	return *ptr
}

//go:nosplit
//go:noinline
func LoadAcq64(ptr *uint64) uint64 {
	return *ptr
}

//go:nosplit
//go:noinline
func LoadAcquintptr(ptr *uintptr) uintptr {
	return *ptr
}

//go:noescape
func Xadd(ptr *uint32, delta int32) uint32

//go:noescape
func Xadd64(ptr *uint64, delta int64) uint64

//go:noescape
func Xadduintptr(ptr *uintptr, delta uintptr) uintptr

//go:noescape
func Xchg(ptr *uint32, new uint32) uint32

//go:noescape
func Xchg64(ptr *uint64, new uint64) uint64

//go:noescape
func Xchguintptr(ptr *uintptr, new uintptr) uintptr

//go:nosplit
//go:noinline
func Load8(ptr *uint8) uint8 {
	return *ptr
}

//go:noescape
func And8(ptr *uint8, val uint8)

//go:noescape
func Or8(ptr *uint8, val uint8)

//go:noescape
func And(ptr *uint32, val uint32)

//go:noescape
func Or(ptr *uint32, val uint32)

// NOTE: Do not add atomicxor8 (XOR is not idempotent).

//go:noescape
func Cas64(ptr *uint64, old, new uint64) bool

//go:noescape
func CasRel(ptr *uint32, old, new uint32) bool

//go:noescape
func Store(ptr *uint32, val uint32)

//go:noescape
func Store8(ptr *uint8, val uint8)

//go:noescape
func Store64(ptr *uint64, val uint64)

//go:noescape
func StoreRel(ptr *uint32, val uint32)

//go:noescape
func StoreRel64(ptr *uint64, val uint64)

//go:noescape
func StoreReluintptr(ptr *uintptr, val uintptr)

// StorepNoWB performs *ptr = val atomically and without a write
// barrier.
//
// NO go:noescape annotation; see atomic_pointer.go.
//
// This function cannot have go:noescape annotations,
// because while ptr does not escape, val does.
// If val is marked as not escaping, the compiler will make incorrect
// escape analysis decisions about the pointer value being stored.
func StorepNoWB(ptr unsafe.Pointer, val unsafe.Pointer)
