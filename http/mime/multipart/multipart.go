package multipart

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/pianoyeg94/go-http/mime"
	"github.com/pianoyeg94/go-http/mime/quotedprintable"
	"github.com/pianoyeg94/go-http/textproto"
)

// emptyParams is used as default Content-Disposition
// parameters if there's was an error parsing the original
// Content-Disposition parameters.
var emptyParams = make(map[string]string)

// https://www.rfc-editor.org/rfc/rfc7578#section-4.1
// https://www.rfc-editor.org/rfc/rfc2046#section-5.1

// https://www.rfc-editor.org/rfc/rfc2183
// https://www.rfc-editor.org/rfc/rfc2184
// https://www.w3.org/Protocols/rfc1341/5_Content-Transfer-Encoding.html

// tspecials :=  "(" / ")" / "<" / ">" / "@" /
// "," / ";" / ":" / "\" / <">
// "/" / "[" / "]" / "?" / "="
// ; Must be in quoted-string,
// ; to use within parameter values

// The only mandatory global parameter for the "multipart" media type is
// the boundary parameter, which consists of 1 to 70 characters from a
// set of characters known to be very robust through mail gateways, and
// NOT ending with white space.
// That's why this constant needs to be at least 76 for this package to work correctly.
// This is because \r\n--separator_of_len_70-- would fill the buffer and it
// wouldn't be safe to consume a single byte from it.
const peekBufferSize = 4096

// A Part represents a single part in a multipart body.
type Part struct {
	// The headers of the body, if any, with the keys canonicalized
	// in the same fashion that the Go http.Request headers are.
	// For example, "foo-bar" changes case to "Foo-Bar"
	Header textproto.MIMEHeader

	mr *Reader // multipart reader

	disposition       string            // possible Content-Disposition header value
	dispositionParams map[string]string // possible Content-Disposition header value parameters

	// r is either a reader directly reading from mr, or it's a
	// wrapper around such a reader, decoding the Content-Transfer-Encoding
	//
	// Either is a partReader which reads bytes directly reading from mr,
	// or if there's a Content-Transfer-Encoding header in the part and
	// it's value is "quoted-printable" then the partReader will be wrapped
	// in a *quotedprintable.Reader
	r io.Reader

	n       int   // known data bytes, which are part of the body, waiting in mr.bufReader
	total   int64 // total data bytes read already
	err     error // error to return when n == 0
	readErr error // read error observed from mr.bufReader
}

// FormName returns the name parameter if p has a Content-Disposition
// of type "form-data".  Otherwise it returns an empty string.
func (p *Part) FormName() string {
	// See https://tools.ietf.org/html/rfc2183 section 2 for EBNF
	// of Content-Disposition value format.
	//
	// In the extended BNF notation of [RFC 822], the Content-Disposition
	// header field is defined as follows:
	//
	//    disposition := "Content-Disposition" ":"
	// 				  disposition-type
	// 				  *(";" disposition-parm)
	//
	//    disposition-type := "inline"
	// 					 / "attachment"
	// 					 / extension-token
	// 					 ; values are not case-sensitive
	//
	//    disposition-parm := filename-parm
	// 					 / creation-date-parm
	// 					 / modification-date-parm
	// 					 / read-date-parm
	// 					 / size-parm
	// 					 / parameter
	//
	//    filename-parm := "filename" "=" value
	//
	//    creation-date-parm := "creation-date" "=" quoted-date-time
	//
	//    modification-date-parm := "modification-date" "=" quoted-date-time
	//
	//    read-date-parm := "read-date" "=" quoted-date-time
	//
	//    size-parm := "size" "=" 1*DIGIT
	//
	//	  quoted-date-time := quoted-string
	//					   ; contents MUST be an RFC 822 `date-time'
	//					   ; numeric timezones (+HHMM or -HHMM) MUST be used
	if p.dispositionParams == nil {
		// Parses Content-Disposition header field.
		//
		// Via ParseMediaType parses a media type value and any optional
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
		// 1. Validates that the given content disposition value consists
		//    only of token chars.
		//
		// 2. Consumes every content disposition parameter name-value pair
		//    (if any). Parameter names should only consist of token chars.
		//    Parameter values should only consist of token chars or should
		//    be quoted strings.
		//
		// 3. Stiches parameter values together if necessary.
		//
		//    Long MIME media type or disposition parameter values do not interact well with header line wrapping conventions.
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
		// 4. Decodes paramater values with character set and language information into UTF8 encoding, percent-hex decoding if necessary.
		//
		//    Parameter Value Character Set and Language Information:
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
		// 5. Returns content disposition string, paramater map and any error occured during decoding
		p.parseContentDisposition()
	}

	// In a regular HTTP response, the Content-Disposition response header is a header indicating if the content is expected to be displayed inline
	// in the browser, that is, as a Web page or as part of a Web page, or as an attachment, that is downloaded and saved locally.
	//
	// In a multipart/form-data body, the HTTP Content-Disposition general header is a header that must be used on each subpart of a multipart body
	// to give information about the field it applies to. The subpart is delimited by the boundary defined in the Content-Type header. Used on the
	// body itself, Content-Disposition has no effect.
	//
	// The Content-Disposition header is defined in the larger context of MIME messages for email, but only a subset of the possible parameters apply
	// to HTTP forms and POST requests. Only the value form-data, as well as the optional directive name and filename, can be used in the HTTP context.
	//
	// As a response header for the main body
	// ---------------------------------------------
	// The first parameter in the HTTP context is either inline (default value, indicating it can be displayed inside the Web page, or as the Web page)
	// or attachment (indicating it should be downloaded; most browsers presenting a 'Save as' dialog, prefilled with the value of the filename parameters
	// if present).
	//
	// As a header for a multipart body
	// --------------------------------
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition
	if p.disposition != "form-data" {
		return ""
	}
	return p.dispositionParams["name"]
}

// FileName returns the filename parameter of the Part's Content-Disposition
// header. If not empty, the filename is passed through filepath.Base (which is
// platform dependent) before being returned.
func (p *Part) FileName() string {
	if p.dispositionParams == nil {
		// Parses Content-Disposition header field.
		//
		// Via ParseMediaType parses a media type value and any optional
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
		// 1. Validates that the given content disposition value consists
		//    only of token chars.
		//
		// 2. Consumes every content disposition parameter name-value pair
		//    (if any). Parameter names should only consist of token chars.
		//    Parameter values should only consist of token chars or should
		//    be quoted strings.
		//
		// 3. Stiches parameter values together if necessary.
		//
		//    Long MIME media type or disposition parameter values do not interact well with header line wrapping conventions.
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
		// 4. Decodes paramater values with character set and language information into UTF8 encoding, percent-hex decoding if necessary.
		//
		//    Parameter Value Character Set and Language Information:
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
		// 5. Returns content disposition string, paramater map and any error occured during decoding
		p.parseContentDisposition()
	}
	filename := p.dispositionParams["filename"]
	if filename == "" {
		return ""
	}

	// RFC 7578, Section 4.2 requires that if a filename is provided, the
	// directory path information must not be used.
	//
	// Base returns the last element of path.
	// Trailing path separators are removed before extracting the last element.
	// If the path is empty, Base returns ".".
	// If the path consists entirely of separators, Base returns a single separator.
	return filepath.Base(filename)
}

// Two common ways of presenting multipart electronic messages are as a
// main document with a list of separate attachments, and as a single
// document with the various parts expanded (displayed) inline.
//
// A mechanism is needed to allow the sender to transmit this sort of
// presentational information to the recipient; the Content-Disposition
// header provides this mechanism, allowing each component of a message
// to be tagged with an indication of its desired presentation semantics.
//
//	disposition := "Content-Disposition" ":"
//				    disposition-type
//				    *(";" disposition-parm)
//
//	disposition-type := "inline"
//					  / "attachment"
//					  / extension-token
//					  ; values are not case-sensitive
//
//	disposition-parm := filename-parm
//					  / creation-date-parm
//					  / modification-date-parm
//					  / read-date-parm
//					  / size-parm
//					  / parameter
func (p *Part) parseContentDisposition() {
	v := p.Header.Get("Content-Disposition")
	var err error
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
	// 1. Validates that the given content disposition value consists
	//    only of token chars.
	//
	// 2. Consumes every content disposition parameter name-value pair
	//    (if any). Parameter names should only consist of token chars.
	//    Parameter values should only consist of token chars or should
	//    be quoted strings.
	//
	// 3. Stiches parameter values together if necessary.
	//
	//    Long MIME media type or disposition parameter values do not interact well with header line wrapping conventions.
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
	// 4. Decodes paramater values with character set and language information into UTF8 encoding, percent-hex decoding if necessary.
	//
	//    Parameter Value Character Set and Language Information:
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
	// 5. Returns content disposition string, paramater map and any error occured during decoding
	p.disposition, p.dispositionParams, err = mime.ParseMediaType(v)
	if err != nil {
		p.dispositionParams = emptyParams
	}
}

// NewReader creates a new multipart buffered Reader reading from r using the
// given MIME boundary.
//
// The Content-Type field for multipart entities requires one parameter,
// "boundary". The boundary delimiter line is then defined as a line
// consisting entirely of two hyphen characters ("-", decimal value 45)
// followed by the boundary parameter value from the Content-Type header
// field, optional linear whitespace, and a terminating CRLF.
//
// The boundary is obtained from the "boundary" parameter of
// the message's "Content-Type" header. Use mime.ParseMediaType to
// parse such headers.
//
// The grammar for parameters on the Content-type field is such that
// it is often necessary to enclose the boundary
// parameter values in quotes on the Content-type line.  This is not
// always necessary, but never hurts.
//
// The boundary delimiter MUST NOT appear inside any of the encapsulated parts.
//
// The boundary delimiter MUST occur at the beginning of a line, i.e.,
// following a CRLF, and the initial CRLF is considered to be attached
// to the boundary delimiter line rather than part of the preceding
// part.
//
// The boundary may be followed by zero or more characters of
// linear whitespace. It is then terminated by either another CRLF and
// the header fields for the next part, or by two CRLFs, in which case
// there are no header fields for the next part.
//
// NOTE:  The CRLF preceding the boundary delimiter line is conceptually
// attached to the boundary so that it is possible to have a part that
// does not end with a CRLF (line break). Body parts that must be
// considered to end with line breaks, therefore, must have two CRLFs
// preceding the boundary delimiter line, the first of which is part of
// the preceding body part, and the second of which is part of the
// encapsulation boundary.
//
// Boundary delimiters must not appear within the encapsulated material,
// and must be no longer than 70 characters, not counting the two
// leading hyphens.
//
// The boundary delimiter line following the last body part is a
// distinguished delimiter that indicates that no further body parts
// will follow.  Such a delimiter line is identical to the previous
// delimiter lines, with the addition of two more hyphens after the
// boundary parameter value.
// --gc0pJq0M:08jU534c0p--
func NewReader(r io.Reader, boundary string) *Reader {
	b := []byte("\r\n--" + boundary + "--") // final boundary
	return &Reader{
		// never calls Read on underlying Reader once an error has been seen,
		// the encountered error is returned on all future reads
		bufReader:        bufio.NewReaderSize(&stickyErrorReader{r: r}, peekBufferSize),
		nl:               b[:2],           // newline before the boundary
		nlDashBoundary:   b[:len(b)-2],    // boundary between parts
		dashBoundaryDash: b[2:],           // final boundary without newline prepended
		dashBoundary:     b[2 : len(b)-2], // boundary between parts without newline prepended
	}
}

// stickyErrorReader is an io.Reader which never calls Read on its
// underlying Reader once an error has been seen. (the io.Reader
// interface's contract promises nothing about the return values of
// Read calls after an error, yet this package does do multiple Reads
// after error)
type stickyErrorReader struct {
	r   io.Reader
	err error
}

func (r *stickyErrorReader) Read(p []byte) (n int, _ error) {
	if r.err != nil {
		return 0, r.err
	}
	n, r.err = r.r.Read(p)
	return n, r.err
}

func newPart(mr *Reader, rawPart bool) (*Part, error) {
	bp := &Part{
		Header: make(map[string][]string),
		mr:     mr,
	}
	// headers are read into the underlying .mr *Reader's buffer
	if err := bp.populateHeaders(); err != nil { // if no error, overwrites bp.Header with a populated MIMEHeader map
		return nil, err
	}
	bp.r = partReader{bp}

	// rawPart is used to switch between Part.NextPart and Part.NextRawPart, which always reads the raw body bytes
	if !rawPart {
		const cte = "Content-Transfer-Encoding"
		// by default Part.NextPart, if the "Content-Transfer-Encoding"
		// of the next part is "quoted-printable", wraps the part reader
		// into a reader, which decodes the "quoted-printable" content
		// into raw bytes
		if strings.EqualFold(bp.Header.Get(cte), "quoted-printable") {
			bp.Header.Del(cte)
			bp.r = quotedprintable.NewReader(bp.r)
		}
	}

	return bp, nil
}

func (p *Part) populateHeaders() error {
	r := textproto.NewReader(p.mr.bufReader)

	// 1) Validates that the first header line doesn't start with a leading space.
	//    If it does, returns an empty MIMEHeader map and a Protocol error
	//    containing the malformed line as a string.
	//    Other errors (not Protocol errors) can come only from the underlying buffer's reader, usually network.
	//    An error may occure in between reading header lines, then MIMEHeader map will be partially populated
	//    and the error will be also returned.
	//
	// 2) Reads in in all the headers, validates, canonicalizes them and puts them into the MIMEHeader map:
	//    	- Turns multiline header values into values separated by spaces.
	//
	//      - Canonicalization converts the first letter and any letter following a hyphen to upper case;
	//        the rest are converted to lowercase:
	// 				Allowed characters in a header field name are:
	// 				  - "a" to "z"
	// 				  - "A" to "Z"
	// 				  - "0" to "9"
	// 				  - "!", "#", "$", "%", "&", "'", "*", "+", "-", ".", "^", "_", "`", "|", "~"
	//
	// 				1. If there's an invalid header character in the key, then the key is not canonicalized
	// 				   and is returned as is converted to a string, `ok` will be false. This will copy bytes to the string.
	//
	// 				2. If there's a space in the key or before the colon separating the key from the value, then the key
	// 				   is not canonicalized and is returned as is converted to a string, but `ok` will be true,
	// 				   to disallow request smuggling by normalizing invalid headers. This will copy bytes to the string.
	//
	// 				3. Canonicalizes the header key and if the key is in the commonHeader map, avoids allocation by
	// 				   interning the string from the commonHeader map.
	//
	//      - Validates characters in the header values:
	// 				RFC 7230 says:
	//
	//						field-content  = field-vchar [ 1*( SP / HTAB ) field-vchar ] = spaces or tabs before header value is allowed
	// 				                                                                    (just after the colon, separating the field name and value)
	//						field-vchar    = VCHAR / obs-text
	//						obs-text       = %x80-FF = A recipient SHOULD treat other octets in field content (obs-text) as opaque data.
	//					                              (Extended ASCII table from https://www.rapidtables.com/code/text/ascii-table.html)
	//
	// 				RFC 5234 says:
	//
	//					HTAB           =  %x09 = from ASCII table
	//					SP             =  %x20 = from ASCII table
	//					VCHAR          =  %x21-7E = ASCII characters 33 to 126
	//
	//      - Throws away spaces after the colon.
	//
	//      - Combines same header keys with different values into key to a slice of values within the MIMEHeader map.
	//        Mostly the slice will consist of a single value, since multi-valued headers are rare.
	header, err := r.ReadMIMEHeader()
	if err == nil {
		p.Header = header
	}
	return err
}

// partReader implements io.Reader by reading raw bytes directly from the
// wrapped *Part, without doing any Transfer-Encoding decoding.
type partReader struct {
	p *Part
}

// Read reads the body of a part, after its headers and before the next part (if any) begins.
//
// Read cases:
//
//  1. The buffered reader's underlying buffer already contains some body bytes but no multipart boundary,
//     and caller provided buffer can accomodate all of those bytes - just copy all of the body bytes into
//     caller's buffer and return the number of body bytes copied and no error.
//
//  2. The buffered reader's underlying buffer already contains some body bytes but no multipart boundary
//     and caller provided buffer CANNOT accomodate all of those bytes - just copy the number of body bytes
//     that fit into caller's buffer and return the number of body bytes copied and no error.
//
//  3. The buffered reader's underlying buffer already contains all of the or remainining body bytes and a multipart
//     boundary, followed by the next part's octets (if not final boundary), copy the body bytes up to the bounary
//     into the caller's buffer and return the number of body bytes copied and io.EOF.
//
//  4. This is the first read of the part and the buffered reader's underlying buffer contains some octets, but the buffer
//     content starts with a multipart boundary (actual, not accidental), just return 0 and io.EOF, signifing that the current
//     part's body is empty.
//
//  5. The buffered reader's underlying buffer already contains some, remaining or all of the body octets and those
//     octets by accident include or end with a byte pattern matching the multipart boundary, copy the body bytes up to
//     and including the accidental multipart boundary, but not including the body bytes after the boundary (they will be
//     returned on the next call to Read), return the number of body bytes copied and no error.
//
//  6. The buffered reader's underlying buffer doesn't contain any body bytes, read in as much body bytes
//     as a single call to the stream reader returnes in a single read. If no error occured, then follow the
//     steps describe in cases 1 through 5.
//
//  7. The buffered reader's underlying buffer already contains some, remaining or all of the body octets and those
//     octets by accident end with a byte pattern strictly matching a multipart boundary, since we don't know if it's
//     an accidental boundary or not, we need more data buffered for that, follow the steps described in case 6.
func (pr partReader) Read(d []byte) (int, error) {
	p := pr.p            // *Part that owns this partReader
	br := p.mr.bufReader // multipart *Reader's underlying buffered reader (bufio.Reader), which wraps the original raw stream reader

	// Read into buffer until we identify some data to return,
	// or we find a reason to stop (boundary or read error).
	// Check out scanUntilBoundary for detailed behavior description,
	// when we encounter a multipart boundary in the underlying read buffer.
	for p.n == 0 && p.err == nil {
		peek, _ := br.Peek(br.Buffered()) // get reference to bytes in buffer
		// p.err will be either io.EOF, if we read a part boundary into the buffer
		// (there's a next part or this is the final part),
		// or p.readErr if we read from the underlying stream into buffered reader's buffer
		// because there was not enough data and got an error from the underlying stream
		p.n, p.err = scanUntilBoundary(peek, p.mr.dashBoundary, p.mr.nlDashBoundary, p.total, p.readErr)
		if p.n == 0 && p.err == nil {
			// Force buffered I/O to read more into buffer.
			// The buffer ends with a multipart boundary and we need some more data to make the decision -
			// we have an empty part body, do we have the next part in our buffer, this is the last part
			// or is there an accidental match within the current part's body with the multipart boundary:
			//     We may have some body content of the current part in the buffer, which by accident partially
			//     matches with the multipart boundary (this content begins with the boundary, but it's continued
			//     right away with some arbitrary sequence of bytes without any spaces, tabs, crlf and lf in between
			//     the boundary and the arbitrary sequence of bytes).
			_, p.readErr = br.Peek(len(peek) + 1) // force read from underlying raw stream reader
			if p.readErr == io.EOF {              // byte stream closed abruptly
				p.readErr = io.ErrUnexpectedEOF
			}
		}
	}

	// Read out from "data to return" part of buffer.
	if p.n == 0 { // either the part's body is empty or an error occured while reading from the underlying stream into buffered reader's buffer
		return 0, p.err
	}

	n := len(d)
	if n > p.n { // cap caller's buffer to the actual amount of body bytes read from the underlying stream into buffered reader's buffer
		n = p.n
	}

	n, _ = br.Read(d[:n]) // copy body bytes from the underlying buffered reader's buffer into caller's buffer
	p.total += int64(n)   // update total size of the part
	p.n -= n              // update the amount of data bytes already waiting in the unerlying buffer reader's buffer
	if p.n == 0 {         // no more current part bytes left in the buffered reader's underlying buffer, return (behaviour defined by io.Reader)
		return n, p.err
	}

	// caller provided buffer CANNOT accomodate the body bytes
	// read from the underlying stream into buffered reader's buffer.
	// If any error occured while reading from the underlying stream,
	// keep it until the caller provides a sufficient buffer to accomodate
	// the left over body bytes
	return n, nil
}

// scanUntilBoundary scans buf to identify how much of it can be safely
// returned as part of the Part body.
// dashBoundary is "--boundary".
// nlDashBoundary is "\r\n--boundary" or "\n--boundary", depending on what mode we are in.
// The comments below (and the name) assume "\n--boundary", but either is accepted.
// total is the number of bytes read out so far. If total == 0, then a leading "--boundary" is recognized.
// readErr is the read error, if any, that followed reading the bytes in buf.
// scanUntilBoundary returns the number of data bytes from buf that can be
// returned as part of the Part body and also the error to return (if any)
// once those data bytes are done.
func scanUntilBoundary(buf, dashBoundary, nlDashBoundary []byte, total int64, readErr error) (int, error) {
	if total == 0 { // just started reading content from the current part's body
		// At beginning of body, allow dashBoundary.
		// The part's body may be empty, we may have
		// a new part in the buffer right after the headers (if any).
		if bytes.HasPrefix(buf, dashBoundary) {
			switch matchAfterPrefix(buf, dashBoundary, readErr) {
			case -1:
				// We have some body content of the current part in the buffer,
				// which by accident partially matches our multipart boundary
				// (content begins with the boundary, but it's continued right away
				// with some arbitrary sequence of bytes without any spaces, tabs, crlf
				// and lf in between the boundary and the arbitrary sequence of bytes).
				return len(dashBoundary), nil
			case 0:
				// We have only our multipart boundary in the buffer and
				// we need some more data to make the decision - do we have a part
				// with an empty body or does the body by accident starts with our
				// multipart boundary (case described above)
				return 0, nil
			case +1:
				// We have a part boundary at the beginning of the buffer,
				// so a part with an empty body
				return 0, io.EOF
			}

		}
	}

	// Search for "\n--boundary" (beginning of the next part)
	// We have a next part in the buffer, the current part is the last part
	// or an accidental match of some content with our multipart boundary
	// withint the current part's body
	if i := bytes.Index(buf, nlDashBoundary); i >= 0 {
		switch matchAfterPrefix(buf[i:], nlDashBoundary, readErr) {
		case -1:
			// We have some body content of the current part somewhere in the read buffer,
			// which by accident partially matches our multipart boundary
			// (this content begins with the boundary, but it's continued right away
			// with some arbitrary sequence of bytes without any spaces, tabs, crlf
			// and lf in between the boundary and the arbitrary sequence of bytes).
			return i + len(nlDashBoundary), nil
		case 0:
			// The buffer ends with a multipart boundary and we need some more data to make the decision -
			// do we have the next part in our buffer, this is the last part or is there an accidental match
			// within the current part's body with the multipart boundary (case described above).
			return i, nil // tell caller we scanned data up till the accidental or actual multipart boundary
		case +1:
			// We have a part boundary in the buffer (there's a next part or this is the final part),
			// return the amount of body content from this part (i)
			return i, io.EOF
		}
	}

	if bytes.HasPrefix(nlDashBoundary, buf) { // will be always false
		return 0, readErr
	}

	// Otherwise, anything up to the final \n is not part of the boundary
	// and so must be part of the body.
	// Also if the section from the final \n onward is not a prefix of the boundary,
	// it too must be part of the body.
	i := bytes.LastIndexByte(buf, nlDashBoundary[0])        // find last \n in buffer
	if i >= 0 && bytes.HasPrefix(nlDashBoundary, buf[i:]) { // if the last \n in buffer is followed by a boundary, read only until \n
		return i, nil
	}

	// otherwise the buffer doesn't contain content from the next part,
	// buffer contains only this part's body content
	return len(buf), readErr
}

// matchAfterPrefix checks whether buf should be considered to match the boundary.
// The prefix is "--boundary" or "\r\n--boundary" or "\n--boundary",
// and the caller has verified already that bytes.HasPrefix(buf, prefix) is true.
//
// matchAfterPrefix returns +1 if the buffer does match the boundary,
// meaning the prefix is followed by a double dash, space, tab, cr, nl,
// or end of input.
// It returns -1 if the buffer definitely does NOT match the boundary,
// meaning the prefix is followed by some other character.
// For example, "--foobar" does not match "--foo".
// It returns 0 more input needs to be read to make the decision,
// meaning that len(buf) == len(prefix) and readErr == nil.
func matchAfterPrefix(buf, prefix []byte, readErr error) int {
	if len(buf) == len(prefix) {
		if readErr != nil { // may be an io.ErrUnexpectedEOF, so tell the caller that this part's body content is over
			return +1
		}
		return 0 // needs more to be read into buffer to make the decision
	}
	c := buf[len(prefix)] // get next byte after dashBoundary/nlDashBoundary

	if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
		return +1 // we have a part boundary at the beginning of the buffer
	}

	// Try to detect boundaryDash (final boundary)
	if c == '-' {
		if len(buf) == len(prefix)+1 {
			// may be an io.ErrUnexpectedEOF, so tell the caller that this part's body content is over,
			// so the boundary match is considered an accident
			if readErr != nil {
				// Prefix + "-" does not match
				return -1
			}
			return 0 // needs more to be read into buffer to make the decision
		}
		if buf[len(prefix)+1] == '-' {
			return +1 // we have a final part boundary at the beginning of the buffer
		}
	}

	// accidental boundary match (content begins with the boundary,
	// but it's continued right away with some arbitrary sequence of bytes
	// without any spaces, tabs, crlf and lf in between the boundary
	// and the arbitrary sequence of bytes).
	return -1
}

// Reader is an iterator over parts in a MIME multipart body.
// Reader's underlying parser consumes its input as needed. Seeking
// isn't supported.
type Reader struct {
	bufReader *bufio.Reader // buffering reader which reads from the network somewhere down the chain of io.Readers
	tempDir   string        // used in tests

	currentPart *Part
	partsRead   int

	nl               []byte // newline before the boundary, newline style - "\r\n" or "\n" (set after seeing first boundary line)
	nlDashBoundary   []byte // boundary between parts
	dashBoundaryDash []byte // final boundary without newline prepended
	dashBoundary     []byte // boundary between parts without newline prepended
}

func (r *Reader) nextPart(rawPart bool) (*Part, error) {
	if r.currentPart != nil {
		// r.currentPart.Close() TODO;
	}

	if string(r.dashBoundary) == "--" {
		return nil, fmt.Errorf("multipart: boundary is empty")
	}

	// The boundary delimiter MUST occur at the beginning of a line, i.e.,
	// following a CRLF, and the initial CRLF is considered to be attached
	// to the boundary delimiter line rather than part of the preceding
	// part.
	//
	// The boundary may be followed by zero or more characters of
	// linear whitespace. It is then terminated by either another CRLF and
	// the header fields for the next part, or by two CRLFs, in which case
	// there are no header fields for the next part.
	//
	// NOTE:  The CRLF preceding the boundary delimiter line is conceptually
	// attached to the boundary so that it is possible to have a part that
	// does not end with a CRLF (line break). Body parts that must be
	// considered to end with line breaks, therefore, must have two CRLFs
	// preceding the boundary delimiter line, the first of which is part of
	// the preceding body part, and the second of which is part of the
	// encapsulation boundary.

	expectedPart := false
	for {
		// reads in boundary line
		// avoids copying line from buffer, points to data inside buffer,
		// valid only until next call to read
		line, err := r.bufReader.ReadSlice('\n')
		if err == io.EOF && r.isFinalBoundary(line) {
			// If the buffer ends in "--boundary--" without the
			// trailing "\r\n", ReadSlice will return an error
			// (since it's missing the '\n'), but this is a valid
			// multipart EOF so we need to return io.EOF instead of
			// a fmt-wrapped one.
			//
			// CRLF conceptually prepended to the final boundary was
			// read in via the previous returned part and is already in
			// buffer. We've consumed it during the first iteration of
			// this loop (bytes.Equal(line, r.nl) case bellow).
			return nil, io.EOF
		}
		if err != nil {
			return nil, fmt.Errorf("multipart: NextPart: %w", err)
		}

		if r.isBoundaryDelimiterLine(line) {
			// CRLF conceptually prepended to the boundary was
			// read in via the previous returned part and is already in
			// buffer. We've consumed it during the first iteration of
			// this loop (bytes.Equal(line, r.nl) case bellow).
			r.partsRead++
			bp, err := newPart() // TODO!!!!!!
		}

	}
}

// isFinalBoundary reports whether line is the final boundary line
// indicating that all parts are over.
// It matches `^--boundary--[ \t]*(\r\n)?$`
func (r *Reader) isFinalBoundary(line []byte) bool {
	if !bytes.HasPrefix(line, r.dashBoundaryDash) {
		return false
	}

	rest := line[len(r.dashBoundaryDash):] // get only the possible ending CRLF
	rest = skipLWSPChar(rest)              // remove leading spaces and tabs before CRLF

	return len(rest) == 0 || bytes.Equal(rest, r.nl) // final boundary may or may not end with a new line
}

func (r *Reader) isBoundaryDelimiterLine(line []byte) bool {
	// https://tools.ietf.org/html/rfc2046#section-5.1
	//   The boundary delimiter line is then defined as a line
	//   consisting entirely of two hyphen characters ("-",
	//   decimal value 45) followed by the boundary parameter
	//   value from the Content-Type header field, optional linear
	//   whitespace, and a terminating CRLF.
	if !bytes.HasPrefix(line, r.dashBoundary) {
		return false
	}

	rest := line[len(r.dashBoundary):] // get only the possible ending CRLF (although CRLF is )
	rest = skipLWSPChar(rest)          // strip linear leading whitespace before CRLF

	// On the first part, see our lines are ending in \n instead of \r\n
	// and switch into that mode if so. This is a violation of the spec,
	// but occurs in practice.
	if r.partsRead == 0 && len(rest) == 1 && rest[0] == '\n' {
		r.nl = r.nl[1:]                         // get rid of leading '\r'
		r.nlDashBoundary = r.nlDashBoundary[1:] // get rid of leading '\r'
	}

	return bytes.Equal(rest, r.nl) // boundary should end with CRLF
}

// skipLWSPChar returns b with leading spaces and tabs removed.
// RFC 822 defines:
//
//	LWSP-char = SPACE / HTAB
func skipLWSPChar(b []byte) []byte {
	for len(b) > 0 && (b[0] == '\n' || b[0] == '\t') {
		b = b[1:]
	}

	return b
}
