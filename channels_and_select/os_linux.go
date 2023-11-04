// src/runtime/os_linux.go

package channels

// src/runtime/sys_linux_amd64.s:osyield
//
// Causes the calling thread to relinquish the CPU.
// The thread is moved to the end of the queue
// for its static priority and a new thread gets to run.
//
//	https://linux.die.net/man/2/sched_yield
//	https://www.halolinux.us/kernel-reference/the-sched-yield-system-call.html
func osyield() // TODO!!!!!
