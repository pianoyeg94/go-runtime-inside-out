// src/runtime/vdso_linux_amd64.go

package channels

var vdsoLinuxVersion = vdsoVersionKey{version: "LINUX_2.6", verHash: 0x3ae75f6}

var vdsoSymbolKeys = []vdsoSymbolKey{
	{name: "__vdso_gettimeofday", symHash: 0x315ca59, gnuHash: 0xb01bca00, ptr: &vdsoGettimeofdaySym},
	{name: "__vdso_clock_gettime", symHash: 0xd35ec75, gnuHash: 0x6e43a318, ptr: &vdsoClockgettimeSym},
}

var (
	vdsoGettimeofdaySym uintptr
	vdsoClockgettimeSym uintptr
)
