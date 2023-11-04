package quotedprintable

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestReader(t *testing.T) {
	tests := []struct {
		in, want string
		err      any
	}{
		{in: "", want: ""},
		{in: "foo bar", want: "foo bar"},
		{in: "foo bar=3D", want: "foo bar="},       // 3D in decimal is 61 which is the '=' ascii character, basically escape of '='
		{in: "foo bar=3d", want: "foo bar="},       // lax (not so strict), 3d in decimal is 61 which is the '=' ascii character, basically escape of '='
		{in: "foo bar=\n", want: "foo bar"},        // deviation from RFC 2045, in addition to "=\r\n", "=\n" is also treated as soft line break
		{in: "foo bar\n", want: "foo bar\n"},       // somewhat lax (not so strict), deviation from RFC 2045, LF is treated as CRLF
		{in: "foo bar=0D=0A", want: "foo bar\r\n"}, // =0D=0A is the hex encoding of CRLF
		{in: " A B =\r\n C ", want: " A B  C"},     // =\r\n - soft line break should be removed
		{in: " A B =\n C ", want: " A B  C"},       // lax (not so strict). treating LF as CRLF, soft line break should be removed
		{in: "foo=\nbar", want: "foobar"},          // soft line break should be removed
		{in: "foo\x00bar", want: "foo", err: "quotedprintable: invalid unescaped byte 0x00 in body"}, // only octets 32-60, 62-126 can be represented in plain ASCII
		{in: "foo bar\xff", want: "foo bar\xff"},                                                     // as an extension to RFC 2045, we accept values >= 0x80 without complaint

		// Equal sign.
		{in: "=3D30\n", want: "=30\n"},        // =3D is the encoded '='
		{in: "=00=FF0=\n", want: "\x00\xff0"}, // =\n treated as a soft line break and thus removed, =00=FF are encoded bytes, 0 is a plain ascii character

		// Trailing whitespace
		{in: "foo  \n", want: "foo\n"},                                      // when decoding a Quoted-Printable body, any trailing white space on a line must be deleted, as it will necessarily have been added by intermediate transport agents
		{in: "foo  \n\nfoo =\n\nfoo=20\n\n", want: "foo\n\nfoo \nfoo \n\n"}, // remove trailing WS, remove soft line break, =20 is encoded WS

		// Tests that we allow bare \n and \r through, despite it being strictly
		// not permitted per RFC 2045, Section 6.7 Page 22 bullet (4).
		{in: "foo\nbar", want: "foo\nbar"},
		{in: "foo\rbar", want: "foo\rbar"},
		{in: "foo\r\nbar", want: "foo\r\nbar"},

		// Different types of soft line-breaks.
		{in: "foo=\r\nbar", want: "foobar"},
		{in: "foo=\nbar", want: "foobar"},
		{in: "foo=\rbar", want: "foo", err: "quotedprintable: invalid hex byte 0x0d"}, // =\r is not a soft line break, thus \r is treaded as a raw byte, which doesn't fall into the 32-60, 62-126 octets
		// Issue 15486, accept trailing soft line-break at end of input.
		{in: "foo=", want: "foo"},
		{in: "=", want: "", err: `quotedprintable: invalid bytes after =: ""`}, // do not except empty line consisting only of soft line breaks

		// Example from RFC 2045:
		{in: "Now's the time =\n" + "for all folk to come=\n" + " to the aid of their country.", want: "Now's the time for all folk to come to the aid of their country."},
		{in: "accept UTF-8 right quotation mark: '", want: "accept UTF-8 right quotation mark: '"},
	}
	for _, tt := range tests {
		var buf strings.Builder
		_, err := io.Copy(&buf, NewReader(strings.NewReader(tt.in)))
		if got := buf.String(); got != tt.want {
			t.Errorf("for %q, got %q; want %q", tt.in, got, tt.want)
		}
		switch verr := tt.err.(type) {
		case nil:
			if err != nil {
				t.Errorf("for %q, got unexpected error: %v", tt.in, err)
			}
		case string:
			if got := fmt.Sprint(err); got != verr {
				t.Errorf("for %q, got error %q; want %q", tt.in, got, verr)
			}
		case error:
			if err != verr {
				t.Errorf("for %q, got error %q; want %q", tt.in, err, verr)
			}
		}
	}
}
