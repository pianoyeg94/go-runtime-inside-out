package textproto

import (
	"bufio"
	"bytes"
	"fmt"
	"strings"
	"sync"
)

// A Reader implements convenience methods for reading requests
// or responses from a text protocol network connection.
type Reader struct {
	R   *bufio.Reader
	dot *dotReader
	buf []byte // a re-usable buffer for readContinuedLineSlice
}

// NewReader returns a new Reader reading from r.
//
// To avoid denial of service attacks, the provided bufio.Reader
// should be reading from an io.LimitReader or similar Reader to bound
// the size of responses.
func NewReader(r *bufio.Reader) *Reader {
	return &Reader{R: r}
}

// readLineSlice may return a line without copying
// it from the underlying buffer if on the first attempt
// a full line could be read into the underlying buffer.
// Else a copy of the bytes will be returned.
func (r *Reader) readLineSlice() ([]byte, error) {
	r.closeDot() // no-op in case of ReadMIMEHeader
	var line []byte
	for {
		l, more, err := r.R.ReadLine() // points to line in the underlying buffer, no copy
		if err != nil {                // error comes only from the the buffer's underlying reader
			return nil, err
		}

		// Avoid the copy if the first call produced a full line.
		if line == nil && !more {
			return l, nil
		}

		// We have no choice but copy the data from the underlying buffer
		line = append(line, l...)
		if !more {
			break
		}
	}

	return line, nil
}

// trim returns s with leading and trailing spaces and tabs removed.
// It does not assume Unicode or UTF-8.
func trim(s []byte) []byte {
	var i int
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}

	n := len(s)
	for n > i && (s[n-1] == ' ' || s[n-1] == '\t') {
		n--
	}

	return s[i:n]
}

// readContinuedLineSlice reads continued lines from the reader buffer,
// returning a byte slice with all lines. The validateFirstLine function
// is run on the first read line, and if it returns an error then this
// error is returned from readContinuedLineSlice.
func (r *Reader) readContinuedLineSlice(validateFirstLine func([]byte) error) ([]byte, error) {
	if validateFirstLine == nil {
		return nil, fmt.Errorf("missing validateFirstLine func")
	}

	// Read the first line.
	// this may avoid copying the line from the underlying buffer,
	// if a line was read into the buffer on the first attempt, or was already
	// there. Since the buffer is usually at least 4096 bytes, and header lines
	// are usually less than that, a copy is usually avoided.
	line, err := r.readLineSlice() // err can come only from buffer's underlying reader (network, for example)
	if err != nil {
		return nil, err
	}

	if len(line) == 0 { // blank line - no continuation
		return line, nil
	}

	// in case of r.ReadMIMEHeader(), validates that
	// there's a colon present in the line, which
	// splits key and value
	if err = validateFirstLine(line); err != nil {
		return nil, err
	}

	// Optimistically assume that we have started to buffer the next line
	// and it starts with an ASCII letter (the next header key), or a blank
	// line, so we can avoid copying that buffered data around in memory
	// and skipping over non-existent whitespace.
	if r.R.Buffered() > 1 {
		peek, _ := r.R.Peek(2)                                               // doesn't force read from underlying reader because the buffered amount check
		if (len(peek) > 0 && (isASCIILetter(peek[0]) || peek[0] == '\n')) || // next header or end of header section
			(len(peek) == 2 && peek[0] == '\r' && peek[1] == '\n') { // end of header section
		}
	}

	// We're dealing with a multi-line header value.
	// HTTP/1.1 header field values can be folded onto multiple lines if the continuation line begins
	// with a space or horizontal tab. All linear white space, including folding, has the same semantics as SP.
	// A recipient MAY replace any linear white space with a single SP before interpreting
	// the field value or forwarding the message downstream.

	// ReadByte or the next readLineSlice will flush the read buffer;
	// copy the slice into buf.
	//
	// trim - The field-content does not include any leading or trailing LWS:
	// linear white space occurring before the first non-whitespace character
	// of the field-value or after the last non-whitespace character of the field-value.
	// Such leading or trailing LWS MAY be removed without changing the semantics of the field value.
	r.buf = append(r.buf[:0], trim(line)...)

	// Read continuation lines.
	for r.skipSpace() > 0 { // read until conitnuation line pattern ends (continuation begins on newline with ' ' or '\t')
		// this may avoid copying the line from the underlying buffer,
		// if a line was read into the buffer on the first attempt, or was already
		// there. Since the buffer is usually at least 4096 bytes, and header continuation lines
		// are usually less than that, a copy is usually avoided.
		line, err := r.readLineSlice() // may overwrite previous line in buffer, err can come only from buffer's underlying reader (network, for example)
		if err != nil {                // just return the incomplete header without error
			break
		}

		r.buf = append(r.buf, ' ') // A recipient MAY replace any linear white space with a single SP before interpreting the field value or forwarding the message downstream.
		r.buf = append(r.buf, trim(line)...)
	}

	return r.buf, nil
}

// skipSpace skips R over all spaces and returns the number of bytes skipped.
// HTTP/1.1 header field values can be folded onto multiple lines if the continuation line begins
// with a space or horizontal tab.
func (r *Reader) skipSpace() int {
	var n int
	for {
		c, err := r.R.ReadByte() // may read from network
		if err != nil {
			// Bufio will keep err until next read.
			break
		}

		if c != ' ' && c != '\t' {
			r.R.UnreadByte()
			break
		}
		n++
	}

	return n
}

type dotReader struct {
	r     *Reader
	state int
}

// closeDot drains the current DotReader if any,
// making sure that it reads until the ending dot line.
//
// if no dot reader required and thus not set in reader,
// this is a no-op
func (r *Reader) closeDot() {
	if r.dot == nil {
		return
	}
}

var colon = []byte(":")

// ReadMIMEHeader reads a MIME-style header from r.
// The header is a sequence of possibly continued Key: Value lines
// ending in a blank line.
// The returned map m maps CanonicalMIMEHeaderKey(key) to a
// sequence of values in the same order encountered in the input.
//
// For example, consider this input:
//
//	My-Key: Value 1
//	Long-Key: Even
//	       Longer Value
//	My-Key: Value 2
//
// Given that input, ReadMIMEHeader returns the map:
//
//	map[string][]string{
//		"My-Key": {"Value 1", "Value 2"},
//		"Long-Key": {"Even Longer Value"},
//	}
func (r *Reader) ReadMIMEHeader() (MIMEHeader, error) {
	// Avoid lots of small slice allocations later by allocating one
	// large one ahead of time which we'll cut up into smaller
	// slices. If this isn't big enough later, we allocate small ones.
	// Allocates strings ahead of time for header field values.
	var strs []string
	// forces a buffer load if empty
	// peeks content of buffer without copying and counts newlines
	hint := r.upcomingHeaderNewlines()
	if hint > 0 {
		strs = make([]string, hint)
	}

	m := make(MIMEHeader, hint)

	// http: the first header line cannot start with a leading space.
	//       if it does, return an empty MIMEHeader map and a Protocol error
	//       containing the malformed line
	// peek returns the reference to the underlting buffer content
	if buf, err := r.R.Peek(1); err == nil && (buf[0] == ' ' || buf[0] == '\t') {
		// this may avoid copying the line from the underlying buffer,
		// if a line was read into the buffer on the first attempt, or was already
		// there. Since the buffer is usually at least 4096 bytes, and header lines
		// are usually less than that, a copy is usually avoided.
		line, err := r.readLineSlice() // err can come only from buffer's underlying reader (network, for example)
		if err != nil {
			return m, err
		}
		return m, ProtocolError("malformed MIME header initial line: " + string(line))
	}

	// read in all the headers, validate, canonicalize them and
	// put them into the MIMEHeader map
	for {
		// May avoid copying the line from the underlying buffer,
		// if it was read into the buffer on the first attempt, or was already
		// there. Since the buffer is 4096 bytes, and header lines
		// are usually less than that, a copy of the line bytes
		// is usually avoided.
		//
		// The no-copy scenario can only happen if the header value isn't a
		// multi-line value. If it is, the lines are read and copied into r.buf
		// until the current header value is exhausted.
		// New lines and WS before each line-fold are replaced with a single SP.
		// So, basically the multi-line header value is transformed into a
		// single line with values separated by spaces.
		//
		// Err can come only from buffer's underlying reader
		// (network, for example).
		kv, err := r.readContinuedLineSlice(mustHaveFieldNameColon)
		if len(kv) == 0 { // end of header section
			return m, err
		}

		k, v, ok := bytes.Cut(kv, colon) // separate key from value
		if !ok {
			return m, ProtocolError("malformed MIME header line: " + string(kv))
		}

		// Canonicalization converts the first letter and any letter following a hyphen to upper case;
		// the rest are converted to lowercase.
		//
		// Allowed character in a header field name are:
		//   - "a" to "z"
		//   - "A" to "Z"
		//   - "0" to "9"
		//   - "!", "#", "$", "%", "&", "'", "*", "+", "-", ".", "^", "_", "`", "|", "~"
		//
		// 1. If there's an invalid header character in the key, then the key is not canonicalized
		//    and is returned as is converted to a string, `ok` will be false. This will copy bytes to the string.
		//
		// 2. If there's a space in the key or before the colon separating the key from the value, then the key
		//    is not canonicalized and is returned as is converted to a string, but `ok` will be true,
		//    to disallow request smuggling by normalizing invalid headers. This will copy bytes to the string.
		//
		// 3. Canonicalizes the header key and if the key is in the commonHeader map, avoids allocation by
		//    interning the string from the commonHeader map.
		key, ok := canonicalMIMEHeaderKey(k)
		if !ok {
			return m, ProtocolError("malformed MIME header line: " + string(kv))
		}

		// validate characters in header value
		// RFC 7230 says:
		//
		//		field-content  = field-vchar [ 1*( SP / HTAB ) field-vchar ] = spaces or tabs before header value is allowed
		//                                                                     (just after the colon, separating the field name and value)
		//		field-vchar    = VCHAR / obs-text
		//		obs-text       = %x80-FF = A recipient SHOULD treat other octets in field content (obs-text) as opaque data.
		//	                              (Extended ASCII table from https://www.rapidtables.com/code/text/ascii-table.html)
		//
		// RFC 5234 says:
		//
		//	HTAB           =  %x09 = from ASCII table
		//	SP             =  %x20 = from ASCII table
		//	VCHAR          =  %x21-7E = ASCII characters 33 to 126
		for _, c := range v {
			if !validHeaderValueByte(c) {
				return m, ProtocolError("malformed MIME header line: " + string(kv))
			}
		}

		// As per RFC 7230 field-name is a token, tokens consist of one or more chars.
		// We could return a ProtocolError here, but better to be liberal in what we
		// accept, so if we get an empty key, skip it.
		if key == "" {
			continue
		}

		// Skip initial spaces in value.
		value := strings.TrimLeft(string(v), " \t")

		vv := m[key]
		// if no header key with the same name was previously encountered
		// (non-multivalued header) and there's still place in the ahead
		// of time allocated strings buffer
		if vv == nil && len(strs) > 0 {
			// More than likely this will be a single-element key.
			// Most headers aren't multi-valued.
			// Set the capacity on strs[0] to 1, so any future append
			// won't extend the slice into the other strings.
			//
			// vv = strs[:1:1] => get slice with length 1 and capacity of 1 from the preallocated buffer
			// strs = strs[1:] => shrink buffer by 1 string
			vv, strs = strs[:1:1], strs[1:]
			vv[0] = value
			m[key] = vv
		} else { // repeated header key name (multivalued header), or preallocated string buffer capacity is exhausted
			m[key] = append(vv, value)
		}

		if err != nil { // if there's was a network error from the underlying buffer's io.Reader, but the header line was still read in
			return m, err
		}
	}

}

func mustHaveFieldNameColon(line []byte) error {
	if bytes.IndexByte(line, ':') < 0 {
		return ProtocolError(fmt.Sprintf("malformed MIME header: missing colon: %q", line))
	}
	return nil
}

var nl = []byte("\n")

// upcomingHeaderNewlines returns an approximation of the number of newlines
// that will be in this header. If it gets confused, it returns 0.
func (r *Reader) upcomingHeaderNewlines() (n int) {
	// Try to determine the 'hint' size.
	r.R.Peek(1) // force a buffer load if empty
	s := r.R.Buffered()
	if s == 0 {
		return 0
	}

	peek, _ := r.R.Peek(s) // get content slice without copying it from the underlying buffer
	return bytes.Count(peek, nl)
}

// CanonicalMIMEHeaderKey returns the canonical format of the
// MIME header key s. The canonicalization converts the first
// letter and any letter following a hyphen to upper case;
// the rest are converted to lowercase. For example, the
// canonical key for "accept-encoding" is "Accept-Encoding".
// MIME header keys are assumed to be ASCII only.
// If s contains a space or invalid header field bytes, it is
// returned without modifications.
//
// If the header key is already canonical
// avoids an uneccessary allocation of a byte slice
// with copy of the string's contents.
func CanonicalMIMEHeaderKey(s string) string {
	// Quick check for canonical encoding.
	upper := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !validHeaderFieldByte(c) {
			return s
		}

		if upper && 'a' <= c && c <= 'z' {
			// string needs to be modified, can't avoid allocation
			s, _ = canonicalMIMEHeaderKey([]byte(s))
			return s
		}

		if !upper && 'A' <= c && c <= 'Z' {
			// string needs to be modified, can't avoid allocation
			s, _ = canonicalMIMEHeaderKey([]byte(s))
			return s
		}
		upper = c == '-'
	}
	return s
}

// toLower is used to convert lower case characters
// to upper case and vice versa by adding or subtracting
// this value from the given character
const toLower = 'a' - 'A'

// validHeaderFieldByte reports whether c is a valid byte in a header
// field name. RFC 7230 says:
//
//	header-field   = field-name ":" OWS field-value OWS
//	field-name     = token
//	tchar = "!" / "#" / "$" / "%" / "&" / "'" / "*" / "+" / "-" / "." /
//	        "^" / "_" / "`" / "|" / "~" / DIGIT / ALPHA
//	token = 1*tchar
//
// So the allowed character in a header field name are:
//   - "a" to "z"
//   - "A" to "Z"
//   - "0" to "9"
//   - "!", "#", "$", "%", "&", "'", "*", "+", "-", ".", "^", "_", "`", "|", "~"
func validHeaderFieldByte(c byte) bool {
	// mask is a 128-bit bitmap with 1s for allowed bytes,
	// so that the byte c can be tested with a shift and an and.
	// If c >= 128, then 1<<c and 1<<(c-64) will both be zero,
	// and this function will return false.
	const mask = 0 |
		// set bits number 50 to 58, which correspond to ascii codes 49-57 ("0"-"9")
		// (1<<(10)-1) sets ten bits for numbers 0-9 and then shifts them to the positions 50-58
		(1<<(10)-1)<<'0' |
		// set bits number 98 to 123, which correspond to ascii codes 97-122 ("a"-"z")
		// (1<<(10)-1) sets 26 bits for characters a-z and then shifts them to the positions 98-123
		(1<<(26)-1)<<'a' |
		// set bits number 66 to 91, which correspond to ascii codes 65-90 ("A"-"Z")
		// (1<<(10)-1) sets 26 bits for characters A-Z and then shifts them to the positions 66-91
		(1<<(26)-1)<<'A' |
		// sets bits corresponding to the ascii codes by which 1 is shifted
		1<<'!' |
		1<<'#' |
		1<<'$' |
		1<<'%' |
		1<<'&' |
		1<<'\'' |
		1<<'*' |
		1<<'+' |
		1<<'-' |
		1<<'.' |
		1<<'^' |
		1<<'_' |
		1<<'`' |
		1<<'|' |
		1<<'~'

	// ((uint64(1)<<c)&(mask&(1<<64-1)):
	//     - tests if character is in beween the 0th and 63th ascii code and if it is one of the allowed
	//       characters out of those codes
	//
	//     - (mask&(1<<64-1)): masks of the lower 64 bits of the 128-bit bitmask
	//
	//     - ((uint64(1)<<c): if 'c' is a character code between 0 and 63, then the result will be non-zero
	//
	//     - ands (&) the two parts, because even if 'c' is in between 0-63, it may be not a part of
	//       the allowed character set
	//
	// (uint64(1)<<(c-64))&(mask>>64)):
	// 	   - tests if character is in beween the 64th and 127th ascii code and if it is one of the allowed
	//       characters out of those codes
	//
	//     - (mask>>64): gets hand of the 64 high-order bits of the 128-bit bitmask to test for ascii codes 64-127
	//
	//     - (uint64(1)<<(c-64)): (c-64) obtains the bit number so it could fit into uint64
	//
	//     - ands (&) the two parts, because even if 'c' is in between 64-127, it may be not a part of
	//       the allowed character set
	return ((uint64(1)<<c)&(mask&(1<<64-1)) |
		(uint64(1)<<(c-64))&(mask>>64)) != 0
}

// validHeaderValueByte reports whether c is a valid byte in a header
// field value. RFC 7230 says:
//
//		field-content  = field-vchar [ 1*( SP / HTAB ) field-vchar ] = one space or tab before header value is allowed
//		field-vchar    = VCHAR / obs-text
//		obs-text       = %x80-FF = A recipient SHOULD treat other octets in field content (obs-text) as opaque data.
//	                              (Extended ASCII table from https://www.rapidtables.com/code/text/ascii-table.html)
//
// RFC 5234 says:
//
//	HTAB           =  %x09 = from ASCII table
//	SP             =  %x20 = from ASCII table
//	VCHAR          =  %x21-7E = ASCII characters 33 to 126
func validHeaderValueByte(c byte) bool {
	// mask is a 128-bit bitmap with 1s for allowed bytes,
	// so that the byte c can be tested with a shift and an and.
	// If c >= 128, then 1<<c and 1<<(c-64) will both be zero.
	// Since this is the obs-text range, we invert the mask to
	// create a bitmap with 1s for disallowed bytes.                                                ' ' (bit 32, space)     '\t' (bit 9, tab)
	//                                                                                               |                       |
	//                                                                                               V                       V
	// 1111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111111100000000000000000000001000000000
	// |                 bits 33 to 126 representing valid ASCII characters                         |
	// +--------------------------------------------------------------------------------------------+
	//
	//
	const mask = 0 |
		(1<<(0x7f-0x21)-1)<<0x21 | // VCHAR: %x21-7E; 1<<(0x7f-0x21)-1 = 96-bits of 1s = 128-bit bitmap with bits 33 to 126 set to 1, first 0-31 bits are set to zero
		1<<0x20 | //                  SP: %x20; sets bit 32
		1<<0x09 //                    HTAB: %x09; sets bit 9

	// ((uint64(1)<<c)&^(mask&(1<<64-1)):
	// ==================================
	//     - tests if character is in beween the 0th and 63th ascii code and if it is one of the allowed
	//       characters out of those codes, also tests for the 128th to 191th obsolete characters
	//
	//     - (uint64(1)<<c): if 'c' is a character code between 0 and 63, then the result will be non-zero,
	//                       if 'c' is a character code between 128th an 255th obsolete characters, the the result will be a full 64-bit run of zeroes
	//
	//     - (mask&(1<<64-1)): masks of the lower 64 bits of the 128-bit bitmask : 1111111111111111111111111111111100000000000000000000001000000000
	//
	//     - ^(mask&(1<<64-1)): invert the mask to create a bitmap with 1s for   : 0000000000000000000000000000000011111111111111111111110111111111
	//                          disallowed bytes, this way the result will
	//                          always be non-zero TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	//
	//     - ands (&) the two parts, because even if 'c' is in between 0-63, it may be not a part of
	//       the allowed character set TODO!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!
	//
	//
	// (uint64(1)<<(c-64))&(mask>>64)):
	// ================================
	//     - (mask>>64): mask of the higher 64 bits of the 128-bit bitmask       : 1111111111111111111111111111111111111111111111111111111111111111
	//
	//      - ^(mask>>64)                                                        : 0000000000000000000000000000000000000000000000000000000000000000
	//
	//     - (uint64(1)<<(c-64)): if 'c' is a character code between 64 and 127, then the result will be non-zero,
	//                            if 'c' is a character code between 128th an 255th obsolete characters, the the result will be a full 64-bit run of zeroes
	//
	//     -
	//
	//     - ands (&) the two parts, because even if 'c' is in between 0-63, it may be not a part of
	//       the allowed character set
	//
	// ((uint64(1)<<c)&^(mask&(1<<64-1)) | (uint64(1)<<(c-64))&^(mask>>64)):
	// ====================================================================
	//     - if at least one bit is set, the  valid bit TODO!!!!!!!!!!!!!!!!!!
	//
	//
	// Examples:
	// ============
	// 		Example 1 (' ', space, 32nd char in the ASCII table):
	//      ------------------------
	//      	1) ^(mask&(1<<64-1))                                                 = 0000000000000000000000000000000011111111111111111111110111111111
	//          2) (uint64(1)<<c) = (uint64(1)<<32)                                  = 0000000000000000000000000000000100000000000000000000000000000000
	//          3) (uint64(1)<<c)&^(mask&(1<<64-1))                                  = 0000000000000000000000000000000000000000000000000000000000000000
	//
	//          4) (mask>>64)                                                        = 1111111111111111111111111111111111111111111111111111111111111111
	//          5) ^(mask>>64)                                                       = 0000000000000000000000000000000000000000000000000000000000000000
	//          6) ((uint64(1)<<(c-64)) = ((uint64(1)<<(' '-64)) - will always be 0  = 0000000000000000000000000000000000000000000000000000000000000000
	//
	//          7) ((uint64(1)<<c)&^(mask&(1<<64-1)) | (uint64(1)<<(c-64))&^(mask>>64)) == 0
	return ((uint64(1)<<c)&^(mask&(1<<64-1)) |
		(uint64(1)<<(c-64))&^(mask>>64)) == 0

}

// canonicalMIMEHeaderKey is like CanonicalMIMEHeaderKey but is
// allowed to mutate the provided byte slice before returning the
// string.
//
// For invalid inputs (if a contains spaces or non-token bytes), a
// is unchanged and a string copy is returned.
func canonicalMIMEHeaderKey(a []byte) (_ string, ok bool) {
	noCanon := false
	// See if a looks like a header key. If not, return it unchanged.
	for _, c := range a {
		if validHeaderFieldByte(c) {
			continue
		}

		// Don't canonicalize.
		if c == ' ' {
			// We accept invalid headers with a space before the
			// colon, but must not canonicalize them.
			// See https://go.dev/issue/34540.
			noCanon = true
			continue
		}

		return string(a), false
	}

	if noCanon {
		return string(a), true
	}

	upper := true
	for i, c := range a {
		// Canonicalize: first letter upper case
		// and upper case after each dash.
		// (Host, User-Agent, If-Modified-Since).
		// MIME headers are ASCII only, so no Unicode issues.
		if upper && 'a' <= c && c <= 'z' {
			c -= toLower
		} else if !upper && 'A' <= c && c <= 'Z' {
			c += toLower
		}
		a[i] = c
		upper = c == '-' // for next time
	}
	commonHeaderOnce.Do(initCommonHeader)
	// The compiler recognizes m[string(byteSlice)] as a special
	// case, so a copy of a's bytes into a new string does not
	// happen in this map lookup:
	if v := commonHeader[string(a)]; v != "" {
		return v, true
	}

	return string(a), true
}

// commonHeader interns common header strings.
// Used to avoid allocating a backing array for header key string,
// when converting it from bytes.
var commonHeader map[string]string

var commonHeaderOnce sync.Once

func initCommonHeader() {
	commonHeader = make(map[string]string)
	for _, v := range []string{
		"Accept",
		"Accept-Charset",
		"Accept-Encoding",
		"Accept-Language",
		"Accept-Ranges",
		"Cache-Control",
		"Cc",
		"Connection",
		"Content-Id",
		"Content-Language",
		"Content-Length",
		"Content-Transfer-Encoding",
		"Content-Type",
		"Cookie",
		"Date",
		"Dkim-Signature",
		"Etag",
		"Expires",
		"From",
		"Host",
		"If-Modified-Since",
		"If-None-Match",
		"In-Reply-To",
		"Last-Modified",
		"Location",
		"Message-Id",
		"Mime-Version",
		"Pragma",
		"Received",
		"Return-Path",
		"Server",
		"Set-Cookie",
		"Subject",
		"To",
		"User-Agent",
		"Via",
		"X-Forwarded-For",
		"X-Imforwards",
		"X-Powered-By",
	} {
		commonHeader[v] = v
	}
}
