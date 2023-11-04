package http2

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"

	"github.com/pianoyeg94/go-http2/hpack"
	"github.com/pianoyeg94/go-http2/httpguts"
	"github.com/pianoyeg94/go-http2/io"
)

const frameHeaderLen = 9

var padZeros = make([]byte, 255) // zeros for padding

// A FrameType is a registered frame type as defined in
// https://httpwg.org/specs/rfc7540.html#rfc.section.11.2
type FrameType uint8

// book: page 121
const (
	FrameData         FrameType = 0x0
	FrameHeaders      FrameType = 0x1
	FramePriority     FrameType = 0x2
	FrameRSTStream    FrameType = 0x3
	FrameSettings     FrameType = 0x4
	FramePushPromise  FrameType = 0x5
	FramePing         FrameType = 0x6
	FrameGoAway       FrameType = 0x7
	FrameWindowUpdate FrameType = 0x8
	FrameContinuation FrameType = 0x9
)

var frameName = map[FrameType]string{
	FrameData:         "DATA",
	FrameHeaders:      "HEADERS",
	FramePriority:     "PRIORITY",
	FrameRSTStream:    "RST_STREAM",
	FrameSettings:     "SETTINGS",
	FramePushPromise:  "PUSH_PROMISE",
	FramePing:         "PING",
	FrameGoAway:       "GOAWAY",
	FrameWindowUpdate: "WINDOW_UPDATE",
	FrameContinuation: "CONTINUATION",
}

func (t FrameType) String() string {
	if s, ok := frameName[t]; ok {
		return s
	}

	return fmt.Sprintf("UNKNOWN_FRAME_TYPE_%d", uint8(t))
}

// Flags is a bitmask of HTTP/2 flags.
// The meaning of flags varies depending on the frame type.
type Flags uint8

// Has reports whether f contains all (0 or more) flags in v.
func (f Flags) Has(v Flags) bool {
	return (f & v) == v
}

// Frame-specific FrameHeader flag bits.
const (
	// Data Frame
	// book: page 134
	FlagDataEndStream Flags = 0x1
	FlagDataPadded    Flags = 0x8

	// Headers Frame
	// book: page 131-132
	FlagHeadersEndStream  Flags = 0x1
	FlagHeadersEndHeaders Flags = 0x4
	FlagHeadersPadded     Flags = 0x8
	FlagHeadersPriority   Flags = 0x20

	// Settings Frame
	// book: page 124
	FlagSettingsAck Flags = 0x1

	// Ping Frame
	// book: page 137-138
	FlagPingAck Flags = 0x1

	// Continuation Frame
	// book: page 137
	FlagContinuationEndHeaders Flags = 0x4

	// Push Promise Frame
	// book: page 138
	FlagPushPromiseEndHeaders Flags = 0x4
	FlagPushPromisePadded     Flags = 0x8
)

var flagName = map[FrameType]map[Flags]string{
	FrameData: {
		FlagDataEndStream: "END_STREAM",
		FlagDataPadded:    "PADDED",
	},
	FrameHeaders: {
		FlagHeadersEndStream:  "END_STREAM",
		FlagHeadersEndHeaders: "END_HEADERS",
		FlagHeadersPadded:     "PADDED",
		FlagHeadersPriority:   "PRIORITY",
	},
	FrameSettings: {
		FlagSettingsAck: "ACK",
	},
	FramePing: {
		FlagPingAck: "ACK",
	},
	FrameContinuation: {
		FlagContinuationEndHeaders: "END_HEADERS",
	},
	FramePushPromise: {
		FlagPushPromiseEndHeaders: "END_HEADERS",
		FlagPushPromisePadded:     "PADDED",
	},
}

// a frameParser parses a frame given its FrameHeader and payload
// bytes. The length of payload will always equal fh.Length (which
// might be 0).
type frameParser func(fc *frameCache, fh FrameHeader, countError func(string), payload []byte) (Frame, error)

var frameParsers = map[FrameType]frameParser{
	FrameData:         parseDataFrame,
	FrameHeaders:      parseHeadersFrame,
	FramePriority:     parsePriorityFrame,
	FrameRSTStream:    parseRSTStreamFrame,
	FrameSettings:     parseSettingsFrame,
	FramePushPromise:  parsePushPromise,
	FramePing:         parsePingFrame,
	FrameGoAway:       parseGoAwayFrame,
	FrameWindowUpdate: parseWindowUpdateFrame,
	FrameContinuation: parseContinuationFrame,
}

func typeFrameParser(t FrameType) frameParser {
	if f := frameParsers[t]; f != nil {
		return f
	}

	return parseUnknownFrame
}

// A FrameHeader is the 9 byte header of all HTTP/2 frames.
//
// See https://httpwg.org/specs/rfc7540.html#FrameHeader
// book: page 121-122
//
// R: A reserved 1-bit field. The semantics of this bit are undefined,
// and the bit MUST remain unset (0x0) when sending and MUST be ignored when receiving.
//
//	+-----------------------------------------------+
//	|                 Length (24)                   |
//	+---------------+---------------+---------------+
//	|   Type (8)    |   Flags (8)   |
//	+-+-------------+---------------+-------------------------------+
//	|R|      Stream Identifier (31)                                 |
//	+=+=============================================================+
//	|                   Frame Payload (0...)                      ...
//	+---------------------------------------------------------------+
type FrameHeader struct {
	valid bool // caller can access []byte fields in the Frame

	// Type is the 1 byte frame type. There are ten standard frame
	// types, but extension frame types may be written by WriteRawFrame
	// and will be returned by ReadFrame (as UnknownFrame).
	Type FrameType

	// Flags are the 1 byte of 8 potential bit flags per frame.
	// They are specific to the frame type.
	Flags Flags

	// Length is the length of the frame, not including the 9 byte header.
	// The maximum size is one byte less than 16MB (uint24), but only
	// frames up to 16KB are allowed without peer agreement (values greater
	// than 2^14 (16,384) MUST NOT be sent unless the receiver has set a larger
	// value for SETTINGS_MAX_FRAME_SIZE).
	Length uint32 // 3 bytes + 1 byte of padding

	// StreamID is which stream this frame is for. Certain frames
	// are not stream-specific (associated with the connection as a whole),
	// in which case this field is 0 (0th control stream).
	StreamID uint32 // 31 bits + 1 resered bit
}

func (h FrameHeader) Header() FrameHeader { return h }

func (h FrameHeader) String() string {
	var buf bytes.Buffer
	buf.WriteString("[FrameHeader ")
	h.writeDebug(&buf)
	buf.WriteByte(']')
	return buf.String()
}

// writeDebug converts frame header's type, flags
// stream id and length fields into a human-readable
// text form:
//
//	"typeName flags=flagName1|flagName2 stream=1 len=1234"
func (h FrameHeader) writeDebug(buf *bytes.Buffer) {
	buf.WriteString(h.Type.String())
	if h.Flags != 0 {
		buf.WriteString(" flags=")
		set := 0
		for i := uint8(0); i < 8; i++ {
			if h.Flags&(1<<i) == 0 {
				continue
			}
			set++
			if set > 1 {
				buf.WriteByte('|')
			}
			name := flagName[h.Type][Flags(1<<i)]
			if name != "" {
				buf.WriteString(name)
			} else {
				fmt.Fprintf(buf, "0x%x", 1<<i)
			}
		}
	}
	if h.StreamID != 0 {
		fmt.Fprintf(buf, " stream=%d", h.StreamID)
	}
	fmt.Fprintf(buf, " len=%d", h.Length)
}

func (h *FrameHeader) checkValid() {
	if !h.valid {
		panic("Frame accessor called on non-owned Frame")
	}
}

func (h *FrameHeader) invalidate() { h.valid = false }

// frame header bytes.
// Used only by ReadFrameHeader.
var fhBytes = sync.Pool{
	New: func() interface{} {
		buf := make([]byte, frameHeaderLen)
		return &buf
	},
}

func ReadFrameHeader(r io.Reader) (FrameHeader, error) {
	bufp := fhBytes.Get().(*[]byte)
	defer fhBytes.Put(bufp)
	return readFrameHeader(*bufp, r)

}

func readFrameHeader(buf []byte, r io.Reader) (FrameHeader, error) {
	if _, err := io.ReadFull(r, buf[:frameHeaderLen]); err != nil {
		return FrameHeader{}, err
	}

	return FrameHeader{
		Length:   (uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])), // uint24 big endian to little endian
		Type:     FrameType(buf[3]),
		Flags:    Flags(buf[4]),
		StreamID: binary.BigEndian.Uint32(buf[5:]) & (1<<31 - 1), // with most significant bit set to 0 (reserved bit)
		valid:    true,
	}, nil
}

// A Frame is the base interface implemented by all frame types.
// Callers will generally type-assert the specific frame type:
// *HeadersFrame, *SettingsFrame, *WindowUpdateFrame, etc.
//
// Frames are only valid until the next call to Framer.ReadFrame.
type Frame interface {
	Header() FrameHeader

	// invalidate is called by Framer.ReadFrame to make this
	// frame's buffers as being invalid, since the subsequent
	// frame will reuse them.
	invalidate()
}

// A Framer reads and writes Frames.
type Framer struct {
	r         io.Reader
	lastFrame Frame // is only used to validate that no other frame is received if we're waiting for contiguos CONTINUATION frames
	errDetail error // TODO!!!!!

	// countError is a non-nil func that's called on a frame parse
	// error with some unique error path token. It's initialized
	// from Transport.CountError or Server.CountError.
	countError func(errToken string)

	// lastHeaderStream is non-zero if the last frame was an
	// unfinished HEADERS/CONTINUATION.
	lastHeaderStream uint32

	maxReadSize uint32
	headerBuf   [frameHeaderLen]byte // temporary buffer for the 9-byte frame header

	// TODO!!!!! let getReadBuf be configurable, and use a less memory-pinning
	// allocator in server.go to minimize memory pinned for many idle conns.
	// Will probably also need to make frame invalidation have a hook too.
	getReadBuf func(size uint32) []byte
	readBuf    []byte // cache for default getReadBuf

	w    io.Writer
	wbuf []byte // temporary buffer used for assembling frames before they are written to the underlying `w` writer

	// AllowIllegalWrites permits the Framer's Write methods to
	// write frames that do not conform to the HTTP/2 spec. This
	// permits using the Framer to test other HTTP/2
	// implementations' conformance to the spec.
	// If false, the Write methods will prefer to return an error
	// rather than comply.
	//
	// List all places where it's used:
	// (
	//   invalid streamID in `.startWriteDataPadded()`,
	//   non-zero padding bytes in `.startWriteDataPadded()`,
	//   invalid window increment in `.WriteWindowUpdate()`,
	//   invalid streamID in `.WriteHeaders()`,
	//   invalid dependent streamID in `.WriteHeaders()`
	//   invalid streamID in `.WritePriority()`,
	//   invalid streamID in `.WriteRSTStream()`,
	//   invalid streamID in `.WriteContinuation()`,
	//   invalid streamID in `.WritePushPromise()`,
	//   invalid PromiseID in `.WritePushPromise()`,
	// )
	//
	AllowIllegalWrites bool

	// AllowIllegalReads permits the Framer's ReadFrame method
	// to return non-compliant frames or frame orders.
	// This is for testing and permits using the Framer to test
	// other HTTP/2 implementations' conformance to the spec.
	// It is not compatible with ReadMetaHeaders.
	//
	// Currently only used to allow out of order HEADERS and
	// CONTINUATION frames to be processed.
	AllowIllegalReads bool

	// ReadMetaHeaders if non-nil causes ReadFrame to merge
	// HEADERS and CONTINUATION frames together and return
	// MetaHeadersFrame instead.
	ReadMetaHeaders *hpack.Decoder

	// MaxHeaderListSize is the http2 MAX_HEADER_LIST_SIZE.
	// It's used only if ReadMetaHeaders is set; 0 means a sane default
	// (currently 16MB)
	// If the limit is hit, MetaHeadersFrame.Truncated is set true.
	//
	// SETTINGS_MAX_HEADER_LIST_SIZE: This advisory setting informs a
	// peer of the maximum size of header list that the sender is
	// prepared to accept, in octets. The value is based on the
	// uncompressed size of header fields, including the length of the
	// name and value in octets plus an overhead of 32 octets for each
	// header field.
	// For any given request, a lower limit than what is advertised MAY
	// be enforced. The initial value of this setting is unlimited.
	//
	// An endpoint can use the SETTINGS_MAX_HEADER_LIST_SIZE to advise peers
	// of limits that might apply on the size of header blocks.  This
	// setting is only advisory, so endpoints MAY choose to send header
	// blocks that exceed this limit and risk having the request or response
	// being treated as malformed (`MetaHeadersFrame.Truncated is set true`).
	// This setting is specific to a connection, so any request or response
	// could encounter a hop with a lower, unknown limit.  An intermediary can
	// attempt to avoid this problem by passing on values presented by different peers,
	// but they are not obligated to do so.
	//
	// A server that receives a larger header block than it is willing to
	// handle can send an HTTP 431 (Request Header Fields Too Large) status
	// code [RFC6585].  A client can discard responses that it cannot
	// process.  The header block MUST be processed to ensure a consistent
	// connection state, unless the connection is closed.
	//
	// 	+------------------------+------+---------------+---------------+
	//  | Name                   | Code | Initial Value | Specification |
	//  +------------------------+------+---------------+---------------+
	//  | MAX_HEADER_LIST_SIZE   | 0x6  | (infinite)    | Section 6.5.2 |
	//  +------------------------+------+---------------+---------------+
	MaxHeaderListSize uint32

	// TODO: track which type of frame & with which flags was sent
	// last. Then return an error (unless AllowIllegalWrites) if
	// we're in the middle of a header block and a
	// non-Continuation or Continuation on a different stream is
	// attempted to be written.

	// Both set to true if GODEBUG environment variable contains http2debug=2.
	logReads, logWrites bool

	// Only used for logging written writes, when this Framer's logWrites flag
	// is true (debugFramer's logReads flags is always set to false).
	// Written binary frames are written to debugFramerBuf (bellow) and the
	// read by this debugFramer. debugFramer's read serves the purpose of
	// validating correctnes of the written frame.
	debugFramer *Framer
	// When logWrites, is true written frames are placed into this buffer
	// and read by debugFramer above. debugFramer's read serves the puprose of
	// validating correctnes of the written frame.
	debugFramerBuf *bytes.Buffer
	// Set to log.Printf by default, called only when logReads is true.
	// Is used to write messages about each hpack decoded header field,
	// if ReadMetaHeaders is not nil. Is also used to write each read and
	// decoded frame as a whole.
	debugReadLoggerf func(string, ...interface{})
	// Set to log.Printf by default, called only when logWrites is true, used to write error messages,
	// if debugFramer fails to decode a written frame and info messages about successfully written frames.
	debugWriteLoggerf func(string, ...interface{})

	frameCache *frameCache // nil if frames aren't reused (default) TODO!!!!!
}

func (fr *Framer) maxHeaderListSize() uint32 {
	if fr.MaxHeaderListSize == 0 {
		return 16 << 20 // sane default (16MB), per docs
	}
	return fr.MaxHeaderListSize
}

// startWrite writes the frame header of a frame to Framer's wbuf buffer.
// Frame header binary layout: https://httpwg.org/specs/rfc7540.html#FrameHeader
func (f *Framer) startWrite(ftype FrameType, flags Flags, streamID uint32) {
	// Write the FrameHeader.
	f.wbuf = append(f.wbuf[:0],
		0, // 3 bytes of length, filled in endWrite
		0,
		0,
		byte(ftype),
		byte(flags),
		byte(streamID>>24), // big-endian
		byte(streamID>>16),
		byte(streamID>>8),
		byte(streamID),
	)
}

// endWrite writes frame body length and
// writes the binary frame to the underlying writer
func (f *Framer) endWrite() error {
	// Now that we know the final size, fill in the FrameHeader in
	// the space previously reserved for it. Abuse append.
	//
	// Length of payload in frame header is expressed
	// as an unsigned 24-bit integer, so size of frame body
	// can't be greater than (1 << 24 -1).
	length := len(f.wbuf) - frameHeaderLen
	if length >= (1 << 24) {
		return ErrFrameTooLarge
	}

	_ = append(f.wbuf[:0], byte(length>>16), byte(length>>8), byte(length)) // big-endian 24-bit length field
	if f.logWrites {
		f.logWrite()
	}

	n, err := f.w.Write(f.wbuf)
	if err == nil && n != len(f.wbuf) {
		err = io.ErrShortWrite
	}

	return err
}

func (f *Framer) logWrite() {
	if f.debugFramer == nil {
		// first time logging a written frame

		// buffer to which writtem frames will be written and later decoded by the debugFramer,
		// which validates correctness of written frames biary format
		f.debugFramerBuf = new(bytes.Buffer)
		f.debugFramer = NewFramer(nil, f.debugFramerBuf)
		f.debugFramer.logReads = false // we log it ourselves, saying "wrote" bellow (just before returning from this method)
		// Let us read anything, even if we accidentally wrote it
		// in the wrong order:
		f.debugFramer.AllowIllegalReads = true
	}
	// write frame to temporary buffer owned by `debugFramer`,
	// so that the next call to `debugFramer'`s `ReadFrame()`
	// method can validate the frame's binary format
	f.debugFramerBuf.Write(f.wbuf)
	fr, err := f.debugFramer.ReadFrame()
	if err != nil {
		f.debugWriteLoggerf("http2: Framer %p: failed to decode just-writtem frame", f) // log.Printf by default
		return
	}
	f.debugWriteLoggerf("http2: Framer %p: wrote %v", f, summarizeFrame(fr)) // log.Printf by default, write human-readable frame representation
}

func (f *Framer) writeByte(v byte)     { f.wbuf = append(f.wbuf, v) }
func (f *Framer) writeBytes(v []byte)  { f.wbuf = append(f.wbuf, v...) }
func (f *Framer) writeUint16(v uint16) { f.wbuf = append(f.wbuf, byte(v>>8), byte(v)) }
func (f *Framer) writeUint32(v uint32) {
	f.wbuf = append(f.wbuf, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

const (
	minMaxFrameSize = 1 << 14
	maxFrameSize    = 1<<24 - 1
)

// SetReuseFrames allows the Framer to reuse Frames (Data frames).
// If called on a Framer, Frames returned by calls to ReadFrame are only
// valid until the next call to ReadFrame.
func (fr *Framer) SetReuseFrames() {
	if fr.frameCache != nil {
		return
	}
	fr.frameCache = &frameCache{}
}

type frameCache struct {
	dataFrame DataFrame
}

func (fc *frameCache) getDataFrame() *DataFrame {
	if fc == nil {
		return &DataFrame{}
	}
	return &fc.dataFrame
}

// NewFramer returns a Framer that writes frames to w and reads them from r.
func NewFramer(w io.Writer, r io.Reader) *Framer {
	fr := Framer{
		w:                 w,
		r:                 r,
		countError:        func(string) {},
		logReads:          logFrameReads,  // GODEBUG http2debug=2
		logWrites:         logFrameWrites, // GODEBUG http2debug=2
		debugReadLoggerf:  log.Printf,
		debugWriteLoggerf: log.Printf,
	}
	fr.getReadBuf = func(size uint32) []byte {
		if cap(fr.readBuf) >= int(size) {
			return fr.readBuf[:size]
		}
		fr.readBuf = make([]byte, size)
		return fr.readBuf
	}

	fr.SetMaxReadFrameSize(maxFrameSize)
	return &fr
}

// SetMaxReadFrameSize sets the maximum size of a frame
// that will be read by a subsequent call to ReadFrame.
// It is the caller's responsibility to advertise this
// limit with a SETTINGS frame.
func (fr *Framer) SetMaxReadFrameSize(v uint32) {
	if v > maxFrameSize {
		v = maxFrameSize
	}
	fr.maxReadSize = v
}

// ErrorDetail returns a more detailed error of the last error
// returned by Framer.ReadFrame. For instance, if ReadFrame
// returns a StreamError with code PROTOCOL_ERROR, ErrorDetail
// will say exactly what was invalid. ErrorDetail is not guaranteed
// to return a non-nil value and like the rest of the http2 package,
// its return value is not protected by an API compatibility promise.
// ErrorDetail is reset after the next call to ReadFrame.
func (fr *Framer) ErrorDetail() error {
	return fr.errDetail
}

// ErrFrameTooLarge is returned from Framer.ReadFrame when the peer
// sends a frame that is larger than declared with SetMaxReadFrameSize.
var ErrFrameTooLarge = errors.New("http2: frame to large")

// terminalReadFrameError reports whether err is an unrecoverable
// error from ReadFrame and no other frames should be read.
func terminalReadFrameError(err error) bool {
	if _, ok := err.(StreamError); ok {
		return false
	}

	return err != nil
}

// ReadFrame reads a single frame. The returned Frame is only valid
// until the next call to ReadFrame.
//
// If the frame is larger than previously set with SetMaxReadFrameSize, the
// returned error is ErrFrameTooLarge. Other errors may be of type
// ConnectionError, StreamError, or anything else from the underlying
// reader.
func (fr *Framer) ReadFrame() (Frame, error) {
	fr.errDetail = nil
	fh, err := readFrameHeader(fr.headerBuf[:], fr.r)
	if err != nil {
		return nil, err
	}

	if fh.Length > fr.maxReadSize {
		return nil, ErrFrameTooLarge
	}

	payload := fr.getReadBuf(fh.Length)
	if _, err := io.ReadFull(fr.r, payload); err != nil {
		return nil, err
	}

	f, err := typeFrameParser(fh.Type)(fr.frameCache, fh, fr.countError, payload)
	if err != nil {
		if ce, ok := err.(connError); ok {
			return nil, fr.connError(ce.Code, ce.Reason)
		}

		return nil, err
	}

	if err := fr.checkFrameOrder(f); err != nil {
		return nil, err
	}
	if fr.logReads {
		fr.debugReadLoggerf("http2: Framer %p: read %v", fr, summarizeFrame(f))
	}
	if fh.Type == FrameHeaders && fr.ReadMetaHeaders != nil {
		return fr.readMetaFrame(f.(*HeadersFrame))
	}

	return f, nil
}

// connError returns ConnectionError(code) but first
// stashes away a public reason to the caller can optionally relay it
// to the peer before hanging up on them. This might help others debug
// their implementations.
func (fr *Framer) connError(code ErrCode, reason string) error {
	fr.errDetail = errors.New(reason)
	return ConnectionError(code)
}

// checkFrameOrder reports an error if f is an invalid frame to return
// next from ReadFrame. Mostly it checks whether HEADERS and
// CONTINUATION frames are contiguous.
func (fr *Framer) checkFrameOrder(f Frame) error {
	last := fr.lastFrame
	fr.lastFrame = f
	if fr.AllowIllegalReads {
		return nil
	}

	fh := f.Header()
	if fr.lastHeaderStream != 0 {
		if fh.Type != FrameContinuation {
			return fr.connError(ErrCodeProtocol, fmt.Sprintf(
				"got %s for stream %d; expected CONTINUATION following %s for stream %d",
				fh.Type,
				fh.StreamID,
				last.Header().Type,
				fr.lastHeaderStream,
			))
		}

		if fh.StreamID != fr.lastHeaderStream {
			return fr.connError(ErrCodeProtocol, fmt.Sprintf(
				"got CONTINUATION for stream %d; expected stream %d",
				fh.StreamID,
				fr.lastHeaderStream,
			))
		}
	} else if fh.Type == FrameContinuation {
		return fr.connError(ErrCodeProtocol, fmt.Sprintf("unexpected CONITNUATION for stream %d", fh.StreamID))
	}

	switch fh.Type {
	case FrameHeaders, FrameContinuation:
		if fh.Flags.Has(FlagHeadersEndHeaders) {
			fr.lastHeaderStream = 0
		} else {
			fr.lastHeaderStream = fh.StreamID
		}
	}

	return nil
}

// A DataFrame conveys arbitrary, variable-length sequences of octets
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
type DataFrame struct {
	FrameHeader
	data []byte
}

func (f *DataFrame) StreamEnded() bool {
	return f.FrameHeader.Flags.Has(FlagDataEndStream)
}

// Data returns the frame's data octets, not including any padding
// size byte or padding suffix bytes.
// The caller must not retain the returned memory past the next
// call to ReadFrame.
func (f *DataFrame) Data() []byte {
	f.checkValid()
	return f.data
}

func parseDataFrame(fc *frameCache, fh FrameHeader, countError func(string), payload []byte) (Frame, error) {
	if fh.StreamID == 0 {
		// DATA frames MUST be associated with a stream. If a
		// DATA frame is received whose stream identifier
		// field is 0x0, the recipient MUST respond with a
		// connection error (Section 5.4.1) of type
		// PROTOCOL_ERROR.
		countError("frame_data_stream_0")
		return nil, connError{ErrCodeProtocol, "DATA frame with stream ID 0"}
	}

	f := fc.getDataFrame() // TODO!!!!! how is the frame cache used?
	f.FrameHeader = fh

	var padSize byte
	if fh.Flags.Has(FlagHeadersPadded) {
		var err error
		if payload, padSize, err = readByte(payload); err != nil {
			countError("frame_data_pad_byte_short")
			return nil, err
		}
	}

	if int(padSize) > len(payload) {
		// If the length of the padding is greater than the
		// length of the frame payload, the recipient MUST
		// treat this as a connection error.
		// Filed: https://github.com/http2/http2-spec/issues/610
		countError("frame_data_pad_too_big")
		return nil, connError{ErrCodeProtocol, "pad size larger than data payload"}
	}

	f.data = payload[:len(payload)-int(padSize)]
	return f, nil
}

var (
	errStreamID    = errors.New("invalid stream ID")
	errDepStreamID = errors.New("invalid dependent stream ID")
	errPadLength   = errors.New("pad length to large")
	errPadBytes    = errors.New("padding bytes must all be zeros unless AllowIllegalWrites is enabled")
)

// validStreamIDOrZero returns true if the
// passed in `streamID` fits into 31 bits.
func validStreamIDOrZero(streamID uint32) bool {
	return streamID&(1<<31) == 0
}

// validStreamID returns true if the passed
// in `streamID` is not zero and fits into
// 31 bits.
func validStreamID(streamID uint32) bool {
	return streamID != 0 && streamID&(1<<31) == 0
}

// WriteData writes a DATA frame.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility not to violate the maximum frame size
// and to not call other Write methods concurrently.
func (f *Framer) WriteData(streamID uint32, endStream bool, data []byte) error {
	return f.WriteDataPadded(streamID, endStream, data, nil)
}

// WriteDataPadded writes a DATA frame with optional padding.
//
// If pad is nil, the padding bit is not sent.
// The length of pad must not exceed 255 bytes.
// The bytes of pad must all be zero, unless f.AllowIllegalWrites is set.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility not to violate the maximum frame size
// and to not call other Write methods concurrently.
func (f *Framer) WriteDataPadded(streamID uint32, endStream bool, data, pad []byte) error {
	if err := f.startWriteDataPadded(streamID, endStream, data, pad); err != nil {
		return err
	}
	// writes frame body length into the first 3 octets of the frame header
	// and flushes the binary frame to the underlying writer
	return f.endWrite()
}

// startWriteDataPadded is WriteDataPadded, but only writes the frame to the Framer's internal buffer.
// The caller should call endWrite to write frame's body length into the frame header and flush the frame
// to the underlying writer.
func (f *Framer) startWriteDataPadded(streamID uint32, endStream bool, data, pad []byte) error {
	if !validStreamID(streamID) && !f.AllowIllegalWrites {
		// `streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}

	if len(pad) > 0 {
		if len(pad) > 255 {
			// since the pad length field within a DATA frame
			// occupies only the 1st byte, it CANNOT specify
			// padding longer than 255 bytes
			return errPadLength
		}
		if !f.AllowIllegalWrites {
			for _, b := range pad {
				if b != 0 {
					// "Padding octets MUST be set to zero when sending."
					return errPadBytes
				}
			}
		}
	}

	var flags Flags
	if endStream {
		flags |= FlagDataEndStream
	}
	if pad != nil {
		flags |= FlagDataPadded
	}
	// writes the frame header to Framer's wbuf buffer
	// (frame type, flags and stream id, frame length
	// will be written during the call to `f.endWrite()`)
	f.startWrite(FrameData, flags, streamID)

	// +---------------+
	// |Pad Length? (8)|
	// +---------------+-----------------------------------------------+
	// |                            Data (*)                         ...
	// +---------------------------------------------------------------+
	// |                           Padding (*)                       ...
	// +---------------------------------------------------------------+
	if pad != nil {
		// Pad Length: An 8-bit field containing the length of the frame padding
		// in units of octets. This field is conditional and is only present if
		// the PADDED flag is set.
		f.wbuf = append(f.wbuf, byte(len(pad))) //
	}
	// Data: Application data. The amount of data is the remainder of the frame
	// payload after subtracting the length of the other fields that are present.
	f.wbuf = append(f.wbuf, data...)
	// Padding: Padding octets that contain no application semantic value.
	f.wbuf = append(f.wbuf, pad...)
	return nil
}

// A SettingsFrame conveys configuration parameters that affect how
// endpoints communicate, such as preferences and constraints on peer
// behavior.
//
// See https://httpwg.org/specs/rfc7540.html#SETTINGS
// book: page 124-127
//
// The payload of a SETTINGS frame consists of zero or more parameters,
// each consisting of an unsigned 16-bit setting identifier and
// an unsigned 32-bit value.
//
// +-------------------------------+
// |       Identifier (16)         |
// +-------------------------------+-------------------------------+
// |                        Value (32)                             |
// +---------------------------------------------------------------+
//
// The following parameters are defined:
//
// SETTINGS_HEADER_TABLE_SIZE (0x1): Allows the sender to inform the remote endpoint of the maximum size of
// the header compression table used to decode header blocks, in octets. The encoder can select any size equal to
// or less than this value by using signaling specific to the header compression format inside a header block.
// The initial value is 4,096 octets.
//
// SETTINGS_ENABLE_PUSH (0x2):
// This setting can be used to disable server push. An endpoint MUST NOT send a PUSH_PROMISE frame
// if it receives this parameter set to a value of 0. An endpoint that has both set this parameter to 0
// and had it acknowledged MUST treat the receipt of a PUSH_PROMISE frame as a connection error of type PROTOCOL_ERROR.
// The initial value is 1, which indicates that server push is permitted. Any value other than 0 or 1 MUST be treated
// as a connection error of type PROTOCOL_ERROR.
//
// SETTINGS_MAX_CONCURRENT_STREAMS (0x3):
// Indicates the maximum number of concurrent streams that the sender will allow. This limit is directional:
// it applies to the number of streams that the sender permits the receiver to create. Initially, there is no limit to this value.
// It is recommended that this value be no smaller than 100, so as to not unnecessarily limit parallelism.
// A value of 0 for SETTINGS_MAX_CONCURRENT_STREAMS SHOULD NOT be treated as special by endpoints. A zero value does prevent
// the creation of new streams; however, this can also happen for any limit that is exhausted with active streams.
// Servers SHOULD only set a zero value for short durations; if a server does not wish to accept requests,
// closing the connection is more appropriate.
//
// SETTINGS_INITIAL_WINDOW_SIZE (0x4):
// Indicates the sender's initial window size (in octets) for stream-level flow control.
// The initial value is 216-1 (65,535) octets. This setting affects the window size of all streams.
// Values above the maximum flow-control window size of 231-1 MUST be treated as a connection error of type FLOW_CONTROL_ERROR.
//
// SETTINGS_MAX_FRAME_SIZE (0x5):
// Indicates the size of the largest frame payload that the sender is willing to receive, in octets.
// The initial value is 214 (16,384) octets. The value advertised by an endpoint MUST be between this initial value
// and the maximum allowed frame size (224-1 or 16,777,215 octets), inclusive. Values outside this range MUST be treated
// as a connection error of type PROTOCOL_ERROR.
//
// SETTINGS_MAX_HEADER_LIST_SIZE (0x6):
// This advisory setting informs a peer of the maximum size of header list that the sender is prepared to accept, in octets.
// The value is based on the uncompressed size of header fields, including the length of the name and value in octets
// plus an overhead of 32 octets for each header field. For any given request, a lower limit than what is advertised MAY be enforced.
// The initial value of this setting is unlimited.
//
// Most values in SETTINGS benefit from or require an understanding of when the peer has received and applied the changed parameter values.
// In order to provide such synchronization timepoints, the recipient of a SETTINGS frame in which the ACK flag is not set MUST apply
// the updated parameters as soon as possible upon receipt.
//
// The values in the SETTINGS frame MUST be processed in the order they appear, with no other frame processing between values.
// Unsupported parameters MUST be ignored. Once all values have been processed, the recipient MUST immediately emit a SETTINGS frame
// with the ACK flag set. Upon receiving a SETTINGS frame with the ACK flag set, the sender of the altered parameters can rely
// on the setting having been applied.
//
// If the sender of a SETTINGS frame does not receive an acknowledgement within a reasonable amount of time, it MAY issue a connection error
// of type SETTINGS_TIMEOUT.
type SettingsFrame struct {
	FrameHeader
	p []byte
}

func parseSettingsFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	if fh.Flags.Has(FlagSettingsAck) && fh.Length > 0 {
		// When this (ACK 0x1) bit is set, the payload of the
		// SETTINGS frame MUST be empty. Receipt of a
		// SETTINGS frame with the ACK flag set and a length
		// field value other than 0 MUST be treated as a
		// connection error (Section 6.5) of type
		// FRAME_SIZE_ERROR.
		// https://httpwg.org/specs/rfc7540.html#SETTINGS
		countError("frame_settings_ack_with_length")
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	if fh.StreamID != 0 {
		// SETTINGS frames always apply to a connection,
		// never a single stream. The stream identifier for a
		// SETTINGS frame MUST be zero (0x0).  If an endpoint
		// receives a SETTINGS frame whose stream identifier
		// field is anything other than 0x0, the endpoint MUST
		// respond with a connection error (Section 5.4.1) of
		// type PROTOCOL_ERROR.
		// https://httpwg.org/specs/rfc7540.html#SETTINGS
		countError("frame_settings_has_stream")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	if len(p)%6 != 0 {
		countError("frame_settings_mod_6")
		// Expecting even number of 6 byte settings.
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	f := SettingsFrame{FrameHeader: fh, p: p}
	if v, ok := f.Value(SettingInitialWindowSize); ok && v > (1<<31)-1 {
		countError("frame_settings_window_size_too_big")
		// Values above the maximum flow control window size of 2^31 - 1 MUST
		// be treated as a connection error (Section 5.4.1) of type
		// FLOW_CONTROL_ERROR.
		return nil, ConnectionError(ErrCodeFlowControl)
	}

	return &f, nil
}

func (f *SettingsFrame) IsAck() bool {
	return f.Flags.Has(FlagSettingsAck)
}

func (f *SettingsFrame) Value(id SettingID) (v uint32, ok bool) {
	f.checkValid()
	for i := 0; i < f.NumSettings(); i++ {
		if s := f.Setting(i); s.ID == id {
			return s.Val, true
		}
	}

	return 0, false
}

// Setting returns the setting from the frame at the given 0-based index.
// The index must be >= 0 and less than f.NumSettings().
func (f *SettingsFrame) Setting(i int) Setting {
	buf := f.p
	return Setting{
		ID:  SettingID(binary.BigEndian.Uint16(buf[i*6 : (i*6)+2])), // range explained: from i*6 means from the 0-based index, to (i*6)+2 means the id is 16 bits wide
		Val: binary.BigEndian.Uint32(buf[(i*6)+2 : (i*6)+6]),        // range explained: (i*6)+2 means just after the setting id, (i*6)+6 means 4 bytes for the setting value
	}
}

func (f *SettingsFrame) NumSettings() int { return len(f.p) / 6 }

// HasDuplicates reports whether f contains any duplicate setting IDs.
func (f *SettingsFrame) HasDuplicates() bool {
	num := f.NumSettings()
	if num == 0 {
		return false
	}

	// If it's small enough (the common case), just do the n^2
	// thing and avoid a map allocation.
	if num < 10 {
		for i := 0; i < num; i++ {
			idi := f.Setting(i).ID
			for j := i + 1; j < num; j++ {
				idj := f.Setting(j).ID
				if idi == idj {
					return true
				}
			}
		}
	}

	seen := map[SettingID]bool{}
	for i := 0; i < num; i++ {
		id := f.Setting(i).ID
		if seen[id] {
			return true
		}
		seen[id] = true
	}

	return false
}

func (f *SettingsFrame) ForEachSetting(fn func(Setting) error) error {
	f.checkValid()
	for i := 0; i < f.NumSettings(); i++ {
		if err := fn(f.Setting(i)); err != nil {
			return err
		}
	}
	return nil
}

// WriteSettings writes a SETTINGS frame with zero or more settings
// specified and the ACK bit not set.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WriteSettings(settings ...Setting) error {
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameSettings, 0, 0)
	for _, s := range settings {
		f.writeUint16(uint16(s.ID))
		f.writeUint32(s.Val)
	}
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// WriteSettingsAck writes an empty SETTINGS frame with the ACK bit set.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WriteSettingsAck() error {
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameSettings, FlagSettingsAck, 0)
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

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
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.7
// book: page 137-138
type PingFrame struct {
	FrameHeader
	Data [8]byte
}

func (f *PingFrame) IsAck() bool { return f.FrameHeader.Flags.Has(FlagPingAck) }

func parsePingFrame(_ *frameCache, fh FrameHeader, countError func(string), payload []byte) (Frame, error) {
	if len(payload) != 8 {
		// Receipt of a PING frame with a length field value
		// other than 8 MUST be treated as a connection error
		// of type FRAME_SIZE_ERROR.
		countError("frame_ping_length")
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	if fh.StreamID != 0 {
		// PING frames are not associated with any individual stream.
		// If a PING frame is received with a stream identifier field
		// value other than 0x0, the recipient MUST respond with a
		// connection error of type PROTOCOL_ERROR.
		countError("frame_ping_has_stream")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	f := PingFrame{FrameHeader: fh}
	copy(f.Data[:], payload)

	return &f, nil
}

func (f *Framer) WritePing(ack bool, data [8]byte) error {
	var flags Flags
	if ack {
		flags |= FlagPingAck
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FramePing, flags, 0)
	f.writeBytes(data[:]) // appends bytes to the underlying `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// A GoAwayFrame is used to initiate shutdown of a connection
// or to signal serious error conditions. It allows an endpoint
// to gracefully stop accepting new streams while still finishing
// processing of previously established streams (informs the remote
// peer to stop creating streams on this connection). This enables
// administrative actions, like server maintenance.
//
// There is an inherent race condition between an endpoint starting
// new streams and the remote sending a GOAWAY frame. To deal with
// this case, the GOAWAY contains the stream identifier of the last
// peer-initiated stream that was or might be processed on the sending
// endpoint in this connection. For instance, if the server sends a
// GOAWAY frame, the identified stream is the highest-numbered stream
// initiated by the client.
//
// Once sent, the sender will ignore frames sent on streams initiated
// by the receiver if the stream has an identifier higher than the included
// last stream identifier. Receivers of a GOAWAY frame MUST NOT open additional
// streams on the connection, although a new connection can be established for
// new streams.
//
// If the receiver of the GOAWAY has sent data on streams with a higher stream
// identifier than what is indicated in the GOAWAY frame, those streams are not
// or will not be processed. The receiver of the GOAWAY frame can treat the streams
// as though they had never been created at all, thereby allowing those streams to be
// retried later on a new connection.
//
// Endpoints SHOULD always send a GOAWAY frame before closing a connection so that the
// remote peer can know whether a stream has been partially processed or not. For example,
// if an HTTP client sends a POST at the same time that a server closes a connection, the
// client cannot know if the server started to process that POST request if the server does
// not send a GOAWAY frame to indicate what streams it might have acted on.
//
// An endpoint might choose to close a connection without sending a GOAWAY for misbehaving peers.
//
// A GOAWAY frame might not immediately precede closing of the connection; a receiver of a GOAWAY
// that has no more use for the connection SHOULD still send a GOAWAY frame before terminating
// the connection.
//
// The last stream identifier in the GOAWAY frame contains the highest-numbered stream identifier
// for which the sender of the GOAWAY frame might have taken some action on or might yet take action on.
// All streams up to and including the identified stream might have been processed in some way.
// The last stream identifier can be set to 0 if no streams were processed.
//
// Note: In this context, "processed" means that some data from the stream was passed to some higher layer
// of software that might have taken some action as a result.
//
// On streams with lower- or equal-numbered identifiers that were not closed completely prior to the connection
// being closed, reattempting requests, transactions, or any protocol activity is not possible, with the exception
// of idempotent actions like HTTP GET, PUT, or DELETE. Any protocol activity that uses higher-numbered streams
// can be safely retried using a new connection.
//
// Activity on streams numbered lower or equal to the last stream identifier might still complete successfully.
// The sender of a GOAWAY frame might gracefully shut down a connection by sending a GOAWAY frame, maintaining
// the connection in an "open" state until all in-progress streams complete.
//
// An endpoint MAY send multiple GOAWAY frames if circumstances change. For instance, an endpoint that sends
// GOAWAY with NO_ERROR during graceful shutdown could subsequently encounter a condition that requires immediate
// termination of the connection. The last stream identifier from the last GOAWAY frame received indicates which
// streams could have been acted upon. Endpoints MUST NOT increase the value they send in the last stream identifier,
// since the peers might already have retried unprocessed requests on another connection.
//
// A client that is unable to retry requests loses all requests that are in flight when the server closes the connection.
// This is especially true for intermediaries that might not be serving clients using HTTP/2. A server that is attempting
// to gracefully shut down a connection SHOULD send an initial GOAWAY frame with the last stream identifier set to 23^1-1
// and a NO_ERROR code. This signals to the client that a shutdown is imminent and that initiating further requests is prohibited.
// After allowing time for any in-flight stream creation (at least one round-trip time), the server can send another GOAWAY frame
// with an updated last stream identifier. This ensures that a connection can be cleanly shut down without losing requests.
//
// After sending a GOAWAY frame, the sender can discard frames for streams initiated by the receiver with identifiers higher than
// the identified last stream. However, any frames that alter connection state cannot be completely ignored. For instance, HEADERS,
// PUSH_PROMISE, and CONTINUATION frames MUST be minimally processed to ensure the state maintained for header compression is consistent
// ; similarly, DATA frames MUST be counted toward the connection flow-control window. Failure to process these frames can cause flow control
// or header compression state to become unsynchronized.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.8
// book: page 135-136
//
//	+-+-------------------------------------------------------------+
//	|R|                  Last-Stream-ID (31)                        |
//	+-+-------------------------------------------------------------+
//	|                      Error Code (32)                          |
//	+---------------------------------------------------------------+
//	|                  Additional Debug Data (*)                    |
//	+---------------------------------------------------------------+
type GoAwayFrame struct {
	FrameHeader
	LastStreamID uint32
	ErrCode      ErrCode
	debugData    []byte
}

// DebugData returns any debug data in the GOAWAY frame. Its contents
// are not defined.
// The caller must not retain the returned memory past the next
// call to ReadFrame.
func (f *GoAwayFrame) DebugData() []byte {
	f.checkValid()
	return f.debugData
}

func parseGoAwayFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	if fh.StreamID != 0 {
		// The GOAWAY frame applies to the connection, not a specific stream.
		// An endpoint MUST treat a GOAWAY frame with a stream identifier other
		// than 0x0 as a connection error of type PROTOCOL_ERROR.
		countError("frame_goaway_has_stream")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	if len(p) < 8 {
		// should at least contain a reserved bit, a 31-bit LastStreamID
		// and a 32-bit ErrCode
		countError("frame_goaway_short")
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	return &GoAwayFrame{
		FrameHeader:  fh,
		LastStreamID: binary.BigEndian.Uint32(p[:4]) & (1<<31 - 1), // & (1<<31 - 1) - zero out the higher-order reserved bit
		ErrCode:      ErrCode(binary.BigEndian.Uint32(p[:8])),
		debugData:    p[8:],
	}, nil
}

func (f *Framer) WriteGoAway(maxStreamID uint32, code ErrCode, debugData []byte) error {
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameGoAway, 0, 0)
	f.writeUint32(maxStreamID & (1<<31 - 1)) // mask off reserved 1st bit and write to `wbuf` in big-endian order
	f.writeUint32(uint32(code))              // write to `wbuf` in big-endian order
	f.writeBytes(debugData)                  // appends bytes to the underlying `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// An UnknownFrame is the frame type returned when the frame type is unknown
// or no specific frame type parser exists.
type UnknownFrame struct {
	FrameHeader
	p []byte
}

// Payload returns the frame's payload (after the header).  It is not
// valid to call this method after a subsequent call to
// Framer.ReadFrame, nor is it valid to retain the returned slice.
// The memory is owned by the Framer and is invalidated when the next
// frame is read.
func (f *UnknownFrame) Payload() []byte {
	f.checkValid()
	return f.p
}

func parseUnknownFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	return &UnknownFrame{fh, p}, nil
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
type WindownUpdateFrame struct {
	FrameHeader
	Increment uint32 // never read with high bit set
}

func parseWindowUpdateFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	if len(p) != 4 {
		// A WINDOW_UPDATE frame with a length other than 4 octets
		// MUST be treated as a connection error of type FRAME_SIZE_ERROR.
		countError("frame_windowupdate_bad_len")
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	inc := binary.BigEndian.Uint32(p[:4]) & 0x7fffffff // mask off high reserved bit
	if inc == 0 {
		// A receiver MUST treat the receipt of a
		// WINDOW_UPDATE frame with an flow control window
		// increment of 0 as a stream error (Section 5.4.2) of
		// type PROTOCOL_ERROR; errors on the connection flow
		// control window MUST be treated as a connection
		// error (Section 5.4.1).
		if fh.StreamID == 0 {
			return nil, ConnectionError(ErrCodeProtocol)
		}

		countError("frame_windowupdate_zero_inc_stream")
		return nil, streamError(fh.StreamID, ErrCodeProtocol)
	}

	return &WindownUpdateFrame{
		FrameHeader: fh,
		Increment:   inc,
	}, nil
}

// WriteWindowUpdate writes a WINDOW_UPDATE frame.
// The increment value must be between 1 and 2,147,483,647, inclusive.
// If the Stream ID is zero, the window update applies to the
// connection as a whole.
func (f *Framer) WriteWindowUpdate(streamId, incr uint32) error {
	// "The legal range for the increment to the flow control window is 1 to 2^31-1 (2,147,483,647) octets."
	if (incr < 1 || incr > 2147483647) && !f.AllowIllegalWrites {
		return errors.New("illegal window increment value")
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameWindowUpdate, 0, streamId)
	f.writeUint32(incr) // no need to mask off high bit, because bound checks were already performed above and the high bit is definitely NOT set
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// A HeadersFrame is used to open a stream and additionally carries a
// header block fragment. It can be sent on a stream in the "idle",
// "reserved (local)", "open", or "half-closed (remote)" state.
//
// https://httpwg.org/specs/rfc7540.html#rfc.section.6.2
// book: page 129-134
//
// +---------------+
// |Pad Length? (8)|
// +-+-------------+-----------------------------------------------+
// |E|                 Stream Dependency? (31)                     |
// +-+-------------+-----------------------------------------------+
// |  Weight? (8)  |
// +-+-------------+-----------------------------------------------+
// |                   Header Block Fragment (*)                 ...
// +---------------------------------------------------------------+
// |                           Padding (*)                       ...
// +---------------------------------------------------------------+
//
// Pad Length: An 8-bit field containing the length of the frame padding in units of octets.
// This field is only present if the PADDED flag is set.
//
// E (`.Priority.Exclusive`): A single-bit flag indicating that the stream dependency is exclusive.
// This field is only present if the PRIORITY flag is set.
//
// Stream Dependency (`.Priority.StreamDep`): A 31-bit stream identifier for the stream that this stream depends on.
// This field is only present if the PRIORITY flag is set.
//
// Weight (`.Priority.Weight`): An unsigned 8-bit integer representing a priority weight for the stream.
// Add one to the value to obtain a weight between 1 and 256.
// This field is only present if the PRIORITY flag is set.
//
// Header Block Fragment (`.headerFragBuf`): A header block fragment
//
// Padding: Padding octets.
type HeadersFrame struct {
	FrameHeader

	// Priority is set if FlagHeadersPriority is set in the FrameHeader.
	Priority PriorityParam

	// The payload of a HEADERS frame contains a header block fragment.
	// A header block that does not fit within a HEADERS frame is continued
	// in a CONTINUATION frame.
	headerFragBuf []byte // not owned
}

func (f *HeadersFrame) HeaderBlockFragment() []byte {
	f.checkValid()
	return f.headerFragBuf
}

// When set, bit 2 indicates that this frame contains an entire header block
// and is not followed by any CONTINUATION frames.
//
// A HEADERS frame without the END_HEADERS flag set MUST be followed by a
// CONTINUATION frame for the same stream.
func (f *HeadersFrame) HeadersEnded() bool {
	return f.FrameHeader.Flags.Has(FlagHeadersEndHeaders)
}

// When set, bit 0 indicates that the header block is the last
// that the endpoint will send for the identified stream.
//
// A HEADERS frame carries the END_STREAM flag that signals the end of a stream.
// However, a HEADERS frame with the END_STREAM flag set can be followed by
// CONTINUATION frames on the same stream. Logically, the CONTINUATION frames
// are part of the HEADERS frame.
func (f *HeadersFrame) StreamEnded() bool {
	return f.FrameHeader.Flags.Has(FlagHeadersEndStream)
}

// When set, bit 5 indicates that the Exclusive Flag (E),
// Stream Dependency, and Weight fields are present.
func (f *HeadersFrame) HasPriority() bool {
	return f.FrameHeader.Flags.Has(FlagHeadersPriority)
}

func parseHeadersFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (_ Frame, err error) {
	hf := HeadersFrame{
		FrameHeader: fh,
	}

	if fh.StreamID == 0 {
		// HEADERS frames MUST be associated with a stream. If a HEADERS frame
		// is received whose stream identifier field is 0x0, the recipient MUST
		// respond with a connection error (Section 5.4.1) of type
		// PROTOCOL_ERROR.
		countError("frame_headers_zero_stream")
		return nil, connError{ErrCodeProtocol, "HEADERS frame with stream ID 0"}
	}

	var padLength uint8
	if fh.Flags.Has(FlagHeadersPadded) {
		// When set, bit 3 indicates that the Pad Length field
		// and any padding that it describes are present.
		if p, padLength, err = readByte(p); err != nil {
			countError("frame_headers_pad_short")
			return
		}
	}

	if fh.Flags.Has(FlagHeadersPriority) {
		// When set, bit 5 indicates that the Exclusive Flag (E),
		// Stream Dependency, and Weight fields are present.
		var v uint32
		if p, v, err = readUint32(p); err != nil {
			countError("frame_headers_prio_short")
			return nil, err
		}

		hf.Priority.StreamDep = v & 0x7fffffff               // mask of the high bit, which is the exclusive flag
		hf.Priority.Exclusive = (v != hf.Priority.StreamDep) // high bit was set
		if p, hf.Priority.Weight, err = readByte(p); err != nil {
			countError("frame_headers_prio_weight_short")
			return nil, err
		}
	}

	if len(p)-int(padLength) < 0 {
		// Padding that exceeds the size remaining for the header block fragment
		// MUST be treated as a PROTOCOL_ERROR.
		countError("frame_headers_pad_too_big")
		return nil, streamError(fh.StreamID, ErrCodeProtocol)
	}

	hf.headerFragBuf = p[:len(p)-int(padLength)]

	return &hf, nil
}

// HeadersFrameParam are the parameters for writing a HEADERS frame.
type HeadersFrameParam struct {
	// StreamID is the required Stream ID to initiate
	// when is part of a PUSH_PROMISE frame.
	StreamID uint32
	// BlockFragment is part (or all) of a Header Block.
	BlockFragment []byte

	// EndStream indicates that the header block is the last that
	// the endpoint will send for the identified stream. Setting
	// this flag causes the stream to enter one of "half closed"
	// states.
	EndStream bool

	// EndHeaders indicates that this frame contains an entire
	// header block and is not followed by any
	// CONTINUATION frames.
	EndHeaders bool

	// PadLength is the optional number of bytes of zeros to add
	// to this frame.
	PadLength uint8

	// Priority, if non-zero, includes stream priority information
	// in the HEADERS frame.
	Priority PriorityParam
}

// WriteHeaders writes a single HEADERS frame.
//
// This is a low-level header writing method. Encoding headers and
// splitting them into any necessary CONTINUATION frames is handled
// elsewhere. TODO!!!!! where?
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WriteHeaders(p HeadersFrameParam) error {
	if !validStreamID(p.StreamID) && !f.AllowIllegalWrites {
		// `streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}
	var flags Flags
	if p.PadLength != 0 {
		flags |= FlagHeadersPadded
	}
	if p.EndStream {
		flags |= FlagHeadersEndStream
	}
	if p.EndHeaders {
		flags |= FlagHeadersEndHeaders
	}
	if !p.Priority.IsZero() {
		// either stream id is NOT 0,
		// or exclusive is true,
		// or weight is NOT 0
		flags |= FlagHeadersPriority
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameHeaders, flags, p.StreamID)
	if p.PadLength != 0 {
		f.writeByte(p.PadLength) // append value of pad length field to `wbuf`, which is a single octet in size
	}
	if !p.Priority.IsZero() {
		// either stream id is NOT 0,
		// or exclusive is true,
		// or weight is NOT 0
		v := p.Priority.StreamDep
		if !validStreamIDOrZero(v) && !f.AllowIllegalWrites {
			// dependent stream id should be 0
			// or should NOT exceed 31 bits
			return errDepStreamID
		}
		if p.Priority.Exclusive {
			v |= 1 << 31 // set highest bit, which is the exclusive flag
		}
		f.writeUint32(v)               // append exclusive flag with dependent stream id to `wbuf`
		f.writeByte(p.Priority.Weight) // append one octet singnifying the priority weight to `wbuf`
	}
	f.wbuf = append(f.wbuf, p.BlockFragment...)        // append the actual headers fragment to `wbuf`
	f.wbuf = append(f.wbuf, padZeros[:p.PadLength]...) // append padding if any to `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// A PriorityFrame specifies the sender-advised priority of a stream.
// It can be sent in any stream state, including idle or closed streams.
//
// The PRIORITY frame can be sent on a stream in any state, though it
// cannot be sent between consecutive frames that comprise a single header block.
// Note that this frame could arrive after processing or frame sending has completed,
// which would cause it to have no effect on the identified stream. For a stream that
// is in the "half-closed (remote)" or "closed" state, this frame can only affect processing
// of the identified stream and its dependent streams; it does not affect frame transmission
// on that stream.
//
// The PRIORITY frame can be sent for a stream in the "idle" or "closed" state. This allows for
// the reprioritization of a group of dependent streams by altering the priority of an unused
// or closed parent stream.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.3
// book: pages 128-129
//
// +-+-------------------------------------------------------------+
// |E|                  Stream Dependency (31)                     |
// +-+-------------+-----------------------------------------------+
// |   Weight (8)  |
// +-+-------------+
//
// E: A single-bit flag indicating that the stream dependency is exclusive.
//
// Stream Dependency: A 31-bit stream identifier for the stream that this stream depends on.
//
// Weight: An unsigned 8-bit integer representing a priority weight for the stream.
// Add one to the value to obtain a weight between 1 and 256.
type PriorityFrame struct {
	FrameHeader
	PriorityParam
}

// PriorityParam are the stream prioritzation parameters.
// book: page 128-129, 131
type PriorityParam struct {
	// StreamDep is a 31-bit stream identifier for the
	// stream that this stream depends on. Zero means no
	// dependency.
	StreamDep uint32

	// Exclusive is whether the dependency is exclusive.
	Exclusive bool

	// Weight is the stream's zero-indexed weight. It should be
	// set together with StreamDep, or neither should be set. Per
	// the spec, "Add one to the value to obtain a weight between
	// 1 and 256."
	Weight uint8
}

func (p PriorityParam) IsZero() bool {
	return p == PriorityParam{}
}

func parsePriorityFrame(_ *frameCache, fh FrameHeader, countError func(string), payload []byte) (Frame, error) {
	if fh.StreamID == 0 {
		// If a PRIORITY frame is received with a stream identifier of 0x0,
		// the recipient MUST respond with a connection error of type PROTOCOL_ERROR.
		countError("frame_priority_zero_stream")
		return nil, connError{ErrCodeProtocol, "PRIORITY frame stream ID 0"}
	}

	if len(payload) != 5 {
		// A PRIORITY frame with a length other than 5 octets MUST be treated
		// as a stream error of type FRAME_SIZE_ERROR.
		return nil, connError{ErrCodeFrameSize, fmt.Sprintf("PRIORITY frame payload size was %d; want 5", len(payload))}
	}

	v := binary.BigEndian.Uint32(payload[:4])
	streamID := v & 0x7fffffff // // mask off high bit, which is the exclusive flag
	return &PriorityFrame{
		FrameHeader: fh,
		PriorityParam: PriorityParam{
			Weight:    payload[4],
			StreamDep: streamID,
			Exclusive: streamID != v, // was high bit set?
		},
	}, nil
}

// WritePriority writes a PRIORITY frame.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WritePriority(streamID uint32, p PriorityParam) error {
	if !validStreamID(streamID) && !f.AllowIllegalWrites {
		// `streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}
	if !validStreamIDOrZero(p.StreamDep) {
		// dependent stream id should be 0
		// or should NOT exceed 31 bits
		return errDepStreamID
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FramePriority, 0, streamID)
	v := p.StreamDep
	if p.Exclusive {
		// set highest bit, which is the exclusive flag
		v |= 1 << 31
	}
	f.writeUint32(v)      // append exclusive bit and dependent stream id to Framer's `wbuf`
	f.writeByte(p.Weight) // append one octet singnifying the priority weight to Framer's `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// A RSTStreamFrame allows for immediate abnormal termination of a stream.
// RST_STREAM is sent to request cancellation of a stream or to indicate
// that an error condition has occurred.
//
// The RST_STREAM frame contains a single unsigned, 32-bit integer identifying
// the error code . The error code indicates why the stream is being terminated.
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
type RSTStreamFrame struct {
	FrameHeader
	ErrCode ErrCode
}

func parseRSTStreamFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	if len(p) != 4 {
		// A RST_STREAM frame with a length other than 4 octets MUST be treated
		// as a connection error of type FRAME_SIZE_ERROR.
		countError("frame_rststream_bad_len")
		return nil, ConnectionError(ErrCodeFrameSize)
	}

	if fh.StreamID == 0 {
		// RST_STREAM frames MUST be associated with a stream.
		// If a RST_STREAM frame is received with a stream identifier of 0x0,
		// the recipient MUST treat this as a connection error of type PROTOCOL_ERROR.
		countError("frame_rststream_zero_stream")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	return &RSTStreamFrame{fh, ErrCode(binary.BigEndian.Uint32(p[:4]))}, nil
}

// WriteRSTStream writes a RST_STREAM frame.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WriteRSTStream(streamID uint32, code ErrCode) error {
	if !validStreamID(streamID) && !f.AllowIllegalWrites {
		// `streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameRSTStream, 0, streamID)
	f.writeUint32(uint32(code)) // append code to Framer's `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// A ContinuationFrame is used to continue a sequence of header block fragments.
// Any number of CONTINUATION frames can be sent, as long as the preceding frame
// is on the same stream and is a HEADERS, PUSH_PROMISE, or CONTINUATION frame
// without the END_HEADERS flag set.
//
// The CONTINUATION frame payload contains a header block fragment.
//
// See https://httpwg.org/specs/rfc7540.html#rfc.section.6.10
// book: page 137
//
//	+---------------------------------------------------------------+
//	|                   Header Block Fragment (*)                 ...
//	+---------------------------------------------------------------+
type ContinuationFrame struct {
	FrameHeader
	headerFragBuf []byte
}

func parseContinuationFrame(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (Frame, error) {
	if fh.StreamID == 0 {
		// CONTINUATION frames MUST be associated with a stream.
		// If a CONTINUATION frame is received whose stream identifier field is 0x0,
		// the recipient MUST respond with a connection error of type PROTOCOL_ERROR.
		countError("frame_continuation_zero_stream")
		return nil, connError{ErrCodeProtocol, "CONTINUATION frame with stream ID 0"}
	}

	return &ContinuationFrame{fh, p}, nil
}

func (f *ContinuationFrame) HeaderBlockFragment() []byte {
	f.checkValid()
	return f.headerFragBuf
}

// When set, bit 2 indicates that this frame ends a header block.
// If the END_HEADERS bit is not set, this frame MUST be followed
// by another CONTINUATION frame.
// A receiver MUST treat the receipt of any other type of frame or
// a frame on a different stream as a connection error of type PROTOCOL_ERROR.
func (f *ContinuationFrame) HeadersEnded() bool {
	return f.FrameHeader.Flags.Has(FlagContinuationEndHeaders)
}

// WriteContinuation writes a CONTINUATION frame.
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WriteContinuation(streamID uint32, endHeaders bool, headerBlockFragment []byte) error {
	if !validStreamID(streamID) && !f.AllowIllegalWrites {
		// `streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}
	var flags Flags
	if endHeaders {
		flags |= FlagContinuationEndHeaders
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FrameContinuation, flags, streamID)
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	f.wbuf = append(f.wbuf, headerBlockFragment...) // append bytes to Framer's `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

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
type PushPromiseFrame struct {
	FrameHeader

	// PUSH_PROMISE frame includes
	// the unsigned 31-bit identifier
	// of the stream the endpoint plans
	// to create
	PromiseID uint32

	// along with a set of headers that
	// provide additional context for
	// the stream.
	headerFragBuf []byte // not owned
}

func (f *PushPromiseFrame) HeaderBlockFragment() []byte {
	f.checkValid()
	return f.headerFragBuf
}

// When set, bit 2 indicates that this frame contains
// an entire header block and is not followed by any
// CONTINUATION frames.
//
// A PUSH_PROMISE frame without the END_HEADERS flag set
// MUST be followed by a CONTINUATION frame for the same stream.
func (f *PushPromiseFrame) HeadersEnded() bool {
	return f.FrameHeader.Flags.Has(FlagContinuationEndHeaders)
}

func parsePushPromise(_ *frameCache, fh FrameHeader, countError func(string), p []byte) (_ Frame, err error) {
	pp := PushPromiseFrame{
		FrameHeader: fh,
	}
	if pp.StreamID == 0 {
		// PUSH_PROMISE frames MUST be associated with an existing,
		// peer-initiated stream. The stream identifier of a
		// PUSH_PROMISE frame indicates the stream it is associated
		// with. If the stream identifier field specifies the value
		// 0x0, a recipient MUST respond with a connection error
		// (Section 5.4.1) of type PROTOCOL_ERROR.
		countError("frame_pushpromise_zero_stream")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	// The PUSH_PROMISE frame includes optional padding.
	// Padding fields and flags are identical to those
	// defined for DATA frames
	var padLength uint8
	if fh.Flags.Has(FlagPushPromisePadded) {
		if p, padLength, err = readByte(p); err != nil {
			countError("frame_pushpromise_pad_short")
			return
		}
	}

	if p, pp.PromiseID, err = readUint32(p); err != nil {
		countError("frame_pushpromise_promiseid_short")
		return
	}
	pp.PromiseID = pp.PromiseID & (1<<31 - 1) // mask off high-order bit

	if int(padLength) > len(p) {
		// like the DATA frame, error out if padding is longer than the body.
		countError("frame_pushpromise_pad_too_big")
		return nil, ConnectionError(ErrCodeProtocol)
	}

	pp.headerFragBuf = p[:len(p)-int(padLength)]

	return &pp, nil
}

// PushPromiseParam are the parameters for writing a PUSH_PROMISE frame.
type PushPromiseParam struct {
	// StreamID is the required Stream ID to initiate (client initiated).
	StreamID uint32

	// PromiseID is the required Stream ID which this
	// Push Promises
	// Server-initiated streams (which at the moment are used only
	// for push streams) are even-numbered.
	PromiseID uint32

	// BlockFragment is part (or all) of a Header Block.
	BlockFragment []byte

	// EndHeaders indicates that this frame contains an entire
	// header block and is not followed by any
	// CONTINUATION frames.
	EndHeaders bool

	// PadLength is the optional number of bytes of zeros to add
	// to this frame.
	PadLength uint8
}

// WritePushPromise writes a single PushPromise Frame.
//
// As with Header Frames, This is the low level call for writing
// individual frames. Continuation frames are handled elsewhere. TODO!!!!! where?
//
// It will perform exactly one Write to the underlying Writer.
// It is the caller's responsibility to not call other Write methods concurrently.
func (f *Framer) WritePushPromise(p PushPromiseParam) error {
	if !validStreamID(p.StreamID) && !f.AllowIllegalWrites {
		// `p.streamID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}

	var flags Flags
	if p.PadLength != 0 {
		flags |= FlagPushPromisePadded
	}
	if p.EndHeaders {
		flags |= FlagPushPromiseEndHeaders
	}
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(FramePushPromise, flags, p.StreamID)
	if p.PadLength != 0 {
		// append value of pad length field to `wbuf`, which is a single octet in size
		f.writeByte(p.PadLength)
	}
	if !validStreamID(p.PromiseID) && !f.AllowIllegalWrites {
		// `p.PromiseID` should NOT be equal to 0
		// or exceed 31 bits
		return errStreamID
	}
	f.writeUint32(p.PromiseID) // append to Framer's `wbuf`
	f.wbuf = append(f.wbuf, p.BlockFragment...)
	f.wbuf = append(f.wbuf, padZeros[:p.PadLength]...) // if pad length is 0, nothing will be appended
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

// WriteRawFrame writes a raw frame. This can be used to write
// extension frames unknown to this package.
func (f *Framer) WriteRawFrame(t FrameType, flags Flags, streamID uint32, payload []byte) error {
	// writes frame header with the first 3 octets of the
	// length field temporarily set to 0, frame type, flags
	// and stream id to Framer's `wbuf`.
	f.startWrite(t, flags, streamID)
	f.writeBytes(payload) // append payload bytes to Framer's `wbuf`
	// writes frame body length into the first 3 octets of
	// the frame header and flushes the binary frame to the
	// underlying writer
	return f.endWrite()
}

func readByte(p []byte) (remain []byte, b byte, err error) {
	if len(p) == 0 {
		return nil, 0, io.ErrUnexpectedEOF
	}

	return p[1:], p[0], nil
}

func readUint32(p []byte) (remain []byte, v uint32, err error) {
	if len(p) < 4 {
		return nil, 0, io.ErrUnexpectedEOF
	}

	return p[4:], binary.BigEndian.Uint32(p[:4]), nil
}

// Currently unused
type streamEnder interface {
	StreamEnded() bool
}

// HEADERS frame:
//
//	When set, bit 2 indicates that this frame contains an entire header block
//	and is not followed by any CONTINUATION frames.
//	A HEADERS frame without the END_HEADERS flag set MUST be followed by a
//	CONTINUATION frame for the same stream.
//
// CONTINUATION frame:
//
//	When set, bit 2 indicates that this frame ends a header block.
//	If the END_HEADERS bit is not set, this frame MUST be followed
//	by another CONTINUATION frame.
//	A receiver MUST treat the receipt of any other type of frame or
//	a frame on a different stream as a connection error of type PROTOCOL_ERROR.
//
// PUSH_PROMISE frame:
//
//	When set, bit 2 indicates that this frame contains
//	an entire header block and is not followed by any
//	CONTINUATION frames.
//	A PUSH_PROMISE frame without the END_HEADERS flag set
//	MUST be followed by a CONTINUATION frame for the same stream.
type headersEnder interface {
	HeadersEnded() bool
}

type headersOrContinuation interface {
	// HEADERS frame:
	//
	//	When set, bit 2 indicates that this frame contains an entire header block
	//	and is not followed by any CONTINUATION frames.
	//	A HEADERS frame without the END_HEADERS flag set MUST be followed by a
	//	CONTINUATION frame for the same stream.
	//
	// CONTINUATION frame:
	//
	//	When set, bit 2 indicates that this frame ends a header block.
	//	If the END_HEADERS bit is not set, this frame MUST be followed
	//	by another CONTINUATION frame.
	//	A receiver MUST treat the receipt of any other type of frame or
	//	a frame on a different stream as a connection error of type PROTOCOL_ERROR.
	//
	// PUSH_PROMISE frame:
	//
	//	When set, bit 2 indicates that this frame contains
	//	an entire header block and is not followed by any
	//	CONTINUATION frames.
	//	A PUSH_PROMISE frame without the END_HEADERS flag set
	//	MUST be followed by a CONTINUATION frame for the same stream.
	headersEnder
	HeaderBlockFragment() []byte // headers content
}

// A MetaHeadersFrame is the representation of one HEADERS frame and
// zero or more contiguous CONTINUATION frames and the decoding of
// their HPACK-encoded contents.
//
// This type of frame does not appear on the wire and is only returned
// by the Framer when Framer.ReadMetaHeaders is set. TODO!!!!! Framer.ReadFrame method?
type MetaHeadersFrame struct {
	*HeadersFrame

	// Fields are the fields contained in the HEADERS and
	// CONTINUATION frames. The underlying slice is owned by the
	// Framer and must not be retained after the next call to
	// ReadFrame.
	//
	// Fields are guaranteed to be in the correct http2 order and
	// not have unknown pseudo header fields or invalid header
	// field names or values. Required pseudo header fields may be
	// missing, however. Use the MetaHeadersFrame.Pseudo accessor
	// method to access pseudo headers. TODO!!!!! add more details
	Fields []hpack.HeaderField

	// Truncated is whether the max header list size limit was hit
	// and Fields is incomplete. The hpack decoder state is still
	// valid, however. TODO!!!!! add more details
	Truncated bool
}

// PseudoValue returns the given pseudo header field's value.
// The provided pseudo field should not contain the leading colon.
func (mh *MetaHeadersFrame) PseudoValue(pseudo string) string {
	for _, hf := range mh.Fields {
		if !hf.IsPseudo() {
			// iterated through all of the pseudo headers
			// without finding the requested one
			return ""
		}
		if hf.Name[1:] == pseudo {
			return hf.Value
		}
	}
	return ""
}

// RegularFields returns the regular (non-pseudo) header fields of mh.
// The caller does not own the returned slice.
func (mh *MetaHeadersFrame) RegularFields() []hpack.HeaderField {
	for i, hf := range mh.Fields {
		if !hf.IsPseudo() {
			return mh.Fields[i:]
		}
	}
	return nil
}

// PseudoFields returns the pseudo header fields of mh.
// The caller does not own the returned slice.
func (mh *MetaHeadersFrame) PseudoFields() []hpack.HeaderField {
	for i, hf := range mh.Fields {
		if !hf.IsPseudo() {
			return mh.Fields[:i]
		}
	}
	return mh.Fields
}

func (mh *MetaHeadersFrame) checkPseudos() error {
	var isRequest, isResponse bool
	pf := mh.PseudoFields()
	for i, hf := range pf {
		switch hf.Name {
		case ":method", ":path", ":scheme", ":authority":
			isRequest = true
		case ":status":
			isResponse = true
		default:
			return pseudoHeaderError(hf.Name)
		}
		// Check for duplicates.
		// This would be a bad algorithm, but N is 4.
		// And this doesn't allocate.
		for _, hf2 := range pf[:i] {
			if hf.Name == hf2.Name {
				return duplicatePseudoHeaderError(hf.Name)
			}
		}
	}
	if isRequest && isResponse {
		return errMixPseudoHeaderTypes
	}
	return nil
}

func (fr *Framer) maxHeaderStringLength() int {
	v := fr.maxHeaderListSize()
	if uint32(int(v)) == v {
		// looks like this is always executed on x86-64 bit
		return int(v)
	}
	// They had a crazy big number for MaxHeaderBytes anyway,
	// so give them unlimited header lengths (looks like this never happens on x86-64 bit):
	return 0
}

// readMetaFrame returns 0 or more CONTINUATION frames from fr and
// merge them into the provided hf and returns a MetaHeadersFrame
// with the decoded hpack values. TODO!!!!! add more details
func (fr *Framer) readMetaFrame(hf *HeadersFrame) (*MetaHeadersFrame, error) {
	if fr.AllowIllegalReads {
		return nil, errors.New("illegal use of AllowIllegalReads with ReadMetaHeaders")
	}
	mh := &MetaHeadersFrame{
		HeadersFrame: hf,
	} // save already parsed HEADERS frame

	// SETTINGS_MAX_HEADER_LIST_SIZE: This advisory setting informs a
	// peer of the maximum size of header list that the sender is
	// prepared to accept, in octets. The value is based on the
	// uncompressed size of header fields, including the length of the
	// name and value in octets plus an overhead of 32 octets for each
	// header field.
	//
	// If MaxHeaderListSize is 0, it means a sane default (currently 16MB)
	// will be returned.
	// If the limit is hit, MetaHeadersFrame.Truncated is set true.
	var remainSize = fr.maxHeaderListSize()
	var sawRegular bool // pseudo headers should come before regular headers

	var invalid error          // pseudo header field errors
	hdec := fr.ReadMetaHeaders // hpack decoder

	// SetEmitEnabled controls whether the emitFunc provided to NewDecoder
	// should be called. The default is true.
	//
	// If true, the emitFunc will be called on every parsed hpack.HeaderField,
	// which will be passed in as the only argument.
	//
	// TODO!!!!! This facility exists to let servers enforce MAX_HEADER_LIST_SIZE
	// while still decoding and keeping in-sync with decoder state, but
	// without doing unnecessary decompression or generating unnecessary
	// garbage for header fields past the limit.
	hdec.SetEmitEnabled(true)
	// sets the maximum size of a HeaderField name or
	// value string. If a string exceeds this length (before or
	// after any decompression), upcoming Write will return ErrStringLength.
	//
	// On x86-64 bit is always equal to SETTINGS_MAX_HEADER_LIST_SIZE with
	// a sane default of 16MB.
	hdec.SetMaxStringLength(fr.maxHeaderStringLength())
	hdec.SetEmitFunc(func(hf hpack.HeaderField) {
		if VerboseLogs && fr.logReads { // set via the GODEBUG environment variable, when it contains http2debug=2 (or directly)
			// log hpack decoded header field
			fr.debugReadLoggerf("http2: decoded hpack field %+v", hf) // log.Printf by default
		}
		if !httpguts.ValidHeaderFieldValue(hf.Value) {
			// if value contains control characters

			// Don't include the value in the error, because it may be sensitive.
			invalid = headerFieldValueError(hf.Name)
		}
		isPseudo := strings.HasPrefix(hf.Name, ":")
		if isPseudo {
			if sawRegular {
				invalid = errPseudoAfterRegular // pseudo headers cannot come after regular headers
			}
		} else {
			sawRegular = true
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
			if !validWireHeaderFieldName(hf.Name) {
				invalid = headerFieldNameError(hf.Name)
			}
		}

		if invalid != nil {
			// do not call this function
			// on following decode headers
			// of this header block
			hdec.SetEmitEnabled(false)
			return
		}

		// https://httpwg.org/specs/rfc7541.html#rfc.section.4.1
		// "The size of the dynamic table is the sum of the size of
		// its entries. The size of an entry is the sum of its name's
		// length in octets (as defined in Section 5.2), its value's
		// length in octets (see Section 5.2), plus 32.  The size of
		// an entry is calculated using the length of the name and
		// value without any Huffman encoding applied."
		size := hf.Size()
		// remainSize initially defaults to the value of the
		// SETTINGS_MAX_HEADER_LIST_SIZE setting, which defaults
		// to 16MB if not specified otherwise.
		if size > remainSize {
			hdec.SetEmitEnabled(false) // disable calls to this functions on remaining headers
			mh.Truncated = true        // whether the max header list size limit was hit and Fields is incomplete
			return
		}
		remainSize -= size

		mh.Fields = append(mh.Fields, hf)
	})
	// Lose reference to MetaHeadersFrame:
	defer hdec.SetEmitFunc(func(f hpack.HeaderField) {})

	var hc headersOrContinuation = hf // first header field in a possible chain of upcoming CONTINUATION frames
	for {
		frag := hc.HeaderBlockFragment() // get frame content
		// On first call:
		//     Optimistically assumes that this call writes
		//     a complete header block, so it just assigns
		//     frag to decoder's "work in progress" buffer,
		//     which doesn't survive upcoming calls to Write.
		//     So if the header block consists only of a
		//     single HEADERS frame, we avoid copying frag
		//     into decoder's underlying buffer, which accomodates
		//     content of contiguous header fragments.
		//
		//     If the assumption fails, copies the header block
		//     into decoder's underlying buffer.
		//
		//     On each decoded header field calls the above emit
		//     function with the decoded header field (either
		//     deduced from static and dynamic HPACK tables,
		//     huffman decoded or just plain text).
		//
		//     Updates HPACK dynamic table state with the decoded
		//     header field if neccessary.
		// Successive calls (CONTINUATION frames):
		//     Copies header block fragments into decoder's underlying
		//     buffer contatenating it with possibly unparsed data
		//     from previous calls to Write.
		//
		//     On each decoded header field calls the above emit
		//     function with the decoded header field (either
		//     deduced from static and dynamic HPACK tables,
		//     huffman decoded or just plain text).
		//
		//     Updates HPACK dynamic table state with the decoded
		//     header field if neccessary.
		//
		// On every HPACK decoded header field the registered previosuly
		// emit func is called. It validates:
		//     - That the header field value contains no control characters.
		//
		//     - That pseudo header fields do not come after regular fields.
		//
		//     - That header field names are a valid HTTP/1.x header names.
		//       Also HTTP/2 imposes the additional restriction that uppercase
		//       ASCII letters are not allowed in header field names.
		//       ABNF diagram for valid HTTP/1.x header names:
		//		     header-field   = field-name ":" OWS field-value OWS
		//	         OWS            = optional whitespace byte (SP | HTAB)
		//		     field-name     = token
		//		     token          = 1*tchar
		//		     tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
		//		             "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
		//
		//    - That the complete headers block doesn't exceed SETTINGS_MAX_HEADER_LIST_SIZE setting,
		//      which defaults to 16MB if not specified otherwise.
		//
		// If the above validations fail, the emit func is disabled and isn't called for upcoming
		// header field from the current header block. The upcoming header fields are still read and
		// HPACK decoded to preserve consistent state of the dynamic HPACK header compression fields
		// betweem peers.
		if _, err := hdec.Write(frag); err != nil {
			return nil, ConnectionError(ErrCodeCompression)
		}

		if hc.HeadersEnded() {
			// HEADERS frame has no following CONTINUATION frames
			// or it's the last frame in the chain of CONTINUATION frames
			break
		}
		if f, err := fr.ReadFrame(); err != nil { // read next CONTINUATION frame
			return nil, err
		} else {
			hc = f.(*ContinuationFrame) // guaranteed by checkFrameOrder
		}
	}

	mh.HeadersFrame.headerFragBuf = nil // first header frame with possible continuation frames coming after it
	mh.HeadersFrame.invalidate()        // invalidates frame

	if err := hdec.Close(); err != nil { // resets decoders `firstField` attribute to true (for next header field)
		// if decoder's underlying buffer has content left (resets the buffer)
		return nil, ConnectionError(ErrCodeCompression)
	}
	if invalid != nil {
		fr.errDetail = invalid
		if VerboseLogs {
			log.Printf("http2: invalid header: %v", invalid)
		}
		return nil, StreamError{mh.StreamID, ErrCodeProtocol, invalid}
	}

	if err := mh.checkPseudos(); err != nil { // valide pseudo header fields
		fr.errDetail = err
		if VerboseLogs {
			log.Printf("http2: invalid pseudo headers: %v", err)
		}
		return nil, StreamError{mh.StreamID, ErrCodeProtocol, err}
	}
	return mh, nil
}

// summarizeFrame converts information
// carried by a frame into a human-readable
// form for verbose/debug logging. Example:
//
//	"SETTINGS flags=flagName1|flagName2 stream=1 len=1234, settings: settingName1=settingValue1, settingName2=settingValue2"
func summarizeFrame(f Frame) string {
	var buf bytes.Buffer // buffer for human-readable frame information representation
	// writeDebug converts frame header's type, flags
	// stream id and length fields into a human-readable
	// text form:
	//
	//	"typeName flags=flagName1|flagName2 stream=1 len=1234"
	f.Header().writeDebug(&buf)
	switch f := f.(type) {
	case *SettingsFrame:
		// "SETTINGS flags=flagName1|flagName2 stream=1 len=1234, settings: settingName1=settingValue1, settingName2=settingValue2"
		n := 0
		f.ForEachSetting(func(s Setting) error {
			n++
			if n == 1 {
				buf.WriteString(", settings:")
			}
			fmt.Fprintf(&buf, " %v=%v,", s.ID, s.Val)
			return nil
		})
		if n > 0 {
			buf.Truncate(buf.Len() - 1) // remove trailing comma
		}
	case *DataFrame:
		// "DATA flags=flagName1|flagName2 stream=1 len=1234 data=dataOctets (234 bytes ommited)"

		// Data returns the frame's data octets, not including any padding
		// size byte or padding suffix bytes.
		// The caller must not retain the returned memory past the next
		// call to ReadFrame.
		data := f.Data()
		const max = 256 // at max 256 octets
		if len(data) > max {
			data = data[:256]
		}
		fmt.Fprintf(&buf, " data=%q", data) // write raw data octets, at max 256 octets
		if len(f.Data()) > max {
			fmt.Fprintf(&buf, " (%d bytes ommited)", len(f.Data())-max)
		}
	case *WindownUpdateFrame:
		// if window update on connection:
		//   "WINDOW_UPDATE flags=flagName1|flagName2 stream=1 len=1234 (conn) incr=23456"
		if f.StreamID == 0 {
			buf.WriteString(" (conn)")
		}
		fmt.Fprintf(&buf, " incr=%v", f.Increment)
	case *PingFrame:
		// "PING flags=flagName1|flagName2 stream=1 len=1234 ping=pingDataOctets"
		fmt.Fprintf(&buf, " ping=%q", f.Data[:])
	case *GoAwayFrame:
		// "GOAWAY flags=flagName1|flagName2 stream=1 len=1234 LastStreamID=11 ErrCode=errCodeName Debug=debugDataOctets"
		fmt.Fprintf(&buf, " LastStreamID=%v ErrCode=%v Debug=%q", f.LastStreamID, f.ErrCode, f.debugData)
	case *RSTStreamFrame:
		// "RST_STREAM flags=flagName1|flagName2 stream=1 len=1234 ErrCode=errCodeName"
		fmt.Fprintf(&buf, " ErrCode=%v", f.ErrCode)
	}
	return buf.String()
}
