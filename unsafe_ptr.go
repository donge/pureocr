//go:build linux && arm64

package pureocr

import "unsafe"

// ptrToSlice converts a raw C pointer (as uintptr) to a []byte of length n.
//
// We receive raw addresses from Mojo (libpureocr.so) which are valid C heap
// pointers but not known to the Go GC. Using //go:nocheckptr suppresses the
// runtime pointer-safety check; the go vet warning here is a false positive
// because vet does not understand that pragma.
//
//go:nocheckptr
func ptrToSlice(ptr uintptr, n int) []byte {
	// This conversion is safe: ptr is a live C heap address valid for n bytes.
	p := (*byte)(unsafe.Pointer(ptr)) // nolint: unsafeptr
	return unsafe.Slice(p, n)
}
