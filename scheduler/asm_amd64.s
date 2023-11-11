#include "go_asm.h"
#include "go_tls.h"
#include "funcdata.h"
#include "textflag.h"
#include "cgo/abi_amd64.h"

/*
 *  go-routine
 */

// func gogo(buf *gobuf)
// restore state from Gobuf; longjmp
// $0-8: function doesn't have a stack frame and is called with an 8 byte argument,
// which is the pointer to g.sched.
TEXT runtime·gogo(SB), NOSPLIT, $0-8
    MOVQ    buf+0(FP), BX       // gobuf
    MOVQ    gobuf_g(BX), DX     // guintptr
    MOVQ    0(DX), CX           // make sure g != nil
    JMP     gogo<>(SB)

TEXT gogo<>(SB), NOSPLIT, $0