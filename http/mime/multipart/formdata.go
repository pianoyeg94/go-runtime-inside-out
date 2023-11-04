package multipart

import (
	"math"
	"os"
)

func (r *Reader) readForm(maxMemory int64) (_ *Form, err error) {
	form := &Form{make(map[string][]string), make(map[string][]*FileHeader)}
	defer func() {
		if err != nil {
			form.RemoveAll()
		}
	}()

	// Reserve an additional 10 MB for non-file parts.
	maxValueBytes := maxMemory + int64(10<<20)
	if maxValueBytes <= 0 {
		if maxMemory < 0 { // if maxMemory was -10 MB or less
			maxValueBytes = 0
		} else { // if maxMemory was so big, that adding 10MB to it, resulted in int64 overflow
			maxValueBytes = math.MaxInt64
		}
	}

	for {

	}

	return nil, nil
}

// Form is a parsed multipart form.
// Its File parts are stored either in memory or on disk,
// and are accessible via the *FileHeader's Open method.
// Its Value parts are stored as strings.
// Both are keyed by field name.
// https://habr.com/ru/post/511114/
// https://ru.wikipedia.org/wiki/Multipart/form-data
// https://www.rfc-editor.org/rfc/rfc7578
// "percent-encoding": https://www.rfc-editor.org/rfc/rfc3986#section-2.1
type Form struct {
	Value map[string][]string
	File  map[string][]*FileHeader
}

func (f *Form) RemoveAll() error {
	var err error
	for _, fhs := range f.File {
		for _, fh := range fhs {
			if fh.tmpfile != "" {
				e := os.Remove(fh.tmpfile)
				if e != nil && err == nil {
					err = e
				}
			}
		}
	}

	return err
}

// A FileHeader describes a file part of a multipart request.
type FileHeader struct {
	Filename string
	Size     int64

	content []byte
	tmpfile string
}
