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