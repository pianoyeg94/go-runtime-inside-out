package channels

import (
	// "internal/abi"
	"unsafe"
)

const debugSelect = false

// Select case descriptor.
// Known to compiler.
// Changes here must also be made in src/cmd/compile/internal/walk/select.go's scasetype.
type scase struct {
	c    *hchan         // chan
	elem unsafe.Pointer // data element
}

var (
	chansendpc = abi.FuncPCABIInternal(chansend) // chansend function program counter
	chanrecvpc = abi.FuncPCABIInternal(chanrecv) // chanrecv function program counter
)

// block() parks the current goroutine forever
// and switches to g0 scheduling stack.
//
// Used in reflect select for select with no cases.
func block() {
	gopark(nil, nil, waitReasonSelectNoCases, traceEvGoStop, 1) // forever
}

// selectgo implements the select statement.
//
// cas0 points to an array of type [ncases]scase, and order0 points to
// an array of type [2*ncases]uint16 where ncases must be <= 65536.
// Both reside on the goroutine's stack (regardless of any escaping in
// selectgo).
//
// For race detector builds, pc0 points to an array of type
// [ncases]uintptr (also on the stack); for other builds, it's set to
// nil.
//
// selectgo returns the index of the chosen scase, which matches the
// ordinal position of its respective select{recv,send,default} call.
// Also, if the chosen scase was a receive operation, it reports whether
// a value was received.
func selectgo(cas0 *scase, order0 *uint16, pc0 *uintptr, nsends, nrecvs int, block bool) (int, bool) {
	if debugSelect {
		print("select: cas0=", cas0, "\n")
	}

	// NOTE: In order to maintain a lean stack size, the number of scases
	// is capped at 65536 (both arrays reside on caller's stack and may be actually
	// smaller, so after casting to pointers to arrays, part of them may be only backed
	// by virtual memory).
	cas1 := (*[1 << 16]scase)(unsafe.Pointer(cas0)) // cast to pointer to array of scases with length 65536 (not all backed by mapped memory)
	// cast to pointer to array of uint16 with length double the size
	// of cas1 (not all backed by mapped memory), double the size because
	// is dvidided into 2 equal parts: pollorder and lockorder arrays.
	order1 := (*[1 << 17]uint16)(unsafe.Pointer(order0))

	ncases := nsends + nrecvs // total select cases count (excluding default case if any)

	// cap cases array length and capacity to total select cases count
	// (all backed by mapped memory)
	scases := cas1[:ncases:ncases]
	// cap pollorder array length and capacity to total select cases count
	// (all backed by mapped memory), currently the array holds garbage,
	// because isn't zero-initialized by compiler.
	pollorder := order1[:ncases:ncases]
	// lockorder comes right after the pollorder array, cap length and capacity
	// to total select cases count (all backed by mapped memory), currently the
	// array holds garbage, because isn't zero-initialized by compiler.
	lockorder := order1[ncases:][:ncases:ncases]
	// NOTE: pollorder/lockorder's underlying array was not zero-initialized by compiler.

	// TODO!!!!!
	//
	// Even when raceenabled is true, there might be select
	// statements in packages compiled without -race (e.g.,
	// ensureSigM in runtime/signal_unix.go).
	// var pcs []uintptr
	// if raceenabled && pc0 != nil {
	// 	pc1 := (*[1 << 16]uintptr)(unsafe.Pointer(pc0))
	// 	pcs = pc1[:ncases:ncases]
	// }
	// casePC := func(casi int) uintptr {
	// 	if pcs == nil {
	// 		return 0
	// 	}
	// 	return pcs[casi]
	// }
	//
	// var t0 int64
	// if blockprofilerate > 0 {
	// 	t0 = cputicks()
	// }

	// The compiler rewrites selects that statically have
	// only 0 or 1 cases plus default into simpler constructs
	// (chansend and chanrecv with block argument passed as false).
	// The only way we can end up with such small sel.ncase
	// values here is for a larger select in which most channels
	// have been nilled out. The general code handles those
	// cases correctly, and they are rare enough not to bother
	// optimizing (and needing to test).

	// generate permuted order
	norder := 0 // count of select cases after dropping cases with nil channels
	for i := range scases {
		cas := &scases[i]

		// Omit cases with nil channels from the poll and lock orders.
		if cas.c == nil {
			cas.elem = nil // allow GC
			continue
		}

		// get random index between 0 and norder, so that
		// the i index into original cases order residing at
		// random index j in pollorder can be inserted at norder
		// index into pollorder (originally holds garbage).
		j := fastrandn(uint32(norder) + 1)
		// insert random index i into original cases order into
		// monotonically growing norder index
		pollorder[norder] = pollorder[j]
		// replace value at random index <= norder, which
		// is growing monotonically with every non-nil channel,
		// with index from the original order of select cases
		pollorder[j] = uint16(i)
		norder++
	}
	pollorder = pollorder[:norder] // since norder grew monotonically with every non-nil channel, we can cap pollorder's capacity
	lockorder = lockorder[:norder] // since norder grew monotonically with every non-nil channel, we can cap lockorder's capacity

	// sort the cases by Hchan address to get the locking order
	// (same channels but with different operations should be contiguous
	// in lockorder to no lock same channel twice).
	// Use simple heap sort, to guarantee n log n time and constant stack footprint.
	for i := range lockorder {
		_ = i
		// TODO!!!!!!
	}

	return 0, true
}

func (c *hchan) sortkey() uintptr {
	return uintptr(unsafe.Pointer(c))
}

// A runtimeSelect is a single case passed to rselect.
// This must match ../reflect/value.go:/runtimeSelect
type runtimeSelect struct {
	dir selectDir      // select case direction (send, receive, default)
	typ unsafe.Pointer // channel type (not used here)
	ch  *hchan         // channel
	val unsafe.Pointer // ptr to data (SendDir) or ptr to receive buffer (RecvDir)
}

// These values must match ../reflect/value.go:/SelectDir.
type selectDir int

const (
	_             selectDir = iota
	selectSend              // case Chan <- Send
	selectRecv              // case <-Chan:
	selectDefault           // default
)

//go:linkname reflect_rselect reflect.rselect
func reflect_rselect(cases []runtimeSelect) (int, bool) {
	if len(cases) == 0 {
		// parks the current goroutine forever
		// and switches to g0 scheduling stack.
		block()
	}

	// TODO!!!!! what's the reason for ordering select cases like this?
	//
	// select cases with excluded default case if any,
	// where send cases come first in order of their appearance
	// and receive cases come after in reversed order of their
	// appearance. Passed into selectgo as the select cases
	// to work with.
	sel := make([]scase, len(cases))
	// mapping of select case indexes within sel to their original
	// indexes in program order from the passed in reflect cases
	orig := make([]int, len(cases))
	// send and receive case counters. Used to contract sel and
	// orig if there's a default case present, because the default
	// case's original position in program order is tracked by the
	// dflt variable bellow. Also used to determine if there's only
	// a default select case present. Also passed down to selectgo.
	nsends, nrecvs := 0, 0
	// used to track default case's (if any) original position in
	// program order within the reflect cases passed in. Is also
	// passed to selectgo as the block boolean parameter. block
	// is false if dflt is -1, otherwise is true.
	dflt := -1
	for i, rc := range cases {
		var j int // position to insert converted reflect select case into the sel slice
		switch rc.dir {
		case selectDefault:
			dflt = i
			continue
		case selectSend:
			j = nsends
			nsends++
		case selectRecv:
			nrecvs++
			j = len(cases) - nrecvs
		}

		sel[j] = scase{c: rc.ch, elem: rc.val}
		orig[j] = i
	}

	// Only a default case.
	if nsends+nrecvs == 0 {
		return dflt, false
	}

	// Compact sel and orig if there's a default case present.
	if nsends+nrecvs < len(cases) {
		copy(sel[nsends:], sel[len(cases)-nrecvs:])
		copy(orig[nsends:], orig[len(cases)-nrecvs:])
	}

	order := make([]uint16, 2*(nsends+nrecvs)) // TODO!!!!!
	_ = order
}
