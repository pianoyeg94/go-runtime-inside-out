// src/runtime/vdso_linux.go

package channels

// Look up symbols in the Linux vDSO.

// This code was originally based on the sample Linux vDSO parser at
// https://git.kernel.org/cgit/linux/kernel/git/torvalds/linux.git/tree/tools/testing/selftests/vDSO/parse_vdso.c

// This implements the ELF dynamic linking spec at
// http://sco.com/developers/gabi/latest/ch5.dynamic.html

// The version section is documented at
// https://refspecs.linuxfoundation.org/LSB_3.2.0/LSB-Core-generic/LSB-Core-generic/symversion.html

type vdsoSymbolKey struct {
	name    string
	symHash uint32
	gnuHash uint32
	ptr     *uintptr
}

type vdsoVersionKey struct {
	version string
	verHash uint32
}
