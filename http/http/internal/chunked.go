package internal

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

const maxLineLength = 4096 // assumed <= bufio.defaultBufSize (underlying bufio reader uses a 4096-byte buffer)

var ErrLineTooLong = errors.New("header line to long")

// NewChunkedReader returns a new chunkedReader that translates the data read from r
// out of HTTP "chunked" format before returning it.
// The chunkedReader returns io.EOF when the final 0-length chunk is read.
//
// NewChunkedReader is not needed by normal applications. The http package
// automatically decodes chunking when reading response bodies.
func NewChunkedReader(r io.Reader) io.Reader {
	br, ok := r.(*bufio.Reader)
	if !ok {
		br = bufio.NewReader(r)
	}
	return &chunkedReader{r: br}
}

// https://www.rfc-editor.org/rfc/rfc9112#name-chunked-transfer-coding
type chunkedReader struct {
	r        *bufio.Reader
	n        uint64 // unread bytes in chunk
	err      error
	buf      [2]byte // scrap buffer for throwing away '\r\n' at the end of each chunk
	checkEnd bool    // whether need to check for \r\n chunk footer
}

func (cr *chunkedReader) beginChunk() {
	// chunk-size CRLF
	var line []byte
	if line, cr.err = readChunkLine(cr.r); cr.err != nil { // read chunk length omitting any extensions present
		return
	}

	cr.n, cr.err = parseHexUint(line) // parse chunk length into a uint64
	if cr.err != nil {
		return
	}

	if cr.n == 0 { // sentinel last chunk with size 0
		cr.err = io.EOF
	}
}

func (cr *chunkedReader) chunkHeaderAvailable() bool {
	n := cr.r.Buffered() // do not perform a possible read from network
	if n > 0 {           // maybe we have another chunk header available
		peek, _ := cr.r.Peek(n)                 // do not perform a read, beginChunk will do it for us
		return bytes.IndexByte(peek, '\n') >= 0 // if true we have a new chunk header available
	}
	return false
}

// Read may yield a partial chunk, a single chunk,
// a single chunk + part of the next chunk or even multiple
// chunks in a single call. It depends on the size of the provided
// buffer 'b' and whether after reading a single chunk there's a chunk size
// line already present in the buffer for the next chunk.
func (cr *chunkedReader) Read(b []uint8) (n int, err error) {
	for cr.err == nil {
		if cr.checkEnd {
			// we have read in the whole chunk but not "\r\n",
			if n > 0 && cr.r.Buffered() < 2 {
				// We have some data. Return early (per the io.Reader
				// contract) instead of potentially blocking while
				// reading more.
				//
				// "\r\n" will be read into the scrap buffer next time Read is called,
				// becuase cr.checkEnd will still be set. Only after that the subsequent
				// Read will begin parsing the new chunk.
				break
			}

			// read CRLF into scrap buffer
			if _, err := io.ReadFull(cr.r, cr.buf[:2]); err == nil {
				if string(cr.buf[:]) != "\r\n" {
					cr.err = errors.New("malformed chunked encoding")
					break
				}
			} else {
				if cr.err == io.EOF {
					cr.err = io.ErrUnexpectedEOF
				}
				break
			}
			cr.checkEnd = false
		}

		if cr.n == 0 {
			// if this call already yielded a full chunk,
			// and there's no next chunk's size line
			// available in buffer, let the next call to Read handle
			// the new chunk.
			//
			// BUT if there's is already a next chunk size line available in buffer,
			// we continue processing the next chunk via cr.beginChunk(),
			// which may or not fit into the provided user buffer.
			// So a single call to Read may yield 1 or more chunks.
			if n > 0 && !cr.chunkHeaderAvailable() {
				// We've read enough. Don't potentially block
				// reading a new chunk header.
				break
			}

			cr.beginChunk() // parse chunk length into cr.n from first line ommitting extensions
			continue
		}

		if len(b) == 0 { // b cannot accomodate full chunk, at this point part of the chunk may be already read into b
			break
		}

		rbuf := b                     // b's length decreases on each iteration as new chunks of data are read into it
		if uint64(len(rbuf)) > cr.n { // cr.n decreases on each iteration as more and more data is read from the chunk
			rbuf = rbuf[:cr.n] // cap the buffer to remaining chunk size
		}

		var n0 int
		n0, cr.err = cr.r.Read(rbuf)
		n += n0
		b = b[n0:]
		cr.n -= uint64(n0)
		// If we're at the end of a chunk, read the next two
		// bytes to verify they are "\r\n".
		if cr.n == 0 && cr.err == nil { // check chunk footer
			cr.checkEnd = true
		} else if cr.err == io.EOF { // encountered EOF before reading the whole chunk
			cr.err = io.ErrUnexpectedEOF
		}
	}

	return n, cr.err // at this point the chunk may be fully or partially read into b
}

func readChunkLine(b *bufio.Reader) ([]byte, error) {
	p, err := b.ReadSlice('\n')
	if err != nil {
		// We always know when EOF is coming.
		// If the caller asked for a line, there should be a line.
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		} else if err == bufio.ErrBufferFull {
			err = ErrLineTooLong
		}

		return nil, err
	}

	if len(p) >= maxLineLength {
		return nil, ErrLineTooLong
	}

	p = trimTrailingWhitespace(p)
	if p, err = removeChunkExtension(p); err != nil {
		return nil, err
	}

	return p, nil
}

func trimTrailingWhitespace(b []byte) []byte {
	for len(b) > 0 && isASCIISpace(b[len(b)-1]) {
		b = b[:len(b)-1]
	}
	return b
}

func isASCIISpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '\r'
}

var semi = []byte(";")

// removeChunkExtension removes any chunk-extension from p.
// For example,
//
//	"0" => "0"
//	"0;token" => "0"
//	"0;token=val" => "0"
//	`0;token="quoted string"` => "0"
//
// https://www.rfc-editor.org/rfc/rfc9112#name-chunk-extensions
func removeChunkExtension(p []byte) ([]byte, error) {
	p, _, _ = bytes.Cut(p, semi)
	// TODO: care about exact syntax of chunk extensions? We're
	// ignoring and stripping them anyway. For now just never
	// return an error.
	return p, nil
}

func NewChunkedWriter(w io.Writer) io.WriteCloser {
	return &chunkedWriter{w}
}

// Writing to chunkedWriter translates to writing in HTTP chunked Transfer
// Encoding wire format to the underlying Wire chunkedWriter.
type chunkedWriter struct {
	Wire io.Writer // is usually buffered
}

// Write the contents of data as one chunk to Wire.
// NOTE: Note that the corresponding chunk-writing procedure in Conn.Write has
// a bug since it does not check for success of io.WriteString
func (cw *chunkedWriter) Write(data []byte) (n int, err error) {
	// Don't send 0-length data. It looks like EOF for chunked encoding.
	if len(data) == 0 {
		return 0, nil
	}

	// write size of chunk in hex without any extensions
	if _, err = fmt.Fprintf(cw.Wire, "%x\r\n", len(data)); err != nil {
		return 0, err
	}

	// write the actual chunk data
	if n, err = cw.Wire.Write(data); err != nil {
		return
	}

	if n != len(data) {
		err = io.ErrShortWrite
		return
	}

	// write ending CRLF
	if _, err = io.WriteString(cw.Wire, "\r\n"); err != nil {
		return
	}

	// if underlying Wire is a buffer, it needs to be flushed
	if bw, ok := cw.Wire.(*FlushAfterChunkWriter); ok {
		err = bw.Flush()
	}

	return
}

// Close writes sentinel 0 sized chunk
func (cw *chunkedWriter) Close() error {
	_, err := io.WriteString(cw.Wire, "0\r\n")
	return err
}

// FlushAfterChunkWriter signals from the caller of NewChunkedWriter
// that each chunk should be followed by a flush. It is used by the
// http.Transport code to keep the buffering behavior for headers and
// trailers, but flush out chunks aggressively in the middle for
// request bodies which may be generated slowly. See Issue 6574.
type FlushAfterChunkWriter struct {
	*bufio.Writer
}

func parseHexUint(v []byte) (n uint64, err error) {
	for i, b := range v {
		switch {
		case '0' <= b && b <= '9': // chars '0' throuh '9' can be converted to uint64 as follows
			b = b - '0' // convert to nibble representing 0-9 in binary
		case 'a' <= b && b <= 'f':
			b = b - 'a' + 10 // convert to binary nibble
		case 'A' <= b && b <= 'F':
			b = b - 'A' + 10
		default:
			return 0, errors.New("invalid byte in chunk length")
		}

		if i == 16 { // if greater than a 64 bit uint
			return 0, errors.New("http chunk length too large")
		}

		n <<= 4 // make place for new nibble in little-endian representation
		n |= uint64(b)
	}

	return
}
