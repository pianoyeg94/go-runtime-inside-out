package hpack

import "io"

const (
	uint32Max              = ^uint32(0)
	initialHeaderTableSize = 4096 // 4KB, the default dynamic header table size described in HPACK specification
)

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
//	A change in the maximum size of the dynamic table is signaled via the first value in a HEADERS frame by the encoder.
type Encoder struct {
	dynTab dynamicTable

	// minSize is the minimum table size set by
	// SetMaxDynamicTableSize after the previous Header Table Size Update.
	// May differ from `.dynTab.maxSize` if someone calls `.SetMaxDynamicTableSizeLimit()`
	// with a greater size and before a new write to peer is issued? TODO!!!!!
	minSize uint32

	// maxSizeLimit is the maximum table size this encoder
	// supports. This will protect the encoder from too large
	// size (by default is 4KB, the default dynamic header table size
	// described in HPACK specification).
	// May be changed by the `.SetMaxDynamicTableSizeLimit()` method.
	// Only ever set at connection establishment.
	maxSizeLimit uint32

	// tableSizeUpdate indicates whether "Header Table Size Update" is required.
	// On next write also send the "Header Table Size Update" bytes
	tableSizeUpdate bool

	w   io.Writer // w is the encoder's underlying writer
	buf []byte    // buffer used to write header field before writing it to the wire
}

// NewEncoder returns a new Encoder which performs HPACK encoding. An
// encoded data is written to w.
func NewEncoder(w io.Writer) *Encoder {
	e := &Encoder{
		minSize:         uint32Max,              // TODO!!!!!
		maxSizeLimit:    initialHeaderTableSize, // 4KB, the default dynamic header table size described in HPACK specification
		tableSizeUpdate: false,
		w:               w,
	}
	e.dynTab.table.init()                       // just allocates the dynamicTable's `.byName` and `.byNameValue` maps
	e.dynTab.setMaxSize(initialHeaderTableSize) // just sets dynamicTable's `.maxSize` to initialHeaderTableSize
	return e
}

// WriteField encodes f into a single Write to e's underlying Writer.
// This function may also produce bytes for "Header Table Size Update"
// if necessary. If produced, it is done before encoding f.
func (e *Encoder) WriteField(f HeaderField) error {
	e.buf = e.buf[:0] // clear buffer

	// a SETTINGS frame with the new value of SETTINGS_HEADER_TABLE_SIZE
	// was received from the other peer, signal to the peer that it should
	// shrink it's dynamic table on the decoding side of things
	if e.tableSizeUpdate {
		e.tableSizeUpdate = false
		if e.minSize < e.dynTab.maxSize { // TODO!!!!! in what scenario can `e.minSize` become less than `e.dynTab.maxSize` ?
			e.buf = appendTableSize(e.buf, e.minSize)
		}
		e.minSize = uint32Max
		e.buf = appendTableSize(e.buf, e.dynTab.maxSize)
	}

	idx, nameValueMatch := e.searchTable(f)
	if nameValueMatch { // header name and value are represented by a single index
		e.buf = appendIndexed(e.buf, idx)
	} else {
		indexing := e.shouldIndex(f) // should NOT be never-indexed and should be smaller in size than the dynamic table's current maximum size
		if indexing {
			// index header name and value into the dynamic table
			// (the peer decoder will do the same upon receiving this
			// set of headers and next time will encode this header name-value pair
			// as a single index into the dynamic table)
			e.dynTab.add(f)
		}

		if idx == 0 {
			// header name not previously indexed and not present in static table,
			// so header will be written as a name string - value string pair, possibly
			// huffman encoded
			e.buf = appendNewName(e.buf, f, indexing)
		} else {
			// header name previously indexed or is present in the static table,
			// so header will be sent as an index - value string pair, with the
			// value possibly huffman encoded
			e.buf = appendIndexedName(e.buf, f, idx, indexing)
		}
	}

	n, err := e.w.Write(e.buf)
	if err == nil && n != len(e.buf) {
		err = io.ErrShortWrite
	}

	return err
}

// searchTable searches f in both static and dynamic header tables.
// The static header table is searched first. Only when there is no
// exact match for both name and value, the dynamic header table is
// then searched.
// If there is no match, i is 0. If both name and value
// match, i is the matched index and nameValueMatch becomes true. If
// only name matches, i points to that index and nameValueMatch
// becomes false.
//
// More concise:
//
//  1. If there's a name value match in the static table, it's index is returned.
//
//  2. If there's a name value match in the dynamic table, it's index is returned.
//
//  3. If there's no name match in the static table, but there is on in the dynamic table, that index is returned.
//
//  4. Return static table's name index (valid or not).
//
// The returned index is a 1-based HPACK index. For dynamic tables, HPACK says
// that index 1 should be the newest entry, but t.ents[0] is the oldest entry,
// meaning t.ents is reversed for dynamic tables. Hence, when t is a dynamic
// table, the return value i actually refers to the entry t.ents[t.len()-i].
func (e *Encoder) searchTable(f HeaderField) (i uint64, nameValueMatch bool) {
	i, nameValueMatch = staticTable.search(f)
	if nameValueMatch {
		return i, true
	}

	j, nameValueMatch := e.dynTab.table.search(f)
	if nameValueMatch || (i == 0 && j != 0) {
		// HPACK specifies that the static and dymaic tables
		// share the same index space, so add the static table size
		// to the dynamic table index
		return j + uint64(staticTable.len()), nameValueMatch
	}

	return i, false
}

// SetMaxDynamicTableSize changes the dynamic header table size to v.
// The actual size is bounded by the value passed to SetMaxDynamicTableSizeLimit,
// which may change the default 4KB assigned to the `.maxSizeLimit` field.
//
// A change in the maximum size of the dynamic table is signaled via a dynamic table size update
// (in HTTP/2, this is a SETTINGS frame with the new value of SETTINGS_HEADER_TABLE_SIZE in which
// the ACK flag is not set, a recipent must apply the updated parameter as soon as possible upon receipt - call `.SetMaxDynamicTableSize()`).
func (e *Encoder) SetMaxDynamicTableSize(v uint32) {
	// An encoder can choose to use less capacity than this maximum size, but the chosen size
	// MUST stay lower than or equal to the maximum set by the protocol. Controlled by the `.maxSizeLimit`
	// field (by default is 4KB). May be changed by the `.SetMaxDynamicTableSizeLimit()` method.
	if v > e.maxSizeLimit {
		v = e.maxSizeLimit
	}

	if v < e.minSize {
		e.minSize = v // TODO!!!!!
	}

	// next header field write will observe the flag being set,
	// and will produce bytes for "Header Table Size Update"
	// before encoding the actual `HeaderField`
	e.tableSizeUpdate = true
	e.dynTab.setMaxSize(v) // may evict entries if `v` is smaller than `.dynTable.maxSize`
}

// MaxDynamicTableSize returns the current dynamic header table size.
func (e *Encoder) MaxDynamicTableSize() (v uint32) {
	return e.dynTab.maxSize
}

// Called only once at passive and active client connection establishment
// (server and client).
//
// SetMaxDynamicTableSizeLimit changes the maximum value that can be
// specified in SetMaxDynamicTableSize to v. By default, it is set to
// 4096, which is the same size of the default dynamic header table
// size described in HPACK specification. If the current maximum
// dynamic header table size is strictly greater than v, "Header Table
// Size Update" will be done in the next WriteField call and the
// maximum dynamic header table size is truncated to v.
func (e *Encoder) SetMaxDynamicTableSizeLimit(v uint32) {
	e.maxSizeLimit = v
	if e.dynTab.maxSize > v {
		// next header field write will observe the flag being set,
		// and will produce bytes for "Header Table Size Update"
		// before encoding the actual `HeaderField`
		e.tableSizeUpdate = true
		e.dynTab.setMaxSize(v) // will evict entries
	}
}

// shouldIndex reports whether f should be indexed
// (not sensitive and is smaller in size than the dynamic table's maximum size).
func (e *Encoder) shouldIndex(f HeaderField) bool {
	return !f.Sensitive && f.Size() <= e.dynTab.maxSize
}

// appendIndexed appends index i, as encoded in "Indexed Header Field"
// representation, to dst and returns the extended buffer.
//
// An indexed header field representation identifies an entry in either
// the static table or the dynamic table:
//
//	 0   1   2   3   4   5   6   7
//	+---+---+---+---+---+---+---+---+
//	| 1 |        Index (7+)         |
//	+---+---------------------------+
//
// An indexed header field starts with the '1' 1-bit pattern, followed by the index
// of the matching header field, represented as an integer with a 7-bit prefix
//
// The index value of 0 is not used. It MUST be treated as a decoding error if found
// in an indexed header field representation.
//
// book: pages 259-260
func appendIndexed(dst []byte, i uint64) []byte {
	first := len(dst) // get index of the first byte of the variable int after it will be written to dst
	dst = appendVarInt(dst, 7, i)
	// set first bit of the prefix, to indicate that this is
	// a literal header field representation, straight lookup
	// from the table (either static or dynamic)
	dst[first] |= 0x80
	return dst
}

// appendNewName appends f, as encoded in one of "Literal Header field
// - New Name" representation variants, to dst and returns the
// extended buffer.
//
// If f.Sensitive is true, "Never Indexed" representation is used. If
// f.Sensitive is false and indexing is true, "Incremental Indexing"
// representation is used.
//
// https://httpwg.org/specs/rfc7541.html#literal.header.with.incremental.indexing
// https://httpwg.org/specs/rfc7541.html#literal.header.without.indexing
// https://httpwg.org/specs/rfc7541.html#literal.header.never.indexed
//
// book: pages 260-264
func appendNewName(dst []byte, f HeaderField, indexing bool) []byte {
	dst = append(dst, encodeTypeByte(indexing, f.Sensitive)) // 01000000 for incremental indexing, 00000000 for without indexing, 00010000 for never indexed
	dst = appendHpackString(dst, f.Name)
	return appendHpackString(dst, f.Value)
}

// appendIndexedName appends f and index i referring indexed name
// entry (header name), as encoded in one of "Literal Header field - Indexed Name"
// representation variants, to dst and returns the extended buffer.
//
// If f.Sensitive is true, "Never Indexed" representation is used. If
// f.Sensitive is false and indexing is true, "Incremental Indexing"
// representation is used.
//
// https://httpwg.org/specs/rfc7541.html#literal.header.with.incremental.indexing
// https://httpwg.org/specs/rfc7541.html#literal.header.without.indexing
// https://httpwg.org/specs/rfc7541.html#literal.header.never.indexed
//
// book: pages 260-264
func appendIndexedName(dst []byte, f HeaderField, i uint64, indexing bool) []byte {
	first := len(dst) // first byte of variable int to be appended to set the right bit after append
	var n byte
	if indexing {
		n = 6 // 01000000 (6-bit prefix)
	} else {
		n = 4 // never indexed 00010000 (4-bit prefix)
	}
	dst = appendVarInt(dst, n, i)                       // append header name table index
	dst[first] |= encodeTypeByte(indexing, f.Sensitive) // set the right bit according to the type of indexing
	return appendHpackString(dst, f.Value)              // append and maybe huffman encode the header value
}

// appendTableSize appends v, as encoded in "Header Table Size Update"
// representation, to dst and returns the extended buffer.
//
// A dynamic table size update signals a change to the size of the dynamic table.
//
//	  0   1   2   3   4   5   6   7
//	+---+---+---+---+---+---+---+---+
//	| 0 | 0 | 1 |   Max size (5+)   |
//	+---+---------------------------+
//
// A dynamic table size update starts with the '001' 3-bit pattern,
// followed by the new maximum size, represented as an integer with a 5-bit prefix.
func appendTableSize(dst []byte, v uint32) []byte {
	first := len(dst)
	dst = appendVarInt(dst, 5, uint64(v))
	dst[first] |= 0x20 // set the starting '001' 3-bit pattern
	return dst
}

// appendVarInt appends i, as encoded in variable little-endian integer form using n
// bit prefix, to dst and returns the extended buffer.
//
// least significant bit comes first as in little-endian bit order.
//
// See
// https://httpwg.org/specs/rfc7541.html#integer.representation
//
// Check out book: pages 259-264, to see variable prefix lengths.
func appendVarInt(dst []byte, n byte, i uint64) []byte {
	k := uint64((1 << n) - 1) // get max uint64 that can be encoded within the prefix
	if i < k {                // if uint64 to be encoded fits within the prefix
		return append(dst, byte(i))
	}

	dst = append(dst, byte(k)) // fill prefix length with ones
	i -= k                     // decrease the initial value by (1 << n) - 1

	// slice i into 7-bit chunks and stop
	// when i can be represented by 7 bits
	for ; i >= 128; i >>= 7 { // while i has at least 8 bits of value left
		dst = append(dst, byte(0x80|(i&0x7f))) // mask off 7 right-most bits of value and store 1 in the left-most bit
	}

	return append(dst, byte(i)) // 0 in the left-most bit and 7 bits of value
}

// appendHpackString appends s, as encoded in "String Literal"
// representation, to dst and returns the extended buffer.
//
// s will be encoded in Huffman codes only when it produces strictly
// shorter byte string.
//
// https://httpwg.org/specs/rfc7541.html#string.literal.representation
func appendHpackString(dst []byte, s string) []byte {
	huffmamLength := HuffmanEncodeLength(s)
	if huffmamLength < uint64(len(s)) {
		first := len(dst) // get index of the first byte of the variable int huffman length, to later set the first bit
		dst = appendVarInt(dst, 7, huffmamLength)
		dst = AppendHuffmanString(dst, s)
		dst[first] |= 0x80 // set the 1st bit of the data written, which is the H one-bit flag, indicating that the octets of the string are Huffman encoded
	} else {
		dst = appendVarInt(dst, 7, uint64(len(s)))
		dst = append(dst, s...)
	}
	return dst
}

// encodeTypeByte returns type byte. If sensitive is true, type byte
// for "Never Indexed" (book pages 263-264) representation is returned. If sensitive is
// false and indexing is true, type byte for "Incremental Indexing" (book pages 260-262)
// representation is returned. Otherwise, type byte for "Without Indexing" is returned (book pages 262-263).
func encodeTypeByte(indexing, sensitive bool) byte {
	if sensitive {
		return 0x10 // 0b00010000
	}
	if indexing {
		return 0x40 // 0b01000000
	}
	return 0
}
