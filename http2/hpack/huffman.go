package hpack

import (
	"bytes"
	"errors"
	"io"
	"sync"
)

var bufPool = sync.Pool{
	New: func() interface{} { return new(bytes.Buffer) },
}

// HuffmanDecode decodes the string in v and writes the expanded
// result to w, returning the number of bytes written to w and the
// Write call's return value. At most one Write call is made.
// Doesn't specify maxLen to huffmanDecode.
func HuffmanDecode(w io.Writer, v []byte) (int, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := huffmanDecode(buf, 0, v); err != nil {
		return 0, err
	}
	return w.Write(buf.Bytes())
}

// HuffmanDecodeToString decodes the string in v.
// Doesn't specify maxLen to huffmanDecode.
func HuffmanDecodeToString(v []byte) (string, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer bufPool.Put(buf)
	if err := huffmanDecode(buf, 0, v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// ErrInvalidHuffman is returned for errors found decoding
// Huffman-encoded strings.
var ErrInvalidHuffman = errors.New("hpack: invalid Huffman-encoded data")

// https://www.youtube.com/watch?v=xqD0ZEZ0DRU&list=PLLX0OlcGRiD6UHdTkgXxxHyhVEw6sX8Al&index=5
// huffmanDecode decodes v to buf.
// TODO!!!!! Why even use maxLen? If maxLen is greater than 0, attempts to write more to buf than
// maxLen bytes will return ErrStringLength.
func huffmanDecode(buf *bytes.Buffer, maxLen int, v []byte) error {
	rootHuffmanNode := getRootHuffmanNode()
	n := rootHuffmanNode
	// cur is the bit buffer that has not been fed into n.
	// cbits is the number of low order bits in cur that are valid and also servers the purpose of telling us if we need to read in the next byte
	// sbits is the number of bits of the symbol prefix being decoded.
	cur, cbits, sbits := uint(0), uint8(0), uint8(0)
	for _, b := range v {
		cur = cur<<8 | uint(b) // (cur<<8) makes room for next byte, (| uint(b)) writes byte to the cur buffer into freed space
		cbits += 8             // read in another byte
		sbits += 8             // read in another byte
		for cbits >= 8 {
			idx := byte(cur >> (cbits - 8)) // gets the just read in byte or any left over bits from previous iteration + part of the read in byte
			n = n.children[idx]             // get huffman tree node
			if n == nil {                   // should be an internal or leaf node or else we would have already broken out of the loop
				return ErrInvalidHuffman
			}
			if n.children == nil { // we've reached a leaf node, we can decode the original symbol
				if maxLen != 0 && buf.Len() == maxLen {
					return ErrStringLength
				}
				buf.WriteByte(n.sym) // write decoded symbol to buffer
				cbits -= n.codeLen   // cbits may be greater than 0 and less than 8 after this if we have bits from next huffman code in `cur`
				n = rootHuffmanNode  // start from root huffman tree node - decode next huffman code
				sbits = cbits        // we may have bits left over from next huffman code in `cur` buffer
			} else {
				cbits -= 8 // read in byte didn't complete the current huffman code, need to read in another byte
			}
		}
	}

	for cbits > 0 { // may have a huffman code less than 8 bits in size or padding bits left in `cur` buffer
		n = n.children[byte(cur<<(8-cbits))] // byte(cur<<(8-cbits)) turns the huffman code or at max 7 bits padding into a index
		if n == nil {
			return ErrInvalidHuffman
		}
		if n.children != nil || n.codeLen > cbits { // got EOS padding bits left over in `cur` buffer
			break
		}
		if maxLen != 0 && buf.Len() == maxLen {
			return ErrStringLength
		}
		// got a huffman code less than 8 bits in size left over in `cur`
		buf.WriteByte(n.sym) // write decoded symbol to buffer
		cbits -= n.codeLen   // may have EOS padding bits left after this
		n = rootHuffmanNode  // reset to root huffman tree node
		sbits = cbits        // may have EOS padding bits left after this
	}

	if sbits > 7 {
		// Either there was an incomplete symbol, or overlong padding.
		// Both are decoding errors per RFC 7541 section 5.2.
		return ErrInvalidHuffman
	}

	if mask := uint(1<<cbits - 1); cur&mask != mask { // uint(1<<cbits - 1) gets as much 1 bits as there're bits left over
		// Trailing bits must be a prefix of EOS (all ones) per RFC 7541 section 5.2.
		return ErrInvalidHuffman
	}

	return nil
}

// incomparable is a zero-width, non-comparable type. Adding it to a struct
// makes that struct also non-comparable, and generally doesn't add
// any size (as long as it's first).
type incomparable [0]func()

type node struct {
	_ incomparable

	// children is non-nil for internal nodes
	children *[256]*node

	// The following are only valid if children is nil (leaf nodes):
	codeLen uint8 // number of bits that led to the output of sym
	sym     byte  // output symbol
}

func newInternalNode() *node {
	return &node{children: new([256]*node)}
}

var (
	buildRootOnce       sync.Once
	lazyRootHuffmanNode *node
)

func getRootHuffmanNode() *node {
	buildRootOnce.Do(buildRootHuffmanNode)
	return lazyRootHuffmanNode
}

func buildRootHuffmanNode() {
	if len(huffmanCodes) != 256 {
		panic("unexpected size")
	}

	lazyRootHuffmanNode = newInternalNode()
	// allocate a leaf node for each of the 256 symbols
	leaves := new([256]node)
	for sym, code := range huffmanCodes {
		codeLen := huffmanCodeLen[sym]
		cur := lazyRootHuffmanNode
		for codeLen > 8 {
			codeLen -= 8
			i := uint8(code >> codeLen)
			if cur.children[i] == nil {
				cur.children[i] = newInternalNode()
			}
			cur = cur.children[i]
		}

		shift := 8 - codeLen
		start, end := int(uint8(code<<shift)), int(1<<shift)
		leaves[sym].sym = byte(sym)
		leaves[sym].codeLen = codeLen
		for i := start; i < start+end; i++ {
			cur.children[i] = &leaves[sym]
		}
	}
}

// AppendHuffmanString appends s, as encoded in Huffman codes, to dst
// and returns the extended buffer.
func AppendHuffmanString(dst []byte, s string) []byte {
	// This relies on the maximum huffman code length being 30 (See tables.go huffmanCodeLen array)
	// So if a uint64 buffer has less than 32 valid bits can always accommodate another huffmanCode.
	var (
		// buffer which can accommodate minimum 2 huffman codes
		// huffman encoded characters are appended to destination in 32 bit batches
		// if their are more than 32 valid bits in buffer, the rest will go into the next 32 bit batch
		x uint64
		// number of valid bits present in x
		// (bits of the buffered huffman codes for a 32 bit batch)
		n uint // 31
	)
	for i := 0; i < len(s); i++ { // last batch may be less than 32 bits and require padding, handled outside of this loop
		c := s[i]                    // get charecter to encode
		n += uint(huffmanCodeLen[c]) // update number of valid bits in the current 32 bit batch
		x <<= huffmanCodeLen[c] % 64 // make room in buffer for the upcoming huffman code
		x |= uint64(huffmanCodes[c]) // write huffman code to buffer
		if n >= 32 {                 // 32 bit batch is full
			// calculate number of bits which should go to the next batch
			n %= 32 // Normally would be -= 32 but %= 32 informs compiler 0 <= n <= 31 for upcoming shift
			// get 32 bit batch without bits belonging to the next batch
			y := uint32(x >> n) // Compiler doesn't combine memory writes if y isn't uint32
			// write 32 bit batch to buffer and preceeed
			dst = append(dst, byte(y>>24), byte(y>>16), byte(y>>8), byte(y))
		}
	}

	// Add padding bits if necessary
	if over := n % 8; over > 0 { // over is the number of bits that need extra bits to be added to accommodate a full octet
		const (
			eosCode    = 0x3fffffff
			eosNBits   = 30                        // eos code is 30 bits long
			eosPadByte = eosCode >> (eosNBits - 8) // get the left most byte of the eos code
		)
		pad := 8 - over // how many bits to pad to get a full octet
		// (x << pad) - give room for padding
		// (eosPadByte >> over) - get the padding bits in place
		x = (x << pad) | (eosPadByte >> over) // pad
		n += pad                              // 8 now divides into n exactly
	}

	// n in (0, 8, 16, 24, 32), write last batch of huffman codes to destination
	switch n / 8 {
	case 0: // all batches were already written to destination
		return dst
	case 1: // last batch has only one byte of data possibly padded out
		return append(dst, byte(x))
	case 2: // last batch has only two bytes of data possibly padded out
		y := uint16(x)
		return append(dst, byte(y>>8), byte(y))
	case 3: // last batch has only 3 bytes of data possibly padded out
		y := uint16(x >> 8)
		return append(dst, byte(y>>8), byte(y), byte(x))
	}
	// case 4: // last batch is a full 32 bit batch, possibly padded out to 32 bits
	y := uint32(x)
	return append(dst, byte(y>>24), byte(y>>16), byte(y>>8), byte(y))
}

// HuffmanEncodeLength returns the number of bytes required to encode
// s in Huffman codes. The result is round up to byte boundary.
func HuffmanEncodeLength(s string) uint64 {
	var n uint64
	for i := 0; i < len(s); i++ {
		n += uint64(huffmanCodeLen[s[i]])
	}

	// n + 7 because 7 is the maximum padding that may be required,
	// if the padding required is less, it will be ommited after division
	return (n + 7) / 8
}
