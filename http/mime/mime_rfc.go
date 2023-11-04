package mime

// RFC2045
// ==========================================
//
//
//
// Content-Transfer-Encoding Syntax
// ------------------------------------------
// The Content-Transfer-Encoding field's value is a single token
// specifying the type of encoding, as enumerated below. Formally:
//     encoding := "Content-Transfer-Encoding" ":" mechanism
//     mechanism := "7bit" / "8bit" / "binary" / "quoted-printable" / "base64" / ietf-token / x-token
//
// These values are not case sensitive -- Base64 and BASE64 and bAsE64 are all equivalent.
// An encoding type of 7BIT (plain ASCII characters) requires that the body is already in a 7bit mail-ready
// representation. This is the default value -- that is, "Content-Transfer-Encoding: 7BIT" is assumed if the
// Content-Transfer-Encoding header field is not present.
//
//
// Content-Transfer-Encodings Semantics
// ------------------------------------------
// This single Content-Transfer-Encoding token actually provides two pieces of information. It specifies what
// sort of encoding transformation the body was subjected to and hence what decoding operation must be used to
// restore it to its original form, and it specifies what the domain of the result is.
//
// The transformation part of any Content-Transfer-Encodings specifies, either explicitly or implicitly, a single,
// well-defined decoding algorithm, which for any sequence of encoded octets either transforms it to the original
// sequence of octets which was encoded, or shows that it is illegal as an encoded sequence.
//
// Three transformations are currently defined: identity, the "quoted- printable" encoding, and the "base64" encoding.
// The domains are "binary", "8bit" and "7bit".
//
// 7bit Data
// "7bit data" refers to data that is all represented as relatively short lines with 998 octets or less between CRLF
// line separation sequences No octets with decimal values greater than 127 (beyound ASCII character) are allowed and
// neither are NULs (octets with decimal value 0).  CR (decimal value 13) and LF (decimal value 10) octets only occur
// as part of CRLF line separation sequences.
//
// 8bit Data
// "8bit data" refers to data that is all represented as relatively short lines with 998 octets or less between CRLF
// line separation sequences, but octets with decimal values greater than 127 (beyond ASCII characters) may be used.
// As with "7bit data" CR and LF octets only occur as part of CRLF line separation sequences and no NULs are allowed.
//
// Binary Data
// "Binary data" refers to data where any sequence of octets whatsoever is allowed.
//
// The Content-Transfer-Encoding values "7bit", "8bit", and "binary" all mean that the identity (i.e. NO) encoding
// transformation has been performed. As such, they serve simply as indicators of the domain of the body data, and provide
// useful information about the sort of encoding that might be needed for transmission in a given transport system.
//
// The quoted-printable and base64 encodings transform their input from an arbitrary domain into material in the "7bit" range,
// thus making it safe to carry over restricted transports.
//
// NOTE: The five values defined for the Content-Transfer-Encoding field imply nothing about the media type other than the algorithm
// by which it was encoded or the transport system requirements if unencoded.
//
//
// New Content-Transfer-Encodings
// ------------------------------------------
// Implementors may, if necessary, define private Content-Transfer- Encoding values, but must use an x-token, which is a name prefixed by
// "X-", to indicate its non-standard status, e.g., "Content-Transfer- Encoding: x-my-new-encoding". Additional standardized Content-Transfer-Encoding
// values must be specified by a standards-track RFC. As such, all content-transfer-encoding namespace except that beginning with "X-" is explicitly
// reserved to the IETF for future use.
//
//
// Interpretation and Use
// ------------------------------------------
// If a Content-Transfer-Encoding header field appears as part of a message header, it applies to the entire body of that message.
// If a Content-Transfer-Encoding header field appears as part of an entity's headers, it applies only to the body of that entity.
// If an entity is of type "multipart" the Content-Transfer-Encoding is not permitted to have any value other than "7bit", "8bit" or "binary".
// All encodings that are desired for bodies of type multipart must be done at the innermost level, by encoding the actual body that needs to be encoded.
//
// NOTE ON ENCODING RESTRICTIONS:  Though the prohibition against using content-transfer-encodings on composite body data may seem overly restrictive,
// it is necessary to prevent nested encodings, in which data are passed through an encoding algorithm multiple times, and must be decoded multiple times
// in order to be properly viewed.
//
// It should be noted that most media types are defined in terms of octets rather than bits, so that the mechanisms described here are mechanisms
// for encoding arbitrary octet streams, not bit streams.  If a bit stream is to be encoded via one of these mechanisms, it must first be converted
// to an 8bit byte stream using the network standard bit order ("big-endian"), in which the earlier bits in a stream become the higher-order bits in
// a 8bit byte. A bit stream not ending at an 8bit boundary must be padded with zeroes. RFC 2046 provides a mechanism for noting the addition of such
// padding in the case of the application/octet-stream media type, which has a "padding" parameter.
//
// Any entity with an unrecognized Content-Transfer-Encoding must be treated as if it has a Content-Type of "application/octet-stream", regardless of
// what the Content-Type header field actually says.
//
//
// Translating Encodings
// ------------------------------------------
// The quoted-printable and base64 encodings are designed so that conversion between them is possible. The only issue that arises in such a conversion is
// the handling of hard line breaks in quoted-printable encoding output. When converting from quoted-printable to base64 a hard line break in the quoted-printable
// form represents a CRLF sequence in the canonical form of the data. It must therefore be converted to a corresponding encoded CRLF in the base64 form of the
// data. Similarly, a CRLF sequence in the canonical form of the data obtained after base64 decoding must be converted to a quoted-printable hard line break,
// but ONLY when converting text data.
//
//
// Quoted-Printable Content-Transfer-Encoding
// ------------------------------------------
// The Quoted-Printable encoding is intended to represent data that largely consists of octets that correspond to printable characters in the US-ASCII character
// set. It encodes the data in such a way that the resulting octets are unlikely to be modified by mail transport. If the data being encoded are mostly US-ASCII
// text, the encoded form of the data remains largely recognizable by humans. A body which is entirely US-ASCII may also be encoded in Quoted-Printable to ensure
// the integrity of the data should the message pass through a character-translating, and/or line-wrapping gateway.
//
// In this encoding, octets are to be represented as determined by the following rules:
//     1. (General 8bit representation) Any octet, except a CR or LF that is part of a CRLF line break of the canonical (standard) form of the data being encoded,
//        may be represented by an "=" followed by a two digit hexadecimal representation of the octet's value. The digits of the hexadecimal alphabet, for this
//        purpose, are "0123456789ABCDEF". Uppercase letters must be used; lowercase letters are not allowed. Thus, for example, the decimal value 12 (US-ASCII
//        form feed) can be represented by "=0C", and the decimal value 61 (US-ASCII EQUAL SIGN) can be represented by "=3D". This rule must be followed except
//        when the following rules allow an alternative encoding.
//
//     2. (Literal representation) Octets with decimal values of 33 through 60 inclusive (61 is the "=" sign, which should be encoded), and 62 hrough 126 (last
//        US-ASCII character with the decimal value of 127 is DEL), inclusive, MAY be represented as the US-ASCII characters which correspond to those octets
//        (EXCLAMATION POINT through LESS THAN, and GREATER THAN through TILDE, respectively).
//
//     3. (White Space) Octets with values of 9 and 32 MAY be represented as US-ASCII TAB (HT) and SPACE characters, respectively, but MUST NOT be so represented
//        at the end of an encoded line. Any TAB (HT) or SPACE characters on an encoded line MUST thus be followed on that line by a printable character.
//        In particular, an "=" at the end of an encoded line, indicating a soft line break may follow one or more TAB (HT) or SPACE characters. It follows that
//        an octet with decimal value 9 or 32 appearing at the end of an encoded line must be represented according to Rule #1. This rule is necessary because
//        some MTAs (Message Transport Agents, programs which transport messages from one user to another, or perform a portion of such transfers) are known to
//        pad lines of text with SPACEs, and others are known to remove "white space" characters from the end of a line. Therefore, when decoding a Quoted-
//        Printable body, any trailing white space on a line must be deleted, as it will necessarily have been added byb intermediate transport agents.
//
//     4. (Line Breaks) A line break in a text body, represented as a CRLF sequence in the text canonical form, must be represented by a (RFC 822) line break,
//        which is also a CRLF sequence, in the Quoted-Printable encoding. Since the canonical representation of media types other than text do not generally
//        include the representation of line breaks as CRLF sequences, no hard line breaks can occur in the quoted-printable encoding of such types. Sequences
//        like "=0D", "=0A", "=0A=0D" and "=0D=0A" will routinely appear in non-text data represented in quoted-printable, of course.
//
//     5. (Soft Line Breaks) The Quoted-Printable encoding REQUIRES that encoded lines be no more than 76 characters long.  If longer lines are to be encoded
//         with the Quoted-Printable encoding, "soft" line breaks must be used. An equal sign as the last character on a encoded line indicates such a non-
//         significant ("soft") line break in the encoded text. The 76 character limit does not count the trailing CRLF, but counts all other characters,including
//         any equal signs.
//
// Since the hyphen character ("-") may be represented as itself in the Quoted-Printable encoding, care must be taken, when encapsulating a quoted-printable
// encoded body inside one or more multipart entities, to ensure that the boundary delimiter does not appear anywhere in the encoded body (A good strategy is
// to choose a boundary that includes a character sequence such as "=_" which can never appear in a quoted-printable body).
//
// A way to get reasonably reliable transport through EBCDIC gateways is to also quote the US-ASCII characters !"#$@[\]^`{|}~ according to rule #1.
//
// Because quoted-printable data is generally assumed to be line- oriented, it is to be expected that the representation of the breaks between the lines of
// quoted-printable data may be altered in transport, in the same manner that plain text mail has always been altered in Internet mail when passing between
// systems with differing newline conventions. If such alterations are likely to constitute a corruption of the data, it is probably more sensible to use the
// base64 encoding rather than the quoted-printable encoding.
//
// NOTE: Several kinds of substrings cannot be generated according to the encoding rules for the quoted-printable content-transfer-encoding, and hence are
// formally illegal if they appear in the output of a quoted-printable encoder. This note enumerates these cases and suggests ways to handle such illegal
// substrings if any are encountered in quoted-printable data that is to be decoded:
//     1. An "=" followed by two hexadecimal digits, one or both of which are lowercase letters in "abcdef", is formally illegal. A robust implementation might
//        choose to recognize them as the corresponding uppercase letters.
//
//     2. An "=" followed by a character that is neither a hexadecimal digit (including "abcdef") nor the CR character of a CRLF pair is illegal. A reasonable
//        approach by a robust implementation might be to include the "=" character and the following character in the decoded data without any transformation
//        and, if possible, indicate to the user that proper decoding was not possible at this point in the data.
//
//     3. An "=" cannot be the ultimate (последний) or penultimate (предпоследний) character in an encoded object. This could be handled as in case (2) above.
//
//     4. Control characters other than TAB, or CR and LF as parts of CRLF pairs, must not appear. The same is true for octets with decimal values greater
//        than 126 (127 is DEL and octet 128 is beyond the US-ASCII range). If found in incoming quoted-printable data by a decoder, a robust implementation might
//        exclude them from the decoded data and warn the user that illegal characters were discovered.
//
//     5. Encoded lines must not be longer than 76 characters, not counting the trailing CRLF. If longer lines are found in incoming, encoded data, a robust
//        implementation might nevertheless decode the lines, and might report the erroneous encoding to the user.
//
// WARNING TO IMPLEMENTORS:
// If binary data is encoded in quoted-printable, care must be taken to encode CR and LF characters as "=0D" and "=0A", respectively.  In particular, a CRLF
// sequence in binary data should be encoded as "=0D=0A".  Otherwise, if CRLF were represented as a hard line break, it might be incorrectly decoded on platforms
// with different line break conventions.
//
// For formalists, the syntax of quoted-printable data is described by the following grammar:
//
//     quoted-printable := qp-line *(CRLF qp-line)
//
//     qp-line := *(qp-segment transport-padding CRLF)
//                qp-part transport-padding
//
//     qp-part := qp-section
//                ; Maximum length of 76 characters
//
//     qp-segment := qp-section *(SPACE / TAB) "="
//                   ; Maximum length of 76 characters
//
//     qp-section := [*(ptext / SPACE / TAB) ptext]
//
//     ptext := hex-octet / safe-char
//
//     safe-char := <any octet with decimal value of 33 through 60 inclusive, and 62 through 126>
//                  ; Characters not listed as "mail-safe" in RFC 2049 are also not recommended.
//
//     hex-octet := "=" 2(DIGIT / "A" / "B" / "C" / "D" / "E" / "F")
//                  ; Octet must be used for characters > 127, =,
//                  ; SPACEs or TABs at the ends of lines, and is
//                  ; recommended for any character not listed in
//                  ; RFC 2049 as "mail-safe".
//
//     transport-padding := *LWSP-char
//                          ; Composers MUST NOT generate
//                          ; non-zero length transport
//                          ; padding, but receivers MUST
//                          ; be able to handle padding
//                          ; added by message transports.
//
// IMPORTANT:
// The addition of LWSP between the elements shown in this BNF is NOT allowed since this BNF does not specify a structured header field.
//
//
// Base64 Content-Transfer-Encoding
// ------------------------------------------
// The Base64 Content-Transfer-Encoding is designed to represent arbitrary sequences of octets in a form that need not be humanly readable. The encoding and
// decoding algorithms are simple, but the encoded data are consistently only about 33 percent larger than the unencoded data.
//
// A 65-character subset of US-ASCII is used, enabling 6 bits to be represented per printable character. (The extra 65th character, "=", is used to signify a
// special processing function.)
//
// The encoding process represents 24-bit groups of input bits as output strings of 4 encoded characters. Proceeding from left to right, a 24-bit input group
// is formed by concatenating 3 8bit input groups. These 24 bits are then treated as 4 concatenated 6-bit groups, each of which is translated into a single digit
// in the base64 alphabet. When encoding a bit stream via the base64 encoding, the bit stream must be presumed to be ordered with the most-significant-bit first.
// That is, the first bit in the stream will be the high-order bit in the first 8bit byte, and the eighth bit will be the low-order bit in the first 8bit byte,
// and so on.
//
// Each 6-bit group is used as an index into an array of 64 printable characters. The character referenced by the index is placed in the output string. These
// characters are selected so as to be universally representable, and the set excludes characters with particular significance to SMTP (e.g., ".", CR, LF) and
// to the multipart boundary delimiters defined in RFC 2046 (e.g., "-"):
//     ABCDEFGHIGKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/
//     (pad) =
//
// The encoded output stream must be represented in lines of no more than 76 characters each. All line breaks or other characters not found in the above list
// must be ignored by decoding software. In base64 data, characters other than those in the above list, line breaks, and other white space probably indicate
// a transmission error, about which a warning message or even a message rejection might be appropriate under some circumstances.
//
// Special processing is performed if fewer than 24 bits are available at the end of the data being encoded. A full encoding quantum is always completed at
// the end of a body.  When fewer than 24 input bits are available in an input group, zero bits are added (on the right) to form an integral number (целое число)
// of 6-bit groups. Padding at the end of the data is performed using the "=" character. Since all base64 input is an integral number of octets, only the
// following cases can arise:
//     1. the final quantum of encoding input is an integral multiple of 24 bits; here, the final unit of encoded output will be an integral multiple of 4
//        characters with no "=" padding,
//
//     2. the final quantum of encoding input is exactly 8 bits; here, the final unit of encoded output will be two characters followed by two "=" padding
//        characters,
//
//     3. the final quantum of encoding input is exactly 16 bits; here, the final unit of encoded output will be three characters followed by one "=" padding
//        character.
//
// Because it is used only for padding at the end of the data, the occurrence of any "=" characters may be taken as evidence that the end of the data has been
// reached (without truncation in transit).
//
// Care must be taken to use the proper octets for line breaks if base64 encoding is applied directly to text material that has not been converted to canonical
// form. In particular, text line breaks must be converted into CRLF sequences prior to base64 encoding. The important thing to note is that this may be done
// directly by the encoder rather than in a prior canonicalization step in some implementations.
//
//
// Content-ID Header Field
// ------------------------------------------
// In constructing a high-level user agent, it may be desirable to allow one body to make reference to another. Accordingly, bodies may be labelled using the
// "Content-ID" header field, which is syntactically identical to the "Message-ID" header field:
//
//     id := "Content-ID" ":" msg-id
//
// Like the Message-ID values, Content-ID values must be generated to be world-unique.
//
// The Content-ID value may be used for uniquely identifying MIME entities in several contexts, particularly for caching data referenced by the
// message/external-body mechanism. Although the Content-ID header is generally optional, its use is MANDATORY in implementations which generate data of the
// optional MIME media type "message/external-body".  That is, each message/external-body entity must have a Content-ID field to permit caching of such data.
//
//
// Content-Description Header Field
// ------------------------------------------
// The ability to associate some descriptive information with a given body is often desirable.  For example, it may be useful to mark an "image" body as
// "a picture of the Space Shuttle Endeavor." Such text may be placed in the Content-Description header field. This header field is always optional.
//
//     description := "Content-Description" ":" *text
//
// The description is presumed to be given in the US-ASCII character set, although the mechanism specified in RFC 2047 may be used for non-US-ASCII
// Content-Description values.

// RFC2046
// ==========================================
//
//
//
// The initial document in this set, RFC 2045, specifies the various headers used to describe the structure of MIME messages. This second document defines
// the general structure of the MIME media typing system and defines an initial set of media types. The third document, RFC 2047, describes extensions to
// RFC 822 to allow non-US-ASCII text data in Internet mail header fields. The fourth document, RFC 2048, specifies various IANA registration procedures
// for MIME-related facilities. The fifth and final document, RFC 2049, describes MIME conformance criteria as well as providing some illustrative examples
// of MIME message formats, acknowledgements, and the bibliography.
//
//
// Introduction
// ------------------------------------------
// The first document in this set, RFC 2045, defines a number of header fields, including Content-Type. The Content-Type field is used to specify the nature
// of the data in the body of a MIME entity, by giving media type and subtype identifiers, and by providing auxiliary information that may be required for
// certain media types. After the type and subtype names, the remainder of the header field is simply a set of parameters, specified in an attribute/value
// notation. The ordering of parameters is not significant.
//
// In general, the top-level media type is used to declare the general type of data, while the subtype specifies a specific format for that type of data.
// Thus, a media type of "image/xyz" is enough to tell a user agent that the data is an image, even if the user agent has no knowledge of the specific image
// format "xyz". Such information can be used, for example, to decide whether or not to show a user the raw data from an unrecognized subtype -- such an
// action might be reasonable for unrecognized subtypes of "text", but not for unrecognized subtypes of "image" or "audio". For this reason, registered
// subtypes of "text", "image", "audio", and "video" should not contain embedded information that is really of a different type. Such compound formats should
// be represented using the "multipart" or "application" types.
//
// Parameters are modifiers of the media subtype, and as such do not fundamentally affect the nature of the content. The set of meaningful parameters depends
// on the media type and subtype. Most parameters are associated with a single specific subtype. However, a given top-level media type may define parameters
// which are applicable to any subtype of that type. Parameters may be required by their defining media type or subtype or they may be optional.
// MIME implementations must also ignore any parameters whose names they do not recognize.
//
//
// Definition of a Top-Level Media Type
// ------------------------------------------
// The definition of a top-level media type consists of:
//     (1) a name and a description of the type, including criteria for whether a particular type would qualify under that type,
//
//     (2) the names and definitions of parameters, if any, which are defined for all subtypes of that type (including whether such parameters are
//         required or optional),
//
//     (3) how a user agent and/or gateway should handle unknown subtypes of this type,
//
//     (4) general considerations on gatewaying entities of this top-level type, if any, and
//
//     (5) any restrictions on content-transfer-encodings for entities of this top-level type.
//
//
// Overview Of The Initial Top-Level Media Types
// ----------------------------------------------
// The five discrete top-level media types are:
//     (1) text -- textual information.  The subtype "plain" in particular indicates plain text containing no formatting commands or directives of any sort.
//         Plain text is intended to be displayed "as-is". No special software is required to get the full meaning of the text, aside from support for the
//         indicated character set. Other subtypes are to be used for enriched text in forms where application software may enhance the appearance of the
//         text, but such software must not be required in order to get the general idea of the content. Possible subtypes of "text" thus include any word
//         processor format that can be read without resorting to software that understands the format. In particular, formats that employ embeddded binary
//         formatting information are not considered directly readable. A very simple and portable subtype, "richtext", was defined in RFC 1341, with a further
//         revision in RFC 1896 under the name "enriched".
//
//     (2) image -- image data.  "Image" requires a display device (such as a graphical display, a graphics printer, or a FAX machine) to view the information.
//         An initial subtype is defined for the widely-used image format JPEG. .  subtypes are defined for two widely-used image formats, jpeg and gif.
//
//     (3) audio -- audio data.  "Audio" requires an audio output device (such as a speaker or a telephone) to "display" the contents.  An initial subtype "basic"
//         is defined in this document.
//
//     (4) video -- video data.  "Video" requires the capability to display moving images, typically including specialized hardware and software. An initial subtype
//         "mpeg" is defined in this document.
//
//     (5) application -- some other kind of data, typically either uninterpreted binary data or information to be processed by an application. The subtype
//         "octet-stream" is to be used in the case of uninterpreted binary data, in which case the simplest recommended action is to offer to write the
//         information into a file for the user.
//
// The two composite top-level media types are:
//     (1) multipart -- data consisting of multiple entities of independent data types. Four subtypes are initially defined, including the basic "mixed" subtype
//         specifying a generic mixed set of parts, "alternative" for representing the same data in multiple formats, b"parallel" for parts intended to be viewed
//         simultaneously, and "digest" for multipart entities in which each part has a default type of "message/rfc822".
//
//     (2) message -- an encapsulated message.  A body of media type "message" is itself all or a portion of some kind of message object.  Such objects may or may
//         not in turn contain other entities.  The "rfc822" subtype is used when the encapsulated content is itself an RFC 822 message.  The "partial" subtype is
//         defined for partial RFC 822 messages, to permit the fragmented transmission of bodies that are thought to be too large to be passed through transport
//         facilities in one piece.  Another subtype, "external-body", is defined for specifying large bodies by reference to an external data source.
//
//
// Discrete Media Type Values
// ----------------------------------------------
// Five of the seven initial media type values refer to discrete bodies. The content of these types must be handled by non-MIME mechanisms; they are opaque to
// MIME processors.
//
//
// Text Media Type
// ----------------------------------------------
// The "text" media type is intended for sending material which is principally textual in form. A "charset" parameter may be used to indicate the character set
// of the body text for "text" subtypes, notably including the subtype "text/plain", which is a generic subtype for plain text. Plain text does not provide for
// or allow formatting commands, font attribute specifications, processing instructions, interpretation directives, or content markup. Plain text is seen simply
// as a linear sequence of characters, possibly interrupted by line breaks or page breaks. Plain text may allow the stacking of several characters in the same
// position in the text. Plain text in scripts like Arabic and Hebrew may also include facilitites that allow the arbitrary mixing of text segments with opposite
// writing directions.
//
// Beyond plain text, there are many formats for representing what might be known as "rich text".  An interesting characteristic of many such representations is
// that they are to some extent readable even without the software that interprets them. It is useful, then, to distinguish them, at the highest level, from such
// unreadable data as images, audio, or text represented in an unreadable form. In the absence of appropriate interpretation software, it is reasonable to show
// subtypes of "text" to the user, while it is not reasonable to do so with most nontextual data. Such formatted textual data should be represented using subtypes
// of "text".
//
//
// Representation of Line Breaks
// ----------------------------------------------
// The canonical form of any MIME "text" subtype MUST always represent a line break as a CRLF sequence. Similarly, any occurrence of CRLF in MIME "text" MUST
// represent a line break. Use of CR and LF outside of line break sequences is also forbidden.
//
//  This rule applies regardless of format or character set or sets involved.
//
// NOTE: The proper interpretation of line breaks when a body is displayed depends on the media type. In particular, while it is appropriate to treat a line break
// as a transition to a new line when displaying a "text/plain" body, this treatment is actually incorrect for other subtypes of "text" like "text/enriched".
// Similarly, whether or not line breaks should be added during display operations is also a function of the media type. It should not be necessary to add any
// line breaks to display "text/plain" correctly, whereas proper display of "text/enriched" requires the appropriate addition of line breaks.
//
// NOTE: Some protocols defines a maximum line length.  E.g. SMTP allows a maximum of 998 octets before the next CRLF sequence. To be transported by such protocols,
// data which includes too long segments without CRLF sequences must be encoded with a suitable content-transfer-encoding.
//
//
// Charset Parameter
// ----------------------------------------------
// A critical parameter that may be specified in the Content-Type field for "text/plain" data is the character set. This is specified with a "charset" parameter,
// as in:
//     Content-type: text/plain; charset=iso-8859-1
// Unlike some other parameter values, the values of the charset parameter are NOT case sensitive. The default character set, which must be assumed in the absence
// of a charset parameter, is US-ASCII.
//
// Note that if the specified character set includes 8-bit characters and such characters are used in the body, a Content-Transfer-Encoding header field and a
// corresponding encoding on the data are required inorder to transmit the body via some mail transfer protocols, such as SMTP.
//
//
// Unrecognized Subtypes
// ----------------------------------------------
// Unrecognized subtypes of "text" should be treated as subtype "plain" as long as the MIME implementation knows how to handle the charset. Unrecognized subtypes
// which also specify an unrecognized charset should be treated as "application/octet- stream".
//
//
// Image Media Type
// ----------------------------------------------
// A media type of "image" indicates that the body contains an image. The subtype names the specific image format. These names are not case sensitive. An initial
// subtype is "jpeg" for the JPEG format using JFIF encoding [JPEG].
//
// Unrecognized subtypes of "image" should at a miniumum be treated as "application/octet-stream". Implementations may optionally elect to pass subtypes of
// "image" that they do not specifically recognize to a secure and robust general-purpose image viewing application, if such an application is available.
//
//
// Audio Media Type
// ----------------------------------------------
// A media type of "audio" indicates that the body contains audio data. The initial subtype of "basic" is specified providing an absolutely minimal lowest common
// denominator audio format.
//
// Unrecognized subtypes of "audio" should at a miniumum be treated as "application/octet-stream". Implementations may optionally elect to pass subtypes of
// "audio" that they do not specifically recognize to a robust general-purpose audio playing application, if such an application is available.
//
//
// Video Media Type
// ----------------------------------------------
// A media type of "video" indicates that the body contains a time- varying-picture image, possibly with color and coordinated sound. The term 'video' is used in
// its most generic sense, rather than with reference to any particular technology or format, and is not meant to preclude subtypes such as animated drawings
// encoded compactly. The subtype "mpeg" refers to video coded according to the MPEG standard [MPEG].
//
// Note that although in general this document strongly discourages the mixing of multiple media in a single body, it is recognized that many so-called video
// formats include a representation for synchronized audio, and this is explicitly permitted for subtypes of "video".
//
// Unrecognized subtypes of "video" should at a minumum be treated as "application/octet-stream". Implementations may optionally elect to pass subtypes of "video"
// that they do not specifically recognize to a robust general-purpose video display application, if such an application is available.
//
//
// Application Media Type
// ----------------------------------------------
// The "application" media type is to be used for discrete data which do not fit in any of the other categories, and particularly for data to be processed by some
// type of application program. This document defines one subtype - octet-stream.
//
//
// Octet-Stream Subtype
// ----------------------------------------------
// The "octet-stream" subtype is used to indicate that a body contains arbitrary binary data.  The set of currently defined parameters is:
//     (1) TYPE -- the general type or category of binary data. This is intended as information for the human recipient rather than for any automatic processing.
//
//     (2) PADDING -- the number of bits of padding that were appended to the bit-stream comprising the actual contents to produce the enclosed 8bit byte-oriented
//         data.  This is useful for enclosing a bit-stream in a body when the total number of bits is not a multiple of 8.
//
// Both of these parameters are optional.
//
// The recommended action for an implementation that receives an "application/octet-stream" entity is to simply offer to put the data in a file, with any
// Content-Transfer-Encoding undone, or perhaps to use it as input to a user-specified process.
//
// To reduce the danger of transmitting rogue programs, it is strongly recommended that implementations NOT implement a path-search mechanism whereby an arbitrary
// program named in the Content-Type parameter (e.g., an "interpreter=" parameter) is found and executed using the message body as input.
//
//
// Composite Media Type Values
// ----------------------------------------------
// The remaining two of the seven initial Content-Type values refer to composite entities.  Composite entities are handled using MIME mechanisms -- a MIME processor
// typically handles the body directly.
//
//
// Multipart Media Type
// ----------------------------------------------
// In the case of multipart entities, in which one or more different sets of data are combined in a single body, a "multipart" media type field must appear in the
// entity's header. The body must then contain one or more body parts, each preceded by a boundary delimiter line, and the last one followed by a closing boundary
// delimiter line. After its boundary delimiter line, each body part then consists of a header area, a blank line, and a body area.
//
// NO header fields are actually required in body parts. A body part that starts with a blank line, therefore, is allowed and is a body part for which all default
// values are to be assumed. In such a case, the absence of a Content-Type header usually indicates that the corresponding body has a content-type of
// "text/plain; charset=US-ASCII".
//
// The only header fields that have defined meaning for body parts are those the names of which begin with "Content-". All other header fields may be ignored in
// body parts. Although they should generally be retained if at all possible, they may be discarded by gateways if necessary.  Such other fields are permitted to
// appear in body parts but must not be depended on.  "X-" fields may be created for experimental or private purposes, with the recognition that the information
// they contain may be lost at some gateways.
//
// All present and future subtypes of the "multipart" type must use an identical syntax. Subtypes may differ in their semantics, and may impose additional
// restrictions on syntax, but must conform to the required syntax for the "multipart" type. This requirement ensures that all conformant user agents will at least
// be able to recognize and separate the parts of any multipart entity, even those of an unrecognized subtype.
//
// As stated in the definition of the Content-Transfer-Encoding field [RFC 2045], no encoding other than "7bit", "8bit", or "binary" is permitted for entities of
// type "multipart".  The "multipart" boundary delimiters and header fields are always represented as 7bit US-ASCII in any case (though the header fields may encode
// non-US-ASCII header text as per RFC 2047) and data within the body parts can be encoded on a part-by-part basis, with Content-Transfer-Encoding fields for each
// appropriate body part.
//
//
// Common Syntax
// ----------------------------------------------
// This section defines a common syntax for subtypes of "multipart". All subtypes of "multipart" must use this syntax.
//
// The Content-Type field for multipart entities requires one parameter, "boundary". The boundary delimiter line is then defined as a line consisting entirely of
// two hyphen characters ("-", decimal value 45) followed by the boundary parameter value from the Content-Type header field, optional linear whitespace, and a
// terminating CRLF.
//
// WARNING TO IMPLEMENTORS: The grammar for parameters on the Content- type field is such that it is often necessary to enclose the boundary parameter values in
// quotes on the Content-type line. This is not always necessary, but never hurts. Implementors should be sure to study the grammar carefully in order to avoid
// producing invalid Content-type fields. Thus, a typical "multipart" Content-Type header field might look like this:
//
//     Content-Type: multipart/mixed; boundary=gc0p4Jq0M2Yt08j34c0p
//
// But the following is not valid:
//
//     Content-Type: multipart/mixed; boundary=gc0pJq0M:08jU534c0p
//
// (because of the colon) and must instead be represented as
//
//     Content-Type: multipart/mixed; boundary="gc0pJq0M:08jU534c0p"
//
// The boundary delimiter MUST occur at the beginning of a line, i.e., following a CRLF, and the initial CRLF is considered to be attached to the boundary delimiter
// line rather than part of the preceding part. The boundary may be followed by zero or more characters of linear whitespace. It is then terminated by either another
// CRLF and the header fields for the next part, or by two CRLFs, in which case there are no header fields for the next part. If no Content-Type field is present it
// is assumed to be "message/rfc822" in a "multipart/digest" and "text/plain" otherwise.
//
// NOTE: The CRLF preceding the boundary delimiter line is conceptually attached to the boundary so that it is possible to have a part that does not end with a
// CRLF (line  break). Body parts that must be considered to end with line breaks, therefore, must have two CRLFs preceding the boundary delimiter line, the first
// of which is part of the preceding body part, and the second of which is part of the encapsulation boundary.
//
// Boundary delimiters must not appear within the encapsulated material, and must be no longer than 70 characters, not counting the two leading hyphens.
//
// The boundary delimiter line following the last body part is a distinguished delimiter that indicates that no further body parts will follow. Such a delimiter
// line is identical to the previous delimiter lines, with the addition of two more hyphens after the boundary parameter value.
//
//   --gc0pJq0M:08jU534c0p--
//
// NOTE TO IMPLEMENTORS:  Boundary string comparisons must compare the boundary value with the beginning of each candidate line. An exact match of the entire
// candidate line is not required; it is sufficient that the boundary appear in its entirety following the CRLF.
//
// There appears to be room for additional information prior to the first boundary delimiter line and following the final boundary delimiter line. These areas
// should generally be left blank, and implementations must ignore anything that appears before the first boundary delimiter line or after the last one.
//
// NOTE:  These "preamble" and "epilogue" areas are generally not used because of the lack of proper typing of these parts and the lack of clear semantics for
// handling these areas at gateways, particularly X.400 gateways. However, rather than leaving the preamble area blank, many MIME implementations have found this
// to be a convenient place to insert an explanatory note for recipients who read the message with pre-MIME software, since such notes will be ignored by
// MIME-compliant software.
//
// NOTE:  Because boundary delimiters must not appear in the body parts being encapsulated, a user agent must exercise care to choose a unique boundary parameter
// value.  The boundary parameter value in the example above could have been the result of an algorithm designed to produce boundary delimiters with a very low
// probability of already existing in the data to be encapsulated without having to prescan the data. Alternate algorithms might result in more "readable" boundary
// delimiters for a recipient with an old user agent, but would require more attention to the possibility that the boundary delimiter might appear at the beginning
// of some line in the encapsulated part. The simplest boundary delimiter line possible is something like "---", with a closing boundary delimiter line of "-----".
//
// The use of a media type of "multipart" in a body part within another "multipart" entity is explicitly allowed. In such cases, for obvious reasons, care must be
// taken to ensure that each nested "multipart" entity uses a different boundary delimiter.
//
// The use of the "multipart" media type with only a single body part may be useful in certain contexts, and is explicitly permitted.
//
// NOTE: Experience has shown that a "multipart" media type with a single body part is useful for sending non-text media types. It has the advantage of providing
// the preamble as a place to include decoding instructions. In addition, a number of SMTP gateways move or remove the MIME headers, and a clever MIME decoder can
// take a good guess at multipart boundaries even in the absence of the Content-Type header and thereby successfully decode the message.
//
// The only mandatory global parameter for the "multipart" media type is the boundary parameter, which consists of 1 to 70 characters from a set of characters known
// to be very robust through mail gateways, and NOT ending with white space. (If a boundary delimiter line appears to end with white space, the white space must be
// presumed to have been added by a gateway, and must be deleted.)  It is formally specified by the following BNF:
//
//     boundary := 0*69<bchars> bcharsnospace
//
//     bchars := bcharsnospace / " "
//
//     bcharsnospace := DIGIT / ALPHA / "'" / "(" / ")" /
//                      "+" / "_" / "," / "-" / "." /
//                      "/" / ":" / "=" / "?"
//
// Overall, the body of a "multipart" entity may be specified as follows:
//
//    dash-boundary := "--" boundary
//                     ; boundary taken from the value of
//                     ; boundary parameter of the
//                     ; Content-Type field.
//
//    multipart-body := [preamble CRLF]
//                      dash-boundary transport-padding CRLF
//                      body-part *encapsulation
//                      close-delimiter transport-padding
//                      [CRLF epilogue]
//
//    transport-padding := *LWSP-char
//                         ; Composers MUST NOT generate
//                         ; non-zero length transport
//                         ; padding, but receivers MUST
//                         ; be able to handle padding
//                         ; added by message transports.
//
//    encapsulation := delimiter transport-padding
//                     CRLF body-part
//
//    delimiter := CRLF dash-boundary
//
//    close-delimiter := delimiter "--"
//
//    preamble := discard-text
//
//    epilogue := discard-text
//
//    discard-text := *(*text CRLF) *text
//                    ; May be ignored or discarded.
//
//    body-part := MIME-part-headers [CRLF *OCTET]
//                 ; Lines in a body-part must not start
//                 ; with the specified dash-boundary and
//                 ; the delimiter must not appear anywhere
//                 ; in the body part.  Note that the
//                 ; semantics of a body-part differ from
//                 ; the semantics of a message, as
//                 ; described in the text.
//
//    OCTET := <any 0-255 octet value>
//
// IMPORTANT:  The free insertion of linear-white-space and RFC 822 comments between the elements shown in this BNF is NOT allowed since this BNF does not specify
// a structured header field.
//
// NOTE:  In certain transport enclaves, RFC 822 restrictions such as the one that limits bodies to printable US-ASCII characters may not be in force. The
// relaxation of these restrictions should be construed as locally extending the definition of bodies, for example to include octets outside of the US-ASCII range,
// as long as these extensions are supported by the transport and adequately documented in the Content-Transfer-Encoding header field.  However, in no event are
// headers (either message headers or body part headers) allowed to contain anything other than US-ASCII characters.
//
//
// Mixed Subtype
// ----------------------------------------------
// The "mixed" subtype of "multipart" is intended for use when the body parts are independent and need to be bundled in a particular order. Any "multipart"
// subtypes that an implementation does not recognize must be treated as being of subtype "mixed".

// RFC2047
//
//
//
// This memo describes techniques to allow the encoding of non-ASCII text in various portions of a message header, in a manner which is unlikely to confuse
// existing message handling software.
//
// Certain sequences of "ordinary" printable ASCII characters (known as "encoded-words") are reserved for use as encoded data. The syntax of encoded-words
// is such that they are unlikely to "accidentally" appear as normal text in message headers.
//
// Generally, an "encoded-word" is a sequence of printable ASCII characters that begins with "=?", ends with "?=", and has two "?"s in between. It specifies
// a character set and an encoding method, and also includes the original text encoded as graphic ASCII characters, according to the rules for that encoding
// method.
//
// A mail composer that implements this specification will provide a means of inputting non-ASCII text in header fields, but will translate these fields
// (or appropriate portions of these fields) into encoded-words before inserting them into the message header.
//
// A mail reader that implements this specification will recognize encoded-words when they appear in certain portions of the message header. Instead of
// displaying the encoded-word "as is", it will everse the encoding and display the original text in the designated character set.
//
//
// Syntax of encoded-words
// ----------------------------------------------
// An 'encoded-word' is defined by the following ABNF grammar. The notation of RFC 822 is used, with the exception that white space characters MUST NOT
// appear between components of an 'encoded-word'.
//
//     encoded-word = "=?" charset "?" encoding "?" encoded-text "?="
//
//     charset = token    ; see section "Character sets"
//
//     encoding = token   ; see section "Encodings"
//
//     token = 1*<Any CHAR except SPACE, CTLs, and especials>
//
//     especials = "(" / ")" / "<" / ">" / "@" / "," / ";" / ":" / "
//                 <"> / "/" / "[" / "]" / "?" / "." / "="
//
//     encoded-text = 1*<Any printable ASCII character other than "?"
//                       or SPACE>
//                    ; (but see "Use of encoded-words in message
//                    ; headers", section "Use of encoded-words in message headers")
//
// Both 'encoding' and 'charset' names are case-independent.  Thus the
// charset name "ISO-8859-1" is equivalent to "iso-8859-1", and the
// encoding named "Q" may be spelled either "Q" or "q".
//
// An 'encoded-word' may not be more than 75 characters long, including
// 'charset', 'encoding', 'encoded-text', and delimiters.  If it is
// desirable to encode more text than will fit in an 'encoded-word' of
// 75 characters, multiple 'encoded-word's (separated by CRLF SPACE) may
// be used.
//
// While there is no limit to the length of a multiple-line header
// field, each line of a header field that contains one or more
// 'encoded-word's is limited to 76 characters.
//
// The length restrictions are included both to ease interoperability
// through internetwork mail gateways, and to impose a limit on the
// amount of lookahead a header parser must employ (while looking for a
// final ?= delimiter) before it can decide whether a token is an
// "encoded-word" or something else.
//
// IMPORTANT: 'encoded-word's are designed to be recognized as 'atom's
// by an RFC 822 parser.  As a consequence, unencoded white space
// characters (such as SPACE and HTAB) are FORBIDDEN within an
// 'encoded-word'.  For example, the character sequence
//
//     =?iso-8859-1?q?this is some text?=
//
// would be parsed as four 'atom's, rather than as a single 'atom' (by
// an RFC 822 parser) or 'encoded-word' (by a parser which understands
// 'encoded-words').  The correct way to encode the string "this is some
// text" is to encode the SPACE characters as well, e.g.
//
//     =?iso-8859-1?q?this=20is=20some=20text?=
//
// The characters which may appear in 'encoded-text' are further
// restricted by the rules in section "Use of encoded-words in message headers".
//
//
// Character sets
// ----------------------------------------------
// The 'charset' portion of an 'encoded-word' specifies the character
// set associated with the unencoded text.  A 'charset' can be any of
// the character set names allowed in an MIME "charset" parameter of a
// "text/plain" body part, or any character set name registered with
// IANA for use with the MIME text/plain content-type.
//
// Some character sets use code-switching techniques to switch between
// "ASCII mode" and other modes.  If unencoded text in an 'encoded-word'
// contains a sequence which causes the charset interpreter to switch
// out of ASCII mode, it MUST contain additional control codes such that
// ASCII mode is again selected at the end of the 'encoded-word'.  (This
// rule applies separately to each 'encoded-word', including adjacent
// 'encoded-word's within a single header field.)
//
//
// Encodings
// ----------------------------------------------
// Initially, the legal values for "encoding" are "Q" and "B".  These
// encodings are described below.  The "Q" encoding is recommended for
// use when most of the characters to be encoded are in the ASCII
// character set; otherwise, the "B" encoding should be used.
// Nevertheless, a mail reader which claims to recognize 'encoded-word's
// MUST be able to accept either encoding for any character set which it
// supports.
//
// Only a subset of the printable ASCII characters may be used in
// 'encoded-text'. Space and tab characters are not allowed, so that
// the beginning and end of an 'encoded-word' are obvious.  The "?"
// character is used within an 'encoded-word' to separate the various
// portions of the 'encoded-word' from one another, and thus cannot
// appear in the 'encoded-text' portion. Other characters are also
// illegal in certain contexts.  For example, an 'encoded-word' in a
// 'phrase' preceding an address in a From header field may not contain
// any of the "specials" defined in RFC 822.  Finally, certain other
// characters are disallowed in some contexts, to ensure reliability for
// messages that pass through internetwork mail gateways.
//
// The "B" encoding automatically meets these requirements.  The "Q"
// encoding allows a wide range of printable characters to be used in
// non-critical locations in the message header (e.g., Subject), with
// fewer characters available for use in other locations.
//
//
// The "B" encoding
// ----------------------------------------------
// The "B" encoding is identical to the "BASE64" encoding defined by RFC 2045.
//
//
// The "Q" encoding
// ----------------------------------------------
// The "Q" encoding is similar to the "Quoted-Printable" content-
// transfer-encoding defined in RFC 2045.  It is designed to allow text
// containing mostly ASCII characters to be decipherable on an ASCII
// terminal without decoding.
//    (1) Any 8-bit value may be represented by a "=" followed by two
//        hexadecimal digits.  For example, if the character set in use
//        were ISO-8859-1, the "=" character would thus be encoded as
//        "=3D", and a SPACE by "=20".  (Upper case should be used for
//        hexadecimal digits "A" through "F".)
//
//    (2) The 8-bit hexadecimal value 20 (e.g., ISO-8859-1 SPACE) may be
//        represented as "_" (underscore, ASCII 95.).  (This character may
//        not pass through some internetwork mail gateways, but its use
//        will greatly enhance readability of "Q" encoded data with mail
//        readers that do not support this encoding.)  Note that the "_"
//        always represents hexadecimal 20, even if the SPACE character
//        occupies a different code position in the character set in use.
//
//    (3) 8-bit values which correspond to printable ASCII characters other
//        than "=", "?", and "_" (underscore), MAY be represented as those
//        characters.  (But see section "Use of encoded-words in message headers"
//        for restrictions.)  In particular, SPACE and TAB MUST NOT be represented
//        as themselves within encoded words.
//
//
// Use of encoded-words in message headers
// ----------------------------------------------
// An 'encoded-word' may appear in a message header or body part header
// according to the following rules:
//     (1) An 'encoded-word' may replace a 'text' token (as defined by RFC 822)
//         in any Subject or Comments header field, any extension message
//         header field, or any MIME body part field for which the field body
//         is defined as '*text'.  An 'encoded-word' may also appear in any
//         user-defined ("X-") message or body part header field.
//
//         Ordinary ASCII text and 'encoded-word's may appear together in the
//         same header field.  However, an 'encoded-word' that appears in a
//         header field defined as '*text' MUST be separated from any adjacent
//         'encoded-word' or 'text' by 'linear-white-space'.
//
//    (2) An 'encoded-word' may appear within a 'comment' delimited by "(" and
//        ")", i.e., wherever a 'ctext' is allowed.  More precisely, the RFC
//        822 ABNF definition for 'comment' is amended as follows:
//
//        comment = "(" *(ctext / quoted-pair / comment / encoded-word) ")"
//
//        A "Q"-encoded 'encoded-word' which appears in a 'comment' MUST NOT
//        contain the characters "(", ")" or "
//        'encoded-word' that appears in a 'comment' MUST be separated from
//        any adjacent 'encoded-word' or 'ctext' by 'linear-white-space'.
//
//        It is important to note that 'comment's are only recognized inside
//        "structured" field bodies.  In fields whose bodies are defined as
//        '*text', "(" and ")" are treated as ordinary characters rather than
//        comment delimiters, and rule (1) of this section applies.  (See RFC
//        822, sections 3.1.2 and 3.1.3)
//
//    (3) As a replacement for a 'word' entity within a 'phrase', for example,
//        one that precedes an address in a From, To, or Cc header.  The ABNF
//        definition for 'phrase' from RFC 822 thus becomes:
//
//        phrase = 1*( encoded-word / word )
//
//        In this case the set of characters that may be used in a "Q"-encoded
//        'encoded-word' is restricted to: <upper and lower case ASCII
//        letters, decimal digits, "!", "*", "+", "-", "/", "=", and "_"
//        (underscore, ASCII 95.)>.  An 'encoded-word' that appears within a
//        'phrase' MUST be separated from any adjacent 'word', 'text' or
//        'special' by 'linear-white-space'.
//
// These are the ONLY locations where an 'encoded-word' may appear.  In
// particular:
//
// + An 'encoded-word' MUST NOT appear in any portion of an 'addr-spec'.
//
// + An 'encoded-word' MUST NOT appear within a 'quoted-string'.
//
// + An 'encoded-word' MUST NOT be used in a Received header field.
//
// + An 'encoded-word' MUST NOT be used in parameter of a MIME
//   Content-Type or Content-Disposition field, or in any structured
//   field body except within a 'comment' or 'phrase'.
//
// The 'encoded-text' in an 'encoded-word' must be self-contained;
// 'encoded-text' MUST NOT be continued from one 'encoded-word' to
// another.  This implies that the 'encoded-text' portion of a "B"
// 'encoded-word' will be a multiple of 4 characters long; for a "Q"
// 'encoded-word', any "=" character that appears in the 'encoded-text'
// portion will be followed by two hexadecimal characters.
//
// Each 'encoded-word' MUST encode an integral number of octets.  The
// 'encoded-text' in each 'encoded-word' must be well-formed according
// to the encoding specified; the 'encoded-text' may not be continued in
// the next 'encoded-word'.  (For example, "=?charset?Q?=?=
// =?charset?Q?AB?=" would be illegal, because the two hex digits "AB"
// must follow the "=" in the same 'encoded-word'.)
//
// Each 'encoded-word' MUST represent an integral number of characters.
// A multi-octet character may not be split across adjacent 'encoded-
// word's.
//
// Only printable and white space character data should be encoded using
// this scheme.  However, since these encoding schemes allow the
// encoding of arbitrary octet values, mail readers that implement this
// decoding should also ensure that display of the decoded data on the
// recipient's terminal will not cause unwanted side-effects.
