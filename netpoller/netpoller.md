ListenTCP() from src/net/tcpsock.go is called by Listen() from src/net/dial.go.

(sl *sysListener) listenTCPProto() is called by ListenTCP() from src/net/tcpsock.go
(sd *sysDialer) dialTCP() is called by Dial() and DialContext() from src/net/dial.go

(sl *sysListener) listenTCPProto() is called by (sl *sysListener) listenTCP()
(sd *sysDialer) doDialTCPProto() is called by (sd *sysDialer) dialTCP()

src/net/ipsock_posix.go:internetSocket is called by src/net/tspsock_posix.go (sl *sysListener) listenTCPProto() or
                                                                             (sd *sysDialer) doDialTCPProto()

src/net/sock_posix.go:socket is called by src/net/ipsock_posix.go:internetSocket

When the first socket is created by a go program the following happens (socket function from src/net/sock_posix.go):

    - a socket id created via a syscall with the appropriate family, sotype and protocol.

    - This syscall should return a new socket file descriptor

    - After that default options are set on the socket (src/net/sockopt_linux.go)
  
    ```go
	func setDefaultSockopts(s, family, sotype int, ipv6only bool) error {
		if family == syscall.AF_INET6 && sotype != syscall.SOCK_RAW {
			// Allow both IP versions even if the OS default
			// is otherwise. Note that some operating systems
			// never admit this option.
			syscall.SetsockoptInt(s, syscall.IPPROTO_IPV6, syscall.IPV6_V6ONLY, boolint(ipv6only))
		}
		if (sotype == syscall.SOCK_DGRAM || sotype == syscall.SOCK_RAW) && family != syscall.AF_UNIX {
			// Allow broadcast.
			return os.NewSyscallError("setsockopt", syscall.SetsockoptInt(s, syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1))
		}
		return nil
	}
	```

	- After that the socket descriptor together with it's family, sotype and net is passed into
      newFD() which should return a new *netFD ready for asyncio (net in this case can either be
	  "unix", "unix4", "unix6", "unixgram4", "unixgram6", "unixpacket", "unixpacket4", "unixpacket6", "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6").
	  (src/net/fd_unix.go)

	- newFD() initializes and returns *netFD with the following fields:

    	```go
		// Network file descriptor.
	  	ret := &netFD{
			pfd: poll.FD{
				// System file descriptor. Immutable until Close.
				Sysfd:         sysfd,
				// Whether this is a streaming descriptor, as opposed to a
				// packet-based descriptor like a UDP socket. Immutable.
				IsStream:      sotype == syscall.SOCK_STREAM,
				// Whether a zero byte read indicates EOF. This is false for a
				// message based socket connection.
				ZeroReadIsEOF: sotype != syscall.SOCK_DGRAM && sotype != syscall.SOCK_RAW,

				// Other fields not initialized here in newFD()
				// ==============================================

				// Lock sysfd and serialize access to Read and Write methods.
				/* fdmu fdMutex */

				// Platform dependent state of the file descriptor.
				/* SysFile */

				// I/O poller (epoll fd, will be initialized only once,
				// when first netFd's init() method is called).
				/*pd pollDesc */

				// Semaphore signaled when file is closed.
				/* csema uint32 */

				// Non-zero if this file has been set to blocking mode.
				/* isBlocking uint32 */

				// Whether this is a file rather than a network socket.
				/* isFile bool */
			},
			// immutable until Close
			family: family,
			sotype: sotype,
			net:    net,

			// Other fields not initialized here in newFD()
			// ==============================================

			/*
			isConnected bool // handshake completed or use of association with peer
			laddr       Addr
			raddr       Addr
			*/
		}
	  	```
	 
	- This *netFD is returned to the initial socket function.

    - If the socket function had a laddr (listen address) passed, then in case of TCP the listenStream() method
      is called on the *netFD (max listener backlog on Linux is read from the "/proc/sys/net/core/somaxconn" 
	  file, or a default of 128 is used on error or if somaxconn is 0, if somaxconn is greater than 65535 and 
	  we are on Linux kernel version < 4.1, which can pass backlog only as uint16, we have to cap the backlog to max
	  uint16, which is exactly 65535, on Linux >= 4.1, we can have a much bigger backlog read from somaxconn).

	- *netFDs listenStream() method binds the socket to the listen address and calls listen on it. After that it
       calls *netFDs init() method, which in turn calls its underlying "poll.FD's" Init() method. 

	- If this is the first socket in our golang program, then an EPOLL kernel instance is created with 
      syscall.EPOLL_CLOEXEC flag, and its file descriptor is stored in the `epfd` global variable 
	  (src/runtime/netpoll_epoll.go).
	  Creates a non-blocking unix pipe and registering its read end to be tracked by epoll 
	  (the corresponding write end will be used to unblock epoll_wait syscalls by writing  a zero-byte to it, 
	  which will trigger an I/O event on the read end, both fds of the pipe are global variables netpollBreakRd 
	  and netpollBreakWr).
	  Atomically sets the netpollInited global variable flag.

#### src/internal/poll/fd_poll_runtime.go:pollDesc.init()

```go
func (pd *pollDesc) init(fd *FD) error {}
```

1) Only on the globally first call (via sync.Once) initializes an epoll control kernel structure and 
   stores its fd in the epfd global variable.
   This includes:
      - Initializing the epoll control kernel structure and storing its handle fd 
        in the epfd global variable

      - Creating a non-blocking unix pipe and registering its read end to be tracked by epoll
        (the corresponding write end will be used to unblock epoll_wait syscalls by writing 
         a zero-byte to it, which will trigger an I/O event on the read end).

      - Atomically setting the netpollInited global variable flag

2) Creates or reuses a pointer to an instance of src/runtime/netpoll.go:pollDesc (off-go-heap singly-linked list cache), 
   registers the underlying os fd extracted from *FD via epoll_ctl in edge-triggered (EPOLLET) mode to be simultaneously 
   polled for read, write and peer shutdown events.
   This includes: 
      - Getting a pointer to src/runtime/netpoll.go:pollDesc from a freshly or 
        already allocated src/runtime/netpoll.go:pollcache. The cache resides in 
        mmaped non-GC memory. Must be in non-GC memory because sometimes during its lifecycle a
        src/runtime/netpoll.go:pollDesc is referenced only from epoll/kqueue internals.

      - Registering the underlying os fd via epoll_ctl in edge-triggered mode to be 
        simultaneously polled for read, write and peer shutdown events.
        Before calling epoll_ctl an unaligned pointer to src/runtime/netpoll.go:pollDesc is stored 
        in the epoll event's data field. This pointer will be later passed in to a callback function, 
        which fires when an I/O event is registered on the underlying fd.

3) If an error occurs while doing the epoll_ctl syscall, the errno is transformed into
   an Errno error wrapper and returned.

4) Otherwise stores a pointer to the resulting src/runtime/netpoll.go:pollDesc in 
   the src/internal/poll/fd_poll_runtime.go:pollDesc .runtimeCtx field (which is a uintptr, not tracked by GC).


5) After that *netFD is returned to the caller of socket(), with initialzed epoll and registered with it to be tracked for 
   for read, write and peer shutdown events.


#### src/internal/poll/fd_poll_runtime.go:pollDesc


```go
type pollDesc struct {
   // Basically points to an off-heap struct not tracked by GC, 
   // that's why it's not an unsafe.Pointer
   // The runtime system considers an unsafe.Pointer as a reference 
   // to an object, which keeps the object alive for GC. It does not 
   // consider a uintptr as such a reference. (That is, while unsafe.Pointer 
   // has a pointer type, uintptr has integer type.),
   runtimeCtx uintptr 
   // points to
   //    |
   //    |
   //    V
   // Network poller descriptor (at src/runtime/netpoll.go)
   //
   // No heap pointers.
   type pollDesc struct {
   	_     sys.NotInHeap
   	link  *pollDesc      // in pollcache, protected by pollcache.lock
   	fd    uintptr        // constant for pollDesc usage lifetime
   	fdseq atomic.Uintptr // protects against stale pollDesc
   
   	// atomicInfo holds bits from closing, rd, and wd,
   	// which are only ever written while holding the lock,
   	// summarized for use by netpollcheckerr,
   	// which cannot acquire the lock.
   	// After writing these fields under lock in a way that
   	// might change the summary, code must call publishInfo
   	// before releasing the lock.
   	// Code that changes fields and then calls netpollunblock
   	// (while still holding the lock) must call publishInfo
   	// before calling netpollunblock, because publishInfo is what
   	// stops netpollblock from blocking anew
   	// (by changing the result of netpollcheckerr).
   	// atomicInfo also holds the eventErr bit,
   	// recording whether a poll event on the fd got an error;
   	// atomicInfo is the only source of truth for that bit.
   	atomicInfo atomic.Uint32 // atomic pollInfo
   
   	// rg, wg are accessed atomically and hold g pointers.
   	// (Using atomic.Uintptr here is similar to using guintptr elsewhere.)
   	rg atomic.Uintptr // pdReady, pdWait, G waiting for read or pdNil
   	wg atomic.Uintptr // pdReady, pdWait, G waiting for write or pdNil
   
   	lock    mutex // protects the following fields
   	closing bool
   	user    uint32    // user settable cookie
   	rseq    uintptr   // protects from stale read timers
   	rt      timer     // read deadline timer (set if rt.f != nil)
   	rd      int64     // read deadline (a nanotime in the future, -1 when expired)
   	wseq    uintptr   // protects from stale write timers
   	wt      timer     // write deadline timer
   	wd      int64     // write deadline (a nanotime in the future, -1 when expired)
   	self    *pollDesc // storage for indirect interface. See (*pollDesc).makeArg.
   }
}
```


#



#### src/internal/poll/fd_poll_runtime.go:pollDesc.init()

```go
func (pd *pollDesc) init(fd *FD) error {}
```

1) 


1) Only on the globally first call (via sync.Once) initializes an epoll control kernel structure and 
   stores its fd in the epfd global variable.
   This includes:
      - Initializing the epoll control kernel structure and storing its handle fd 
        in the epfd global variable

      - Creating a non-blocking unix pipe and registering its read end to be tracked by epoll
        (the corresponding write end will be used to unblock epoll_wait syscalls by writing 
         a zero-byte to it, which will trigger an I/O event on the read end).

      - Atomically setting the netpollInited global variable flag

2) Creates or reuses a pointer to an instance of src/runtime/netpoll.go:pollDesc (off-go-heap singly-linked list cache), 
   registers the underlying os fd extracted from *FD via epoll_ctl in edge-triggered (EPOLLET) mode to be simultaneously 
   polled for read, write and peer shutdown events.
   This includes: 
      - Getting a pointer to src/runtime/netpoll.go:pollDesc from a freshly or 
        already allocated src/runtime/netpoll.go:pollcache. The cache resides in 
        mmaped non-GC memory. Must be in non-GC memory because sometimes during its lifecycle a
        src/runtime/netpoll.go:pollDesc is referenced only from epoll/kqueue internals.

      - Registering the underlying os fd via epoll_ctl in edge-triggered mode to be 
        simultaneously polled for read, write and peer shutdown events.
        Before calling epoll_ctl an unaligned pointer to src/runtime/netpoll.go:pollDesc is stored 
        in the epoll event's data field. This pointer will be later passed in to a callback function, 
        which fires when an I/O event is registered on the underlying fd.

3) If an error occurs while doing the epoll_ctl syscall, the errno is transformed into
   an Errno error wrapper and returned.

4) Otherwise stores a pointer to the resulting src/runtime/netpoll.go:pollDesc in 
   the src/internal/poll/fd_poll_runtime.go:pollDesc .runtimeCtx field (which is a uintptr, not tracked by GC).


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.close()

func (pd *pollDesc) close() {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   wasn't initialized or is already closed, so we return from this function immediately.

2) evict() should be called on pd before this method is called. 
   (src/internal/poll/fd_poll_runtime.go => src/runtime/netpoll.go:poll_runtime_pollUnblock(pd *pollDesc)).
   Under *pollDesc's lock will set its `.closing` field to true. And increment its `.rseq` and `.wseq` fields.

   TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!seq fields

   After that it will call *pollDesc's `publishInfo()` method, which while still holding *pollDesc's lock will 
   updates pd.atomicInfo (returned by pd.info) using the other values in pd. It must be called while holding pd.lock,
   and it must be called after changing anything that might affect the info bits. In practice this means after changing closing or changing rd or wd from < 0 to >= 0. In the current case it will just binrary or the `pollClosing` bit
   with the atomic info. 

   TODO!!!!!!!!! add more info about:
   ```go
   info |= uint32(pd.fdseq.Load()&pollFDSeqMask) << pollFDSeq
   ```

   Aftee that it will mark any read and/or write goroutines blocked on the fd as ready to run,
   meaning that *pollDesc's `.rg` and `.wg` goroutine pointers will become nil pointers. The
   returned goroutines (from `.rg` and/or `.wg`) will be stored in local variables just for now.
   If the *pollDesc hand any read or write deadline timers associated with them, they will be deleted
   and reset ton nil in *pollDesc `.rt` and/or `.wt` fields.

   Only now *pollDesc lock can be released, since it will no longer be modified. 

   The runtime will try to put the `.rg` and/or `.wg` goroutines on their corresponding local run queues. 
   They will be put in the _p_.runnext slot (the P in which context's pollDesc.close() was called). 
   If the local run queue is full, the goroutines will be put on the global run queue. After the goroutines 
   are scheduled they will find out that the pollDesc is closed and will return the pollErrClosing error 
   to the caller of waitRead/waitWrite. This error will be transformed to ErrNetClosing during bubbling up
   to higher levels.
   (check out src/runtime/netpoll.go:poll_runtime_pollUnblock() for more details).

3) Removes the underlying os fd from being tracked by the epoll kernel structure.

4) The *pollDesc will be also returned back to the off-go-heap singly list cache.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.evict()

func (pd *pollDesc) evict() {}

Evicts fd from the pending list, unblocking any I/O running on fd.

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   wasn't initialized or is already closed, so we return from this function immediately.

2) src/internal/poll/fd_poll_runtime.go => src/runtime/netpoll.go:poll_runtime_pollUnblock(pd *pollDesc)).
   Under *pollDesc's lock will set its `.closing` field to true. And increment its `.rseq` and `.wseq` fields.

   TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!seq fields

   After that it will call *pollDesc's `publishInfo()` method, which while still holding *pollDesc's lock will 
   updates pd.atomicInfo (returned by pd.info) using the other values in pd. It must be called while holding pd.lock,
   and it must be called after changing anything that might affect the info bits. In practice this means after changing closing or changing rd or wd from < 0 to >= 0. In the current case it will just binrary or the `pollClosing` bit
   with the atomic info. 

   TODO!!!!!!!!! add more info about:
   ```go
   info |= uint32(pd.fdseq.Load()&pollFDSeqMask) << pollFDSeq
   ```

   Aftee that it will mark any read and/or write goroutines blocked on the fd as ready to run,
   meaning that *pollDesc's `.rg` and `.wg` goroutine pointers will become nil pointers. The
   returned goroutines (from `.rg` and/or `.wg`) will be stored in local variables just for now.
   If the *pollDesc hand any read or write deadline timers associated with them, they will be deleted
   and reset ton nil in *pollDesc `.rt` and/or `.wt` fields.

   Only now *pollDesc lock can be released, since it will no longer be modified. 

   The runtime will try to put the `.rg` and/or `.wg` goroutines on their corresponding local run queues. 
   They will be put in the _p_.runnext slot (the P in which context's pollDesc.close() was called). 
   If the local run queue is full, the goroutines will be put on the global run queue. After the goroutines 
   are scheduled they will find out that the pollDesc is closed and will return the pollErrClosing error 
   to the caller of waitRead/waitWrite. This error will be transformed to ErrNetClosing during bubbling up
   to higher levels.
   (check out src/runtime/netpoll.go:poll_runtime_pollUnblock() for more details).


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.prepare()

func (pd *pollDesc) prepare(mode int, isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   wasn't initialized or is already closed, so we return from this function immediately.

2) Prepares a descriptor for polling in mode, which is 'r' or 'w'.
   The underlying fd is always tracked for both read and write events
   so that no additional sys calls should be made each time the mode 
   is requested to be switched. 
   
   Stores a nil pointer in the read/write (rg/wg) fields (depending on the mode specified) 
   instead of a particular goroutine blocked on this particular fd (if any).
   This signifies that no goroutine is currently using this pollDesc to read/write.
   But it may so after resetting the mode.

3) Returns nil if the I/O mode was reset successfully.
   
   Returns ErrNetClosing, os.ErrDeadlineExceeded, src.internal.poll.fd.ErrNotPollable
   if an I/O deadline or an error during scanning for I/O events previously occured.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.prepareRead()

func (pd *pollDesc) prepareRead(isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   wasn't initialized or is already closed, so we return from this function immediately.

2) Prepares a descriptor for polling in read mode.
   The underlying fd is always tracked for both read and write events
   so that no additional sys calls should be made each time the mode 
   is requested to be switched. 

   The switch between modes only requires some user-space attributes to be reset
   on the .runtimeCtx field, which points to an instance of src/runtime/netpoll.go:pollDesc

   Stores a nil pointer instead of a read goroutine blocked on this particular fd (if any).
   This signifies that no goroutine is currently using this pollDesc to read.
   But it may so after resetting the mode.

3) Returns nil if the I/O mode was reset successfully.
   
   Returns ErrNetClosing, os.ErrDeadlineExceeded, src.internal.poll.fd.ErrNotPollable
   if the fd is closed, an I/O deadline or an error during scanning for I/O events 
   previously occured.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.prepareWrite()

func (pd *pollDesc) prepareWrite(isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   wasn't initialized or is already closed, so we return from this function immediately.

2) Prepares a descriptor for polling in write mode.
   The underlying fd is always tracked for both read and write events
   so that no additional sys calls should be made each time the mode 
   is requested to be switched. 

   The switch between modes only requires some user-space attributes to be reset
   on the .runtimeCtx field, which points to an instance of src/runtime/netpoll.go:pollDesc

   Stores a nil pointer instead of a write goroutine blocked on this particular fd (if any).
   This signifies that no goroutine is currently using this pollDesc to write.
   But it may so after resetting the mode.

3) Returns nil if the I/O mode was reset successfully.
   
   Returns ErrNetClosing, os.ErrDeadlineExceeded, src.internal.poll.fd.ErrNotPollable
   if the fd is closed or an I/O deadline previously occured.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.wait() => src/runtime/netpoll.go:poll_runtime_pollWait()

func (pd *pollDesc) wait(mode int, isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   so errors.New("waiting for unsupported file type") error is returned from this method call.

2) Ensures that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events. If one of those conditions is true, then
   ErrNetClosing/ErrFileClosing, or ErrDeadlineExceeded or ErrNotPollable errors
   are returned respectively.

3) If an event is already pending for the requested I/O mode nil (pollNoError) is returned immediately
   to the caller of this method, signifying that the next I/O syscall in the specified mode 
   will not block (will not yield EAGAIN, if there's enough data in the socket buffer).

4) Otherwise we advertise that we're about to block on I/O by atomically setting .runtimeCtx's
   semaphore (`.rg` or `.wg` goroutine pointer fields) to pdWait. 
   If the semaphore (`.rg` or `.wg`) is already set pdWait state of points to a goroutine, it means,
   that someone is already waiting of this poll descriptor to be reado to read or write (depending on th mode),
   so a "runtime: double wait" panic is thrown.

5) Recheks that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events, otherwise a corresponding error will be returned.

6) Before parking the current goroutine, which called this method, tries to atomically
   store a pointer to this goroutine in .runtimeCtx's semaphore (via `netpollblockcommit()` callback)
   After that the count of goroutines blocked on the netpoller is 
   atomically incremented (used by the runtime to skip netpolling if no goroutines are doing I/O).

   If the store of the goroutine in `netpollblockcommit()` succeeds, it means that pd wan't closed, 
   no I/O deadline or scanning error occured just before we're about to park the goroutine.
   Otherwise the current goroutine will not be parked and the pending error will be returned
   to the caller immediately.

7) Parks the currently executing goroutine, suspending this function call until 
   the goroutine is scheduled back either when the requested I/O event occurs, deadline is reached 
   or this pd is closed.

   ```go
   gopark(netpollblockcommit, unsafe.Pointer(gpp), waitReasonIOWait, traceBlockNet, 5)

   // Puts the current goroutine into a waiting state and calls unlockf on the
   // system stack.
   //
   // If unlockf returns false, the goroutine is resumed.
   //
   // unlockf must not access this G's stack, as it may be moved between
   // the call to gopark and the call to unlockf.
   //
   // Note that because unlockf is called after putting the G into a waiting
   // state, the G may have already been readied by the time unlockf is called
   // unless there is external synchronization preventing the G from being
   // readied. If unlockf returns false, it must guarantee that the G cannot be
   // externally readied.
   // Reason explains why the goroutine has been parked. It is displayed in stack
   // traces and heap dumps. Reasons should be unique and descriptive. Do not
   // re-use reasons, add new ones.
   func gopark(
      unlockf func(*g, unsafe.Pointer) bool, 
      lock unsafe.Pointer, 
      reason waitReason, 
      traceEv byte, 
      traceskip int,
    ) {
   	if reason != waitReasonSleep {
   		checkTimeouts() // no-op on every system except JS
   	}

   	// bumps up the count of acquired or pending
   	// to be acquired futex-based mutexes by this M.
   	// Returns the current M.
   	// TODO: 'keeps the garbage collector from being invoked'?
   	// TODO: 'keeps the G from moving to another M?'
   	mp := acquirem()
   	gp := mp.curg // current running goroutine that called gopark
   	status := readgstatus(gp)
   	// if the current goroutine that is about to park itself isn't running user code
   	// or has just received a GC signal to scan its own stack, then it's a runtime error
   	if status != _Grunning && status != _Gscanrunning {
   		throw("gopark: bad g status")
   	}

   	mp.waitlock = lock // in case of a channel gopark, it's a pointer to hchan's lock
   	// TODO:
   	mp.waitunlockf = unlockf     // in case of a channel gopark, it's a callback that does TODO!!!! and unlocks hchan's lock
   	gp.waitreason = reason       // in case of a channel gopark. it's waitReasonChanReceive
   	mp.waittraceev = traceEv     // TODO:
   	mp.waittraceskip = traceskip // TODO:
   	// decreases the count of acquired or pending
   	// to be acquired futex-based mutexes by this M.
   	// TODO: and if there're no acquired or pending mutexes
   	// restores the stack preemption request if one was pending
   	// on the current goroutine.
   	releasem(mp)
   	// can't do anything that might move the G between Ms here.
   	mcall(park_m) // never returns, saves this G's exeuction context and switches to g0 stack to call the passed in function
   }

   // link - src/runtime/asm_amd64.s
   // mcall switches from the g to the g0 stack and invokes fn(g),
   // where g is the goroutine that made the call.
   // mcall saves g's current PC/SP in g->sched so that it can be restored later.
   // It is up to fn to arrange for that later execution, typically by recording
   // g in a data structure, causing something to call ready(g) later.
   // In our case pointer to G is stored in poll.FDs .rg attribute, and poll.FD which 
   // is stored in epoll's event data,
   // so when there's data to be read this parked goroutine can be woken up.
   // mcall returns to the original goroutine g later, when g has been rescheduled.
   // fn must not return at all; typically it ends by calling schedule, to let the m
   // run other goroutines.
   //
   // mcall can only be called from g stacks (not g0, not gsignal).
   //
   // This must NOT be go:noescape: if fn is a stack-allocated closure,
   // fn puts g on a run queue, and g executes before fn returns, the
   // closure will be invalidated while it is still executing.
   func mcall(fn func(*g))

   // park g's continuation on g0.
   // called on g0's system stack.
   // by this time g's rbp, rsp and rip (PC)
   // are saved in g's .sched<gobuf>.
   func park_m(gp *g) {
   	mp := getg().m

   	// TODO!!!!! if trace.enabled {}

   	// N.B. Not using casGToWaiting here because the waitreason is
   	// set by park_m's caller - `gopark()`.
   	// Will loop if the g->atomicstatus is in a Gscan status until the routine that
   	// put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
   	// yielding to the OS scheduler).
   	casgstatus(gp, _Grunning, _Gwaiting)
   	dropg() // sets `gp`s M association to nil + sets M's `.curg` association with `gp` to nil

   	// `mp.waitunlockf` function passed into `gopark()` and saved in M, this function determines if
   	// the goroutine should still be parked (if does not return false), does some additional
   	// bookkeeping depending on the concrete function and unlocks the waitlock, passed in
   	// to `gopark()` and saved in `mp.waitlock`.
   	//
   	// Channel recieve example, function `chanparkcommit`:
   	// 		chanparkcommit is called as part of the `gopark` routine
   	//      after the goroutine's status transfers to `_Gwaiting` and
   	//      the goroutine is dissasociated from the M, which was executing
   	//      it, BUT just before another goroutine gets scheduled by g0.
   	//
   	//      The main responsibility of chanparkcommit is to set goroutine's
   	//      `activeStackChans` attribute to true, which tells the other parts
   	//      of the runtime that there are unlocked sudogs that point into gp's stack (sudog.elem).
   	//      So stack copying must lock the channels of those sudogs.
   	//      In the end chanparkcommit unlocks channel's lock and returns true,
   	//      which tells `gopark` to go on an schedule another goroutine.
   	if fn := mp.waitunlockf; fn != nil {
   		ok := fn(gp, mp.waitlock)
   		mp.waitunlockf = nil
   		mp.waitlock = nil
   		if !ok { // some condition met, so that we don't need to park the goroutine anymore
   			// TODO!!!!! if trace.enabled {}
   			casgstatus(gp, _Gwaiting, _Grunnable) // transition goroutine back to _Grunnable
   			execute(gp, true)                     // Schedule it back, never returns.
   		}
   	}

	   // already on g0, schedule another goroutine
	   schedule()
   ```

8) If this goroutine will be unblocked because of an I/O deadline or pd closure, the corresponding
   error will be returned to the caller.

9) After the requested I/O event is registered by epoll on the undelying fd the goroutine 
   previously executing this function is scheduled back to be run in the near future, thus 
   resuming this method call and returning nil to the caller, signifying that the next I/O syscall 
   in the specified mode will not block.

   TODO!!!!!!!!!!!!!!!!!!!!! add details about how it will be scheduled back.

10) In both cases .runtimeCtx's semaphore is reset to nil.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.waitRead()

func (pd *pollDesc) waitRead(isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   so errors.New("waiting for unsupported file type") error is returned from this method call.

2) Ensures that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events. If one of those conditions is true, then
   ErrNetClosing/ErrFileClosing, or ErrDeadlineExceeded or ErrNotPollable errors
   are returned respectively.

3) If a read event is already pending pollNoError code is returned immediately to the caller of this method, 
   signifying that the next read syscall will not block.

4) Otherwise we advertise that we're about to block on I/O by atomically setting .runtimeCtx's
   semaphore to pdWait. 
   If meanwhile another goroutine blocked on this particular pd in read mode a panic is thrown:
   "runtime: double wait"

5) Recheks that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events, otherwise a corresponding error will be returned.

6) Before parking the current goroutine, which called this method, tries to atomically
   store a pointer to this goroutine in .runtimeCtx's 
      // rg accessed atomically and hold g pointers.
	   // (Using atomic.Uintptr here is similar to using guintptr elsewhere.)
	   rg atomic.Uintptr // pdReady, pdWait, G waiting for read or pdNil
   After that the count of goroutines blocked on the netpoller is 
   atomically incremented (used by the runtime to skip netpolling if no goroutines are doing I/O).

   If the store succeeds, it means that pd wan't closed, no I/O deadline or
   scanning error occured just before we're about to park the goroutine.
   Otherwise the current goroutine will not be parked and the pending error will be returned
   to the caller immediately.

7) Parks the currently executing goroutine, suspending this function call until 
   the goroutine is scheduled back either when a read epoll event occurs, deadline is reached 
   or this pd is closed.

   ```go
   gopark(netpollblockcommit, unsafe.Pointer(gpp), waitReasonIOWait, traceBlockNet, 5)

   // Puts the current goroutine into a waiting state and calls unlockf on the
   // system stack.
   //
   // If unlockf returns false, the goroutine is resumed.
   //
   // unlockf must not access this G's stack, as it may be moved between
   // the call to gopark and the call to unlockf.
   //
   // Note that because unlockf is called after putting the G into a waiting
   // state, the G may have already been readied by the time unlockf is called
   // unless there is external synchronization preventing the G from being
   // readied. If unlockf returns false, it must guarantee that the G cannot be
   // externally readied.
   // Reason explains why the goroutine has been parked. It is displayed in stack
   // traces and heap dumps. Reasons should be unique and descriptive. Do not
   // re-use reasons, add new ones.
   func gopark(
      unlockf func(*g, unsafe.Pointer) bool, 
      lock unsafe.Pointer, 
      reason waitReason, 
      traceEv byte, 
      traceskip int,
    ) {
   	if reason != waitReasonSleep {
   		checkTimeouts() // no-op on every system except JS
   	}

   	// bumps up the count of acquired or pending
   	// to be acquired futex-based mutexes by this M.
   	// Returns the current M.
   	// TODO: 'keeps the garbage collector from being invoked'?
   	// TODO: 'keeps the G from moving to another M?'
   	mp := acquirem()
   	gp := mp.curg // current running goroutine that called gopark
   	status := readgstatus(gp)
   	// if the current goroutine that is about to park itself isn't running user code
   	// or has just received a GC signal to scan its own stack, then it's a runtime error
   	if status != _Grunning && status != _Gscanrunning {
   		throw("gopark: bad g status")
   	}

   	mp.waitlock = lock // in case of a channel gopark, it's a pointer to hchan's lock
   	// TODO:
   	mp.waitunlockf = unlockf     // in case of a channel gopark, it's a callback that does TODO!!!! and unlocks hchan's lock
   	gp.waitreason = reason       // in case of a channel gopark. it's waitReasonChanReceive
   	mp.waittraceev = traceEv     // TODO:
   	mp.waittraceskip = traceskip // TODO:
   	// decreases the count of acquired or pending
   	// to be acquired futex-based mutexes by this M.
   	// TODO: and if there're no acquired or pending mutexes
   	// restores the stack preemption request if one was pending
   	// on the current goroutine.
   	releasem(mp)
   	// can't do anything that might move the G between Ms here.
   	mcall(park_m) // never returns, saves this G's exeuction context and switches to g0 stack to call the passed in function
   }

   // link - src/runtime/asm_amd64.s
   // mcall switches from the g to the g0 stack and invokes fn(g),
   // where g is the goroutine that made the call.
   // mcall saves g's current PC/SP in g->sched so that it can be restored later.
   // It is up to fn to arrange for that later execution, typically by recording
   // g in a data structure, causing something to call ready(g) later.
   // In our case pointer to G is stored in poll.FDs .rg attribute, and poll.FD which 
   // is stored in epoll's event data,
   // so when there's data to be read this parked goroutine can be woken up.
   // mcall returns to the original goroutine g later, when g has been rescheduled.
   // fn must not return at all; typically it ends by calling schedule, to let the m
   // run other goroutines.
   //
   // mcall can only be called from g stacks (not g0, not gsignal).
   //
   // This must NOT be go:noescape: if fn is a stack-allocated closure,
   // fn puts g on a run queue, and g executes before fn returns, the
   // closure will be invalidated while it is still executing.
   func mcall(fn func(*g))

   // park g's continuation on g0.
   // called on g0's system stack.
   // by this time g's rbp, rsp and rip (PC)
   // are saved in g's .sched<gobuf>.
   func park_m(gp *g) {
   	mp := getg().m

   	// TODO!!!!! if trace.enabled {}

   	// N.B. Not using casGToWaiting here because the waitreason is
   	// set by park_m's caller - `gopark()`.
   	// Will loop if the g->atomicstatus is in a Gscan status until the routine that
   	// put it in the Gscan state is finished (spins for 2.5 to 5 microseconds in between
   	// yielding to the OS scheduler).
   	casgstatus(gp, _Grunning, _Gwaiting)
   	dropg() // sets `gp`s M association to nil + sets M's `.curg` association with `gp` to nil

   	// `mp.waitunlockf` function passed into `gopark()` and saved in M, this function determines if
   	// the goroutine should still be parked (if does not return false), does some additional
   	// bookkeeping depending on the concrete function and unlocks the waitlock, passed in
   	// to `gopark()` and saved in `mp.waitlock`.
   	//
   	// Channel recieve example, function `chanparkcommit`:
   	// 		chanparkcommit is called as part of the `gopark` routine
   	//      after the goroutine's status transfers to `_Gwaiting` and
   	//      the goroutine is dissasociated from the M, which was executing
   	//      it, BUT just before another goroutine gets scheduled by g0.
   	//
   	//      The main responsibility of chanparkcommit is to set goroutine's
   	//      `activeStackChans` attribute to true, which tells the other parts
   	//      of the runtime that there are unlocked sudogs that point into gp's stack (sudog.elem).
   	//      So stack copying must lock the channels of those sudogs.
   	//      In the end chanparkcommit unlocks channel's lock and returns true,
   	//      which tells `gopark` to go on an schedule another goroutine.
   	if fn := mp.waitunlockf; fn != nil {
   		ok := fn(gp, mp.waitlock)
   		mp.waitunlockf = nil
   		mp.waitlock = nil
   		if !ok { // some condition met, so that we don't need to park the goroutine anymore
   			// TODO!!!!! if trace.enabled {}
   			casgstatus(gp, _Gwaiting, _Grunnable) // transition goroutine back to _Grunnable
   			execute(gp, true)                     // Schedule it back, never returns.
   		}
   	}

	   // already on g0, schedule another goroutine
	   schedule()
   ```

8) If this goroutine will be unblocked because of an I/O deadline or pd closure, the corresponding
   error will be returned to the caller.

9) After the read event is registered by epoll on the undelying fd the goroutine 
   previously executing this function is scheduled back to be run in the near future, thus 
   resuming this method call and returning true to the caller, signifying that the next I/O syscall 
   will not block.

   TODO!!!!!!!!!!!!!!!!!!!!! add details about how it will be scheduled back.

10) In both cases .runtimeCtx's rg is reset to nil, meaning that there's no current goroutine
    waiting for an EPOLLIN event to occur.



------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.waitWrite() (same as waitRead)

func (pd *pollDesc) waitWrite(isFile bool) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, 
   so errors.New("waiting for unsupported file type") error is returned from this method call.

2) Ensures that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events. If one of those conditions is true, then
   ErrNetClosing/ErrFileClosing, or ErrDeadlineExceeded or ErrNotPollable errors
   are returned respectively.

3) If a write event is already pending nil is returned immediately to the caller of this method, 
   signifying that the next write syscall will not block.

4) Otherwise we advertise that we're about to block on I/O by atomically setting .runtimeCtx's
   semaphore to pdWait. 
   If meanwhile another goroutine blocked on this particular pd in read mode a panic is thrown:
   "runtime: double wait"

5) Recheks that the pd isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events, otherwise a corresponding error will be returned.

6) Before parking the current goroutine, which called this method, tries to atomically
   store a pointer to this goroutine in .runtimeCtx's semaphore.
   After that the count of goroutines blocked on the netpoller is 
   atomically incremented (used by the runtime to skip netpolling if no goroutines are doing I/O).

   If the store succeeds, it means that pd wan't closed, no I/O deadline or
   scanning error occured just before we're about to park the goroutine.
   Otherwise the current goroutine will not be parked and the pending error will be returned
   to the caller immediately.

7) Parks the currently executing goroutine, suspending this function call until 
   the goroutine is scheduled back either when a write epoll event occurs, deadline is reached 
   or this pd is closed.

8) If this goroutine will be unblocked because of an I/O deadline or pd closure, the corresponding
   error will be returned to the caller.

9) After the write event is registered by epoll on the undelying fd the goroutine 
   previously executing this function is scheduled back to be run in the near future, thus 
   resuming this method call and returning nil to the caller, signifying that the next I/O syscall 
   will not block.

10) In both cases .runtimeCtx's semaphore is reset to nil.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:pollDesc.pollable()

func (pd *pollDesc) pollable() bool {}

If pd wasn't initialized or isn't supported by epoll the underlying .runtimeCtx field 
will be nil, so false will be returned from this method.
Otherwise true will be returned.


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:setDeadlineImpl()

func setDeadlineImpl(fd *FD, t time.Time, mode int) error {}

1) If no pointer to a src/runtime/netpoll.go:pollDesc instance is stored in 
   the .runtimeCtx field, it means that pd isn't supported by epoll, pd wasn't initialized
   or is already closed. In this case an ErrNoDeadline error is returned.

2) Converts the passed in deadline into duration from now. If the deadline is already due 
   duration is set to -1. No deadline is 0.

3) Increment reference to fdMutex. TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!

src/runtime/netpoll.go:poll_runtime_pollSetDeadline(pd *pollDesc, d int64, mode int) 

4) Reads the previous value of .runtimeCtx's read/write deadline into a local variable 
   and remembers whether a deadline was previously set.

5) If the passed in deadline is equal to the currently set deadline this method becomes no-op 
   and returns immediately.

6) If the passed in delay (`d`) in nanoseconds is positive then the deadline is calculated by adding the delay
   to the current value of the MONOTONIC clock.

7) Stores the new deadline in .runtimeCtx's corresponding deadline field.

8) If the passed in deadline is 0 or negative no deadline timers are created and 
   the current deadline timers, if any, are deleted.
   In this case the blocked on I/O goroutine, if any, is woken up, the caller of the
   pollDesc.waitRead()/pollDesc.waitWrite() method will get ErrDeadlineExceeded error.

9)  If the deadline timer's callback function is NOT set to nil it means that the previous deadline
   timer is still in progress and it needs to be invalidated. This is done by incrementing 
   .runtimeCtx's .rseq/.wseq field. When the invalidated timer's callback fires

func poll_runtime_pollSetDeadline(pd *pollDesc, d int64, mode int) {}



7) After updating pollDesc's deadlines once again remembers whether the pollDesc had a read 
   and write deadline simultaneously set. This info is stored in a new local variable.

8) If only a read deadline was specified then the 'src/runtime/netpoll.go:netpollReadDeadline()'
   function is chosen as the read deadline timer's callback (pollDesc.rt.f).
   If both read and write deadlines were specified then the 'src/runtime/netpoll.go:netpollDeadline()' 
   function is chosen as the read deadline timer's callback (pollDesc.rt.f). 
   In this case the write deadline timer will not be created because the read and write deadlines 
   will share the same timer and thus the same callback function.
   If only a write deadline was specified then the 'src/runtime/netpoll.go:netpollWriteDeadline()'
   function is chosen as the write deadline timer's callback (pollDesc.wt.f)

9) If read deadline timer's callback function is NOT set to nil, it means that the previous deadline
   timer is still in progress and that the goroutine associated with pollDesc's underlying fd is still
   blocked on I/O. 
   In this case the timer associated with the previous deadline is invalidated by
   incrementing pollDesc's .rseq field. This means when the invalidated timer fires and its callback 
   function is called, it will find out that it's .seq field is not equal to pollDesc's .rseq field, 
   so it will do nothing. In this case the previous callback function will not be called no matter what, 
   because the timer itself will be reset or deleted (if the new deadline is <= 0).
   After that modifies the current deadline timer to fire after the new deadline, with possibly
   a new callback function type and new .seq field.

10) If read deadline timer's callback function is set to nil, it means that we're deadling with a fresh
    pollDesc.
    In this case a new deadline timer is created with the corresponding callback function, current
    pollDesc wrapped into an empty interface as arg and .seq field set to the current value 
    of pollDesc's .rseq field.

11) The same manipulation with deadline timers is repeated if a new write deadline was requested 
    to be set and the current read deadline is not equal to the requested write deadline.
    If the current read deadline is equal to the requested write deadline, they will share the
    same timer callback function, so there's no need to create any write deadline timers.

12) When the deadline timer fires it will call its callback function which will wake up the
    associated goroutine/goroutines if any of them are still blocked on I/O. These goroutines
    will find out that they were unblocked because of an I/O deadline:
      - netpollDeadline(), netpollReadDeadline(), netpollWriteDeadline() funcs all share the same
        implementation through netpolldeadlineimpl()

      - Checks the current value of pollDesc's sequence field against this timer's seq field,
        and if they're not equal, it means that the pollDesc was reused or its deadline
        timer was reset. In this case the callback function will be no-op.
        When the deadline timer was created its arg field was set to pollDesc's sequence field
        value, which was valid at that point in time. If this pollDesc was reused before 
        this deadline timer fired or its deadline was reset, its sequence field got incremented. 
        That's why stale deadline timers can be detected when they fire.
   
      - Otherwise the blocked on I/O goroutines will be added to a list of ready to run goroutines. 
        Later, during the scheduler's iteration, the runtime will try to put it on its corresponding 
        local run queue. It will be put in the _p_.runnext slot, so that this goroutine will get its 
        time slice on M as soon as possible.
        If the local run queue is full, runnext puts this goroutine/goroutines on the global queue. 
        After the goroutine/goroutines unblock, they will find out by checking the 
        pollExpiredWriteDeadline and/or pollExpiredReadDeadline bits, that they were woken up because 
        an I/O deadline occured. So they will return the pollErrTimeout error code to the caller 
        of poll_runtime_pollWait.
   


------------------------------------------------------------------------------------------------------------


src/internal/poll/fd_poll_runtime.go:convertErr()

func convertErr(res int, isFile bool) error {}

Converts src/runtime/netpoll.go errors:

   - pollNoError => nil

   - pollErrClosing => ErrNetClosing or ErrFileClosing if we're dealing with a file

   - pollErrTimeout => os.ErrDeadlineExceeded

   - pollErrNotPollable => src.internal.poll.fd.ErrNotPollable




============================================================================================================




src/runtime/netpoll.go:pollDesc.atomicInfo

Type: type pollInfo uint32

- Holds bits from .closing, .rd, and .wd fields. 
  Duplicates and summarizes state for use by netpollcheckerr.
  The need for state duplication stems from the fact that
  .closing, .rd, and .wd fields can only be written to while holding the pollDesc's lock,
  but netpollcheckerr cannot acquire it, while it still needs to check the
  pollDesc's state.

  After writing to .closing, .rd, and .wd fields, code must call publishInfo
  before releasing the lock. publishInfo sets the bits according to the state
  held in .closing, .rd, and .wd fields. It must be called while holding the lock 
  because the bits are written directly without using atomic operations.

  netpollcheckerr is not allowed to acquire the lock because it's usually called
  when the lock is already held.

- Also holds the pollEventErr bit, recording whether epoll event scanning on the undelying fd
  resulted in an error. atomicInfo is the only source of truth for that bit.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:info()

Signature: func (pd *pollDesc) info() pollInfo {}

Atomically loads the value of pollDesc's .atomicInfo field, which duplicates state in bits
from .closing, .rd, and .wd fields, and is the only source of truth for the pollEventErr bit.
pollEventErr reports whether epoll event scanning on the undelying fd resulted in an error.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:publishInfo()

Signature: func (pd *pollDesc) publishInfo() {}

Atomically sets pollClosing, pollExpiredReadDeadline, pollExpiredWriteDeadline, pollExpiredReadDeadline bits
in pollDesc's .atomicInfo according to the state held in .closing, .rd, and .wd fields.
It must be called while holding pollDesc's lock.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:setEventErr()

Signature: func (pd *pollDesc) setEventErr(b bool) {}

Atomically sets pollEventErr bit (0 or 1) in pollDesc's .atomicInfo according to the boolean
value passed in as a parameter to this function.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollServerInit()

func poll_runtime_pollServerInit() {}

For more details check out src/runtime/netpoll.go:netpollGenericInit().

1) Initializes an epoll control kernel structure, 
   stores its fd in the epfd global variable.

2) Creates a non-blocking unix pipe,
   registers the read end of the pipe with epoll, 
   the corresponding write end will be used to unblock epoll_wait syscalls
   by writing a zero-byte to it, which will trigger an I/O event on the read end.
   Sets the read and write end in netpollBreakRd, netpollBreakWr global variables respectively.

3) Atomically sets the netpollInited global variable flag.



------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollGenericInit()

func netpollGenericInit() {}

1) Atomically checks the global netpollInited flag to see if netpolling was already initialized
   and if it was, returns immediately.

2) Initializes the runtime netpollInitLock mutex with rank 'lockRankNetpollInit', which means that
   only the lockRankTimers mutex can be held at the time of acquiring this netpollInitLock mutex.

3) Acquires the netpollInitLock mutex.

4) Within the criticle section checks whether the global netpollInited flag is set. This is done 
   because before entering the criticle section of this function some other part of the 
   runtime could have inited the netpoller. 
   If some other part of the runtime was quicker to init the netpoller 
   this function releases the netpollInitLock mutex an returns immediately.

5) Calls 'src/runtime/netpoll_epoll.go:netpollinit()' which initializes an epoll kernel structure,
   stores the resulting epoll control fd in the epfd global variable. The epoll control fd is 
   close-on-exec. 
   Creates a non-blocking unix pipe, registers the pipe's read end fd to be tracked 
   by epoll for read events, stores the read and write end fds of the unix pipe in the 
   netpollBreakRd, netpollBreakWr global variables respectively. 
   The read and write end fds of the unix pipe will be used to break indefinite epoll_wait() calls.

6) Atomically sets the netpollInited flag, releases the netpollInitLock mutex and returns.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollinited()

func netpollinited() bool {}

Checks whether the epoll control kernel structure was already initialized and other
preparation steps have been completed.
For details check out src/runtime/netpoll.go:netpollGenericInit().


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_isPollServerDescriptor()

func poll_runtime_isPollServerDescriptor(fd uintptr) bool {}

Reports whether fd is a descriptor being internally used by netpoll.

Returns true if the passed in fd is either epoll's control fd or
unix pipe's read/write end fd used to wake up the netpoller from a blocking epoll_wait syscall.

Otherwise, returns false.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollOpen()

1) Get's a pointer to a pollDesc from a freshly or already allocated pollcache. 
   All of the pollDescs are stored in mmaped non-GC memory. Must be in non-GC memory because 
   can be referenced only from epoll/kqueue internals. Otherwise pollDescs would be prematurely
   collected by the GC, which knows nothing about epoll events referencing those pollDescs. 
   An epoll event references a pollDesc in its .data field.

2) Acquires the lock of the retrieved and reusable pollDesc 
  (the retrieved value is a pointer to pollDesc in mmaped non-GC memory).

3) Atomically loads .rg and .wg semaphores represented as atomic.Uintptrs for atomic access, 
   which can hold:  
     - pdReady - if io readiness notification on the original fd is pending

     - pdWait - if a goroutine is preparing to park on the semaphore, but not yet parked 
                (this means that it's about to block on awaiting for some read/write epoll event 
                 to occure on the underlying fd)

     - 0 (nil pointer) - if no goroutine is currently blocked on I/O, 
                         not preparing to block on I/O or no I/O readiness notification is pending.

     - G pointer - the goroutine is parked and blocked on the semaphore waiting for I/O notification

    If some stale goroutine is about to block on I/O or is already blocked on the supposed to be newly 
    acquired pollDesc from cache, a panic is thrown.
    The panic should be thrown because the code that previously returned this particular pollDesc
    back to cache to be reused with some other underlying fd, had to set the value of the semaphore
    back to nil and schedule the blocked goroutine to run. If at that time a goroutine was preparing 
    to block on the pollDesc the value of the semaphore had to be also set to nil.
    If an I/O notification was already pending at the time of pollDesc's closure, pdReady status should 
    have been left as is. Because of that it's acceptable to have the semaphores to be set
    not only to nil but also to pdReady, because the notification was associated with the old
    underlying fd and has nothing to do with the new one.

4) Sets the pollDesc's .fd field to the underlying os fd passed in to this function as a parameter.

5) Resets pollDesc's .closing field to false and sets the pollEventErr bit in .atomicInfo
   to 0, meaning that no event scanning error occured on the new os fd.

6) Increments pollDesc's .rseq and .wseq fields, which are used to protect 
   from stale read and write timers. The mechanism for this is basically the following:
      When a read/write deadline is set on the pollDesc, the current value of .rseq/.wseq is passed
      into the timer associated with this particular deadline. When the timer fires, a custom callback
      function is called, which checks the timer's .seq field against the current value of pollDesc's
      .rseq/.wseq. If they are not equal, it means that this particular pollDesc was reused or
      deadline was reset and the timer event is no longer valid, so the callback function will do nothing.
      That's why .rseq and .wseq fields are incremented on opening a pollDesc, which could have been 
      previously used with some other underlying os fd, meaning the deadlines for the previous fd 
      are no longer valid for the new fd, so stale timer events should be ignored.

7) Atomically resets .rg and .wg semaphores to 0 (nil pointer), meaning no goroutine is currently blocked 
   on the underlying os fd preparing to wait or is waiting for I/O events to occure. If an I/O notification
   is pending on this pollDesc from a previous fd it will also be ingored by resetting the semaphores.

8) Resets the values of read and write deadlines because no deadline is currently associated with
   the passed in os fd.

9) Stores a reference to this pollDesc in its .self field. This field will be used with read/write
   deadline timers. When creating a timer .self will be converted into an empty interface and passed
   into the timer. When the timer fires a custom callback function will be called with the value of 
   .self. This custom callback function happens to be one of the following:
      netpollDeadline, 
      netpollReadDeadline 
      or netpollWriteDeadline. 
   These callbacks will convert .self back to a *pollDesc, check if the timer is not stale, in which case 
   will unblock the goroutine waiting for I/O by scheduling it to run. When the blocked goroutine is
   scheduled back to run, it will recognize that a read/write deadline has occured by examining
   the deadline values which will be negative in this case.
   

10) Sets pollClosing, pollExpiredReadDeadline, pollExpiredWriteDeadline, pollExpiredReadDeadline bits to 0
    in pollDesc's .atomicInfo field according to the state held in .closing, .rd, and .wd fields, which
    are currently set to false and 0.

11) Releases pollDesc's lock.

12) Registers the underlying os fd via epoll_ctl() in edge-triggered mode to be 
    simultaneously polled for read, write and peer shutdown events*. 
    This is cheaper than making syscalls every time we want to switch an fd 
    to be polled for some other type of event. 
    So the process of recognizing which events are we currently tracking is deligated to 
    pollDesc and its .rg and .wg fields.

    Before calling epoll_ctl() an unaligned pointer to the pollDesc is stored 
    in the epoll event's data field. This pointer will be later passed in to a callback function 
    which fires when an event is registered on the underlying fd (netpollready).
    This function will add the goroutine that was blocked on the underlying fd 
    to a list of ready to run goroutines, and later, during the scheduler's iteration, 
    the goroutine will be added to either a local or global run queue.
    
    * A peer shutdown event occurs when the other side closes the connection.

13) If an error occurs while calling epoll_ctl(), then the pollDesc is freed back to pollCache and 
    the errno is returned to the caller of this method.
    Otherwise this method returns the set up and ready for use pollDesc being already tracked 
    by epoll.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollClose()

func poll_runtime_pollClose(pd *pollDesc) {}

1) Receives a pointer to a pollDesc instance residing in non-GC mmapped memory 
   as this method's only argument.

2) If unblock wasn't previously called with this pollDesc, which should have 
   put the blocked on I/O goroutine (goroutines) on the local or global run queue
   then a panic is thrown (this is checked using the .closing flag). Without
   scheduling the blocked goroutine (goroutines), which are reading and/or writing to a socket,
   will not know, tha the underlying fd is no longer polled for I/O, 
   this could also lead to goroutine leaks.
   Unblock should have also set this pollDesc's .rg and .wg semaphores to either pdReady
   or nil so that it can be reused in cache. If unblock wasn't called, then a panic is thrown.

3) Removes the underlying fd from being tracked by the epoll kernel structure

5) Returns the pointer to pollDesc residing in non-GC mmapped memory to cache.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:free() 

func (c *pollCache) free(pd *pollDesc) {}

Checkout src/runtime/netpoll.go:pollCache.alloc() for details on 
how pollDescs are allocated from cache.

1) Within a locked section links the pollDesc to the current head of 
   the singly-linked list cache and promotes this pollDesc to be the current head.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollReset()

func poll_runtime_pollReset(pd *pollDesc, mode int) int {}

Prepares a descriptor for polling in mode, which is 'r' or 'w'.
The underlying fd is always tracked for both read and write events
so that no additional sys calls should be made each time the mode 
is requested to be switched. 

1) Checks that the passed in pollDesc isn't closed, hasn't timed out
   and no error occured during scanning for I/O.
   If one of these scenarios occured then pollErrClosing, pollErrTimeout or 
   pollErrNotPollable error codes are returned respectively.

2) If everything is ok a nil pointer is stored either in the .rg or .wg semaphores
   depending on the mode passed in as a parameter to this function. 
   This signifies that no goroutine is currently using this pollDesc to read and/or write.
   But it may so after reseting the mode.

3) Returns pollNoError code


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollWait()

func poll_runtime_pollWait(pd *pollDesc, mode int) int {}

1) Ensures that the passed in pollDesc isn't closed, hasn't timed out on I/O and no error occured 
   during scanning for epoll events. If one of those conditions is true, then pollErrClosing, 
   pollErrTimeout or pollErrNotPollable error codes are returned respectively.

2) If an event is already pending for the requested I/O mode a pollNoError code is returned
   to the caller immediately.

3) Otherwise the pollDesc's read/write semaphore is atomically set to pdWait, singalling that 
   this goroutine is about to block on I/O in the requested mode.

4) If in between setting the semaphore's value to pdWait and just before the currently executing 
   goroutine is parked, some other goroutine blocked on awaiting the same type of I/O event,
   a panic is thrown "runtime: double wait".
   This goroutine will try to block until an I/O event occurs, deadline is reached 
   or the pollDesc is closed. This is done without actually trying to park the goroutine,
   except for then event is consumed by the other goroutine.

5) Parks the currently executing goroutine, suspending this function call until 
   the goroutine is scheduled back either when the requested I/O event occurs, deadline is reached 
   or pollDesc is closed. Before yielding control, the count of goroutines blocked on the netpoller is 
   atomically incremented (netpollWaiters global variable).

6) After the requested I/O event is registered by epoll on the undelying fd
   the goroutine previously executing this function is scheduled back to be run
   in the near future thus resuming this function call.

7) After requested I/O event is registered by epoll on the undelying fd, a deadline occurs or 
   pollDesc is closed prematurely, this function returns a boolean value which will be true
   if the goroutine was unblocked by the requested I/O event. In all cases the read/write
   semaphore is set back to nil.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollSetDeadline()

func poll_runtime_pollSetDeadline(pd *pollDesc, d int64, mode int) {}

1) Acquires pollDesc's lock because its fields will be mutated.
   If this pollDesc is marked as closing the lock is immediately released and 
   this function returns immediately.

2) Reads the previous values of pollDesc's read and write deadlines
   into local variables and remembers whether the pollDesc has a read and write deadline 
   previously and simultaneously set.

3) If the passed in deadline is equal to the currently set deadline returns immediately.

4) If the passed in new deadline is positive then a small nanoseconds jitter is added to it.
   
5) Depending on the mode passed in saves the new deadline in pollDesc's .rd and/or .wd
   fields.

6) If the deadline passed in is 0 or negative, then the pollExpiredWriteDeadline and/or 
   pollExpiredReadDeadline bits are set to 1 in pollDesc's .atomicInfo field. In this case 
   no deadline timers are created and the current deadline timers if any are deleted. 
   All goroutines if any blocked on I/O will be immediately unblocked, in which case they will 
   acknowledge that a read/write deadline has occured. Also pollDesc's lock will 
   be released immediately. 
   Else the pollExpiredWriteDeadline and/or pollExpiredReadDeadline bits are set to 0, 
   meaning no read and/or write deadlines have yet occured.

7) After updating pollDesc's deadlines once again remembers whether the pollDesc had a read 
   and write deadline simultaneously set. This info is stored in a new local variable.

8) If only a read deadline was specified then the 'src/runtime/netpoll.go:netpollReadDeadline()'
   function is chosen as the read deadline timer's callback (pollDesc.rt.f).
   If both read and write deadlines were specified then the 'src/runtime/netpoll.go:netpollDeadline()' 
   function is chosen as the read deadline timer's callback (pollDesc.rt.f). 
   In this case the write deadline timer will not be created because the read and write deadlines 
   will share the same timer and thus the same callback function.
   If only a write deadline was specified then the 'src/runtime/netpoll.go:netpollWriteDeadline()'
   function is chosen as the write deadline timer's callback (pollDesc.wt.f)

9) If read deadline timer's callback function is NOT set to nil, it means that the previous deadline
   timer is still in progress and that the goroutine associated with pollDesc's underlying fd is still
   blocked on I/O. 
   In this case the timer associated with the previous deadline is invalidated by
   incrementing pollDesc's .rseq field. This means when the invalidated timer fires and its callback 
   function is called, it will find out that it's .seq field is not equal to pollDesc's .rseq field, 
   so it will do nothing. In this case the previous callback function will not be called no matter what, 
   because the timer itself will be reset or deleted (if the new deadline is <= 0).
   After that modifies the current deadline timer to fire after the new deadline, with possibly
   a new callback function type and new .seq field.

10) If read deadline timer's callback function is set to nil, it means that we're deadling with a fresh
    pollDesc.
    In this case a new deadline timer is created with the corresponding callback function, current
    pollDesc wrapped into an empty interface as arg and .seq field set to the current value 
    of pollDesc's .rseq field.

11) The same manipulation with deadline timers is repeated if a new write deadline was requested 
    to be set and the current read deadline is not equal to the requested write deadline.
    If the current read deadline is equal to the requested write deadline, they will share the
    same timer callback function, so there's no need to create any write deadline timers.

12) When the deadline timer fires it will call its callback function which will wake up the
    associated goroutine/goroutines if any of them are still blocked on I/O. These goroutines
    will find out that they were unblocked because of an I/O deadline:
      - netpollDeadline(), netpollReadDeadline(), netpollWriteDeadline() funcs all share the same
        implementation through netpolldeadlineimpl()

      - Checks the current value of pollDesc's sequence field against this timer's seq field,
        and if they're not equal, it means that the pollDesc was reused or its deadline
        timer was reset. In this case the callback function will be no-op.
        When the deadline timer was created its arg field was set to pollDesc's sequence field
        value, which was valid at that point in time. If this pollDesc was reused before 
        this deadline timer fired or its deadline was reset, its sequence field got incremented. 
        That's why stale deadline timers can be detected when they fire.
   
      - Otherwise the blocked on I/O goroutines will be added to a list of ready to run goroutines. 
        Later, during the scheduler's iteration, the runtime will try to put it on its corresponding 
        local run queue. It will be put in the _p_.runnext slot, so that this goroutine will get its 
        time slice on M as soon as possible.
        If the local run queue is full, runnext puts this goroutine/goroutines on the global queue. 
        After the goroutine/goroutines unblock, they will find out by checking the 
        pollExpiredWriteDeadline and/or pollExpiredReadDeadline bits, that they were woken up because 
        an I/O deadline occured. So they will return the pollErrTimeout error code to the caller 
        of poll_runtime_pollWait.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:poll_runtime_pollUnblock()

func poll_runtime_pollUnblock(pd *pollDesc) {}

1) Receives a pointer to pollDesc instance residing in non-GC mmapped memory 
   as this method's only argument.

2) Acquires the pollDesc's lock because its fields will be read and mutated 
   (.closing, .rseq, .wseq, .atomicInfo, .rt, .wt)

3) If pollDesc is already closing or closed then a panic is thrown

4) Marks the pollDesc as closing by setting its .closing field to true

5) Increments pollDesc's .rseq and .wseq fields, which are used to protect 
   from stale read and write timers. The mechanism for this is basically the following:
      When a read/write deadline is set on the pollDesc, the current value of .rseq/.wseq is passed
      into the timer associated with this particular deadline. When the timer fires, a custom callback
      function is called, which checks the timer's .seq field against the current value of pollDesc's
      .rseq/.wseq. If they are not equal, it means that this particular pollDesc was reused
      and the timer event is no longer valid. That's why .rseq and .wseq fields are incremented
      on unblocking a pollDesc, so that if a deadline timer fires, its callback function
      will do nothing to wake up the blocked goroutines, because this function has
      already unblocked them (check out step 9 of this function for more details).

6) Sets pollClosing bit to 1 in pollDesc's .atomicInfo field according 
   to the state held in pollDesc's .closing attribute.

7) Resets both .rg and .wg semaphores to nil and extracts pointers 
   to read/write goroutines if any are currently blocked on I/O.

8) Marks pollDesc's .rt and .wt read/write deadline timers as to be deleted from the 
   timers heap in the near future.
   The timer may be on some other P, so we can't actually remove it from the timers heap. 
   We can only mark it as deleted. It will be removed in due course by the P whose heap 
   it is on.

9) If either a read/write goroutine or both were previously blocked on the underlying fd, 
   then we atomically decrement the global variable netpollWaiters for each of these goroutines. 
   After that the goroutines will be marked as ready to run. The runtime will try to put
   them on their corresponding local run queues. They will be put in the _p_.runnext slot.
   If the local run queue is full, runnext puts the goroutines on the global run queue.
   After the goroutines are scheduled they will find out that the pollDesc is closed and will
   return the pollErrClosing error code to the caller of poll_runtime_pollWait.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollready() 

/go:nowritebarrier
func netpollready(toRun *gList, pd *pollDesc, mode int32) {}

Is called by the platform-specific netpoll function, 
which in case of Linux uses epoll_wait to poll and get a list 
of I/O ready fds and their corresponding pollDescs.

Declares that the fd associated with pollDesc is ready for I/O.
The toRun argument is used to build a list of goroutines to return
from the platform-specific netpoll function used by the runtime (this
function is called while iterating over events, and the gList is build
up during the iterations).

The mode argument is 'r', 'w', or 'r'+'w' to indicate
whether the fd is ready for reading or writing or both.

This may run while the world is stopped, so write barriers are not allowed. TODO!!!!!

1) Is called for each fd and its associated pollDesc after runtime polls for I/O
   using epoll_wait.

2) Depending on the I/O mode/modes the fd is being tracked for, retrieves the value of 
   pollDesc's semaphore (or sempahores, if both read and write events are being tracked on the fd).

3) In a spinning lock loop pattern based on atomic operations and 
   the compare and swap operation in particular for each I/O mode specified:
   - Checks the current value of the semaphore and if it's equal to pdReady, returns a nil goroutine
     pointer immediately, because someone did it already.
     If it's equal to a nil pointer, meaning that no goroutine is currently blocked on the semaphore
     waiting for I/O, and I/O readiness status wasn't requested, this function returns a nil goroutine
     pointer immediately.
   - Sets the samphore to pdReady.
   - Returns the pointer to the goroutine which was blocked on I/O.

4) The pointers to read and/or write goroutines are added to *gList which will be used by the runtime,
   which called the platform-specific netpoll function, to schedule blocked on I/O goroutines.
   After unblocking the goroutines will find out that their pollDescs are in pdReady I/O status, 
   so they can perform a non-blocking I/O operation.
   If no goroutines were blocked on the fd, the nothing will be scheduled for this particular
   pollDesc.

   Basically if a read and write event occured on the underlying fd, but the pollDesc is tracked by
   the runtime only for read events, a nil goroutine will be returned for the write mode, so 
   in the end only the read goroutine will be unblocked to do non-blocking I/O.
   This is how mode switching happens on the application level to get rid of additional epoll_ctl 
   syscalls, to switch between polling modes on the underlying OS fd.



------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollcheckerr() 

func netpollcheckerr(pd *pollDesc, mode int32) int {}

1) If pollDesc is about to be closed or is already closed, meaning the underlying fd
   is no longer tracked for I/O events by the epoll kernel structure, then error code
   pollErrClosing is returned from this function.

2) If read or write deadline was reached then pollErrTimeout error code 
   is returned from this function.

3) If an error previously occured while scanning for read I/O events on the underlying fd, then 
   pollErrNotPollable error code is returned from this function.
   No need to do this on write event scanning errors because they will be captured 
   in a subsequent write call (write syscall) that is able to report a more specific error.
   This subsequent write call will not return syscall.EAGAIN but rather the write error directly.
   The subsequent write call will happen immediately after the goroutine is unblocked, because the
   generall pattern for async I/O is:
      1. Try I/O
      2. If EAGAIN is returned, block on awating I/O event
      3. Try I/O again

4) If everything is ok pollNoError code is returned from this function


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollblockcommit() 

func netpollblockcommit(gp *g, gpp unsafe.Pointer) bool {}

1) Is used as a setup callback before parking a goroutine to wait for an I/O event to occur.

2) Atomically increments the count of goroutines waiting for the poller.
   The scheduler uses this to decide whether to block
   waiting for the poller if there is nothing else to do.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollgoready() 

func netpollgoready(gp *g, traceskip int) {}

Is called by:
   - src/runtime/netpoll.go:poll_runtime_pollSetDeadline()
   - src/runtime/netpoll.go:poll_runtime_pollUnblock()
   - src/runtime/netpoll.go:netpolldeadlineimpl() which is called as callback 
     when an I/O deadline timer triggers

Is used by these functions to schedule blocked on I/O goroutines.

1) Atomically decrement the netpollWaiters global variable. It is incremented every time before
   a goroutine is about to block on I/O and is used by the runtime to decide whether to poll for
   I/O or not. If netpollWaiters is 0, net polling will be skipped.

2)  Marks the passed in goroutine as ready to run. During the scheduler's next iteration, 
    the runtime will try to put it on its corresponding local run queue. It will be put in 
    the _p_.runnext slot, so that this goroutine will get its time slice on M as soon as possible.

   If the local run queue is full, runnext puts this goroutine on the global queue. 
   The goroutine will be unblocked and run on M, when it will be picked up from the local
   or global run queue.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollblock() 

func netpollblock(pd *pollDesc, mode int32, waitio bool) bool {}

Returns true if IO is ready, or false if timedout or closed.
waitio - wait only for completed IO and ignore any errors.
When waitio is false then the goroutine calling this will not be parked and errors
will not be checked, false will be returned directly if no event is pending on the pollDesc, 
otherwise true will be returned. pollDesc's semaphore will be also set to nil.

1) Depending on the I/O mode requested to be blocked on, retrieves 
   the value of pollDesc's semaphore.

2) If the requested type of I/O event is already pending on this fd, sets the semaphore
   to nil and returns.

3) Otherwise uses a spinning lock pattern to atomically set the semaphore's value to pdWait. 
   This marks the pollDesc as one on which a goroutine is about to block waiting for an I/O event.

4) If waitio is true and no errors previosuly occured on this pollDesc, parks the goroutine 
   suspending this function call until the goroutine is scheduled back 
   when an I/O event or deadline occurs or pollDesc is closed.  
   Before yielding control the count of goroutines waiting for the poller is also 
   atomically incremented (netpollWaiters global variable).
   If in between setting the sempahore's value to pdWait and parking this goroutine,
   some other goroutine blocked on waiting for the same type of I/O event, the goroutine
   will resume executing this function immediately without yielding control. False will
   be returned. This could also happen if a deadline timer fires in between setting pollDesc's semaphore
   to nil.

5) After the awaited I/O event is registered by epoll on the undelying fd 
   or a race condition described in the previous step occurs, this goroutine will
   be unblocked/unparked and scheduled to be executed in the near future.

6) After this function resumes execution, if this goroutine was unblocked because an I/O event occured, 
   pollDesc's semaphore is atomically set to nil and true is returned.
   If this goroutine was unblocked for a different reason, false is returned.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpollunblock() 

func netpollunblock(pd *pollDesc, mode int32, ioready bool) *g {}

1) Depending on the I/O mode requested to be blocked on, retrieves 
   the value of pollDesc's semaphore.

2) In a spinning lock loop pattern based on atomic operations and 
   the compare and swap operation in particular:
      - Checks the current value of the semaphore and if it's equal to pdReady, returns a nil goroutine
        pointer immediately, because some previous call to this function did it already.
        If it's equal to a nil pointer, meaning that no goroutine is currently blocked on the semaphore
        waiting for I/O, and I/O readiness status wasn't requested, this function returns a nil goroutine
        pointer immediately.
      - Sets the samphore to pdReady if I/O readiness status was requested or to nil if not.
      - Returns the pointer to the goroutine which was blocked on I/O or a nil goroutine pointer.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:netpolldeadlineimpl() 
Signature: func netpolldeadlineimpl(pd *pollDesc, seq uintptr, read, write bool) {}

src/runtime/netpoll.go:netpollDeadline() 
Signature: func netpollDeadline(arg any, seq uintptr) {}

src/runtime/netpoll.go:netpollReadDeadline()
Signature: func netpollDeadline(arg any, seq uintptr) {}

src/runtime/netpoll.go:netpollWriteDeadline() 
Signature: func netpollDeadline(arg any, seq uintptr) {}

1) netpollDeadline(), netpollReadDeadline(), netpollWriteDeadline() funcs all share the same
   implementation through netpolldeadlineimpl(), the only difference being the combination of
   read and write boolean arguments passed into it.
      - netpollDeadline() - calls netpolldeadlineimpl() with read and write arguments both set to true
      - netpollReadDeadline() - calls netpolldeadlineimpl() with only the read argument set to true
      - netpollWriteDeadline() - calls netpolldeadlineimpl() with only the write argument set to true

   They also share the same preparation step before calling on netpolldeadlineimpl().
   This step is the act of converting the arg interface{} passed in by the deadline timer
   when it fires into a pointer to a pollDesc instance. The pollDesc being the instance to
   which the deadline timer belongs.

2) netpolldeadlineimpl() acquires the pollDesc's lock because its fields will be mutated and read.
   The fields to be mutated are:
      - .rd and/or .wd deadlines depending on the modes requested
      - .atomicInfo
      - .rg and/or .wg goroutine semaphores

3) Depending on the mode/modes passed in chooses against which pollDesc's sequence field
   to check the sequence provided by the fired timer.
      - If the mode requested is read or read + write, then pollDesc's .rseq field is chosen
      - If only write mode was requested, then pollDesc's .wseq field is chosen

4) If pollDesc's sequence field/fields are not equal to deadline timer's seq field, it means
   that this particular pollDesc was reused with some other underlying fd or its deadline timer/timers
   were reset. When the deadline timer was created its arg field was set to pollDesc's sequence field
   value, which was valid at that point in time. If this pollDesc was reused before 
   this deadline timer fired or its deadline was reset, its sequence field got incremented. 
   So when this callback function fires, belonging to the old deadline timer, the arg value 
   and pollDesc's sequence field value will differ.
   In this case pollDesc's lock is released and this callback returns immediately, because
   its sequence of operations was to be executed with the old pollDesc or when its old deadline
   elapsed, but it's not the case anymore -  this deadline timer is stale.

5) Depending on the mode specified, pollDesc's read and/or write deadlines are set to -1, so that when
   the goroutine/goroutines blocked on this pollDesc get scheduled, they will find out that they
   were woken up not because of an I/O event, but because their I/O deadline elapsed.

5) Duplicates deadline state by setting the pollExpiredWriteDeadline and/or 
   pollExpiredReadDeadline bits to 1 in pollDesc's .atomicInfo field. These bits will be used as 
   negative read/write deadlines in the previous step to notify the blocked goroutine that it was 
   woken up because its I/O deadline has elapsed.

6) Atomically sets pollDesc's binary semaphore value to nil and returns pointers to blocked goroutines
   if any.

7) Release pollDesc's lock - no fields will be read or mutated further on.

8) Adds the goroutine/goroutines, that were blocked on the underlying fd to a list of ready to 
   run goroutines. Later, during the scheduler's iteration, the runtime will try to put it 
   on its corresponding local run queue. It will be put in the _p_.runnext slot, so that
   this goroutine will get its time slice on M as soon as possible.
   If the local run queue is full, runnext puts this goroutine/goroutines on the global queue. 
   After the goroutine/goroutines unblock, they will find out by checking the pollExpiredWriteDeadline 
   and/or pollExpiredReadDeadline bits, that they were woken up because an I/O deadline occured.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:pollCache.alloc() 

func (c *pollCache) alloc() *pollDesc {}

pollCache is a singly-linked list with head at '.first' and consiting of *pollDesc nodes, each pointing
to the next *pollDesc using its '.link' field.

pollCache is allocated in mmaped non-GC memory, because pollDesc's at some point in time are 
referenced only from epoll/kqueue internals.

1) Aquires the cache mutex 

2) Checks whether the head of the cache is nil:
     - nil means that the cache is empty
     - not nil means there's at least one pollDesc in the cache

3) If the cache is empty or all pollDescs are currently in use, allocates a new chunck of cache memory:
      - Calculates the number of pollDescs which could be stored in the cache 
        by deviding 4KB (pollBlockSize const) by the size of a pollDesc.
        If the result is 0, then only 1 pollDesc can be stored in the cache block.

      - Allocates a zeroed chunk of memory with mmap. The chunk will be about 
        4KB (pollBlockSize const) in size. 
        This memory is allocated directly from the OS, and thus is a raw piece of memory which is non-GC. 
        Must be in non-GC memory because pollDesc's at some point in time are referenced only 
        from epoll/kqueue internals.

      - A pointer to the mmaped memory chunk's start address is returned. 
        Using this address and pointer arithmetic all of the available mmaped memory is filled with 
        pollDescs, all linked into a singly-linked list. 
        Pointer to the first pollDesc is stored in the cache's .first field (singly-linked list head).

4) Because cache allocation could happen if all pollDescs are currently in use, the cache size could
   grow from 4KB to 8KB, 12KB and so on.

5) The pointer to the first element of the singly-linked list cache is retrieved,
   and the pointer to the next pollDesc becomes the head ('.first'), restoring 
   the single-linked list structure.

5) The retrieved *pollDesc's lock is initialized with a rank of lockRankPollDesc, which means
   that no runtime lock can be held when acquiring this lock.

6) Releases the cache mutex

7) Returns the retrieved pointer to pollDesc


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:makeArg() 

func (pd *pollDesc) makeArg() (i any) {}

Converts *pollDesc to an interface{}.
Does not do any allocation. Normally, such
a conversion requires an allocation because pointers to
go:notinheap types (which pollDesc is) must be stored
in interfaces indirectly.

Is used by src/runtime/netpoll.go:poll_runtime_pollSetDeadline() to store *pollDesc in I/O 
deadline timer's arg attribute which is of type any. After the deadline timer fires
arg will be passed into its callback function and unwrapped into a *pollDesc.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll.go:pdEface
src/runtime/netpoll.go:pdType

Are used to by src/runtime/netpoll.go:makeArg() to add type info about *pollDesc 
to the empty interface it will be converted to.


------------------------------------------------------------------------------------------------------------




============================================================================================================



   
src/runtime/netpoll_epoll.go:netpollinit()

func netpollinit() {}

1) Initializes an epoll kernel structure and receives its control fd:
    - First tries to do the epoll_create1() system call (go's epollcreate1 is written in assembly),
      which has the benefit of atomically setting the resulting control fd as close-on-exec.
      By passing the close-on-exec flag to epollcreate1, we specify that the fd should be closed 
      when a new executable overlays a child process. The flag passing guarantees that there will 
      be no race condition - the use of this flag is essential in some multithreaded programs, 
      because using a separate fcntl(2) F_SETFD operation to set the FD_CLOEXEC flag does not
      suffice to avoid race conditions where one thread opens a file descriptor and attempts to set 
      its close-on-exec flag using fcntl(2) at the same time as another thread 
      does a fork(2) plus execve(2).

    - If the call to epoll_create1() fails, tries to create the epoll kernel structure using 
      epoll_create() system call (go's epollcreate is written in assembly). 
      The drawback of this system call is that to make the control fd close-on-exec, a separate fcntl(2) 
      system call should be made, which is prone to race conditions.

    - If none of the system calls succeed a panic is be thrown.

    - Assigns the resulting epoll control fd to the 'epfd' global variable

2) Calls src/runtime/nbpipe_pipe2.go:nonblockingPipe() to create a unix pipe in non-blocking mode.
   This in turn calls the pipe2() system call with O_CLOEXEC and O_NONBLOCK flags. 
   O_NONBLOCK marks the fd for non-blocking async I/O and O_CLOEXEC marks it as close-on-exec.
   This call returns a read and write fd. Read end of the pipe will be tracked by epoll for read events 
   in level-triggered mode, meaning that read events will keep being reported until the data is read 
   to completion. If epoll_wait doesn't block, it means that there's no need to read data from the 
   pipe, because epoll_wait doesn't require to be woken up from a blocking call.
   Next time epoll_wait wakes enters a blocking poll, the same read event will be reported 
   on the read end of the unix pipe, and this time the data will be read to completetion, 
   so that the next epoll_wait blocking call will not be unblocked by a level-triggered 
   event on the unix pipe.
   Basically if epoll_wait blocks indefinitely the write end of the pipe can be used to wake up 
   the epoll_wait call.
   
3) Instantiates an epoll EPOLLIN event and stores an unaligned pointer to netpollBreakRd 
   global variable in it's .data attribute. netpollBreakRd will hold the read end of the unix pipe 
   fd by the time this function returns.
   Pointer to netpollBreakRd global variable is stored in event's .data attribute as an array of 8 bytes 
   on both 32 and 64 bit architectures. Represents an unaligned pointer on a 64 bit architecture, 
   because the .events field of the event structure comes before the .data field and is 4 bytes in
   size (uint32).
   If the pointer would be alligned on a 64 bit architecture then the .events field would consume
   8 bytes of memory because of a 4 byte padding.
   So we get a structure worth of 12 bytes in size instead of a 16 bytes structure. 

   Unaligned memory access usually consumes more time (not on modern intel architectures and others), 
   but it allows more efficient use of the available memory. 
   In practice there is no evidence that unaligned data processing could be several times slower. 
   On a cheap Core 2 processor, there is a difference of about 10% in some tests. 
   On a more recent processor (Core i7), there is no measurable difference.

   On recent Intel processors (Sandy Bridge and Nehalem), there is no performance penalty for 
   reading or writing misaligned memory operands according to Agner Fog. 
   There might be more of a difference on some AMD processors, but the busy AMD server 
   that was tested showed no measurable penalty due to data alignment.

   Claim: On recent Intel and 64-bit ARM processors, data alignment does not make processing 
   a lot faster. It is a micro-optimization. Data alignment for speed is a myth.

4) Makes an epoll_ctl (go's epollctl is written in assembly) system call with the EPOLL_CTL_ADD command 
   flag, which registers the unix pipe's read end fd to be tracked for read events 
   in level-triggered mode (EPOLLIN).
   A pointer to netpollBreakRd global variable is stored in event's .data attribute, 
   which specifies data that the kernel should save and then return on event scanning (epoll_wait).
  
   The pointer to netpollBreakRd global variable is stored in event's .data attribute, so that
   on event scanning, when an event is registered on the netpollBreakRd fd, it can be distinguished from
   other fds, which became ready during this particular scanning iteration. The netpollBreakRd is
   skipped on non-blocking epoll_wait calls without processing it, because it is of no use
   during a non-blocking call. On the other hand during a blocking epoll_wait call, the daga from 
   netpollBreakRd is read to completion so that no more level-triggered events occur on subsequent
   events scanning.

5) Stores read and write end fds of the unix pipe in netpollBreakRd and netpollBreakWr global
   variables respectively to be accessible from anywhere in the runtime.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll_epoll.go:netpollIsPollDescriptor()

func netpollIsPollDescriptor(fd uintptr) bool {}

Returns true if the passed in fd is either epoll's control fd or
unix pipe's read/write end fd used to wake up the netpoller from a blocking scan call.

Otherwise, returns false.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll_epoll.go:netpollopen()

func netpollopen(fd uintptr, pd *pollDesc) int32 {}


1) Intializes an epoll event in edge-triggered mode to be tracked for read events, 
   write events, peer shutdown event (the other side closed the connection).
   
   Event flags: EPOLLIN|EPOLLOUT|EPOLLRDHUP|EPOLLET

   1. EPOLLET flag forces epoll to treat this event as an edge-triggered event which is best described
      by comparing edge- and level-triggered events:
         Edge triggered
         --------------
            - fd is added to epoll with EPOLLIN|EPOLLET flags
            - epoll_wait() blocks waiting for an event to happen
            - we write 19 bytes into the fd
            - epoll_wait() unblocks with an EPOLLIN event
            - we ignore the incoming data
            - epoll_wait() blocks waiting for an event to happen
            - we write 19 bytes into the fd
            - epoll_wait() unblocks with an EPOLLIN event
            - epoll_wait() blocks waiting for an event to happen

         Level triggered
         ---------------
         - fd is added to epoll with the EPOLLIN flag (without EPOLLET)
         - epoll_wait() blocks waiting for an event to happen
         - we write 19 bytes into the fd
         - epoll_wait() unblocks with an EPOLLIN event
         - we ignore the incoming data
         - epoll_wait() unblocks with an EPOLLIN event
   
      More on this:
         https://man7.org/linux/man-pages/man7/epoll.7.html
         https://habr.com/ru/post/416669/

   2. EPOLLIN|EPOLLOUT - read and write events
      Supplying these flags together helps with reducing the amount of system calls 
      when switching from read to write operations on a non-blocking fd.

      When used as an edge-triggered interface, for performance
      reasons, it is possible to add the file descriptor inside the
      epoll interface (EPOLL_CTL_ADD) once by specifying
      (EPOLLIN|EPOLLOUT).  This allows you to avoid continuously
      switching between EPOLLIN and EPOLLOUT calling epoll_ctl() with
      EPOLL_CTL_MOD.

   3. EPOLLRDHUP - means peer shutdown (other side closed the connection). 
      On the contrary EPOLLHUP signals an unexpected close of the socket, 
      i.e. usually an internal error.

2) Stores unaligned pointer to passed in pollDesc in event's data attribute to be later retrieved
   on event occurance on the fd.
   It will be used: 
      - to distinguish between read events occuring on the unix pipe, 
        used to wake up the netpoller from a blocking scan call, and events occuring
        on regular fds, which should be processed by the netpoller.
      - to mark pollDescs as ready for non-blocking I/O and schedule blocked goroutines associated
        with them to continue their execution. 
        The goroutines will return pollNoError from their src/runtime/netpoll.go:poll_runtime_pollWait()
        calls if the underlying fd is ready for non-blocking I/O, otherwise they will return error codes.

3) Adds the fd via epoll_ctl() to be tracked for the above mentioned events and returns 
   the negative code returned by epoll_ctl().
   The resulting code is negated before being returned, because on Linux, 
   a failed system call using the SYSCALL assembly instruction will return the value -errno in 
   the rax register, so we need to negate the code to transform it into a valid errno.
   If everyting went well, a 0 code is returned.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll_epoll.go:netpollclose()

func netpollclose(fd uintptr) int32 {}

Instructs the epoll control kernel structure to stop monitoring the specified fd for I/O events.
Returns the negated code returned by the epoll_ctl() syscall. 
The resulting code is negated before being returned, because on Linux, 
a failed system call using the SYSCALL assembly instruction will return the value -errno in 
the rax register, so we need to negate the code to transform it into a valid errno.
If everyting went well, a 0 code is returned.


------------------------------------------------------------------------------------------------------------


src/runtime/netpoll_epoll.go:netpollBreak()

func netpollBreak() {}

Interrupts a blocked epollwait call by writing a 0 byte to the write end of a unix pipe, 
while it's read end is controlled by epoll, so a read event will be recorded in level-triggered mode,
therefore unblocking a blocking epollwait call. Otherwise the event will be ignored until a subsequent
blocking call occurs.

1) To avoid duplicate concurrent writes to netpollBreakWr, a global netpollWakeSig flag is used.
   Before writing to netpollBreakWr an atomic compare-and-swap operation is pefrormed,
   to see if the netpollWakeSig flag is already set, in which case netpollBreak is a no-op 
   function call.
   If the flag wasn't previously set by some other concurrent netpollBreak call, 
   the netpollWakeSig flag will be set to avoid any upcomming concurrent writes to netpollBreakWr,
   and this function's execution continues.
   The netpollWakeSig flag will be reset after netpoll processes the I/O event on the netpollBreakRd 
   unix pipe fd (this will only happen on blocking calls, otherwise the level-triggered event on 
   netpollBreakRd will be ignored).

2) Writes a 0 byte to netpollBreakWr write end of the unix pipe ingorig any signal interruptions.
   If the write will be blocking netpollBreak returns immediately.
   
3) All of this basically triggers a levele-triggered I/O read event on the netpollBreakRd fd, therefore 
   unblocking an indefinite call to epollwait 
   (is indefinite if no other fds are currently tracked by epoll.) Othewise during a non-blocking epoll_wait 
   all the level-triggered event will be ignored and processed only on subsequent blocking calls.
------------------------------------------------------------------------------------------------------------


src/runtime/netpoll_epoll.go:netpoll()

func netpoll(delay int64) gList {}

Checks for ready network connections.
Returns a list of goroutines that become runnable.

delay < 0: blocks indefinitely
delay == 0: does not block, just polls
delay > 0: block for up to that many nanoseconds

1) If epoll kernel structure wasn't yet initialized returns and empty gList with no goroutines

2) Calculates time to block on polling:
      - If delay if less than 0, polling for I/O will block indefinitely until an I/O event occurs
      - If delay is 0, a poll will be performed without blocking, returning fds that a already 
        ready for I/O 
      - If delay is less than or equal to 1 million nanoseconds the delay is translated to 1 millisecond
      - If delay is greater than 1 millisecond and less than or equal 11.5 days, the delay is truncated 
        to 1 second
      - If delay is greateer than 11.5 days, delay is truncated to 11.5 days

3) Declares an aray of 128 epollevents which contain the event mask and auxiliary data.
   This array will later be filled with events produced from scanning by epollwait. 
   Basically this restricts the amount of events to process in one shot to 128.

4) Polls for I/O events for the specified timout and excpects at most 128 events to be returned.

5) If the return code from epollwait is negative it means that an error occured. In particular
   it could be a signal interrupt (EINTR) - it occurs if another syscall fired while the epolwait 
   syscall was blocked indefinitely, this happens becuase the OS needs to somehow interrupt 
   indefinite syscalls if some other prioritized syscall occurs.
   In the case of EINTR if the timeout specified was greateer than 0 an empty gList
   without any goroutines is returned, this is done so the caller can recalculate a new
   delay taking the time that has already elapsed into account.
   If epollwait wasn't a blocking call or was an indefinite call, the same call to epollwait
   is retried again.
   If the error wasn't of type EINTR a panic is thrown.

6) After a successful call to epollwait the epollevent list will be filled with events 
   containing event mask and auxiliary data in the form of pollDesc pointers (through them 
   we can read the original underlying fds and schedule associated goroutines blocked on I/O)

7) Then we start looping through the list of epollevents on which non-blocking I/O 
   was possibly registered. All events with a mask of 0 are discarded, 
   meaning that this particular fd wasn't tracked for any events.

8) If event data holds a pointer to netpollBreakRd, it means that someone tried to unblock the
   epollwait blocking call by writing a 0 byte to the write end of the unix pipe. If the event
   that occured on netpollBreakRd isn't a read event, a panic is thrown. 
   If the read event was caught as a result of a non-blocking epollwait call, we do not read the byte 
   from netpollBrakRd and just continue scanning for the remaining events. 
   This is done because netpollBrakRd is registered in level-triggered mode, 
   so it will signal over and over again on each epollwait call until all data is read from it, 
   thus it will be processed only when needed, in case of a blocking epollwait call.

   In case of a blocking epollwait call we read the incoming byte into a temporary buffer 
   and reset the global netpollWakeSig flag to zero, which guarded concurrent
   writes to netpollBreakRw. From now on netpollBreakRw is available for writing.
   The global netpollWakeSig flag is reset only when we read from netpollBreakRd, otherwise
   there's no point of reseting it, because there's data left in the pipe which will be reported 
   once again during subsequent epollwait calls.

   This strategy is chosen because when epollwait is non-blocking we do not realy care 
   about events on netpollBreakRd, because the call to epollwait will unblock immediately
   after scanning for events, so there's no need to wake it up.


9) Once again we continue processing the remaining I/O events which occured on standard fds registered
   by the user in edge-triggered mode.

   For each event calculates whether the event was of type read or write.

   Read mode is considered if: 
      - the fd became available for non-blocking read
      - the other side closed the connection
      - the socket was closed unexpectedly
      - an error condition occured on the associated fd

   If at least one of th conditions is true a read flag is added to the resulting mode.

   Write mode is considered if:
      - the fd became available for non-blocking write
      - the socket was closed unexpectedly
      - an error condition occured on the associated fd

   If at least one of this conditions is true a write flag is added to the resulting mode.

   So theoretically it's possible the the resulting mode will be of read and write.

10) If at least so of the above mentioned events were registered on the fd, the pointer
    to the associated pollDesc is retrieved from the events data.
    If the event that occured was an EPOLLERR (error) the pollEventErr bit (0 or 1) in pollDesc's .atomicInfo 
    is set, which will later be used to check for errors on the underlying fd.

11) src/runtime/netpoll.go:netpollready() is called passing it the toRun *gList, pollDesc and mode,
    which could be read and/or write.
    A gList is basically a singly linked list of goroutines. Each goroutine can only be
    on one gQueue (global or local) or gList at a time.
    gQueue is also a singly linked list of goroutines.

    Depending on the I/O mode/modes the fd is being tracked for, the value of 
    pollDesc's semaphore will be retrieved.

    In a spinning lock loop pattern based on atomic operations 
    (compare and swap operation in particular) for each I/O mode specified:
      - Checks the current value of the semaphore and if it's equal to pdReady, returns a nil goroutine
         pointer immediately, because someone did it already.
         If it's equal to a nil pointer, meaning that no goroutine is currently blocked on the semaphore
         waiting for I/O, and I/O readiness status wasn't requested, this function returns 
         a nil goroutine pointer immediately.
      - Sets the samphore to pdReady.
      - Returns the pointer to the goroutine which was blocked on I/O.

   The pointers to read and/or write goroutines are added to *gList.
   
   When this nepoll function returnes the runtime will schedule blocked on I/O goroutines
   based on this *gList.
   After unblocking the goroutines will find out that their pollDescs are in pdReady I/O status, 
   so they can perform a non-blocking I/O operation.
   If I/O events occured on the fd, but no goroutines were blocked on pollDesc, then these
   particular epoll I/O events will be no-op.

   Basically if a read and write event occured on the underlying fd, but the pollDesc is tracked by
   the runtime only for read events, a nil goroutine will be returned for the write mode, so 
   in the end only the read goroutine will be unblocked to do non-blocking I/O.
   This is how mode switching happens on the application level to get rid of additional epoll_ctl 
   syscalls, to switch between polling modes on the underlying OS fd.
