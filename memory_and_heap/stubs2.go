package heap

import "unsafe"

// return value is only set on linux to be used in osinit().
//
// Written in assembly in sys_linux_amd64.s
//
// On success,it returns zero, on error, it returns -1 and errno
// is set to indicate the error.
func madvise(addr unsafe.Pointer, n uintptr, flags int32) int32
