#define SYS_madvise		28

TEXT runtime·madvise(SB),NOSPLIT,$0
    MOVQ    addr+0(FP), DI    // move addr from parameter passed by stack into register for upcoming system call
    MOVQ    n+8(FP), SI       // move n from parameter passed by stack into register for upcoming system call
    MOVL    flags+16(FP), DX  // move flags (advice) from parameter passed by stack into register for upcoming system call
    MOVQ    $SYS_madvise, AX  // move system call number into register for upcoming system call
    SYSCALL                   // perform the madvise system call
    MOVL    AX, ret+24(FP)    // prepare result code to be returned by stack (on success,it returns zero, on error, it returns -1 and errno is set to indicate the error).
    RET