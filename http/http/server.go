package http

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"internal/godebug"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// ErrHijacked is returned by ResponseWriter.Write calls when
	// the underlying connection has been hijacked using the
	// Hijacker interface. A zero-byte write on a hijacked
	// connection will return ErrHijacked without any other side
	// effects.
	ErrHijacked = errors.New("http: connection has been hijacked")
)

// TODO!!!!!
// A Handler responds to an HTTP request.
//
// ServeHTTP should write reply headers and data to the ResponseWriter
// and then return. Returning signals that the request is finished; it
// is not valid to use the ResponseWriter or read from the
// Request.Body after or concurrently with the completion of the
// ServeHTTP call.
//
// Depending on the HTTP client software, HTTP protocol version, and
// any intermediaries between the client and the Go server, it may not
// be possible to read from the Request.Body after writing to the
// ResponseWriter. Cautious handlers should read the Request.Body
// first, and then reply.
//
// Except for reading the body, handlers should not modify the
// provided Request.
//
// If ServeHTTP panics, the server (the caller of ServeHTTP) assumes
// that the effect of the panic was isolated to the active request.
// It recovers the panic, logs a stack trace to the server error log,
// and either closes the network connection or sends an HTTP/2
// RST_STREAM, depending on the HTTP protocol. To abort a handler so
// the client sees an interrupted response but the server doesn't log
// an error, panic with the value ErrAbortHandler.
type Handler interface {
	ServerHTTP(ResponseWriter, *Request)
}

// A ResponseWriter interface is used by an HTTP handler to
// construct an HTTP response.
//
// A ResponseWriter may not be used after the Handler.ServeHTTP method
// has returned.
type ResponseWriter interface {
	// TODO!!!!!
	// Header returns the header map that will be sent by
	// WriteHeader. The Header map also is the mechanism with which
	// Handlers can set HTTP trailers.
	//
	// Changing the header map after a call to WriteHeader (or
	// Write) has no effect unless the HTTP status code was of the
	// 1xx class or the modified headers are trailers.
	//
	// There are two ways to set Trailers. The preferred way is to
	// predeclare in the headers which trailers you will later
	// send by setting the "Trailer" header to the names of the
	// trailer keys which will come later. In this case, those
	// keys of the Header map are treated as if they were
	// trailers. See the example. The second way, for trailer
	// keys not known to the Handler until after the first Write,
	// is to prefix the Header map keys with the TrailerPrefix
	// constant value. See TrailerPrefix.
	//
	// To suppress automatic response headers (such as "Date"), set
	// their value to nil.
	Header() Header

	// TODO!!!!!
	// Write writes the data to the connection as part of an HTTP reply.
	//
	// If WriteHeader has not yet been called, Write calls
	// WriteHeader(http.StatusOK) before writing the data. If the Header
	// does not contain a Content-Type line, Write adds a Content-Type set
	// to the result of passing the initial 512 bytes of written data to
	// DetectContentType. Additionally, if the total size of all written
	// data is under a few KB and there are no Flush calls, the
	// Content-Length header is added automatically.
	//
	// Depending on the HTTP protocol version and the client, calling
	// Write or WriteHeader may prevent future reads on the
	// Request.Body. For HTTP/1.x requests, handlers should read any
	// needed request body data before writing the response. Once the
	// headers have been flushed (due to either an explicit Flusher.Flush
	// call or writing enough data to trigger a flush), the request body
	// may be unavailable. For HTTP/2 requests, the Go HTTP server permits
	// handlers to continue to read the request body while concurrently
	// writing the response. However, such behavior may not be supported
	// by all HTTP/2 clients. Handlers should read before writing if
	// possible to maximize compatibility.
	Write([]byte) (int, error)

	// TODO!!!!!
	// WriteHeader sends an HTTP response header with the provided
	// status code.
	//
	// If WriteHeader is not called explicitly, the first call to Write
	// will trigger an implicit WriteHeader(http.StatusOK).
	// Thus explicit calls to WriteHeader are mainly used to
	// send error codes or 1xx informational responses.
	//
	// The provided code must be a valid HTTP 1xx-5xx status code.
	// Any number of 1xx headers may be written, followed by at most
	// one 2xx-5xx header. 1xx headers are sent immediately, but 2xx-5xx
	// headers may be buffered. Use the Flusher interface to send
	// buffered data. The header map is cleared when 2xx-5xx headers are
	// sent, but not with 1xx headers.
	//
	// The server will automatically send a 100 (Continue) header
	// on the first read from the request body if the request has
	// an "Expect: 100-continue" header. TODO!!!!!
	WriteHeader(statusCode int)
}

var (
	// ServerContextKey is a context key. It can be used in HTTP
	// handlers with Context.Value to access the server that
	// started the handler. The associated value will be of
	// type *Server.
	ServerContextKey = &contextKey{"htto-server"}

	// LocalAddrContextKey is a context key. It can be used in
	// HTTP handlers with Context.Value to access the local
	// address the connection arrived on.
	// The associated value will be of type net.Addr.
	LocalAddrContextKey = &contextKey{"local-addr"}
)

// A conn represents the server side of an HTTP connection.
type conn struct {
	// server is the server on which the connection arrived.
	// Immutable; never nil.
	server *Server

	// cancelCtx cancels the connection-level context.
	// Usually a child of server's base context. There
	// may be several more contexts in between if server's
	// BaseContext and ConnState context hooks return new
	// contexts derived from the base one.
	// Also the base or a context hook derived context has
	// ServerContextKey and LocalAddrContextKey key-value pairs set.
	cancelCtx context.CancelFunc

	// TODO!!!!! add details about CloseNotifier callers.
	//
	// rwc is the underlying network connection.
	// This is never wrapped by other types and is the value given out
	// to CloseNotifier callers. It is usually of type *net.TCPConn or
	// *tls.Conn.
	rwc net.Conn

	// remoteAddr is rwc.RemoteAddr().String(). It is not populated synchronously
	// inside the Listener's Accept goroutine, as some implementations block.
	// It is populated immediately inside the (*conn).serve goroutine.
	// This is the value of a Handler's (*Request).RemoteAddr.
	remoteAddr string

	// TODO!!!!!
	//
	// tlsState is the TLS connection state when using TLS.
	// nil means not TLS.
	tlsState *tls.ConnectionState

	// TODO!!!!! add details about "It is set via checkConnErrorWriter{w}, where bufw writes.".
	//
	// werr is set to the first write error to rwc.
	// It is set via checkConnErrorWriter{w}, where bufw writes.
	werr error

	// TODO!!!!!
	//
	// r is bufr's read source. It's a wrapper around rwc that provides
	// io.LimitedReader-style limiting (while reading request headers)
	// and functionality to support CloseNotifier. See *connReader docs.
	// r *connReader

	// TODO!!!!!
	//
	// bufr reads from r.
	bufr *bufio.Reader

	// TODO!!!!!
	//
	// bufw writes to checkConnErrorWriter{c}, which populates werr on error.
	bufw *bufio.Writer

	// TODO!!!!! why and when is it set?
	//
	// lastMethod is the method of the most recent request
	// on this connection, if any.
	lastMethod string

	// TODO!!!!!
	// curReq atomic.Pointer[response] // (which has a Request in it)

	// TODO!!!!!
	curState atomic.Uint64 // packed (unixtime<<8|uint8(ConnState))

	// TODO!!!!!
	//
	// mu guards hijackedv
	mu sync.Mutex

	// TODO!!!!!
	//
	// hijackedv is whether this connection has been hijacked
	// by a Handler with the Hijacker interface.
	// It is guarded by mu.
	//
	// https://www.geeksforgeeks.org/tcp-ip-hijacking/ - in our case
	// means the http handler took over the responsiblity for the raw net.Conn,
	// we're not responsible for it anymore.
	hijackedv bool
}

// hijacked tells whether this connection has been hijacked
// by a Handler with the Hijacker interface.
// It is guarded by mu.
//
// https://www.geeksforgeeks.org/tcp-ip-hijacking/ - in our case
// means the http handler took over the responsiblity for the raw net.Conn,
// we're not responsible for it anymore.
func (c *conn) hijacked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hijackedv
}

// debugServerConnections controls whether all server connections are wrapped
// with a verbose logging wrapper.
const debugServerConnections = false

// Create new connection (*conn) from rwc (net.Conn)
func (srv *Server) newConn(rwc net.Conn) *conn {
	c := &conn{
		server: srv,
		rwc:    rwc,
	}
	if debugServerConnections {
		// newLoggingConn returns a net.Conn which logs writes,
		// reads and close.
		//
		// Each loggingConn has a base name of "server" + sequence number starting from 1.
		c.rwc = newLoggingConn("server", c.rwc)
	}
	return c
}

// Read next request from connection.
func (c *conn) readRequest(ctx context.Context) error {
	if c.hijacked() {
		// https://www.geeksforgeeks.org/tcp-ip-hijacking/ - in our case
		// means the http handler took over the responsiblity for the raw net.Conn,
		// we're not responsible for it anymore.
		return ErrHijacked
	}

	var (
		wholeReqDeadline time.Time // or zero if none
		hdrDeadline      time.Time // or zero if none
	)
	t0 := time.Now()
	if d := c.server.readHeaderTimeout(); d > 0 {
		// readHeaderTimeout returns Server's ReadHeaderTimeout
		// or ReadTimeout if ReadHeaderTimeout is zero.
		//
		// Sot if at least one of the is not zero we calculare the
		// read header timeout.
		hdrDeadline = t0.Add(d)
	}
	if d := c.server.ReadTimeout; d > 0 {
		wholeReqDeadline = t0.Add(d)
	}
	// SetReadDeadline sets the deadline for future Read calls
	// and any currently-blocked Read call.
	// A zero value for t means Read will not time out.
	c.rwc.SetReadDeadline(hdrDeadline)
	if d := c.server.WriteTimeout; d > 0 {
		// right after we read the request, set the response deadline on the connection
		defer func() {
			c.rwc.SetWriteDeadline(time.Now().Add(d))
		}()
	}
}

const (
	runHooks  = true
	skipHooks = false
)

// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
// https://www.geeksforgeeks.org/tcp-ip-hijacking/ - in our case
// means a handler taking over the raw net.Conn
func (c *conn) setState(nc net.Conn, state ConnState, runHook bool) {
	srv := c.server
	switch state {
	case StateNew:
		srv.trackConn(c, true)
	case StateHijacked, StateClosed:
		srv.trackConn(c, false)
	}
	if state > 0xff || state < 0 {
		// if state occupies more than a byte or is negative
		panic("internal error")
	}
	// for unix seconds to take more that 56 bits 2284931318 years
	// should pass from now, that's why we can use 56 bits to store
	// the unix timestamp.
	packedState := uint64(time.Now().Unix()<<8) | uint64(state)
	c.curState.Store(packedState)
	if !runHook { // usually is derived from runHooks = true or skipHooks = false global variables
		return
	}
	if hook := srv.ConnState; hook != nil {
		// ConnState specifies an optional callback function that is
		// called when a client connection changes state. See the
		// ConnState type and associated constants for details.
		hook(nc, state)
	}
}

// Serve a new connection.
func (c *conn) serve(ctx context.Context) {
	c.remoteAddr = c.rwc.RemoteAddr().String()
	// LocalAddrContextKey is a context key. It can be used in
	// HTTP handlers with Context.Value to access the local
	// address the connection arrived on.
	ctx = context.WithValue(ctx, LocalAddrContextKey, c.rwc.LocalAddr())

	// TODO!!!!! var inFlightResponse *response
	defer func() {
		// TODO!!!!!
	}()

	if tlsConn, ok := c.rwc.(*tls.Conn); ok {
		_ = tlsConn
		// TODO!!!!!
	}

	// HTTP/1.x from here on.

	ctx, cancelCtx := context.WithCancel(ctx)
	c.cancelCtx = cancelCtx
	defer cancelCtx()

	// Request-Response loop.
	for {

	}
}

// A Server defines parameters for running an HTTP server.
// The zero value for Server is a valid configuration.
type Server struct {
	// Addr optionally specifies the TCP address for the server to listen on,
	// in the form "host:port". If empty, ":http" (port 80) is used.
	// The service names are defined in RFC 6335 and assigned by IANA.
	// See net.Dial for details of the address format.
	Addr string

	Handler Handler // handler to invoke on each request, http.DefaultServeMux if nil

	// TODO!!!!!
	// DisableGeneralOptionsHandler, if true, passes "OPTIONS *" requests to the Handler,
	// otherwise responds with 200 OK and Content-Length: 0.
	DisableGeneralOptionsHandler bool

	// TODO!!!!!
	// TLSConfig optionally provides a TLS configuration for use
	// by ServeTLS and ListenAndServeTLS. Note that this value is
	// cloned by ServeTLS and ListenAndServeTLS, so it's not
	// possible to modify the configuration with methods like
	// tls.Config.SetSessionTicketKeys. To use
	// SetSessionTicketKeys, use Server.Serve with a TLS Listener
	// instead.
	TLSConfig *tls.Config

	// ReadTimeout is the maximum duration for reading the entire
	// request, including the body. A zero or negative value means
	// there will be no timeout.
	//
	// Because ReadTimeout does not let Handlers make per-request
	// decisions on each request body's acceptable deadline or
	// upload rate, most users will prefer to use
	// ReadHeaderTimeout. It is valid to use them both.
	ReadTimeout time.Duration

	// ReadHeaderTimeout is the amount of time allowed to read
	// request headers. The connection's read deadline is reset
	// after reading the headers and the Handler can decide what
	// is considered too slow for the body. If ReadHeaderTimeout
	// is zero, the value of ReadTimeout is used. If both are
	// zero, there is no timeout.
	ReadHeaderTimeout time.Duration

	// TODO!!!!!
	// WriteTimeout is the maximum duration before timing out
	// writes of the response. It is reset whenever a new
	// request's header is read. Like ReadTimeout, it does not
	// let Handlers make decisions on a per-request basis.
	// A zero or negative value means there will be no timeout.
	WriteTimeout time.Duration

	// TODO!!!!!
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Connection_management_in_HTTP_1.x
	// https://book.hacktricks.xyz/pentesting-web/abusing-hop-by-hop-headers
	// https://habr.com/ru/articles/495308/
	// https://habr.com/ru/companies/flant/articles/343348/
	//
	// IdleTimeout is the maximum amount of time to wait for the
	// next request when keep-alives are enabled. If IdleTimeout
	// is zero, the value of ReadTimeout is used. If both are
	// zero, there is no timeout.
	IdleTimeout time.Duration

	// TODO!!!!!
	// MaxHeaderBytes controls the maximum number of bytes the
	// server will read parsing the request header's keys and
	// values, including the request line. It does not limit the
	// size of the request body.
	// If zero, DefaultMaxHeaderBytes is used.
	MaxHeaderBytes int

	// TODO!!!!!
	//
	// TLSNextProto optionally specifies a function to take over
	// ownership of the provided TLS connection when an ALPN
	// protocol upgrade has occurred. The map key is the protocol
	// name negotiated. The Handler argument should be used to
	// handle HTTP requests and will initialize the Request's TLS
	// and RemoteAddr if not already set. The connection is
	// automatically closed when the function returns.
	// If TLSNextProto is not nil, HTTP/2 support is not enabled
	// automatically.
	TLSNextProto map[string]func(*Server, *tls.Conn, Handler)

	// TODO!!!!!
	//
	// ConnState specifies an optional callback function that is
	// called when a client connection changes state. See the
	// ConnState type and associated constants for details.
	ConnState func(net.Conn, ConnState)

	// TODO!!!!!
	//
	// ErrorLog specifies an optional logger for errors accepting
	// connections, unexpected behavior from handlers, and
	// underlying FileSystem errors.
	// If nil, logging is done via the log package's standard logger.
	ErroLog *log.Logger

	// TODO!!!!!
	//
	// BaseContext optionally specifies a function that returns
	// the base context for incoming requests on this server.
	// The provided Listener is the specific Listener that's
	// about to start accepting requests.
	// If BaseContext is nil, the default is context.Background().
	// If non-nil, it must return a non-nil context.
	BaseContext func(net.Listener) context.Context

	// TODO!!!!!
	//
	// ConnContext optionally specifies a function that modifies
	// the context used for a new connection c. The provided ctx
	// is derived from the base context and has a ServerContextKey
	// value.
	ConnContext func(ctx context.Context, c net.Conn) context.Context

	inShutdown atomic.Bool // true when server is in shutdown

	disableKeepAlives atomic.Bool // TODO!!!!!
	nextProtoOnce     sync.Once   // guards setupHTTP2_* init, TODO!!!!!
	nextProtoErr      error       // result of http2.ConfigureServer if used, TODO!!!!!

	mu         sync.Mutex                 // protects listeners, activeConn maps bellow TODO!!!!!
	listeners  map[*net.Listener]struct{} // TODO!!!!!
	activeConn map[*conn]struct{}         // stores active connections currently established to this server TODO!!!!!
	onShutdown []func()                   // TODO!!!!!

	listenerGroup sync.WaitGroup // TODO!!!!!
}

// A ConnState represents the state of a client connection to a server.
// It's used by the optional Server.ConnState hook.
type ConnState int

const (
	// TODO!!!!!
	//
	// StateNew represents a new connection that is expected to
	// send a request immediately. Connections begin at this
	// state and then transition to either StateActive or
	// StateClosed.
	StateNew ConnState = iota

	// TODO!!!!!
	//
	// StateActive represents a connection that has read 1 or more
	// bytes of a request. The Server.ConnState hook for
	// StateActive fires before the request has entered a handler
	// and doesn't fire again until the request has been
	// handled. After the request is handled, the state
	// transitions to StateClosed, StateHijacked, or StateIdle.
	// For HTTP/2, StateActive fires on the transition from zero
	// to one active request, and only transitions away once all
	// active requests are complete. That means that ConnState
	// cannot be used to do per-request work; ConnState only notes
	// the overall state of the connection.
	StateActive

	// TODO!!!!!
	//
	// StateIdle represents a connection that has finished
	// handling a request and is in the keep-alive state, waiting
	// for a new request. Connections transition from StateIdle
	// to either StateActive or StateClosed.
	StateIdle

	// TODO!!!!!
	//
	// StateHijacked represents a hijacked connection.
	// This is a terminal state. It does not transition to StateClosed.
	// From now on the conn is owned by the handler, which hijacked the
	// connection, and we're not responsible of it anymore.
	StateHijacked

	// TODO!!!!!
	//
	// StateClosed represents a closed connection.
	// This is a terminal state. Hijacked connections do not
	// transition to StateClosed.
	StateClosed
)

var stateName = map[ConnState]string{
	StateNew:      "new",
	StateActive:   "active",
	StateIdle:     "idle",
	StateHijacked: "hijacked",
	StateClosed:   "closed",
}

// TODO!!!!!
//
// ListenAndServe listens on the TCP network address srv.Addr and then
// calls Serve to handle requests on incoming connections.
// Accepted connections are configured to enable TCP keep-alives.
//
// If srv.Addr is blank, ":http" is used.
//
// ListenAndServe always returns a non-nil error. After Shutdown or Close,
// the returned error is ErrServerClosed.
func (srv *Server) ListenAndServe() error {
	if srv.shuttingDown() { // loads the value of server's `inShutdown` atomic bool
		return ErrServerClosed
	}

	addr := srv.Addr
	if addr == "" {
		addr = ":http" // http is the name of the service which maps to port 80, empty addr means a wildcard address, basically is 0.0.0.0:80
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	return srv.Serve(ln)
}

var testHookServerServe func(*Server, net.Listener) // only used for testing purposes, if non-nil

// TODO!!!!!!
//
// shouldDoServeHTTP2 reports whether Server.Serve should configure
// automatic HTTP/2. (which sets up the srv.TLSNextProto map)
func (srv *Server) shouldConfigureHTTP2ForServer() bool {
	if srv.TLSConfig == nil {
		// Compatibility with Go 1.6:
		// If there's no TLSConfig, it's possible that the user just
		// didn't set it on the http.Server, but did pass it to
		// tls.NewListener and passed that listener to Serve.
		// So we should configure HTTP/2 (to set up srv.TLSNextProto)
		// in case the listener returns an "h2" *tls.Conn.
		return true
	}
	// The user specified a TLSConfig on their http.Server.
	// In this, case, only configure HTTP/2 if their tls.Config
	// explicitly mentions "h2". Otherwise http2.ConfigureServer
	// would modify the tls.Config to add it, but they probably already
	// passed this tls.Config to tls.NewListener. And if they did,
	// it's too late anyway to fix it. It would only be potentially racy.
	// See Issue 15908.
	return strSliceContains(srv.TLSConfig.NextProtos, http2NextProtoTLS)
}

// ErrServerClosed is returned by the Server's Serve, ServeTLS, ListenAndServe,
// and ListenAndServeTLS methods after a call to Shutdown or Close.
var ErrServerClosed = errors.New("http: Server closed")

// TODO!!!!!
//
// Serve accepts incoming connections on the Listener l, creating a
// new service goroutine for each. The service goroutines read requests and
// then call srv.Handler to reply to them.
//
// HTTP/2 support is only enabled if the Listener returns *tls.Conn
// connections and they were configured with "h2" in the TLS
// Config.NextProtos.
//
// Serve always returns a non-nil error and closes l.
// After Shutdown or Close, the returned error is ErrServerClosed.
func (srv *Server) Serve(l net.Listener) error {
	if fn := testHookServerServe; fn != nil {
		// only set during tests
		fn(srv, l) // call hook with unwrapped listener
	}

	origListener := l
	// onceCloseListener wraps a net.Listener, protecting it from
	// multiple Close calls.
	l = &onceCloseListener{Listener: l}
	defer l.Close()

	// TODO!!!!!
	//
	// if err := srv.setupHTTP2_Serve(); err != nil {
	// 	return err
	// }

	// TODO!!!!!
	//
	// trackListener adds or removes a net.Listener to the set of tracked
	// listeners.
	//
	// We store a pointer to interface in the map set, in case the
	// net.Listener is not comparable. This is safe because we only call
	// trackListener via Serve and can track+defer untrack the same
	// pointer to local variable there. We never need to compare a
	// Listener from another caller.
	//
	// It reports whether the server is still up (not Shutdown or Closed).
	if !srv.trackListener(&l, true) {
		return ErrServerClosed
	}
	defer srv.trackListener(&l, false)

	baseCtx := context.Background()
	if srv.BaseContext != nil {
		// BaseContext optionally specifies a function that returns
		// the base context for incoming requests on this server.
		// The provided Listener is the specific Listener that's
		// about to start accepting requests.
		// If BaseContext is nil, the default is context.Background().
		// If non-nil, it must return a non-nil context.
		baseCtx = srv.BaseContext(origListener)
		if baseCtx == nil {
			panic("BaseContext returned a nil context")
		}
	}

	// How long to sleep on accept failure.
	//
	// Is reset to 0 on every successfull client connection.
	//
	// On first accept error is set to 5ms and keeps doubling
	// on every consecutive accept error up till a maximum of 1s.
	var tempDelay time.Duration

	ctx := context.WithValue(baseCtx, ServerContextKey, srv)
	for {
		rw, err := l.Accept() // returns client net.Conn and error
		if err != nil {
			if srv.shuttingDown() {
				return ErrServerClosed
			}
			if ne, ok := err.(net.Error); ok && ne.Temporary() {
				// Deprecated: Temporary errors are not well-defined.
				// Most "temporary" errors are timeouts, and the few exceptions are surprising.
				// Do not use this method.
				if tempDelay == 0 {
					tempDelay = 5 * time.Millisecond
				} else {
					tempDelay *= 2
				}
				if max := 1 * time.Second; tempDelay > max {
					tempDelay = max
				}
				// Uses either ErrorLog which is specified as an optional logger
				// for errors accepting connections, unexpected behavior from handlers,
				// and underlying FileSystem errors.
				// Or if nil, logging is done via the log package's standard global logger.
				srv.logf("http: Accept error: %v; retrying in %v", err, tempDelay)
				// On first accept error is set to 5ms and keeps doubling
				// on every consecutive accept error up till a maximum of 1s.
				// On success is reset to 0.
				time.Sleep(tempDelay)
				continue
			}
			return err
		}
		connCtx := ctx
		if cc := srv.ConnContext; cc != nil {
			// ConnContext optionally specifies a function that modifies
			// the context used for a new connection c. The provided ctx
			// is derived from the base context and has a ServerContextKey
			// value.
			connCtx = cc(connCtx, rw)
			if connCtx == nil {
				panic("ConnContext returned nil")
			}
		}
		tempDelay = 0
		c := srv.newConn(rw) // create new connection (*conn) from rwc (net.Conn)
		// tracks *conn in s.activeConn map, set's conn state to
		// <current unixtimestamp | state (56 bits + 8 bits)>,
		// calls server's ConnState, if set, which specifies an
		// optional callback function that is called when a client
		// connection changes state.
		//
		// for unix seconds to take more that 56 bits 2284931318 years
		// should pass from now, that's why we can use 56 bits to store
		// the unix timestamp.
		c.setState(c.rwc, StateNew, runHooks) // before Serve can return
	}
}

// TODO!!!!!
//
// trackListener adds or removes a net.Listener to the set of tracked
// listeners.
//
// We store a pointer to interface in the map set, in case the
// net.Listener is not comparable. This is safe because we only call
// trackListener via Serve and can track+defer untrack the same
// pointer to local variable there. We never need to compare a
// Listener from another caller.
//
// It reports whether the server is still up (not Shutdown or Closed).
func (s *Server) trackListener(ln *net.Listener, add bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listeners == nil {
		s.listeners = make(map[*net.Listener]struct{})
	}
	if add {
		if s.shuttingDown() {
			return false
		}
		s.listeners[ln] = struct{}{}
		s.listenerGroup.Add(1)
	} else {
		delete(s.listeners, ln)
		s.listenerGroup.Done()
	}
	return true
}

func (s *Server) trackConn(c *conn, add bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeConn == nil {
		s.activeConn = make(map[*conn]struct{})
	}
	if add {
		s.activeConn[c] = struct{}{}
	} else {
		delete(s.activeConn, c)
	}
}

// readHeaderTimeout returns Server's ReadHeaderTimeout
// or ReadTimeout if ReadHeaderTimeout is zero.
func (s *Server) readHeaderTimeout() time.Duration {
	if s.ReadHeaderTimeout != 0 {
		return s.ReadHeaderTimeout
	}
	return s.ReadTimeout
}

func (s *Server) shuttingDown() bool {
	return s.inShutdown.Load()
}

func (s *Server) logf(format string, args ...any) {
	if s.ErroLog != nil {
		// ErrorLog specifies an optional logger for errors accepting
		// connections, unexpected behavior from handlers, and
		// underlying FileSystem errors.
		// If nil, logging is done via the log package's standard logger.
		s.ErroLog.Printf(format, args...)
	} else {
		log.Printf(format, args...)
	}
}

var http2server = godebug.New("http2server")

// TODO!!!!!
//
// onceSetNextProtoDefaults configures HTTP/2, if the user hasn't
// configured otherwise. (by setting srv.TLSNextProto non-nil)
// It must only be called via srv.nextProtoOnce (use srv.setupHTTP2_*).
func (srv *Server) onceSetNextProtoDefaults() {
	if omitBunldedHTTP2 || http2server.Value() == "0" {
		return
	}
	// Enable HTTP/2 by default if the user hasn't otherwise
	// configured their TLSNextProto map.
	// if srv.TLSNextProto == nil {
	// 	conf :=
	// }
}

// onceCloseListener wraps a net.Listener, protecting it from
// multiple Close calls.
type onceCloseListener struct {
	net.Listener
	once     sync.Once
	closeErr error // set once and returned on subsequent calls to Close
}

func (oc *onceCloseListener) Close() error {
	oc.once.Do(oc.close)
	return oc.closeErr
}

func (oc *onceCloseListener) close() { oc.closeErr = oc.Listener.Close() }

// loggingConn is used for debugging.
type loggingConn struct {
	name string // TODO!!!!!
	net.Conn
}

var (
	uniqNameMu   sync.Mutex
	uniqNameNext = make(map[string]int)
)

// newLoggingConn returns a net.Conn which logs writes,
// reads and close.
//
// Each loggingConn has a base name of "server" + sequence number starting from 1.
func newLoggingConn(baseName string, c net.Conn) net.Conn {
	uniqNameMu.Lock()
	defer uniqNameMu.Unlock()
	uniqNameNext[baseName]++
	return &loggingConn{
		name: fmt.Sprintf("%s-%d", baseName, uniqNameNext[baseName]),
		Conn: c,
	}
}

func (c *loggingConn) Write(p []byte) (n int, err error) {
	log.Printf("%s.Write(%d) = ....", c.name, len(p))
	n, err = c.Conn.Write(p)
	log.Printf("%s.Write(%d) = %d, %v", c.name, len(p), n, err)
	return
}

func (c *loggingConn) Read(p []byte) (n int, err error) {
	log.Printf("%s.Read(%d) = ....", c.name, len(p))
	n, err = c.Conn.Read(p)
	log.Printf("%s.Read(%d) = %d, %v", c.name, len(p), n, err)
	return
}

func (c *loggingConn) Close() (err error) {
	log.Printf("%s.Close() = ...", c.name)
	err = c.Conn.Close()
	log.Printf("%s.Close() = %v", c.name, err)
	return
}

func strSliceContains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
