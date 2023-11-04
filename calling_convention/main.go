package main

func main() {
	EmptyFunc()
	FuncConst()
	FuncConstMultiple()
	a := FuncAdd(243253536346, 3, 5, 56, 67, 45, 1, 14, 456, 1000)
	_ = FuncAdd(243253536346, 3, 5, 56, 67, a, 1, 14, 456, 1000)
	_ = DoCallAdd()
}

// https://go.googlesource.com/go/+/refs/heads/dev.regabi/src/cmd/compile/internal-abi.md
// https://dr-knz.net/go-calling-convention-x86-64.html

//go:noinline
func EmptyFunc() {
}

//go:noinline
func FuncConst() int {
	// TEXT main.FuncConst(SB)
	//   main.go:13            0x4553e0                b87b000000              MOVL $0x7b, AX
	//   main.go:13            0x4553e5                c3                      RET
	return 123
}

//go:noinline
func FuncConstMultiple() (int, int, int, int, int, int, int, int, int, int, int, int, int) {
	// 	TEXT main.FuncConstMultiple(SB)
	//   main.go:27     0x455400     48c74424084e000000     MOVQ $0x4e, 0x8(SP)     movq $0x4e,0x8(%rsp)
	//   main.go:27     0x455409     48c744241022000000     MOVQ $0x22, 0x10(SP)    movq $0x22,0x10(%rsp)
	//   main.go:27     0x455412     48c744241823000000     MOVQ $0x23, 0x18(SP)    movq $0x23,0x18(%rsp)
	//   main.go:27     0x45541b     48c744242022000000     MOVQ $0x22, 0x20(SP)    movq $0x22,0x20(%rsp)
	//   main.go:27     0x455424     b87b000000             MOVL $0x7b, AX          mov $0x7b,%eax
	//   main.go:27     0x455429     bbfe000000             MOVL $0xfe, BX          mov $0xfe,%ebx
	//   main.go:27     0x45542e     b90c000000             MOVL $0xc, CX           mov $0xc,%ecx
	//   main.go:27     0x455433     bf0e000000             MOVL $0xe, DI           mov $0xe,%edi
	//   main.go:27     0x455438     be00010000             MOVL $0x100, SI         mov $0x100,%esi
	//   main.go:27     0x45543d     41b8ea000000           MOVL $0xea, R8          mov $0xea,%r8d
	//   main.go:27     0x455443     41b922000000           MOVL $0x22, R9          mov $0x22,%r9d
	//   main.go:27     0x455449     41ba38000000           R10                     mov $0x38,%r10d
	//   main.go:27     0x45544f     41bb2d000000           MOVL $0x2d, R11         mov $0x2d,%r11d
	//   main.go:27     0x455455     c3                     RET                     retq

	// The first 9 values are returned via registers (rax, rbx, rcx, rdi, rsi, r8, r9, r10, r11)
	// If there are more than 9 return values they will be returned on the goroutine stack,
	// the 10th return value will be on top (lower in memory) and so on

	return 123, 254, 12, 14, 256, 234, 34, 56, 45, 78, 34, 35, 34
}

//go:noinline
func FuncAdd(a, b, c, d, e, f, g, h, i, j int) int {
	// TEXT main.FuncAdd(SB)
	// main.go:67     0x455500     4801d8         ADDQ BX, AX          add %rbx,%rax
	// main.go:67     0x455503     4829c8         SUBQ CX, AX          sub %rcx,%rax
	// main.go:67     0x455506     4829f8         SUBQ DI, AX          sub %rdi,%rax
	// main.go:67     0x455509     4829f0         SUBQ SI, AX          sub %rsi,%rax
	// main.go:67     0x45550c     4c29c0         SUBQ R8, AX          sub %r8,%rax
	// main.go:67     0x45550f     4c29c8         SUBQ R9, AX          sub %r9,%rax
	// main.go:67     0x455512     4c29d0         SUBQ R10, AX         sub %r10,%rax
	// main.go:67     0x455515     4c29d8         SUBQ R11, AX         sub %r11,%rax
	// main.go:67     0x455518     488b4c2408     MOVQ 0x8(SP), CX     mov 0x8(%rsp),%rcx
	// main.go:67     0x45551d     4829c8         SUBQ CX, AX          sub %rcx,%rax
	// main.go:67     0x455520     c3             RET                  retq

	// The first 9 values are passed in via registers (rax, rbx, rcx, rdi, rsi, r8, r9, r10, r11)
	// The remaining arguments are pushed onto the goroutine stack starting with the last argument,
	// so that the 10th argument is on top of the stack (lower in memory)

	return a + b - c - d - e - f - g - h - i - j
}

//go:noinline
func DoCallAdd() int { return FuncAdd(1, 2, 3, 4, 5, 6, 7, 8, 9, 10) }
