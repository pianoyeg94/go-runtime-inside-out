// src/runtime/asm_amd64.s

/*
 *  go-routine
 */

// func gogo(buf *gobuf)
// restore state from Gobuf; longjmp
TEXT runtime·gogo(SB), NOSPLIT, $0-8 // 0 byte stack frame, 8 byte argument pointer to goroutine's `.gobuf` which contains all the neccessary state 
    MOVQ    buf+0(FP), BX   // gobuf
    MOVQ    gobuf_g(BX), DX // store goroutine pointer in DX
    MOVQ    0(DX), CX       // make sure g != nil
    JMP gogo<>(SB)          // linker knows the offset of the `gogo<>` symbol relative to the virtual SB register

TEXT gogo<>(SB), NOSPLIT, $0 // 0 byte stack frame and no arguments
    // The runtime pointer to the g structure is maintained through the value of an otherwise unused (as far as Go is concerned) 
    // register in the MMU. 
    // In the runtime package, assembly code can include go_tls.h, which defines an OS- and architecture-dependent macro 
    // get_tls for accessing this register.
    // The get_tls macro takes one argument, which is the register to load the g pointer into.
    // '''
    //  #define	get_tls(r)	MOVQ TLS, r - here TLS is an alias for a -4 offset from the 
                                        // fs register which points to the memory reserved for TLS
    //  #define	g(r)	0(r)(TLS*1)
    // '''
    // https://chao-tic.github.io/blog/2018/12/25/tls#the-initialisation-of-tcb-or-tls
    // https://stackoverflow.com/questions/6611346/how-are-the-fs-gs-registers-used-in-linux-amd64
    // https://uclibc.org/docs/tls.pdf
    get_tls(CX)                       // make CX to point to TLS area (using the FS register and a self offset of -4)
    MOVQ    DX, g(CX)                 // store previously deduced goroutine pointer from goroutine's `.gobuf` passed into `runtime·gogo` in the TLS area
    MOVQ    DX, R14                   // set the g register
    MOVQ    gobuf_sp(BX), SP          // restore SP
    MOVQ    gobuf_ret(BX), AX         // restore top-level function's (executed by the goroutine) return address into AX
    MOVQ    gobuf_ctxt(BX), DX        // restore goroutine's context into DX
    MOVQ    gobuf_bp(BX), BP          // restore goroutine's base pointer into BP (x86-64 is a framepointer-enabled architecture)
    MOVQ	$0, gobuf_sp(BX)	      // clear to help garbage collector
	MOVQ	$0, gobuf_ret(BX)
	MOVQ	$0, gobuf_ctxt(BX)
	MOVQ	$0, gobuf_bp(BX)
    MOVQ    gobuf_pc(BX), BX          // restore goroutine's next instruction to execute
    JMP BX                            // continue executing goroutine's next instruction

// func mcall(fn func(*g))
// Switch to m->g0's stack, call fn(g).
// Fn must never return. It should gogo(&g->sched)
// to keep running g.
// The TEXT directive declares the symbol runtime·mcall and the instructions 
// that follow form the body of the function.
// SB: Static base pointer: global symbols. - linker will now this function's
// offset from the static base pointer.
// <ABIInternal> annotation specifies that this function follows golang's new ABI
// NOSPLIT - since the frame is 0 bytes, there's no need to include a stack overflow check.
TEXT runtime·mcall<ABIInternal>(SB), NOSPLIT, $0-8 // 0 byte stack frame, 8 byte argument pointer to func
    // save fn pointer in BX
    // ========================================================================================================
    // AX is the register through which the first function argument is passed, so move fn to DX.
    // DX at call time holds the "Closure context pointer", on the other hand when the body of a function is
    // executing it's a scratch register.
    MOVQ    AX, DX  // DX = fn


    // save state in g->sched
    // ========================================================================================================
    // move caller's PC into BX (basically PC of the goroutine that yielded control)
    // in case of gopark PC should point to the return instruction from the gopark call
    MOVQ 0(SP), BX                   // caller's PC
    MOVQ BX, (g_sched+gobuf_pc)(R14) // set g's .sched<gobuf>.pc to the PC on which this goroutine yielded
    LEAQ fn+0(FP), BX ;              // move caller's SP into BX

    // set g's .sched<gobuf>.sp to the stack pointer before it yielded.
    // basically saving all local variables
    MOVQ BX, (g_sched+gobuf_sp)(R14) // set g's .sched<gobuf>.sp to the stack pointer before it yielded

   
    // While BP points to the current stack frame, SP points to the top of the stack. Because the compiler 
    // knows the difference between BP and SP at any point within the function, it is free to use either one 
    // as the base for the local variables.
    // In golang BP is callee-saved. The assembler automatically inserts BP save/restore when frame size 
    // is larger than zero.
    // A stack frame is just the local function's playground: the region of stack the current function uses.
    // Although a language can do well without using stack frames and be even faster, most languages, 
    // including golang need stack frames for "unwinding" (for example, to "throw exceptions/panics" to 
    // an ancestor caller of the current function); i.e. to "unwind" stack frames that one or more functions 
    // can be aborted and control passed to some ancestor function, without leaving unneeded stuff on the stack.

    // Using BP as a general purpose register is allowed, however it can interfere with sampling-based profiling.

    // set g's .sched<gobuf>.bp to the base pointer before it yielded (points to this g's current stack frame)
    MOVQ BP, (g_sched+gobuf_bp)(R14) 


    // switch to m->g0 & its stack, call fn
    // ========================================================================================================
    MOVQ g_m(R14), BX        // get the M pointer from the yielding G and store it in BX
    MOVQ m_g0(BX), SI        // SI = g.m.g0, put pointer to M's g0 goroutine into SI
    CMPQ SI, R14             // if g == m->g0 call badmcall, otherwise proceed to the goodm: label
    JNE goodm
    JMP runtime·badmcall(SB) // throws "runtime: mcall function returned"
goodm:
    MOVQ R14, AX             // AX (and arg 0) = g, make yielding G the argument to the function to be called on g0's stack
    MOVQ SI, R14             // g = g.m.g0, means current g is g0

    // The runtime pointer to the g structure is maintained through the value of an otherwise unused (as far as Go is concerned) 
    // register in the MMU. 
    // In the runtime package, assembly code can include go_tls.h, which defines an OS- and architecture-dependent macro 
    // get_tls for accessing this register.
    // The get_tls macro takes one argument, which is the register to load the g pointer into.
    // '''
    //  #define	get_tls(r)	MOVQ TLS, r - here TLS is an alias for a -4 offset from the 
                                        // fs register which points to the memory reserved for TLS
    //  #define	g(r)	0(r)(TLS*1)
    // '''
    // https://chao-tic.github.io/blog/2018/12/25/tls#the-initialisation-of-tcb-or-tls
    // https://stackoverflow.com/questions/6611346/how-are-the-fs-gs-registers-used-in-linux-amd64
    // https://uclibc.org/docs/tls.pdf
    get_tls(CX)                       // make CX to point to TLS area (using the FS register and a self offset of -4)
    MOVQ R14, g(CX)                   // Set G in TLS
    MOVQ (g_sched+gobuf_sp)(R14), SP  // sp = g0.sched.sp, set out stack pointer to point to g0's top off the stack
    PUSHQ AX                          // open up space for fn's arg spill slot, AX may be clobbered during the call of the passed in func
    MOVQ 0(DX), R12                   // move function pointer passed in to R12
    CALL R12                          // fn(g), call passed in function on g0s stack passing it the goroutine that called us
    // fn should not return
    // --------------------
    POPQ AX                           // restore yielded goroutine into AX register to pass it to 
    JMP runtime·badmcall2(SB)         // throw("runtime: mcall function returned")
    RET

// systemstack_switch is a dummy routine that systemstack leaves at the bottom
// of the G stack. We need to distinguish the routine that
// lives at the bottom of the G stack from the one that lives
// at the top of the system stack because the one at the top of
// the system stack terminates the stack walk (see topofstack()).
TEXT runtime·systemstack_switch(SB), NOSPLIT, $0-0 // 0 byte stack frame and no arguments
	RET

// func systemstack(fn func())
// 
// The TEXT directive declares the symbol runtime·systemstack and the instructions 
// that follow form the body of the function.
// SB: Static base pointer: global symbols. - linker will now this function's
// offset from the static base pointer.
// NOSPLIT - since the frame is 0 bytes, there's no need to include a stack overflow check.
// Is an ABI0 call, func argument is passed by stack
// 
// The FP pseudo-register is a virtual frame pointer used to refer to function arguments. 
// The compilers maintain a virtual frame pointer and refer to the arguments on the stack 
// as offsets from that pseudo-register. Thus 0(FP) is the first argument to the function, 
// 8(FP) is the second (on a 64-bit machine), and so on. However, when referring to a 
// function argument this way, it is necessary to place a name at the beginning, 
// as in first_arg+0(FP) and second_arg+8(FP). (The meaning of the offset—offset from 
// the frame pointer—distinct from its use with SB, where it is an offset from the symbol.) 
// The assembler enforces this convention, rejecting plain 0(FP) and 8(FP). 
// The actual name is semantically irrelevant but should be used to document the argument's name. 
// It is worth stressing that FP is always a pseudo-register, not a hardware register, 
// even on architectures with a hardware frame pointer.
// Presumably on amd64 FP = RBP = RSP = RBP + 8 (+8 because the return address is on top of the stack).
TEXT runtime·systemstack(SB), NOSPLIT, $0-8 // 0 byte stack frame, 8 byte argument pointer to func
    MOVQ    fn+0(FP), DI                // basically get func() argument from stack to DI
    get_tls(CX)                         // make CX to point to TLS area (using the FS register and a self offset of -4 under the hood)
    MOVQ    g(CX), AX                   // AX = g
    MOVQ    g_m(AX), BX                 // BX = m

    CMPQ	AX, m_gsignal(BX)           // if current g is curren't M's signal handling g, already on system stack, no need to switch, tail call passed in function
    JEQ	noswitch

    MOVQ    m_go(BX), DX                // DX = g0
    CMPQ    AX, DX                      // if already on g0 stack, no need to switch to system stack, tail call passed in function
    JMP noswitch

    CMPQ    AX, m_curg(BX)              // current g is not currently executing on M
    jmp bad

    // switch stacks
	// save our state in g->sched. Pretend to
	// be systemstack_switch if the G stack is scanned.
    // Check out gosave_systemstack_switch for more details.
    CALL gosave_systemstack_switch<>(SB)

    // switch to g0
    MOVQ    DX, g(CX)                   // store g0 in TLS, because CX is currently pointing at the TLS area
    MOVQ    DX, R14                     // set the g register to g0
    // restore g0's stack pointer
    // can clobber M in BX, because it can later be retrieved through g0's M reference
    MOVQ    (g_sched+go_buf_sp)(DX), BX  
    MOVQ    BX, SP

    // call target function
    MOVQ    DI, DX                      // TODO !!!!!!!!!! why move func to DX
    MOVQ    0(DI), DI                   // looks like a no-op TODO !!!!!!!!!!
    CALL    DI                          // call target function

    // switch back to g
    get_tls(CX)                         // make CX to point to TLS area (using the FS register and a self offset of -4 under the hood)
    MOVQ    g(CX), AX                   // move g0 into AX, so that we can get the current M back into BX
    MOVQ    g_m(AX), BX                 // restore M from g0 into BX
    MOVQ    m_curg(BX), AX              // move the original G into AX ??????
    MOVQ    AX, g(CX)                   // restore original G in TLS (CX is currently pointing to the TLS area)
    MOVQ    (g_sched+gobuf_sp)(AX), SP  // restore g's stack pointer
    RET                                 // will return and g will continue to PC after this call to systemstack()

noswitch:
    // already on m stack; tail call the function.
	// Using a tail call here cleans up tracebacks since we won't stop
	// at an intermediate systemstack.
    // 
    // TCO (tail call optimization) turns a function call 
    // in tail position into a goto, a jump.
    // Tail-call optimization is where you are able to avoid allocating a new stack frame 
    // for a function because the calling function will simply return the value that 
    // it gets from the called function.
    // TCO (Tail Call Optimization) is the process by which a smart compiler can make a call 
    // to a function and take no additional stack space. The only situation in which this happens
    // is if the last instruction executed in a function f is a call to a function g (Note: g can be f). 
    // The key here is that f no longer needs stack space - it simply calls g and then returns 
    // whatever g would return. In this case the optimization can be made that g just runs and 
    // returns whatever value it would have to the thing that called f (in this case nothing)
    // https://stackoverflow.com/questions/310974/what-is-tail-call-optimization
    MOVQ DI, DX    // DX = fn func()
    MOVQ 0(DI), DI // move pointer to function to DI
    JMP DI         // tail call (already on systemstack, reuse it)

bad:
    // Bad: g is not gsignal, not g0, not curg. What is it?
    MOVQ $runtime·badsystemstack(SB), AX  // moves func to AX
    CALL AX // writes "fatal: systemstack called from unexpected goroutine" error to stderr
    // call interrupt at index 3 from the interrupt descriptor table, will generate a SIGTRAP.
    // The default behaviour of SIGTRAP is to terminate the process and dump core.
    INT	$3  

// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
TEXT runtime·procyield(SB),NOSPLIT,$0-0 // no stack frame and no argument passed by stack (argument passed in by register AX)
    MOVL    cycles+0(FP), AX  // for old ABI argument is passed by stack, put in the AX register to conform with the new ABI
again:
    // PAUSE notifies the CPU that this is a spinlock wait loop 
    // so memory and cache accesses may be optimized.
    // PAUSE may actually stop CPU for some time to save power. 
    // Older CPUs decode it as REP NOP, so you don't have to check if its supported. 
    // Older CPUs will simply do nothing (NOP) as fast as possible.
    // 
    // The intel manual and other information available state that:
    //     The processor uses this hint to avoid the memory order violation in most situations, 
    //     which greatly improves processor performance. 
    //     For this reason, it is recommended that a PAUSE instruction be placed 
    //     in all spin-wait loops. An additional function of the PAUSE instruction 
    //     is to reduce the power consumed by Intel processors.
    // 
    // https://stackoverflow.com/questions/12894078/what-is-the-purpose-of-the-pause-instruction-in-x86
    // 
    // https://stackoverflow.com/questions/4725676/how-does-x86-pause-instruction-work-in-spinlock-and-can-it-be-used-in-other-sc
    PAUSE 
    SUBL    $1, AX            // loop calling PAUSE until the `cycles` param is exhausted (becomes 0)
    JNZ again
    RET


// Save state of caller into g->sched,
// but using fake PC from systemstack_switch.
// Must only be called from functions with no locals ($0)
// or else unwinding from systemstack_switch is incorrect.
// Smashes R9.
TEXT gosave_systemstack_switch<>(SB),NOSPLIT, $0 // 0 byte stack frame and no arguments
    MOVQ	$runtime·systemstack_switch(SB), R9  
    // Pretend to be systemstack_switch if the G stack is scanned.
    // g's PC now points to address of the dummy function $runtime·systemstack_switch, which just performs a return.
    // So no local variables and pointers from the stack. This means that when a G is in a systemstack() call,
    // if a GC cycle happens at that point, it will not scan it's stack during this cycle.
    // TODO !!!!!!!!!!!!!!!!!!!!! Why cannot GC scan a G's stack during a systemstack() call?
	MOVQ	R9, (g_sched+gobuf_pc)(R14)          

    // move current g's stack pointer ommitting the func() argument 
    // to runtime·systemstack to which SP is currently pointing into R9
    LEAQ    8(SP), R9                    
    MOVQ    R9, (g_sched+gobuf_sp)(R14)          // set current g's .sched<gobuf>.sp to its stack pointer before switching to g0's system stack
    MOVQ    $0, (g_sched+gobuf_ret)(R14)         // set current g's .sched<gobuf>.ret addr to 0 before switching to g0's system stack, TODO!!!!!!!!!!
    MOVQ    BP, (go_sched+gobuf_bp)(R14)         // set current g's .sched<gobuf>.bp to its base pointer before switching to g0's system stack

    // Assert ctxt is zero. See func save. TODO!!!!! proc.go:func save(pc, sp uintptr) {}
    MOVQ    (g_sched+gobuf_ctxt)(R14), R9
    // https://en.wikipedia.org/wiki/TEST_(x86_instruction)
    TESTQ   R9, R9                               // the TESTQ instruction performs a bitwise AND on two operands, set ZF to 1 if R9 == 0
    JZ  2(PC)                                    // if ctxt is zero, jump straight to RET, else call abort()

    // calls interrupt at index 3 from the interrupt descriptor table, will generate a SIGTRAP.
    // The default behaviour of SIGTRAP is to terminate the process and dump core.
    // if SIGTRAP signal handler is overriden by user, so that it doesn't terminate the process, enters an infinite loop,
    // doing nothing and wasting CPU cycles
    CALL    runtime·abort(SB)

    RET

TEXT runtime·abort(SB),NOSPLIT,$0-0
    // call interrupt at index 3 from the interrupt descriptor table, will generate a SIGTRAP.
    // The default behaviour of SIGTRAP is to terminate the process and dump core.
    INT	$3
loop: 
    JMP loop // if SIGTRAP signal handler is overriden by user, so that it doesn't terminate the process, enter an infinite loop
