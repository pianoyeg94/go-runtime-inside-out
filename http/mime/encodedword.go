package mime

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Encoded-words is a techinique to allow the encoding of non-ASCII text in various portions of a message header.

// A WordEncoder is an RFC 2047 encoded-word encoder.
type WordEncoder byte

const (
	// BEncoding represents Base64 encoding scheme as defined by RFC 2045.
	BEncoding = WordEncoder('b')

	// QEncoding represents the Q-encoding scheme as defined by RFC 2047.
	//
	// The "Q" encoding is similar to the "Quoted-Printable" content-
	// transfer-encoding defined in RFC 2045.  It is designed to allow text
	// containing mostly ASCII characters to be decipherable on an ASCII
	// terminal without decoding.
	//
	// The Quoted-Printable encoding is intended to represent data that largely
	// consists of octets that correspond to printable characters in the US-ASCII
	// character set. It encodes the data in such a way that the resulting octets
	// are unlikely to be modified by mail transport. If the data being encoded
	// are mostly US-ASCII text, the encoded form of the data remains largely
	// recognizable by humans.
	//
	// "Q" encoding:
	//     1. (General 8bit representation) Any octet, except a CR or LF that is part
	//        of a CRLF line break of the canonical (standard) form of the data being
	//        encoded, may be represented by an "=" followed by a two digit hexadecimal
	//        representation of the octet's value. The digits of the hexadecimal alphabet,
	//        for this purpose, are "0123456789ABCDEF". Uppercase letters must be used;
	//        lowercase letters are not allowed. Thus, for example, the decimal value 12
	//        (US-ASCII form feed) can be represented by "=0C", and the decimal value 61
	//        (US-ASCII EQUAL SIGN) can be represented by "=3D". This rule must be followed
	//        except when the following rules allow an alternative encoding.
	//
	//     2. 8-bit values which correspond to printable ASCII characters other
	//        than "=", "?", and "_" (underscore), MAY be represented as those
	//        characters (except SPACE and HTAB). Printable ASCII characters
	//        are from 33 through 126 inclusive (! to ~).
	//
	//     3. Unencoded white space characters (such as SPACE and HTAB) are FORBIDDEN.
	//        These characters are not allowed, so that the beginning and end of an 'encoded-word'
	//        are obvious.
	//
	//        NOTE: The 8-bit hexadecimal value 20 (e.g., ISO-8859-1 SPACE) may be
	//              represented as "_" (underscore, ASCII 95.).  (This character may
	//              not pass through some internetwork mail gateways, but its use
	//              will greatly enhance readability of "Q" encoded data with mail
	//              readers that do not support this encoding.)  Note that the "_"
	//              always represents hexadecimal 20, even if the SPACE character
	//              occupies a different code position in the character set in use.
	//
	//     4. A "Q"-encoded 'encoded-word' which appears in a 'comment' MUST NOT
	//        contain the characters "(", ")" or " (double-quote).
	QEncoding = WordEncoder('q')
)

var (
	errInvalidWord = errors.New("mime: invalid RFC 2047 encoded-word")
)

// Encode returns the encoded-word form of s. If s is ASCII without special
// characters, it is returned unchanged. The provided charset is the IANA
// charset name of s. It is case insensitive.
func (e WordEncoder) Encode(charset, s string) string {
	if !needsEncoding(s) {
		return s
	}
	return e.encodeWord(charset, s) // may split into multiple encoded-words
}

// needsEncoding returns false if s is ASCII without special characters,
// otherwise returns true (tells whether the passed in string should be
// encoded as an encoded-word/words or can remain as plain text).
func needsEncoding(s string) bool {
	for _, b := range s {
		if (b < ' ' || b > '~') && b != '\t' {
			return true
		}
	}
	return false
}

func (e WordEncoder) encodeWord(charset, s string) string {
	var buf strings.Builder
	// Could use a hint like len(s)*3 (base64), but that's not enough
	// for cases with word splits and too much for simpler inputs.
	// 48 is close to maxEncodedWordLen/2, but adjusted to allocator size class.
	buf.Grow(48)

	// openWord writes the beginning of an encoded-word into buf in the
	// following format:
	//
	//		"=?" charset "?" encoding "?"
	//
	//	    encoding = q or b
	//
	// closeWord will append the closing "?=" after 'encoded-text'
	// is written to buf.
	e.openWord(&buf, charset)
	if e == BEncoding {
		e.bEncode(&buf, charset, s) // may split into multiple encoded-words
	} else {
		e.qEncode(&buf, charset, s) // may split into multiple encoded-words
	}
	// closeWord writes the end of an encoded-word into buf ("?=").
	closeWord(&buf)

	return buf.String()
}

const (
	// The maximum length of an encoded-word is 75 characters.
	// See RFC 2047, section 2.
	maxEncodedWordLen = 75
	// maxContentLen is how much content can be encoded, ignoring the header and
	// 2-byte footer (63 bytes).
	maxContentLen = maxEncodedWordLen - len("=?UTF-8?q?") - len("?=")
)

var maxBase64Len = base64.StdEncoding.DecodedLen(maxContentLen) // 47 bytes, maximum amount of raw bytes that can be encoded in 63 bytes of base64

// bEncode encodes s using base64 encoding and writes it to buf.
func (e WordEncoder) bEncode(buf *strings.Builder, charset, s string) {
	w := base64.NewEncoder(base64.StdEncoding, buf) // base64 writer
	// If the charset is not UTF-8 or if the base64-encoded content
	// is short (less than or equal to 47 unencoded bytes, less than
	// or equal to 63 base64-encoded bytes), do not bother splitting
	// the encoded-word.
	if !isUTF8(charset) || base64.StdEncoding.EncodedLen(len(s)) <= maxContentLen {
		io.WriteString(w, s)
		w.Close() // flush encoded bytes to underlying buf
		return
	}

	// currentLen is used to track the length of the current encoded-word,
	//            so that we do not exceed the maximum encoded-word length of 75 bytes.
	//            This constraint includes 'charset', 'encoding', 'encoded-text', and
	//            delimiters. Original content length in this case should be <= 47 bytes,
	//            and the base64-encoded content should be <= 63 bytes.
	//
	// last       starting index of next encoded word
	//
	// runeLen    is used track the previous rune length to skip that amount of written
	//            prepared to be written bytes on next iteration
	var currentLen, last, runeLen int
	for i := 0; i < len(s); i += runeLen {
		_, runeLen = utf8.DecodeRuneInString(s[i:])

		if currentLen+runeLen <= maxBase64Len {
			currentLen += runeLen
		} else {
			io.WriteString(w, s[last:i])
			w.Close() // flush encoded bytes to underlying buf
			e.splitWord(buf, charset)
			last = i
			currentLen = runeLen
		}
	}
	io.WriteString(w, s[last:]) // no-op if whole string already written, otherwise the original string content left is less than 47 bytes
	w.Close()                   // flush encoded bytes to underlying buf
}

func (e WordEncoder) qEncode(buf *strings.Builder, charset, s string) {
	// We only split encoded-words when the charset is UTF-8.
	if !isUTF8(charset) {
		// Else encode the string as a single encoded-word.
		//
		// May result in an invalid encoded-word because it should be no more
		// than 75 characters long, including 'charset', 'encoding', 'encoded-text',
		// and delimiters.
		writeQString(buf, s)
		return
	}

	// currentLen is used to track the length of the current encoded-word,
	//            so that we do not exceed the maximum encoded-word length of 75 bytes.
	//            This constraint includes 'charset', 'encoding', 'encoded-text', and
	//            delimiters.
	//
	// runeLen    is used track the previous rune length to skip that amount of written
	//            bytes on next iteration
	var currentLen, runeLen int
	for i := 0; i < len(s); i += runeLen {
		b := s[i] // common scenario when s[i] is an ASCII character
		// Multi-byte characters must not be split across encoded-words.
		// See RFC 2047, section 5.3.
		//
		// The 'encoded-text' in an 'encoded-word' must be self-contained;
		// 'encoded-text' MUST NOT be continued from one 'encoded-word' to
		// another.
		//
		// Each 'encoded-word' MUST represent an integral number of characters.
		// A multi-octet character may not be split across adjacent 'encoded-
		// word's.
		var encLen int
		if b >= ' ' && b <= '~' && b != '=' && b != '?' && b != '_' {
			// raw ASCII character except reserved characters
			runeLen, encLen = 1, 1
		} else {
			// UTF-8 multibyte rune or reserved ASCII character
			_, runeLen = utf8.DecodeRuneInString(s[:i])
			encLen = 3 * runeLen // 3 character hex for every raw byte of rune
		}

		if currentLen+encLen > maxContentLen {
			// if raw content except header and footer of encoded-word exceeds 63 bytes,
			// split encoded-words on integral number of characters boundary.
			e.splitWord(buf, charset) // writes footer for previous encoded-word and header for the upcoming one
			currentLen = 0
		}
		writeQString(buf, s[i:i+runeLen])
		currentLen += encLen
	}
}

// writeQString encodes s using Q encoding and writes it to buf.
func writeQString(buf *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch b := s[i]; {
		case b == ' ':
			// Unencoded white space characters (such as SPACE and HTAB) are FORBIDDEN.
			// These characters are not allowed, so that the beginning and end of an 'encoded-word'
			// are obvious.
			//
			// The 8-bit hexadecimal value 20 (e.g., ISO-8859-1 SPACE) may be
			// represented as "_" (underscore, ASCII 95.).  (This character may
			// not pass through some internetwork mail gateways, but its use
			// will greatly enhance readability of "Q" encoded data with mail
			// readers that do not support this encoding.)  Note that the "_"
			// always represents hexadecimal 20, even if the SPACE character
			// occupies a different code position in the character set in use.
			buf.WriteByte('_')
		case b >= '!' && b <= '~' && b != '=' && b != '?' && b != '_':
			// 8-bit values which correspond to printable ASCII characters other
			// than "=", "?", and "_" (underscore), MAY be represented as those
			// characters (except SPACE and HTAB). Printable ASCII characters
			// form the range 33 through 126 inclusive (! to ~).
			buf.WriteByte(b)
		default:
			// Any octet, except a CR or LF that is part of a CRLF line break of
			// the canonical (standard) form of the data being encoded, may be
			// represented by an "=" followed by a two digit hexadecimal
			// representation of the octet's value. The digits of the hexadecimal alphabet,
			// for this purpose, are "0123456789ABCDEF". Uppercase letters must be used;
			// lowercase letters are not allowed. Thus, for example, the decimal value 12
			// (US-ASCII form feed) can be represented by "=0C", and the decimal value 61
			// (US-ASCII EQUAL SIGN) can be represented by "=3D". This rule must be followed
			// except when the following rules allow an alternative encoding.
			buf.WriteByte('=')
			buf.WriteByte(upperhex[b>>4])
			buf.WriteByte(upperhex[b&0x0f])
		}
	}
}

// openWord writes the beginning of an encoded-word into buf in the
// following format:
//
//		"=?" charset "?" encoding "?"
//
//	    encoding = q or b
//
// closeWord will append the closing "?=" after 'encoded-text'
// is written to buf.
func (e WordEncoder) openWord(buf *strings.Builder, charset string) {
	buf.WriteString("=?")
	buf.WriteString(charset)
	buf.WriteByte('?')
	buf.WriteByte(byte(e))
	buf.WriteByte('?')
}

// closeWord writes the end of an encoded-word into buf.
func closeWord(buf *strings.Builder) {
	buf.WriteString("?=")
}

// splitWord closes the current encoded-word and opens a new one.
func (e WordEncoder) splitWord(buf *strings.Builder, charset string) {
	closeWord(buf)
	buf.WriteByte(' ') // seperate encoded-words with LW (linear whitespace)
	e.openWord(buf, charset)
}

func isUTF8(charset string) bool {
	return strings.EqualFold(charset, "UTF-8")
}

const upperhex = "0123456789ABCDEF"

// A WordDecoder decodes MIME headers containing RFC 2047 encoded-words.
type WordDecoder struct {
	// CharsetReader, if non-nil, defines a function to generate
	// charset-conversion readers, converting from the provided
	// charset into UTF-8.
	// Charsets are always lower-case. utf-8, iso-8859-1 and us-ascii charsets
	// are handled by default.
	// One of the CharsetReader's result values must be non-nil.
	CharsetReader func(charset string, input io.Reader) (io.Reader, error)
}

// Decode decodes an RFC 2047 encoded-word.
func (d *WordDecoder) Decode(word string) (string, error) {
	// See https://tools.ietf.org/html/rfc2047#section-2 for details.
	// Our decoder is permissive, we accept empty encoded-text.
	if len(word) < 8 || !strings.HasPrefix(word, "=?") || !strings.HasSuffix(word, "?=") || strings.Count(word, "?") != 4 {
		// len(word) < 8: "=?" + "at least one byte to specify a character set" + "?" "Q|B" + "?" + "?="
		return "", errInvalidWord
	}
	word = word[2 : len(word)-2] // strip off "=?" and "?="

	// split word "UTF-8?q?text" into "UTF-8" and "q?text"
	charset, text, _ := strings.Cut(word, "?")
	if charset == "" {
		return "", errInvalidWord
	}
	// split word  "q?text" into 'q' and "text"
	encoding, text, _ := strings.Cut(text, "?")
	if len(encoding) != 1 {
		// should be B or Q (also q and b, we handle lowercase encoding gracefuly)
		return "", errInvalidWord
	}

	content, err := decode(encoding[0], text) // decode text from B (base64) or Q encoding into raw bytes
	if err != nil {
		return "", err
	}

	var buf strings.Builder
	// convert converts raw bytes in charset `charset` into utf-8 charset (if not already in utf-8)
	if err := d.convert(&buf, charset, content); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// DecodeHeader decodes all encoded-words of the given string. It returns an
// error if and only if CharsetReader of d returns an error.
func (d *WordDecoder) DecoderHeader(header string) (string, error) {
	// If there is no encoded-word, returns before creating a buffer.
	i := strings.Index(header, "=?")
	if i == -1 {
		return header, nil
	}

	var buf strings.Builder

	buf.WriteString(header[:i]) // write data before start of the first encoded-word
	header = header[i:]

	betweenWords := false // TODO!!!!!
	for {
		start := strings.Index(header, "=?")
		if start == -1 {
			// no more encoded-words left in header
			break
		}
		cur := start + len("=?") // position of first character of charset

		i := strings.Index(header[cur:], "?")
		if i == -1 {
			// charset doesn't end with a ?, possibly incomplete encoded-word
			// or not an encoded word at all
			break
		}

		charset := header[cur : cur+i]
		cur += i + len("?") // position of encoding byte (Q or B)

		if len(header) < cur+len("Q??=") {
			// incomplete encoded word or not an encoded word at all
			break
		}
		encoding := header[cur]
		cur++ // increment to proceed to ?

		if header[cur] != '?' {
			// invalid encoded word or not an encoded word at all
			break
		}
		cur++ // increment to proceed to first byte of content

		j := strings.Index(header[cur:], "?=")
		if j == -1 {
			// incomplete content of encoded word or not an encoded word at all
			break
		}
		text := header[cur : cur+j] // encoded word content
		end := cur + j + len("?=")  // position of next byte after encoded word

		content, err := decode(encoding, text) // decode text from B (base64) or Q encoding into raw bytes
		if err != nil {
			// invalid encoded-word content encoding or not an encoded word at all
			betweenWords = false              // consider the possibility that we got data partially matching encoded-word patterns but not an encoded-word
			buf.WriteString(header[:start+2]) // write raw "=?" to buffer
			// on next iteration try out next encoded-word (if any),
			// the partially matching encoded-word patterns data will be written
			// to buffer as is after check `start > 0 && (!betweenWords || hasNonWhitespace(header[:start]))`
			// bellow or outside of the loop, if there's no more succeeding encoded-words
			header = header[start+2:]
			continue
		}

		// Write characters before the encoded-word. White-space and newline
		// characters separating two encoded-words must be deleted.
		if start > 0 && (!betweenWords || hasNonWhitespace(header[:start])) {
			// This can happen if there was data that partially matched encoded-word
			// patterns, but wasn't an encoded-word (check out decode error handling above).
			//
			// if there's content before the encoded-word and there's no preceeding
			// encoded-word and the content has at least one non-WS character,
			// write it to buf before writing the decoded encoded-word content
			buf.WriteString(header[:start])
		}

		// convert converts raw bytes in charset `charset` into utf-8 charset (if not already in utf-8)
		if err := d.convert(&buf, charset, content); err != nil {
			return "", err
		}

		// discard content before possible white-space and newline characters separating
		// the next encoded-word from the just decoded one
		header = header[end:]
		betweenWords = true
	}

	if len(header) > 0 {
		// write left over non-encoded-word data
		buf.WriteString(header)
	}

	return buf.String(), nil
}

func decode(encoding byte, text string) ([]byte, error) {
	switch encoding {
	case 'B', 'b':
		return base64.StdEncoding.DecodeString(text)
	case 'Q', 'q':
		return qDecode(text)
	default:
		return nil, errInvalidWord
	}
}

// convert converts raw bytes in charset `charset` into utf-8 charset (if not already in utf-8)
func (d *WordDecoder) convert(buf *strings.Builder, charset string, content []byte) error {
	switch {
	case strings.EqualFold("utf-8", charset):
		// raw bytes already in utf-8 charset,
		// just write to buffer
		buf.Write(content)
	case strings.EqualFold("iso-8859-1", charset):
		// ASCII vs  Latin-1 (ISO-8859-1):
		//     ASCII is a seven-bit encoding technique which assigns
		//     a number to each of the 128 characters used most frequently
		//     in American English. ISO 8859 is an eight-bit extension to
		//     ASCII developed by ISO (the International Organization for Standardization).
		//     ISO 8859 includes the 128 ASCII characters along with an additional 128
		//     characters, such as the British pound symbol, the American cent symbol, äöåéü
		//     and other characters .
		//
		// UTF-8 vs Latin-1 (ISO-8859-1):
		//     UTF-* is a variable-length encoding, latter single-byte fixed length encoding.
		//     Latin-1 encodes just the first 256 code points of the Unicode character set,
		//     whereas UTF-8 can be used to encode all code points. At physical encoding level,
		//     only codepoints 0 - 127 get encoded identically; code points 128 - 255 differ by
		//     becoming 2-byte sequence with UTF-8 whereas they are single bytes with Latin-1.
		for _, c := range content {
			buf.WriteRune(rune(c))
		}
	case strings.EqualFold("us-ascii", charset):
		for _, c := range content {
			if c >= utf8.RuneSelf {
				// NOT us-ascii char, unicode.ReplacementChar represents invalid code points.
				buf.WriteRune(unicode.ReplacementChar)
			} else {
				buf.WriteByte(c)
			}
		}
	default:
		// If unknow charset (other than utf-8, iso-8859-1, us-ascii)
		// CharsetReader, if non-nil, defines a function to generate
		// charset-conversion readers, converting from the provided
		// charset into UTF-8.
		// One of the CharsetReader's result values must be non-nil.
		if d.CharsetReader == nil {
			return fmt.Errorf("mime: unhandled charset %q", charset)
		}
		r, err := d.CharsetReader(strings.ToLower(charset), bytes.NewReader(content))
		if err != nil {
			return err
		}
		if _, err = io.Copy(buf, r); err != nil {
			return err
		}
	}
	return nil
}

// hasNonWhitespace reports whether s (assumed to be ASCII) contains at least
// one byte of non-whitespace.
func hasNonWhitespace(s string) bool {
	for _, b := range s {
		switch b {
		// Encoded-words can only be separated by linear white spaces which does
		// not include vertical tabs (\v).
		case ' ', '\t', '\n', '\r':
		default:
			return true
		}
	}
	return false
}

// qDecode decodes a Q encoded string.
func qDecode(s string) ([]byte, error) {
	dec := make([]byte, len(s)) // may overstimate if string contains control, non-ASCII or reserved by encoded-word syntax characters
	n := 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '_':
			// The 8-bit hexadecimal value 20 (e.g., ISO-8859-1 SPACE) may be
			// represented as "_" (underscore, ASCII 95.).  (This character may
			// not pass through some internetwork mail gateways, but its use
			// will greatly enhance readability of "Q" encoded data with mail
			// readers that do not support this encoding.)  Note that the "_"
			// always represents hexadecimal 20, even if the SPACE character
			// occupies a different code position in the character set in use.
			dec[n] = ' '
		case c == '=':
			// Any octet, except a CR or LF that is part of a CRLF line break of
			// the canonical (standard) form of the data being encoded, may be
			// represented by an "=" followed by a two digit hexadecimal
			// representation of the octet's value. The digits of the hexadecimal alphabet,
			// for this purpose, are "0123456789ABCDEF". Uppercase letters must be used;
			// lowercase letters are not allowed. Thus, for example, the decimal value 12
			// (US-ASCII form feed) can be represented by "=0C", and the decimal value 61
			// (US-ASCII EQUAL SIGN) can be represented by "=3D". This rule must be followed
			// except when the following rules allow an alternative encoding.
			if i+2 >= len(s) {
				// not enough bytes for a hex substring byte representation
				return nil, errInvalidWord
			}
			b, err := readHexByte(s[i+1], s[i+2])
			if err != nil {
				return nil, err
			}
			dec[n] = b
			i += 2
		case (c >= ' ' && c <= '~') || c == '\n' || c == '\r' || c == '\t':
			// 8-bit values which correspond to printable ASCII characters other
			// than "=", "?", and "_" (underscore), MAY be represented as those
			// characters. Printable ASCII characters form the range 33 through
			// 126 inclusive (! to ~) and CR, LF and HTAB.
			//
			// SPACE and HTAB are bad characters within an encoded-word, but handle
			// such violations gracefuly.
			dec[n] = c
		default:
			return nil, errInvalidWord
		}
		n++
	}

	return dec[:n], nil
}

// readHexByte returns the byte from its quoted-printable representation.
func readHexByte(a, b byte) (byte, error) {
	var hb, lb byte
	var err error
	if hb, err = fromHex(a); err != nil {
		return 0, err
	}
	if lb, err = fromHex(b); err != nil {
		return 0, err
	}
	return hb<<4 | lb, nil
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
	return 0, fmt.Errorf("mime: invalid hex byte %#02x", b)
}
