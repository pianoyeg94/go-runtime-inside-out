// go/src/runtime/type.go
package channels

import "unsafe"

// tflag is documented in reflect/type.go.
//
//	tflag is used by an rtype to signal what extra type information is
//	available in the memory directly following the rtype value.
//
// tflag values must be kept in sync with copies in:
//
//	cmd/compile/internal/reflectdata/reflect.go
//	cmd/link/internal/ld/decodesym.go
//	reflect/type.go
//	internal/reflectlite/type.go
type tflag uint8

// Needs to be in sync with ../cmd/link/internal/ld/decodesym.go:/^func.commonsize,
// ../cmd/compile/internal/reflectdata/reflect.go:/^func.dcommontype and
// ../reflect/type.go:/^type.rtype.
// ../internal/reflectlite/type.go:/^type.rtype.
type _type struct {
	// size is the number of words in the object,
	// and ptrdata is the number of words in the prefix
	// of the object that contains pointers. That is, the final
	// size - ptrdata words contain no pointers.
	// If ptrdata is 0 - objects of this type do not contain pointers.
	size    uintptr // number of words in the object
	ptrdata uintptr // size of memory prefix holding all pointers

	hash       uint32
	tflag      tflag
	align      uint8
	fieldAlign uint8
	kind       uint8

	// function for comparing objects of this type
	// (ptr to object A, ptr to object B) -> ==?
	equal func(unsafe.Pointer, unsafe.Pointer) bool

	// gcdata stores the GC type data for the garbage collector.
	// If the KindGCProg bit is set in kind, gcdata is a GC program.
	// Otherwise it is a ptrmask bitmap. See mbitmap.go for details.
	gcdata *byte
}

type chantype struct {
	typ  _type
	elem *_type
	dir  uintptr // TODO:
}
