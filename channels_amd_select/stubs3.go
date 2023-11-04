// src/runtime/stubs3.go

package channels

// nanotime1 is written in assembly,
// check out sys_linux_amd64.s.
//
// Gets monotonic clock nanoseconds
// (time passed after Linux boot).
// Switches to g0 stack if neccessary,
// because on Linux kernel versions >= 2.6
// this is a vDSO symbol call
// (no switching from user-land too kernel-land and back to user-land).
// On kernel versions < 2.6 falls back to an actual syscall.
func nanotime1() int64
