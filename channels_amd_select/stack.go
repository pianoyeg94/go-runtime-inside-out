package channels

const (
	// // The stack guard is a pointer this many bytes above the
	// bottom of the stack.
	//
	// The guard leaves enough room for one _StackSmall frame (128 bytes) plus
	// a _StackLimit chain of NOSPLIT calls (800 bytes)
	_StackGuard = 928 // TODO!!!!!

	// Goroutine preemption request.
	// 0xfffffade in hex.
	stackPreemt = uintPtrMask & -1314
)

const (
	uintPtrMask = 1<<(8*PtrSize) - 1 // on 64-bit systems will be a full run of 64 ones
)
