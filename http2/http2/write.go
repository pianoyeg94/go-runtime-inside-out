package http2

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/url"

	"github.com/pianoyeg94/go-http2/hpack"
	"github.com/pianoyeg94/go-http2/httpguts"
)

// TODO!!!!!
//
// writeFramer is implemented by any type that is used to write frames.
type writeFramer interface {
	writeFrame(writeContext)

	// TODO!!!!!
	//
	// staysWithinBuffer reports whether this writer promises that
	// it will only write less than or equal to size bytes, and it
	// won't Flush the write context.
	staysWithinBuffer(size int) bool
}

// writeContext is the interface needed by the various frame writer
// types below. All the writeFrame methods below are scheduled via the
// frame writing scheduler (see writeScheduler in writesched.go).
//
// This interface is implemented by *serverConn.
//
// TODO: decide whether to a) use this in the client code (which didn't
// end up using this yet, because it has a simpler design, not
// currently implementing priorities), or b) delete this and
// make the server code a bit more concrete.
type writeContext interface {
	Framer() *Framer
	Flush() error
	CloseConn() error
	// TODO!!!!!
	//
	// HeaderEncoder returns an HPACK encoder that writes to the
	// returned buffer.
	HeaderEncoder() (*hpack.Encoder, *bytes.Buffer)
}

type flushFrameWriter struct{}

func (flushFrameWriter) writeFrame(ctx writeContext) error {
	return ctx.Flush()
}

func (flushFrameWriter) staysWithinBuffer(max int) bool { return false /* always flushes */ }

type writeSettings []Setting

func (s writeSettings) staysWithinBuffer(max int) bool {
	const settingSize = 6 // uint16 ID (which setting is being set) + uint32 Value
	return frameHeaderLen+settingSize*len(s) <= max
}

func (s writeSettings) writeFrame(ctx writeContext) error {
	// WriteSettings writes a SETTINGS frame with zero or more settings
	// specified and the ACK bit not set.
	//
	// It will perform exactly one Write to the underlying Writer
	// (for example, server's *bufferedWriter)
	// It is the caller's responsibility to not call other Write methods
	// concurrently.
	//
	// 1. Writes frame header with the first 3 octets of the
	//    length field temporarily set to 0, frame type, flags (zero)
	//    and stream id (zero) to Framer's `wbuf`.
	//
	// 2. Writes each setting to Framer's `wbuf` (uint16 ID + uint32 Value).
	//
	// 3. Writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer (for example, server's *bufferedWriter)
	//    (big-endian 24-bit length field).
	return ctx.Framer().WriteSettings([]Setting(s)...)
}

// A GoAwayFrame is used to initiate shutdown of a connection
// or to signal serious error conditions. It allows an endpoint
// to gracefully stop accepting new streams while still finishing
// processing of previously established streams (informs the remote
// peer to stop creating streams on this connection). This enables
// administrative actions, like server maintenance.
type writeGoAway struct {
	maxStreamID uint32
	code        ErrCode
}

func (p *writeGoAway) writeFrame(ctx writeContext) error {
	// 1. Writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags (zero)
	// and stream id (zero) to Framer's `wbuf`.
	//
	// 2. Writes uint32 maxStreamID in big-endian order to `wbuf`
	//    with reserved high bit set to 0.
	//
	// 3. Writes uint32 ErrCode to `wbuf` in big-endian order.
	//
	// 4. No debugData is written (last paramater is nil).
	//
	// 5. writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer
	err := ctx.Framer().WriteGoAway(p.maxStreamID, p.code, nil)
	ctx.Flush() // ignore error: we're hanging up on them anyway
	return err
}

func (*writeGoAway) staysWithinBuffer(max int) bool { return false } // flushes

// writeData writes a DataFrame, which conveys arbitrary, variable-length sequences of octets
// associated with a stream. One or more DATA frames are used, for instance,
// to carry HTTP request or response payloads.
//
// DATA frames are subject to flow control and can only be sent when a stream
// is in the "open" or "half-closed (remote)" state. The entire DATA frame payload
// is included in flow control, including the Pad Length and Padding fields if present.
// If a DATA frame is received whose stream is not in "open" or "half-closed (local)" state,
// the recipient MUST respond with a stream error of type STREAM_CLOSED.
//
// Note: A frame can be increased in size by one octet by including a Pad Length field with
// a value of zero.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.1
// book: page 134-135
//
// +---------------+
// |Pad Length? (8)|
// +---------------+-----------------------------------------------+
// |                            Data (*)                         ...
// +---------------------------------------------------------------+
// |                           Padding (*)                       ...
// +---------------------------------------------------------------+
//
// Pad Length: An 8-bit field containing the length of the frame padding in units
// of octets. This field is conditional (as signified by a "?" in the diagram) and
// is only present if the PADDED flag is set.
//
// Data: Application data. The amount of data is the remainder of the frame payload
// after subtracting the length of the other fields that are present.
//
// Padding: Padding octets that contain no application semantic value. Padding octets
// MUST be set to zero when sending. A receiver is not obligated to verify padding but
// MAY treat non-zero padding as a connection error of type PROTOCOL_ERROR.
type writeData struct {
	streamID  uint32
	p         []byte
	endStream bool // flag, no more data frames will be written to this stream
}

func (w *writeData) String() string {
	return fmt.Sprintf("writeDatas(stream=%d, p=%d, endStream=%v)", w.streamID, len(w.p), w.endStream)
}

func (w *writeData) writeFrame(ctx writeContext) error {
	// 1. writes the frame header to Framer's wbuf buffer.
	//    Frame type = DATA;
	//    flags = end stream, if no more data frames after this one;
	//    stream id;
	//    frame length will be written during the call to `f.endWrite()` (data, no padding)
	//
	// 2. Writes data to Framer's wbuf.
	//
	// 3. Writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer.
	return ctx.Framer().WriteData(w.streamID, w.endStream, w.p)
}

func (w *writeData) staysWithinBuffer(max int) bool {
	return frameHeaderLen+len(w.p) <= max
}

// handlerPanicRST is the message sent from handler goroutines when
// the handler panics.
//
// A RSTStreamFrame allows for immediate abnormal termination of a stream.
// RST_STREAM is sent to request cancellation of a stream or to indicate
// that an error condition has occurred.
//
// The RST_STREAM frame contains a single unsigned, 32-bit integer identifying
// the error code. The error code indicates why the stream is being terminated.
//
// The RST_STREAM frame fully terminates the referenced stream and causes it to enter
// the "closed" state. After receiving a RST_STREAM on a stream, the receiver MUST NOT
// send additional frames for that stream, with the exception of PRIORITY. However,
// after sending the RST_STREAM, the sending endpoint MUST be prepared to receive and
// process additional frames sent on the stream that might have been sent by the peer
// prior to the arrival of the RST_STREAM.
//
// RST_STREAM frames MUST NOT be sent for a stream in the "idle" state.
// If a RST_STREAM frame identifying an idle stream is received, the recipient MUST treat
// this as a connection error of type PROTOCOL_ERROR.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.4
// book: pages 138-139
//
//	+---------------------------------------------------------------+
//	|                        Error Code (32)                        |
//	+---------------------------------------------------------------+
type handlerPanicRST struct {
	StreamID uint32
}

func (hp handlerPanicRST) writeFrame(ctx writeContext) error {
	// 1. Writes frame header with the first 3 octets of the
	//    length field temporarily set to 0, frame type, flags
	//    and stream id to Framer's `wbuf`.
	//
	// 2. Writes StreamID as 32 unsigned int in big-endian order.
	//
	// 3. Writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer.
	return ctx.Framer().WriteRSTStream(hp.StreamID, ErrCodeInternal)
}

func (hp handlerPanicRST) staysWithinBuffer(max int) bool { return frameHeaderLen+4 <= max } // 4 stands for the 32-bit StreamID

/**
 * // StreamError is an error that only affects one stream within an
 * // HTTP/2 connection (from errors.go)
 * type StreamError struct {
 *     StreamID uint32
 * 	   Code     ErrCode
 * 	    Cause    error // optional additional detail
 * }
 */
func (se StreamError) writeFrame(ctx writeContext) error {
	// 1. Writes frame header with the first 3 octets of the
	//    length field temporarily set to 0, frame type, flags
	//    and stream id to Framer's `wbuf`.
	//
	// 2. Writes StreamID as 32 unsigned int in big-endian order.
	//
	// 3. Writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer.
	return ctx.Framer().WriteRSTStream(se.StreamID, se.Code)
}

func (se StreamError) staysWithinBuffer(max int) bool { return frameHeaderLen+4 <= max } // 4 stands for the 32-bit StreamID

// A PingFrame is a mechanism for measuring a minimal round trip time
// from the sender, as well as determining whether an idle connection
// is still functional.
// In addition to the frame header, PING frames MUST contain 8 octets
// of opaque data in the payload. A sender can include any value
// it chooses and use those octets in any fashion.
// Receivers of a PING frame that does not include an ACK flag MUST send
// a PING frame with the ACK flag set in response, with an identical payload.
// PING responses SHOULD be given higher priority than any other frame.
// PING frames are not associated with any individual stream.
type writePingAck struct{ pf *PingFrame }

func (w writePingAck) writeFrame(ctx writeContext) error {
	// 1. writes frame header with the first 3 octets of the
	//    length field temporarily set to 0, frame type, flags
	//    with the ACK flag set and stream id 0 to Framer's `wbuf`.
	//
	// 2. writes 8 bytes of data to `wbuf`
	//
	// 3. writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer
	return ctx.Framer().WritePing(true, w.pf.Data)
}

func (w writePingAck) staysWithinBuffer(max int) bool { return frameHeaderLen+len(w.pf.Data) <= max } // PING frames MUST contain 8 octets of opaque data in the payload

type writeSettingsAck struct{}

// Writes an empty SETTINGS frame with the ACK bit set.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (writeSettingsAck) writeFrame(ctx writeContext) error {
	// 1. writes frame header with the first 3 octets of the
	//    length field temporarily set to 0, frame type, flags with the ACK bit set
	//    and stream id 0 to Framer's `wbuf`.
	//
	// 2. writes frame body length into the first 3 octets of
	//    the frame header and flushes the binary frame to the
	//    underlying writer
	return ctx.Framer().WriteSettingsAck()
}

func (writeSettingsAck) staysWithinBuffer(max int) bool { return frameHeaderLen <= max }

// splitHeaderBlock splits headerBlock into fragments so that each fragment fits
// in a single frame, then calls fn (usually writeHeaderBlock) for each fragment.
// firstFrag/lastFrag are true for the first/last fragment, respectively.
func splitHeaderBlock(ctx writeContext, headerBlock []byte, fn func(ctx writeContext, frag []byte, firstFlag, lastFlag bool) error) error {
	// For now we're lazy and just pick the minimum MAX_FRAME_SIZE
	// that all peers must support (16KB). Later we could care
	// more and send larger frames if the peer advertised it, but
	// there's little point. Most headers are small anyway (so we
	// generally won't have CONTINUATION frames), and extra frames
	// only waste 9 bytes anyway.
	const maxFrameSize = 16384

	first := true
	for len(headerBlock) > 0 {
		frag := headerBlock
		if len(frag) > maxFrameSize {
			frag = frag[:maxFrameSize]
		}
		headerBlock = headerBlock[len(frag):]
		if err := fn(ctx, frag, first, len(headerBlock) == 0); err != nil {
			return err
		}
		first = false
	}
	return nil
}

// writeResHeaders is a request to write a HEADERS and 0+ CONTINUATION frames
// for HTTP response headers or trailers from a server handler.
type writeResHeaders struct {
	streamID    uint32
	httpResCode int         // 0 means no ":status" line
	h           http.Header // http headers map[string][]string, may be nil if no headers
	trailers    []string    // if non-nil, holds keys of h to write. nil means all (not http trailers, just for selective header key-value writes)
	endStream   bool        // no DATA frame follows (empty body or if trailer headers TODO!!!!! really?)

	date          string // TODO!!!!! datetime format?
	contentType   string
	contentLength string
}

func encKV(enc *hpack.Encoder, k, v string) {
	if VerboseLogs {
		log.Printf("http2: server encoding header %q = %q", k, v)
	}
	enc.WriteField(hpack.HeaderField{Name: k, Value: v}) // TODO!!!!! add details about the encoding process
}

func (w *writeResHeaders) staysWithinBuffer(max int) bool {
	// TODO: this is a common one. It'd be nice to return true
	// here and get into the fast path if we could be clever and
	// calculate the size fast enough, or at least a conservative
	// upper bound that usually fires. (Maybe if w.h and
	// w.trailers are nil, so we don't need to enumerate it.)
	// Otherwise I'm afraid that just calculating the length to
	// answer this question would be slower than the ~2µs benefit.
	return false
}

func (w *writeResHeaders) writeFrame(ctx writeContext) error {
	enc, buf := ctx.HeaderEncoder() // returns connection's HPAC encoder and header write buffer TODO!!!!! add more details
	// Reset resets the buffer to be empty,
	// but it retains the underlying storage for use by future writes.
	buf.Reset()

	if w.httpResCode != 0 {
		// httpCodeString relies on jump table for
		// most common http codes: 200 and 404,
		// other codes are converted via strconv.Itoa
		encKV(enc, ":status", httpCodeString(w.httpResCode)) // HPACK encodes header into header write buffer
	}

	// Skips writing HPACK encoded invalid headers into header write buffer.
	// Per RFC 7540, Section 8.1.2, header field names have to be ASCII characters
	// (just as in HTTP/1.x).
	//
	// Also skips non-lower-case header keys, because header field names MUST be converted
	// to lowercase prior to their encoding in HTTP/2.
	//
	// Also skips header keys that do not consist of token characters:
	//     "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /  "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
	//
	encodeHeaders(enc, w.h, w.trailers) // writes HPACK encoded headers into buffer

	// HPACK encode the following headers separately.
	if w.contentType != "" {
		encKV(enc, "content-type", w.contentType)
	}
	if w.contentLength != "" {
		encKV(enc, "content-length", w.contentLength)
	}
	if w.date != "" {
		encKV(enc, "date", w.date)
	}

	headerBlock := buf.Bytes()
	if len(headerBlock) == 0 && w.trailers == nil {
		panic("unexpected empty hpack")
	}

	// splitHeaderBlock splits headerBlock into fragments so that each fragment fits
	// in a single frame, then calls writeHeaderBlock for each fragment, which in turn
	// writes the respecitve HEADER or CONTINUATION frame.
	// firstFrag/lastFrag are true for the first/last fragment, respectively.
	return splitHeaderBlock(ctx, headerBlock, w.writeHeaderBlock)
}

// writeHeaderBlock writes a single header block fragment
func (w *writeResHeaders) writeHeaderBlock(ctx writeContext, frag []byte, firstFrag, lastFrag bool) error {
	if firstFrag {
		return ctx.Framer().WriteHeaders(HeadersFrameParam{
			StreamID:      w.streamID,
			BlockFragment: frag,
			EndStream:     w.endStream,
			EndHeaders:    lastFrag,
		})
	} else {
		return ctx.Framer().WriteContinuation(w.streamID, lastFrag, frag)
	}
}

// writePushPromise is a request to write a PUSH_PROMISE and 0+ CONTINUATION frames.
//
// A PushPromiseFrame is used to notify the peer endpoint in advance
// of streams the sender intends to initiate, currently is only used
// to initiate a server stream.
//
// PUSH_PROMISE frames MUST only be sent on a peer-initiated (client) stream
// that is in either the "open" or "half-closed (remote)" state.
//
// Promised streams are not required to be used in the order they are promised.
// The PUSH_PROMISE only reserves stream identifiers for later use.
//
// PUSH_PROMISE MUST NOT be sent if the SETTINGS_ENABLE_PUSH setting of the peer
// endpoint is set to 0.
//
// Recipients of PUSH_PROMISE frames can choose to reject promised streams by returning
// a RST_STREAM referencing the promised stream identifier back to the sender of the PUSH_PROMISE.
//
// A PUSH_PROMISE frame modifies the connection state in two ways. First, the inclusion of a header
// block potentially modifies the state maintained for header compression. Second, PUSH_PROMISE also
// reserves a stream for later use, causing the promised stream to enter the "reserved" state.
//
// The sender MUST ensure that the promised stream is a valid choice for a new stream identifier,
// that is, the promised stream MUST be in the "idle" state.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.6
// book: page 138
//
//	+---------------+
//	|Pad Length? (8)|
//	+-+-------------+-----------------------------------------------+
//	|R|                  Promised Stream ID (31)                    |
//	+-+-----------------------------+-------------------------------+
//	|                   Header Block Fragment (*)                 ...
//	+---------------------------------------------------------------+
//	|                           Padding (*)                       ...
//	+---------------------------------------------------------------+
type writePushPromise struct {
	streamID uint32   // pusher stream (client-initiated stream on which we send the promise)
	method   string   // for :method
	url      *url.URL // for :scheme, :authority, :path
	h        http.Header

	// Creates an ID for a pushed stream. This runs on serveG just before
	// the frame is written. The returned ID is copied to promisedID.
	allocatePromisedID func() (uint32, error)
	promisedID         uint32
}

func (w *writePushPromise) staysWithinBuffer(max int) bool {
	// TODO: this is a common one. It'd be nice to return true
	// here and get into the fast path if we could be clever and
	// calculate the size fast enough, or at least a conservative
	// upper bound that usually fires. (Maybe if w.h and
	// w.trailers are nil, so we don't need to enumerate it.)
	// Otherwise I'm afraid that just calculating the length to
	// answer this question would be slower than the ~2µs benefit.
	return false
}

func (w *writePushPromise) writeFrame(ctx writeContext) error {
	enc, buf := ctx.HeaderEncoder() // returns connection's HPAC encoder and header write buffer TODO!!!!! add more details
	// Reset resets the buffer to be empty,
	// but it retains the underlying storage for use by future writes.
	buf.Reset()

	// HPACK encode the following headers separately.
	encKV(enc, ":method", w.method)
	encKV(enc, ":scheme", w.url.Scheme)
	encKV(enc, ":authority", w.url.Host)
	encKV(enc, ":path", w.url.RequestURI())
	// Skips writing HPACK encoded invalid headers into header write buffer.
	// Per RFC 7540, Section 8.1.2, header field names have to be ASCII characters
	// (just as in HTTP/1.x).
	//
	// Also skips non-lower-case header keys, because header field names MUST be converted
	// to lowercase prior to their encoding in HTTP/2.
	//
	// Also skips header keys that do not consist of token characters:
	//     "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /  "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
	//
	encodeHeaders(enc, w.h, nil) // writes HPACK encoded headers into buffer

	headerBlock := buf.Bytes()
	if len(headerBlock) == 0 {
		panic("unexpected empty hpack")
	}

	// splitHeaderBlock splits headerBlock into fragments so that each fragment fits
	// in a single frame, then calls writeHeaderBlock for each fragment, which in turn
	// writes the respecitve HEADER or CONTINUATION frame.
	// firstFrag/lastFrag are true for the first/last fragment, respectively.
	return splitHeaderBlock(ctx, headerBlock, w.writeHeaderBlock)
}

// writeHeaderBlock writes a single header block fragment
func (w *writePushPromise) writeHeaderBlock(ctx writeContext, frag []byte, firstFrag, lastFrag bool) error {
	if firstFrag {
		return ctx.Framer().WritePushPromise(PushPromiseParam{
			StreamID:      w.streamID,
			PromiseID:     w.promisedID,
			BlockFragment: frag,
			EndHeaders:    lastFrag,
		})
	} else {
		return ctx.Framer().WriteContinuation(w.streamID, lastFrag, frag)
	}
}

type write100ContinueHeadersFrame struct {
	streamID uint32
}

func (w write100ContinueHeadersFrame) writeFrame(ctx writeContext) error {
	enc, buf := ctx.HeaderEncoder()
	buf.Reset()
	encKV(enc, ":status", "100")
	return ctx.Framer().WriteHeaders(HeadersFrameParam{
		StreamID:      w.streamID,
		BlockFragment: buf.Bytes(), // fits into a single fragment
		EndStream:     false,
		EndHeaders:    true,
	})
}

func (w write100ContinueHeadersFrame) staysWithinBuffer(max int) bool {
	// Sloppy but conservative:
	return 9+2*(len(":status")+len("100")) <= max
}

// A WindowUpdateFrame is used to implement flow control.
// Flow control operates at two levels: on each individual
// stream and on the entire connection.
// Flow control only applies to frames that are identified
// as being subject to flow control. Of the frame types defined
// by RFC 7540, this includes only DATA frames. Frames that are
// exempt from flow control MUST be accepted and processed, unless
// the receiver is unable to assign resources to handling the frame.
//
// The payload of a WINDOW_UPDATE frame is one reserved bit plus an
// unsigned 31-bit integer indicating the number of octets that the
// sender can transmit in addition to the existing flow-control window.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.9
// book: page 127-128
//
//	+-+-------------------------------------------------------------+
//	|R|              Window Size Increment (31)                     |
//	+-+-------------------------------------------------------------+
type writeWindowUpdate struct {
	streamID uint32 // or 0 for conn-level
	n        uint32
}

func (wu writeWindowUpdate) staysWithinBuffer(max int) bool { return frameHeaderLen+4 <= max }

func (wu writeWindowUpdate) writeFrame(ctx writeContext) error {
	return ctx.Framer().WriteWindowUpdate(wu.streamID, wu.n)
}

// encodeHeaders encodes an http.Header. If keys is not nil, then (k, h[k])
// is encoded only if k is in keys.
func encodeHeaders(enc *hpack.Encoder, h http.Header, keys []string) {
	if keys == nil { // write all headers from http.Header
		sorter := sorterPool.Get().(*sorter) // preserves underlying slice capacity between pool acquires
		// Using defer here, since the returned keys from the
		// sorter.Keys method is only valid until the sorter
		// is returned:
		defer sorterPool.Put(sorter)
		keys = sorter.Keys(h) // keys slice is now owned by sorter
	}
	for _, k := range keys {
		vv := h[k]
		k, ascii := lowerHeader(k)
		if !ascii {
			// Skip writing invalid headers. Per RFC 7540, Section 8.1.2, header
			// field names have to be ASCII characters (just as in HTTP/1.x).
			continue
		}
		// Reports whether v is a valid HTTP/1.x header name.
		// HTTP/2 imposes the additional restriction that uppercase ASCII
		// letters are not allowed.
		//
		// RFC 7230 says:
		//
		//		header-field   = field-name ":" OWS field-value OWS
		//	    OWS            = optional whitespace byte (SP | HTAB)
		//		field-name     = token
		//		token          = 1*tchar
		//		tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
		//		        "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
		//
		// Further, http2 says:
		//
		//	"Just as in HTTP/1.x, header field names are strings of ASCII
		//	characters that are compared in a case-insensitive
		//	fashion. However, header field names MUST be converted to
		//	lowercase prior to their encoding in HTTP/2. "
		if !validWireHeaderFieldName(k) {
			// Skip it as backup paranoia. Per
			// golang.org/issue/14048, these should
			// already be rejected at a higher level.
			continue
		}

		// The Transfer-Encoding header specifies the form of encoding used to safely
		// transfer the payload body to the user.
		//
		// Note: HTTP/2 disallows all uses of the Transfer-Encoding header other than the HTTP/2 specific:
		// "trailers". HTTP 2 provides its own more efficient mechanisms for data streaming than chunked transfer
		// and forbids the use of the header. Usage of the header in HTTP/2 may likely result in a specific protocol
		// error as HTTP/2 Protocol prohibits the use.
		//
		// Transfer-Encoding is a hop-by-hop header, that is applied to a message between two nodes, not to a resource itself.
		// Each segment of a multi-node connection can use different Transfer-Encoding values. If you want to compress data over
		// the whole connection, use the end-to-end Content-Encoding header instead.
		//
		// Transfer-Encoding: chunked
		// Transfer-Encoding: compress
		// Transfer-Encoding: deflate
		// Transfer-Encoding: gzip
		//
		// Several values can be listed, separated by a comma
		// Transfer-Encoding: gzip, chunked
		isTE := k == "transfer-encoding"
		for _, v := range vv {
			// ValidHeaderFieldValue reports whether v is a valid "field-value" according to
			// http://www.w3.org/Protocols/rfc2616/rfc2616-sec4.html#sec4.2 :
			//
			//	message-header = field-name ":" [ field-value ]
			//	field-value    = *( field-content | LWS )
			//	field-content  = <the OCTETs making up the field-value
			//	                 and consisting of either *TEXT or combinations
			//	                 of token, separators, and quoted-string>
			//
			// http://www.w3.org/Protocols/rfc2616/rfc2616-sec2.html#sec2.2 :
			//
			//	TEXT           = <any OCTET except CTLs,
			//	                  but including LWS>
			//	LWS            = [CRLF] 1*( SP | HT )
			//	CTL            = <any US-ASCII control character
			//	                 (octets 0 - 31) and DEL (127)>
			//
			// RFC 7230 says:
			//
			//	field-value    = *( field-content / obs-fold )
			//	obs-fold       =  N/A to http2, and deprecated
			//	field-content  = field-vchar [ 1*( SP / HTAB ) field-vchar ]
			//	field-vchar    = VCHAR / obs-text
			//	obs-text       = %x80-FF (128 - 255, out of ASCII range)
			//	VCHAR          = "any visible [USASCII] character"
			//
			// http2 further says: "Similarly, HTTP/2 allows header field values
			// that are not valid. While most of the values that can be encoded
			// will not alter header field parsing, carriage return (CR, ASCII
			// 0xd), line feed (LF, ASCII 0xa), and the zero character (NUL, ASCII
			// 0x0) might be exploited by an attacker if they are translated
			// verbatim. Any request or response that contains a character not
			// permitted in a header field value MUST be treated as malformed
			// (Section 8.1.2.6). Valid characters are defined by the
			// field-content ABNF rule in Section 3.2 of [RFC7230]."
			//
			// This function does not (yet?) properly handle the rejection of
			// strings that begin or end with SP or HTAB.
			if !httpguts.ValidHeaderFieldValue(v) {
				// TODO: return an error? golang.org/issue/14048
				// For now just omit it.
				continue
			}

			// HTTP/2 does not use the Connection header field to indicate connection-specific header fields;
			// in this protocol, connection-specific metadata is conveyed by other means. An endpoint MUST NOT
			// generate an HTTP/2 message containing connection-specific header fields; any message containing
			// connection-specific header fields MUST be treated as malformed (Section 8.1.2.6).
			//
			// The only exception to this is the TE header field, which MAY be present in an HTTP/2 request; when it is,
			// it MUST NOT contain any value other than "trailers".
			//
			// This means that an intermediary transforming an HTTP/1.x message to HTTP/2 will need to remove any header fields
			// nominated by the Connection header field, along with the Connection header field itself. Such intermediaries SHOULD
			// also remove other connection-specific header fields, such as Keep-Alive, Proxy-Connection, Transfer-Encoding, and Upgrade,
			// even if they are not nominated by the Connection header field.
			//
			// The HTTP Trailer header is used to allow clients to append certain HTTP headers to the end of a chunked message.
			// It has a single directive, which is the list of HTTP header names that will be included.
			//
			// TODO: more of "8.1.2.2 Connection-Specific Header Fields"
			if isTE && v != "trailers" {
				continue
			}
			encKV(enc, k, v)
		}
	}
}
