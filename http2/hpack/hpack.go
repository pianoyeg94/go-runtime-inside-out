// Package hpack implements HPACK, a compression format for
// efficiently representing HTTP header fields in the context of HTTP/2.
//
// See http://tools.ietf.org/html/draft-ietf-httpbis-header-compression-09
package hpack

import (
	"bytes"
	"errors"
	"fmt"
)

// HPACK: Header Compression for HTTP/2: https://httpwg.org/specs/rfc7541.html

// A DecodingError is something the spec defines as a decoding error.
type DecodingError struct {
	Err error
}

func (de DecodingError) Error() string {
	return fmt.Sprintf("decoding error: %v", de.Err)
}

// An InvalidIndexError is returned when an encoder references a table
// entry before the static table or after the end of the dynamic table.
type InvalidIndexError int

func (e InvalidIndexError) Error() string {
	return fmt.Sprintf("invalid indexed representation index %d", int(e))
}

// A HeaderField is a name-value pair. Both the name and value are
// treated as opaque sequences of octets.
type HeaderField struct {
	Name, Value string

	// Sensitive means that this header field should never be
	// indexed.
	Sensitive bool
}

// IsPseudo reports whether the header field is an http2 pseudo header.
// That is, it reports whether it starts with a colon.
// It is not otherwise guaranteed to be a valid pseudo header field,
// though.
func (hf HeaderField) IsPseudo() bool {
	return len(hf.Name) != 0 && hf.Name[0] == ':'
}

func (hf HeaderField) String() string {
	var suffix string
	if hf.Sensitive {
		suffix = " (sensitive)"
	}

	// %q - a double-quoted string safely escaped with Go syntax.
	return fmt.Sprintf("header field %q = %q%s", hf.Name, hf.Value, suffix)
}

// Size returns the size of an entry per RFC 7541 section 4.1.
func (hf HeaderField) Size() uint32 {
	// https://httpwg.org/specs/rfc7541.html#rfc.section.4.1
	// "The size of the dynamic table is the sum of the size of
	// its entries. The size of an entry is the sum of its name's
	// length in octets (as defined in Section 5.2), its value's
	// length in octets (see Section 5.2), plus 32.  The size of
	// an entry is calculated using the length of the name and
	// value without any Huffman encoding applied."

	// This can overflow if somebody makes a large HeaderField
	// Name and/or Value by hand, but we don't care, because that
	// won't happen on the wire because the encoding doesn't allow
	// it.
	return uint32(len(hf.Name) + len(hf.Value) + 32)
}

// A Decoder is the decoding context for incremental processing of
// header blocks.
type Decoder struct {
	dynTab dynamicTable

	// user-provided callback into which a decoded HeaderField is passed into,
	// will be called for each valid field parsed, in the same goroutine as calls
	// to Write, before Write returns TODO!!!!!
	emit func(f HeaderField)

	// controls whether the emitFunc provided to NewDecoder
	// should be called. The default is true.
	//
	// TODO!!!!! This facility exists to let servers enforce MAX_HEADER_LIST_SIZE
	// while still decoding and keeping in-sync with decoder state, but
	// without doing unnecessary decompression or generating unnecessary
	// garbage for header fields past the limit.
	emitEnabled bool // whether calls to emit are enabled
	maxStrLen   int  // 0 means unlimited TODO!!!!! who sets maxStrLen and why?

	// TODO!!!!! buf is the unparsed buffer. It's only written to
	// saveBuf if it was truncated in the middle of a header
	// block. Because it's usually not owned, we can only
	// process it under Write.
	buf []byte // not owned; only valid during Write

	// TODO!!!!!! saveBuf is previous data passed to Write which we weren't able
	// to fully parse before. Unlike buf, we own this data.
	saveBuf bytes.Buffer

	firstField bool // processing the first field of the header block, is used to check that a dynamic header table update occurs as the first header value
}

// NewDecoder returns a new decoder with the provided maximum dynamic
// table size. The emitFunc will be called for each valid field
// parsed, in the same goroutine as calls to Write, before Write returns.
func NewDecoder(maxDynamicTableSize uint32, emitFunc func(f HeaderField)) *Decoder {
	d := &Decoder{
		emit:        emitFunc,
		emitEnabled: true,
		firstField:  true,
	}
	d.dynTab.table.init() // just allocates the dynamicTable's `.byName` and `.byNameValue` maps

	// sets the upper bound that the encoded stream
	// (via dynamic table size updates) may set the maximum size
	// to, maxSize may go up to this, inclusive (but not above)
	d.dynTab.allowedMaxSize = maxDynamicTableSize
	d.dynTab.setMaxSize(maxDynamicTableSize) // just sets dynamicTable's `.maxSize` to `maxDynamicTableSize`
	return d
}

// TODO!!!!! ErrStringLength is returned by Decoder.Write when the max string length
// (as configured by Decoder.SetMaxStringLength) would be violated.
var ErrStringLength = errors.New("hpack: string too long")

// TODO!!!!! what for?  SetMaxStringLength sets the maximum size of a HeaderField name or
// value string. If a string exceeds this length (even after any
// decompression), Write will return ErrStringLength.
// A value of 0 means unlimited and is the default from NewDecoder.
func (d *Decoder) SetMaxStringLength(n int) {
	d.maxStrLen = n
}

// SetEmitFunc changes the callback used when new header fields
// are decoded.
// It must be non-nil. It does not affect EmitEnabled.
func (d *Decoder) SetEmitFunc(emitFunc func(f HeaderField)) {
	d.emit = emitFunc
}

// SetEmitEnabled controls whether the emitFunc provided to NewDecoder
// should be called. The default is true.
//
// TODO!!!!! This facility exists to let servers enforce MAX_HEADER_LIST_SIZE
// while still decoding and keeping in-sync with decoder state, but
// without doing unnecessary decompression or generating unnecessary
// garbage for header fields past the limit.
func (d *Decoder) SetEmitEnabled(v bool) { d.emitEnabled = v }

// EmitEnabled reports whether calls to the emitFunc provided to NewDecoder
// are currently enabled. The default is true.
func (d *Decoder) EmitEnabled() bool { return d.emitEnabled }

// TODO: add method *Decoder.Reset(maxSize, emitFunc) to let callers re-use Decoders and their
// underlying buffers for garbage reasons.

func (d *Decoder) SetMaxDynamicTableSize(v uint32) {
	d.dynTab.setMaxSize(v)
}

// SetAllowedMaxDynamicTableSize sets the upper bound that the encoded
// stream (via dynamic table size updates) may set the maximum size
// to.
func (d *Decoder) SetAllowedMaxDynamicTableSize(v uint32) {
	d.dynTab.allowedMaxSize = v
}

// Encoder decides how to update the dynamic table and as such can control how much memory is used by the dynamic table.
// To limit the memory requirements of the decoder, the dynamic table size is strictly bounded by the other peer by sending
// the SETTINGS_HEADER_TABLE_SIZE parameter with the SETTINGS frame.
//
// An encoder can choose to use less capacity than this maximum size, but the chosen size MUST stay lower than or equal
// to the maximum set by the protocol.
//
// The decoder updates the dynamic table during the processing of a list of header field representations.
//
// Decoder issued:
//
//	A change in the maximum size of the dynamic table is signaled via a dynamic table size update by the decoder.
//	(in HTTP/2, this is a SETTINGS frame with the new value of SETTINGS_HEADER_TABLE_SIZE in which
//	the ACK flag is not set, a recipent must apply the updated parameter as soon as possible upon receipt - call `.SetMaxDynamicTableSize()`).
//	The new maximum size MUST be lower than or equal to the limit determined by the protocol using HPACK.
//	A value that exceeds this limit MUST be treated as a decoding error. In HTTP/2, this limit is the last value
//	of the SETTINGS_HEADER_TABLE_SIZE parameter received from the decoder and acknowledged by the encoder
//	(once the new maximum is applied the recipient MUST immediately emit a SETTINGS frame with the ACK flag set, if the sender of
//	a SETTINGS frame does not receive an acknowledgement within a reasonable amount of time, it MAY issue a connection error of type SETTINGS_TIMEOUT).
//	Reducing the maximum size of the dynamic table can cause entries to be evicted.
//
// Encoder issued perhaps as a result of receiving SETTINGS_HEADER_TABLE_SIZE from the decoder:
//
//	A change in the maximum size of the dynamic table is signaled via the first field in a HEADERS frame sent by the encoder.
type dynamicTable struct {
	// https://httpwg.org/specs/rfc7541.html#rfc.section.2.3.2
	table          headerFieldTable
	size           uint32 // in bytes
	maxSize        uint32 // current maxSize
	allowedMaxSize uint32 // maxSize may go up to this, inclusive
}

// Reducing the maximum size of the dynamic table can cause entries to be evicted.
func (dt *dynamicTable) setMaxSize(v uint32) {
	dt.maxSize = v
	dt.evict()
}

func (dt *dynamicTable) add(f HeaderField) {
	dt.table.addEntry(f)
	// https://httpwg.org/specs/rfc7541.html#rfc.section.4.1
	// "The size of the dynamic table is the sum of the size of
	// its entries. The size of an entry is the sum of its name's
	// length in octets (as defined in Section 5.2), its value's
	// length in octets (see Section 5.2), plus 32.  The size of
	// an entry is calculated using the length of the name and
	// value without any Huffman encoding applied."
	dt.size += f.Size()
}

// If we're too big, evict old stuff.
func (dt *dynamicTable) evict() {
	var n int
	for dt.size > dt.maxSize && n < dt.table.len() { // evict at max the whole table
		// https://httpwg.org/specs/rfc7541.html#rfc.section.4.1
		// "The size of the dynamic table is the sum of the size of
		// its entries. The size of an entry is the sum of its name's
		// length in octets (as defined in Section 5.2), its value's
		// length in octets (see Section 5.2), plus 32.  The size of
		// an entry is calculated using the length of the name and
		// value without any Huffman encoding applied."
		dt.size -= dt.table.ents[n].Size()
		n++
	}
	// evicts the n oldest entries in the table.
	// Deletes entries from byName and byNameValue maps.
	// Performs some manipulations to save the ents slice's
	// capacity after removing entries from the beginning of this slice.
	dt.table.evictOldest(n)
}

// maxTableIndex returns the current maximum index
// within the combined static and dynamic table index address space.
// https://httpwg.org/specs/rfc7541.html#rfc.section.2.3.3
func (d *Decoder) maxTableIndex() int {
	// This should never overflow. RFC 7540 Section 6.5.2 limits the size of
	// the dynamic table to 2^32 bytes, where each entry will occupy more than
	// one byte. Further, the staticTable has a fixed, small length.
	return d.dynTab.table.len() + staticTable.len()
}

func (d *Decoder) at(i uint64) (hf HeaderField, ok bool) {
	// See Section 2.3.3: https://httpwg.org/specs/rfc7541.html#rfc.section.2.3.3
	if i == 0 { // indexing starts at 1
		return
	}

	if i <= uint64(staticTable.len()) { // index belongs to static table
		return staticTable.ents[i-1], true // `i-1` because inexing starts with 1
	}

	if i > uint64(d.maxTableIndex()) { // not in static and not in dynamic table
		return
	}

	// In the dynamic table, newer entries have lower indices.
	// However, dt.ents[0] is the oldest entry. Hence, dt.ents is
	// the reversed dynamic table, so the passed in index with the
	// length of the static tables subtracted is treated as an offset
	// from the last element in the `dt.ents` slice of HeaderFields.
	//
	// `int(i)-staticTable.len())` because the length of the static table
	// is subtracted to find the index into the dynamic table according to
	// the specification.
	dt := d.dynTab.table
	return dt.ents[dt.len()-(int(i)-staticTable.len())], true
}

// DecodeFull decodes an entire block.
//
// TODO: remove this method and make it incremental later? This is
// easier for debugging now.
func (d *Decoder) DecodeFull(p []byte) ([]HeaderField, error) {
	var hf []HeaderField
	saveFunc := d.emit
	defer func() { d.emit = saveFunc }()
	d.emit = func(f HeaderField) { hf = append(hf, f) } // after each header value pair is decoded it will be passed to this function
	if _, err := d.Write(p); err != nil {
		return nil, err
	}
	if err := d.Close(); err != nil {
		return nil, err
	}
	return hf, nil
}

// Close declares that the decoding is complete and resets the Decoder
// to be reused again for a new header block. If there is any remaining
// data in the decoder's buffer, Close returns an error.
func (d *Decoder) Close() error {
	if d.saveBuf.Len() > 0 {
		d.saveBuf.Reset()
		return DecodingError{errors.New("truncated headers")}
	}
	d.firstField = true
	return nil
}

func (d *Decoder) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		// TODO!!!!! Prevent state machine CPU attacks (making us redo
		// work up to the point of finding out we don't have
		// enough data)
		return
	}

	// Only copy the data if we have to. Optimistically assume
	// that p will contain a complete header block.
	if d.saveBuf.Len() == 0 { // if no previous data passed to Write which we weren't able to fully parse before is residing in `saveBuf`
		d.buf = p
	} else {
		d.saveBuf.Write(p) // concatenate new data with old unparsed data left over from previous call to Write
		d.buf = d.saveBuf.Bytes()
		d.saveBuf.Reset()
	}

	for len(d.buf) > 0 {
		err = d.parseHeaderFieldRepr() // will eventually call `.callEmit` with the parsed out HeaderField
		if err == errNeedMore {
			// Extra paranoia, making sure saveBuf won't
			// get too large. All the varint and string
			// reading code earlier should already catch
			// overlong things and return ErrStringLength,
			// but keep this as a last resort.
			const varIntOverhead = 8 // conservative, may not fit into the first byte
			if d.maxStrLen != 0 && int64(len(d.buf)) > 2*(int64(d.maxStrLen)+varIntOverhead) {
				return 0, ErrStringLength
			}

			d.saveBuf.Write(d.buf) // save for next call to Write with more data
			return len(p), nil
		}
		d.firstField = false
		if err != nil {
			break
		}
	}

	return len(p), err
}

// errNeedMore is an internal sentinel error value that means the
// buffer is truncated and we need to read more data before we can
// continue parsing.
var errNeedMore = errors.New("need more data")

type indexType int

const (
	indexedTrue indexType = iota
	indexedFalse
	indexedNever
)

func (v indexType) indexed() bool   { return v == indexedTrue }
func (v indexType) sensitive() bool { return v == indexedNever }

// returns errNeedMore if there isn't enough data available.
// any other error is fatal.
// consumes d.buf if it returns nil.
// precondition: must be called with len(d.buf) > 0
func (d *Decoder) parseHeaderFieldRepr() error {
	// TODO!!!!!
	// header field prefix, the bit pattern of which
	//  tells us the type of header field we're currently decoding
	b := d.buf[0]
	switch {
	case b&128 != 0:
		// Indexed representation.
		// High bit set?
		// https://httpwg.org/specs/rfc7541.html#rfc.section.6.1
		return d.parseFieldIndexed()
	case b&192 == 64:
		// 6.2.1 Literal Header Field with Incremental Indexing
		// 0b10xxxxxx: top two bits are 10
		// https://httpwg.org/specs/rfc7541.html#rfc.section.6.2.1
		d.parseFieldLiteral(6, indexedTrue)
	case b&240 == 0:
		// 6.2.2 Literal Header Field without Indexing
		// 0b0000xxxx: top four bits are 0000
		// https://httpwg.org/specs/rfc7541.html#rfc.section.6.2.2
		return d.parseFieldLiteral(4, indexedFalse)
	case b&240 == 16:
		// 6.2.3 Literal Header Field never Indexed
		// 0b0001xxxx: top four bits are 0001
		// https://httpwg.org/specs/rfc7541.html#rfc.section.6.2.3
		return d.parseFieldLiteral(4, indexedNever)
	case b&224 == 32:
		// 6.3 Dynamic Table Size Update
		// Top three bits are '001'.
		// https://httpwg.org/specs/rfc7541.html#rfc.section.6.3
		return d.parseDynamicTableSizeUpdate()
	}

	return DecodingError{errors.New("invalid encoding")}
}

// (same invariants and behavior as parseHeaderFieldRepr)
func (d *Decoder) parseFieldIndexed() error {
	buf := d.buf

	// 7-bit prefix, because the 1st bit is reserved for indicating that this header
	// field is indexed
	// index may be encoded in more than 7 bits, in this case the first
	// 7 bits are zeroed-out and the actual interger is encoded in 7-bit chunks
	// in little-endian byte order (7-bit chunks, because the 1st bit of each byte
	// is used as a continuation flag, which, if is set, tells us, the decoder,
	// that there're more 7-bit chunks of the integer yet to come).
	idx, buf, err := readVarInt(7, buf) // read header field index to index the combined static and dynamic table index address space
	// err may be either errVarintOverflow (if overflows 64-bit integer)
	// or errNeedMore if there's not enough space in `buf` to decode the whole index
	if err != nil {
		return err
	}

	hf, ok := d.at(idx) // get value of cached HeaderField key-value pair from static or dynamic table
	if !ok {
		return DecodingError{InvalidIndexError(idx)}
	}

	d.buf = buf // update the unparsed buffer "pointer"

	// callEmit validates HeaderField's name and value
	// against Decoder's maxStrLen if it's set (not 0).
	// Also calls Decoder's emit() function, if emit is enabled.
	//
	// Only error that can be returned from this method is ErrStringLength.
	return d.callEmit(HeaderField{Name: hf.Name, Value: hf.Value}) // copy name and value into a new HeaderField
}

// (same invariants and behavior as parseHeaderFieldRepr)
//
// `n` parameter is the length of the header prefix in bits.
// TODO!!!!! The `it` parameter holds details about,
// whether the header name-value pair should be added to
// the dynamic table or not.
func (d *Decoder) parseFieldLiteral(n uint8, it indexType) error {
	buf := d.buf                            // used to store the in progress buffer "pointer" while parsing the header name and value
	nameIdx, buf, err := readVarInt(n, buf) // parse the header prefix and header name index if indexed in static table
	if err != nil {
		return err
	}

	var hf HeaderField
	wantStr := d.emitEnabled || it.indexed()
	var undecodedName unencodedString
	if nameIdx > 0 { // if header name already indexed in static table
		ihf, ok := d.at(nameIdx) // get header name from static table
		if !ok {
			return DecodingError{InvalidIndexError(nameIdx)}
		}
		hf.Name = ihf.Name // copy header name
	} else { // header name is a string literal (huffman or not)
		// read header name validating it's length if Decoder's maxStrLen is not 0 +
		// determining via the 1st bit of the prefix specifying the length of the string
		// if the header name is huffman encoded.
		// Update buffer pointer to point just after the header name.
		undecodedName, buf, err = d.readString(buf)
		if err != nil {
			return err
		}
	}

	// read header value validating it's length if Decoder's maxStrLen is not 0 +
	// determining via the 1st bit of the prefix specifying the length of the string
	// if the header value is huffman encoded or not.
	// Update buffer pointer to point just after the header name.
	undecodedValue, buf, err := d.readString(buf)
	if err != nil {
		return err
	}

	if wantStr { // if emitEnabled or header name-value should be indexed
		if nameIdx <= 0 { // header name is a string literal
			hf.Name, err = d.decodeString(undecodedName) // possibly do huffman decoding
			if err != nil {
				return err
			}
		}
		hf.Value, err = d.decodeString(undecodedValue) // possibly do huffman decoding
		if err != nil {
			return err
		}
	}

	d.buf = buf // copy new buffer pointer
	if it.indexed() {
		d.dynTab.add(hf)
	}
	hf.Sensitive = it.sensitive() // just copy the value

	// callEmit validates HeaderField's name and value
	// against Decoder's maxStrLen if it's set (not 0).
	// Also calls Decoder's emit() function, if emit is enabled.
	//
	// Only error that can be returned from this method is ErrStringLength.
	return d.callEmit(hf)
}

// callEmit validates HeaderField's name and value
// against Decoder's maxStrLen if it's set (not 0).
// Also calls Decoder's emit() function, if emit is enabled.
//
// Only error that can be returned from this method is ErrStringLength.
func (d *Decoder) callEmit(hf HeaderField) error {
	if d.maxStrLen != 0 {
		if len(hf.Name) > d.maxStrLen || len(hf.Value) > d.maxStrLen {
			return ErrStringLength
		}
	}

	if d.emitEnabled {
		d.emit(hf)
	}

	return nil
}

// (same invariants and behavior as parseHeaderFieldRepr)
//
// Decoder issued:
//
//	A change in the maximum size of the dynamic table is signaled via a dynamic table size update by the decoder.
//	(in HTTP/2, this is a SETTINGS frame with the new value of SETTINGS_HEADER_TABLE_SIZE in which
//	the ACK flag is not set, a recipent must apply the updated parameter as soon as possible upon receipt - call `.SetMaxDynamicTableSize()`).
//	The new maximum size MUST be lower than or equal to the limit determined by the protocol using HPACK.
//	A value that exceeds this limit MUST be treated as a decoding error. In HTTP/2, this limit is the last value
//	of the SETTINGS_HEADER_TABLE_SIZE parameter received from the decoder and acknowledged by the encoder
//	(once the new maximum is applied the recipient MUST immediately emit a SETTINGS frame with the ACK flag set, if the sender of
//	a SETTINGS frame does not receive an acknowledgement within a reasonable amount of time, it MAY issue a connection error of type SETTINGS_TIMEOUT).
//	Reducing the maximum size of the dynamic table can cause entries to be evicted.
//
// Encoder issued perhaps as a result of receiving SETTINGS_HEADER_TABLE_SIZE from the decoder:
//
//	A change in the maximum size of the dynamic table is signaled via the first field in a HEADERS frame sent by the encoder.
func (d *Decoder) parseDynamicTableSizeUpdate() error {
	// RFC 7541, sec 4.2: This dynamic table size update MUST occur at the
	// beginning of the first header block following the change to the dynamic table size.
	if !d.firstField && d.dynTab.size > 0 {
		return DecodingError{errors.New("dynamic table size update MUST occur at the beginning of a header block")}
	}

	buf := d.buf                         // used to store the in progress buffer "pointer" while parsing the dynamic table size update
	size, buf, err := readVarInt(5, buf) // a dynamic table size update header should have a 5-bit prefix   0   1   2   3   4   5   6   7
	//                                                                                                     +---+---+---+---+---+---+---+---+
	//                                                                                                     | 0 | 0 | 1 |   Max size (5+)   |
	//                                                                                                     +---+---------------------------+
	if err != nil {
		return err
	}

	if size > uint64(d.dynTab.allowedMaxSize) {
		return DecodingError{errors.New("dynamic table size update too large")}
	}

	d.dynTab.setMaxSize(uint32(size)) // may cause entries to be evicted
	d.buf = buf
	return nil
}

var errVarintOverflow = DecodingError{errors.New("varint integer overflows")}

// readVarInt reads an unsigned variable length integer off the
// beginning of p. n is the parameter as described in
// https://httpwg.org/specs/rfc7541.html#rfc.section.5.1.
//
// least significant bit come first as in little-endian bit order.
//
// n must always be between 1 and 8.
//
// The returned remain buffer is either a smaller suffix of p, or err != nil.
// The error is errNeedMore if p doesn't contain a complete integer.
func readVarInt(n byte, p []byte) (i uint64, remain []byte, err error) {
	if n < 1 || n > 8 { // prefix should be at least 1 bit and at most 8 bits
		panic("bad n")
	}

	if len(p) == 0 {
		return 0, p, errNeedMore
	}

	i = uint64(p[0]) // get prefix
	if n < 8 {
		i &= (1 << uint64(n)) - 1 // mask off the header type
	}

	if i < (1<<uint64(n))-1 { // if i fits within the prefix
		return i, p[1:], nil
	}

	origP := p
	p = p[1:]
	var m uint64
	for len(p) > 0 {
		b := p[0] // get next octet
		p = p[1:]
		i += uint64(b&127) << m // mask off the flag bit and add next 7 bits to resulting uint64 (little-endian)
		if b&128 == 0 {         // left-most bit was zero (sentinel value), last part of varint
			return i, p, nil
		}

		m += 7 //
		if m >= 63 {
			return 0, origP, errVarintOverflow
		}
	}

	return 0, origP, errNeedMore // if couldn't get full varint from p
}

// readString reads an hpack string from p.
//
// TODO!!!!! It returns a reference to the encoded string data to permit deferring decode costs
// until after the caller verifies all data is present.
func (d *Decoder) readString(p []byte) (u unencodedString, remain []byte, err error) {
	if len(p) == 0 {
		return u, p, errNeedMore
	}

	// 128 = 0b10000000, mask-off the high bit and check if it's set or not,
	// if set the string is huffman encoded
	isHuff := p[0]&128 != 0
	// 7-bit prefix, because the 1st bit is reserved for the huffman flag,
	// string length may be encoded in more than 7 bits, in this case the first
	// 7 bits are zeroed-out and the actual interger is encoded in 7-bit chunks
	// in little-endian byte order (7-bit chunks, because the 1st bit of each byte
	// is used as a continuation flag, which, if is set, tells us, the decoder,
	// that there're more 7-bit chunks of the integer yet to come).
	strLen, p, err := readVarInt(7, p)
	// err may be either errVarintOverflow (if overflows 64-bit integer)
	// or errNeedMore if there's not enough space in `p` to decode the whole
	// integer
	if err != nil {
		return u, p, err
	}

	if d.maxStrLen != 0 && strLen > uint64(d.maxStrLen) {
		// TODO!!!!! Returning an error here means Huffman decoding errors
		// for non-indexed strings past the maximum string length
		// are ignored, but the server is returning an error anyway
		// and because the string is not indexed the error will not
		// affect the decoding state.
		return u, nil, ErrStringLength
	}

	// if after decoding the string's
	// length there's not enough room
	// for the actual string in `p`
	if uint64(len(p)) < strLen {
		return u, nil, errNeedMore
	}

	u.isHuff = isHuff
	u.b = p[:strLen] // actual string data (huffman encoded or not)
	return u, p[strLen:], nil
}

type unencodedString struct {
	isHuff bool
	b      []byte
}

func (d *Decoder) decodeString(u unencodedString) (string, error) {
	if !u.isHuff {
		return string(u.b), nil
	}

	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset() // don't trust others
	var s string
	err := huffmanDecode(buf, d.maxStrLen, u.b) // TODO!!!!! maxStrLen
	if err == nil {
		s = buf.String()
	}
	buf.Reset() // be nice to GC
	bufPool.Put(buf)
	return s, nil
}
