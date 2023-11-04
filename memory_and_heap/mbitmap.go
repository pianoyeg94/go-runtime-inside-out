package heap

import "unsafe"

// addb returns the byte pointer p+n.
//
//go:nowritebarrier
//go:nosplit
func addb(p *byte, n uintptr) *byte {
	// Note: wrote out full expression instead of calling add(p, n)
	// to reduce the number of temporaries generated by the
	// compiler for this trivial expression during inlining.
	return (*byte)(unsafe.Pointer(uintptr(unsafe.Pointer(p)) + n))
}
