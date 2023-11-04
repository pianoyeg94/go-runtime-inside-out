package quotedprintable

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestWriter(t *testing.T) {
	testWriter(t, false)
}

func TestWriterBinary(t *testing.T) {
	testWriter(t, true)
}

func testWriter(t *testing.T, binary bool) {
	tests := []struct {
		in, want, wantB string
	}{
		{in: "", want: ""},
		{in: "foo bar", want: "foo bar"},
		{in: "foo bar=", want: "foo bar=3D"},                                       // test escape of '=', 3D in decimal is 61 which is the '=' ascii character, =3D is the encoded '='
		{in: "foo bar\r", want: "foo bar\r\n", wantB: "foo bar=0D"},                // test that in binary CR is hex encoded and in non-binary CR is converted to CRLF
		{in: "foo bar\r\r", want: "foo bar\r\n\r\n", wantB: "foo bar=0D=0D"},       // test that in binar  CRs are hex encoded and in non-binary CRs are converted to CRLFs
		{in: "foo bar\n", want: "foo bar\r\n", wantB: "foo bar=0A"},                // test that in binary LF is hex encoded and in non-binary LF is converted to CRLF
		{in: "foo bar\r\n", want: "foo bar\r\n", wantB: "foo bar=0D=0A"},           // test that in binary CRLF is hex encoded and in non-binary CRLF remains untouched
		{in: "foo bar\r\r\n", want: "foo bar\r\n\r\n", wantB: "foo bar=0D=0D=0A"},  // test that CR is converted to CRLF, in binary CRLFs are hex encoded and in non-binary CRLFs remain untouched
		{in: "foo bar ", want: "foo bar=20"},                                       // test hex encoding of whitespace at the end of a line
		{in: "foo bar\t", want: "foo bar=09"},                                      // test hex encoding of whitespace at the end of a line
		{in: "foo bar  ", want: "foo bar =20"},                                     // test hex encoding of whitespace ONLY at the end of a line
		{in: "foo bar \n", want: "foo bar=20\r\n", wantB: "foo bar =0A"},           // test hex encoding of whitespace at the end of a line and conversion of LF to CRLF in non-binary, in binary - hex encoding of LF
		{in: "foo bar \r", want: "foo bar=20\r\n", wantB: "foo bar =0D"},           // test hex encoding of whitespace at the end of a line and conversion of CR to CRLF in non-binary, in binary - hex encoding of CR
		{in: "foo bar \r\n", want: "foo bar=20\r\n", wantB: "foo bar =0D=0A"},      // test hex encoding of whitespace at the end of a line in non-binary, in binary - hex encoding of CRLF
		{in: "foo bar  \n", want: "foo bar =20\r\n", wantB: "foo bar  =0A"},        // test hex encoding of whitespace ONLY at the end of a line and conversion of LF to CRLF in non-binary, in binary - hex encoding of LF
		{in: "foo bar  \n ", want: "foo bar =20\r\n=20", wantB: "foo bar  =0A=20"}, // test hex encoding of whitespace ONLY at the end of lines and conversion of LF to CRLF in non-binary, in binary - hex encoding of LF
		{in: "¡Hola Señor!", want: "=C2=A1Hola Se=C3=B1or!"},                       // test hex encoding of UTF-8 character set
		{
			in:   "\t !\"#$%&'()*+,-./ :;<>?@[\\]^_`{|}~",
			want: "\t !\"#$%&'()*+,-./ :;<>?@[\\]^_`{|}~", // test ASCII characters in allowed range untouched
		},
		{
			in:   strings.Repeat("a", 75),
			want: strings.Repeat("a", 75), // test ASCII character in allowed range untouched
		},
		{
			in:   strings.Repeat("a", 76), // test insertion of soft line break when line exceeds the 76 byte limit
			want: strings.Repeat("a", 75) + "=\r\na",
		},
		{
			in:   strings.Repeat("a", 72) + "=",
			want: strings.Repeat("a", 72) + "=3D", // test escape of '=', 3D in decimal is 61 which is the '=' ascii character, =3D is the encoded '='
		},
		{
			in:   strings.Repeat("a", 73) + "=",        // test escape of '=', 3D in decimal is 61 which is the '=' ascii character, =3D is the encoded '=' +
			want: strings.Repeat("a", 73) + "=\r\n=3D", // insertion  of soft line break because line exceeds the 76 byte limit with '=' hex encoded
		},
		{
			in:   strings.Repeat("a", 74) + "=",        // test escape of '=', 3D in decimal is 61 which is the '=' ascii character, =3D is the encoded '=' +
			want: strings.Repeat("a", 74) + "=\r\n=3D", // insertion  of soft line break because line exceeds the 76 byte limit with '=' hex encoded
		},
		{
			in:   strings.Repeat("a", 75) + "=",        // test escape of '=', 3D in decimal is 61 which is the '=' ascii character, =3D is the encoded '=' +
			want: strings.Repeat("a", 75) + "=\r\n=3D", // insertion  of soft line break because line exceeds the 76 byte limit with '=' hex encoded
		},
		{
			in:   strings.Repeat(" ", 73),
			want: strings.Repeat(" ", 72) + "=20", // test hex encoding of whitespace at the end of a line
		},
		{
			in:   strings.Repeat(" ", 74),              // test hex encoding of whitespace at the end of a line +
			want: strings.Repeat(" ", 73) + "=\r\n=20", // insertion of soft line break because line exceeds the 76 byte limit with last ' ' hex encoded
		},
		{
			in:   strings.Repeat(" ", 75),              // test hex encoding of whitespace at the end of a line +
			want: strings.Repeat(" ", 74) + "=\r\n=20", // insertion of soft line break because line exceeds the 76 byte limit with last ' ' hex encoded
		},
		{
			in:   strings.Repeat(" ", 76),              // test hex encoding of whitespace at the end of a line +
			want: strings.Repeat(" ", 75) + "=\r\n=20", // insertion of soft line break because line exceeds the 76 byte
		},
		{
			in:   strings.Repeat(" ", 77),               // test hex encoding of whitespace at the end of a line +
			want: strings.Repeat(" ", 75) + "=\r\n =20", // insertion of soft line break because line exceeds the 76 byte
		},
	}
	for _, tt := range tests {
		buf := new(strings.Builder)
		w := NewWriter(buf)

		want := tt.want
		if binary {
			w.Binary = true
			if tt.wantB != "" {
				want = tt.wantB
			}
		}

		if _, err := w.Write([]byte(tt.in)); err != nil {
			t.Errorf("Write(%q): %v", tt.in, err)
			continue
		}

		got := buf.String()
		if got != want {
			t.Errorf("Write(%q), got:\n%q\nwant:\n%q", tt.in, got, want)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	buf := new(bytes.Buffer)
	w := NewWriter(buf)
	if _, err := w.Write(testMsg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := NewReader(buf)
	gotBytes, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("Error while reading from Reader: %v", err)
	}

	got := string(gotBytes)
	if got != string(testMsg) {
		t.Errorf("Encoding and decoding changed the message, got:\n%s", got)
	}
}

// From https://fr.wikipedia.org/wiki/Quoted-Printable
var testMsg = []byte("Quoted-Printable (QP) est un format d'encodage de données codées sur 8 bits, qui utilise exclusivement les caractères alphanumériques imprimables du code ASCII (7 bits).\r\n" +
	"\r\n" +
	"En effet, les différents codages comprennent de nombreux caractères qui ne sont pas représentables en ASCII (par exemple les caractères accentués), ainsi que des caractères dits « non-imprimables ».\r\n" +
	"\r\n" +
	"L'encodage Quoted-Printable permet de remédier à ce problème, en procédant de la manière suivante :\r\n" +
	"\r\n" +
	"Un octet correspondant à un caractère imprimable de l'ASCII sauf le signe égal (donc un caractère de code ASCII entre 33 et 60 ou entre 62 et 126) ou aux caractères de saut de ligne (codes ASCII 13 et 10) ou une suite de tabulations et espaces non situées en fin de ligne (de codes ASCII respectifs 9 et 32) est représenté tel quel.\r\n" +
	"Un octet qui ne correspond pas à la définition ci-dessus (caractère non imprimable de l'ASCII, tabulation ou espaces non suivies d'un caractère imprimable avant la fin de la ligne ou signe égal) est représenté par un signe égal, suivi de son numéro, exprimé en hexadécimal.\r\n" +
	"Enfin, un signe égal suivi par un saut de ligne (donc la suite des trois caractères de codes ASCII 61, 13 et 10) peut être inséré n'importe où, afin de limiter la taille des lignes produites si nécessaire. Une limite de 76 caractères par ligne est généralement respectée.\r\n")

func BenchmarkWriter(b *testing.B) {
	for i := 0; i < b.N; i++ {
		w := NewWriter(io.Discard)
		w.Write(testMsg)
		w.Close()
	}
}
