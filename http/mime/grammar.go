package mime

import "strings"

// isTSpecial reports whether rune is in 'tspecials' as defined by RFC
// 1521 and RFC 2045.
//
// https://www.rfc-editor.org/rfc/rfc1521#section-4
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
//
// tspecials :=  "(" / ")" / "<" / ">" / "@" /
// ______________"," / ";" / ":" / "\" / <">
// ______________"/" / "[" / "]" / "?" / "="
// ______________; Must be in quoted-string,
// ______________; to use within parameter values
func isTSpecial(r rune) bool {
	return strings.ContainsRune(`()<>@,;:\"/[]?=`, r)
}

// isTokenChar reports whether rune is in 'token' as defined by RFC
// 1521 and RFC 2045 (any (US-ASCII) CHAR except SPACE, CTLs, or tspecials)
//
// https://www.rfc-editor.org/rfc/rfc1521#section-4
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
func isTokenChar(r rune) bool {
	// token := 1*<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials>
	//
	// 0x20 = space (first non-control ASCII character)
	// 0x7f = delete (last ASCII character)
	return r > 0x20 && r < 0x7f && !isTSpecial(r)
}

// isToken reports whether s is a 'token' as defined by RFC 1521
// and RFC 2045.
//
// https://www.rfc-editor.org/rfc/rfc1521#section-4
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
func isToken(s string) bool {
	if s == "" {
		return false
	}

	return strings.IndexFunc(s, isTokenChar) < 0
}
