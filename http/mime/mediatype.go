package mime

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// FormatMediaType serializes mediatype t and the parameters
// param as a media type conforming to RFC 2045 and RFC 2616.
// The type and parameter names are written in lower-case.
// When any of the arguments result in a standard violation then
// FormatMediaType returns the empty string.
func FormatMediaType(t string, param map[string]string) string {
	var b strings.Builder
	if major, sub, ok := strings.Cut(t, "/"); !ok {
		// `t` is a mediatype WITHOUT a subtype
		if !isToken(t) {
			// type should only contain token characters:
			//   token := 1*<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials>
			//
			// Should NOT contain:
			//   0x20 = space (first non-control ASCII character)
			//   0x7f = delete (last ASCII character)
			//
			//   tspecials :=  "(" / ")" / "<" / ">" / "@" /
			// ________________"," / ";" / ":" / "\" / <">
			// ________________"/" / "[" / "]" / "?" / "="
			return ""
		}
		b.WriteString(strings.ToLower(t))
	} else {
		// `t` is a mediatype with a subtype
		if !isToken(major) || !isToken(sub) {
			// type and subtype should only contain token characters:
			//   token := 1*<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials>
			//
			// Should NOT contain:
			//   0x20 = space (first non-control ASCII character)
			//   0x7f = delete (last ASCII character)
			//
			//   tspecials :=  "(" / ")" / "<" / ">" / "@" /
			// ________________"," / ";" / ":" / "\" / <">
			// ________________"/" / "[" / "]" / "?" / "="
			return ""
		}
		b.WriteString(strings.ToLower(major))
		b.WriteByte('/')
		b.WriteString(strings.ToLower(sub))
	}

	attrs := make([]string, 0, len(param)) // stores sorted parameter names
	for a := range param {
		attrs = append(attrs, a)
	}
	sort.Strings(attrs)

	for _, attribute := range attrs {
		value := param[attribute] // get parameter value by name

		// write parameter name separated from mediatype and other parameters by "; ".
		b.WriteByte(';')
		b.WriteByte(' ')
		if !isToken(attribute) {
			// parameter names should only contain token characters:
			//   token := 1*<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials>
			//
			// Should NOT contain:
			//   0x20 = space (first non-control ASCII character)
			//   0x7f = delete (last ASCII character)
			//
			//   tspecials :=  "(" / ")" / "<" / ">" / "@" /
			// ________________"," / ";" / ":" / "\" / <">
			// ________________"/" / "[" / "]" / "?" / "="
			return ""
		}
		b.WriteString(strings.ToLower(attribute)) // write parameter name after "; "

		// Returns false if value is ASCII without special characters,
		// otherwise returns true (tells whether the passed in value
		// can remain unchanged or should be percent-hex-encoded with
		// character set and language information, since the encoded-word
		// mechanisms are not available to parameter values).
		//
		// Only character set information is not sufficient to properly display
		// some sorts of information - language information is also needed.
		// For example, support for handicapped users may require reading text string
		// aloud. The language the text is written in is needed for this to be done correctly.
		// Some parameter values may need to be displayed, hence there is a need to allow for
		// the inclusion of language information.
		needEnc := needsEncoding(value)
		if needEnc {
			// RFC 2231 section 4
			//
			// Asterisks ("*") are reused to provide the indicator that language and character
			// set information is present and encoding is being used. Specifically, an asterisk
			// at the end of a parameter name acts as an indicator that character set and language
			// information may appear at the beginning of the parameter value.
			b.WriteByte('*')
		}
		b.WriteByte('=') // next comes the parameter value

		if needEnc {
			// A single quote is used to separate the character set, language, and actual value information
			// in the parameter value string.
			//
			//     Content-Type: application/x-stuff;
			//      title*=us-ascii'en-us'This%20is%20%2A%2A%2Afun%2A%2A%2A
			//
			// Note that it is perfectly permissible to leave either the character set or language field blank.
			// In our case we omit lanugage information. Note also that the single quote delimiters MUST be present
			// even when one of the field values is omitted.
			b.WriteString("utf-8''")

			offset := 0 // running index into value at which a run of characters that do need percent-hex encoding begins
			for index := 0; index < len(value); index++ {
				ch := value[index]
				// {RFC 2231 section 7}
				// attribute-char := <any (US-ASCII) CHAR except SPACE, CTLs, "*", "'", "%", or tspecials>
				//
				// Should NOT contain:
				//   0x20 = space (first non-control ASCII character)
				//   0x7F = delete (last ASCII character)
				//
				//   tspecials :=  "(" / ")" / "<" / ">" / "@" /
				// ________________"," / ";" / ":" / "\" / <">
				// ________________"/" / "[" / "]" / "?" / "="
				if ch <= ' ' || ch >= 0x7F || ch == '*' || ch == '\'' || ch == '%' || isTSpecial(rune(ch)) {
					// percent-hex encode
					b.WriteString(value[offset:index]) // write previously accumalated characters (if any) that do not need percent-hex encoding
					offset = index + 1                 // since we've encountered a byte that needs to be percent-hex encoded, advance the offset

					// write percent-hex encoded byte
					b.WriteByte('%')
					b.WriteByte(upperhex[ch>>4])
					b.WriteByte(upperhex[ch&0x0F])
				}
			}
			b.WriteString(value[offset:]) // write the left-over run of characters (if any) that do not need percent-hex encoding
			continue                      // write (and possibly encode) next parameter name-value pair
		}

		if isToken(value) {
			// value doesn't need to enclosed in quotes.
			//
			// value consists only of *<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials> characters
			//
			// Should NOT contain:
			//   0x20 = space (first non-control ASCII character)
			//   0x7F = delete (last ASCII character)
			//
			//   tspecials :=  "(" / ")" / "<" / ">" / "@" /
			// ________________"," / ";" / ":" / "\" / <">
			// ________________"/" / "[" / "]" / "?" / "="
			b.WriteString(value)
			continue // write (and possibly encode) next parameter name-value pair
		}

		// value needs to be enclosed in quotes
		b.WriteByte('"')
		// Running index into value at which a run of characters
		// that do need to be escaped via the '\\'  begins.
		//
		// OR index at which the character that needs to escaped resides at.
		offset := 0
		for index := 0; index < len(value); index++ {
			character := value[index]
			if character == '"' || character == '\\' {
				b.WriteString(value[offset:index]) // write run of non-escaped characters possibly together wuth last character that needs to be escaped
				offset = index
				b.WriteByte('\\') // write escape sequence, character to be escaped will be written on succeeding iterations or after the loop stops
			}
		}
		b.WriteString(value[offset:]) // write run of non-escaped characters possibly together wuth last character that needs to be escaped
		b.WriteByte('"')
	}
	return b.String()
}

// checkMediaTypeDisposition validates that the given
// media type consists only of token chars as per RFC 1521
// and 2045 and can be optionally followed by a subtype
// separated from the type by a forward slash "/".
//
// token := 1*<any (US-ASCII) CHAR except SPACE, CTLs, or tspecials>
// _________0x20 = space (first non-control ASCII character)
// _________0x7f = delete (last ASCII character)
//
// tspecials :=  "(" / ")" / "<" / ">" / "@" /
// ______________"," / ";" / ":" / "\" / <">
// ______________"/" / "[" / "]" / "?" / "="
// ______________; Must be in quoted-string,
// ______________; to use within parameter values
func checkMediaTypeDisposition(s string) error {
	typ, rest := consumeToken(s) // since "/" isn't considered as a token as per RFC 1521 and 2045, consuming a token yields the content type without the subtype
	if typ == "" {
		return errors.New("mime: no media type")
	}
	if rest == "" {
		// no media subtype, just return
		return nil
	}
	if !strings.HasPrefix(rest, "/") {
		// if there is a content subtype, then it should be separated
		// from the type by a forward slash ("/")
		return errors.New("mime: expected slash after first token")
	}
	subtype, rest := consumeToken(rest[1:]) // rest[1:] - skip "/"
	if subtype == "" {
		return errors.New("mime: expected token after slash")
	}
	if rest != "" {
		return errors.New("mime: unexpected content after media subtype")
	}
	return nil
}

// ErrInvalidMediaParameter is returned by ParseMediaType if
// the media type value was found but there was an error parsing
// the optional parameters
var ErrInvalidMediaParameter = errors.New("mime: invalid media parameter")

// ParseMediaType parses a media type value and any optional
// parameters, per RFC 1521.  Media types are the values in
// Content-Type and Content-Disposition headers (RFC 2183).
// On success, ParseMediaType returns the media type converted
// to lowercase and trimmed of white space and a non-nil map.
// If there is an error parsing the optional parameters,
// the media type will be returned along with the error
// ErrInvalidMediaParameter.
// The returned map, params, maps from the lowercase
// attribute to the attribute value with its case preserved.
//
// https://www.rfc-editor.org/rfc/rfc1521
// https://www.rfc-editor.org/rfc/rfc2183 (https://www.rfc-editor.org/rfc/rfc2231.html, https://www.rfc-editor.org/rfc/rfc2047)
func ParseMediaType(v string) (mediatype string, params map[string]string, err error) {
	base, _, _ := strings.Cut(v, ";") // remove any existing parameters and leave just the media type and subtype
	mediatype = strings.TrimSpace(strings.ToLower(base))

	// validates that the given media type consists only of
	// token chars as per RFC 1521 and 2045 and can be
	// optionally followed by a subtype separated from the type
	// by a forward slash "/".
	err = checkMediaTypeDisposition(mediatype)
	if err != nil {
		return "", nil, err
	}

	params = make(map[string]string) // media type params

	// Map of base parameter name -> parameter name -> value
	// for parameters containing a '*' character.
	// Lazily initialized.
	//
	// RFC 2184 parts 3 through 5 (https://www.rfc-editor.org/rfc/rfc2231.html).
	var continuation map[string]map[string]string

	// Existing MIME mechanisms provide for named media type
	// (content-type field) parameters as well as named disposition
	// (content-disposition field). A MIME media type may specify any
	// number of parameters associated with all of its subtypes, and any
	// specific subtype may specify additional parameters for its own use. A
	// MIME disposition value may specify any number of associated
	// parameters, the most important of which is probably the attachment
	// disposition's filename parameter.
	v = v[len(base):] // get only the parameters
	for len(v) > 0 {
		v = strings.TrimLeftFunc(v, unicode.IsSpace) // trim any whitespace after last parameter or content-type/content-disposition value
		if len(v) == 0 {
			// no more parameters to parse
			break
		}
		key, value, rest := consumeMediaParam(v) // consumes parameter key, which should be a token and parameter value, which should be a token or a quoted string
		if key == "" {
			if strings.TrimSpace(rest) == ";" {
				// Ignore trailing semicolons.
				// Not an error.
				break
			}
			// Parse error.
			return mediatype, nil, ErrInvalidMediaParameter
		}

		// If NOT one of the cases listed bellow within the `if`
		// block, will remain an alias to `params`.
		//
		// If one of the cases listed bellow within the `if`
		// block, will be the second-level map assoicated with:
		//   - parameter value continuations, in which case this second-level map
		//     will hold multiple key-value pairs of the form:
		//         {
		//           "URL*0":`"ftp://"`,
		//           "URL*1":`"cs.utk.edu/pub/moore/bulk-mailer/bulk-mailer.tar"`,
		//         }
		//
		//   - parameter value character set and language information, in which
		//     case this second-level map will contain a single key-value pair of
		//     the form:
		//         {
		//             "title*": "us-ascii'en-us'This%20is%20%2A%2A%2Afun%2A%2A%2A"
		//         }
		//
		//   -  parameter value continuations and parameter value character set and
		//      language information combined, in which case this second-level map
		//      will hold multiple key-value pairs of the form:
		//         {
		//             "title*1*": "us-ascii'en'This%20is%20even%20more%20"
		//             "title*2*": "%2A%2A%2Afun%2A%2A%2A%20"
		//             "title*3": `"isn't it!"`
		//         }
		pmap := params
		if baseName, _, ok := strings.Cut(key, "*"); ok {
			// Parameter key containing a "*" (is not a `tspecial`, but a token),
			// could mean the following:
			//   - A Parameter Value Continuation:
			//         Long MIME media type or disposition parameter values do not interact well with header line wrapping conventions.
			//         In particular, proper header line wrapping depends on there being places where linear whitespace (LWSP) is allowed,
			//         which may or may not be present in a parameter value, and even if present may not be recognizable as such since specific
			//         knowledge of parameter value syntax may not be available to the agent doing the line wrapping. The result is that long parameter
			//         values may end up getting truncated or otherwise damaged by incorrect line wrapping implementations.
			//
			//         A mechanism is therefore needed to break up parameter values into smaller units that are amenable to line wrapping. Any such mechanism
			//         MUST be compatible with existing MIME processors. This means that
			//            (1) the mechanism MUST NOT change the syntax of MIME media type and disposition lines, and
			//
			//            (2) the mechanism MUST NOT depend on parameter ordering since MIME states that parameters are not order sensitive.
			//                Note that while MIME does prohibit modification of MIME headers during transport, it is still possible that parameters
			//                will be reordered when user agent level processing is done.
			//
			//         The obvious solution, then, is to use multiple parameters to contain a single parameter value and to use some kind of distinguished name
			//         to indicate when this is being done. And this obvious solution is exactly what is specified here: The asterisk character ("*") followed
			//         by a decimal count is employed to indicate that multiple parameters are being used to encapsulate a single parameter value. The count
			//         starts at 0 and increments by 1 for each subsequent section of the parameter value.  Decimal values are used and neither leading zeroes
			//         nor gaps in the sequence are allowed.
			//
			//         The original parameter value is recovered by concatenating the various sections of the parameter, in order. For example, the content-type field
			//             Content-Type: message/external-body; access-type=URL;
			//              URL*0="ftp://";
			//              URL*1="cs.utk.edu/pub/moore/bulk-mailer/bulk-mailer.tar"
			//
			//         is semantically identical to
			//
			//              Content-Type: message/external-body; access-type=URL;
			//              URL="ftp://cs.utk.edu/pub/moore/bulk-mailer/bulk-mailer.tar"
			//
			//         Note that quotes around parameter values are part of the value syntax; they are NOT part of the value itself. Furthermore, it is explicitly permitted
			//         to have a mixture of quoted and unquoted continuation fields.
			//
			//
			//   - Parameter Value Character Set and Language Information:
			//         Some parameter values may need to be qualified with character set or language information. It is clear that a distinguished parameter name is needed
			//         to identify when this information is present along with a specific syntax for the information in the value itself. In addition, a lightweight encoding
			//         mechanism is needed to accomodate 8 bit information in parameter values.
			//
			//         Asterisks ("*") are reused to provide the indicator that language and character set information is present and encoding is being used. A single quote ("'")
			//         is used to delimit the character set and language information at the beginning of the parameter value. Percent signs ("%") are used as the encoding flag.
			//
			//         Specifically, an asterisk at the end of a parameter name acts as an indicator that character set and language information may appear at the beginning of
			//         the parameter value. A single quote is used to separate the character set, language, and actual value information in the parameter value string, and a percent
			//         sign is used to flag octets encoded in hexadecimal. For example:
			//			   Content-Type: application/x-stuff;
			//               title*=us-ascii'en-us'This%20is%20%2A%2A%2Afun%2A%2A%2A
			//
			//         Note that it is perfectly permissible to leave either the character set or language field blank. Note also that the single quote delimiters MUST be present even
			//         when one of the field values is omitted.  This is done when either character set, language, or both are not relevant to the parameter value at hand.  This MUST NOT
			//         be done in order to indicate a default character set or language -- parameter field definitions MUST NOT assign a default character set or lanugage.
			//
			//
			//   - Combining Character Set, Language, and Parameter Continuations:
			//         Character set and language information may be combined with the parameter continuation mechanism. For example:
			//             Content-Type: application/x-stuff
			//              title*1*=us-ascii'en'This%20is%20even%20more%20
			//              title*2*=%2A%2A%2Afun%2A%2A%2A%20
			//              title*3="isn't it!"
			//
			//         Note that:
			//            (1) Language and character set information only appear at the beginning of a given parameter value.
			//
			//            (2) Continuations do not provide a facility for using more than one character set or language in the same parameter value.
			//
			//            (3) A value presented using multiple continuations may contain a mixture of encoded and unencoded segments.
			//
			//            (4) The first segment of a continuation MUST be encoded if language and character set information are given.
			//
			//            (5) If the first segment of a continued parameter value is encoded the language and character set field delimiters MUST be present even
			//                when the fields are left blank.
			if continuation == nil {
				continuation = make(map[string]map[string]string) // first time encountered one of the above cases, lazily allocate
			}
			var ok bool
			if pmap, ok = continuation[baseName]; !ok {
				// we've encountered the first parameter value continuation or
				// a single parameter value character set and language information
				// specification (see above)
				continuation[baseName] = make(map[string]string)
				pmap = continuation[baseName]
			}
		}
		if v, exists := pmap[key]; exists && v != value {
			// Duplicate parameter names are incorrect, but we allow them if their values are equal.
			return "", nil, errors.New("mime: duplicate parameter name")
		}
		pmap[key] = value
		v = rest
	}

	// Stitch together any continuations or things with stars
	// (i.e. RFC 2231 things with stars: "foo*0" or "foo*").
	// For more details check out the above explanation.
	var buf strings.Builder
	for key, pieceMap := range continuation { // for every base key name
		singlePartKey := key + "*"
		if v, ok := pieceMap[singlePartKey]; ok {
			// we've got a parameter value with character set and language information
			if decv, ok := decode2231Enc(v); ok {
				// at least character set information is present in parameter value
				// and is specified as ascii or utf-8, if there're any percent-hex
				// encoded bytes in the parameter value, the encoding should be valid,
				// the string retured is percent-hex unescaped
				params[key] = decv
			}
			continue
		}

		buf.Reset() // reset for a new set of parameter value continuations
		valid := false
		for n := 0; ; n++ {
			simplePart := fmt.Sprintf("%s*%d", key, n)
			if v, ok := pieceMap[simplePart]; ok {
				// an unencoded parameter value continuation part
				// without character set and language information
				valid = true // if at least one part is valid, event if the other's are not, still write it to the `params` map at the end
				buf.WriteString(v)
				continue
			}
			encodedPart := simplePart + "*"
			v, ok := pieceMap[encodedPart]
			if !ok {
				// not found unencoded continuation part above,
				// but doesn't look like a part with character
				// set and language information, so stop parsing
				// and write to the `params` map anything that
				// is parsed up till now
				break
			}
			valid = true
			if n == 0 {
				// we've got a parameter value continuation part with character set and language information if it's the 0th part
				// BUG: Doesn't validate the following rule: The first segment of a continuation MUST be encoded if language and
				// character set information are given
				if decv, ok := decode2231Enc(v); ok {
					// at least character set information is present in parameter value
					// and is specified as ascii or utf-8, if there're any percent-hex
					// encoded bytes in the parameter value, the encoding should be valid,
					// the string retured is percent-hex unescaped
					buf.WriteString(decv)
				}
			} else {
				// or just percent-hex encoded part (character set and language information was specified in the 0th part)
				decv, _ := percentHexUnescape(v)
				buf.WriteString(decv)
			}
		}
		if valid {
			params[key] = buf.String() // store the stitched together paramater value continuations as a single value
		}
	}

	// return `mediatype`, `params`, `err`
	return
}

// Parameter Value Character Set and Language Information:
//
// Some parameter values may need to be qualified with character set or language information. It is clear that a distinguished parameter name is needed
// to identify when this information is present along with a specific syntax for the information in the value itself. In addition, a lightweight encoding
// mechanism is needed to accomodate 8 bit information in parameter values.
//
// Asterisks ("*") are reused to provide the indicator that language and character set information is present and encoding is being used. A single quote ("'")
// is used to delimit the character set and language information at the beginning of the parameter value. Percent signs ("%") are used as the encoding flag.
//
// Specifically, an asterisk at the end of a parameter name acts as an indicator that character set and language information may appear at the beginning of
// the parameter value. A single quote is used to separate the character set, language, and actual value information in the parameter value string, and a percent
// sign is used to flag octets encoded in hexadecimal. For example:
//
//	Content-Type: application/x-stuff;
//	  title*=us-ascii'en-us'This%20is%20%2A%2A%2Afun%2A%2A%2A
//
// Note that it is perfectly permissible to leave either the character set or language field blank. Note also that the single quote delimiters MUST be present even
// when one of the field values is omitted. This is done when either character set, language, or both are not relevant to the parameter value at hand.  This MUST NOT
// be done in order to indicate a default character set or language -- parameter field definitions MUST NOT assign a default character set or lanugage.
//
// https://www.rfc-editor.org/rfc/rfc2231.html
func decode2231Enc(v string) (string, bool) {
	sv := strings.SplitN(v, "'", 3) // get slice containing character set, language and paramater value
	if len(sv) != 3 {
		// Note that it is perfectly permissible to leave either
		// the character set or language field blank. Note also
		// that the single quote delimiters MUST be present even
		// when one of the field values is omitted.
		return "", false
	}
	// TODO: ignoring lang in sv[1] for now. If anybody needs it we'll
	// need to decide how to expose it in the API. But I'm not sure
	// anybody uses it in practice.
	charset := strings.ToLower(sv[0])
	if len(charset) == 0 {
		// ignores case when only the language information is present,
		// may be a BUG, because still should parse out the parameter value?
		return "", false
	}
	if charset != "us-ascii" && charset != "utf-8" {
		// TODO: unsupported encoding
		return "", false
	}

	encv, err := percentHexUnescape(sv[2]) // counts, validates any percent-hex escaped bytes and unescapes them if neccessary
	if err != nil {
		return "", false
	}

	return encv, true
}

// isNotTokenChar reports whether a rune is
// a control character, space or delete from the ASCII table,
// a tspecial from the ASCII table or not an ASCII character at all.
//
// https://www.rfc-editor.org/rfc/rfc1521#section-4
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
//
// tspecials :=  "(" / ")" / "<" / ">" / "@" /
// ______________"," / ";" / ":" / "\" / <">
// ______________"/" / "[" / "]" / "?" / "="
// ______________; Must be in quoted-string,
// ______________; to use within parameter values
func isNotTokenChar(r rune) bool {
	return !isTokenChar(r)
}

// consumeToken consumes a token from the beginning of provided
// string, per RFC 2045 section 5.1 (referenced from 2183), and returns
// the token consumed and the rest of the string. Returns ("", v) on
// failure to consume at least one character.
//
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
// https://www.rfc-editor.org/rfc/rfc2183 (section 2)
func consumeToken(v string) (toke, rest string) {
	notPos := strings.IndexFunc(v, isNotTokenChar)
	if notPos == -1 {
		// non-token not found, means that the string consists entirely of token chars, so return the original string as the token
		return v, ""
	}
	if notPos == 0 {
		// string begins with a non-token char, so return empty string as the token
		return "", v
	}

	return v[0:notPos], v[notPos:] // split string by first non-token char
}

// consumeValue consumes a "value" per RFC 2045, where a value is
// either a 'token' or a 'quoted-string'.  On success, consumeValue
// returns the value consumed (and de-quoted/escaped, if a
// quoted-string) and the rest of the string. On failure, returns
// ("", v).
//
// https://www.rfc-editor.org/rfc/rfc2045#section-5.1
// https://www.rfc-editor.org/rfc/rfc2183 (section 2)
func consumeValue(v string) (value, rest string) {
	if v == "" {
		return
	}

	if v[0] != '"' {
		// not quoted-string, thus should be a token
		return consumeToken(v)
	}

	// parse a quoted-string
	buffer := new(strings.Builder)
	for i := 1; i < len(v); i++ { // `i := 1` to skip the first quote character '"'
		r := v[i]
		if r == '"' {
			// encountered a '"", end of quoted string
			return buffer.String(), v[i+1:] // `v[i+1:]` discards the quote from `rest`
		}
		// When MSIE (Mircosoft Internet Explorer) (deprecated) sends a full
		// file path (in "intranet mode"), it does not escape backslashes:
		// "C:\dev\go\foo.txt", not "C:\\dev\\go\\foo.txt".
		//
		// No known MIME generators emit unnecessary backslash escapes
		// for simple token characters like numbers and letters.
		//
		// If we see an unnecessary backslash escape, assume it is from MSIE
		// and intended as a literal backslash. This makes Go servers deal better
		// with MSIE without affecting the way they handle conforming MIME
		// generators.
		if r == '\\' && i+1 < len(v) && isTSpecial(rune(v[i+1])) { // `isTSpecial(rune(v[i+1]))` assumes next character is a backslash
			buffer.WriteByte(v[i+1]) // unescape backslash
			i++
			continue
		}
		if r == '\r' || r == '\n' {
			// there should be no CR, LF or CRLF in parameter values
			return "", v
		}
		buffer.WriteByte(v[i])
	}
	// Did not find end quote.
	return "", v
}

func consumeMediaParam(v string) (param, value, rest string) {
	rest = strings.TrimLeftFunc(v, unicode.IsSpace) // trim any whitespace after last parameter or content-type/content-disposition value
	if !strings.HasPrefix(rest, ";") {
		// parameters should be separated
		// from content-type/content-disposition
		// and between each other by semicolons (";")
		return "", "", rest
	}

	rest = rest[1:]                                    // consume semicolon
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace) // trim any whitespace after the semicolon separator and before the actual paramater
	param, rest = consumeToken(rest)                   // consume parameter name up to "=", which is not a token char, but a tspecial
	param = strings.ToLower(param)
	if param == "" {
		return "", "", v
	}

	rest = strings.TrimLeftFunc(rest, unicode.IsSpace) // trim any whitespace after the parameter name and before "="
	if !strings.HasPrefix(rest, "=") {
		// parameter name and value should
		// be separated by "="
		return "", "", v
	}
	rest = rest[1:]                                    // consume equals sign
	rest = strings.TrimLeftFunc(rest, unicode.IsSpace) // trim any whitespace after "=" and before the parameter value
	value, rest2 := consumeValue(rest)                 // consume a value being a token- or a quoted- string, in which case it's de-quoted and can consist of non-token characters
	if value == "" && rest2 == rest {
		// need to compare rest2 against rest, because
		// the value could be an empty string after de-quotation,
		// in which case it should be returned bellow
		return "", "", v
	}
	rest = rest2
	return param, value, rest
}

func percentHexUnescape(s string) (string, error) {
	// Count %, check that they're well-formed (followed by hexadecimal bytes).
	percents := 0
	for i := 0; i < len(s); {
		if s[i] != '%' {
			// got unencoded character
			i++
			continue
		}
		// possibly a percent hex encoded
		percents++
		if i+2 >= len(s) || !ishex(s[i+1]) || !ishex(s[i+2]) {
			// not enough characters left in string, need at least 2 to represent a single byte in hex,
			// or at least one character of the 2 is not in the hex character ranges (0-9, a-z, a-Z)
			s = s[i:]
			if len(s) > 3 {
				s = s[0:3] // to add malformed hex byte to error message
			}
			return "", fmt.Errorf("mime: bogus characters after %%: %q", s)
		}
		i += 3 // skip percent and 2 hex characters
	}
	if percents == 0 {
		// no characters were percent-hex escaped, return original string
		return s, nil
	}

	t := make([]byte, len(s)-2*percents) // 3 characters of percent-hex encoding will become 1 byte, that's why `len(s)-2*percents`
	j := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case '%':
			// got pecent-hex encoded byte
			t[j] = unhex(s[i+1])<<4 | unhex(s[i+2])
			j++    // one byte written
			i += 3 // 3 bytes read
		default:
			t[j] = s[i]
			j++
			i++
		}
	}

	return string(t), nil
}

func ishex(c byte) bool {
	switch {
	case '0' <= c && c <= '9':
		return true
	case 'a' <= c && c <= 'f':
		return true
	case 'A' <= c && c <= 'F':
		return true
	}
	return false
}

// unhex single hex nibble
func unhex(c byte) byte {
	switch {
	case '0' <= c && c <= '9':
		return c - '0'
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}
