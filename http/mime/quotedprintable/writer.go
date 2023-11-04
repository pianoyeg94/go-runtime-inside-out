package quotedprintable

import "io"

const lineMaxLen = 76 // needs soft-line breaks after this

// A Writer is a quoted-printable writer that implements io.WriteCloser.
type Writer struct {
	// Binary mode treats the writer's input as pure binary and processes end of
	// line bytes (CRLF, LF) as binary data.
	Binary bool

	w io.Writer // underlying writer
	i int       // current position in the `.line` buffer

	// // temporary storage for a quoted-printable encoded line before
	// it's flushed to the underlying writer, greater than `lineMaxLen` by
	// 3 to leave space for soft line breaks
	line [78]byte

	// used to signal that we've encountered a CR to skip next LF character if any,
	// because the CR causes a CRLF addition to the line and line flush
	cr bool
}

// NewWriter returns a new Writer that writes to w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{w: w}
}

// Write encodes p using quoted-printable encoding and writes it to the
// underlying io.Writer. It limits line length to 76 characters. The encoded
// bytes are not necessarily flushed until the Writer is closed.
func (w *Writer) Write(p []byte) (n int, err error) {
	for i, b := range p {
		switch {
		// Octets with decimal values of 32 (' ') through 60 inclusive ('<'), and 62 ('>') through 126 ('~'),
		// inclusive, MAY be represented as the ASCII characters. Octet with decimal value of 61 is the '=' character,
		// which is a special character in "quotedprintable".
		//
		// Simple writes are done in batch (coniguous bytes that don't need to be hex encoded or escaped like '=').
		case b >= '!' && b <= '~' && b != '=':
			continue // write to line when contiguous stride of bytes that don't need to be encoded ends
		case isWhitespace(b) || !w.Binary && (b == '\n' || b == '\r'): // same as above
			continue
		}

		// if a contiguous stride of bytes that don't need to be hex encoded
		// has come to an end, write them in a batch, may flush to underlying
		// writer if the 76 character line limit is exceeded
		if i > n {
			if err := w.write(p[n:i]); err != nil {
				return n, err
			}
			n = i
		}

		// may insert a soft line break and
		// flush to underlying writer if
		// the 76 character line limit is exceeded
		if err := w.encode(b); err != nil {
			return n, err
		}
		n++
	}

	if n == len(p) { // if we don't have a stride of coniguous bytes that don't need to be hex encoded at the end of the iterations
		return n, nil
	}

	if err := w.write(p[n:]); err != nil { //if we have a stride of coniguous bytes that don't need to be hex encoded at the end the iterations
		return n, err
	}

	return len(p), nil
}

// Close closes the Writer, flushing any unwritten data to the underlying
// io.Writer, but does not close the underlying io.Writer.
//
// Basically this Writer can be reused after Close() is called on it.
func (w *Writer) Close() error {
	// checkLastByte encodes the last buffered byte if it is a space or a tab,
	// because octets with values of 9 (tab) and 32 (space) MAY be represented
	// as ASCII TAB (HT) and SPACE characters, respectively,
	// but MUST NOT be so represented at the end of an encoded line
	if err := w.checkLastByte(); err != nil {
		return err
	}

	// flush flushes the current content
	// in the `.line` temporary storage
	// to the underlying `io.Writer` and
	// resets the position in the line buffer to 0
	return w.flush()
}

// write limits text encoded (not hex encoded) in quoted-printable to 76 characters per line.
func (w *Writer) write(p []byte) error {
	for _, b := range p {
		if b == '\n' || b == '\r' {
			// If the previous byte was \r, the CRLF has already been inserted
			// (check out down bellow)
			if w.cr && b == '\n' {
				w.cr = false
				continue
			}

			if b == '\r' { // for skipping next LF if any performed above
				w.cr = true
			}

			// Encodes the last buffered byte if it is a space or a tab,
			// octets with values of 9 (tab) and 32 (space) MAY be represented
			// as ASCII TAB (HT) and SPACE characters, respectively,
			// but MUST NOT be so represented at the end of an encoded line.
			// May flush current line to the underlying writer if the addition
			// of encoded whitespace would exceed the 76 character line limit.
			if err := w.checkLastByte(); err != nil {
				return err
			}

			// Adds CRLF to the end of the line and
			// flushes the line to the underlying writer,
			// resetting `.i` to zero, which is the current
			// position in the line.
			if err := w.insertCRLF(); err != nil {
				return err
			}

			continue
		}

		// addition of a new character would exceed
		// the 76 character line limit, insert soft
		// line break, flush line to the underlying
		// writer and reset line position to 0.
		if w.i == lineMaxLen-1 {
			if err := w.insertSoftLineBreak(); err != nil {
				return err
			}
		}

		w.line[w.i] = b
		w.i++        // advance line position
		w.cr = false // if CR wasn't followed by a LF
	}

	return nil
}

// encode hex encodes a byte into the form "=1C"
func (w *Writer) encode(b byte) error {
	// if 3 more characters written into buffer would exceed the
	// maximum line liength of 76 characters, insert a soft line break,
	// and flush the buffer to the underlying writer
	if lineMaxLen-1-w.i < 3 { //
		if err := w.insertSoftLineBreak(); err != nil {
			return err
		}
	}

	w.line[w.i] = '='
	w.line[w.i+1] = upperhex[b>>4]   // write the hex character corresponding to the high nibble of the byte
	w.line[w.i+2] = upperhex[b&0x0f] // masks off the low nibble of the byte and writes it's corresonding hex character
	w.i += 3

	return nil
}

// Any octet, except those indicating a line break according to the newline convention
// of the canonical form of the data being encoded, may be represented by an "=" followed by
// a two digit hexadecimal representation of the octet's value. The digits of the hexadecimal alphabet,
// for this purpose, are "0123456789ABCDEF". Uppercase letters must be used when sending hexadecimal data,
// though a robust implementation may choose to recognize lowercase letters on receipt.
const upperhex = "0123456789ABCDEF"

// checkLastByte encodes the last buffered byte if it is a space or a tab,
// because octets with values of 9 (tab) and 32 (space) MAY be represented
// as ASCII TAB (HT) and SPACE characters, respectively,
// but MUST NOT be so represented at the end of an encoded line
func (w *Writer) checkLastByte() error {
	if w.i == 0 { // we're currently at the start of the `line` buffer
		return nil
	}

	b := w.line[w.i-1]
	if isWhitespace(b) {
		w.i-- // ovewrite unencoded whitespace
		// may flush buffer to the underlying writer
		// if the addition of encoded whitespace
		// would exceed the 76 character line limit
		if err := w.encode(b); err != nil {
			return err
		}
	}

	return nil
}

// insertSoftLineBreak inserts a soft line break
// in the form of "=\r\n" into the
// temporary `.line` buffer and flushes
// the line to the underlying `io.Writer`
// and resets the position in the buffer
// to 0
func (w *Writer) insertSoftLineBreak() error {
	w.line[w.i] = '='
	w.i++

	return w.insertCRLF()
}

// insertCRLF inserts CRLF into the
// temporary `.line` buffer and flushes
// the line to the underlying `io.Writer`
// and resets the position in the buffer
// to 0
func (w *Writer) insertCRLF() error {
	w.line[w.i] = '\r'
	w.line[w.i+1] = '\n'
	w.i += 2

	return w.flush()
}

// flush flushes the current content
// in the `.line` temporary storage
// to the underlying `io.Writer` and
// resets the position in the line buffer to 0
func (w *Writer) flush() error {
	if _, err := w.Write(w.line[:w.i]); err != nil {
		return err
	}

	w.i = 0
	return nil
}

func isWhitespace(b byte) bool {
	return b == ' ' || b == '\t'
}
