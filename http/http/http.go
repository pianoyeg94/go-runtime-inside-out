package http

// omitBundledHTTP2 is set by omithttp2.go when the nethttpomithttp2
// build tag is set. That means h2_bundle.go isn't compiled in and we
// shouldn't try to use it.
var omitBunldedHTTP2 bool

// TODO(bradfitz): move common stuff here. The other files have accumulated
// generic http stuff in random places.

// TODO!!!!! https://github.com/teh-cmc/go-internals/tree/master/chapter2_interfaces
//
// contextKey is a value for use with context.WithValue. It's used as
// a pointer so it fits in an interface{} without allocation.
type contextKey struct {
	name string
}
