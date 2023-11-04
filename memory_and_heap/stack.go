package heap

const (
	// Goroutine preemption request.
	// 0xfffffade in hex.
	stackPreemt = uintPtrMask & -1314
)

const (
	uintPtrMask = 1<<(8*PtrSize) - 1 // on 64-bit systems will be a full run of 64 ones, used to mask off overflowing values
)
