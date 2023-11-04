package http2

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"sync"
	"time"
)

// Test hooks.
var (
	testHookOnConn    func()
	testHookOnPanicMu *sync.Mutex                                               // guards testHookOnPanic, nil except in tests
	testHookOnPanic   func(sc *serverConn, panicVal interface{}) (rePanic bool) // for tests, if returns true, the panic from serverConn's server() method is propagated
)

// Server is an HTTP/2 server.
type Server struct {
	state *serverInternalState

	// IdleTimeout specifies how long until idle clients should be
	// closed with a GOAWAY frame. PING frames are not considered
	// activity for the purposes of IdleTimeout.
	//
	// If not set, defaults to HTTP 1.1 server's IdleTimeout, if it's
	// not set on the HTTP 1.1 server, defaults to HTTP 1.1 server's
	// ReadTimeout.
	//
	// A GOAWAY frame is used to initiate shutdown of a connection
	// or to signal serious error conditions. It allows an endpoint
	// to gracefully stop accepting new streams while still finishing
	// processing of previously established streams (informs the remote
	// peer to stop creating streams on this connection).
	//
	// Once sent, the sender will ignore frames sent on streams initiated
	// by the receiver if the stream has an identifier higher than the included
	// last stream identifier. Receivers of a GOAWAY frame MUST NOT open additional
	// streams on the connection, although a new connection can be established for
	// new streams.
	//
	// Endpoints SHOULD always send a GOAWAY frame before closing a connection so that the
	// remote peer can know whether a stream has been partially processed or not. For example,
	// if an HTTP client sends a POST at the same time that a server closes a connection, the
	// client cannot know if the server started to process that POST request if the server does
	// not send a GOAWAY frame to indicate what streams it might have acted on.
	//
	// After sending a GOAWAY frame, the sender can discard frames for streams initiated
	// by the receiver with identifiers higher that the identified last stream. However,
	// any frames that alter connection state cannot be completely ignored. For instance,
	// HEADERS, PUSH_PROMISE, and CONTINUATION frames MUST be minimally processed to ensure
	// the state maintained for header compression is consistent ; similarly, DATA frames MUST
	// be counted toward the connection flow-control window. Failure to process these frames can
	// cause flow control or header compression state to become unsynchronized.
	IdleTimeout time.Duration
}

type serverInternalState struct {
	mu          sync.Mutex
	activeConns map[*serverConn]struct{}
}

func (s *serverInternalState) registerConn(sc *serverConn) {
	if s == nil {
		return // if the Server was used without calling ConfigureServer
	}

	s.mu.Lock()
	s.activeConns[sc] = struct{}{}
	s.mu.Unlock()
}

func (s *serverInternalState) unregisterConn(sc *serverConn) {
	if s == nil {
		return // if the Server was used without calling ConfigureServer
	}
	s.mu.Lock()
	delete(s.activeConns, sc)
	s.mu.Unlock()
}

// TODO!!!!!
//
// ConfigureServer adds HTTP/2 support to a net/http Server.
//
// The configuration conf may be nil.
//
// ConfigureServer must be called before s begins serving.
func ConfigureServer(s *http.Server, conf *Server) error {
	if s == nil {
		panic("nil *http.Server")
	}
	if conf == nil {
		conf = new(Server)
	}
	conf.state = &serverInternalState{activeConns: make(map[*serverConn]struct{})}

	// IdleTimeout specifies how long until idle clients should be
	// closed with a GOAWAY frame. PING frames are not considered
	// activity for the purposes of IdleTimeout.
	//
	// If not set, defaults to HTTP 1.1 server's IdleTimeout, if it's
	// not set on the HTTP 1.1 server, defaults to HTTP 1.1 server's
	// ReadTimeout.
	//
	// A GOAWAY frame is used to initiate shutdown of a connection
	// or to signal serious error conditions. It allows an endpoint
	// to gracefully stop accepting new streams while still finishing
	// processing of previously established streams (informs the remote
	// peer to stop creating streams on this connection).
	if h1, h2 := s, conf; h2.IdleTimeout == 0 {
		if h1.IdleTimeout != 0 {
			h2.IdleTimeout = h1.IdleTimeout
		} else {
			h2.IdleTimeout = h1.ReadTimeout
		}
	}
	// TODO!!!!! s.RegisterOnShutdown(conf.state.startGracefulShutdown)

	if s.TLSConfig == nil {
		// TODO!!!!!
		s.TLSConfig = new(tls.Config)
	} else if s.TLSConfig.CipherSuites != nil && s.TLSConfig.MinVersion < tls.VersionTLS13 {
		// TODO!!!!!
	}

	// TODO!!!!!
	//
	// Note: not setting MinVersion to tls.VersionTLS12,
	// as we don't want to interfere with HTTP/1.1 traffic
	// on the user's server. We enforce TLS 1.2 later once
	// we accept a connection. Ideally this should be done
	// during next-proto selection, but using TLS <1.2 with
	// HTTP/2 is still the client's bug.

	// PreferServerCipherSuites is a legacy field and has no effect.
	//
	// It used to control whether the server would follow the client's or the
	// server's preference. Servers now select the best mutually supported
	// cipher suite based on logic that takes into account inferred client
	// hardware, server hardware, and security.
	//
	// Deprecated: PreferServerCipherSuites is ignored.
	s.TLSConfig.PreferServerCipherSuites = true

	if !strSliceContains(s.TLSConfig.NextProtos, NextProtoTLS /* == "h2" */) {
		s.TLSConfig.NextProtos = append(s.TLSConfig.NextProtos, NextProtoTLS) // TODO!!!!!
	}
	if !strSliceContains(s.TLSConfig.NextProtos, "http/1.1") {
		s.TLSConfig.NextProtos = append(s.TLSConfig.NextProtos, "http/1.1") // TODO!!!!!
	}

	if s.TLSNextProto == nil {
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
		s.TLSNextProto = map[string]func(hs *http.Server, c *tls.Conn, h http.Handler){}
	}
	protoHandler := func(hs *http.Server, c *tls.Conn, h http.Handler) {
		if testHookOnConn != nil {
			// no-op by default
			testHookOnConn()
		}
		// The TLSNextProto interface predates contexts, so
		// the net/http package passes down its per-connection
		// base context via an exported but unadvertised
		// method on the Handler. This is for internal
		// net/http<=>http2 use only.
		var ctx context.Context

		// TODO!!!!!
		// BaseContext is an exported but unadvertised http.Handler method
		// recognized by x/net/http2 to pass down a context; the TLSNextProto
		// API predates context support so we shoehorn through the only
		// interface we have available.
		//     `func (h initALPNRequest) BaseContext() context.Context { return h.ctx }`
		//
		// Context passed into accepted connection's .serve() method by *http.Server.
		//
		// Possible chain of contexts passed into conntection's .serve() method (starting from
		// inner most context):
		//     1. *http.Server's BaseContext optionally specifies a function that returns
		//         the base context for incoming requests on this server.
		//         If BaseContext is nil, the default is context.Background().
		//         If non-nil, it must return a non-nil context.
		//
		//     2. Adds ServerContextKey to the context, which provides access to the underlying *http.Server.
		//
		//     3. *http.Server's ConnContext optionally specifies a function that modifies the context
		//         used for a new connection c. The provided ctx is derived from the base context and has
		//         a ServerContextKey value.
		//
		//     4. Adds LocalAddrContextKey to the context, which provides access the local address the connection
		//        arrived on. The associated value will be of type net.Addr.
		type baseContexter interface {
			BaseContext() context.Context
		}
		if bc, ok := h.(baseContexter); ok {
			ctx = bc.BaseContext()
		}
	}

	return nil
}

// ServeConnOpts are options for the Server.ServeConn method.
type ServeConnOpts struct {
	// Context is the base context to use.
	// If nil, context.Background is used
	Context context.Context

	// BaseConfig optionally sets the base configuration
	// for values. If nil, defaults are used.
	BaseConfig *http.Server

	// Handler specifies which handler to use for processing
	// requests. If nil, BaseConfig.Handler is used. If BaseConfig
	// or BaseConfig.Handler is nil, http.DefaultServeMux is used.
	Handler http.Handler

	// UpgradeRequest is an initial request received on a connection
	// undergoing an h2c (unencrypted http2) upgrade. The request body must have been
	// completely read from the connection before calling ServeConn,
	// and the 101 Switching Protocols response written.
	// book: page 109
	UpgradeRequest *http.Request

	// Settings is the decoded contents of the HTTP2-Settings header
	// in an h2c upgrade request.
	// The content of the HTTP2-Settings header field is the payload of
	// a SETTINGS frame, encoded as a base64url string.
	// https://httpwg.org/specs/rfc7540.html#Http2SettingsHeader
	Settings []byte

	// SawClientPreface is set if the HTTP/2 connection preface
	// has already been read from the connection.
	// book: 113
	SawClientPreface bool
}

func (o *ServeConnOpts) context() context.Context {
	if o != nil && o.Context != nil {
		return o.Context
	}

	return context.Background()
}

func (o *ServeConnOpts) baseConfig() *http.Server {
	if o != nil && o.BaseConfig != nil {
		return o.BaseConfig
	}

	return new(http.Server)
}

func (o *ServeConnOpts) handler() http.Handler {
	if o != nil {
		if o.Handler != nil {
			return o.Handler
		}

		if o.BaseConfig != nil && o.BaseConfig.Handler != nil {
			return o.BaseConfig.Handler
		}
	}

	return http.DefaultServeMux
}

// TODO!!!!!
//
// ServeConn serves HTTP/2 requests on the provided connection and
// blocks until the connection is no longer readable.
//
// ServeConn starts speaking HTTP/2 assuming that c has not had any
// reads or writes. It writes its initial settings frame and expects
// to be able to read the preface and settings frame from the
// client. If c has a ConnectionState method like a *tls.Conn, the
// ConnectionState is used to verify the TLS ciphersuite and to set
// the Request.TLS field in Handlers.
//
// ServeConn does not support h2c by itself. Any h2c support must be
// implemented in terms of providing a suitably-behaving net.Conn.
//
// The opts parameter is optional. If nil, default values are used.
func (s *Server) ServeConn(c net.Conn, opts *ServeConnOpts) {
	// Possible chain of contexts returned from serveConnBaseContext
	// (starting from inner most context) and returned:
	//     1. *http.Server's BaseContext optionally specifies a function that returns
	//         the base context for incoming requests on this server.
	//         If BaseContext is nil, the default is context.Background().
	//         If non-nil, it must return a non-nil context.
	//
	//     2. Adds ServerContextKey to the context, which provides access to the underlying *http.Server.
	//
	//     3. *http.Server's ConnContext optionally specifies a function that modifies the context
	//         used for a new connection c. The provided ctx is derived from the base context and has
	//         a ServerContextKey value.
	//
	//     4. Adds LocalAddrContextKey to the context, which provides access the local address the connection
	//        arrived on. The associated value will be of type net.Addr.
	//
	//     5. Context with cancel
	baseCtx, cancel := serveConnBaseContext(c, opts)
	defer cancel()

	sc := &serverConn{
		srv:           s,
		hs:            opts.baseConfig(),
		conn:          c,
		baseCtx:       baseCtx,
		remoteAddrStr: c.RemoteAddr().String(),
		bw:            newBufferedWriter(c),
		handler:       opts.handler(),
		streams:       make(map[uint32]*stream),

		// TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!
	}

	s.state.registerConn(sc)
	defer s.state.unregisterConn(sc)

	// TODO if sc.hs.WriteTimeout != 0 {}

	// TODO

}

func serveConnBaseContext(c net.Conn, opts *ServeConnOpts) (context.Context, func()) {
	// Possible chain of contexts passed into conntection's .serve() method (starting from
	// inner most context and returned by opts.context()):
	//     1. *http.Server's BaseContext optionally specifies a function that returns
	//         the base context for incoming requests on this server.
	//         If BaseContext is nil, the default is context.Background().
	//         If non-nil, it must return a non-nil context.
	//
	//     2. Adds ServerContextKey to the context, which provides access to the underlying *http.Server.
	//
	//     3. *http.Server's ConnContext optionally specifies a function that modifies the context
	//         used for a new connection c. The provided ctx is derived from the base context and has
	//         a ServerContextKey value.
	//
	//     4. Adds LocalAddrContextKey to the context, which provides access the local address the connection
	//        arrived on. The associated value will be of type net.Addr.
	ctx, cancel := context.WithCancel(opts.context())
	ctx = context.WithValue(ctx, http.LocalAddrContextKey, c.LocalAddr())
	if hs := opts.baseConfig(); hs != nil {
		ctx = context.WithValue(ctx, http.ServerContextKey, hs)
	}

	return ctx, cancel
}

type serverConn struct {
	// Immutable:
	srv           *Server
	hs            *http.Server
	conn          net.Conn
	bw            *bufferedWriter // writing to conn
	handler       http.Handler
	baseCtx       context.Context
	framer        *Framer
	remoteAddrStr string

	// TODO!!!!!
	//
	// Everything following is owned by the serve loop; use serveG.check():

	// Used to verify funcs are on serve(), basically the goroutine id on
	// which `func (s *Server) ServeConn(c net.Conn, opts *ServeConnOpts)`
	// was called.
	serveG goroutineLock

	streams map[uint32]*stream
}

// stream represents a stream. This is the minimal metadata needed by
// the serve goroutine. Most of the actual stream state is owned by
// the http.Handler's goroutine in the responseWriter. Because the
// responseWriter's responseWriterState is recycled at the end of a
// handler, this struct intentionally has no pointer to the
// *responseWriter{,State} itself, as the Handler ending nils out the
// responseWriter's state field.
type stream struct {
	// immutable:
	sc        *serverConn
	id        uint32
	ctx       context.Context
	cancelCtx func()
}

// notePanic is used only for tests.
func (sc *serverConn) notePanic() {
	// Note: this is for serverConn.serve panicking, not http.Handler code.
	if testHookOnPanicMu != nil {
		testHookOnPanicMu.Lock()
		defer testHookOnPanicMu.Unlock()
	}
	if testHookOnPanic != nil {
		if e := recover(); e != nil {
			if testHookOnPanic(sc, e) {
				panic(e)
			}
		}
	}
}

func (sc *serverConn) serve() {
	sc.serveG.check()
	defer sc.notePanic() // only in tests
	defer sc.conn.Close()
}
