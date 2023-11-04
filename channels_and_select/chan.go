// go/src/runtime/chan.go
package channels

import (
	"unsafe"

	"github.com/pianoyeg94/go-runtime-inside-out/channels_and_select/atomic"
)

const (
	maxAlign = 8

	// uintptr(-int(unsafe.Sizeof(hchan{}))&(maxAlign-1)) = 0
	// unsafe.Sizeof(hchan{}) = 112
	// hchanSize = 112 bytes
	hchanSize = unsafe.Sizeof(hchan{}) + uintptr(-int(unsafe.Sizeof(hchan{}))&(maxAlign-1))

	debugChan = false
)

type hchan struct {
	qcount   uint           // total data in the queue
	dataqsiz uint           // size of the circular queue
	buf      unsafe.Pointer // points to an array of dataqsiz elements
	elemsize uint16         // element size can't be greater than 64KB
	closed   uint32
	elemtype *_type // element type

	// head and tail overlap is prevent by empty and full checks
	// using the `.dataqsiz` and `.qcount` attributes
	sendx uint  // send index (tail of the circular buffer)
	recvx uint  // receive index (head of the circular buffer)
	recvq waitq // list of recv waiters
	sendq waitq // list of send waiters

	// lock protects all fields in hchan, as well as several
	// fields in sudogs blocked on this channel.
	//
	// TODO!!!!! Do not change another G's status while holding this lock
	// (in particular, do not ready a G), as this can deadlock
	// with stack shrinking.
	lock mutex
}

type waitq struct {
	first *sudog
	last  *sudog
}

//go:linkname reflect_makechan reflect.makechan
func reflect_makechan(t *chantype, size int) *hchan {
	return makechan(t, size)
}

func makechan64(t *chantype, size int64) *hchan {
	if int64(int(size)) != size { // if we're on a 32-bit platform
		panic(plainError("makechan: size out of range"))
	}

	return makechan(t, int(size))
}

func makechan(t *chantype, size int) *hchan {
	elem := t.elem

	// compiler checks this but be safe
	// (element size can't be greater than 64KB)
	if elem.size >= 1<<16 {
		throw("makechan: invalid channel element type")
	}

	// chaneel structure and it's element type should be aligned by 8
	if hchanSize%maxAlign != 0 || elem.align > maxAlign {
		throw("makechan: bad alignment")
	}

	// channel buffer cannot exceed 256 TiB - 112 bytes in size
	mem, overflow := MulUintptr(elem.size, uintptr(size))
	if overflow || mem > maxAlloc-hchanSize || size < 0 {
		panic(plainError("makechan: size out of range"))
	}

	// Hchan does not contain pointers interesting for GC when elements stored in buf do not contain pointers.
	// buf points into the same allocation, elemtype is persistent.
	// SudoG's are referenced from their owning thread so they can't be collected.
	// TODO(dvyukov,rlh): Rethink when collector can move allocated objects.
	var c *hchan
	switch {
	case mem == 0: // unbuffered channel
		// Queue or element size is zero.
		c = (*hchan)(mallocgc(hchanSize, nil, true))
		// Race detector uses this location for synchronization.
		c.buf = c.raceaddr()
	case elem.ptrdata == 0: // buffered channel with elements containing no pointers
		// Elements do not contain pointers.
		// Allocate hchan and buf in one call.
		c = (*hchan)(mallocgc(hchanSize+mem, nil, true))
		c.buf = add(unsafe.Pointer(c), hchanSize)
	default: // buffered channel with elements containing pointers
		// Elements contain pointers.
		c = new(hchan)
		c.buf = mallocgc(mem, elem, true)
	}

	c.elemsize = uint16(elem.size)
	c.elemtype = elem
	c.dataqsiz = uint(size)

	lockInit(&c.lock, lockRankHchan) // defaults to no-op

	if debugChan {
		print("makechan: chan=", c, "; elemsize=", elem.size, ": dataqsiz=", size, "\n")
	}

	return c
}

// chanbuf(c, i) is pointer to the i'th slot in the buffer.
func chanbuf(c *hchan, i uint) unsafe.Pointer {
	return add(c.buf, uintptr(i)*uintptr(c.elemsize))
}

// full reports whether a send on c would block (that is, the channel is full).
// It uses a single word-sized read of mutable state, so although
// the answer is instantaneously true, the correct answer may have changed
// by the time the calling function receives the return value.
func full(c *hchan) bool {
	// c.dataqsiz is immutable (never written after the channel is created)
	// so it is safe to read at any time during channel operation.
	if c.dataqsiz == 0 { // send on unbuffered channel will block if there's no receiver
		// Assumes that a pointer read is relaxed-atomic,
		// even if reordered with if c.dataqsiz == 0 {}
		// seeing a subhistory of the c.recvq.first relaxed-atomic is sufficient
		// TODO: why is seeing a subhistory sufficient?
		return c.recvq.first == nil
	}

	// Assumes that a uint read is relaxed-atomic,
	// seeing a subhistory of the c.qcount relaxed-atomic is sufficient
	// TODO: why is seeing a subhistory sufficient?
	return c.qcount == c.dataqsiz
}

/*
 * generic single channel send/recv
 * If block is false,
 * then the protocol will not
 * sleep but return if it could
 * not complete.
 *
 * TODO!!!!! sleep can wake up with g.param == nil
 * when a channel involved in the sleep has
 * been closed.  it is easiest to loop and re-run
 * the operation; we'll see that it's now closed.
 */
func chansend(c *hchan, ep unsafe.Pointer, block bool, callerpc uintptr) bool {
	if c == nil {
		if !block { // one can write to a nil channel in case of select-default switch
			return false
		}
		// block forever, never returns, another goroutine's continuation on g0.
		// TODO!!!!! Eventually the trace will be logged and the process will terminate.
		// TODO!!!!! since no one will be holding a reference to this goroutine, will it be garbage collected?
		// Why not panic right here?
		gopark(nil, nil, waitReasonChanSendNilChan, traceEvGoStop, 2)
		throw("unreachable")
	}

	if debugChan {
		print("chansend: chan=", c, "\n")
	}

	// TODO!!!!! if raceenabled {
	// 	racereadpc(c.raceaddr(), callerpc, abi.FuncPCABIInternal(chansend))
	// }

	// Fast path: check for failed non-blocking operation without acquiring the lock (for select-default cases)
	//
	// After observing that the channel is not closed, we observe that the channel is
	// not ready for sending. Each of these observations is a single word-sized read
	// (first c.closed and second full()).
	// Because a closed channel cannot transition from 'ready for sending' to
	// 'not ready for sending', even if the channel is closed between the two observations,
	// they imply a moment between the two when the channel was both not yet closed
	// and not ready for sending. We behave as if we observed the channel at that moment,
	// and report that the send cannot proceed.
	//
	// It is okay if the reads are reordered here: if we observe that the channel is not
	// ready for sending and then observe that it is not closed, that implies that the
	// channel wasn't closed during the first observation.
	//
	//
	// TODO!!!!! However, nothing here guarantees forward progress.
	// We rely on the side effects of lock release in chanrecv() and closechan()
	// to update this thread's view of c.closed and full().
	if !block && c.closed == 0 && full(c) {
		return false
	}

	// TODO!!!!! var t0 int64
	// if blockprofilerate > 0 {
	// 	t0 = cputicks()
	// }

	lock(&c.lock)

	if c.closed != 0 {
		unlock(&c.lock)
		panic(plainError("send on closed channel"))
	}

	// TODO!!!!! so message delivery can be reordered if there's data in the buffer
	// and an active receiver (later message goes straight to the receiver bypassing
	// data in buffer)?
	if sg := c.recvq.dequeue(); sg != nil { // if we have at least one active receiver?
		// Found a waiting receiver. We pass the value we want to send
		// directly to the receiver, bypassing the channel buffer (if any).

		// TODO!!!!! send(c, sg, ep, func() { unlock(&c.lock) }, 3)

		return true
	}

	// single check for active receiver above,
	// otherwise if channel is buffered and there's
	// space in the buffer, add data to queue
	if c.qcount < c.dataqsiz {
		// Space is available in the channel buffer. Enqueue the element to send.
		qp := chanbuf(c, c.sendx) // uses pointer arithmetic to get sendx'th slot in buffer (tail pointer), returns pointer into the slot

		// TODO!!!!! if raceenabled {
		// 	racenotify(c, c.sendx, nil)
		// }

		typedmemmove(c.elemtype, qp, ep) // append data to queue
		c.sendx++                        // advance tail
		if c.sendx == c.dataqsiz {       // check for tail pointer overflow, wrap around case
			c.sendx = 0
		}
		c.qcount++
		unlock(&c.lock)
		return true
	}

	if !block { // after the fast path non-locked check somebody has stolen our active receiver or free slot in buffer, so if select-default, just return
		unlock(&c.lock)
		return false
	}

	// Block on the channel. Some receiver will complete our operation for us
	// (will write the data straight to the receiver-assoicated goroutine's stack).
	gp := getg()
	mysg := acquireSudog() // get from local cache, steal from global cache or allocate a new one
	mysg.releasetime = 0   // TODO!!!!!
	// TODO!!!!! if t0 != 0 {
	// 	mysg.releasetime = -1
	// }

	// TODO!!!!! No stack splits between assigning elem and enqueuing mysg
	// on gp.waiting where copystack can find it.
	mysg.elem = ep        // pointer to variable on stack of the parking goroutine or to heap
	mysg.waitlink = nil   // TODO!!!!!
	mysg.g = gp           // save goroutine pointer in `sudog`
	mysg.isSelect = false // TODO!!!!!
	mysg.c = c            // TODO!!!!!
	gp.waiting = mysg     // TODO!!!!! sudog structures this g is waiting on (that have a valid elem ptr); in lock order
	gp.param = nil        // TODO!!!!!: when a channel operation wakes up a blocked goroutine, it sets param to point to the sudog of the completed blocking operation.
	c.sendq.enqueue(mysg) // for receiver

	// Signal to anyone trying to shrink our stack that we're about
	// to park on a channel. The window between when this G's status
	// changes and when we set gp.activeStackChans is not safe for
	// stack shrinking - there are unlocked sudogs that point into G's stack (sudog.elem),
	// stack copying must lock the channels of those sudogs after gp.activeStackChans is set.
	gp.parkingOnChan.Store(true)

	// never returns, schedules another goroutine's continuation on g0
	// TODO!!!!! when do we resume?
	gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanSend, traceEvGoBlockSend, 2)

	// TODO!!!!!
	//
	// Ensure the value being sent is kept alive until the
	// receiver copies it out. The sudog has a pointer to the
	// stack object, but sudogs aren't considered as roots of the
	// stack tracer.
	KeepAlive(ep)

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	return true
}

func send(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int) {
	// TODO!!!!!
	//
	// if raceenabled {
	// 	if c.dataqsiz == 0 {
	// 		racesync(c, sg)
	// 	} else {
	// 		// Pretend we go through the buffer, even though
	// 		// we copy directly. Note that we need to increment
	// 		// the head/tail locations only when raceenabled.
	// 		racenotify(c, c.recvx, nil)
	// 		racenotify(c, c.recvx, sg)
	// 		c.recvx++
	// 		if c.recvx == c.dataqsiz {
	// 			c.recvx = 0
	// 		}
	// 		c.sendx = c.recvx // c.sendx = (c.sendx+1) % c.dataqsiz
	// 	}
	// }
	if sg.elem != nil {
		// copy ep directly from our stack to sudog's elem,
		// which is on the receiver goroutines stack or points
		// from it's stack to heap
		sendDirect(c.elemtype, sg, ep)
		sg.elem = nil
	}
	gp := sg.g
	unlockf()
	gp.param = unsafe.Pointer(sg)
	sg.success = true
	// TODO!!!!!
	//
	// if sg.releasetime != 0 {
	// 	sg.releasetime = cputicks()
	// }

	// Switches to the per-OS-thread stack and
	// switches back after the call.
	//
	// 1. Does a CAS of sender goroutine's status from Gwaiting or Gscanwaiting to Grunnable,
	//    may loop if the g->atomicstatus is in a Gscan status until the routine that
	//    put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
	//    yielding to the OS scheduler).
	//
	// 2. Tries to put the sender goroutine on the local runnable queue into the pp.runnext slot.
	//    If there was a goroutine already in this slot tries to kick it out
	//    into the tail of the local runnable queue, if it's full, runnext puts
	//    the kicked out goroutine onto the global queue.
	//
	// 3. // TODO!!!!! wakep()
	goready(gp, skip+1)
}

// TODO!!!!!
//
// Sends and receives on unbuffered or empty-buffered channels are the
// only operations where one running goroutine writes to the stack of
// another running goroutine. The GC assumes that stack writes only
// happen when the goroutine is running and are only done by that
// goroutine. Using a write barrier is sufficient to make up for
// violating that assumption, but the write barrier has to work.
// typedmemmove will call bulkBarrierPreWrite, but the target bytes
// are not in the heap, so that will not help. We arrange to call
// memmove and typeBitsBulkBarrier instead.

func sendDirect(t *_type, sg *sudog, src unsafe.Pointer) {
	// src is on our stack, dst is a slot on another stack.

	// TODO!!!!!
	//
	// Once we read sg.elem out of sg, it will no longer
	// be updated if the destination's stack gets copied (shrunk).
	// So make sure that no preemption points can happen between read & use.
	dst := sg.elem
	typeBitsBulkBarrier(t, uintptr(dst), uintptr(src), t.size) // TODO!!!!!
	// No need for cgo write barrier checks because dst is always
	// Go memory.
	memmove(dst, src, t.size) // TODO!!!!!
}

func recvDirect(t *_type, sg *sudog, dst unsafe.Pointer) {
	// dst is on our stack or the heap, src is on another stack.
	// The channel is locked, so src will not move during this
	// operation.
	src := sg.elem
	typeBitsBulkBarrier(t, uintptr(dst), uintptr(src), t.size) // TODO!!!!!
	memmove(dst, src, t.size)                                  // TODO!!!!!
}

func closechan(c *hchan) {
	if c == nil {
		panic(plainError("close of nil channel"))
	}

	lock(&c.lock)
	if c.closed != 0 {
		unlock(&c.lock)
		panic(plainError("close of closed channel"))
	}

	// TODO!!!!!
	//
	// if raceenabled {
	// 	callerpc := getcallerpc()
	// 	racewritepc(c.raceaddr(), callerpc, abi.FuncPCABIInternal(closechan))
	// 	racerelease(c.raceaddr())
	// }

	c.closed = 1

	var glist gList

	// release all readers
	// (blocked readers mean
	// that a buffered channel
	// has no data in it's buffer)
	for {
		sg := c.recvq.dequeue()
		if sg == nil {
			// no more active readers
			break
		}
		if sg.elem != nil {
			typedmemclr(c.elemtype, sg.elem)
			sg.elem = nil
		}
		// TODO!!!!!
		//
		// if sg.releasetime != 0 {
		// 	sg.releasetime = cputicks()
		// }
		gp := sg.g
		gp.param = unsafe.Pointer(sg) // TODO!!!!! no reader checks this param, what's the puprpose of this?
		sg.success = false            // will be observed by reader and returned as a result of it's receive operation
		// TODO!!!!!
		//
		// if raceenabled {
		// 	raceacquireg(gp, c.raceaddr())
		// }
		glist.push(gp) // add gp to the head of the singly-linked list
	}

	// release all blocked writers (they will panic
	// "send on closed channel" upon observing sudog's
	// success flag set to false)
	for {
		sg := c.sendq.dequeue()
		if sg == nil {
			// no more active writers
			break
		}
		sg.elem = nil // decrement reference to writer's send value
		// TODO!!!!!
		//
		// if sg.releasetime != 0 {
		// 	sg.releasetime = cputicks()
		// }
		gp := sg.g
		sg.success = false // will be observed by writer, it will react with a panic "send on closed channel"
		// TODO!!!!!
		//
		// 	if raceenabled {
		// 		raceacquireg(gp, c.raceaddr())
		// 	}
		glist.push(gp)
	}
	unlock(&c.lock)

	// Ready all Gs now that we've dropped the channel lock.
	for !glist.empty() {
		gp := glist.pop()
		gp.schedlink = 0 // remove singly-linked list pointer to next element
		// Switches to the per-OS-thread stack and
		// switches back after the call.
		//
		// 1. Does a CAS of sender goroutine's status from Gwaiting or Gscanwaiting to Grunnable,
		//    may loop if the g->atomicstatus is in a Gscan status until the routine that
		//    put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
		//    yielding to the OS scheduler).
		//
		// 2. Tries to put the sender goroutine on the local runnable queue into the pp.runnext slot.
		//    If there was a goroutine already in this slot tries to kick it out
		//    into the tail of the local runnable queue, if it's full, runnext puts
		//    the kicked out goroutine onto the global queue.
		//
		// 3. // TODO!!!!! wakep()
		goready(gp, 3)
	}
}

// empty reports whether a read from c would block (that is, the channel is
// empty).  It uses a single atomic read of mutable state.
func empty(c *hchan) bool {
	// c.dataqsiz is immutable.
	if c.dataqsiz == 0 { // unbuffered channel, atomically check for active sender (sudog waiting on the send queue)
		return atomic.Loadp(unsafe.Pointer(&c.sendq.first)) == nil
	}

	// buffered channel, just check instant data in circular buffer
	return atomic.Loaduint(&c.qcount) == 0
}

// chanrecv receives on channel c and writes the received data to ep.
// ep may be nil, in which case received data is ignored.
// If block == false and no elements are available, returns (false, false).
// Otherwise, if c is closed, zeros *ep and returns (true, false).
// Otherwise, fills in *ep with an element and returns (true, true).
// A non-nil ep must point to the heap or the caller's stack.
func chanrecv(c *hchan, ep unsafe.Pointer, block bool) (selected, received bool) { // TODO!!!!! selected?
	// TODO!!!!! raceenabled: don't need to check ep, as it is always on the stack
	// or is new memory allocated by reflect.

	if debugChan {
		print("chanrecv: chan=", c, "\n")
	}

	if c == nil { // recieve on nil channel
		if !block { // participating in a select + default, just return
			return
		}

		// block forever, never returns, another goroutine's continuation on g0.
		// TODO!!!!! Eventually the trace will be logged and the process will terminate.
		// TODO!!!!! since no one will be holding a reference to this goroutine, will it be garbage collected?
		gopark(nil, nil, waitReasonChanReceiveNilChan, traceEvGoStop, 2)
		throw("unreachable")
	}

	// Fast path: check for failed non-blocking operation without acquiring the lock.
	if !block && empty(c) { // select + default + channel is not ready for receiving (unbufferd with no pending sender sudog or buffered with no data in circular buffer)
		// After observing that the channel is not ready for receiving, we observe whether the
		// channel is closed.
		//
		// Reordering of these checks could lead to incorrect behavior when racing with a close.
		// For example, if the channel was open and not empty, was closed, and then drained,
		// reordered reads could incorrectly indicate "open and empty". To prevent reordering,
		// we use atomic loads for both checks, and TODO!!!!! rely on emptying and closing to happen in
		// separate critical sections under the same lock.  This assumption fails when closing
		// an unbuffered channel with a blocked send, but that is an error condition anyway.
		if atomic.Load(&c.closed) == 0 {
			// Because a channel cannot be reopened, the later observation of the channel
			// being not closed implies that it was also not closed at the moment of the
			// first observation. We behave as if we observed the channel at that moment
			// and report that the receive cannot proceed.
			return
		}
		// The channel is irreversibly closed. Re-check whether the channel has any pending data
		// to receive, which could have arrived between the empty and closed checks above.
		// Sequential consistency is also required here, when racing with such a send.
		if empty(c) {
			// The channel is irreversibly closed and empty.

			// TODO!!!!! if raceenabled {}

			// TODO!!!!! if receiveing from channel is done into a variable,
			// clear memory pointed to by ep, essentially storing the zero value
			// of channel's type into that variable?
			if ep != nil {
				typedmemclr(c.elemtype, ep)
			}
			return true, false
		}
	}

	// TODO!!!!! var t0 int64
	// if blockprofilerate > 0 {
	// 	t0 = cputicks()
	// }

	// from now on manipulate channel only when holding the lock
	lock(&c.lock)

	if c.closed != 0 { // channel closed
		if c.qcount == 0 { // if channel is closed and it's unbuffered or there's no data in its circular buffer
			// TODO!!!!! if raceenabled {
			// 	raceacquire(c.raceaddr())
			// }
			unlock(&c.lock) // it's ok to unlock here, because we only need to deal with user's variable

			// TODO!!!!! if receiveing from channel is done into a variable,
			// clear memory pointed to by ep, essentially storing the zero value
			// of channel's type into that variable?
			if ep != nil {
				typedmemclr(c.elemtype, ep)
			}
			return true, false
		}
		// The channel has been closed, but the channel's buffer have data, so we need to proceed to hand out that data to the channel user.

	} else { // channel NOT closed
		// Just found waiting sender with not closed.
		if sg := c.sendq.dequeue(); sg != nil {
			// Found a waiting sender. If buffer is size 0, receive value
			// directly from sender. Otherwise, receive from head of queue
			// and add sender's value to the tail of the queue (both map to
			// the same buffer slot because the queue is full, because otherise
			// the sender wouldn't block on sending to a buffered channel).
			// Readies the sender goroutine before returning.
			recv(c, sg, ep, func() { unlock(&c.lock) }, 3)
			return true, true
		}
	}

	// no active sender, will not re-check active sender, so try channel's buffer if it has one
	if c.qcount > 0 { // channel is buffered and the buffer is not empty
		// Receive directly from queue
		qp := chanbuf(c, c.recvx) // uses pointer arithmetic to get recvx'th slot in buffer (head pointer), returns pointer into the slot

		// TODO!!!!! if raceenabled {
		// 	racenotify(c, c.recvx, nil)
		// }

		if ep != nil { // if channel receive into a variable pointing to stack or heap
			typedmemmove(c.elemtype, ep, qp) // write result to receiver's stack via the `ep` pointer (uses memmove under the hood)
		}
		typedmemclr(c.elemtype, qp) // clear slot, not garbage collected
		c.recvx++                   // advance head
		if c.recvx == c.dataqsiz {  // check for head pointer overflow, wrap around case
			c.recvx = 0
		}
		c.qcount--
		unlock(&c.lock)
		return true, true
	}

	if !block { // after the fast path non-locked check somebody has stolen our value, so if select-default, just return
		unlock(&c.lock)
		return false, false
	}

	// no sender available: block on this channel.
	gp := getg()
	mysg := acquireSudog() // get from local cache, steal from global cache or allocate a new one
	mysg.releasetime = 0   // TODO!!!!!

	// TODO!!!!! if t0 != 0 { releasetime is - 1 if cpu profiling is on
	// 	mysg.releasetime = -1
	// }

	// TODO!!!!! No stack splits between assigning elem and enqueuing mysg
	// on gp.waiting where copystack can find it.
	mysg.elem = ep        // pointer to variable on stack of the parking goroutine or to heap
	mysg.waitlink = nil   // TODO!!!!!
	mysg.g = gp           // save goroutine pointer in `sudog`
	mysg.isSelect = false // TODO!!!!!
	gp.waiting = mysg     // TODO!!!!! sudog structures this g is waiting on (that have a valid elem ptr); in lock order
	gp.param = nil        // TODO!!!!!: when a channel operation wakes up a blocked goroutine, it sets param to point to the sudog of the completed blocking operation.
	c.recvq.enqueue(mysg) // protected by hchan lock, enqueu sudog onto list of recieve waiters TODO!!!!!

	// Signal to anyone trying to shrink our stack that we're about
	// to park on a channel. The window between when this G's status
	// changes and when we set gp.activeStackChans is not safe for
	// stack shrinking - there are unlocked sudogs that point into G's stack (sudog.elem),
	// stack copying must lock the channels of those sudogs after gp.activeStackChans is set.
	gp.parkingOnChan.Store(true)

	// never returns, schedules another goroutine's continuation on g0
	// TODO!!!!! when do we resume?
	gopark(chanparkcommit, unsafe.Pointer(&c.lock), waitReasonChanReceive, traceEvGoBlockRecv, 2)

	// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

	return true, true
}

// recv processes a receive operation on a full channel c.
// There are 2 parts:
//  1. The value sent by the sender sg is put into the channel
//     and the sender is woken up to go on its merry way.
//  2. The value received by the receiver (the current G) is
//     written to ep.
//
// For synchronous channels, both values are the same.
// For asynchronous channels, the receiver gets its data from
// the channel buffer's head slot and the sender's data is put into the
// channel buffer's tail slot.
// Channel c must be full and locked. recv unlocks c with unlockf.
// sg must already be dequeued from c.
// A non-nil ep must point to the heap or the caller's stack.
func recv(c *hchan, sg *sudog, ep unsafe.Pointer, unlockf func(), skip int) {
	if c.dataqsiz == 0 { // unbuffered channel, active sender
		// if raceenabled {
		// 	racesync(c, sg)
		// }
		if ep != nil {
			// copy data from sender
			//
			// memmove sg's elem (senders value)
			// directly to our stack under a write barrier
			recvDirect(c.elemtype, sg, ep)
		}

	} else { // buffered channel, buffer full, active sender
		// Queue is full. Take the item at the
		// head of the queue. Make the sender enqueue
		// its item at the tail of the queue. Since the
		// queue is full, those are both the same slot.
		qp := chanbuf(c, c.recvx) // uses pointer arithmetic to get recvx'th slot in buffer (head pointer), returns pointer into the slot

		// TODO!!!!! if raceenabled {
		// 	racenotify(c, c.recvx, nil)
		// 	racenotify(c, c.recvx, sg)
		// }

		// copy data from queue to receiver goroutine
		if ep != nil {
			typedmemmove(c.elemtype, ep, qp)
		}

		// copy data from sender to queue into the tail slot which is the same as the head slot
		typedmemmove(c.elemtype, qp, sg.elem)
		c.recvx++
		if c.recvx == c.dataqsiz {
			c.recvx = 0
		}
		c.sendx = c.recvx // c.sendx = (c.sendx+1) % c.dataqsiz
	}

	sg.elem = nil
	gp := sg.g        // sender goroutine
	unlockf()         // unlock channel
	sg.success = true // TODO!!!!! how is it used?

	// TODO!!!!! if sg.releasetime != 0 {
	// 	sg.releasetime = cputicks()
	// }

	// Switches to the per-OS-thread stack and
	// switches back after the call.
	//
	// 1. Does a CAS of sender goroutine's status from Gwaiting or Gscanwaiting to Grunnable,
	//    may loop if the g->atomicstatus is in a Gscan status until the routine that
	//    put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
	//    yielding to the OS scheduler).
	//
	// 2. Tries to put the sender goroutine on the local runnable queue into the pp.runnext slot.
	//    If there was a goroutine already in this slot tries to kick it out
	//    into the tail of the local runnable queue, if it's full, runnext puts
	//    the kicked out goroutine onto the global queue.
	//
	// 3. // TODO!!!!! wakep()
	goready(gp, skip+1)
}

// chanparkcommit is called as part of the `gopark` routine
// after the goroutine's status transfers to `_Gwaiting` and
// the goroutine is dissasociated from the M, which was executing
// it, BUT just before another goroutine gets scheduled by g0.
//
// The main responsibility of chanparkcommit is to set goroutine's
// `activeStackChans` attribute to true, which tells the other parts
// of the runtime that there are unlocked sudogs that point into gp's stack (sudog.elem).
// So stack copying must lock the channels of those sudogs.
// In the end chanparkcommit unlocks channel's lock and returns true,
// which tells `gopark` to go on an schedule another goroutine.
func chanparkcommit(gp *g, chanLock unsafe.Pointer) bool {
	// TODO!!!!! There are unlocked sudogs that point into gp's stack (sudog.elem).
	// Stack copying must lock the channels of those sudogs.
	// Set activeStackChans here instead of before we try parking
	// because we could self-deadlock in stack growth on the
	// channel lock.
	gp.activeStackChans = true

	// TODO!!!!! Mark that it's safe for stack shrinking to occur now,
	// because any thread acquiring this G's stack for shrinking
	// is guaranteed to observe activeStackChans after this store.
	gp.parkingOnChan.Store(false)

	// TODO!!!!! Make sure we unlock after setting activeStackChans and
	// unsetting parkingOnChan. The moment we unlock chanLock
	// we risk gp getting readied by a channel operation and
	// gp could continue running before the unlock is visible
	// (even to gp itself).
	unlock((*mutex)(chanLock))
	return true
}

// enqueue inserts a sudog pointer
// at the end of the waitq doubly-linked list.
// Protected by hchan's lock.
func (q *waitq) enqueue(sgp *sudog) {
	sgp.next = nil
	x := q.last
	if x == nil { // if list is empty
		sgp.prev = nil
		q.first = sgp
		q.last = sgp
		return
	}

	sgp.prev = x
	x.next = sgp
	q.last = sgp
}

// dequeue retrieves a sudog pointer
// from the start of the waitq doubly-linked list.
// Protected by hchan's lock.
func (q *waitq) dequeue() *sudog {
	for {
		sgp := q.first
		if sgp == nil { // queue is empty
			return nil
		}

		y := sgp.next
		if y == nil { // only one element in the queue, queue becomes empty
			q.first = nil
			q.last = nil
		} else {
			y.prev = nil
			q.first = y
			sgp.next = nil // mark as removed (see dequeueSudoG)
		}

		// TODO: if sgp.isSelect && !sgp.g.selectDone.CompareAndSwap(0, 1) {}

		return sgp
	}
}

// TODO!!!!! raceaddr returns a pointer to pointer to buf,
// essentially a double pointer.
// The pointer points to a nil pointer, because
// the channel is unbuffered.
func (c *hchan) raceaddr() unsafe.Pointer {
	// Treat read-like and write-like operations on the channel to
	// happen at this address. Avoid using the address of qcount
	// or dataqsiz, because the len() and cap() builtins read
	// those addresses, and we don't want them racing with
	// operations like close().
	return unsafe.Pointer(&c.buf)
}
