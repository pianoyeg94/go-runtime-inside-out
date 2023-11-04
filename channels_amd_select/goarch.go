package channels

// PtrSize is the size of a pointer in bytes - unsafe.Sizeof(uintptr(0)) but as an ideal constant.
// It is also the size of the machine's native word size (that is, 4 on 32-bit systems, 8 on 64-bit).
//
// ^uintptr(0) = 32 set bits on 32-bit systems, and 64 set bits on 64-bit systems.
// On 32-bit systems `(^uintptr(0) >> 63)` - will yield 0, so PtrSize will remain 4.
// On 64-bit systems `(^uintptr(0) >> 63)` - will yield 1, so PtrSize will become 8.
const PtrSize = 4 << (^uintptr(0) >> 63)
