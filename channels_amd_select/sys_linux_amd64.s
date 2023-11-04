// src/runtime/sys_linux_amd64.s

// About vDSO: https://github.com/0xAX/linux-insides/blob/master/SysCall/linux-syscall-3.md
// 
// Returns nanoseconds (CLOCK_MONOTONIC).
// 
// CLOCK_MONOTONIC
//      A nonsettable system-wide clock that represents monotonic
//      time since—as described by POSIX—"some unspecified point
//      in the past".  On Linux, that point corresponds to the
//      number of seconds that the system has been running since
//      it was booted. 
//      
//      The CLOCK_MONOTONIC clock is not affected by discontinuous
//      jumps in the system time (e.g., if the system
//      administrator manually changes the clock), but is affected
//      by the incremental adjustments performed by adjtime(3) and
//      NTP.  This clock does not count time that the system is
//      suspended.  All CLOCK_MONOTONIC variants guarantee that
//      the time returned by consecutive calls will not go
//      backwards, but successive calls may—depending on the
//      architecture—return identical (not-increased) time values.
// 
// func nanotime1() int64
TEXT runtime·nanotime1(SB),NOSPLIT,$16-8 // 16-byte frame, 8-byte argument (TODO!!!!! why, there's no argument in the function prototype?)
    // We don't know how much stack space the VDSO code will need,
	// so switch to g0.
	// In particular, a kernel configured with CONFIG_OPTIMIZE_INLINING=n and hardening TODO!!!!! 
    // can use a full page of stack space in gettime_sym
	// due to stack probes inserted to avoid stack/heap collisions.
	// See issue #20427.

    MOVQ	SP, R12	                            // Save old SP; R12 unchanged by C code.
    MOVQ    g_m(R14), BX                        // BX unchanged by C code.

    // TODO!!!!! For assembly functions with Go prototypes, go vet will check that the argument names and offsets match. 
    // On 32-bit systems, the low and high 32 bits of a 64-bit value are distinguished by adding a _lo or _hi suffix to the name, 
    // as in arg_lo+0(FP) or arg_hi+4(FP). If a Go prototype does not name its result, the expected assembly name is ret.
    //  The SP pseudo-register is a virtual stack pointer used to refer to frame-local variables and the arguments being prepared for function calls. 
    // It points to the highest address within the local stack frame, so references should use negative offsets in 
    // the range [−framesize, 0): x-8(SP), y-4(SP), and so on.
    // On architectures with a hardware register named SP, the name prefix distinguishes references to the virtual stack pointer from 
    // references to the architectural SP register. That is, x-8(SP) and -8(SP) are different memory locations: 
    // the first refers to the virtual stack pointer pseudo-register, while the second refers to the hardware's SP register.

    // Set vdsoPC and vdsoSP for SIGPROF traceback. TODO!!!!! for SIGPROF traceback?
	// Save the old values on stack and restore them on exit,
	// so this function is reentrant (https://www.geeksforgeeks.org/reentrant-function/ , https://stackoverflow.com/questions/34758863/what-is-reentrant-function-in-c)
    MOVQ    m_vdsoPC(BX), CX                    // TODO!!!!! when not in a vDSO symbol call is 0?
    MOVQ    m_vdsoSP(BX), DX                    // TODO!!!!! when not in a vDSO symbol call is 0?
    MOVQ    CX, 0(SP)                           // save on stack (top of stack for this 16-byte frame)
    MOVQ	DX, 8(SP)                           // save on stack (bottom of stack for this 16-byte frame, 8(SP) = SP + 8)

    
    LEAQ    ret+0(FP), DX                       // get old SP
    MOVQ    -8(DX), CX                          // get return address of the nanotime1 stub
    // save return address of the nanotime1 stub, so that SIGPROF traceback observers this as the PC (rip)
    MOVQ    CX, m_vdsoPC(BX)
    // Save old SP for SIGPROF traceback (current running g's or g0's if nanotime1 called from g0)
    MOVQ    DX, m_vdsoSP(BX)

    CMPQ    R14, m_curg(BX)                     // Only switch if on curg (not g0).
    JNE

    // we're on a running g, switch to g0 stack
    MOVQ    m_g0(BX), DX
    MOVQ    (g_sched+gobuf_sp)(DX), SP          // Set SP to g0 stack

// get __vdso_clock_gettime symbol from linux-vdso.so.1 shared object and call it (the same spec as for the fallback described bellow)
// or fallback to the real system call SYS_CLOCK_GETTIME (CLOCK_GETTIME (#228), https://man7.org/linux/man-pages/man2/clock_gettime.2.html)
// vDSO is available in linux kernel since version 2.6
noswitch:
    // Space for results (`struct timespec *tp` with two 8 byte members):
    // struct timespec {
    //    time_t   tv_sec;        /* seconds */
    //    long     tv_nsec;       /* nanoseconds */
    // };
    SUBQ    $16, SP                             // `tv_sec` on top of function stack, `tv_nsec` just bellow
    ANDQ    $~15, SP                            // Align stack pointer by 16 for C code (required by System V Application Binary Interface AMD64) 
                                                //                                       https://stackoverflow.com/questions/672461/what-is-stack-alignment
                                                //                                       https://stackoverflow.com/questions/4175281/what-does-it-mean-to-align-the-stack
                                                //                                       https://www.isabekov.pro/stack-alignment-when-mixing-asm-and-c-code/
                                                // 
                                                // The point of this is that there are some "SIMD" (Single Instruction, Multiple Data) instructions 
                                                // (also known in x86-land as "SSE" for "Streaming SIMD Extensions") which can perform parallel operations 
                                                // on multiple words in memory, but require those multiple words to be a block starting at an address which 
                                                // is a multiple of 16 bytes.
                                                // 
                                                // In general, the compiler can't assume that particular offsets from RSP will result in a suitable address 
                                                // (because the state of RSP on entry to the function depends on the calling code). But, by deliberately 
                                                // aligning the stack pointer in this way, the compiler knows that adding any multiple of 16 bytes to 
                                                // the stack pointer will result in a 16-byte aligned address, which is safe for use with these SIMD 
                                                // instructions.
                                                // 
                                                // The value of RSP is 0x7fffffffdc68 which is not a multiple of 16 because the value of RSP was decreased 
                                                // by 8 bytes after pushing RBX to the stack before calling printf function in the modified version of the code.
                                                // This becomes crucial when instructions such as "movaps" (move packed single-precision floating-point values 
                                                // from xmm2/m128 to xmm1) require 16 byte alignment of the memory operand. Misaligned stack together with "movaps" 
                                                // instruction in the printf function cause segmentation fault.
                                                // 
                                                // For this reason, the System V Application Binary Interface AMD64 provides the stack alignment requirement in 
                                                // subsection "The Stack Frame": 
                                                // "The end of the input argument area shall be aligned on a 16 (32, if __m256 is passed on stack) byte boundary. 
                                                // In other words, the value (RSP + 8) is always a multiple of 16 (32) when control is transferred to the function 
                                                // entry point."
                                                // 
                                                // The value of RSP before calling any function has to be in the format of 0x???????????????0 where ? represents 
                                                // any 4-bit hexadecimal number. Zero value of the least significant nibble provides 16 byte alignment of the stack.

    // C calling convention - first six arguments are passed via the following registers, 
    // we need only the 2 first registers to pass in 2 arguments: rdi, rsi, rdx, rcx, r8, r9
    MOVL    $1, DI                              // CLOCK_MONOTONIC clockid argument to `int clock_gettime(clockid_t clockid, struct timespec *tp);`
    LEAQ    0(SP), SI                           // load pointer to `struct timespec *tp`, second argument (contains two 8 byte values)
    MOVQ    runtime·vdsoClockgettimeSym(SB), AX // (src/vdso_linux_amd64.go), get __vdso_clock_gettime symbol from linux-vdso.so.1 shared object
    CMPQ    AX, $0                             
    JEQ fallback                                // this version of Linux kernel doesn't support vDSO (kernel < v2.6), fallback to a real syscall
    CALL    AX                                  // call vDSO `__vdso_clock_gettime` symbol with the arguments in DI and SI
ret:
    MOVQ    0(SP), AX                           // sec, get `(struct timespec *tp)->tv_sec`
    MOVQ    8(SP), DX                           // nsec, get `(struct timespec *tp)->tv_nsec`
    MOVQ    R12, SP                             // Restore real SP, clean `struct timespec *tp` from stack
    // Restore vdsoPC, vdsoSP
	// We don't worry about being signaled between the two stores.
	// If we are not in a signal handler, we'll restore vdsoSP to 0,
	// and no one will care about vdsoPC. If we are in a signal handler,
	// we cannot receive another signal.
    MOVQ    8(SP), CX
    MOVQ    CX, m_vdsoSP(BX)
    MOVQ    0(SP), CX
    MOVQ    CX, m_vdsoPC(BX)
    // sec is in AX, nsec in DX
	// return nsec in AX
    IMULQ   $1000000000, AX                     // multiply seconds in AX by amount of nanoseconds in a second and store the result in AX
    ADDQ    DX, AX                              // add nanoseconds from DX to nanoseconds from AX and store the result in AX
    MOVQ    AX, ret+0(FP)                       // since this can be called by old and new golang ABI, return the resulting nanoseconds by stack too
    RET
fallback:                                       // do actual syscall if Linux version doesn't support vDSO (arguments in DI and SI)
    MOVQ    $SYS_clock_gettime, AX  
    SYSCALL
    JMP ret         

// func osyield()
// 
// Causes the calling thread to relinquish the CPU. 
// The thread is moved to the end of the queue 
// for its static priority and a new thread gets to run.
//   https://linux.die.net/man/2/sched_yield
//   https://www.halolinux.us/kernel-reference/the-sched-yield-system-call.html
TEXT runtime·osyield(SB),NOSPLIT,$0
    MOVL    $SYS_sched_yield, AX // syscall #24, 
    SYSCALL
    RET