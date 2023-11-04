// Package quotedprintable implements quoted-printable encoding as specified by
// RFC 2045.
package quotedprintable

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
)

// The Quoted-Printable encoding is intended to represent data that largely consists of octets
// that correspond to printable characters in the ASCII character set. It encodes the data in such a way
// that the resulting octets are unlikely to be modified by mail transport. If the data being encoded
// are mostly ASCII text, the encoded form of the data remains largely recognizable by humans.
// A body which is entirely ASCII may also be encoded in Quoted-Printable to ensure the integrity
// of the data should the message pass through a character-translating, and/or line-wrapping gateway.
//
//
// In this encoding, octets are to be represented as determined by the following rules:
//     1. Rule #1: (General 8-bit representation):
//        	Any octet, except those indicating a line break according to the newline convention
//        	of the canonical form of the data being encoded, may be represented by an "=" followed by
//          a two digit hexadecimal representation of the octet's value. The digits of the hexadecimal alphabet,
//          for this purpose, are "0123456789ABCDEF". Uppercase letters must be used when sending hexadecimal data,
//          though a robust implementation may choose to recognize lowercase letters on receipt.
//          Thus, for example, the value 12 (ASCII form feed) can be represented by "=0C", and the value 61 (ASCII EQUAL SIGN)
//          can be represented by "=3D". Except when the following rules allow an alternative encoding, this rule is mandatory.
//
//     2. Rule #2: (Literal representation):
//          Octets with decimal values of 33 through 60 inclusive, and 62 through 126, inclusive, MAY be represented as
//          the ASCII characters which correspond to those octets (EXCLAMATION POINT through LESS THAN, and GREATER THAN
//          through TILDE, respectively).
//
//     3. Rule #3: (White Space):
//          Octets with values of 9 (tab) and 32 (space) MAY be represented as ASCII TAB (HT) and SPACE characters, respectively,
//          but MUST NOT be so represented at the end of an encoded line. Any TAB (HT) or SPACE characters on an encoded line
//          MUST thus be followed on that line by a printable character. In particular, an "=" at the end of an encoded line,
//          indicating a soft line break (see rule #5) may follow one or more TAB (HT) or SPACE characters. It follows that an octet
//          with value 9 or 32 appearing at the end of an encoded line must be represented according to Rule #1. This rule is necessary
//          because some MTAs (Message Transport Agents, programs which transport messages from one user to another, or perform a part of such transfers)
//          are known to pad lines of text with SPACEs, and others are known to remove "white space" characters from the end of a line.
//          Therefore, when decoding a Quoted-Printable body, any trailing white space on a line must be deleted, as it will necessarily have been added
//          by intermediate transport agents.
//
//     4. Rule #4 (Line Breaks):
//          A line break in a text body part, independent of what its representation is following the canonical representation of the data being encoded,
//          must be represented by a (RFC 822) line break, which is a CRLF sequence, in the Quoted- Printable encoding.
//          If isolated CRs and LFs, or LF CR and CR LF sequences are allowed to appear in binary data according to the canonical form,
//          they must be represented using the "=0D", "=0A", "=0A=0D" and "=0D=0A" notations respectively.
//
//     5. Rule #5 (Soft Line Breaks):
//          The Quoted-Printable encoding REQUIRES that encoded lines be no more than 76 characters long. If longer lines are to be encoded
//          with the Quoted-Printable encoding, 'soft' line breaks must be used. An equal sign as the last character on a encoded line indicates
//          such a non-significant ('soft') line break in the encoded text. Thus if the "raw" form of the line is a single unencoded line that says:

//             Now's the time for all folk to come to the aid of
//          their country.
//
//          This can be represented, in the Quoted-Printable encoding, as:
//
//             Now's the time =
//             for all folk to come=
//               to the aid of their country.
//
//          This provides a mechanism with which long lines are encoded in such a way as to be restored by the user agent. The 76 character limit
//          does not count the trailing CRLF, but counts all other characters, including any equal signs.
//          Since the hyphen character ("-") is represented as itself in the Quoted-Printable encoding, care must be taken, when encapsulating
//          a quoted-printable encoded body in a multipart entity, to ensure that the encapsulation boundary does not appear anywhere in the encoded body.
//          (A good strategy is to choose a boundary that includes a character sequence such as "=_" which can never appear in a quoted-printable body.)
//
// Because quoted-printable data is generally assumed to be line-oriented, it is to be expected that the breaks between the lines of quoted printable data
// may be altered in transport, in the same manner that plain text mail has always been altered in Internet mail when passing between systems with differing
// newline conventions. If such alterations are likely to constitute a corruption of the data, it is probably more sensible to use the base64 encoding rather
// than the quoted-printable encoding.

// Reader is a quoted-printable decoder.
type Reader struct {
	br   *bufio.Reader
	rerr error  // last read error
	line []byte // to be consumed before more of br (local line buffer, borrowed from/points into br's underlying buffer)
}

// NewReader returns a quoted-printable reader, decoding from r.
// If `r` is already a *bufio.Reader and it's buffer size is >= 4096 bytes,
// `r` is used directly.
func NewReader(r io.Reader) *Reader {
	return &Reader{
		br: bufio.NewReader(r),
	}
}

func fromHex(b byte) (byte, error) {
	switch {
	case b >= '0' && b <= '9':
		return b - '0', nil
	case b >= 'A' && b <= 'F':
		return b - 'A' + 10, nil
	// Accept badly encoded bytes.
	case b >= 'a' && b <= 'f':
		return b - 'a' + 10, nil
	}
	return 0, fmt.Errorf("quotedprintable: invalid hex byte 0x%02x", b)
}

func readHexByte(v []byte) (b byte, err error) {
	if len(v) < 2 { // hex byte string representation should be at least 2 ASCII characters long to accomodate 2 nibbles
		return 0, io.ErrUnexpectedEOF
	}

	var hb, lb byte // high and low nibbles
	if hb, err = fromHex(v[0]); err != nil {
		return 0, err
	}
	if lb, err = fromHex(v[1]); err != nil {
		return 0, err
	}

	return hb<<4 | lb, nil // put the nibbles in place into a single byte and return it
}

// Used to delete trailing non-canonical whitespace at the end of
// a quoted-printable line.
// Some MTAs (Message Transport Agents, programs which transport messages
// from one user to another, or perform a part of such transfers) are known
// to pad lines of text with SPACEs, and others are known to remove "white space"
// characters from the end of a line.
// Therefore, when decoding a Quoted-Printable body, any trailing white space on a line
// must be deleted, as it will necessarily have been added by intermediate transport agents.
func isQPDiscardWhitespace(r rune) bool {
	switch r {
	case '\n', '\r', ' ', '\t':
		return true
	}
	return false
}

var (
	crlf       = []byte("\r\n")
	lf         = []byte("\n")
	softSuffix = []byte("=")
)

// Read reads and decodes quoted-printable data from the underlying reader.
func (r *Reader) Read(p []byte) (n int, err error) {
	// Deviations from RFC 2045:
	//  1. in addition to "=\r\n", "=\n" is also treated as soft line break.
	//  2. it will pass through a '\r' or '\n' not preceded by '=', consistent
	//     with other broken QP encoders & decoders.
	//  3. it accepts soft line-break (=) at end of message (issue 15486); i.e.
	//     the final byte read from the underlying reader is allowed to be '=',
	//     and it will be silently ignored.
	//  4. it takes = as literal = if not followed by two hex digits
	//     but not at end of line (issue 13219).
	for len(p) > 0 {
		if len(r.line) == 0 {
			if r.rerr != nil {
				return n, r.rerr
			}
			r.line, r.rerr = r.br.ReadSlice('\n') // read in another line

			// Does the line end in CRLF instead of just LF (CRLF is canonical)?
			hasLF := bytes.HasSuffix(r.line, lf)
			hasCR := bytes.HasSuffix(r.line, crlf)
			wholeLine := r.line // original line, not stripped

			// Some MTAs (Message Transport Agents, programs which transport messages
			// from one user to another, or perform a part of such transfers) are known
			// to pad lines of text with SPACEs, and others are known to remove "white space"
			// characters from the end of a line.
			// Therefore, when decoding a Quoted-Printable body, any trailing white space on a line
			// must be deleted, as it will necessarily have been added by intermediate transport agents.
			//
			// Also strip off CR and LF, because in addition to "=\r\n", "=\n" is also treated
			// as soft line break, consistent with other broken QP encoders & decoders.
			r.line = bytes.TrimRightFunc(wholeLine, isQPDiscardWhitespace)

			// if we have a soft line break ("=") left after stripping off whitespace and "\r\n" or just "\n"
			//
			// The Quoted-Printable encoding REQUIRES that encoded lines be no more than 76 characters long.
			// If longer lines are to be encoded with the Quoted-Printable encoding, 'soft' line breaks must be used.
			// An equal sign as the last character on a encoded line indicates such a non-significant ('soft') line break
			// in the encoded text.
			if bytes.HasSuffix(r.line, softSuffix) { // we've got a soft line break
				rightStripped := wholeLine[len(r.line):] // get content after the "=" indicating a soft line break
				r.line = r.line[:len(r.line)-1]          // strip off the trailing "=" indicating a soft line break

				// If the "=" soft line break isn't followed by LF or CRLF (a new line), but:
				//   - there's some other content after the soft line break read in from the underlying buffer which doesn't start from a newline;
				//   - the read in line is empty after stripping off whitespace and the soft line break;
				//   - there's not content after the soft line break, the stripped line is not empty but we didn't get an io.EOF
				//     (deviations from RFC 2045 listed above, section 3).
				//
				// Treat this as an encoding error.
				if !bytes.HasPrefix(rightStripped, lf) && !bytes.HasPrefix(rightStripped, crlf) && !(len(rightStripped) == 0 && len(r.line) > 0 && r.rerr == io.EOF) {
					// %q - a single-quoted character literal safely escaped with Go syntax.
					r.rerr = fmt.Errorf("quotedprintable: invalid bytes after =: %q", rightStripped)
				}
			} else if hasLF { // we've got a hard line break, restore it (was previously stripped by `isQPDiscardWhitespace``)
				if hasCR { // we've got a canonical CRLF hard line break
					r.line = append(r.line, '\r', '\n')
				} else { // we've got a non-canonical LF hard line break
					r.line = append(r.line, '\n')
				}
			}

			// 1. If we've got a read error, io.EOF or a decoding error after reading the current line,
			//    the amount of read in bytes together with the error will be returned.
			// 2. If we successfully read in the line, will break out of the loop and go on to decoding the line's content.
			continue
		}
		b := r.line[0] // get first byte from the line for decision making

		switch {
		// any octet may be represented by an "=" followed by
		// a two digit hexadecimal representation of the octet's value.
		case b == '=':
			b, err = readHexByte(r.line[1:]) // read 2 bytes right after "=", those bytes are hex nibbles, so decode them into a byte
			// ivalid hex byte or short line with only 0 or 1 hex nibbles
			if err != nil {
				// takes = as literal = if not followed by two hex digits but not at end of line
				if len(r.line) >= 2 && r.line[1] != '\r' && r.line[1] != '\n' {
					// Take the = as a literal =.
					b = '='
					break
				}
				return n, err // short line with only 0 or 1 hex nibbles
			}

			// read hex byte success
			r.line = r.line[2:] // 2 of the 3 ('=' + first hex nibble); other 1 is done below (second hex nibble) - `r.line = r.line[1:]`
		case b == '\t' || b == '\r' || b == '\n': // it will pass through a '\r' or '\n' not preceded by '=', consistent with other broken QP encoders & decoders.
			// '\t' || '\r' || '\n' will be written as is to user-supplied `p` buffer
		case b >= 0x80: // out of ASCII character bounds
			// As an extension to RFC 2045, we accept
			// values >= 0x80 without complaint. Issue 22597.
		case b < ' ' || b > '~':
			// Octets with decimal values of 32 (' ') through 60 inclusive ('<'), and 62 ('>') through 126 ('~'),
			// inclusive, MAY be represented as the ASCII characters. Octet with decimal value of 61 is the '=' character,
			// which is a special character in "quotedprintable".
			//
			// Here it's not the case, so return an error.
			return n, fmt.Errorf("quotedprintable: invalid unescaped byte 0x%02x in body", b)
		}

		// Default:
		// Octets with decimal values of 32 (' ') through 60 inclusive ('<'), and 62 ('>') through 126 ('~'),
		// inclusive, MAY be represented as the ASCII characters. Octet with decimal value of 61 is the '=' character,
		// which is a special character in "quotedprintable".

		p[0] = b  // write resulting byte to caller-provided buffer
		p = p[1:] // advance buffer decreasing its length, so that we can eventually can break out of the loop
		r.line = r.line[1:]
		n++
	}

	return n, nil
}
