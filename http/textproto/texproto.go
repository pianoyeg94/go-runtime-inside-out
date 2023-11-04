package textproto

// A ProtocolError describes a protocol violation such
// as an invalid response or a hung-up connection.
type ProtocolError string

func (p ProtocolError) Error() string {
	return string(p)
}

func isASCIILetter(b byte) bool {
	b |= 0x20
	return 'a' <= b && b <= 'z'
}
